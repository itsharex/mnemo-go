package pan189

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// ParseSession decodes a persisted 189 session from the raw JSON payload.
func ParseSession(raw string) *Session {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var s Session
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil
	}
	if s.SessionKey != "" && s.SessionSecret != "" {
		return &s
	}
	return nil
}

// sessionOf loads the session from the account token. AccessToken holds the
// session key (legacy keeps them in sync); RefreshToken holds the full JSON
// session including username/password for silent re-login.
func sessionOf(tok *model.TokenInfo) (*Session, error) {
	if tok == nil || tok.AccessToken == "" {
		return nil, drive.AuthExpired("天翼云盘未登录")
	}
	s := ParseSession(tok.RefreshToken)
	if s == nil {
		return nil, drive.AuthExpired("天翼云盘会话缺失，请重新登录")
	}
	if tok.AccessToken != "" && tok.AccessToken != s.SessionKey {
		s.SessionKey = tok.AccessToken
	}
	return s, nil
}

// saveSession writes the session back into the token so later calls and
// RefreshAccount see the refreshed credentials.
func saveSession(tok *model.TokenInfo, s *Session) {
	if tok == nil {
		return
	}
	tok.AccessToken = s.SessionKey
	if b, err := json.Marshal(s); err == nil {
		tok.RefreshToken = string(b)
	}
}

// cloudInfo returns the legacy mounted cloud kind. New operations should use
// cloudInfoForID so both virtual roots can coexist in one account.
func cloudInfo(s *Session) (isFamily bool, familyID string) {
	if s == nil || s.CloudType != "family" {
		return false, ""
	}
	return true, s.FamilyID
}

func cloudInfoForID(s *Session, fileID string) (isFamily bool, familyID string) {
	space, _ := pan189SpaceID(fileID)
	if space != spaceFamily {
		return false, ""
	}
	if s == nil {
		return true, ""
	}
	return true, s.FamilyID
}

// randomRequestID mirrors the legacy crypto-random request id header.
func randomRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "id-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	return hex.EncodeToString(b)
}

// rateLimiter serialises 189 requests: low concurrency + min interval, to
// avoid triggering wind-control on list/upload (mirrors legacy limiter).
type rateLimiter struct {
	mu          sync.Mutex
	last        time.Time
	concurrency chan struct{}
	interval    time.Duration
}

func newRateLimiter(concurrency int, interval time.Duration) *rateLimiter {
	return &rateLimiter{
		concurrency: make(chan struct{}, concurrency),
		interval:    interval,
	}
}

func (r *rateLimiter) run(ctx context.Context, fn func() error) error {
	select {
	case r.concurrency <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.concurrency }()
	r.mu.Lock()
	wait := r.interval - time.Since(r.last)
	if wait > 0 {
		r.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
		r.mu.Lock()
	}
	r.last = time.Now()
	r.mu.Unlock()
	return fn()
}

var pan189Limiter = newRateLimiter(2, 280*time.Millisecond)

// Every drive operation receives a cloned token. A Session/Open-token rotation
// must therefore be serialized per account and persisted before the next
// operation reloads it; otherwise two startup requests can independently use
// the same old refresh token and one may wrongly expire the account.
var pan189SessionLocks = struct {
	sync.Mutex
	byAccount map[string]*sync.Mutex
}{byAccount: make(map[string]*sync.Mutex)}

func pan189SessionLock(c drive.Context) *sync.Mutex {
	key := strings.TrimSpace(c.UserID)
	if key == "" {
		key = strings.TrimSpace(c.DriveID)
	}
	if key == "" {
		key = "__unknown__"
	}
	pan189SessionLocks.Lock()
	defer pan189SessionLocks.Unlock()
	lock := pan189SessionLocks.byAccount[key]
	if lock == nil {
		lock = &sync.Mutex{}
		pan189SessionLocks.byAccount[key] = lock
	}
	return lock
}

// reqOptions carries one 189 API request.
type reqOptions struct {
	method  string            // GET/POST (default GET)
	params  map[string]string // encrypted into &params= (signed)
	query   map[string]string // plain query params (unsigned)
	form    map[string]string // urlencoded body
	body    any               // json body
	headers map[string]string
	// family forces personal/family signing; nil follows session.cloudType.
	family *bool
	isXML  bool // response is XML; extract <id> (秒传 commit)
}

type rawResponse struct {
	needRefresh bool
	json        json.RawMessage
	text        string
}

func strVal(m map[string]json.RawMessage, key string) string {
	if v, ok := m[key]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
		var n json.Number
		if string(v) != "null" && json.Unmarshal(v, &n) == nil {
			return n.String()
		}
	}
	return ""
}

// request performs one signed 189 request, transparently refreshing the
// session on InvalidSessionKey / userSessionBO is null and retrying once.
func (d *Driver) request(ctx context.Context, c drive.Context, rawURL string, o reqOptions) (json.RawMessage, error) {
	lock := pan189SessionLock(c)
	lock.Lock()
	defer lock.Unlock()
	if err := drive.ReloadToken(c); err != nil {
		return nil, err
	}
	var out json.RawMessage
	err := pan189Limiter.run(ctx, func() error {
		sess, err := sessionOf(c.Token)
		if err != nil {
			return err
		}
		res, err := d.doOnce(ctx, c.Token, sess, rawURL, o)
		if err != nil {
			return err
		}
		if res.needRefresh {
			next, err := d.refreshSession(ctx, c.Token, sess)
			if err != nil {
				return err
			}
			saveSession(c.Token, next)
			if err := drive.PersistToken(c); err != nil {
				return err
			}
			res, err = d.doOnce(ctx, c.Token, next, rawURL, o)
			if err != nil {
				return err
			}
			if res.needRefresh {
				return drive.AuthExpired("天翼云盘 Session 已失效，请重新登录")
			}
		}
		out = res.json
		return nil
	})
	return out, err
}

// doOnce builds the signed request and decodes one response.
func (d *Driver) doOnce(ctx context.Context, tok *model.TokenInfo, sess *Session, rawURL string, o reqOptions) (*rawResponse, error) {
	useFamily := sess.CloudType == "family"
	if o.family != nil {
		useFamily = *o.family
	}
	signKey, signSecret := sess.SessionKey, sess.SessionSecret
	if useFamily {
		if sess.FamilySessionKey == "" || sess.FamilySessionSecret == "" {
			return nil, errors.New("189 家庭云会话缺失，请重新登录")
		}
		signKey, signSecret = sess.FamilySessionKey, sess.FamilySessionSecret
	}
	if signKey == "" || signSecret == "" {
		return nil, errors.New("189 Session 不完整")
	}
	paramsData, err := encryptParams(o.params, signSecret)
	if err != nil {
		return nil, err
	}
	date := getHTTPDateStr()
	method := strings.ToUpper(o.method)
	if method == "" {
		method = http.MethodGet
	}
	headers := map[string]string{
		"Accept":       "application/json;charset=UTF-8",
		"Referer":      webURL,
		"User-Agent":   ua189,
		"Date":         date,
		"SessionKey":   signKey,
		"X-Request-ID": randomRequestID(),
		"Signature":    signatureOfHmac(signSecret, signKey, method, rawURL, date, paramsData),
	}
	for k, v := range o.headers {
		headers[k] = v
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range clientSuffix() {
		q.Set(k, v)
	}
	if paramsData != "" {
		q.Set("params", paramsData)
	}
	for k, v := range o.query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	var body io.Reader
	if o.form != nil {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
		form := url.Values{}
		for k, v := range o.form {
			form.Set(k, v)
		}
		body = strings.NewReader(form.Encode())
	} else if o.body != nil {
		headers["Content-Type"] = "application/json;charset=UTF-8"
		if b, err := json.Marshal(o.body); err == nil {
			body = strings.NewReader(string(b))
		}
	}

	hc := netx.NewClient(60 * time.Second)
	resp, err := hc.Do(ctx, method, u.String(), headers, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	bodyText := string(text)
	// Session failures can use HTTP 400/401 as well as a 200 business error.
	// Check them before the generic HTTP error branch.
	if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests &&
		(strings.Contains(bodyText, "userSessionBO is null") || strings.Contains(bodyText, "InvalidSessionKey")) {
		return &rawResponse{needRefresh: true, text: bodyText}, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncateStr(bodyText, 300))
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(text, &parsed); err == nil {
		code := strVal(parsed, "errorCode")
		resCode, _ := parsed["res_code"]
		hasErr := code != ""
		if !hasErr && resCode != nil {
			var rcS string
			var rcN int
			if json.Unmarshal(resCode, &rcS) == nil {
				hasErr = rcS != "0"
			} else if json.Unmarshal(resCode, &rcN) == nil {
				hasErr = rcN != 0
			}
		}
		if hasErr {
			msg := firstNonEmpty(strVal(parsed, "errorMsg"), strVal(parsed, "res_message"),
				strVal(parsed, "message"), strVal(parsed, "msg"), code)
			if strings.Contains(code, "InvalidSessionKey") || strings.Contains(msg, "InvalidSessionKey") {
				return &rawResponse{needRefresh: true, text: bodyText}, nil
			}
			if strings.Contains(msg, "验证码") || strings.Contains(msg, "频率") || strings.Contains(msg, "频繁") ||
				strings.Contains(msg, "限流") || strings.Contains(msg, "风控") {
				_ = msg
			}
			return nil, errors.New(msg)
		}
		return &rawResponse{needRefresh: false, json: json.RawMessage(text)}, nil
	}
	if o.isXML {
		idMatch := xmlTag(bodyText, "id")
		if idMatch != "" {
			return &rawResponse{needRefresh: false, json: json.RawMessage(`{"id":"` + jsonEscape(idMatch) + `"}`)}, nil
		}
		return nil, errors.New(truncateStr(bodyText, 160))
	}
	return nil, errors.New(truncateStr(bodyText, 160))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func xmlTag(text, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	i := strings.Index(text, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(text[i+len(open):], close)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(text[i+len(open) : i+len(open)+j])
}

func jsonEscape(s string) string {
	if b, err := json.Marshal(s); err == nil {
		return string(b[1 : len(b)-1])
	}
	return s
}

// refreshSession renews the session key/secret via getSessionForPC, falling
// back to a silent re-login when the open token is invalid or credentials are
// stored (mirrors legacy refreshPan189Session).
func (d *Driver) refreshSession(ctx context.Context, tok *model.TokenInfo, sess *Session) (*Session, error) {
	return d.refreshSessionOnce(ctx, tok, sess, true)
}

func (d *Driver) refreshSessionOnce(ctx context.Context, tok *model.TokenInfo, sess *Session, allowRefresh bool) (*Session, error) {
	if sess == nil {
		return nil, drive.AuthExpired("天翼云盘 Session 缺失，请重新登录")
	}
	relogin := func() (*Session, error) {
		if sess.Username == "" || sess.Password == "" {
			return nil, errors.New("无法刷新 189 Session")
		}
		next, err := loginWithCreds(ctx, sess.Username, sess.Password, "")
		if err != nil {
			return nil, err
		}
		next.CloudType = sess.CloudType
		next.FamilyID = sess.FamilyID
		next.FamilyName = sess.FamilyName
		return next, nil
	}
	if sess.AccessToken == "" {
		return relogin()
	}
	u, _ := url.Parse(apiURL + "/getSessionForPC.action")
	q := u.Query()
	for k, v := range clientSuffix() {
		q.Set(k, v)
	}
	q.Set("appId", appID)
	q.Set("accessToken", sess.AccessToken)
	u.RawQuery = q.Encode()

	status, body, err := pan189SessionResponse(ctx, u.String(), map[string]string{
		"Accept":       "application/json",
		"User-Agent":   ua189,
		"X-Request-ID": randomRequestID(),
	})
	if err != nil {
		return nil, err
	}
	var j map[string]json.RawMessage
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, fmt.Errorf("刷新 189 Session 失败（HTTP %d，响应不是有效 JSON）", status)
	}
	code := strVal(j, "errorCode")
	resCode := strVal(j, "res_code")
	if code == "UserInvalidOpenToken" || resCode == "UserInvalidOpenToken" {
		if allowRefresh && sess.RefreshToken != "" {
			next, refreshErr := refreshPan189OpenToken(ctx, sess)
			if refreshErr == nil {
				// Token rotation may succeed before session exchange fails. Persist
				// the new open tokens while retaining the previous API session.
				if tok != nil {
					saveSession(tok, next)
				}
				return d.refreshSessionOnce(ctx, tok, next, false)
			}
			if sess.Username == "" || sess.Password == "" {
				return nil, refreshErr
			}
		}
		if sess.Username != "" && sess.Password != "" {
			return relogin()
		}
		return nil, drive.AuthExpired("天翼云盘 Session 已失效，请重新登录")
	}
	if status < 200 || status >= 300 || (code != "" && code != "0") || (resCode != "" && resCode != "0") {
		return nil, pan189SessionError(status, j)
	}
	sKey := strVal(j, "sessionKey")
	sSecret := strVal(j, "sessionSecret")
	if sKey != "" && sSecret != "" {
		next := *sess
		next.SessionKey = sKey
		next.SessionSecret = sSecret
		if fs, _ := j["familySessionKey"]; fs != nil {
			next.FamilySessionKey = strVal(j, "familySessionKey")
		}
		if fs, _ := j["familySessionSecret"]; fs != nil {
			next.FamilySessionSecret = strVal(j, "familySessionSecret")
		}
		return &next, nil
	}
	if sess.Username != "" && sess.Password != "" {
		return relogin()
	}
	return nil, pan189SessionError(status, j)
}

const pan189SessionRetryDelay = 300 * time.Millisecond

// Only the idempotent session GET is retried, at most once. Token rotation and
// interactive login are never replayed blindly after a transport failure.
func pan189SessionResponse(ctx context.Context, rawURL string, headers map[string]string) (int, []byte, error) {
	hc := netx.NewClient(60 * time.Second)
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := hc.Do(ctx, http.MethodGet, rawURL, headers, nil)
		status := 0
		var body []byte
		if err == nil {
			status = resp.StatusCode
			body, err = io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		retryable := err != nil || status == http.StatusRequestTimeout || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
		if !retryable || attempt == 1 {
			if err != nil {
				return status, nil, errors.New("刷新 189 Session 网络请求失败，请稍后重试")
			}
			return status, body, nil
		}
		timer := time.NewTimer(pan189SessionRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return 0, nil, ctx.Err()
		}
	}
	return 0, nil, errors.New("刷新 189 Session 失败")
}

func pan189SessionError(status int, fields map[string]json.RawMessage) error {
	code := firstNonEmpty(strVal(fields, "errorCode"), strVal(fields, "res_code"), "unknown")
	message := firstNonEmpty(strVal(fields, "errorMsg"), strVal(fields, "res_message"), strVal(fields, "message"), strVal(fields, "msg"), "未返回有效会话")
	return fmt.Errorf("刷新 189 Session 失败（HTTP %d，code=%s）：%s", status, truncateStr(code, 80), truncateStr(message, 160))
}

func refreshPan189OpenToken(ctx context.Context, sess *Session) (*Session, error) {
	form := url.Values{"clientId": {appID}, "refreshToken": {sess.RefreshToken}, "grantType": {"refresh_token"}, "format": {"json"}}
	hc := netx.NewClient(60 * time.Second)
	resp, err := hc.Do(ctx, http.MethodPost, "https://open.e.189.cn/api/oauth2/refreshToken.do", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json", "User-Agent": ua189,
	}, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	var j map[string]json.RawMessage
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, errors.New("189 令牌续期响应无效，请重新登录")
	}
	access := strVal(j, "accessToken")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || access == "" {
		detail := strings.ToLower(string(body))
		if strings.Contains(detail, "invalid_grant") || strings.Contains(detail, "invalid_token") ||
			strings.Contains(detail, "refresh") && (strings.Contains(detail, "expired") || strings.Contains(detail, "invalid")) {
			return nil, drive.AuthExpired("天翼云盘授权已过期，请重新登录")
		}
		return nil, errors.New("189 令牌续期失败，请重新登录")
	}
	next := *sess
	next.AccessToken = access
	if refresh := strVal(j, "refreshToken"); refresh != "" {
		next.RefreshToken = refresh
	}
	return &next, nil
}

// getFamilyList resolves the family cloud list for a freshly logged-in session
// (the token is not persisted yet, so the raw session is passed directly).
func getFamilyList(ctx context.Context, sess *Session) ([]struct {
	FamilyID   string
	RemarkName string
}, error) {
	req := reqOptions{method: "GET", family: boolPtr(false)}
	res, err := doOnceRaw(ctx, sess, apiURL+"/family/manage/getFamilyList.action", req)
	if err != nil {
		return nil, err
	}
	var j map[string]json.RawMessage
	if err := json.Unmarshal(res, &j); err != nil {
		return nil, err
	}
	var list []struct {
		FamilyID   string
		RemarkName string
	}
	if raw, ok := j["familyInfoResp"]; ok {
		var rawList []json.RawMessage
		if err := json.Unmarshal(raw, &rawList); err != nil {
			return nil, err
		}
		for _, item := range rawList {
			var e struct {
				FamilyID   json.RawMessage `json:"familyId"`
				RemarkName string          `json:"remarkName"`
			}
			_ = json.Unmarshal(item, &e)
			list = append(list, struct {
				FamilyID   string
				RemarkName string
			}{FamilyID: rawIDString(e.FamilyID), RemarkName: e.RemarkName})
		}
	}
	return list, nil
}

// rawIDString coerces a JSON id field that may be a number or string.
func rawIDString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return strings.Trim(string(raw), `"`)
}

func boolPtr(b bool) *bool { return &b }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// doOnceRaw is a bare signed request without session refresh (used before the
// session is persisted, e.g. family list during login).
func doOnceRaw(ctx context.Context, sess *Session, rawURL string, o reqOptions) (json.RawMessage, error) {
	d := &Driver{}
	res, err := d.doOnce(ctx, &model.TokenInfo{AccessToken: sess.SessionKey, RefreshToken: mustJSON(sess)}, sess, rawURL, o)
	if err != nil {
		return nil, err
	}
	return res.json, nil
}
