package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/logging"
	"mnemo-go/internal/model"
	previewserver "mnemo-go/internal/preview"
	"mnemo-go/internal/store"
)

// PlayVideo resolves playback sources and returns a streamable proxy URL for
// the HTML5 <video> element in the frontend.
func (a *App) PlayVideo(userID, driveID, fileID string) (*model.VideoPreview, error) {
	return a.playVideo(userID, driveID, fileID, "")
}

// PlayVideoQuality returns a streamable URL for a specific quality variant.
// An empty quality keeps the default origin-quality selection.
func (a *App) PlayVideoQuality(userID, driveID, fileID, quality string) (*model.VideoPreview, error) {
	return a.playVideo(userID, driveID, fileID, quality)
}

func (a *App) playVideo(userID, driveID, fileID, requestedQuality string) (*model.VideoPreview, error) {
	started := time.Now()
	logging.Info("video playback resolution started", "account_id", redactID(userID), "drive_id", redactID(driveID), "file_id", redactID(fileID), "requested_quality", requestedQuality)
	preview, streamURL, err := a.resolveVideoSource(userID, driveID, fileID, requestedQuality)
	if err != nil {
		logging.Warn("video playback resolution failed", "error", err, "duration", logging.Duration(started))
		return nil, err
	}
	preview.URL = streamURL
	logging.Info("video playback resolution completed", "quality", preview.CurrentQuality, "stream_type", preview.StreamType, "duration", logging.Duration(started))
	return preview, nil
}

func (a *App) resolveVideoSource(userID, driveID, fileID, requestedQuality string) (*model.VideoPreview, string, error) {
	preview, err := drive.GetVideoPreview(userID, driveID, fileID)
	if err != nil {
		return nil, "", err
	}
	if preview == nil || len(preview.Qualities) == 0 {
		return nil, "", errors.New("该文件无可用播放源")
	}
	if requestedQuality == "" {
		requestedQuality = preview.CurrentQuality
	}
	quality, err := chooseVideoQuality(preview.Qualities, requestedQuality)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(quality.URL) == "" {
		return nil, "", errors.New("该文件播放地址为空")
	}
	mediaProxy := a.previewServer()
	if mediaProxy == nil {
		return nil, "", errors.New("预览服务未启动")
	}
	filename := videoFileName(a, userID, driveID, fileID)
	streamType := videoStreamType(quality.Type, filename)
	selectedQuality := qualityIdentifier(quality)
	refresh := func(_ context.Context) (previewserver.PlaybackSource, error) {
		fresh, err := drive.GetVideoPreview(userID, driveID, fileID)
		if err != nil {
			return previewserver.PlaybackSource{}, err
		}
		if fresh == nil {
			return previewserver.PlaybackSource{}, errors.New("刷新后的播放信息为空")
		}
		freshQuality, err := chooseVideoQuality(fresh.Qualities, selectedQuality)
		if err != nil {
			return previewserver.PlaybackSource{}, err
		}
		if strings.TrimSpace(freshQuality.URL) == "" {
			return previewserver.PlaybackSource{}, errors.New("刷新后的播放地址为空")
		}
		return previewserver.PlaybackSource{
			URL:                 freshQuality.URL,
			Headers:             mergeHeaders(fresh.Headers, freshQuality.Headers),
			RequestAuth:         selectRequestAuth(fresh.RequestAuth, freshQuality.RequestAuth),
			AllowPrivateNetwork: fresh.AllowPrivateNetwork,
			Filename:            filename,
			StreamType:          videoStreamType(freshQuality.Type, filename),
			ExpiresAt:           sourceExpiration(fresh, freshQuality),
		}, nil
	}
	streamURL, err := mediaProxy.PlaybackURL(previewserver.PlaybackSource{
		URL:                 quality.URL,
		Headers:             mergeHeaders(preview.Headers, quality.Headers),
		RequestAuth:         selectRequestAuth(preview.RequestAuth, quality.RequestAuth),
		AllowPrivateNetwork: preview.AllowPrivateNetwork,
		Filename:            filename,
		StreamType:          streamType,
		ExpiresAt:           sourceExpiration(preview, quality),
		Refresh:             refresh,
	})
	if err != nil {
		return nil, "", err
	}
	preview.CurrentQuality = selectedQuality
	preview.StreamType = streamType
	a.proxyVideoSubtitles(mediaProxy, userID, driveID, fileID, filename, preview)
	sanitizeVideoPreview(preview)
	return preview, streamURL, nil
}

func qualityIdentifier(quality model.VideoQuality) string {
	if value := strings.TrimSpace(quality.Value); value != "" {
		return value
	}
	if value := strings.TrimSpace(quality.Quality); value != "" {
		return value
	}
	return strings.TrimSpace(quality.Label)
}

func videoStreamType(declared, filename string) string {
	declared = strings.ToLower(strings.TrimSpace(declared))
	switch declared {
	case "hls", "m3u8":
		return "hls"
	case "dash", "mpd":
		return "dash"
	case "mp4", "webm", "ts", "m2ts", "mts", "ogg", "mkv", "avi", "flv", "wmv", "rm", "rmvb", "mpeg":
		return declared
	}
	switch strings.TrimPrefix(strings.ToLower(path.Ext(filename)), ".") {
	case "m3u8":
		return "hls"
	case "mpd":
		return "dash"
	case "mp4", "m4v", "mov", "3gp":
		return "mp4"
	case "webm":
		return "webm"
	case "ogv", "ogg":
		return "ogg"
	case "ts", "m2ts", "mts":
		return "ts"
	case "mkv", "avi", "flv", "wmv", "rm", "rmvb", "mpg", "mpeg":
		return strings.TrimPrefix(strings.ToLower(path.Ext(filename)), ".")
	default:
		return ""
	}
}

func expirationTime(values ...int64) time.Time {
	var earliest time.Time
	for _, value := range values {
		if value <= 0 {
			continue
		}
		// Some providers expose seconds and others milliseconds. Normalize at
		// the app boundary so the local stream session has one representation.
		if value < 10_000_000_000 {
			value *= 1000
		}
		candidate := time.UnixMilli(value)
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return earliest
}

func sourceExpiration(preview *model.VideoPreview, quality model.VideoQuality) time.Time {
	previewExpiry := int64(0)
	if preview != nil {
		previewExpiry = preview.ExpireTime
	}
	return expirationTime(previewExpiry, quality.ExpireTime, driveutil.GetExpiresTime(quality.URL))
}

func chooseVideoQuality(qualities []model.VideoQuality, requested string) (model.VideoQuality, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for _, quality := range qualities {
			if strings.EqualFold(strings.TrimSpace(quality.Quality), requested) ||
				strings.EqualFold(strings.TrimSpace(quality.Value), requested) ||
				strings.EqualFold(strings.TrimSpace(quality.Label), requested) {
				return quality, nil
			}
		}
		return model.VideoQuality{}, fmt.Errorf("不支持的视频画质 %q", requested)
	}
	for _, quality := range qualities {
		if strings.EqualFold(quality.Quality, "origin") || strings.EqualFold(quality.Label, "原画") {
			return quality, nil
		}
	}
	return qualities[0], nil
}

func mergeHeaders(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func selectRequestAuth(base, override model.RequestAuthenticator) model.RequestAuthenticator {
	if override != nil {
		return override
	}
	return base
}

// proxyVideoSubtitles gives <track> URLs the same privacy and header handling
// as the main media stream. A provider may omit subtitles or return an
// unsupported one; neither condition should prevent the video itself playing.
func (a *App) proxyVideoSubtitles(mediaProxy *previewserver.Server, userID, driveID, fileID, videoName string, preview *model.VideoPreview) {
	if mediaProxy == nil || preview == nil || len(preview.Subtitles) == 0 {
		return
	}
	originals := append([]model.Subtitle(nil), preview.Subtitles...)
	proxied := make([]model.Subtitle, 0, len(originals))
	for index, subtitle := range originals {
		if strings.TrimSpace(subtitle.URL) == "" {
			continue
		}
		language := strings.TrimSpace(subtitle.Language)
		source := subtitlePlaybackSource(preview, subtitle, subtitleFileName(videoName, index, subtitle.URL))
		source.Refresh = func(_ context.Context) (previewserver.PlaybackSource, error) {
			fresh, err := drive.GetVideoPreview(userID, driveID, fileID)
			if err != nil {
				return previewserver.PlaybackSource{}, err
			}
			if fresh == nil {
				return previewserver.PlaybackSource{}, errors.New("刷新后的字幕信息为空")
			}
			freshSubtitle, err := chooseSubtitle(fresh.Subtitles, index, language)
			if err != nil {
				return previewserver.PlaybackSource{}, err
			}
			return subtitlePlaybackSource(fresh, freshSubtitle, subtitleFileName(videoName, index, freshSubtitle.URL)), nil
		}
		localURL, err := mediaProxy.PlaybackURL(source)
		if err != nil {
			continue
		}
		subtitle.URL = localURL
		subtitle.Headers = nil
		proxied = append(proxied, subtitle)
	}
	preview.Subtitles = proxied
}

func subtitlePlaybackSource(preview *model.VideoPreview, subtitle model.Subtitle, filename string) previewserver.PlaybackSource {
	return previewserver.PlaybackSource{
		URL:                 subtitle.URL,
		Headers:             mergeHeaders(preview.Headers, subtitle.Headers),
		AllowPrivateNetwork: preview.AllowPrivateNetwork,
		Filename:            filename,
		StreamType:          subtitleStreamType(subtitle.URL),
		ExpiresAt:           subtitleExpiration(preview, subtitle),
	}
}

func subtitleExpiration(preview *model.VideoPreview, subtitle model.Subtitle) time.Time {
	previewExpiry := int64(0)
	if preview != nil {
		previewExpiry = preview.ExpireTime
	}
	return expirationTime(previewExpiry, driveutil.GetExpiresTime(subtitle.URL))
}

func subtitleStreamType(rawURL string) string {
	ext := strings.ToLower(path.Ext(strings.SplitN(rawURL, "?", 2)[0]))
	if ext == ".srt" {
		return "subtitle-srt"
	}
	return "subtitle"
}

func subtitleFileName(videoName string, index int, rawURL string) string {
	base := strings.TrimSuffix(videoName, path.Ext(videoName))
	if strings.TrimSpace(base) == "" {
		base = "subtitle"
	}
	ext := path.Ext(strings.SplitN(rawURL, "?", 2)[0])
	if ext == "" {
		ext = ".vtt"
	}
	return fmt.Sprintf("%s.subtitle-%d%s", base, index+1, ext)
}

func chooseSubtitle(subtitles []model.Subtitle, preferredIndex int, language string) (model.Subtitle, error) {
	if preferredIndex >= 0 && preferredIndex < len(subtitles) && strings.TrimSpace(subtitles[preferredIndex].URL) != "" {
		return subtitles[preferredIndex], nil
	}
	for _, subtitle := range subtitles {
		if strings.EqualFold(strings.TrimSpace(subtitle.Language), language) && strings.TrimSpace(subtitle.URL) != "" {
			return subtitle, nil
		}
	}
	return model.Subtitle{}, errors.New("刷新后的字幕地址为空")
}

// sanitizeVideoPreview ensures the WebView only sees local opaque URLs. The
// provider response remains in the refresh closure in Go, while labels and
// quality identifiers stay available for the player UI.
func sanitizeVideoPreview(preview *model.VideoPreview) {
	if preview == nil {
		return
	}
	preview.Headers = nil
	for index := range preview.Qualities {
		preview.Qualities[index].HTML = ""
		preview.Qualities[index].URL = ""
		preview.Qualities[index].Headers = nil
	}
}

func videoFileName(a *App, userID, driveID, fileID string) string {
	if f, err := drive.GetFile(userID, driveID, fileID); err == nil && f != nil {
		return f.Name
	}
	return fileID
}

// SavePlayCursor persists playback position for resume.
func (a *App) SavePlayCursor(userID, driveID, fileID string, seconds float64) error {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return errors.New("播放位置无效")
	}
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	return st.SavePlayCursor(store.PlayCursor{
		UserID: userID, DriveID: driveID, FileID: fileID,
		Seconds: seconds, Updated: time.Now().Unix(),
	})
}

// GetPlayCursor returns the saved resume position.
func (a *App) GetPlayCursor(userID, driveID, fileID string) float64 {
	st, err := a.storeOrError()
	if err != nil {
		return 0
	}
	v, _ := st.GetPlayCursor(userID, driveID, fileID)
	return v
}
