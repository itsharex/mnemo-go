package lanzou

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

// HTTP clients: normal follows redirects, manual returns the 3xx response
// untouched (login + share-download flows need Location / Set-Cookie).
var (
	httpClient   = &http.Client{Timeout: 60 * time.Second}
	manualClient = &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

// fetchMinInterval is the rate-limit spacing between woozooo requests
// (legacy withProviderRateLimit: concurrency 2 + min interval 300ms).
var fetchMinInterval = 300 * time.Millisecond

type throttle struct {
	mu   sync.Mutex
	last time.Time
}

func (t *throttle) wait() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	next := t.last.Add(fetchMinInterval)
	if next.After(now) {
		time.Sleep(next.Sub(now))
		now = time.Now()
	}
	t.last = now
}

var fetchThrottle = &throttle{}

// fetchResult is one raw HTTP response.
type fetchResult struct {
	text        string
	status      int
	headers     http.Header
	location    string
	bodySkipped bool
}

// isLanzouDirectDownloadResponse 识别已经开始传输的二进制下载响应。下载
// 链接探测只需要响应头，读取正文会造成用户点击下载后的无效等待。
func isLanzouDirectDownloadResponse(headers http.Header) bool {
	if strings.Contains(strings.ToLower(headers.Get("Content-Disposition")), "attachment") {
		return true
	}
	contentType := strings.ToLower(headers.Get("Content-Type"))
	if contentType == "" {
		return false
	}
	for _, marker := range []string{"text/", "json", "xml", "html", "javascript"} {
		if strings.Contains(contentType, marker) {
			return false
		}
	}
	return true
}

// fetchText performs one request through the acw_sc__v2 challenge retry loop
// (up to 3 attempts, mirroring AList). A fresh body copy is sent per attempt.
func fetchText(ctx context.Context, method, rawURL string, headers map[string]string, body []byte, cookie string, manualRedirect bool) (*fetchResult, error) {
	mergedCookie := cookie
	var last *fetchResult
	for attempt := 0; attempt < 3; attempt++ {
		h := make(map[string]string, len(headers)+1)
		for k, v := range headers {
			h[k] = v
		}
		if mergedCookie != "" {
			h["cookie"] = mergedCookie
		}
		fetchThrottle.wait()
		res, err := fetchTextRaw(ctx, method, rawURL, h, body, manualRedirect)
		if err != nil {
			return nil, err
		}
		last = res
		if acw := solveAcwScV2(res.text); acw != "" {
			mergedCookie = mergeAcwCookie(mergedCookie, acw)
			continue
		}
		break
	}
	return last, nil
}

func fetchTextRaw(ctx context.Context, method, rawURL string, headers map[string]string, body []byte, manualRedirect bool) (*fetchResult, error) {
	cl := httpClient
	if manualRedirect {
		cl = manualClient
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	result := &fetchResult{
		status:   resp.StatusCode,
		headers:  resp.Header,
		location: resp.Header.Get("Location"),
	}
	if manualRedirect && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices && isLanzouDirectDownloadResponse(resp.Header) {
		result.bodySkipped = true
		return result, nil
	}
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	result.text = string(text)
	return result, nil
}

// cred is the parsed refresh_token payload (mirrors legacy parseLanzouCred).
type cred struct {
	Type       string `json:"type"`
	Cookie     string `json:"cookie"`
	Account    string `json:"account"`
	Password   string `json:"password"`
	UID        string `json:"uid"`
	VEI        string `json:"vei"`
	UploadTier string `json:"upload_tier,omitempty"`
	BaseURL    string `json:"baseUrl"`
	ShareURL   string `json:"shareUrl"`
}

// parseLanzouCred returns nil when the refresh payload is not an object.
func parseLanzouCred(refresh string) *cred {
	if refresh == "" || !strings.HasPrefix(refresh, "{") {
		return nil
	}
	var c cred
	if err := json.Unmarshal([]byte(refresh), &c); err != nil {
		return nil
	}
	return &c
}

// sessionOf resolves the session cookie/uid/vei/baseUrl from the drive context
// (mirrors lanzouDoupload's credential resolution incl. the "cookie" quirk).
func sessionOf(c drive.Context) (cookie, uid, vei, baseURL string) {
	baseURL = LANZOU_DEFAULT.BaseURL
	if c.Token == nil {
		return "", "", "", baseURL
	}
	cookie = c.Token.AccessToken
	if cr := parseLanzouCred(c.Token.RefreshToken); cr != nil {
		if cr.BaseURL != "" {
			baseURL = cr.BaseURL
		}
		if cr.Cookie != "" && (cookie == "" || cookie == "cookie") {
			cookie = cr.Cookie
		}
		uid = cr.UID
		vei = cr.VEI
	}
	return cookie, uid, vei, baseURL
}

// douploadRaw posts a task form to doupload.php and returns the parsed JSON
// without interpreting zt.
func douploadRaw(ctx context.Context, cookie, uid, vei, baseURL string, form url.Values) (map[string]any, error) {
	if cookie == "" {
		return nil, errors.New("蓝奏未登录（需要 Cookie）")
	}
	qs := url.Values{}
	if uid != "" {
		qs.Set("uid", uid)
	}
	if vei != "" {
		qs.Set("vei", vei)
	}
	rawURL := strings.TrimSuffix(baseURL, "/") + "/doupload.php"
	if enc := qs.Encode(); enc != "" {
		rawURL += "?" + enc
	}
	res, err := fetchText(ctx, http.MethodPost, rawURL, map[string]string{
		"referer":      "https://pc.woozooo.com",
		"user-agent":   LANZOU_DEFAULT.UserAgent,
		"content-type": "application/x-www-form-urlencoded",
	}, []byte(form.Encode()), cookie, false)
	if err != nil {
		return nil, err
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(res.text), &j); err != nil {
		return nil, fmt.Errorf("蓝奏接口异常: %s", truncate(res.text, 120))
	}
	return j, nil
}

// doupload is the driver-level entry with session + zt handling: zt=9
// (cookie expired) re-logs-in once with stored credentials, then replays.
func (d *Driver) doupload(ctx context.Context, c drive.Context, form url.Values) (map[string]any, error) {
	cookie, uid, vei, baseURL := sessionOf(c)
	j, err := douploadRaw(ctx, cookie, uid, vei, baseURL, form)
	if err != nil {
		return nil, err
	}
	zt := numOf(j["zt"])
	if zt == 9 {
		if newCookie, nu, nv, lerr := d.reloginAccount(ctx, c, baseURL); lerr == nil {
			if j2, err2 := douploadRaw(ctx, newCookie, nu, nv, baseURL, form); err2 == nil {
				zt2 := numOf(j2["zt"])
				if zt2 != 9 {
					if zt2 != 1 && zt2 != 2 && zt2 != 4 {
						return nil, errors.New(infOf(j2))
					}
					return j2, nil
				}
			}
		}
		return nil, errors.New("蓝奏 Cookie 已过期，请重新登录")
	}
	if zt != 1 && zt != 2 && zt != 4 {
		return nil, errors.New(infOf(j))
	}
	return j, nil
}

func tokenRefresh(c drive.Context) string {
	if c.Token == nil {
		return ""
	}
	return c.Token.RefreshToken
}

func infOf(j map[string]any) string {
	msg := strOf(j["inf"])
	if msg == "" {
		msg = strOf(j["info"])
	}
	if msg == "" {
		msg = "蓝奏操作失败"
	}
	return msg
}

// lanzouAccountLogin performs the mlogin.php password login and returns the
// Cookie request-header value collected from Set-Cookie lines.
func lanzouAccountLogin(ctx context.Context, account, password string) (string, error) {
	form := url.Values{}
	form.Set("task", "3")
	form.Set("uid", account)
	form.Set("pwd", password)
	form.Set("setSessionId", "")
	form.Set("setSig", "")
	form.Set("setScene", "")
	form.Set("setTocen", "")
	form.Set("formhash", "")
	res, err := fetchText(ctx, http.MethodPost, "https://up.woozooo.com/mlogin.php", map[string]string{
		"content-type": "application/x-www-form-urlencoded",
		"user-agent":   LANZOU_DEFAULT.UserAgent,
		"referer":      "https://pc.woozooo.com",
	}, []byte(form.Encode()), "", true)
	if err != nil {
		return "", err
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(res.text), &j); err != nil {
		return "", errors.New("蓝奏登录响应异常")
	}
	if numOf(j["zt"]) != 1 {
		return "", fmt.Errorf("蓝奏登录失败: %s", truncate(res.text, 200))
	}
	cookie := relaySetCookiesToCookieHeader(res.headers)
	if cookie == "" {
		return "", errors.New("蓝奏登录未返回 Cookie")
	}
	return cookie, nil
}

// reloginAccount refreshes an account-backed cookie after the provider returns
// zt=9 and updates the in-memory token so drive.ops can persist it.
func (d *Driver) reloginAccount(ctx context.Context, c drive.Context, baseURL string) (cookie, uid, vei string, err error) {
	cr := parseLanzouCred(tokenRefresh(c))
	if cr == nil || cr.Type != "account" || cr.Account == "" || cr.Password == "" {
		return "", "", "", errors.New("蓝奏 Cookie 已过期，请重新登录")
	}
	cookie, err = lanzouAccountLogin(ctx, cr.Account, cr.Password)
	if err != nil {
		return "", "", "", err
	}
	uid, vei = lanzouGetVeiAndUid(ctx, cookie, baseURL)
	if uid == "" {
		uid = cr.UID
	}
	if vei == "" {
		vei = cr.VEI
	}
	cr.Cookie = cookie
	cr.UID = uid
	cr.VEI = vei
	applyLanzouSession(c.Token, cr, cookie, uid, vei)
	return cookie, uid, vei, nil
}

func applyLanzouSession(token *model.TokenInfo, cr *cred, cookie, uid, vei string) {
	if token == nil {
		return
	}
	token.AccessToken = cookie
	if uid != "" {
		token.UserID = model.BuildUserID(model.ProviderLanzou, uid)
		token.ProviderAccountID = uid
		token.DefaultDriveID = model.BuildDriveID(model.ProviderLanzou, uid)
	}
	if vei != "" {
		token.DeviceID = vei
	}
	token.RefreshToken = mustJSON(cr)
}

// relaySetCookiesToCookieHeader flattens Set-Cookie lines into a Cookie
// header value (deduped by name, mirroring getRelayCookieHeader).
func relaySetCookiesToCookieHeader(h http.Header) string {
	seen := map[string]string{}
	for _, line := range h.Values("Set-Cookie") {
		pair := strings.SplitN(line, ";", 2)[0]
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			continue
		}
		seen[pair[:eq]] = pair
	}
	parts := make([]string, 0, len(seen))
	for _, v := range seen {
		parts = append(parts, v)
	}
	return strings.Join(parts, "; ")
}

var uidRe = regexp.MustCompile(`uid=([^'"&;]+)`)
var veiRe = regexp.MustCompile(`vei['"]?\s*[:=]\s*['"]([a-zA-Z0-9]+)`)

// lanzouGetVeiAndUid resolves the account uid/vei from mydisk.php (with a
// task-19 ajax fallback when the uid is missing).
func lanzouGetVeiAndUid(ctx context.Context, cookie, baseURL string) (uid, vei string) {
	res, err := fetchText(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/mydisk.php?item=files&action=index", map[string]string{
		"referer":    "https://pc.woozooo.com",
		"user-agent": LANZOU_DEFAULT.UserAgent,
	}, nil, cookie, false)
	if err != nil {
		return "", ""
	}
	html := res.text
	if m := uidRe.FindStringSubmatch(html); len(m) > 1 {
		uid = m[1]
	}
	if p, err := htmlJsonToMap(html); err == nil {
		vei = p["vei"]
	}
	if vei == "" {
		if m := veiRe.FindStringSubmatch(html); len(m) > 1 {
			vei = m[1]
		}
	}
	if uid == "" {
		// fallback: task 19 ajax
		if j, err := douploadRaw(ctx, cookie, "", "", baseURL, url.Values{"task": {"19"}}); err == nil {
			if text := mapVal(j, "text"); text != nil {
				if u := strOf(text["uid"]); u != "" {
					uid = u
				}
				if v := strOf(text["vei"]); v != "" {
					vei = v
				}
			} else if info := mapVal(j, "info"); info != nil {
				if u := strOf(info["uid"]); u != "" {
					uid = u
				}
				if v := strOf(info["vei"]); v != "" {
					vei = v
				}
			}
		}
	}
	return uid, vei
}

// firstOf returns the first non-empty string value among keys.
func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && strOf(v) != "" {
			return v
		}
	}
	return ""
}

// mapVal returns a nested object under key (nil when absent).
func mapVal(v any, key string) map[string]any {
	if m, ok := v.(map[string]any); ok {
		if child, ok := m[key].(map[string]any); ok {
			return child
		}
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
