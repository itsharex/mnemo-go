package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const tsPacketSize = 188
const tsProbeSize = 512 * 1024

type tsPoint struct {
	offset  int64
	seconds float64
}

// tsTimestampPoints reads video PES presentation timestamps following a PAT.
// Starting at the PAT lets the demuxer recover its program map after a seek.
func tsTimestampPoints(data []byte, base int64) []tsPoint {
	var points []tsPoint
	pat := int64(-1)
	for i := 0; i+tsPacketSize <= len(data); i++ {
		if data[i] != 0x47 || i+tsPacketSize < len(data) && data[i+tsPacketSize] != 0x47 {
			continue
		}
		p := data[i : i+tsPacketSize]
		pid := int(p[1]&31)<<8 | int(p[2])
		if pid == 0 {
			pat = base + int64(i)
		}
		at := 4
		if p[3]&0x20 != 0 {
			at += 1 + int(p[4])
		}
		if p[3]&0x10 != 0 && p[1]&0x40 != 0 && at+14 <= len(p) && pat >= 0 {
			pes := p[at:]
			if pes[0] == 0 && pes[1] == 0 && pes[2] == 1 && pes[3] >= 0xe0 && pes[3] <= 0xef && pes[7]&0x80 != 0 {
				b := pes[9:14]
				pts := int64(b[0]&14)<<29 | int64(b[1])<<22 | int64(b[2]&254)<<14 | int64(b[3])<<7 | int64(b[4]>>1)
				points = append(points, tsPoint{pat, float64(pts) / 90000})
				pat = -1
			}
		}
		i += tsPacketSize - 1
	}
	return points
}

func (s *Server) tsProbe(ctx context.Context, source PlaybackSource, offset int64) ([]tsPoint, int64, error) {
	resp, err := s.doProxyRequest(ctx, http.MethodGet, source.URL, source.Headers, source.RequestAuth, fmt.Sprintf("bytes=%d-%d", offset, offset+tsProbeSize-1))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, 0, fmt.Errorf("TS seek requires Range support (HTTP %d)", resp.StatusCode)
	}
	var start, end, total int64
	if _, err = fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil || start != offset || total <= 0 {
		return nil, 0, errors.New("invalid TS byte range")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, tsProbeSize))
	if err != nil {
		return nil, 0, err
	}
	points := tsTimestampPoints(data, offset)
	if len(points) == 0 {
		return nil, 0, errors.New("TS stream has no usable video timestamp in probe")
	}
	return points, total, nil
}

func (s *Server) handleTSSeek(w http.ResponseWriter, r *http.Request, session *playbackSession) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	seconds, err := strconv.ParseFloat(r.URL.Query().Get("seek"), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		http.Error(w, "invalid seek time", 400)
		return
	}
	source, err := session.resolve(r.Context(), false)
	if err != nil || !isTSStream(source.StreamType) {
		http.Error(w, "stream does not support TS seek", 400)
		return
	}
	head, total, err := s.tsProbe(r.Context(), source, 0)
	if err != nil {
		http.Error(w, "无法读取视频索引，请重试", 502)
		return
	}
	origin := head[0].seconds
	target := origin + math.Max(0, seconds-2) // decode a short lead-in before the target
	low := head[0]
	best := low
	highOffset := total - tsProbeSize
	if highOffset < 0 {
		highOffset = 0
	}
	tail, _, err := s.tsProbe(r.Context(), source, highOffset)
	if err != nil {
		http.Error(w, "无法读取视频末尾索引，请重试", 502)
		return
	}
	high := tail[len(tail)-1]
	if target > high.seconds {
		target = math.Max(origin, high.seconds-2)
	}
	for _, p := range head {
		if p.seconds <= target && p.seconds >= best.seconds {
			best = p
		}
	}
	for attempt := 0; attempt < 14 && high.offset > low.offset && high.seconds > low.seconds; attempt++ {
		if target-best.seconds <= 2 {
			break
		}
		ratio := (target - low.seconds) / (high.seconds - low.seconds)
		ratio = math.Max(0.05, math.Min(0.95, ratio))
		offset := low.offset + int64(float64(high.offset-low.offset)*ratio) - tsProbeSize/2
		offset = offset / int64(tsPacketSize) * int64(tsPacketSize)
		if offset <= low.offset {
			offset = low.offset + tsPacketSize
		}
		if offset >= high.offset {
			break
		}
		points, _, probeErr := s.tsProbe(r.Context(), source, offset)
		if probeErr != nil {
			http.Error(w, "视频定位失败，请重试", 502)
			return
		}
		before := false
		for _, p := range points {
			if p.seconds <= target {
				before = true
				if p.seconds > best.seconds {
					best = p
				}
				if p.seconds > low.seconds {
					low = p
				}
			} else if p.seconds < high.seconds {
				high = p
			}
		}
		if !before && points[0].offset <= low.offset {
			break
		}
	}
	if target-best.seconds > 15 {
		http.Error(w, "无法精确定位视频，请重试", 502)
		return
	}
	u := *r.URL
	q := u.Query()
	q.Del("seek")
	q.Set("offset", strconv.FormatInt(best.offset, 10))
	u.RawQuery = q.Encode()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"url": u.String(), "start": math.Max(0, best.seconds-origin)})
}

func isTSStream(kind string) bool {
	return kind == "ts" || kind == "mpegts" || kind == "m2ts" || kind == "mts"
}

func tsOffsetRange(r *http.Request) (string, error) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		return "", errors.New("invalid TS offset")
	}
	start, end := int64(0), int64(-1)
	if raw := r.Header.Get("Range"); raw != "" {
		if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
			return "", errors.New("invalid TS range")
		}
		parts := strings.SplitN(strings.TrimPrefix(raw, "bytes="), "-", 2)
		if len(parts) != 2 {
			return "", errors.New("invalid TS range")
		}
		start, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || start < 0 {
			return "", errors.New("invalid TS range")
		}
		if parts[1] != "" {
			end, err = strconv.ParseInt(parts[1], 10, 64)
			if err != nil || end < start {
				return "", errors.New("invalid TS range")
			}
		}
	}
	if offset > math.MaxInt64-start || end >= 0 && offset > math.MaxInt64-end {
		return "", errors.New("TS range overflow")
	}
	if end >= 0 {
		return fmt.Sprintf("bytes=%d-%d", offset+start, offset+end), nil
	}
	return fmt.Sprintf("bytes=%d-", offset+start), nil
}
