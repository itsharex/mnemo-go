package lanzou

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const (
	lanzouMiB               int64 = 1024 * 1024
	lanzouTierV0                  = "v0"
	lanzouTierV1                  = "v1"
	lanzouTierV2                  = "v2"
	lanzouTierV3                  = "v3"
	lanzouDefaultUploadTier       = lanzouTierV0
)

var lanzouAllowedUploadExtensions = map[string]struct{}{
	"doc": {}, "docx": {}, "zip": {}, "rar": {}, "apk": {}, "txt": {}, "exe": {}, "7z": {}, "e": {}, "z": {}, "ct": {}, "ke": {}, "cetrainer": {}, "db": {}, "tar": {}, "pdf": {}, "w3x": {},
	"epub": {}, "mobi": {}, "azw": {}, "azw3": {}, "osk": {}, "osz": {}, "xpa": {}, "cpk": {}, "lua": {}, "jar": {}, "dmg": {}, "ppt": {}, "pptx": {}, "xls": {}, "xlsx": {}, "mp3": {},
	"ipa": {}, "iso": {}, "img": {}, "gho": {}, "ttf": {}, "ttc": {}, "txf": {}, "dwg": {}, "bat": {}, "imazingapp": {}, "dll": {}, "crx": {}, "xapk": {}, "conf": {},
	"deb": {}, "rp": {}, "rpm": {}, "rplib": {}, "mobileconfig": {}, "appimage": {}, "lolgezi": {}, "flac": {},
	"cad": {}, "hwt": {}, "accdb": {}, "ce": {}, "xmind": {}, "enc": {}, "bds": {}, "bdi": {}, "ssf": {}, "it": {},
	"pkg": {}, "cfg": {}, "mp4": {}, "avi": {}, "png": {}, "jpeg": {}, "jpg": {}, "gif": {}, "webp": {}, "brushset": {},
}

func normalizeLanzouUploadTier(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case lanzouTierV1:
		return lanzouTierV1
	case lanzouTierV2:
		return lanzouTierV2
	case lanzouTierV3:
		return lanzouTierV3
	default:
		return lanzouDefaultUploadTier
	}
}

func lanzouUploadLimit(tier string) int64 {
	switch normalizeLanzouUploadTier(tier) {
	case lanzouTierV1:
		return 200 * lanzouMiB
	case lanzouTierV2:
		return 300 * lanzouMiB
	case lanzouTierV3:
		return 550 * lanzouMiB
	default:
		return 100 * lanzouMiB
	}
}

func lanzouUploadTier(c drive.Context) string {
	if c.Token != nil {
		if cr := parseLanzouCred(c.Token.RefreshToken); cr != nil {
			return normalizeLanzouUploadTier(cr.UploadTier)
		}
	}
	return lanzouDefaultUploadTier
}

// ValidateUpload enforces the member-grade limits documented for the ordinary
// 蓝奏云 account. It is used before enqueueing and again immediately before
// the provider uploads a local file.
func (d *Driver) ValidateUpload(_ context.Context, c drive.Context, name string, size int64) error {
	if size < 0 {
		return errors.New("蓝奏云文件大小无效")
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(name))), ".")
	if _, ok := lanzouAllowedUploadExtensions[ext]; !ok {
		if ext == "" {
			return errors.New("蓝奏云仅支持指定文件格式")
		}
		return fmt.Errorf("蓝奏云不支持 .%s 格式", ext)
	}
	tier := lanzouUploadTier(c)
	limit := lanzouUploadLimit(tier)
	if size > limit {
		return fmt.Errorf("蓝奏云 %s 单文件最大 %d MB", strings.ToUpper(tier), limit/lanzouMiB)
	}
	return nil
}

// UploadOneFile uploads a whole file via html5up.php. The endpoint does not
// support chunked upload, so the multipart payload is staged in a temporary
// file: retries stay seekable without retaining an entire upload in memory.
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil {
		return errors.New("蓝奏: 无效的上传任务")
	}
	cookie, _, _, baseURL := sessionOf(c)
	if cookie == "" {
		return errors.New("未登录")
	}
	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	size := stat.Size()
	if err := d.ValidateUpload(ctx, c, ui.Info.Name, size); err != nil {
		return err
	}
	ui.Info.Size = size
	ui.Info.SizeStr = model.FormatBytes(size)
	folderID := ToLanzouFolderId(ui.Info.ParentFileID)

	ui.Upload.DownState = "uploading"
	ui.Upload.IsDowning = true

	body, contentType, contentLength, err := buildLanzouUploadBody(f, ui.Info.Name, folderID)
	if err != nil {
		return err
	}
	defer func() {
		path := body.Name()
		_ = body.Close()
		_ = os.Remove(path)
	}()

	j, err := uploadLanzouRaw(ctx, cookie, baseURL, contentType, body, contentLength)
	if err != nil {
		return err
	}
	zt := numOf(j["zt"])
	if zt == 9 {
		newCookie, _, _, reloginErr := d.reloginAccount(ctx, c, baseURL)
		if reloginErr != nil {
			return reloginErr
		}
		j, err = uploadLanzouRaw(ctx, newCookie, baseURL, contentType, body, contentLength)
		if err != nil {
			return err
		}
		zt = numOf(j["zt"])
	}
	if zt != 1 && zt != 2 && zt != 4 {
		msg := strOf(j["info"])
		if msg == "" {
			msg = strOf(j["inf"])
		}
		if msg == "" {
			msg = "上传失败"
		}
		return errors.New(msg)
	}
	ui.ReportUploadProgress(size, size)
	ui.Upload.IsDowning = false
	ui.Upload.IsCompleted = true
	ui.Upload.DownState = "completed"
	return nil
}

func buildLanzouUploadBody(source *os.File, name, folderID string) (*os.File, string, int64, error) {
	body, err := os.CreateTemp("", "mnemo-lanzou-upload-*")
	if err != nil {
		return nil, "", 0, err
	}
	cleanup := func(err error) (*os.File, string, int64, error) {
		path := body.Name()
		_ = body.Close()
		_ = os.Remove(path)
		return nil, "", 0, err
	}

	mw := multipart.NewWriter(body)
	for key, value := range map[string]string{
		"task":           "1",
		"vie":            "2",
		"ve":             "2",
		"id":             "WU_FILE_0",
		"name":           name,
		"folder_id_bb_n": folderID,
	} {
		if err := mw.WriteField(key, value); err != nil {
			return cleanup(err)
		}
	}
	part, err := mw.CreateFormFile("upload_file", name)
	if err != nil {
		return cleanup(err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return cleanup(err)
	}
	if _, err := io.Copy(part, source); err != nil {
		return cleanup(err)
	}
	if err := mw.Close(); err != nil {
		return cleanup(err)
	}
	info, err := body.Stat()
	if err != nil {
		return cleanup(err)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return cleanup(err)
	}
	return body, mw.FormDataContentType(), info.Size(), nil
}

func uploadLanzouRaw(ctx context.Context, cookie, baseURL, contentType string, body io.ReadSeeker, contentLength int64) (map[string]any, error) {
	rawURL := strings.TrimSuffix(baseURL, "/") + "/html5up.php"
	mergedCookie := cookie
	var res *fetchResult
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		fetchThrottle.wait()
		// http.Client closes request bodies after each attempt. Keep the staged
		// file open so a CAPTCHA retry can seek it back to the beginning.
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, io.NopCloser(body))
		if err != nil {
			return nil, err
		}
		req.ContentLength = contentLength
		req.Header.Set("referer", "https://pc.woozooo.com")
		req.Header.Set("user-agent", LANZOU_DEFAULT.UserAgent)
		req.Header.Set("content-type", contentType)
		if mergedCookie != "" {
			req.Header.Set("cookie", mergedCookie)
		}
		resp, err := netx.DoUpload(httpClient, req)
		if err != nil {
			return nil, err
		}
		text, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		res = &fetchResult{status: resp.StatusCode, headers: resp.Header, location: resp.Header.Get("Location"), text: string(text)}
		if acw := solveAcwScV2(res.text); acw != "" {
			mergedCookie = mergeAcwCookie(mergedCookie, acw)
			continue
		}
		break
	}
	if res == nil {
		return nil, errors.New("上传失败: 未收到响应")
	}
	if res.status >= 400 {
		return nil, fmt.Errorf("上传失败 HTTP %d", res.status)
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(res.text), &j); err != nil {
		return nil, fmt.Errorf("上传失败: %s", truncate(res.text, 120))
	}
	return j, nil
}
