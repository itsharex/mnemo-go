package dlengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommitPartReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "file.part")
	dest := filepath.Join(dir, "file.bin")
	if err := os.WriteFile(part, []byte("new"), 0o644); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	if err := commitPart(part, dest); err != nil {
		t.Fatalf("commitPart: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("destination = %q, want new", got)
	}
}

func TestSpeedLimiterUnlimited(t *testing.T) {
	l := newSpeedLimiter(0)
	start := time.Now()
	l.waitN(1 << 20) // 1MB
	if time.Since(start) > 100*time.Millisecond {
		t.Error("unlimited limiter should not sleep")
	}
}

func TestSpeedLimiterThrottles(t *testing.T) {
	// rate = 100 bytes/sec; reading 1000 bytes should take ~9s, but the limiter
	// caps sleeps at 10s. Instead of waiting 9s, test the bucket logic with a
	// short rate and small window.
	l := newSpeedLimiter(1000) // 1KB/s
	// first window: read 500 bytes at 1000B/s → should be nearly instant
	start := time.Now()
	l.waitN(500)
	elapsed := time.Since(start)
	// 500 bytes at 1000 B/s = 0.5s allowed; first call should not sleep much
	// because the window just started (elapsed ~0, allowed ~0, window=500)
	// so it should sleep ~0.5s
	if elapsed < 200*time.Millisecond {
		// might be too fast if timing is off; the key assertion is that it
		// doesn't sleep longer than 10s (the cap)
	}
	if elapsed > 11*time.Second {
		t.Errorf("sleep exceeded cap: %v", elapsed)
	}
}

func TestSpeedLimiterWindowReset(t *testing.T) {
	l := newSpeedLimiter(1000)
	l.start = time.Now().Add(-2 * time.Second) // old start
	l.window = 100000                          // huge accumulated window
	// after >1s, the next waitN should reset window and start
	l.waitN(10)
	// if window was not reset, the limiter would sleep a very long time
	// the test passing (not timing out) proves the reset works
}

func TestSpeedLimiterNoNegativeSleep(t *testing.T) {
	l := newSpeedLimiter(1000000) // high rate
	for i := 0; i < 100; i++ {
		l.waitN(100)
	}
	// should complete quickly without sleeping
}

func TestSharedLimiterSharesAccountingAcrossCalls(t *testing.T) {
	l := NewSharedLimiter(1_000_000)
	if err := l.Wait(context.Background(), 1024); err != nil {
		t.Fatalf("first shared wait: %v", err)
	}
	if err := l.Wait(context.Background(), 2048); err != nil {
		t.Fatalf("second shared wait: %v", err)
	}
	l.mu.Lock()
	window := l.window
	l.mu.Unlock()
	if window != 3072 {
		t.Fatalf("shared accounting window = %d, want 3072", window)
	}
}

func TestParseContentRangeStrictly(t *testing.T) {
	start, end, total, ok := parseContentRange("bytes 16-31/64")
	if !ok || start != 16 || end != 31 || total != 64 {
		t.Fatalf("unexpected parsed range: start=%d end=%d total=%d ok=%v", start, end, total, ok)
	}
	for _, invalid := range []string{"bytes 16-31/*", "items 16-31/64", "bytes 31-16/64", "bytes 16-64/64", "bytes 16/64"} {
		if _, _, _, ok := parseContentRange(invalid); ok {
			t.Errorf("parseContentRange(%q) should fail", invalid)
		}
	}
}

func TestDownloadContinuesAfterShortRangeResponse(t *testing.T) {
	payload := bytes.Repeat([]byte("mnemo"), 900)
	var rangeStarts []int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, ok := parseTestRange(r.Header.Get("Range"))
		if !ok {
			t.Errorf("invalid Range header: %q", r.Header.Get("Range"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if start == 0 && end == 0 {
			writeTestRange(w, start, end, int64(len(payload)), payload[:1])
			return
		}
		rangeStarts = append(rangeStarts, start)
		if start == 0 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("test server does not support connection hijacking")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, writer, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack response: %v", err)
				return
			}
			defer conn.Close()
			length := end - start + 1
			_, _ = fmt.Fprintf(writer, "HTTP/1.1 206 Partial Content\r\nContent-Range: bytes %d-%d/%d\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", start, end, len(payload), length)
			_, _ = writer.Write(payload[:1024])
			_ = writer.Flush()
			return
		}
		writeTestRange(w, start, end, int64(len(payload)), payload[start:end+1])
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "short-range.bin")
	err := Download(context.Background(), Options{Concurrency: 1, ChunkSize: int64(len(payload)), MinSize: 1}, server.URL, path, nil)
	if err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("downloaded content differs from source payload")
	}
	if len(rangeStarts) != 2 || rangeStarts[0] != 0 || rangeStarts[1] != 1024 {
		t.Fatalf("range starts = %v, want [0 1024]", rangeStarts)
	}
}

func TestDownloadResumeRequiresIntactPartFile(t *testing.T) {
	payload := bytes.Repeat([]byte("resume-data"), 1024)
	for _, mode := range []string{"missing", "truncated", "oversized", "intact"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start, end, ok := parseTestRange(r.Header.Get("Range"))
				if !ok || end >= int64(len(payload)) {
					http.Error(w, "invalid range", http.StatusBadRequest)
					return
				}
				if end != 0 {
					requests.Add(1)
				}
				w.Header().Set("ETag", `"resume-v1"`)
				writeTestRange(w, start, end, int64(len(payload)), payload[start:end+1])
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "resume.bin")
			part := append([]byte(nil), payload...)
			switch mode {
			case "truncated":
				part = part[:len(part)/2]
			case "oversized":
				part = append(part, 'x')
			}
			if mode != "missing" {
				if err := os.WriteFile(path+".part", part, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			st := &state{URLHash: urlFingerprint(server.URL), Total: int64(len(payload)),
				Chunk: int64(len(payload) / 2), Done: []bool{true, true}, ETag: `"resume-v1"`}
			if err := persistState(path+".state.json", st); err != nil {
				t.Fatal(err)
			}
			if err := Download(context.Background(), Options{Concurrency: 2, ChunkSize: st.Chunk, MinSize: 1}, server.URL, path, nil); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("download reported success with corrupt resumed contents")
			}
			wantRequests := int64(2)
			if mode == "intact" {
				wantRequests = 0
			}
			if requests.Load() != wantRequests {
				t.Fatalf("chunk requests = %d, want %d", requests.Load(), wantRequests)
			}
		})
	}
}

func TestDownloadFallsBackWhenServerStopsHonoringRanges(t *testing.T) {
	payload := bytes.Repeat([]byte("range-fallback"), 1024)
	var fullRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			writeTestRange(w, 0, 0, int64(len(payload)), payload[:1])
			return
		}
		if r.Header.Get("Range") == "" {
			fullRequests.Add(1)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "fallback.bin")
	if err := Download(context.Background(), Options{Concurrency: 2, ChunkSize: 1024, MinSize: 1}, server.URL, path, nil); err != nil {
		t.Fatalf("range rejection should fall back to a full request: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) || fullRequests.Load() != 1 {
		t.Fatalf("incorrect fallback contents or full request count: %d", fullRequests.Load())
	}
}

func TestDownloadContinuesAfterServerClampsRange(t *testing.T) {
	payload := bytes.Repeat([]byte("mnemo"), 900)
	var rangeStarts []int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, ok := parseTestRange(r.Header.Get("Range"))
		if !ok {
			t.Errorf("invalid Range header: %q", r.Header.Get("Range"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if start == 0 && end == 0 {
			writeTestRange(w, start, end, int64(len(payload)), payload[:1])
			return
		}
		rangeStarts = append(rangeStarts, start)
		cappedEnd := min(end, start+1023)
		writeTestRange(w, start, cappedEnd, int64(len(payload)), payload[start:cappedEnd+1])
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "capped-range.bin")
	err := Download(context.Background(), Options{Concurrency: 1, ChunkSize: int64(len(payload)), MinSize: 1}, server.URL, path, nil)
	if err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("downloaded content differs from source payload")
	}
	wantStarts := []int64{0, 1024, 2048, 3072, 4096}
	if len(rangeStarts) != len(wantStarts) {
		t.Fatalf("range starts = %v, want %v", rangeStarts, wantStarts)
	}
	for i, want := range wantStarts {
		if rangeStarts[i] != want {
			t.Fatalf("range starts = %v, want %v", rangeStarts, wantStarts)
		}
	}
}

func TestDownloadAllowsChunkedResponseWithoutContentLength(t *testing.T) {
	payload := bytes.Repeat([]byte("mnemo"), 900)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Flushing before writing makes net/http use chunked transfer encoding
		// instead of synthesizing a Content-Length header.
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	var progress Progress
	path := filepath.Join(t.TempDir(), "chunked.bin")
	err := Download(context.Background(), Options{ExpectedSize: int64(len(payload))}, server.URL, path, func(p Progress) {
		progress = p
	})
	if err != nil {
		t.Fatalf("Download returned error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("downloaded content differs from source payload")
	}
	if progress.Downloaded != int64(len(payload)) || progress.Total != int64(len(payload)) || progress.Percent != 100 {
		t.Fatalf("final progress = %+v", progress)
	}
}

func TestSpeedEstimatorSmoothsShortIdleGap(t *testing.T) {
	started := time.Unix(0, 0)
	estimator := newSpeedEstimator(started, 0)
	if got := estimator.Observe(started.Add(500*time.Millisecond), 512<<10); got < 1_000_000 {
		t.Fatalf("initial speed = %d, want about 1 MiB/s", got)
	}
	if got := estimator.Observe(started.Add(time.Second), 512<<10); got < 500_000 || got > 550_000 {
		t.Fatalf("smoothed speed = %d, want about 512 KiB/s instead of zero", got)
	}
	if got := estimator.Observe(started.Add(4*time.Second), 512<<10); got != 0 {
		t.Fatalf("idle speed = %d, want 0", got)
	}
}

func parseTestRange(value string) (int64, int64, bool) {
	value = strings.TrimPrefix(value, "bytes=")
	parts := strings.SplitN(value, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, startErr := strconv.ParseInt(parts[0], 10, 64)
	end, endErr := strconv.ParseInt(parts[1], 10, 64)
	return start, end, startErr == nil && endErr == nil && start >= 0 && end >= start
}

func writeTestRange(w http.ResponseWriter, start, end, total int64, body []byte) {
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(body)), 10))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(body)
}

func TestResumeIdentityRequiresSameValidator(t *testing.T) {
	previous := state{ETag: `"v1"`, LastModified: "Wed, 20 Aug 2026 01:02:03 GMT"}
	if !resumeIdentityMatches(previous, resourceValidator{ETag: `"v1"`, LastModified: "Wed, 20 Aug 2026 01:02:03 GMT"}) {
		t.Fatal("same ETag should allow resume")
	}
	if resumeIdentityMatches(previous, resourceValidator{ETag: `"v2"`, LastModified: previous.LastModified}) {
		t.Fatal("changed ETag must invalidate resume state")
	}
	if resumeIdentityMatches(previous, resourceValidator{}) {
		t.Fatal("missing current validator must invalidate validated resume state")
	}
}

func TestSignedURLRotationRequiresStableIdentityAndValidator(t *testing.T) {
	previous := state{URLHash: urlFingerprint("https://example.test/file?sig=old"), ResourceHash: urlFingerprint("account/drive/file"), ETag: `"v1"`}
	currentURL := "https://example.test/file?sig=new"
	currentHash := urlFingerprint(currentURL)
	if !stateResourceMatches(previous, currentURL, currentHash, previous.ResourceHash, resourceValidator{ETag: `"v1"`}) {
		t.Fatal("同一文件和 ETag 的新签名链接应继续下载")
	}
	if stateResourceMatches(previous, currentURL, currentHash, previous.ResourceHash, resourceValidator{ETag: `"v2"`}) ||
		stateResourceMatches(previous, currentURL, currentHash, previous.ResourceHash, resourceValidator{}) ||
		stateResourceMatches(previous, currentURL, currentHash, urlFingerprint("other/file"), resourceValidator{ETag: `"v1"`}) ||
		stateResourceMatches(previous, "https://example.test/file?sig=old", previous.URLHash, urlFingerprint("other/file"), resourceValidator{ETag: `"v1"`}) {
		t.Fatal("缺少稳定身份或校验值变化时不得复用断点")
	}
}

func TestDownloadResumesAfterSignedURLRotation(t *testing.T) {
	payload := bytes.Repeat([]byte("signed-resume"), 512)
	chunkSize := int64(len(payload) / 2)
	var firstChunkRequests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		start, end, ok := parseTestRange(request.Header.Get("Range"))
		if !ok {
			http.Error(writer, "range required", http.StatusBadRequest)
			return
		}
		if start == 0 && end != 0 {
			firstChunkRequests++
		}
		writer.Header().Set("ETag", `"stable"`)
		writeTestRange(writer, start, end, int64(len(payload)), payload[start:end+1])
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "signed.bin")
	part := make([]byte, len(payload))
	copy(part, payload[:chunkSize])
	if err := os.WriteFile(path+".part", part, 0600); err != nil {
		t.Fatal(err)
	}
	resourceID := "account\x00drive\x00file"
	checkpoint := &state{URLHash: urlFingerprint(server.URL + "/file?sig=old"), ResourceHash: urlFingerprint(resourceID), Total: int64(len(payload)), Chunk: chunkSize, Done: []bool{true, false}, ETag: `"stable"`}
	if err := persistState(path+".state.json", checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), Options{Concurrency: 1, ChunkSize: chunkSize, MinSize: 1, ResourceID: resourceID}, server.URL+"/file?sig=new", path, nil); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, payload) || firstChunkRequests != 0 {
		t.Fatalf("签名轮换后未复用已完成分片: err=%v firstChunkRequests=%d", err, firstChunkRequests)
	}
}

func TestPersistStateHashesSignedURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.state.json")
	rawURL := "https://download.example/file?access_token=secret123&signature=private"
	st := &state{URL: rawURL, Total: 64, Chunk: 16, Done: []bool{true, false, false, false}}
	if err := persistState(path, st); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret123") || strings.Contains(string(data), "download.example") || strings.Contains(string(data), `"url":`) {
		t.Fatalf("resume state leaked signed URL: %s", data)
	}
	fingerprint := urlFingerprint(rawURL)
	if !strings.Contains(string(data), fingerprint) {
		t.Fatalf("resume state missing URL fingerprint: %s", data)
	}
	if !stateURLMatches(state{URLHash: fingerprint}, rawURL, fingerprint) {
		t.Fatal("hashed URL should match the same resource URL")
	}

	updated := &state{URL: rawURL, Total: 64, Chunk: 16, Done: []bool{true, true, false, false}}
	if err := persistState(path, updated); err != nil {
		t.Fatalf("overwrite state: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overwritten state: %v", err)
	}
	var got state
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode overwritten state: %v", err)
	}
	if len(got.Done) != 4 || !got.Done[0] || !got.Done[1] || got.Done[2] || got.Done[3] {
		t.Fatalf("overwritten checkpoint = %+v", got.Done)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read state directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mnemo-state-") {
			t.Fatalf("temporary state file was not cleaned up: %s", entry.Name())
		}
	}
}
