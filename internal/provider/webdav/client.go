// Package webdav implements a minimal RFC 4918 WebDAV client used by the
// webdav drive provider. Pure Go, no external dependency. PROPFIND responses
// are parsed namespace-agnostically with an XML token walk.
package webdav

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// Client is a WebDAV connection bound to an endpoint + credentials.
type Client struct {
	HTTP     *http.Client
	Endpoint string
	Username string
	Password string
	UA       string
	base     *url.URL
	rootPath string
	authMode webDAVAuthMode
	digest   *digestState
}

type webDAVAuthMode string

const (
	webDAVAuthAuto   webDAVAuthMode = "auto"
	webDAVAuthBasic  webDAVAuthMode = "basic"
	webDAVAuthDigest webDAVAuthMode = "digest"
	webDAVAuthBearer webDAVAuthMode = "bearer"
)

// digestStates keeps only server challenges and nonce counters in memory. It
// is keyed by endpoint and username, never stores a password, and prevents a
// new Client created for every drive operation from causing a fresh 401/530
// negotiation each time.
var digestStates sync.Map // map[string]*digestState

// New builds a client from a connection config.
func New(conn *model.ConnConfig, timeout time.Duration) (*Client, error) {
	if conn == nil || conn.Endpoint == "" {
		return nil, errors.New("webdav: missing endpoint")
	}
	rawEndpoint := strings.TrimSpace(conn.Endpoint)
	if !strings.Contains(rawEndpoint, "://") {
		rawEndpoint = "https://" + rawEndpoint
	}
	base, err := url.Parse(rawEndpoint)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("webdav: endpoint must be a valid http(s) URL")
	}
	if !strings.EqualFold(base.Scheme, "http") && !strings.EqualFold(base.Scheme, "https") {
		return nil, errors.New("webdav: endpoint scheme must be http or https")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("webdav: endpoint must not contain credentials, query, or fragment")
	}
	basePath, err := normalizeDAVPath(base.Path)
	if err != nil {
		return nil, fmt.Errorf("webdav: invalid endpoint path: %w", err)
	}
	base.Path = basePath
	base.RawPath = ""
	configuredRootPath := strings.TrimSpace(conn.RootPath)
	if configuredRootPath == "" {
		// Older Mnemo-Go builds used BasePath for both mounted providers.
		// Keep those persisted WebDAV accounts on the same visible subtree.
		configuredRootPath = conn.BasePath
	}
	rootPath, err := normalizeDAVPath(configuredRootPath)
	if err != nil {
		return nil, fmt.Errorf("webdav: invalid root path: %w", err)
	}
	authMode, err := parseWebDAVAuthMode(conn.AuthType)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	var digest *digestState
	if authMode == webDAVAuthAuto || authMode == webDAVAuthDigest {
		key := strings.ToLower(base.Scheme) + "://" + strings.ToLower(base.Host) + base.EscapedPath() + "\x00" + conn.Username
		state, _ := digestStates.LoadOrStore(key, &digestState{nonceCounts: make(map[string]uint32)})
		digest = state.(*digestState)
	}
	return &Client{
		HTTP:     netx.NewClientWithSystemProxy(timeout).HTTP,
		Endpoint: base.String(),
		Username: conn.Username,
		Password: conn.Password,
		UA:       netx.DefaultUA,
		base:     base,
		rootPath: rootPath,
		authMode: authMode,
		digest:   digest,
	}, nil
}

func (c *Client) newReq(ctx context.Context, method, href string, body io.Reader, headers map[string]string) (*http.Request, error) {
	rawURL, err := c.resolve(href)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if err := c.applyAuth(req); err != nil {
		return nil, err
	}
	return req, nil
}

func parseWebDAVAuthMode(raw string) (webDAVAuthMode, error) {
	switch webDAVAuthMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", webDAVAuthAuto:
		return webDAVAuthAuto, nil
	case webDAVAuthBasic, webDAVAuthDigest, webDAVAuthBearer:
		return webDAVAuthMode(strings.ToLower(strings.TrimSpace(raw))), nil
	default:
		return "", errors.New("webdav: auth type must be auto, basic, digest, or bearer")
	}
}

func (c *Client) applyAuth(req *http.Request) error {
	if c == nil || req == nil || (c.Username == "" && c.Password == "") {
		return nil
	}
	switch c.authMode {
	case webDAVAuthBearer:
		req.Header.Set("Authorization", "Bearer "+c.Password)
		return nil
	case webDAVAuthDigest:
		return c.applyDigestAuth(req)
	case webDAVAuthAuto:
		if c.digest != nil && c.digest.hasChallenge() {
			return c.applyDigestAuth(req)
		}
		fallthrough
	case webDAVAuthBasic:
		req.SetBasicAuth(c.Username, c.Password)
	}
	return nil
}

func (c *Client) applyDigestAuth(req *http.Request) error {
	if c.digest == nil || !c.digest.hasChallenge() {
		return nil
	}
	header, err := c.digest.authorization(req, c.Username, c.Password)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", header)
	return nil
}

// do sends a request and performs at most one Digest challenge retry. A few
// WebDAV gateways use the non-standard 530 status for the initial challenge,
// so it is accepted only when WWW-Authenticate explicitly offers Digest.
// Basic and Bearer are intentionally never retried to avoid extra requests.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	send := c.HTTP.Do
	if req.Method == http.MethodPut {
		send = func(r *http.Request) (*http.Response, error) { return netx.DoUpload(c.HTTP, r) }
	}
	resp, err := send(req)
	if err != nil || resp == nil || (c.authMode != webDAVAuthAuto && c.authMode != webDAVAuthDigest) {
		return resp, err
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != 530 {
		return resp, nil
	}
	challenge, err := digestChallengeFromHeaders(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return resp, nil
	}
	if req.Body != nil && req.GetBody == nil {
		// Retrying a one-shot upload body can corrupt data. A previous successful
		// validation/list request caches the challenge for later PUT operations.
		return resp, nil
	}
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			return resp, nil
		}
		retry.Body = body
	}
	c.digest.setChallenge(challenge)
	if err := c.applyDigestAuth(retry); err != nil {
		return resp, nil
	}
	_ = resp.Body.Close()
	return send(retry)
}

// DownloadAuth exposes a request-scoped authenticator for the transfer and
// preview engines. Digest values must be calculated for every GET/Range
// request because the nonce count is part of the response hash; returning a
// fixed Authorization header would fail or be replay-unsafe.
func (c *Client) DownloadAuth() (map[string]string, model.RequestAuthenticator, error) {
	if c == nil || (c.Username == "" && c.Password == "") {
		return nil, nil, nil
	}
	switch c.authMode {
	case webDAVAuthBearer:
		return map[string]string{"Authorization": "Bearer " + c.Password}, nil, nil
	case webDAVAuthDigest:
		if c.digest == nil || !c.digest.hasChallenge() {
			return nil, nil, errors.New("webdav: Digest challenge is unavailable; please reconnect")
		}
	case webDAVAuthAuto:
		if c.digest == nil || !c.digest.hasChallenge() {
			return map[string]string{"Authorization": basicAuthorization(c.Username, c.Password)}, nil, nil
		}
	default:
		return map[string]string{"Authorization": basicAuthorization(c.Username, c.Password)}, nil, nil
	}
	return nil, func(req *http.Request) error { return c.applyDigestAuth(req) }, nil
}

func basicAuthorization(user, password string) string {
	request := &http.Request{Header: make(http.Header)}
	request.SetBasicAuth(user, password)
	return request.Header.Get("Authorization")
}

type digestState struct {
	mu          sync.Mutex
	challenge   digestChallenge
	has         bool
	nonceCounts map[string]uint32
}

func (s *digestState) hasChallenge() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.has
}

func (s *digestState) setChallenge(challenge digestChallenge) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has || s.challenge != challenge {
		s.nonceCounts = make(map[string]uint32)
	}
	s.challenge = challenge
	s.has = true
}

func (s *digestState) authorization(req *http.Request, username, password string) (string, error) {
	if s == nil || req == nil {
		return "", errors.New("webdav: Digest authentication is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return "", errors.New("webdav: Digest challenge is unavailable")
	}
	challenge := s.challenge
	qop, err := challenge.selectedQOP()
	if err != nil {
		return "", err
	}
	hash, algorithm, session, err := digestHash(challenge.Algorithm)
	if err != nil {
		return "", err
	}
	cnonce := ""
	if qop != "" || session {
		cnonce, err = digestCNonce()
		if err != nil {
			return "", err
		}
	}
	uri := req.URL.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	if req.URL.RawQuery != "" {
		uri += "?" + req.URL.RawQuery
	}
	userValue := username
	if challenge.Userhash {
		userValue = hash(username + ":" + challenge.Realm)
	}
	ha1 := hash(username + ":" + challenge.Realm + ":" + password)
	if session {
		ha1 = hash(ha1 + ":" + challenge.Nonce + ":" + cnonce)
	}
	ha2 := hash(req.Method + ":" + uri)
	response := ""
	nc := ""
	if qop != "" {
		s.nonceCounts[challenge.Nonce]++
		nc = fmt.Sprintf("%08x", s.nonceCounts[challenge.Nonce])
		response = hash(ha1 + ":" + challenge.Nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = hash(ha1 + ":" + challenge.Nonce + ":" + ha2)
	}
	parts := []string{
		"username=" + digestQuote(userValue),
		"realm=" + digestQuote(challenge.Realm),
		"nonce=" + digestQuote(challenge.Nonce),
		"uri=" + digestQuote(uri),
		"response=" + digestQuote(response),
		"algorithm=" + algorithm,
	}
	if challenge.Opaque != "" {
		parts = append(parts, "opaque="+digestQuote(challenge.Opaque))
	}
	if challenge.Userhash {
		parts = append(parts, "userhash=true")
	}
	if qop != "" {
		parts = append(parts, "qop="+qop, "nc="+nc, "cnonce="+digestQuote(cnonce))
	}
	return "Digest " + strings.Join(parts, ", "), nil
}

type digestChallenge struct {
	Realm     string
	Nonce     string
	Opaque    string
	Algorithm string
	QOP       string
	Userhash  bool
}

func (c digestChallenge) selectedQOP() (string, error) {
	if strings.TrimSpace(c.QOP) == "" {
		return "", nil
	}
	for _, value := range strings.Split(c.QOP, ",") {
		if strings.EqualFold(strings.TrimSpace(value), "auth") {
			return "auth", nil
		}
	}
	return "", errors.New("webdav: Digest server only offers unsupported qop (requires auth)")
}

func digestHash(raw string) (func(string) string, string, bool, error) {
	algorithm := strings.ToUpper(strings.TrimSpace(raw))
	if algorithm == "" {
		algorithm = "MD5"
	}
	session := strings.HasSuffix(algorithm, "-SESS")
	base := strings.TrimSuffix(algorithm, "-SESS")
	var sum func([]byte) []byte
	switch base {
	case "MD5":
		sum = func(data []byte) []byte { value := md5.Sum(data); return value[:] }
	case "SHA-256":
		sum = func(data []byte) []byte { value := sha256.Sum256(data); return value[:] }
	case "SHA-512-256":
		sum = func(data []byte) []byte { value := sha512.Sum512_256(data); return value[:] }
	default:
		return nil, "", false, fmt.Errorf("webdav: unsupported Digest algorithm %q", raw)
	}
	return func(value string) string { return fmt.Sprintf("%x", sum([]byte(value))) }, algorithm, session, nil
}

func digestCNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buf), nil
}

func digestQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func digestChallengeFromHeaders(headers []string) (digestChallenge, error) {
	for _, header := range headers {
		for len(header) > 0 {
			index := findDigestScheme(header)
			if index < 0 {
				break
			}
			rest := header[index+len("Digest"):]
			challenge, consumed, err := parseDigestChallenge(rest)
			if err == nil {
				return challenge, nil
			}
			if consumed <= 0 {
				break
			}
			header = rest[consumed:]
		}
	}
	return digestChallenge{}, errors.New("webdav: Digest challenge was not provided")
}

func findDigestScheme(header string) int {
	for i := 0; i+len("Digest") <= len(header); i++ {
		if i > 0 && header[i-1] != ',' && header[i-1] != ' ' && header[i-1] != '\t' {
			continue
		}
		if strings.EqualFold(header[i:i+len("Digest")], "Digest") {
			end := i + len("Digest")
			if end == len(header) || header[end] == ' ' || header[end] == '\t' {
				return i
			}
		}
	}
	return -1
}

func parseDigestChallenge(raw string) (digestChallenge, int, error) {
	params := make(map[string]string)
	i := 0
	for i < len(raw) {
		for i < len(raw) && (raw[i] == ',' || raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		keyStart := i
		for i < len(raw) && ((raw[i] >= 'a' && raw[i] <= 'z') || (raw[i] >= 'A' && raw[i] <= 'Z') || raw[i] == '-') {
			i++
		}
		if keyStart == i {
			break
		}
		key := strings.ToLower(raw[keyStart:i])
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw) || raw[i] != '=' {
			// A comma may introduce the next authentication scheme (for
			// example `Basic realm=...`). Parameters always contain '='.
			if len(params) > 0 {
				break
			}
			return digestChallenge{}, i, errors.New("webdav: malformed Digest challenge")
		}
		i++
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		var value strings.Builder
		if i < len(raw) && raw[i] == '"' {
			i++
			closed := false
			for i < len(raw) {
				if raw[i] == '\\' && i+1 < len(raw) {
					i++
					value.WriteByte(raw[i])
					i++
					continue
				}
				if raw[i] == '"' {
					i++
					closed = true
					break
				}
				value.WriteByte(raw[i])
				i++
			}
			if !closed {
				return digestChallenge{}, i, errors.New("webdav: malformed Digest quoted value")
			}
		} else {
			start := i
			for i < len(raw) && raw[i] != ',' && raw[i] != ' ' && raw[i] != '\t' {
				i++
			}
			value.WriteString(raw[start:i])
		}
		params[key] = value.String()
	}
	challenge := digestChallenge{
		Realm:     params["realm"],
		Nonce:     params["nonce"],
		Opaque:    params["opaque"],
		Algorithm: params["algorithm"],
		QOP:       params["qop"],
		Userhash:  strings.EqualFold(params["userhash"], "true"),
	}
	if challenge.Realm == "" || challenge.Nonce == "" {
		return digestChallenge{}, i, errors.New("webdav: Digest challenge is missing realm or nonce")
	}
	if _, _, _, err := digestHash(challenge.Algorithm); err != nil {
		return digestChallenge{}, i, err
	}
	if _, err := challenge.selectedQOP(); err != nil {
		return digestChallenge{}, i, err
	}
	return challenge, i, nil
}

func (c *Client) resolve(href string) (string, error) {
	if c == nil || c.base == nil {
		return "", errors.New("webdav: client is not initialized")
	}
	raw := strings.TrimSpace(href)
	var resource string
	if raw != "" {
		if u, err := url.Parse(raw); err == nil && u.IsAbs() {
			if !sameOrigin(c.base, u) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return "", errors.New("webdav: resource href points outside the configured endpoint")
			}
			resource = u.Path
		} else if strings.HasPrefix(raw, "//") {
			return "", errors.New("webdav: protocol-relative resource href is not allowed")
		} else {
			resource = raw
		}
	}
	resource, err := normalizeDAVPath(resource)
	if err != nil {
		return "", err
	}
	resource = c.withEndpointPath(resource)
	target := *c.base
	target.Path = resource
	encoded, err := escapeDAVPath(resource)
	if err != nil {
		return "", err
	}
	target.RawPath = encoded
	target.RawQuery = ""
	target.Fragment = ""
	return target.String(), nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) && a.Port() == b.Port()
}

func normalizeDAVPath(raw string) (string, error) {
	if raw == "" || raw == "root" || raw == "/" {
		return "/", nil
	}
	if strings.IndexByte(raw, 0) >= 0 || strings.ContainsRune(raw, '\\') {
		return "", errors.New("webdav: invalid path")
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	trailing := strings.HasSuffix(raw, "/")
	parts := make([]string, 0, strings.Count(raw, "/"))
	for _, part := range strings.Split(raw, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", errors.New("webdav: path traversal is not allowed")
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "/", nil
	}
	clean := "/" + strings.Join(parts, "/")
	if trailing {
		clean += "/"
	}
	return clean, nil
}

// escapeDAVPath encodes each path segment independently. Encoding the whole
// path would turn separators into data; leaving it unescaped makes names such
// as "#" and "?" become URL fragments or queries.
func escapeDAVPath(raw string) (string, error) {
	normalized, err := normalizeDAVPath(raw)
	if err != nil {
		return "", err
	}
	if normalized == "/" {
		return "/", nil
	}
	trailing := strings.HasSuffix(normalized, "/")
	parts := strings.Split(strings.Trim(normalized, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	encoded := "/" + strings.Join(parts, "/")
	if trailing {
		encoded += "/"
	}
	return encoded, nil
}

func (c *Client) endpointPath() string {
	basePath := "/"
	if c != nil && c.base != nil && c.base.Path != "" {
		basePath = c.base.Path
	}
	if c == nil || c.rootPath == "" || c.rootPath == "/" {
		return basePath
	}
	combined := strings.TrimRight(basePath, "/") + "/" + strings.Trim(c.rootPath, "/")
	if strings.HasSuffix(c.rootPath, "/") {
		combined += "/"
	}
	return combined
}

func (c *Client) withEndpointPath(resource string) string {
	base := c.endpointPath()
	if resource == "" || resource == "/" {
		return base
	}
	if base == "/" {
		return resource
	}
	baseClean := strings.TrimRight(base, "/")
	resourceClean := strings.TrimRight(resource, "/")
	if resourceClean == baseClean || strings.HasPrefix(resourceClean, baseClean+"/") {
		return resource
	}
	return baseClean + resource
}

// logicalPath converts a server href to the provider's endpoint-relative id.
// WebDAV servers may return either an absolute URL or a path including the
// configured collection prefix.
func (c *Client) logicalPath(href string) (string, error) {
	raw := strings.TrimSpace(href)
	if raw == "" {
		return "/", nil
	}
	if u, err := url.Parse(raw); err == nil && u.IsAbs() {
		if !sameOrigin(c.base, u) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("webdav: resource href points outside the configured endpoint")
		}
		raw = u.EscapedPath()
	} else if strings.HasPrefix(raw, "//") {
		return "", errors.New("webdav: protocol-relative resource href is not allowed")
	}
	var err error
	raw, err = unescapeDAVPath(raw)
	if err != nil {
		return "", fmt.Errorf("webdav: invalid resource href: %w", err)
	}
	p, err := normalizeDAVPath(raw)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(c.endpointPath(), "/")
	if base != "" {
		if strings.TrimRight(p, "/") == base {
			return "/", nil
		}
		if strings.HasPrefix(p, base+"/") {
			p = strings.TrimPrefix(p, base)
		}
	}
	if p == "" {
		return "/", nil
	}
	return p, nil
}

func unescapeDAVPath(raw string) (string, error) {
	parts := strings.Split(raw, "/")
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return "", err
		}
		parts[i] = decoded
	}
	return strings.Join(parts, "/"), nil
}

// Entry is a normalized directory listing entry.
type Entry struct {
	Name     string
	Path     string
	IsDir    bool
	Size     int64
	Modified time.Time
}

// davEntry is one PROPFIND resource parsed namespace-agnostically.
type davEntry struct {
	Href  string
	Props map[string]string
}

func (e davEntry) davValue(names ...string) string {
	for _, want := range names {
		if v, ok := e.Props[want]; ok {
			return v
		}
	}
	return ""
}

func (e davEntry) davBool(names ...string) bool {
	v := strings.TrimSpace(e.davValue(names...))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "collection")
}

// parsePropfind walks a multistatus document namespace-agnostically.
func parsePropfind(data []byte) ([]davEntry, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []davEntry
	var cur *davEntry
	var inHref, inProp bool
	var hrefText, propText strings.Builder
	var propName string
	var propDepth int
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "response":
				cur = &davEntry{Props: map[string]string{}}
			case "href":
				if cur != nil && !inProp {
					inHref = true
					hrefText.Reset()
				}
			case "prop":
				inProp = true
			default:
				if inProp {
					if propName == "" {
						propName = t.Name.Local
						propDepth = 1
						propText.Reset()
					} else {
						propDepth++
						if t.Name.Local == "collection" {
							propText.WriteString("collection")
						}
					}
				}
			}
		case xml.CharData:
			if inHref {
				hrefText.Write(t)
			} else if inProp && propName != "" {
				propText.Write(t)
			}
		case xml.EndElement:
			if inHref && t.Name.Local == "href" {
				if cur != nil {
					cur.Href = strings.TrimSpace(hrefText.String())
				}
				inHref = false
				hrefText.Reset()
				continue
			}
			if inProp && propName != "" {
				if propDepth > 1 {
					propDepth--
					continue
				}
				if propDepth == 1 && t.Name.Local == propName {
					if cur != nil {
						cur.Props[propName] = strings.TrimSpace(propText.String())
					}
					propName = ""
					propDepth = 0
					propText.Reset()
					continue
				}
			}
			if t.Name.Local == "prop" {
				inProp = false
				continue
			}
			if t.Name.Local == "response" && cur != nil {
				out = append(out, *cur)
				cur = nil
			}
		}
	}
	return out, nil
}

func (c *Client) responseError(method string, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("webdav: %s failed without a response", method)
	}
	parts := make([]string, 0, 3)
	if auth := compactDiagnostic(resp.Header.Get("WWW-Authenticate")); auth != "" {
		parts = append(parts, "WWW-Authenticate: "+auth)
		authLower := strings.ToLower(auth)
		if strings.Contains(authLower, "digest") {
			if c != nil && c.digest != nil && c.digest.hasChallenge() {
				parts = append(parts, "Digest 认证已尝试，服务器仍拒绝请求；请检查用户名、密码或应用密码")
			} else {
				parts = append(parts, "服务器要求 Digest 认证，但挑战参数不受支持")
			}
		} else if !strings.Contains(authLower, "basic") && !strings.Contains(authLower, "bearer") {
			parts = append(parts, "认证方式不兼容：支持 Basic、Digest 或 Bearer")
		}
	}
	if location := compactDiagnostic(resp.Header.Get("Location")); location != "" {
		parts = append(parts, "Location: "+location)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if detail := compactDiagnostic(string(body)); detail != "" {
		parts = append(parts, detail)
	}
	for i := range parts {
		if c != nil && c.Password != "" {
			parts[i] = strings.ReplaceAll(parts[i], c.Password, "[REDACTED]")
		}
		if len(parts[i]) > 512 {
			parts[i] = parts[i][:512] + "…"
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("webdav: %s %d", method, resp.StatusCode)
	}
	return fmt.Errorf("webdav: %s %d: %s", method, resp.StatusCode, strings.Join(parts, "; "))
}

func compactDiagnostic(raw string) string { return strings.Join(strings.Fields(raw), " ") }

// List PROPFINDs a directory (depth 1) and returns its entries.
func (c *Client) List(ctx context.Context, href string) ([]Entry, error) {
	req, err := c.newReq(ctx, "PROPFIND", href, strings.NewReader(propfindAllBody), map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, c.responseError("PROPFIND", resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	entries, err := parsePropfind(body)
	if err != nil {
		return nil, fmt.Errorf("webdav: PROPFIND decode: %w", err)
	}
	base, err := c.logicalPath(href)
	if err != nil {
		return nil, err
	}
	base = strings.TrimRight(base, "/") + "/"
	var out []Entry
	for _, r := range entries {
		p, err := c.logicalPath(r.Href)
		if err != nil {
			return nil, err
		}
		if strings.TrimRight(p, "/") == strings.TrimRight(base, "/") {
			continue
		}
		isDir := r.davBool("resourcetype", "collection") || strings.HasSuffix(p, "/")
		name := path.Base(strings.TrimRight(p, "/"))
		if name == "" || name == "." || name == "/" {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(r.davValue("getcontentlength")), 10, 64)
		var mod time.Time
		if s := strings.TrimSpace(r.davValue("getlastmodified")); s != "" {
			mod, _ = time.Parse(http.TimeFormat, s)
		}
		out = append(out, Entry{Name: name, Path: p, IsDir: isDir, Size: size, Modified: mod})
	}
	return out, nil
}

// Stat PROPFINDs a single resource (depth 0).
func (c *Client) Stat(ctx context.Context, href string) (*Entry, error) {
	return c.stat(ctx, href, propfindAllBody)
}

// CheckCollection requests only the property needed for connection validation.
// allprop may trigger expensive quota or metadata computation on gateways.
func (c *Client) CheckCollection(ctx context.Context) error {
	entry, err := c.stat(ctx, "/", `<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return errors.New("webdav: 挂载路径不是目录或服务器未返回目录属性")
	}
	return nil
}

func (c *Client) stat(ctx context.Context, href, properties string) (*Entry, error) {
	req, err := c.newReq(ctx, "PROPFIND", href, strings.NewReader(properties), map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errors.New("webdav: not found")
	}
	if resp.StatusCode >= 400 {
		return nil, c.responseError("PROPFIND", resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	entries, err := parsePropfind(body)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("webdav: empty PROPFIND response")
	}
	r := entries[0]
	p, err := c.logicalPath(r.Href)
	if err != nil {
		return nil, err
	}
	isDir := r.davBool("resourcetype", "collection")
	size, _ := strconv.ParseInt(strings.TrimSpace(r.davValue("getcontentlength")), 10, 64)
	var mod time.Time
	if s := strings.TrimSpace(r.davValue("getlastmodified")); s != "" {
		mod, _ = time.Parse(http.TimeFormat, s)
	}
	return &Entry{Name: path.Base(strings.TrimRight(p, "/")), Path: p, IsDir: isDir, Size: size, Modified: mod}, nil
}

// Quota reads the optional RFC 4331 quota properties for a collection. A
// server that does not expose these properties is treated as unsupported,
// rather than as a connection failure.
func (c *Client) Quota(ctx context.Context, href string) (used, total int64, err error) {
	req, err := c.newReq(ctx, "PROPFIND", href, strings.NewReader(propfindQuotaBody), map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	if err != nil {
		return 0, 0, err
	}
	resp, err := c.do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound,
			http.StatusMethodNotAllowed, http.StatusConflict, http.StatusNotImplemented:
			return 0, 0, nil
		}
		return 0, 0, c.responseError("PROPFIND quota", resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}
	entries, err := parsePropfind(body)
	if err != nil {
		return 0, 0, fmt.Errorf("webdav: quota decode: %w", err)
	}
	if len(entries) == 0 {
		return 0, 0, nil
	}
	used, usedOK := parseQuotaValue(entries[0].davValue("quota-used-bytes"))
	available, availableOK := parseQuotaValue(entries[0].davValue("quota-available-bytes"))
	if !usedOK || !availableOK || available > math.MaxInt64-used {
		return used, 0, nil
	}
	return used, used + available, nil
}

func parseQuotaValue(raw string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return value, err == nil && value >= 0
}

// Mkcol creates a directory.
func (c *Client) Mkcol(ctx context.Context, href string) error {
	req, err := c.newReq(ctx, "MKCOL", href, nil, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 405 && resp.StatusCode != 301 {
		return fmt.Errorf("webdav: MKCOL %d", resp.StatusCode)
	}
	return nil
}

// Delete removes a resource.
func (c *Client) Delete(ctx context.Context, href string) error {
	req, err := c.newReq(ctx, "DELETE", href, nil, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		return fmt.Errorf("webdav: DELETE %d", resp.StatusCode)
	}
	return nil
}

// Move renames/moves a resource.
func (c *Client) Move(ctx context.Context, from, to string) error {
	destination, err := c.resolve(to)
	if err != nil {
		return err
	}
	req, err := c.newReq(ctx, "MOVE", from, nil, map[string]string{
		"Destination": destination,
		"Overwrite":   "T",
	})
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webdav: MOVE %d", resp.StatusCode)
	}
	return nil
}

// Copy copies a resource to a destination.
func (c *Client) Copy(ctx context.Context, from, to string) error {
	destination, err := c.resolve(to)
	if err != nil {
		return err
	}
	req, err := c.newReq(ctx, "COPY", from, nil, map[string]string{
		"Destination": destination,
		"Overwrite":   "T",
	})
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webdav: COPY %d", resp.StatusCode)
	}
	return nil
}

// Put uploads a body to href.
func (c *Client) Put(ctx context.Context, href string, body io.Reader, size int64) error {
	req, err := c.newReq(ctx, "PUT", href, body, map[string]string{
		"Content-Type": "application/octet-stream",
	})
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webdav: PUT %d", resp.StatusCode)
	}
	return nil
}

// DownloadURL returns the direct URL for a resource.
func (c *Client) DownloadURL(href string) (string, error) { return c.resolve(href) }

const propfindAllBody = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

const propfindQuotaBody = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:quota-used-bytes/><D:quota-available-bytes/></D:prop></D:propfind>`
