// Package captcha opens the PikPak challenge in the system default browser
// and receives the verified token via a local HTTP callback server.
//
// PikPak's captcha flow redirects to a callback URL after the user completes
// the slider. The callback can carry captcha_token in its query or fragment.
// We start a temporary local HTTP server, rewrite the challenge URL to use
// our callback, and wait for the redirect.
package captcha

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/logging"
)

// Session identifies one application-owned captcha callback endpoint.
// The random path keeps unrelated local requests from completing a login.
type Session struct {
	ID          string
	CallbackURL string
}

// CompletedFunc receives the token returned after a visual challenge.
// A token can be empty when PikPak only signals completion through its redirect;
// callers can then exchange the original challenge token with the API.
type CompletedFunc func(session Session, token string)

const legacyCallbackURI = "xlaccsdk01://xbase.cloud/callback"

// RedirectURI keeps the native verification exchange identical to the legacy
// client. The protocol is intercepted inside the window; no OS registration is
// needed. Browser/iframe platforms use their session's localhost endpoint.
func RedirectURI(session Session) string {
	if launchWindow != nil {
		return legacyCallbackURI
	}
	return session.CallbackURL
}

var (
	mu            sync.Mutex
	server        *http.Server
	listener      net.Listener
	onDone        CompletedFunc
	activeSession Session
	completed     bool
	callbackTimer *time.Timer
	windowCancel  context.CancelFunc
	launchWindow  func(context.Context, Session, string, string) error
)

// Show opens the challenge in a platform-owned window without replacing the
// callback/device session used to obtain it. False selects the embedded fallback
// on platforms that do not yet have a native captcha window.
func Show(sessionID, rawURL, profileDir string) (bool, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false, fmt.Errorf("captcha: 无效的验证页面地址")
	}
	mu.Lock()
	if activeSession.ID != sessionID || completed || onDone == nil {
		mu.Unlock()
		return false, fmt.Errorf("captcha: 验证会话已过期，请重新登录")
	}
	if launchWindow == nil {
		mu.Unlock()
		return false, nil
	}
	if windowCancel != nil {
		windowCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	windowCancel = cancel
	session := activeSession
	mu.Unlock()
	if err := launchWindow(ctx, session, rawURL, profileDir); err != nil {
		cancel()
		return true, fmt.Errorf("captcha: 无法打开验证窗口: %w", err)
	}
	return true, nil
}

func callbackToken(session Session, raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	local, _ := url.Parse(session.CallbackURL)
	isLocal := local != nil && u.Scheme == local.Scheme && u.Host == local.Host && u.Path == local.Path
	isLegacy := u.Scheme == "xlaccsdk01" && u.Host == "xbase.cloud" && u.Path == "/callback"
	if !isLocal && !isLegacy {
		return "", false
	}
	fragment, _ := url.ParseQuery(u.Fragment)
	for _, values := range []url.Values{u.Query(), fragment} {
		for _, key := range []string{"captcha_token", "captchaToken", "token"} {
			if token := normalizeToken(values.Get(key)); token != "" {
				return token, true
			}
		}
	}
	return "", true
}

func isReportURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Path != "/credit/v1/report" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "mypikpak.com" || strings.HasSuffix(host, ".mypikpak.com") || host == "mypikpak.net" || strings.HasSuffix(host, ".mypikpak.net")
}

func acceptReport(sessionID, rawURL string, body []byte) bool {
	if !isReportURL(rawURL) || len(body) > 1<<20 {
		return false
	}
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	token := findReportToken(payload, 0)
	if token == "" {
		return false
	}
	_, accepted := complete(sessionID, token)
	return accepted
}

func findReportToken(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	switch v := value.(type) {
	case map[string]any:
		if token, ok := v["captcha_token"].(string); ok {
			if token = normalizeToken(token); token != "" {
				return token
			}
		}
		for _, child := range v {
			if token := findReportToken(child, depth+1); token != "" {
				return token
			}
		}
	case []any:
		for _, child := range v {
			if token := findReportToken(child, depth+1); token != "" {
				return token
			}
		}
	}
	return ""
}

// Like the legacy client, allow the report response to win the redirect race.
func queueCallback(sessionID, token string) {
	mu.Lock()
	defer mu.Unlock()
	if activeSession.ID != sessionID || completed || onDone == nil || callbackTimer != nil {
		return
	}
	callbackTimer = time.AfterFunc(500*time.Millisecond, func() { complete(sessionID, token) })
}

// Start creates a localhost callback endpoint without opening a browser. The
// caller supplies Session.CallbackURL to PikPak while it initializes a visual
// challenge, so the challenge itself redirects back to this application.
func Start(onComplete CompletedFunc) (*Session, error) {
	mu.Lock()
	defer mu.Unlock()
	session, err := startLocked(onComplete)
	if err != nil {
		logging.Warn("captcha callback server start failed", "error", err)
	} else {
		logging.Debug("captcha callback server started", "session_id", session.ID, "callback_host", "127.0.0.1")
	}
	return session, err
}

// Open launches the system browser at the challenge URL and starts a local
// HTTP server to receive the captcha callback. It remains as a legacy fallback;
// the normal login flow keeps the challenge embedded in the login page.
func Open(rawURL string, onComplete CompletedFunc) error {
	mu.Lock()
	defer mu.Unlock()

	session, err := startLocked(onComplete)
	if err != nil {
		logging.Warn("captcha browser session start failed", "error", err)
		return err
	}

	// The challenge URL points to PikPak's captcha page. After the user
	// completes the slider, PikPak redirects to a callback. We intercept by
	// opening the challenge in the system browser — the redirect will land on
	// our local server if PikPak uses a localhost redirect_uri, or we extract
	// the token from the final URL via the /redirect helper.
	challengeURL := buildChallengeURL(rawURL, session.CallbackURL)

	if err := openBrowser(challengeURL); err != nil {
		logging.Warn("captcha browser launch failed", "error", err)
		stopLocked()
		return fmt.Errorf("captcha: 无法打开浏览器: %w", err)
	}
	logging.Info("captcha browser challenge opened", "session_id", session.ID)
	return nil
}

func startLocked(onComplete CompletedFunc) (*Session, error) {
	// Stop any previous session: only one login modal can be active at once.
	stopLocked()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("captcha: start callback server: %w", err)
	}
	id, err := newSessionID()
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("captcha: create callback session: %w", err)
	}
	session := Session{
		ID:          id,
		CallbackURL: fmt.Sprintf("http://%s/callback/%s", ln.Addr().String(), id),
	}

	listener = ln
	onDone = onComplete
	activeSession = session
	completed = false

	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) { handleCallback(w, r, id) }
	mux.HandleFunc("/callback/"+id, handler)
	mux.HandleFunc("/redirect/"+id, handler)
	srv := &http.Server{Handler: mux}
	server = srv
	go func() { _ = srv.Serve(ln) }()
	return &session, nil
}

func newSessionID() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Close stops the callback server.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	stopLocked()
	logging.Debug("captcha callback server stopped")
}

func stopLocked() {
	if windowCancel != nil {
		windowCancel()
		windowCancel = nil
	}
	if callbackTimer != nil {
		callbackTimer.Stop()
		callbackTimer = nil
	}
	// Detach first while the session lock is held. Shutdown can wait for an
	// in-flight callback, which may itself need this lock to report completion.
	// Closing the listener rejects new requests immediately; the detached server
	// then drains asynchronously without blocking a replacement session.
	srv := server
	ln := listener
	server = nil
	listener = nil
	onDone = nil
	activeSession = Session{}
	completed = false
	if ln != nil {
		_ = ln.Close()
	}
	if srv != nil {
		go shutdownServer(srv)
	}
}

func shutdownServer(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// buildChallengeURL appends our callback as a redirect parameter for the
// legacy browser fallback. Normal login supplies the same callback URI during
// PikPak's captcha-init request, before the challenge URL is issued.
func buildChallengeURL(rawURL, callbackURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := parsed.Query()
	if callbackURL != "" {
		q.Set("redirect_uri", callbackURL)
	}
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// handleCallback receives the redirect from PikPak after captcha completion.
func handleCallback(w http.ResponseWriter, r *http.Request, sessionID string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid callback form", http.StatusBadRequest)
			return
		}
	}
	token := extractToken(r)
	if r.Method == http.MethodGet && token == "" {
		// Fragments never reach an HTTP server. Let the landing page forward
		// the browser's fragment before emitting even a tokenless completion.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(callbackPage))
		return
	}
	session, accepted := complete(sessionID, token)
	logging.Info("captcha callback received", "session_id", session.ID, "accepted", accepted, "has_token", token != "")
	// Return a minimal HTML that auto-closes the tab.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><p>验证完成，可关闭此页面</p>`))
	// Shut down the server after a short delay.
	if accepted {
		go func(sessionID string) {
			time.Sleep(2 * time.Second)
			closeSession(sessionID)
		}(session.ID)
	}
}

const callbackPage = `<!doctype html><meta charset="utf-8">
<p id="status">正在确认验证结果…</p>
<script>
(async () => {
  const fragment = new URLSearchParams(location.hash.slice(1));
  let token = '';
  for (const key of ['captcha_token', 'captchaToken', 'token']) {
    const value = (fragment.get(key) || '').trim();
    if (value.length > 20) { token = value; break; }
  }
  const path = location.pathname;
  history.replaceState(null, '', path);
  try {
    const response = await fetch(path, {
      method: 'POST',
      body: new URLSearchParams({captcha_token: token})
    });
    if (!response.ok) throw new Error('callback failed');
    document.getElementById('status').textContent = '验证完成，可关闭此页面';
  } catch {
    document.getElementById('status').textContent = '验证结果未能送达，请返回应用重新验证';
  }
})();
</script>`

func complete(sessionID, token string) (Session, bool) {
	mu.Lock()
	if completed || onDone == nil || activeSession.ID != sessionID {
		mu.Unlock()
		logging.Debug("captcha callback ignored", "reason", "no active session or already completed")
		return Session{}, false
	}
	completed = true
	if callbackTimer != nil {
		callbackTimer.Stop()
		callbackTimer = nil
	}
	if windowCancel != nil {
		windowCancel()
		windowCancel = nil
	}
	callback := onDone
	session := activeSession
	mu.Unlock()
	go callback(session, token)
	return session, true
}

func closeSession(sessionID string) {
	if sessionID == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if activeSession.ID == sessionID {
		stopLocked()
	}
}

func extractToken(r *http.Request) string {
	for _, key := range []string{"captcha_token", "captchaToken", "token"} {
		if t := normalizeToken(r.PostForm.Get(key)); t != "" {
			return t
		}
		if t := normalizeToken(r.URL.Query().Get(key)); t != "" {
			return t
		}
	}
	return ""
}

func normalizeToken(raw string) string {
	t := strings.TrimSpace(raw)
	if len(t) > 20 {
		return t
	}
	return ""
}

// openBrowser opens the system default browser at url.
func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	case "darwin":
		return exec.Command("open", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}
