package pikpak

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/logging"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const providerID = model.ProviderPikpak

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"cloudVideoPreview":   true,
			"offlineDownload":     true,
			"createShare":         true,
			"shareExpiration":     true,
			"sharePassword":       true,
			"combinedShare":       true,
			"shareHistory":        true,
			"manageCreatedShares": true,
			"cancelCreatedShares": true,
			"importShare":         true,
			"trashView":           true,
			"trashRestore":        true,
			"recycleBin":          true,
			"favorite":            true,
			"permanentDelete":     true,
		}, nil),
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "username", Type: "text", Label: "账号（手机/邮箱）", Required: true},
			{Key: "password", Type: "password", Label: "密码", Required: true},
		}},
		Auth:    authSignIn,
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// DeviceStore persists per-account device ids.
var deviceStore *storePath

type apiCaptchaCacheEntry struct {
	token     string
	expiresAt time.Time
}

var apiCaptchaCache = struct {
	sync.Mutex
	items map[string]apiCaptchaCacheEntry
}{items: make(map[string]apiCaptchaCacheEntry)}

// pikpakVerifiedCaptchaWait gives the provider a brief window to persist the
// completed challenge before its first confirmation request.
var pikpakVerifiedCaptchaWait = 1500 * time.Millisecond

// Match the legacy client's bounded token exchange. A challenge URL immediately
// after slider completion can mean its result has not propagated yet.
var pikpakCaptchaExchangeDelays = [...]time.Duration{600 * time.Millisecond, 1200 * time.Millisecond, 2 * time.Second}

// PikPak risk cooldowns are scoped to the normalized login identifier.  A
// blocked PikPak account must not disable another PikPak account, and this
// provider-local state must never gate logins for other providers.
var pikpakLoginCooldown struct {
	sync.Mutex
	until map[string]time.Time
}

func pikpakLoginCooldownError(username string) error {
	key := idKey(username)
	pikpakLoginCooldown.Lock()
	defer pikpakLoginCooldown.Unlock()
	remaining := time.Until(pikpakLoginCooldown.until[key])
	if remaining <= 0 {
		return nil
	}
	seconds := int(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	if seconds < pikpakMinRateLimitSeconds {
		seconds = pikpakMinRateLimitSeconds
	}
	return &PikPakRateLimitError{RetryAfterSeconds: seconds}
}

func rememberPikPakLoginCooldown(username string, err error) {
	var rate *PikPakRateLimitError
	seconds := 0
	if errors.As(err, &rate) {
		seconds = rate.RetryAfterSeconds
	} else {
		var prohibited *PikPakAccessProhibitedError
		if errors.As(err, &prohibited) {
			seconds = prohibited.retryAfterSeconds()
		}
	}
	if seconds <= 0 {
		return
	}
	if seconds < pikpakMinRateLimitSeconds {
		seconds = pikpakMinRateLimitSeconds
	}
	pikpakLoginCooldown.Lock()
	until := time.Now().Add(time.Duration(seconds) * time.Second)
	if pikpakLoginCooldown.until == nil {
		pikpakLoginCooldown.until = make(map[string]time.Time)
	}
	key := idKey(username)
	if until.After(pikpakLoginCooldown.until[key]) {
		pikpakLoginCooldown.until[key] = until
	}
	pikpakLoginCooldown.Unlock()
}

// ResetPikPakLoginCooldown clears the process gate. It is primarily useful
// for isolated integration tests; normal callers should wait for the server
// supplied cooldown instead of bypassing it.
func ResetPikPakLoginCooldown() {
	pikpakLoginCooldown.Lock()
	pikpakLoginCooldown.until = nil
	pikpakLoginCooldown.Unlock()
}

type storePath struct{ dir string }

// SetIdentityDir sets the device identity storage dir (app wiring).
func SetIdentityDir(dir string) { deviceStore = &storePath{dir: dir} }

func (s *storePath) get(username string) string {
	if s == nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(s.dir, idKey(username)+".id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *storePath) set(username, id string) {
	if s == nil {
		return
	}
	_ = os.MkdirAll(s.dir, 0o755)
	_ = os.WriteFile(filepath.Join(s.dir, idKey(username)+".id"), []byte(id), 0o644)
}

func idKey(username string) string {
	return md5hex(strings.ToLower(strings.TrimSpace(username)))
}

// createDeviceID generates the 32-character hexadecimal device id expected by
// PikPak's web login protocol. UUIDs with hyphens are rejected by the service
// in some login flows.
func createDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// getOrCreateDeviceID returns a stable device id for an account.
func getOrCreateDeviceID(username string) string {
	id := ""
	if deviceStore != nil {
		id = deviceStore.get(username)
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if len(id) == 32 && isHex(id) {
		return id
	}
	id = createDeviceID()
	if deviceStore != nil {
		deviceStore.set(username, id)
	}
	return id
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// initCaptcha requests a captcha token (challenge url may be returned).
func initCaptcha(ctx context.Context, hc *netx.Client, deviceID, username string, action, callbackURI string) (string, string, error) {
	return initCaptchaWithPrev(ctx, hc, deviceID, username, action, "", callbackURI)
}

// initCaptchaWithPrev requests a captcha token; previousToken chains a verified
// slider token so the server registers the result (legacy previousToken retry).
// signin 动作的 meta 只含 email/phone_number/username 单字段（对齐旧版）。
func initCaptchaWithPrev(ctx context.Context, hc *netx.Client, deviceID, username, action, previousToken, callbackURI string) (string, string, error) {
	u := strings.TrimSpace(username)
	meta := map[string]string{}
	if strings.Contains(u, "@") && strings.Contains(u, ".") {
		meta["email"] = u
	} else if isPhone(u) {
		meta["phone_number"] = strings.ReplaceAll(strings.ReplaceAll(u, " ", ""), "-", "")
	} else {
		meta["username"] = u
	}
	body := map[string]any{
		"client_id":    clientID,
		"action":       action,
		"device_id":    deviceID,
		"meta":         meta,
		"redirect_uri": loginCaptchaRedirectURI(callbackURI),
	}
	if previousToken != "" {
		body["captcha_token"] = previousToken
	}
	var res struct {
		CaptchaToken string `json:"captcha_token"`
		URL          string `json:"url"`
	}
	resp, err := requestCaptchaInit(ctx, hc, deviceID, "", body)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", "", parseAPIErrorWithRetry(data, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(res.CaptchaToken) == "" {
		return "", "", errors.New("pikpak: 验证接口未返回 captcha token")
	}
	return res.CaptchaToken, res.URL, nil
}

func loginCaptchaRedirectURI(callbackURI string) string {
	if value := strings.TrimSpace(callbackURI); value != "" {
		return value
	}
	return redirectURI
}

// initAPICaptcha follows the drive API captcha protocol. Its meta payload is
// different from the signin payload: API requests identify the account by
// user_id and carry the signed client metadata inside meta.
func initAPICaptcha(ctx context.Context, hc *netx.Client, deviceID, accountID, accessToken, action, previousToken string) (string, string, error) {
	timestamp := timestampNow()
	meta := CaptchaMeta{
		CaptchaSign:   captchaSign(deviceID, timestamp),
		ClientVersion: clientVersion,
		PackageName:   packageName,
		Timestamp:     timestamp,
		UserID:        model.StripUserID(providerID, strings.TrimSpace(accountID)),
	}
	body := map[string]any{
		"client_id": clientID,
		"action":    action,
		"device_id": deviceID,
		"meta":      meta,
	}
	if previousToken != "" {
		body["captcha_token"] = previousToken
	}
	var res struct {
		CaptchaToken string `json:"captcha_token"`
		URL          string `json:"url"`
	}
	resp, err := requestCaptchaInit(ctx, hc, deviceID, accessToken, body)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", "", parseAPIErrorWithRetry(data, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", "", err
	}
	return res.CaptchaToken, res.URL, nil
}

// Only captcha initialization is replayed here, never the sign-in request.
// A reset can occur before HTTP headers arrive; rebuild the JSON body once.
func requestCaptchaInit(ctx context.Context, hc *netx.Client, deviceID, token string, body any) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := hc.Do(ctx, http.MethodPost, userHost+"/v1/shield/captcha/init", captchaHeaders(deviceID, token), netx.JSONBody(body))
		if err == nil {
			return resp, nil
		}
		var networkError *net.OpError
		transient := errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		if attempt >= 1 || !transient || ctx.Err() != nil {
			return nil, fmt.Errorf("PikPak 验证服务连接失败，请检查应用或系统代理后重试: %w", err)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func captchaHeaders(deviceID, token string) map[string]string {
	h := map[string]string{
		"User-Agent":       userAgent,
		"Accept":           "application/json",
		"Referer":          "https://mypikpak.com/",
		"X-Client-Id":      clientID,
		"X-Device-Id":      deviceID,
		"X-Client-Version": clientVersion,
		"Content-Type":     "application/json",
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return h
}

func timestampNow() string { return fmt.Sprint(time.Now().UnixMilli()) }

func apiCaptchaCacheKey(c *client, action string) string {
	return strings.Join([]string{c.deviceID, c.accountID, md5hex(c.accessToken), action}, "\x00")
}

func cachedAPICaptchaToken(c *client, action string) string {
	if c == nil {
		return ""
	}
	key := apiCaptchaCacheKey(c, action)
	now := time.Now()
	apiCaptchaCache.Lock()
	defer apiCaptchaCache.Unlock()
	entry, ok := apiCaptchaCache.items[key]
	if !ok || entry.token == "" || !entry.expiresAt.After(now.Add(10*time.Second)) {
		if ok {
			delete(apiCaptchaCache.items, key)
		}
		return ""
	}
	return entry.token
}

// apiCaptchaToken gets a cached action token or exchanges the previous token
// for a fresh one. PikPak scopes these tokens by device, account and action.
func apiCaptchaToken(ctx context.Context, c *client, action string, forceRefresh bool) (string, error) {
	if c == nil || c.deviceID == "" || c.accountID == "" {
		return "", errors.New("pikpak: 缺少验证码设备或账号标识")
	}
	key := apiCaptchaCacheKey(c, action)
	var previous string
	if !forceRefresh {
		if token := cachedAPICaptchaToken(c, action); token != "" {
			return token, nil
		}
	}
	apiCaptchaCache.Lock()
	if entry, ok := apiCaptchaCache.items[key]; ok {
		previous = entry.token
	}
	apiCaptchaCache.Unlock()
	token, _, err := initAPICaptcha(ctx, c.http, c.deviceID, c.accountID, c.accessToken, action, previous)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", errors.New("pikpak: 验证接口未返回 captcha token")
	}
	apiCaptchaCache.Lock()
	apiCaptchaCache.items[key] = apiCaptchaCacheEntry{token: token, expiresAt: time.Now().Add(4*time.Minute + 30*time.Second)}
	apiCaptchaCache.Unlock()
	return token, nil
}

// AuthResp is the signin/token response.
type AuthResp struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	TokenType        string `json:"token_type"`
	Sub              string `json:"sub"`
	UserID           string `json:"user_id"`
	UserName         string `json:"user_name"`
	Avatar           string `json:"avatar"`
	NickName         string `json:"nick_name"`
	DeviceID         string `json:"device_id"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// signIn logs in with username+password (+captcha).
// 字段与旧版 signInPikPak 一致：body 只有 client_id/username/password，
// captcha_token 走 X-Captcha-Token 头（放 body 会被服务端忽略 → 报 "add captcha"）。
func signIn(ctx context.Context, hc *netx.Client, deviceID, username, password, captchaToken string) (*AuthResp, error) {
	body := map[string]any{
		"client_id": clientID,
		"username":  strings.TrimSpace(username),
		"password":  password,
	}
	headers := captchaHeaders(deviceID, "")
	if captchaToken != "" {
		headers["X-Captcha-Token"] = captchaToken
	}
	var res AuthResp
	resp, err := hc.Do(ctx, http.MethodPost, userHost+"/v1/auth/signin", headers, netx.JSONBody(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, parseAPIErrorWithRetry(data, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Error != "" || res.ErrorDescription != "" {
		return nil, parseAPIErrorWithRetry(data, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if res.AccessToken == "" {
		return nil, errors.New("pikpak: login failed")
	}
	res.DeviceID = deviceID
	return &res, nil
}

// refreshToken obtains new tokens.
func refreshToken(ctx context.Context, hc *netx.Client, deviceID, refresh string) (*AuthResp, error) {
	body := map[string]any{
		"client_id":     clientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	}
	var res AuthResp
	resp, err := hc.Do(ctx, http.MethodPost, userHost+"/v1/auth/token", captchaHeaders(deviceID, ""), netx.JSONBody(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, parseAPIErrorWithRetry(data, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.AccessToken == "" {
		return nil, errors.New("pikpak: refresh failed")
	}
	return &res, nil
}

// retryLoginCaptcha mirrors the legacy login flow after signin reports a
// captcha error. A required captcha reuses the token that was submitted;
// an invalid captcha starts a fresh chain without reusing that token.
func retryLoginCaptcha(ctx context.Context, hc *netx.Client, deviceID, username, action, previousToken, callbackURI string) (string, string, error) {
	return initCaptchaWithPrev(ctx, hc, deviceID, username, action, previousToken, callbackURI)
}

func confirmVerifiedCaptcha(ctx context.Context, hc *netx.Client, deviceID, username, previousToken, callbackURI string) (string, string, error) {
	token, challenge, err := initCaptchaWithPrev(ctx, hc, deviceID, username, "POST:/v1/auth/signin", previousToken, callbackURI)
	for _, delay := range pikpakCaptchaExchangeDelays {
		if err != nil || challenge == "" {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", "", ctx.Err()
		case <-timer.C:
		}
		// Each exchange must carry the most recently issued token, retaining
		// the same device identity and redirect throughout the chain.
		token, challenge, err = initCaptchaWithPrev(ctx, hc, deviceID, username, "POST:/v1/auth/signin", token, callbackURI)
	}
	return token, challenge, err
}

// CaptchaRequiredError carries the visual challenge values while preserving the
// text protocol consumed by the Wails login page.
type CaptchaRequiredError struct {
	URL   string
	Token string
}

func (e *CaptchaRequiredError) Error() string {
	return fmt.Sprintf("pikpak: captcha_required\nurl=%s\ntoken=%s", e.URL, e.Token)
}

// authSignIn is the Registration.Auth login flow.
func authSignIn(ctx context.Context, req drive.AuthRequest) (*model.TokenInfo, error) {
	started := time.Now()
	username := strings.TrimSpace(req.Config["username"])
	password := req.Config["password"]
	logging.Info("PikPak sign-in started", "identifier_type", pikpakIdentifierType(username), "has_captcha_token", strings.TrimSpace(req.Config["captcha_token"]) != "")
	if username == "" || password == "" {
		logging.Warn("PikPak sign-in rejected", "reason", "missing credentials")
		return nil, errors.New("pikpak: 请输入账号和密码")
	}
	if err := pikpakLoginCooldownError(username); err != nil {
		logging.Warn("PikPak sign-in blocked by cooldown")
		return nil, err
	}
	deviceID := getOrCreateDeviceID(username)
	hc := netx.NewClientWithSystemProxy(60 * time.Second)
	callbackURI := strings.TrimSpace(req.Config["captcha_redirect_uri"])

	captchaToken := strings.TrimSpace(req.Config["captcha_token"])
	captchaVerified := strings.EqualFold(strings.TrimSpace(req.Config["captcha_verified"]), "true")
	captchaNeedsConfirmation := captchaVerified && strings.EqualFold(strings.TrimSpace(req.Config["captcha_requires_confirmation"]), "true")
	if captchaToken == "" {
		// A failed captcha init must stop here. Continuing with an empty token
		// turns a transport/rate-limit failure into a misleading login error.
		tok, urlValue, err := initCaptcha(ctx, hc, deviceID, username, "POST:/v1/auth/signin", callbackURI)
		if err != nil {
			rememberPikPakLoginCooldown(username, err)
			logging.Warn("PikPak captcha initialization failed", "error", err)
			return nil, err
		}
		captchaToken = tok
		if urlValue != "" {
			logging.Info("PikPak visual captcha required")
			// 需要滑块验证：把验证 URL 与 token 带回前端，用户完成后带 token 重试
			return nil, &CaptchaRequiredError{URL: urlValue, Token: tok}
		}
	}
	if captchaNeedsConfirmation {
		// The callback can arrive before the provider has registered the slider
		// result. Exchange only after confirmed visual completion, with the
		// legacy retry bound, then submit the login request once.
		timer := time.NewTimer(pikpakVerifiedCaptchaWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		tok, urlValue, confirmErr := confirmVerifiedCaptcha(ctx, hc, deviceID, username, captchaToken, callbackURI)
		if confirmErr != nil {
			rememberPikPakLoginCooldown(username, confirmErr)
			logging.Warn("PikPak captcha completion confirmation failed", "error", confirmErr)
			return nil, confirmErr
		}
		if urlValue != "" {
			logging.Info("PikPak captcha completion requires another visual challenge")
			return nil, &CaptchaRequiredError{URL: urlValue, Token: tok}
		}
		captchaToken = tok
	}

	auth, err := signIn(ctx, hc, deviceID, username, password, captchaToken)
	if err != nil {
		rememberPikPakLoginCooldown(username, err)
		logging.Warn("PikPak sign-in request failed", "error", err)
		if captchaVerified {
			return nil, err
		}
		if !isCaptchaError(err) {
			return nil, err
		}
		previousToken := ""
		if isCaptchaRequiredError(err) {
			previousToken = captchaToken
		}
		// The first failed signin follows the legacy client's single retry.
		// Do not run the post-slider chain here: the user has not completed a
		// new challenge yet.
		tok, urlValue, retryErr := retryLoginCaptcha(ctx, hc, deviceID, username, "POST:/v1/auth/signin", previousToken, callbackURI)
		if retryErr != nil {
			rememberPikPakLoginCooldown(username, retryErr)
			logging.Warn("PikPak captcha retry initialization failed", "error", retryErr)
			return nil, retryErr
		}
		if urlValue != "" {
			logging.Info("PikPak retry requires visual captcha")
			return nil, &CaptchaRequiredError{URL: urlValue, Token: tok}
		}
		captchaToken = tok
		auth, err = signIn(ctx, hc, deviceID, username, password, captchaToken)
	}
	if err != nil {
		rememberPikPakLoginCooldown(username, err)
		logging.Warn("PikPak sign-in retry failed", "error", err)
		return nil, err
	}

	accountID := firstNonEmpty(auth.UserID, auth.Sub, username)
	used, total := int64(0), int64(0)
	if cl := newClient(auth.AccessToken, deviceID, accountID); cl != nil {
		used, total = cl.About(ctx)
	}
	name := auth.NickName
	if name == "" {
		name = auth.UserName
	}
	if name == "" {
		name = username
	}
	tok := &model.TokenInfo{
		TokenFrom:         providerID,
		AccessToken:       auth.AccessToken,
		RefreshToken:      auth.RefreshToken,
		ExpiresIn:         auth.ExpiresIn,
		TokenType:         auth.TokenType,
		UserID:            model.BuildUserID(providerID, accountID),
		ProviderAccountID: accountID,
		UserName:          name,
		NickName:          name,
		Name:              name,
		Avatar:            auth.Avatar,
		DeviceID:          deviceID,
		UsedSize:          used,
		TotalSize:         total,
	}
	logging.Info("PikPak sign-in completed", "duration", logging.Duration(started))
	return tok, nil
}

func pikpakIdentifierType(value string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "@") {
		return "email"
	}
	if isPhone(value) {
		return "phone"
	}
	return "username"
}
