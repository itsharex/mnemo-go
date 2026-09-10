// Package preview hosts the internal local HTTP server used by the frontend
// for media playback and downloads that require headers / local proxying.
//
// Security model:
//   - Every /proxy/ and /local/ request must carry a session token (t=...).
//     The token is generated once at server startup and embedded into URLs
//     built by ProxyURL/LocalURL, so the frontend never handles it directly.
//   - /local/ only serves individually registered files under configured
//     download roots. Browser-visible URLs contain an opaque grant id, not a
//     local absolute path.
//   - /proxy/ validates the target URL scheme (http/https only) and blocks
//     private/loopback/link-local addresses to prevent SSRF.
//   - CORS is restricted to the Wails origin (http://localhost:* /
//     wails://localhost) instead of "*".
//
// Routes:
//
//	GET /proxy/?u=<url>&h=<base64json>&t=<token>   streaming proxy (Range supported)
//	GET /local/<grant-id>?t=<token>                local file serving (Range)
package preview

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// Server is the internal media proxy.
type Server struct {
	ln                net.Listener
	server            *http.Server
	Port              int
	token             string
	roots             []string // allowed local file roots
	allowedProxyHosts map[string]struct{}
	proxyClient       *http.Client
	proxyTransport    *http.Transport
	mu                sync.Mutex // guards roots and allowedProxyHosts
	sessionsMu        sync.Mutex
	sessions          map[string]*playbackSession
	localFilesMu      sync.Mutex
	localFiles        map[string]localFileGrant
}

// PlaybackSource is a browser-playable source registered with the local
// proxy. The URL and headers stay in the Go process; only an opaque session
// id is exposed to the WebView. Refresh is called when a provider rejects an
// expired signed URL, allowing an in-flight video request to recover without
// exposing provider credentials to JavaScript.
type PlaybackSource struct {
	URL                 string
	Headers             map[string]string
	RequestAuth         model.RequestAuthenticator
	AllowPrivateNetwork bool
	Filename            string
	StreamType          string
	ExpiresAt           time.Time
	Refresh             func(context.Context) (PlaybackSource, error)
}

type playbackSession struct {
	mu              sync.Mutex
	source          PlaybackSource
	lastUsed        time.Time
	resources       map[string]string
	resourceIDs     map[string]string
	dashResources   map[string]dashPlaybackResource
	dashResourceIDs map[string]string
	resourceOrder   []playbackResourceRef
	resourceSeq     uint64
}

type localFileGrant struct {
	path     string
	lastUsed time.Time
}

type dashPlaybackResource struct {
	BaseURL       string
	RawQuery      string
	QueryBindings []dashQueryBinding
}

type dashQueryBinding struct {
	Token    string
	LocalKey string
}

type playbackResourceRef struct {
	ID   string
	Dash bool
}

const playbackSessionTTL = 12 * time.Hour
const maxPlaybackResources = 2048
const localFileGrantTTL = 12 * time.Hour
const maxLocalFileGrants = 1024

// dashTokenParam keeps the local stream token separate from an upstream DASH
// URL's query string. Signed query strings stay in the Go-side session and
// are never copied into browser-visible segment URLs.
const dashTokenParam = "_mnemo_stream_token"

// NewServer starts the internal HTTP server on a random port.
// roots are the directory prefixes that /local/ is allowed to serve from.
func NewServer(roots ...string) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		ln.Close()
		return nil, err
	}
	// dedupe + clean roots
	cleanRoots := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := canonicalPath(r)
		if err != nil {
			continue
		}
		if !seen[abs] {
			seen[abs] = true
			cleanRoots = append(cleanRoots, abs)
		}
	}
	s := &Server{
		ln:                ln,
		Port:              ln.Addr().(*net.TCPAddr).Port,
		token:             base64.RawURLEncoding.EncodeToString(tok),
		roots:             cleanRoots,
		allowedProxyHosts: make(map[string]struct{}),
		sessions:          make(map[string]*playbackSession),
		localFiles:        make(map[string]localFileGrant),
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.Proxy = globalProxy
	transport.DialContext = s.safeProxyDialContext
	s.proxyTransport = transport
	s.proxyClient = &http.Client{
		Timeout:       0,
		Transport:     transport,
		CheckRedirect: s.checkProxyRedirect,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/proxy/", s.handleProxy)
	mux.HandleFunc("/stream/", s.handleStream)
	mux.HandleFunc("/local/", s.handleLocal)
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = s.server.Serve(ln) }()
	return s, nil
}

// AddRoot adds a directory to the allowed /local/ roots at runtime.
func (s *Server) AddRoot(dir string) {
	abs, err := canonicalPath(dir)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.roots {
		if r == abs {
			return
		}
	}
	s.roots = append(s.roots, abs)
}

// SetRoots replaces all local preview roots and revokes existing file grants.
// Runtime download-directory changes must not leave previous directories
// permanently readable by the local server.
func (s *Server) SetRoots(dirs ...string) {
	cleanRoots := make([]string, 0, len(dirs))
	seen := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		abs, err := canonicalPath(dir)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		cleanRoots = append(cleanRoots, abs)
	}
	s.mu.Lock()
	s.roots = cleanRoots
	s.mu.Unlock()
	s.localFilesMu.Lock()
	s.localFiles = make(map[string]localFileGrant)
	s.localFilesMu.Unlock()
}

// BaseURL returns http://127.0.0.1:port.
func (s *Server) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", s.Port) }

// ProxyURL builds a proxied media URL for a remote stream. filename is an
// optional display name forwarded to the browser via Content-Disposition so
// the preview shows the real file name instead of a guessed one.
func (s *Server) ProxyURL(target string, headers map[string]string, filename string) string {
	s.rememberProxyHost(target, false)
	hdrs, _ := json.Marshal(headers)
	q := url.Values{}
	q.Set("u", target)
	q.Set("h", base64.StdEncoding.EncodeToString(hdrs))
	q.Set("t", s.token)
	if filename != "" {
		q.Set("f", filename)
	}
	return fmt.Sprintf("%s/proxy/?%s", s.BaseURL(), q.Encode())
}

// PlaybackURL registers a source-backed stream session and returns its local
// URL. Unlike ProxyURL, this does not put the upstream URL or auth headers in
// the browser-visible query string. The session is also the anchor used to
// rewrite HLS playlist references to local proxy routes.
func (s *Server) PlaybackURL(source PlaybackSource) (string, error) {
	source = clonePlaybackSource(source)
	if strings.TrimSpace(source.URL) == "" {
		return "", fmt.Errorf("empty playback source")
	}
	// Provider URLs are registered by the Go side. Remembering the initial host
	// also permits local test/provider endpoints while redirects are still
	// checked by checkProxyRedirect.
	s.rememberProxyHost(source.URL, source.AllowPrivateNetwork)
	if !s.isSafeProxyURL(source.URL) {
		return "", fmt.Errorf("playback source host is not allowed")
	}
	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return "", fmt.Errorf("create playback session: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	now := time.Now()
	s.sessionsMu.Lock()
	for key, session := range s.sessions {
		session.mu.Lock()
		stale := now.Sub(session.lastUsed) > playbackSessionTTL
		session.mu.Unlock()
		if stale {
			delete(s.sessions, key)
		}
	}
	s.sessions[id] = &playbackSession{
		source:          source,
		lastUsed:        now,
		resources:       make(map[string]string),
		resourceIDs:     make(map[string]string),
		dashResources:   make(map[string]dashPlaybackResource),
		dashResourceIDs: make(map[string]string),
	}
	s.sessionsMu.Unlock()
	return fmt.Sprintf("%s/stream/%s?t=%s", s.BaseURL(), id, url.QueryEscape(s.token)), nil
}

func clonePlaybackSource(source PlaybackSource) PlaybackSource {
	if len(source.Headers) > 0 {
		source.Headers = cloneHeaderMap(source.Headers)
	}
	return source
}

func cloneHeaderMap(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func (s *Server) getPlaybackSession(id string) *playbackSession {
	s.sessionsMu.Lock()
	session := s.sessions[id]
	s.sessionsMu.Unlock()
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if time.Since(session.lastUsed) > playbackSessionTTL {
		session.mu.Unlock()
		s.sessionsMu.Lock()
		if s.sessions[id] == session {
			delete(s.sessions, id)
		}
		s.sessionsMu.Unlock()
		return nil
	}
	session.lastUsed = time.Now()
	session.mu.Unlock()
	return session
}

func (session *playbackSession) resolve(ctx context.Context, forceRefresh bool) (PlaybackSource, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	now := time.Now()
	needsRefresh := forceRefresh
	if !needsRefresh && !session.source.ExpiresAt.IsZero() {
		needsRefresh = !now.Before(session.source.ExpiresAt.Add(-30 * time.Second))
	}
	if needsRefresh && session.source.Refresh != nil {
		refresh := session.source.Refresh
		fresh, err := refresh(ctx)
		if err != nil {
			return PlaybackSource{}, err
		}
		fresh = clonePlaybackSource(fresh)
		if strings.TrimSpace(fresh.URL) == "" {
			return PlaybackSource{}, fmt.Errorf("refreshed playback source is empty")
		}
		if fresh.Refresh == nil {
			fresh.Refresh = refresh
		}
		session.source = fresh
	}
	session.lastUsed = now
	return clonePlaybackSource(session.source), nil
}

func (s *Server) playbackResourceURL(sessionID string, session *playbackSession, target string) string {
	session.mu.Lock()
	if id, ok := session.resourceIDs[target]; ok {
		session.lastUsed = time.Now()
		session.mu.Unlock()
		return fmt.Sprintf("%s/stream/%s/%s?t=%s", s.BaseURL(), sessionID, id, url.QueryEscape(s.token))
	}
	session.resourceSeq++
	id := strconv.FormatUint(session.resourceSeq, 36)
	session.resources[id] = target
	session.resourceIDs[target] = id
	session.resourceOrder = append(session.resourceOrder, playbackResourceRef{ID: id})
	session.trimResourcesLocked()
	session.lastUsed = time.Now()
	session.mu.Unlock()
	return fmt.Sprintf("%s/stream/%s/%s?t=%s", s.BaseURL(), sessionID, id, url.QueryEscape(s.token))
}

func (session *playbackSession) dashResourceID(baseURL, rawQuery string) (string, dashPlaybackResource) {
	key := baseURL + "\x00" + rawQuery
	session.mu.Lock()
	if id, ok := session.dashResourceIDs[key]; ok {
		resource := session.dashResources[id]
		session.lastUsed = time.Now()
		session.mu.Unlock()
		return id, resource
	}
	session.resourceSeq++
	id := strconv.FormatUint(session.resourceSeq, 36)
	resource := dashPlaybackResource{BaseURL: baseURL, RawQuery: rawQuery, QueryBindings: dashQueryBindings(rawQuery)}
	session.dashResources[id] = resource
	session.dashResourceIDs[key] = id
	session.resourceOrder = append(session.resourceOrder, playbackResourceRef{ID: id, Dash: true})
	session.trimResourcesLocked()
	session.lastUsed = time.Now()
	session.mu.Unlock()
	return id, resource
}

func (session *playbackSession) trimResourcesLocked() {
	for len(session.resourceOrder) > maxPlaybackResources {
		oldest := session.resourceOrder[0]
		session.resourceOrder = session.resourceOrder[1:]
		if oldest.Dash {
			if source, ok := session.dashResources[oldest.ID]; ok {
				delete(session.dashResources, oldest.ID)
				delete(session.dashResourceIDs, source.BaseURL+"\x00"+source.RawQuery)
			}
			continue
		}
		if target, ok := session.resources[oldest.ID]; ok {
			delete(session.resources, oldest.ID)
			delete(session.resourceIDs, target)
		}
	}
}

// dashResourceURL maps one concrete DASH resource URL to a local route while
// keeping the dynamic path suffix visible to dash.js. Segment templates such
// as chunk-$Number$.m4s are expanded by the browser after the MPD is parsed,
// so storing only a full opaque target (as HLS does) is not sufficient here.
func (s *Server) dashResourceURL(sessionID string, session *playbackSession, target string) (string, error) {
	targetURL, err := url.Parse(target)
	if err != nil || (targetURL.Scheme != "http" && targetURL.Scheme != "https") || targetURL.Hostname() == "" {
		return "", fmt.Errorf("invalid DASH resource URL")
	}
	if !s.isSafeProxyURL(target) {
		return "", fmt.Errorf("DASH resource host is not allowed")
	}
	baseURL := (&url.URL{
		Scheme: targetURL.Scheme,
		Host:   targetURL.Host,
		User:   targetURL.User,
	}).String()
	resourceID, resource := session.dashResourceID(baseURL, targetURL.RawQuery)
	path := targetURL.EscapedPath()
	if path == "" {
		path = "/"
	}
	query := dashTokenParam + "=" + url.QueryEscape(s.token)
	for _, binding := range resource.QueryBindings {
		// Leave DASH placeholders literal so dash.js can substitute them in the
		// local URL without ever receiving the signed provider query string.
		query += "&" + binding.LocalKey + "=" + binding.Token
	}
	return fmt.Sprintf("%s/stream/%s/r/%s%s?%s", s.BaseURL(), sessionID, resourceID, path, query), nil
}

// dashResourceTarget reconstructs an upstream target from a local /r/ route.
// The stored URL is origin-only; the MPD-controlled suffix cannot replace the
// origin, which preserves the proxy's SSRF boundary.
func (s *Server) dashResourceTarget(session *playbackSession, resourceID, escapedSuffix string, query url.Values) (string, bool) {
	if !strings.HasPrefix(escapedSuffix, "/") || strings.HasPrefix(escapedSuffix, "//") {
		return "", false
	}
	resource, ok := session.dashResource(resourceID)
	if !ok {
		return "", false
	}
	baseURL, err := url.Parse(resource.BaseURL)
	if err != nil || baseURL.Hostname() == "" {
		return "", false
	}
	reference, err := url.Parse(escapedSuffix)
	if err != nil || reference.IsAbs() || reference.Host != "" {
		return "", false
	}
	rawQuery, ok := resolveDashQuery(resource, query)
	if !ok {
		return "", false
	}
	reference.RawQuery = rawQuery
	target := baseURL.ResolveReference(reference).String()
	return target, s.isSafeProxyURL(target)
}

func dashQueryBindings(rawQuery string) []dashQueryBinding {
	if rawQuery == "" {
		return nil
	}
	seen := make(map[string]struct{})
	bindings := make([]dashQueryBinding, 0, 2)
	for offset := 0; offset < len(rawQuery); {
		startOffset := strings.IndexByte(rawQuery[offset:], '$')
		if startOffset < 0 {
			break
		}
		start := offset + startOffset
		endOffset := strings.IndexByte(rawQuery[start+1:], '$')
		if endOffset < 0 {
			break
		}
		end := start + endOffset + 2
		token := rawQuery[start:end]
		offset = end
		if !isDASHTemplateToken(token) {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		bindings = append(bindings, dashQueryBinding{Token: token, LocalKey: fmt.Sprintf("_mnemo_dash_%d", len(bindings))})
	}
	return bindings
}

func isDASHTemplateToken(token string) bool {
	if len(token) < 3 || token[0] != '$' || token[len(token)-1] != '$' {
		return false
	}
	name := token[1 : len(token)-1]
	for _, prefix := range []string{"Number", "Time", "RepresentationID", "Bandwidth", "SubNumber"} {
		if name == prefix || strings.HasPrefix(name, prefix+"%") {
			return true
		}
	}
	return false
}

func resolveDashQuery(resource dashPlaybackResource, query url.Values) (string, bool) {
	rawQuery := resource.RawQuery
	for _, binding := range resource.QueryBindings {
		values, ok := query[binding.LocalKey]
		if !ok || len(values) == 0 {
			return "", false
		}
		rawQuery = strings.ReplaceAll(rawQuery, binding.Token, url.QueryEscape(values[0]))
	}
	return rawQuery, true
}

func (session *playbackSession) dashResource(id string) (dashPlaybackResource, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	resource, ok := session.dashResources[id]
	if ok {
		session.lastUsed = time.Now()
	}
	return resource, ok
}

func (session *playbackSession) resource(id string) (string, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	target, ok := session.resources[id]
	if ok {
		session.lastUsed = time.Now()
	}
	return target, ok
}

// LocalURL registers one exact local file and returns an opaque local URL.
// Invalid, missing, directory, or out-of-root paths are never registered.
func (s *Server) LocalURL(path string) string {
	resolved, err := canonicalPath(path)
	if err != nil || !s.isWithinRoots(resolved) {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return ""
	}
	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return ""
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	now := time.Now()
	s.localFilesMu.Lock()
	for key, grant := range s.localFiles {
		if now.Sub(grant.lastUsed) > localFileGrantTTL {
			delete(s.localFiles, key)
		}
	}
	for len(s.localFiles) >= maxLocalFileGrants {
		oldestID := ""
		var oldest time.Time
		for key, grant := range s.localFiles {
			if oldestID == "" || grant.lastUsed.Before(oldest) {
				oldestID, oldest = key, grant.lastUsed
			}
		}
		if oldestID == "" {
			break
		}
		delete(s.localFiles, oldestID)
	}
	s.localFiles[id] = localFileGrant{path: resolved, lastUsed: now}
	s.localFilesMu.Unlock()
	q := url.Values{}
	q.Set("t", s.token)
	return fmt.Sprintf("%s/local/%s?%s", s.BaseURL(), id, q.Encode())
}

func (s *Server) resolveLocalFile(id string) (string, bool) {
	if id == "" || strings.ContainsAny(id, `/\\`) {
		return "", false
	}
	s.localFilesMu.Lock()
	defer s.localFilesMu.Unlock()
	grant, ok := s.localFiles[id]
	if !ok {
		return "", false
	}
	if time.Since(grant.lastUsed) > localFileGrantTTL {
		delete(s.localFiles, id)
		return "", false
	}
	grant.lastUsed = time.Now()
	s.localFiles[id] = grant
	return grant.path, true
}

// validToken checks the session token.
func (s *Server) validToken(r *http.Request) bool {
	query := r.URL.Query()
	return query.Get("t") == s.token || query.Get(dashTokenParam) == s.token
}

// corsHeaders sets a permissive CORS policy for the Wails webview origin.
// Wails v2 serves the frontend from http://localhost:<port> or https://wails.localhost,
// so we echo the request Origin when it looks like a local dev/webview origin,
// and fall back to a wildcard otherwise.
func corsHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	if !isLocalOrigin(origin) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type, Accept, If-Range")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges, Content-Disposition, ETag")
	w.Header().Set("Vary", "Origin")
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validToken(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	target := r.URL.Query().Get("u")
	if target == "" {
		http.Error(w, "missing url", http.StatusBadRequest)
		return
	}
	if !s.isSafeProxyURL(target) {
		http.Error(w, "url not allowed", http.StatusBadRequest)
		return
	}
	var headers map[string]string
	if h := r.URL.Query().Get("h"); h != "" {
		if b, err := base64.StdEncoding.DecodeString(h); err == nil {
			_ = json.Unmarshal(b, &headers)
		}
	}
	// strip hop-by-hop and sensitive headers
	filtered := filterProxyHeaders(headers)
	filename := r.URL.Query().Get("f")
	proxyRequest(s, w, r, target, filtered, filename)
}

// handleStream serves an opaque playback session. The optional path suffix is
// a URL-safe encoded HLS resource; it lets the browser request relative
// segments while the session keeps the provider headers private.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validToken(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	pathValue := strings.TrimPrefix(r.URL.EscapedPath(), "/stream/")
	parts := strings.Split(pathValue, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing playback session", http.StatusBadRequest)
		return
	}
	sessionID := parts[0]
	session := s.getPlaybackSession(sessionID)
	if session == nil {
		http.Error(w, "playback session expired", http.StatusGone)
		return
	}
	resourceTarget := ""
	if len(parts) > 1 {
		if parts[1] == "r" {
			if len(parts) < 3 || parts[2] == "" {
				http.Error(w, "invalid DASH playback resource", http.StatusBadRequest)
				return
			}
			escapedSuffix := "/"
			if len(parts) > 3 {
				escapedSuffix += strings.Join(parts[3:], "/")
			}
			var ok bool
			resourceTarget, ok = s.dashResourceTarget(session, parts[2], escapedSuffix, r.URL.Query())
			if !ok {
				http.Error(w, "invalid DASH playback resource", http.StatusBadRequest)
				return
			}
		} else if len(parts) == 2 && parts[1] != "" {
			var ok bool
			resourceTarget, ok = session.resource(parts[1])
			if !ok {
				http.Error(w, "invalid playback resource", http.StatusBadRequest)
				return
			}
		} else {
			http.Error(w, "invalid playback resource", http.StatusBadRequest)
			return
		}
	}
	s.proxySessionRequest(w, r, sessionID, session, resourceTarget)
}

func (s *Server) handleLocal(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validToken(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/local/")
	path, ok := s.resolveLocalFile(id)
	if !ok {
		http.Error(w, "local file grant not found", http.StatusNotFound)
		return
	}
	abs, err := canonicalPath(path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if !s.isWithinRoots(abs) {
		http.Error(w, "path not allowed", http.StatusForbidden)
		return
	}
	resolved, err := canonicalPath(abs)
	if err != nil || !s.isWithinRoots(resolved) {
		http.Error(w, "path not allowed", http.StatusForbidden)
		return
	}
	f, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info == nil {
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "is a directory", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(resolved))
	http.ServeContent(w, r, resolved, info.ModTime(), f)
}

// isSafeProxyURL validates that a proxy target is http(s) and not a private
// address to prevent SSRF.
func isSafeProxyURL(raw string) bool {
	return isSafeProxyURLWithAllow(raw, nil)
}

// filterProxyHeaders drops hop-by-hop and obviously sensitive headers the
// frontend should not be able to inject into upstream requests.
func filterProxyHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return headers
	}
	drop := map[string]bool{
		"host": true, "connection": true, "content-length": true,
		"transfer-encoding": true, "upgrade": true,
		"proxy-authorization": true, "proxy-authenticate": true,
		"te": true, "trailer": true,
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		if drop[strings.ToLower(k)] {
			continue
		}
		out[k] = v
	}
	return out
}

// proxyRequest streams a remote URL with Range passthrough. It is kept for
// non-player previews; video playback uses the opaque /stream/ session route.
func proxyRequest(s *Server, w http.ResponseWriter, r *http.Request, target string, headers map[string]string, filename string) {
	resp, err := s.doProxyRequest(r.Context(), r.Method, target, headers, nil, r.Header.Get("Range"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyProxyResponseHeaders(w, resp, filename, false)
	corsHeaders(w, r)
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, resp.Body)
	}
}

func (s *Server) proxySessionRequest(w http.ResponseWriter, r *http.Request, sessionID string, session *playbackSession, resourceTarget string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		source, err := session.resolve(ctx, attempt > 0 && resourceTarget == "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		target := source.URL
		if resourceTarget != "" {
			target = resourceTarget
		}
		if !s.isSafeProxyURL(target) {
			http.Error(w, "url not allowed", http.StatusBadRequest)
			return
		}
		resp, err := s.doProxyRequest(ctx, r.Method, target, source.Headers, source.RequestAuth, r.Header.Get("Range"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && attempt == 0 && source.Refresh != nil {
			resp.Body.Close()
			// For a stale HLS/DASH segment we cannot assume the new stream uses
			// the same signed path. Refresh the session first; retrying once still
			// covers providers that rotate only headers, and the adaptive client
			// then reloads the fresh root manifest if its old segment remains gone.
			if resourceTarget != "" {
				if _, err := session.resolve(ctx, true); err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
			}
			continue
		}
		defer resp.Body.Close()
		isHLS := isHLSPlaylist(source.StreamType, resp.Header.Get("Content-Type"), target, resourceTarget == "")
		isDASH := isDASHManifest(source.StreamType, resp.Header.Get("Content-Type"), target, resourceTarget == "")
		if (isHLS || isDASH) && r.Method != http.MethodHead && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			body, readErr := readPlaylistBody(resp)
			if readErr != nil {
				http.Error(w, readErr.Error(), http.StatusBadGateway)
				return
			}
			var rewritten []byte
			var rewriteErr error
			if isHLS {
				rewritten, rewriteErr = s.rewriteHLSPlaylist(sessionID, session, target, body)
			} else {
				rewritten, rewriteErr = s.rewriteDASHManifest(sessionID, session, target, body)
			}
			if rewriteErr != nil {
				http.Error(w, rewriteErr.Error(), http.StatusBadGateway)
				return
			}
			copyProxyResponseHeaders(w, resp, source.Filename, true)
			if isHLS {
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			} else {
				w.Header().Set("Content-Type", "application/dash+xml")
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(rewritten)))
			corsHeaders(w, r)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(rewritten)
			return
		}
		if isSRTSubtitle(source.StreamType, resp.Header.Get("Content-Type"), target) && r.Method != http.MethodHead && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			body, readErr := readPlaylistBody(resp)
			if readErr != nil {
				http.Error(w, readErr.Error(), http.StatusBadGateway)
				return
			}
			converted := srtToWebVTT(body)
			copyProxyResponseHeaders(w, resp, source.Filename, true)
			w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(converted)))
			corsHeaders(w, r)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(converted)
			return
		}
		copyProxyResponseHeaders(w, resp, source.Filename, false)
		corsHeaders(w, r)
		w.WriteHeader(resp.StatusCode)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, resp.Body)
		}
		return
	}
	http.Error(w, "upstream playback URL expired", http.StatusBadGateway)
}

func (s *Server) doProxyRequest(ctx context.Context, method, target string, headers map[string]string, requestAuth model.RequestAuthenticator, byteRange string) (*http.Response, error) {
	if err := s.validateSafeProxyURL(ctx, target); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range filterProxyHeaders(headers) {
		req.Header.Set(key, value)
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	if requestAuth != nil {
		if err := requestAuth(req); err != nil {
			return nil, err
		}
	}
	return s.proxyClient.Do(req)
}

func copyProxyResponseHeaders(w http.ResponseWriter, resp *http.Response, filename string, omitEncoding bool) {
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag", "Cache-Control", "Expires", "Content-Encoding"} {
		if omitEncoding && header == "Content-Encoding" {
			continue
		}
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	if filename != "" {
		w.Header().Set("Content-Disposition", "inline; filename=\""+sanitizeDispositionFilename(filename)+"\"")
	} else if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
}

func isHLSPlaylist(streamType, contentType, target string, rootRequest bool) bool {
	if rootRequest && strings.EqualFold(strings.TrimSpace(streamType), "hls") {
		return true
	}
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "mpegurl") || strings.Contains(strings.ToLower(target), ".m3u8")
}

func isDASHManifest(streamType, contentType, target string, rootRequest bool) bool {
	if rootRequest && strings.EqualFold(strings.TrimSpace(streamType), "dash") {
		return true
	}
	contentType = strings.ToLower(contentType)
	path := strings.ToLower(strings.SplitN(target, "?", 2)[0])
	return strings.Contains(contentType, "dash+xml") || strings.HasSuffix(path, ".mpd")
}

func isSRTSubtitle(streamType, contentType, target string) bool {
	if strings.EqualFold(strings.TrimSpace(streamType), "subtitle-srt") {
		return true
	}
	contentType = strings.ToLower(contentType)
	path := strings.ToLower(strings.SplitN(target, "?", 2)[0])
	return strings.Contains(contentType, "subrip") || strings.HasSuffix(path, ".srt")
}

func readPlaylistBody(resp *http.Response) ([]byte, error) {
	var reader io.Reader = resp.Body
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	return io.ReadAll(io.LimitReader(reader, 8<<20))
}

func (s *Server) rewriteHLSPlaylist(sessionID string, session *playbackSession, playlistURL string, body []byte) ([]byte, error) {
	base, err := url.Parse(playlistURL)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(body), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[index] = rewriteHLSURIAttributes(line, func(raw string) string {
				return s.hlsResourceURL(sessionID, session, resolveHLSReference(base, raw))
			})
			continue
		}
		lines[index] = s.hlsResourceURL(sessionID, session, resolveHLSReference(base, trimmed))
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func (s *Server) hlsResourceURL(sessionID string, session *playbackSession, target string) string {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		// HLS permits data/blob-like URI attributes (for example, identity
		// key formats). They are not network resources and must remain intact.
		return target
	}
	return s.playbackResourceURL(sessionID, session, target)
}

func resolveHLSReference(base *url.URL, raw string) string {
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}

func rewriteHLSURIAttributes(line string, replacement func(string) string) string {
	upper := strings.ToUpper(line)
	for start := 0; ; {
		index := strings.Index(upper[start:], "URI=")
		if index < 0 {
			return line
		}
		index += start + len("URI=")
		if index >= len(line) {
			return line
		}
		if line[index] == '"' {
			end := strings.Index(line[index+1:], "\"")
			if end < 0 {
				return line
			}
			end += index + 1
			line = line[:index+1] + replacement(line[index+1:end]) + line[end:]
			upper = strings.ToUpper(line)
			start = end + 1
			continue
		}
		end := len(line)
		if comma := strings.IndexByte(line[index:], ','); comma >= 0 {
			end = index + comma
		}
		line = line[:index] + replacement(line[index:end]) + line[end:]
		upper = strings.ToUpper(line)
		start = end
	}
}

// rewriteDASHManifest replaces external segment references with local /stream/
// routes. DASH requests are generated from XML templates after parsing, so the
// HLS-style one-resource-per-complete-URL approach would leave relative
// segments pointing at an invalid local path or directly at the provider.
func (s *Server) rewriteDASHManifest(sessionID string, session *playbackSession, manifestURL string, body []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)

	type elementState struct {
		name         xml.Name
		isBaseURL    bool
		isTextURL    bool
		baseResolved bool
	}
	bases := []string{manifestURL}
	elements := make([]elementState, 0, 8)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse DASH manifest: %w", err)
		}

		switch value := token.(type) {
		case xml.StartElement:
			currentBase := bases[len(bases)-1]
			for index := range value.Attr {
				if !isDASHURLAttribute(value.Name.Local, value.Attr[index].Name.Local) || strings.TrimSpace(value.Attr[index].Value) == "" {
					continue
				}
				rewritten, err := s.rewriteDASHReference(sessionID, session, currentBase, value.Attr[index].Value)
				if err != nil {
					return nil, err
				}
				value.Attr[index].Value = rewritten
			}
			if err := encoder.EncodeToken(value); err != nil {
				return nil, err
			}
			isBaseURL := strings.EqualFold(value.Name.Local, "BaseURL")
			elements = append(elements, elementState{
				name:      value.Name,
				isBaseURL: isBaseURL,
				isTextURL: isBaseURL || strings.EqualFold(value.Name.Local, "Location") || strings.EqualFold(value.Name.Local, "PatchLocation"),
			})
			bases = append(bases, currentBase)

		case xml.CharData:
			if len(elements) > 0 {
				state := &elements[len(elements)-1]
				if state.isTextURL && (!state.isBaseURL || !state.baseResolved) {
					raw := string(value)
					trimmed := strings.TrimSpace(raw)
					if trimmed != "" {
						baseIndex := len(bases) - 1
						if state.isBaseURL {
							baseIndex--
						}
						resolved, ok := resolveDASHReference(bases[baseIndex], trimmed)
						if ok {
							rewritten, err := s.dashResourceURL(sessionID, session, resolved)
							if err != nil {
								return nil, err
							}
							if state.isBaseURL {
								bases[baseIndex] = resolved
							}
							leading := raw[:len(raw)-len(strings.TrimLeft(raw, " \t\r\n"))]
							trailing := raw[len(strings.TrimRight(raw, " \t\r\n")):]
							value = xml.CharData([]byte(leading + rewritten + trailing))
						}
						state.baseResolved = true
					}
				}
			}
			if err := encoder.EncodeToken(value); err != nil {
				return nil, err
			}

		case xml.EndElement:
			if err := encoder.EncodeToken(value); err != nil {
				return nil, err
			}
			if len(elements) == 0 || len(bases) < 2 {
				return nil, fmt.Errorf("invalid DASH manifest structure")
			}
			elements = elements[:len(elements)-1]
			bases = bases[:len(bases)-1]

		default:
			if err := encoder.EncodeToken(token); err != nil {
				return nil, err
			}
		}
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func isDASHURLAttribute(element, attribute string) bool {
	attribute = strings.ToLower(attribute)
	switch attribute {
	case "media", "initialization", "index", "sourceurl", "href":
		return true
	case "value":
		return strings.EqualFold(element, "UTCTiming")
	default:
		return false
	}
}

func (s *Server) rewriteDASHReference(sessionID string, session *playbackSession, base, raw string) (string, error) {
	target, ok := resolveDASHReference(base, raw)
	if !ok {
		return raw, nil
	}
	return s.dashResourceURL(sessionID, session, target)
}

func resolveDASHReference(base, raw string) (string, bool) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	target := baseURL.ResolveReference(reference)
	if (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return "", false
	}
	return target.String(), true
}

// srtToWebVTT converts the portable SubRip form that providers commonly
// expose into the format supported by the browser <track> element. The
// conversion deliberately leaves cue text untouched and only normalizes line
// endings, cue indices and timestamp separators.
func srtToWebVTT(body []byte) []byte {
	text := strings.TrimPrefix(string(body), "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if strings.HasPrefix(strings.TrimSpace(text), "WEBVTT") {
		return []byte(text)
	}
	lines := strings.Split(text, "\n")
	var output strings.Builder
	output.WriteString("WEBVTT\n\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isSubtitleCueIndex(trimmed) && index+1 < len(lines) && strings.Contains(lines[index+1], "-->") {
			continue
		}
		if strings.Contains(line, "-->") {
			line = strings.ReplaceAll(line, ",", ".")
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	return []byte(output.String())
}

func isSubtitleCueIndex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// checkProxyRedirect validates redirect targets to prevent the proxy from
// being redirected to private addresses.
func checkProxyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	if !isSafeProxyURL(req.URL.String()) {
		return fmt.Errorf("redirect target not allowed")
	}
	return nil
}

func (s *Server) checkProxyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	if err := s.validateSafeProxyURL(req.Context(), req.URL.String()); err != nil {
		return fmt.Errorf("redirect target not allowed: %w", err)
	}
	return nil
}

func contentTypeFor(path string) string {
	if typ := mime.TypeByExtension(filepath.Ext(path)); typ != "" {
		return typ
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".mp4"), strings.HasSuffix(lower, ".m4v"):
		return "video/mp4"
	case strings.HasSuffix(lower, ".webm"):
		return "video/webm"
	case strings.HasSuffix(lower, ".mkv"):
		return "video/x-matroska"
	case strings.HasSuffix(lower, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(lower, ".m4a"), strings.HasSuffix(lower, ".aac"):
		return "audio/mp4"
	case strings.HasSuffix(lower, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(lower, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(lower, ".ogg"):
		return "audio/ogg"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(lower, ".ico"):
		return "image/x-icon"
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".log"),
		strings.HasSuffix(lower, ".js"), strings.HasSuffix(lower, ".ts"), strings.HasSuffix(lower, ".vue"),
		strings.HasSuffix(lower, ".json"), strings.HasSuffix(lower, ".go"), strings.HasSuffix(lower, ".py"),
		strings.HasSuffix(lower, ".java"), strings.HasSuffix(lower, ".c"), strings.HasSuffix(lower, ".cpp"),
		strings.HasSuffix(lower, ".h"), strings.HasSuffix(lower, ".css"), strings.HasSuffix(lower, ".html"),
		strings.HasSuffix(lower, ".xml"), strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"),
		strings.HasSuffix(lower, ".ini"), strings.HasSuffix(lower, ".sh"), strings.HasSuffix(lower, ".bat"),
		strings.HasSuffix(lower, ".srt"), strings.HasSuffix(lower, ".vtt"), strings.HasSuffix(lower, ".ass"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// Close shuts the server down.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	if s.proxyTransport != nil {
		s.proxyTransport.CloseIdleConnections()
	}
	if s.server == nil {
		return nil
	}
	return s.server.Close()
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real), nil
	}
	// The final file may not exist yet. Resolve the parent so a symlinked
	// directory cannot be used to escape the allowed root.
	parent := filepath.Dir(abs)
	if realParent, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(realParent, filepath.Base(abs)), nil
	}
	return abs, nil
}

func pathWithin(root, candidate string) bool {
	if runtime.GOOS == "windows" && strings.EqualFold(root, candidate) {
		return true
	}
	if root == candidate {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) isWithinRoots(abs string) bool {
	candidate, err := canonicalPath(abs)
	if err != nil {
		return false
	}
	s.mu.Lock()
	roots := append([]string(nil), s.roots...)
	s.mu.Unlock()
	for _, root := range roots {
		if pathWithin(root, candidate) {
			return true
		}
	}
	return false
}

func proxyHostKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func (s *Server) rememberProxyHost(raw string, allowPrivate ...bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return
	}
	privateOptIn := len(allowPrivate) > 0 && allowPrivate[0]
	if !privateOptIn && !isExplicitLocalProxyHost(u.Hostname()) {
		return
	}
	key := proxyHostKey(u)
	s.mu.Lock()
	if s.allowedProxyHosts == nil {
		s.allowedProxyHosts = make(map[string]struct{})
	}
	s.allowedProxyHosts[key] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) isAllowedProxyHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	key := proxyHostKey(u)
	s.mu.Lock()
	_, ok := s.allowedProxyHosts[key]
	s.mu.Unlock()
	return ok
}

func globalProxy(_ *http.Request) (*url.URL, error) {
	raw := strings.TrimSpace(netx.EffectiveProxyURL())
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		if err == nil {
			err = fmt.Errorf("invalid proxy URL")
		}
		return nil, err
	}
	return u, nil
}

func (s *Server) isSafeProxyURL(raw string) bool {
	return isSafeProxyURLWithAllow(raw, s.isAllowedProxyHost)
}

func (s *Server) validateSafeProxyURL(ctx context.Context, raw string) error {
	if !s.isSafeProxyURL(raw) {
		return fmt.Errorf("proxy target is not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("proxy target is invalid")
	}
	if s.isAllowedProxyHost(raw) {
		return nil
	}
	if err := validateResolvedProxyHost(ctx, u.Hostname()); err != nil {
		return err
	}
	return nil
}

func (s *Server) safeProxyDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if isExplicitLocalProxyHost(host) || s.isAllowedProxyHostHostPort(host, port) {
		return (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if isBlockedProxyIP(ip.IP) {
			lastErr = fmt.Errorf("proxy target resolved to a private address")
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("proxy target has no usable address")
	}
	return nil, lastErr
}

func (s *Server) isAllowedProxyHostHostPort(host, port string) bool {
	key := strings.ToLower(strings.TrimSpace(host))
	if port != "" {
		key = net.JoinHostPort(key, port)
	}
	s.mu.Lock()
	_, ok := s.allowedProxyHosts[key]
	s.mu.Unlock()
	return ok
}

func validateResolvedProxyHost(ctx context.Context, host string) error {
	if isExplicitLocalProxyHost(host) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedProxyIP(ip) {
			return fmt.Errorf("proxy target is a private address")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("proxy target DNS lookup failed: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("proxy target has no address")
	}
	for _, ip := range ips {
		if isBlockedProxyIP(ip.IP) {
			return fmt.Errorf("proxy target resolves to a private address")
		}
	}
	return nil
}

func isBlockedProxyIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()
}

func isExplicitLocalProxyHost(host string) bool {
	lower := strings.ToLower(strings.TrimSpace(host))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}
	if ip := net.ParseIP(lower); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func isSafeProxyURLWithAllow(raw string, allow func(string) bool) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return allow != nil && allow(raw)
		}
		return true
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".localhost") {
		return allow != nil && allow(raw)
	}
	return true
}

func isLocalOrigin(raw string) bool {
	if raw == "null" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "wails.localhost" || host == "127.0.0.1" || host == "::1"
}

// sanitizeDispositionFilename strips characters that are invalid inside a
// Content-Disposition filename quoted-string (RFC 6266). It keeps CJK and
// common punctuation but drops quotes, backslashes and control bytes.
func sanitizeDispositionFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r == '"' || r == '\\' || r < 0x20 {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if s == "" {
		return "file"
	}
	return s
}
