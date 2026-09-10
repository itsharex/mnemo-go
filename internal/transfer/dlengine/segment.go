// Package dlengine implements a native multi-connection segmented downloader
// (aria2-style). It replaces the external aria2c.exe dependency: files are
// fetched with parallel HTTP Range requests and assembled at offsets, with
// resume support, per-chunk retry, global speed limiting and progress events.
package dlengine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mnemo-go/internal/netx"

	"golang.org/x/sync/errgroup"
)

// Defaults mirroring sane aria2 behaviour.
const (
	DefaultConcurrency = 8
	DefaultChunkSize   = 16 << 20 // 16 MiB per chunk
	DefaultMinSize     = 32 << 20 // below this: single stream
	MaxRetriesPerChunk = 5
	progressInterval   = 500 * time.Millisecond
	speedSampleWindow  = 2 * time.Second
	// MaxPartialRangeRequests prevents a broken server that returns only a
	// handful of bytes per response from causing an unbounded request loop.
	MaxPartialRangeRequests = 64
)

// Options tunes the download behaviour.
type Options struct {
	Concurrency int   // parallel connections
	ChunkSize   int64 // range chunk size
	MinSize     int64 // files smaller than this use a single stream
	MaxSpeed    int64 // global speed cap in bytes/s (0 = unlimited)
	// ExpectedSize is optional provider metadata. It is used only when the
	// download server omits Content-Length, so a chunked response can still
	// report progress and be verified after a single-stream download.
	ExpectedSize int64
	Limiter      RateLimiter
	Headers      map[string]string
	UserAgent    string
	// RequestAuth runs after static headers are applied, once per outbound
	// request. It is used for request-bound schemes such as HTTP Digest.
	RequestAuth func(*http.Request) error
}

// RateLimiter is shared by concurrent downloads so MaxDownloadSpeed is a
// process-wide cap rather than an independent bucket per task.
type RateLimiter interface {
	Wait(ctx context.Context, n int64) error
}

// Normalize fills zero fields with defaults.
func (o *Options) Normalize() {
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.ChunkSize <= 0 {
		o.ChunkSize = DefaultChunkSize
	}
	if o.MinSize <= 0 {
		o.MinSize = DefaultMinSize
	}
	if o.UserAgent == "" {
		o.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Mnemo/1.0"
	}
}

// Progress is a periodic snapshot.
type Progress struct {
	Downloaded int64 `json:"downloaded"`
	Total      int64 `json:"total"`
	Speed      int64 `json:"speed"` // bytes/s
	Percent    int   `json:"percent"`
}

// state is the persisted resume state.
type state struct {
	URL          string `json:"url,omitempty"` // legacy read-only; never written
	URLHash      string `json:"url_hash,omitempty"`
	Total        int64  `json:"total"`
	Chunk        int64  `json:"chunk"`
	Done         []bool `json:"done"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

type resourceValidator struct {
	ETag         string
	LastModified string
}

var (
	errResourceChanged      = errors.New("dlengine: remote resource changed")
	errInvalidRangeResponse = errors.New("dlengine: invalid range response")
	errShortRangeResponse   = errors.New("dlengine: server returned a shorter valid range")
	errRangeIgnored         = errors.New("dlengine: server ignored range request")
)

// partialRangeReadError reports bytes that were safely written before the
// remote side ended a valid range response. The caller can continue from the
// next byte instead of discarding the whole chunk and downloading it again.
type partialRangeReadError struct {
	err     error
	written int64
}

func (e *partialRangeReadError) Error() string {
	return fmt.Sprintf("dlengine: partial range read after %d bytes: %v", e.written, e.err)
}

func (e *partialRangeReadError) Unwrap() error { return e.err }

// speedEstimator reports a short rolling-window average. Download sources can
// deliver bytes in bursts even while a request remains healthy; a rolling
// window avoids advertising a false zero-speed pause between adjacent bursts.
type speedEstimator struct {
	samples []speedSample
}

type speedSample struct {
	at         time.Time
	downloaded int64
}

func newSpeedEstimator(now time.Time, downloaded int64) *speedEstimator {
	return &speedEstimator{samples: []speedSample{{at: now, downloaded: downloaded}}}
}

func (s *speedEstimator) Observe(now time.Time, downloaded int64) int64 {
	if s == nil {
		return 0
	}
	if len(s.samples) == 0 {
		s.samples = append(s.samples, speedSample{at: now, downloaded: downloaded})
		return 0
	}
	if downloaded < s.samples[len(s.samples)-1].downloaded {
		// A failed chunk can roll back in-flight accounting. Start a fresh
		// sampling window rather than emitting a negative speed.
		s.samples = []speedSample{{at: now, downloaded: downloaded}}
		return 0
	}
	s.samples = append(s.samples, speedSample{at: now, downloaded: downloaded})

	cutoff := now.Add(-speedSampleWindow)
	baseline := 0
	for baseline+1 < len(s.samples) && !s.samples[baseline+1].at.After(cutoff) {
		baseline++
	}
	if baseline > 0 {
		s.samples = append([]speedSample(nil), s.samples[baseline:]...)
	}
	first := s.samples[0]
	elapsed := now.Sub(first.at)
	if elapsed <= 0 || downloaded <= first.downloaded {
		return 0
	}
	return int64(float64(downloaded-first.downloaded) / elapsed.Seconds())
}

// Download fetches url to localPath (plus a .part temp file) using segmented
// parallel range requests. It resumes from the .part file when present.
func Download(ctx context.Context, opts Options, url, localPath string, onProgress func(Progress)) error {
	opts.Normalize()
	partPath := localPath + ".part"
	statePath := localPath + ".state.json"

	// 不能用 http.Client.Timeout（它是含 body 读取的总超时，大文件必超时）。
	// 只限制连接/响应头阶段，body 读取不设总时限（对齐 aria2 的空闲超时语义）。
	hc := &http.Client{
		Transport: &http.Transport{
			Proxy: proxyFunc(),
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 60 * time.Second,
			TLSHandshakeTimeout:   30 * time.Second,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			// A custom DialContext otherwise makes net/http conservatively
			// disable HTTP/2. Let CDN endpoints use HTTP/2 when available while
			// retaining HTTP/1.1 fallback for every existing provider.
			ForceAttemptHTTP2: true,
		},
	}
	defer hc.CloseIdleConnections()

	// Resolve size + range support.
	total, acceptRanges, validator, err := probe(ctx, hc, opts, url)
	if err != nil {
		return err
	}
	if total <= 0 && opts.ExpectedSize > 0 {
		total = opts.ExpectedSize
	}

	urlHash := urlFingerprint(url)
	st := &state{URLHash: urlHash, Total: total, Chunk: opts.ChunkSize, ETag: validator.ETag, LastModified: validator.LastModified}
	if total == 0 || !acceptRanges || total < opts.MinSize {
		// single stream
		return singleStream(ctx, hc, opts, url, localPath, total, validator, onProgress)
	}

	// Completed chunks only exist in the accompanying preallocated part file.
	// Validate it before ensureFile can recreate/extend it with zeroes; otherwise
	// a stale checkpoint can turn missing bytes into a successful corrupt file.
	partInfo, partErr := os.Stat(partPath)
	if partErr != nil && !errors.Is(partErr, os.ErrNotExist) {
		return partErr
	}
	partIntact := partErr == nil && partInfo.Mode().IsRegular() && partInfo.Size() == total
	// Resume from state only when both the remote identity and local data match.
	if b, err := os.ReadFile(statePath); err == nil {
		var prev state
		if partIntact && json.Unmarshal(b, &prev) == nil && stateURLMatches(prev, url, urlHash) && prev.Total == total && prev.Chunk == opts.ChunkSize &&
			resumeIdentityMatches(prev, validator) {
			st = &prev
		}
	}
	numChunks := int((total + opts.ChunkSize - 1) / opts.ChunkSize)
	if len(st.Done) != numChunks {
		st.Done = make([]bool, numChunks)
	}
	st.ETag = validator.ETag
	st.LastModified = validator.LastModified
	st.URL = ""
	st.URLHash = urlHash

	// Ensure part file exists at full size (sparse).
	if err := ensureFile(partPath, total); err != nil {
		return err
	}
	f, err := os.OpenFile(partPath, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	var limiter RateLimiter = opts.Limiter
	if limiter == nil {
		limiter = newSpeedLimiter(opts.MaxSpeed)
	}
	var downloaded atomic.Int64
	// account already-done bytes
	for i, done := range st.Done {
		if done {
			downloaded.Add(chunkLen(st, i))
		}
	}

	sem := make(chan struct{}, opts.Concurrency)
	g, gctx := errgroup.WithContext(ctx)
	var mu sync.Mutex // guards st.Done writes

	for i := 0; i < numChunks; i++ {
		if st.Done[i] {
			continue
		}
		chunkIdx := i
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-gctx.Done():
				return gctx.Err()
			}
			start := int64(chunkIdx) * opts.ChunkSize
			length := chunkLen(st, chunkIdx)
			remainingStart := start
			remainingLength := length
			var lastErr error
			partialRequests := 0
			for attempt := 0; attempt < MaxRetriesPerChunk; {
				err := fetchRange(gctx, hc, opts, url, remainingStart, remainingLength, total, validator, f, limiter, func(n int64) {
					downloaded.Add(n)
				})
				if err == nil {
					mu.Lock()
					st.Done[chunkIdx] = true
					// Persist while holding the same lock that protects st.Done;
					// otherwise parallel chunks can overwrite each other's state.
					_ = persistState(statePath, st)
					mu.Unlock()
					return nil
				}
				lastErr = err
				if errors.Is(err, errResourceChanged) || errors.Is(err, errInvalidRangeResponse) || errors.Is(err, errRangeIgnored) {
					if resumed := remainingStart - start; resumed > 0 {
						downloaded.Add(-resumed)
					}
					return fmt.Errorf("dlengine: chunk %d rejected: %w", chunkIdx, err)
				}
				if gctx.Err() != nil {
					if resumed := remainingStart - start; resumed > 0 {
						downloaded.Add(-resumed)
					}
					return gctx.Err()
				}
				var partial *partialRangeReadError
				if errors.As(err, &partial) && partial.written > 0 && partial.written < remainingLength {
					remainingStart += partial.written
					remainingLength -= partial.written
					partialRequests++
					if partialRequests <= MaxPartialRangeRequests {
						// This is a continuation, not a blind retry: the next request
						// starts exactly after the already persisted bytes.
						continue
					}
				}
				attempt++
				if attempt >= MaxRetriesPerChunk {
					break
				}
				select {
				case <-time.After(time.Duration(500*attempt) * time.Millisecond):
				case <-gctx.Done():
					if resumed := remainingStart - start; resumed > 0 {
						downloaded.Add(-resumed)
					}
					return gctx.Err()
				}
			}
			if resumed := remainingStart - start; resumed > 0 {
				downloaded.Add(-resumed)
			}
			return fmt.Errorf("dlengine: chunk %d failed after %d attempts: %w", chunkIdx, MaxRetriesPerChunk, lastErr)
		})
	}

	// progress ticker
	done := make(chan struct{})
	progressDone := make(chan struct{})
	speed := newSpeedEstimator(time.Now(), downloaded.Load())
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				cur := downloaded.Load()
				if onProgress != nil {
					onProgress(Progress{Downloaded: cur, Total: total, Speed: speed.Observe(time.Now(), cur), Percent: percent(cur, total)})
				}
			}
		}
	}()

	err = g.Wait()
	close(done)
	<-progressDone
	if errors.Is(err, errRangeIgnored) && ctx.Err() == nil {
		// No ranged body was accepted from the rejecting request. All workers
		// are now stopped; restart as a full stream rather than retrying an
		// unsupported range forever. If-Range identity failures stay errors.
		if closeErr := f.Close(); closeErr != nil {
			return closeErr
		}
		// singleStream truncates the part file. Its old chunk bitmap must not
		// survive an interrupted fallback and be reused on a later attempt.
		if removeErr := os.Remove(statePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		return singleStream(ctx, hc, opts, url, localPath, total, validator, onProgress)
	}
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	_ = f.Close()

	// sanity check
	if downloaded.Load() != total {
		return fmt.Errorf("dlengine: downloaded %d != total %d", downloaded.Load(), total)
	}
	// commit: rename part -> final
	if err := commitPart(partPath, localPath); err != nil {
		return err
	}
	_ = os.Remove(statePath)
	if onProgress != nil {
		onProgress(Progress{Downloaded: total, Total: total, Speed: 0, Percent: 100})
	}
	return nil
}

// probe determines the file size, range support and resource validators.
func probe(ctx context.Context, hc *http.Client, opts Options, url string) (int64, bool, resourceValidator, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false, resourceValidator{}, err
	}
	if err := setRequestHeaders(req, opts); err != nil {
		return 0, false, resourceValidator{}, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, false, resourceValidator{}, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	// A server advertising Accept-Ranges can still ignore a particular probe.
	// Only a real 206 response proves that segmented requests are usable.
	acceptRanges := resp.StatusCode == http.StatusPartialContent
	validator := resourceValidator{
		ETag:         strings.TrimSpace(resp.Header.Get("ETag")),
		LastModified: strings.TrimSpace(resp.Header.Get("Last-Modified")),
	}
	var total int64
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		start, end, parsedTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != 0 || end != 0 {
			return 0, false, resourceValidator{}, fmt.Errorf("%w: invalid probe Content-Range", errInvalidRangeResponse)
		}
		if resp.ContentLength >= 0 && resp.ContentLength != 1 {
			return 0, false, resourceValidator{}, fmt.Errorf("%w: probe Content-Length is %d, want 1", errInvalidRangeResponse, resp.ContentLength)
		}
		total = parsedTotal
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		total = resp.ContentLength
	default:
		return 0, false, resourceValidator{}, fmt.Errorf("dlengine: probe http %d", resp.StatusCode)
	}
	if total <= 0 {
		// Chunked CDN responses legitimately omit Content-Length. They cannot
		// be resumed or split safely, but a regular single-stream download is
		// still valid. The caller may supply ExpectedSize for progress display.
		return 0, false, validator, nil
	}
	return total, acceptRanges, validator, nil
}

func parseContentRange(value string) (start, end, total int64, ok bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bytes") {
		return 0, 0, 0, false
	}
	rangeAndTotal := strings.SplitN(fields[1], "/", 2)
	if len(rangeAndTotal) != 2 || rangeAndTotal[1] == "*" {
		return 0, 0, 0, false
	}
	bounds := strings.SplitN(rangeAndTotal[0], "-", 2)
	if len(bounds) != 2 {
		return 0, 0, 0, false
	}
	start, errStart := strconv.ParseInt(bounds[0], 10, 64)
	end, errEnd := strconv.ParseInt(bounds[1], 10, 64)
	total, errTotal := strconv.ParseInt(rangeAndTotal[1], 10, 64)
	if errStart != nil || errEnd != nil || errTotal != nil || start < 0 || end < start || total <= 0 || end >= total {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func resumeIdentityMatches(previous state, current resourceValidator) bool {
	if previous.ETag != "" || current.ETag != "" {
		return previous.ETag != "" && current.ETag != "" && previous.ETag == current.ETag
	}
	if previous.LastModified != "" || current.LastModified != "" {
		return previous.LastModified != "" && current.LastModified != "" && previous.LastModified == current.LastModified
	}
	return true
}

func strongETag(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 || strings.HasPrefix(strings.ToUpper(value), "W/") || value[0] != '"' || value[len(value)-1] != '"' {
		return ""
	}
	return value
}

func (v resourceValidator) ifRange() string {
	if etag := strongETag(v.ETag); etag != "" {
		return etag
	}
	return v.LastModified
}

func (v resourceValidator) setFullRequestCondition(req *http.Request) {
	if etag := strongETag(v.ETag); etag != "" {
		req.Header.Set("If-Match", etag)
		return
	}
	if v.LastModified != "" {
		req.Header.Set("If-Unmodified-Since", v.LastModified)
	}
}

func (v resourceValidator) verifyResponse(header http.Header) error {
	if v.ETag != "" {
		if responseETag := strings.TrimSpace(header.Get("ETag")); responseETag != "" && responseETag != v.ETag {
			return fmt.Errorf("%w: ETag no longer matches", errResourceChanged)
		}
	}
	if v.LastModified != "" {
		if responseModified := strings.TrimSpace(header.Get("Last-Modified")); responseModified != "" && responseModified != v.LastModified {
			return fmt.Errorf("%w: Last-Modified no longer matches", errResourceChanged)
		}
	}
	return nil
}

// fetchRange GETs one range and writes it at the file offset.
func fetchRange(ctx context.Context, hc *http.Client, opts Options, url string, start, length, total int64, validator resourceValidator, f *os.File, limiter RateLimiter, account func(int64)) error {
	if start < 0 || length <= 0 || total <= 0 || start > total-length {
		return errors.New("dlengine: invalid range length")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if err := setRequestHeaders(req, opts); err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))
	if condition := validator.ifRange(); condition != "" {
		req.Header.Set("If-Range", condition)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dlengine: range http %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK {
		if validator.ifRange() != "" {
			return fmt.Errorf("%w: server returned the full resource after If-Range", errResourceChanged)
		}
		return errRangeIgnored
	}
	expectedEnd := start + length - 1
	gotStart, gotEnd, gotTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || gotStart != start || gotEnd > expectedEnd || gotTotal != total {
		return fmt.Errorf("%w: requested bytes %d-%d/%d", errInvalidRangeResponse, start, expectedEnd, total)
	}
	responseLength := gotEnd - gotStart + 1
	if resp.ContentLength >= 0 && resp.ContentLength != responseLength {
		return fmt.Errorf("%w: Content-Length is %d, want %d", errInvalidRangeResponse, resp.ContentLength, responseLength)
	}
	if err := validator.verifyResponse(resp.Header); err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	written := int64(0)
	committed := false
	preservePartial := false
	defer func() {
		if !committed && !preservePartial && written > 0 && account != nil {
			// A retry from the original offset overwrites the same range. Roll
			// back bytes reported by this failed attempt.
			account(-written)
		}
	}()
	body := io.LimitReader(resp.Body, responseLength)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if err := limiter.Wait(ctx, int64(n)); err != nil {
				return err
			}
			if _, werr := f.WriteAt(buf[:n], start+written); werr != nil {
				return werr
			}
			written += int64(n)
			if account != nil {
				account(int64(n))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if written > 0 {
				preservePartial = true
				return &partialRangeReadError{err: err, written: written}
			}
			return err
		}
	}
	if written != responseLength {
		err := fmt.Errorf("dlengine: short read %d != %d", written, responseLength)
		if written > 0 {
			preservePartial = true
			return &partialRangeReadError{err: err, written: written}
		}
		return err
	}
	if responseLength < length {
		// Some CDNs cap an otherwise valid requested range. Continue from the
		// exact next byte instead of retrying the original range or waiting for
		// a backoff interval.
		preservePartial = true
		return &partialRangeReadError{err: errShortRangeResponse, written: written}
	}
	committed = true
	return nil
}

func singleStream(ctx context.Context, hc *http.Client, opts Options, url, localPath string, total int64, validator resourceValidator, onProgress func(Progress)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if err := setRequestHeaders(req, opts); err != nil {
		return err
	}
	validator.setFullRequestCondition(req)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return errResourceChanged
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("dlengine: http %d", resp.StatusCode)
	}
	if err := validator.verifyResponse(resp.Header); err != nil {
		return err
	}
	partPath := localPath + ".part"
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var limiter RateLimiter = opts.Limiter
	if limiter == nil {
		limiter = newSpeedLimiter(opts.MaxSpeed)
	}
	buf := make([]byte, 256<<10)
	var written int64
	last := time.Now()
	speed := newSpeedEstimator(last, 0)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if err := limiter.Wait(ctx, int64(n)); err != nil {
				return err
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if onProgress != nil && time.Since(last) >= progressInterval {
				now := time.Now()
				last = now
				onProgress(Progress{Downloaded: written, Total: total, Speed: speed.Observe(now, written), Percent: percent(written, total)})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if total > 0 && written != total {
		return fmt.Errorf("dlengine: downloaded %d != total %d", written, total)
	}
	if err := commitPart(partPath, localPath); err != nil {
		return err
	}
	_ = os.Remove(localPath + ".state.json")
	if onProgress != nil {
		onProgress(Progress{Downloaded: written, Total: total, Speed: 0, Percent: percent(written, total)})
	}
	return nil
}

// commitPart replaces the destination on every platform. Windows refuses to
// rename over an existing file, so move the old destination aside first and
// restore it if the commit fails.
func commitPart(partPath, localPath string) error {
	if err := os.Rename(partPath, localPath); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}

	if _, err := os.Stat(localPath); err != nil {
		return err
	}
	backup, err := os.CreateTemp(filepath.Dir(localPath), ".mnemo-replace-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(localPath, backupPath); err != nil {
		return err
	}
	if err := os.Rename(partPath, localPath); err != nil {
		_ = os.Rename(backupPath, localPath)
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}

func setHeaders(req *http.Request, headers map[string]string, ua string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
}

func setRequestHeaders(req *http.Request, opts Options) error {
	setHeaders(req, opts.Headers, opts.UserAgent)
	if opts.RequestAuth != nil {
		return opts.RequestAuth(req)
	}
	return nil
}

func ensureFile(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if size < 0 {
		return errors.New("dlengine: negative file size")
	}
	cur, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if cur != size {
		if err := f.Truncate(size); err != nil {
			return err
		}
	}
	if cur < size {
		if _, err := f.Seek(size-1, io.SeekStart); err != nil {
			return err
		}
		if _, err := f.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}

func chunkLen(st *state, idx int) int64 {
	start := int64(idx) * st.Chunk
	if start+st.Chunk > st.Total {
		return st.Total - start
	}
	return st.Chunk
}

func percent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(done * 100 / total)
}

func persistState(path string, st *state) error {
	safeState := *st
	if safeState.URLHash == "" && safeState.URL != "" {
		safeState.URLHash = urlFingerprint(safeState.URL)
	}
	safeState.URL = ""
	b, err := json.Marshal(&safeState)
	if err != nil {
		return err
	}
	return writeStateAtomically(path, b)
}

// writeStateAtomically keeps a valid resume state on disk if the process exits
// during a checkpoint. commitPart provides the Windows-safe replacement path.
func writeStateAtomically(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mnemo-state-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	// Keep the existing state-file mode for compatibility while making the
	// content durable before it becomes visible at its final path.
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := commitPart(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func urlFingerprint(rawURL string) string {
	digest := sha256.Sum256([]byte(rawURL))
	return fmt.Sprintf("%x", digest[:])
}

func stateURLMatches(previous state, rawURL, fingerprint string) bool {
	if previous.URLHash != "" {
		return previous.URLHash == fingerprint
	}
	return previous.URL == rawURL
}

// speedLimiter is a token-bucket style throttle (per second).
type speedLimiter struct {
	mu     sync.Mutex
	rate   int64
	window int64
	start  time.Time
}

func newSpeedLimiter(rate int64) *speedLimiter {
	return &speedLimiter{rate: rate, start: time.Now()}
}

func (l *speedLimiter) waitN(n int64) {
	_ = l.Wait(context.Background(), n)
}

func (l *speedLimiter) Wait(ctx context.Context, n int64) error {
	if n <= 0 {
		return nil
	}
	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return nil
	}
	now := time.Now()
	// slide the window: if more than 1s has passed since the last reset,
	// reset the accumulator and the start timestamp so the limiter keeps
	// tracking the recent rate rather than a lifetime average that drifts.
	if now.Sub(l.start) >= time.Second {
		l.start = now
		l.window = 0
	}
	l.window += n
	elapsed := now.Sub(l.start).Seconds()
	allowed := float64(l.rate) * elapsed
	wait := time.Duration(0)
	if float64(l.window) > allowed {
		wait = time.Duration((float64(l.window) - allowed) / float64(l.rate) * float64(time.Second))
	}
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SharedLimiter is a process-wide cap shared by all downloads in a Manager.
type SharedLimiter struct{ speedLimiter }

func NewSharedLimiter(rate int64) *SharedLimiter {
	return &SharedLimiter{speedLimiter: *newSpeedLimiter(rate)}
}

func (l *SharedLimiter) SetRate(rate int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.rate != rate {
		l.rate = rate
		l.window = 0
		l.start = time.Now()
	}
	l.mu.Unlock()
}

var _ = filepath.Base

// proxyFunc returns a proxy function that honors the netx global proxy first,
// then the OS manual proxy and finally HTTP_PROXY/HTTPS_PROXY.
func proxyFunc() func(*http.Request) (*url.URL, error) {
	gp := netx.EffectiveProxyURL()
	if gp != "" {
		if u, err := url.Parse(gp); err == nil && u.Scheme != "" {
			return http.ProxyURL(u)
		}
	}
	return http.ProxyFromEnvironment
}
