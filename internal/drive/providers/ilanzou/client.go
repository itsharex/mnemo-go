package ilanzou

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

var (
	httpClient   = &http.Client{Timeout: 60 * time.Second}
	manualClient = &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

// fetchMinInterval is the rate-limit spacing between ilanzou API requests
// (legacy withProviderRateLimit: concurrency 2 + min interval 260ms).
var fetchMinInterval = 260 * time.Millisecond

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

// cred is the parsed refresh_token payload (mirrors legacy parseCred).
type cred struct {
	Username string `json:"username"`
	Password string `json:"password"`
	UUID     string `json:"uuid"`
	Token    string `json:"token"`
	UserID   string `json:"userId"`
	Account  string `json:"account"`
}

func parseCred(refresh string) *cred {
	if refresh == "" || !strings.HasPrefix(refresh, "{") {
		return nil
	}
	var c cred
	if err := json.Unmarshal([]byte(refresh), &c); err != nil {
		return nil
	}
	return &c
}

// loginResult is the outcome of ilanzouLogin.
type loginResult struct {
	token     string
	uuid      string
	userId    string
	account   string
	totalSize int64
	usedSize  int64
	hasQuota  bool
}

// requestOptions controls one ilanzouRequest call.
type requestOptions struct {
	method      string
	body        any
	query       map[string]string
	unproved    bool // explicit proved:false (default is proved)
	accessToken string
	uuid        string
}

// ilanzouRequest is the driver-level API entry (port of ilanzouRequestRaw):
// it signs the query with the AES timestamp token, and on code -1/-2 (or a
// missing token) re-logs-in once with stored credentials. Returns the parsed
// JSON plus a non-nil loginResult when a re-login happened (so RefreshAccount
// can persist the fresh session).
func (d *Driver) request(ctx context.Context, c drive.Context, pathName string, opts requestOptions) (map[string]any, *loginResult, error) {
	proved := !opts.unproved
	token := opts.accessToken
	uuid := opts.uuid
	var cr *cred
	if c.Token != nil {
		token = firstNonEmpty(token, c.Token.AccessToken)
		cr = parseCred(c.Token.RefreshToken)
		if cr != nil && uuid == "" {
			uuid = cr.UUID
		}
	}
	if uuid == "" {
		uuid = newDeviceUuid()
	}

	doOnce := func(appToken string) (map[string]any, error) {
		_, tsEnc, err := getTimestampToken(ILANZOU_CONF.Secret)
		if err != nil {
			return nil, err
		}
		params := signParams(uuid, tsEnc)
		if proved && appToken != "" {
			params.Set("appToken", appToken)
		}
		for k, v := range opts.query {
			params.Set(k, v)
		}
		prefix := ILANZOU_CONF.Unproved
		if proved {
			prefix = ILANZOU_CONF.Proved
		}
		rawURL := ILANZOU_CONF.Base + "/" + prefix + pathName + "?" + encodeILanzouParams(params)
		method := strings.ToUpper(opts.method)
		if method == "" {
			method = http.MethodGet
		}
		return ilanzouJSON(ctx, method, rawURL, opts.body)
	}

	j, err := doOnce(token)
	if err != nil {
		return nil, nil, err
	}
	code := numOf(j["code"])
	if proved && (code == -1 || code == -2 || token == "") && cr != nil && cr.Username != "" && cr.Password != "" {
		login, lerr := ilanzouLogin(ctx, cr.Username, cr.Password, cr.UUID)
		if lerr != nil {
			return nil, nil, fmt.Errorf("优享版蓝奏云自动登录失败: %w", lerr)
		}
		token = login.token
		uuid = login.uuid
		if j2, err2 := doOnce(token); err2 == nil {
			j = j2
		} else {
			return nil, nil, err2
		}
		if numOf(j["code"]) == 200 {
			applyLoginSession(c.Token, login)
			return j, login, nil
		}
	}
	if numOf(j["code"]) != 200 {
		msg := strOf(j["msg"])
		if msg == "" {
			msg = "优享版蓝奏云请求失败"
		}
		if proved && (numOf(j["code"]) == -1 || numOf(j["code"]) == -2 || token == "") {
			return nil, nil, drive.AuthExpired("优享版蓝奏云登录已失效，请重新登录")
		}
		return nil, nil, fmt.Errorf("%v: %s", j["code"], msg)
	}
	return j, nil, nil
}

// signParams builds the fixed device/timestamp query parameters.
func signParams(uuid, tsEnc string) url.Values {
	params := url.Values{}
	params.Set("uuid", uuid)
	params.Set("devType", "6")
	params.Set("devCode", uuid)
	params.Set("devModel", "chrome")
	params.Set("devVersion", ILANZOU_CONF.DevVersion)
	params.Set("appVersion", "")
	params.Set("timestamp", tsEnc)
	params.Set("extra", "2")
	return params
}

// ilanzouJSON performs one signed request and decodes the JSON document.
func ilanzouJSON(ctx context.Context, method, rawURL string, body any) (map[string]any, error) {
	fetchThrottle.wait()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", ILANZOU_CONF.Site)
	req.Header.Set("Referer", ILANZOU_CONF.Site+"/")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Content-Type", "application/json") // legacy sends it on every request
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(text), 200))
	}
	var j map[string]any
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&j); err != nil {
		return nil, fmt.Errorf("ilanzou 响应异常: %s", truncate(string(text), 200))
	}
	return j, nil
}

// fetchILanzouUuid prefers the server-assigned uuid (AList Init).
func fetchILanzouUuid(ctx context.Context, seedUuid string) string {
	_, tsEnc, err := getTimestampToken(ILANZOU_CONF.Secret)
	if err != nil {
		return seedUuid
	}
	params := signParams(seedUuid, tsEnc)
	rawURL := ILANZOU_CONF.Base + "/" + ILANZOU_CONF.Unproved + "/getUuid?" + params.Encode()
	j, err := ilanzouJSON(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return seedUuid
	}
	remote := strOf(j["uuid"])
	if remote == "" {
		if data := mapVal(j, "data"); data != nil {
			remote = strOf(data["uuid"])
		}
	}
	if remote != "" {
		return remote
	}
	return seedUuid
}

// ilanzouLogin performs /unproved/login then maps the account id via
// /proved/user/account/map (AList Init).
func ilanzouLogin(ctx context.Context, username, password, uuid string) (*loginResult, error) {
	deviceUuid := uuid
	if deviceUuid == "" {
		deviceUuid = newDeviceUuid()
		deviceUuid = fetchILanzouUuid(ctx, deviceUuid)
	}
	_, tsEnc, err := getTimestampToken(ILANZOU_CONF.Secret)
	if err != nil {
		return nil, err
	}
	params := signParams(deviceUuid, tsEnc)
	rawURL := ILANZOU_CONF.Base + "/" + ILANZOU_CONF.Unproved + "/login?" + params.Encode()
	j, err := ilanzouJSON(ctx, http.MethodPost, rawURL, map[string]any{
		"loginName": username,
		"loginPwd":  password,
	})
	if err != nil {
		return nil, err
	}
	if numOf(j["code"]) != 200 {
		msg := strOf(j["msg"])
		if msg == "" {
			msg = "优享版蓝奏云登录失败"
		}
		return nil, errors.New(msg)
	}
	var token string
	if data := mapVal(j, "data"); data != nil {
		token = strOf(data["appToken"])
	}
	if token == "" {
		return nil, errors.New("优享版蓝奏云未返回 token")
	}

	mapParams := signParams(deviceUuid, tsEnc)
	mapParams.Set("appToken", token)
	mapURL := ILANZOU_CONF.Base + "/" + ILANZOU_CONF.Proved + "/user/account/map?" + encodeILanzouParams(mapParams)
	mapJSON, err := ilanzouJSON(ctx, http.MethodGet, mapURL, nil)
	if err != nil {
		return nil, err
	}
	accountMap := ilanzouAccountMap(mapJSON)
	userId, account := "", ""
	if mm := accountMap; mm != nil {
		userId = strOf(mm["userId"])
		account = strOf(mm["account"])
	}
	userId = firstNonEmpty(userId, username)
	account = firstNonEmpty(account, username)
	totalSize, usedSize, hasQuota := ilanzouQuota(mapJSON)
	return &loginResult{token: token, uuid: deviceUuid, userId: userId, account: account, totalSize: totalSize, usedSize: usedSize, hasQuota: hasQuota}, nil
}

// buildILanzouDownloadUrl builds the signed /unproved/file/redirect URL.
func buildILanzouDownloadUrl(fileID, userID, token, uuid string) (string, error) {
	ts, tsEnc, err := getTimestampToken(ILANZOU_CONF.Secret)
	if err != nil {
		return "", err
	}
	downloadID, err := aesEncryptToHex(fileID+"|"+userID, ILANZOU_CONF.Secret)
	if err != nil {
		return "", err
	}
	auth, err := aesEncryptToHex(fmt.Sprintf("%s|%d", fileID, ts), ILANZOU_CONF.Secret)
	if err != nil {
		return "", err
	}
	params := signParams(uuid, tsEnc)
	params.Set("appToken", token)
	params.Set("enable", "1")
	params.Set("downloadId", downloadID)
	params.Set("auth", auth)
	return ILANZOU_CONF.Base + "/" + ILANZOU_CONF.Unproved + "/file/redirect?" + encodeILanzouParams(params), nil
}

// The service requires literal colons in appToken; other reserved characters
// must remain escaped so they cannot become query parameters.
func encodeILanzouParams(params url.Values) string {
	encoded := params.Encode()
	if token := params.Get("appToken"); token != "" {
		escaped := url.QueryEscape(token)
		encoded = strings.Replace(encoded, "appToken="+escaped, "appToken="+strings.ReplaceAll(escaped, "%3A", ":"), 1)
	}
	return encoded
}

// ---- json helpers ----

func numOf(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	case json.Number:
		n, _ := t.Int64()
		return n
	case int:
		return int64(t)
	case int64:
		return t
	}
	return 0
}

func strOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func mapVal(v any, key string) map[string]any {
	if m, ok := v.(map[string]any); ok {
		if child, ok := m[key].(map[string]any); ok {
			return child
		}
	}
	return nil
}

func ilanzouAccountMap(payload map[string]any) map[string]any {
	if accountMap := mapVal(payload, "map"); accountMap != nil {
		return accountMap
	}
	return mapVal(mapVal(payload, "data"), "map")
}

const maxILanzouQuotaBytes int64 = 1<<63 - 1

func ilanzouNonNegativeInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), typed >= 0
	case int64:
		return typed, typed >= 0
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || typed >= float64(maxILanzouQuotaBytes) || math.Trunc(typed) != typed {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		n, err := typed.Int64()
		return n, err == nil && n >= 0
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return n, err == nil && n >= 0
	default:
		return 0, false
	}
}

// ilanzouQuota extracts KiB-based account-map fields used by the official
// client: base, purchased VIP, and rewarded capacity together form the total.
func ilanzouQuota(payload map[string]any) (totalBytes, usedBytes int64, ok bool) {
	accountMap := ilanzouAccountMap(payload)
	if accountMap == nil {
		return 0, 0, false
	}
	totalKiB := int64(0)
	for _, key := range []string{"totalSize", "vipSize", "rewardSize"} {
		value, exists := accountMap[key]
		if !exists || value == nil {
			continue
		}
		part, valid := ilanzouNonNegativeInt64(value)
		if !valid || totalKiB > maxILanzouQuotaBytes-part {
			return 0, 0, false
		}
		totalKiB += part
	}
	usedRaw, exists := accountMap["usedSize"]
	if !exists || usedRaw == nil || totalKiB <= 0 {
		return 0, 0, false
	}
	usedKiB, valid := ilanzouNonNegativeInt64(usedRaw)
	if !valid || totalKiB > maxILanzouQuotaBytes/1024 || usedKiB > maxILanzouQuotaBytes/1024 {
		return 0, 0, false
	}
	totalBytes = totalKiB * 1024
	usedBytes = usedKiB * 1024
	if usedBytes > totalBytes {
		usedBytes = totalBytes
	}
	return totalBytes, usedBytes, true
}

func applyILanzouQuota(token *model.TokenInfo, totalSize, usedSize int64, hasQuota bool) {
	if token == nil || !hasQuota || totalSize <= 0 {
		return
	}
	if usedSize < 0 {
		return
	}
	if usedSize > totalSize {
		usedSize = totalSize
	}
	token.TotalSize = totalSize
	token.UsedSize = usedSize
	token.FreeSize = totalSize - usedSize
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// RefreshAccount validates the session via /user/account/map; an expired
// token triggers the request-level re-login and the fresh session is persisted
// onto the returned token.
func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if token == nil {
		return nil, drive.AuthExpired("优享版蓝奏云未登录")
	}
	payload, login, err := d.request(ctx, c, "/user/account/map", requestOptions{method: http.MethodGet})
	if err != nil {
		return nil, err
	}
	if login != nil {
		applyLoginSession(token, login)
	}
	totalSize, usedSize, hasQuota := ilanzouQuota(payload)
	applyILanzouQuota(token, totalSize, usedSize, hasQuota)
	return token, nil
}

// applyLoginSession updates only credentials that rotate during an automatic
// re-login. The account identity remains unchanged so a transient mapping
// response cannot move an existing account into a new storage namespace.
func applyLoginSession(token *model.TokenInfo, login *loginResult) {
	if token == nil || login == nil {
		return
	}
	cr := parseCred(token.RefreshToken)
	if cr == nil {
		cr = &cred{}
	}
	cr.UUID = login.uuid
	cr.Token = login.token
	cr.UserID = login.userId
	cr.Account = login.account
	token.AccessToken = login.token
	token.DeviceID = login.uuid
	token.RefreshToken = mustJSON(cr)
	applyILanzouQuota(token, login.totalSize, login.usedSize, login.hasQuota)
}
