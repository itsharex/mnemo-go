// Package netx provides HTTP plumbing shared by every provider: a configurable
// client (UA/referer/proxy/timeout), JSON request helpers, content hashing and
// upload stream helpers.
package netx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mnemo-go/internal/logging"
)

// DefaultUA mirrors the legacy renderer user agent.
const DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Client is a reusable HTTP client factory.
type Client struct {
	HTTP  *http.Client
	UA    string
	Proxy string // optional http(s) proxy url
}

// TestTransportHook, when set, is used as the transport of every NewClient
// http.Client. It exists so provider integration tests can route real outbound
// requests to local mock servers (never set in production).
var TestTransportHook http.RoundTripper

// globalProxy is the application-wide proxy URL set by SetGlobalProxy. Every
// NewClient call picks it up so providers don't each need proxy plumbing.
var globalProxy atomic.Value // stores string

// SetGlobalProxy configures the proxy used by all subsequently created netx
// clients (and the download engine). An empty string disables proxying.
func SetGlobalProxy(proxyURL string) { globalProxy.Store(proxyURL) }

// GlobalProxy returns the currently configured global proxy URL.
func GlobalProxy() string {
	v := globalProxy.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// EffectiveProxyURL keeps API requests and media transfers on the same route:
// an explicit application proxy takes precedence over the OS manual proxy.
func EffectiveProxyURL() string {
	if proxy := GlobalProxy(); proxy != "" {
		return proxy
	}
	return systemProxyURL()
}

// globalUploadRate is the application-wide upload speed cap (bytes/s, 0=unlimited).
// Accessed atomically because SaveSettings writes it while upload goroutines
// read it via GlobalUploadRate.
var globalUploadRate atomic.Int64

var globalUploadThrottle uploadThrottle

type uploadThrottle struct {
	mu      sync.Mutex
	rate    int64
	tokens  float64
	start   time.Time
	changed chan struct{}
}

// SetGlobalUploadRate sets the upload speed cap (bytes/s). 0 disables the cap.
func SetGlobalUploadRate(bytesPerSec int64) {
	globalUploadRate.Store(bytesPerSec)
	globalUploadThrottle.mu.Lock()
	if globalUploadThrottle.rate != bytesPerSec {
		globalUploadThrottle.rate = bytesPerSec
		globalUploadThrottle.tokens = 0
		globalUploadThrottle.start = time.Now()
		if globalUploadThrottle.changed != nil {
			close(globalUploadThrottle.changed)
		}
		globalUploadThrottle.changed = make(chan struct{})
	}
	globalUploadThrottle.mu.Unlock()
}

// GlobalUploadRate returns the current upload speed cap.
func GlobalUploadRate() int64 { return globalUploadRate.Load() }

// WaitGlobalUpload applies the process-wide upload cap to one chunk. It is
// wired into driveutil.ProgressReader so concurrent direct uploads share one
// bucket instead of each creating an independent task-level limit.
func WaitGlobalUpload(n int64) {
	_ = WaitGlobalUploadContext(context.Background(), n)
}

// WaitGlobalUploadContext charges actual bytes against one process-wide bucket.
// Cancellation and settings changes wake waiters without leaving reserved debt.
func WaitGlobalUploadContext(ctx context.Context, n int64) error {
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		globalUploadThrottle.mu.Lock()
		rate := globalUploadThrottle.rate
		if rate <= 0 {
			globalUploadThrottle.mu.Unlock()
			return nil
		}
		now := time.Now()
		burst := max(int64(1), rate/10)
		globalUploadThrottle.tokens = min(float64(burst), globalUploadThrottle.tokens+float64(rate)*now.Sub(globalUploadThrottle.start).Seconds())
		globalUploadThrottle.start = now
		charge := min(n, burst)
		if globalUploadThrottle.tokens >= float64(charge) {
			globalUploadThrottle.tokens -= float64(charge)
			globalUploadThrottle.mu.Unlock()
			n -= charge
			continue
		}
		wait := time.Duration((float64(charge) - globalUploadThrottle.tokens) / float64(rate) * float64(time.Second))
		changed := globalUploadThrottle.changed
		globalUploadThrottle.mu.Unlock()
		timer := time.NewTimer(max(wait, time.Microsecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

type uploadBody struct {
	io.ReadCloser
	ctx context.Context
}

func (b *uploadBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	limit := int64(32 * 1024)
	if rate := GlobalUploadRate(); rate > 0 {
		limit = min(limit, max(int64(1), rate/10))
	}
	if int64(len(p)) > limit {
		p = p[:limit]
	}
	n, err := b.ReadCloser.Read(p)
	if waitErr := WaitGlobalUploadContext(b.ctx, int64(n)); waitErr != nil {
		return 0, waitErr
	}
	return n, err
}

// UploadHTTPClient applies throttling after request construction/signing so
// ContentLength, checksums and seekable SDK inputs keep their original semantics.
type UploadHTTPClient struct {
	Client interface {
		Do(*http.Request) (*http.Response, error)
	}
}

func (c UploadHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return DoUpload(c.Client, req)
}

// DoUpload also wraps replay bodies: redirected/retried bytes count again.
func DoUpload(client interface {
	Do(*http.Request) (*http.Response, error)
}, req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		copy.Body = &uploadBody{ReadCloser: req.Body, ctx: req.Context()}
		if req.GetBody != nil {
			getBody := req.GetBody
			copy.GetBody = func() (io.ReadCloser, error) {
				b, err := getBody()
				if err != nil {
					return nil, err
				}
				return &uploadBody{ReadCloser: b, ctx: req.Context()}, nil
			}
		}
	}
	return client.Do(copy)
}

func (c *Client) DoUpload(ctx context.Context, method, target string, headers map[string]string, body io.Reader) (*http.Response, error) {
	req, err := c.Req(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return DoUpload(c.HTTP, req)
}

// NewClient builds a client with sane defaults.
func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	c := &Client{HTTP: &http.Client{Timeout: timeout}, UA: DefaultUA}
	if TestTransportHook != nil {
		c.HTTP = &http.Client{Timeout: timeout, Transport: TestTransportHook}
		return c
	}
	if gp := GlobalProxy(); gp != "" {
		return c.WithProxy(gp)
	}
	return c
}

// NewClientWithSystemProxy builds a client that prefers the application's
// explicit proxy setting and otherwise uses a configured Windows manual proxy.
// It intentionally leaves PAC/WPAD resolution to the operating system/browser;
// callers can still configure an application proxy when a PAC is in use.
func NewClientWithSystemProxy(timeout time.Duration) *Client {
	c := NewClient(timeout)
	if TestTransportHook != nil || c.Proxy != "" {
		return c
	}
	if proxyURL := systemProxyURL(); proxyURL != "" {
		return c.WithProxy(proxyURL)
	}
	return c
}

// normalizeSystemProxy converts the ProxyServer registry formats used by
// Windows into a URL accepted by http.Transport. HTTPS is preferred for an
// HTTPS request, followed by HTTP and SOCKS entries.
func normalizeSystemProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var httpsProxy, httpProxy, socksProxy, directProxy string
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "://") {
			if directProxy == "" {
				directProxy = part
			}
			continue
		}
		key, value, hasMapping := strings.Cut(part, "=")
		if !hasMapping {
			if directProxy == "" {
				directProxy = part
			}
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "https":
			httpsProxy = value
		case "http":
			httpProxy = value
		case "socks", "socks5":
			socksProxy = value
		}
	}

	for _, candidate := range []struct {
		value         string
		defaultScheme string
	}{
		{httpsProxy, "http"},
		{httpProxy, "http"},
		{socksProxy, "socks5"},
		{directProxy, "http"},
	} {
		if normalized := normalizeProxyEndpoint(candidate.value, candidate.defaultScheme); normalized != "" {
			return normalized
		}
	}
	return ""
}

func normalizeProxyEndpoint(raw, defaultScheme string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = defaultScheme + "://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Hostname() == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return parsed.String()
	default:
		return ""
	}
}

// WithProxy returns a client that routes traffic through proxyURL.
func (c *Client) WithProxy(proxyURL string) *Client {
	clone := *c
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if u, err := url.Parse(proxyURL); err == nil && u.Scheme != "" {
		transport.Proxy = http.ProxyURL(u)
	}
	clone.HTTP = &http.Client{Transport: transport, Timeout: c.HTTP.Timeout}
	clone.Proxy = proxyURL
	return &clone
}

// Req builds an http.Request with the client's UA.
func (c *Client) Req(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UA)
	return req, nil
}

// Do executes a request with the client UA preset.
func (c *Client) Do(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) (*http.Response, error) {
	started := time.Now()
	target := requestTarget(rawURL)
	logging.Debug("HTTP request started", "method", method, "target", target)
	req, err := c.Req(ctx, method, rawURL, body)
	if err != nil {
		logging.Warn("HTTP request construction failed", "method", method, "target", target, "error", err)
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		logging.Warn("HTTP request failed", "method", method, "target", target, "error", err, "duration", logging.Duration(started))
		return nil, err
	}
	logging.Debug("HTTP request completed", "method", method, "target", target, "status", resp.StatusCode, "duration", logging.Duration(started))
	return resp, nil
}

// GetJSON GETs rawURL and decodes a JSON response.
func (c *Client) GetJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	resp, err := c.Do(ctx, http.MethodGet, rawURL, headers, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return DecodeJSON(resp, out)
}

// PostJSON POSTs a JSON body and decodes the JSON response.
func (c *Client) PostJSON(ctx context.Context, rawURL string, headers map[string]string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	headers = cloneHeaders(headers)
	headers["Content-Type"] = "application/json"
	resp, err := c.Do(ctx, http.MethodPost, rawURL, headers, &buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return DecodeJSON(resp, out)
}

// PostForm POSTs form data and decodes JSON.
func (c *Client) PostForm(ctx context.Context, rawURL string, headers map[string]string, form url.Values, out any) error {
	headers = cloneHeaders(headers)
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	resp, err := c.Do(ctx, http.MethodPost, rawURL, headers, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return DecodeJSON(resp, out)
}

func requestTarget(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "invalid"
	}
	return u.Hostname() + u.EscapedPath()
}

func cloneHeaders(headers map[string]string) map[string]string {
	clone := make(map[string]string, len(headers)+1)
	for key, value := range headers {
		clone[key] = value
	}
	return clone
}

// DecodeJSON reads a response into out, tolerating gzip.
func DecodeJSON(resp *http.Response, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(body, 500))
	}
	if len(body) == 0 {
		if out == nil {
			return nil
		}
		return fmt.Errorf("empty response body")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("json decode: %w: %s", err, truncate(body, 300))
	}
	return nil
}

// ReadBody reads and returns the response body (checked status).
func ReadBody(resp *http.Response, max int64) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(body, 500))
	}
	return body, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// JSONBody is a convenience wrapper for request bodies.
func JSONBody(v any) io.Reader {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(v)
	return &buf
}
