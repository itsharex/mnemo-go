package driveutil

import (
	"io"
	"sync/atomic"
	"time"
)

// uploadRateGetter is overridden by netx at init time to avoid a circular
// import (driveutil cannot import netx). It returns the global upload rate.
var uploadRateGetter func() int64
var uploadThrottle func(int64)

// SetUploadRateGetter wires the netx global upload rate into the progress
// reader without creating an import cycle.
func SetUploadRateGetter(f func() int64) { uploadRateGetter = f }

// SetUploadThrottle wires a process-wide throttle for concurrent direct
// uploads. The getter remains as a compatibility fallback for tests and
// embedders that have not installed a throttle callback.
func SetUploadThrottle(f func(int64)) { uploadThrottle = f }

// ProgressReader wraps an io.Reader and invokes onRead after each Read call
// with the cumulative byte count. It is a lightweight progress reporter for
// direct-upload (webdav/s3) providers that do not go through the chunked
// upload queue. When a global upload rate cap is configured, it throttles
// reads to respect the cap.
type ProgressReader struct {
	progressOnly bool
	r            io.Reader
	read         int64
	size         int64
	onRead       func(read int64)
	// token-bucket-style rate limiting state
	bucket int64
	last   time.Time
}

// NewProgressReader wraps r and reports cumulative read progress via onRead.
// size is the total file size (for percentage calculations by the caller).
func NewProgressReader(r io.Reader, size int64, onRead func(int64)) *ProgressReader {
	return &ProgressReader{r: r, size: size, onRead: onRead, last: time.Now()}
}

// NewProgressOnlyReader reports progress when the HTTP send path already owns
// rate limiting. This avoids charging the same bytes twice.
func NewProgressOnlyReader(r io.Reader, size int64, onRead func(int64)) *ProgressReader {
	return &ProgressReader{r: r, size: size, onRead: onRead, progressOnly: true}
}

func (p *ProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read += int64(n)
		if p.onRead != nil {
			p.onRead(p.read)
		}
		if !p.progressOnly {
			p.throttle(int64(n))
		}
	}
	return n, err
}

// throttle applies a token-bucket style sleep so the effective read rate
// stays within the global upload cap. No-op when no cap is set.
func (p *ProgressReader) throttle(n int64) {
	if uploadThrottle != nil {
		uploadThrottle(n)
		return
	}
	var rate int64
	if uploadRateGetter != nil {
		rate = uploadRateGetter()
	}
	if rate <= 0 {
		return
	}
	now := time.Now()
	// refill: add bytes accrued since last call, capped at rate (1s worth)
	elapsed := now.Sub(p.last).Seconds()
	refill := int64(elapsed * float64(rate))
	atomic.AddInt64(&p.bucket, refill)
	if atomic.LoadInt64(&p.bucket) > rate {
		atomic.StoreInt64(&p.bucket, rate)
	}
	p.last = now
	// consume n bytes
	for {
		cur := atomic.LoadInt64(&p.bucket)
		if cur >= n {
			if atomic.CompareAndSwapInt64(&p.bucket, cur, cur-n) {
				return
			}
			continue
		}
		// not enough tokens: sleep for the deficit
		deficit := n - cur
		wait := time.Duration(float64(deficit)/float64(rate)*1e9) * time.Nanosecond
		if wait > 0 {
			time.Sleep(wait)
		}
		// after sleeping, set bucket to 0 (we've effectively spent the deficit)
		atomic.StoreInt64(&p.bucket, 0)
		return
	}
}
