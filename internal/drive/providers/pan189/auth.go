package pan189

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// CloudPersonal and CloudFamily select the mounted cloud at login.
const (
	CloudPersonal = "personal"
	CloudFamily   = "family"

	pan189LoginStateTTL = 5 * time.Minute
)

var errPan189LoginPageExpired = errors.New("189 登录页面已失效")

// CaptchaError is returned when the account requires a graphical captcha.
// The data-URL image is available in LastCaptchaImage and must be supplied back
// via the "validate_code" login-field on the next attempt (the pending login
// parameters are cached and reused for that retry).
type CaptchaError struct {
	CaptchaImage string
}

func (e *CaptchaError) Error() string {
	if e.CaptchaImage != "" {
		return fmt.Sprintf("captcha_required_189\nimage=%s", e.CaptchaImage)
	}
	return "该账号需要图形验证码，请输入图片中的字符后重试"
}

// LastCaptchaImage holds the most recent captcha image as a base64 data URL,
// exposed for the login panel to render when a login fails with CaptchaError.
var (
	loginStateMu  sync.Mutex
	pendingLogins = map[string]*pan189LoginState{}
	lastCaptcha   string
)

// CaptchaImage returns the latest captcha image for UI integrations that do
// not carry the username through the login form.
func CaptchaImage() string {
	loginStateMu.Lock()
	defer loginStateMu.Unlock()
	return lastCaptcha
}

// pan189LoginState mirrors the pending login parameters that must be reused
// between fetching the captcha image and submitting the code.
type pan189LoginState struct {
	User            string
	PasswordProof   string
	CaptchaToken    string
	LT              string
	ParamID         string
	ReqID           string
	RsaUsername     string
	RsaPassword     string
	PublicKey       string
	RsaPrefix       string
	SMSLogin        bool
	SMSCaptchaToken string
	PageKey         string
	Client          *netx.Client
	CreatedAt       time.Time
}

func pan189PasswordProof(password string) string {
	sum := sha256.Sum256([]byte(password))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

func newPan189LoginClient() (*netx.Client, error) {
	hc := netx.NewClient(60 * time.Second)
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	hc.HTTP.Jar = jar
	return hc, nil
}

func takePan189PendingLogin(user, password string) (*pan189LoginState, bool) {
	loginStateMu.Lock()
	defer loginStateMu.Unlock()
	for key, state := range pendingLogins {
		if state == nil || time.Since(state.CreatedAt) > pan189LoginStateTTL {
			delete(pendingLogins, key)
		}
	}
	state := pendingLogins[user]
	if state == nil || state.PasswordProof != pan189PasswordProof(password) || state.Client == nil {
		return nil, false
	}
	return state, true
}

func savePan189PendingLogin(state *pan189LoginState, captcha string) {
	if state == nil || state.User == "" {
		return
	}
	loginStateMu.Lock()
	pendingLogins[state.User] = state
	lastCaptcha = captcha
	loginStateMu.Unlock()
}

func deletePan189PendingLogin(user string) {
	loginStateMu.Lock()
	delete(pendingLogins, user)
	loginStateMu.Unlock()
}

// loginWithCreds runs the 189 account+password login flow. validateCode is the
// graphical captcha text when retrying after a CaptchaError.
func loginWithCreds(ctx context.Context, username, password, validateCode string) (*Session, error) {
	user := strings.TrimSpace(username)
	pass := password
	if user == "" || pass == "" {
		return nil, errors.New("请输入天翼云盘账号和密码")
	}
	session, err := doLogin(ctx, user, pass, validateCode)
	if errors.Is(err, errPan189LoginPageExpired) && ctx.Err() == nil {
		// A refresh invalidates the old RSA parameters, cookies and captcha.
		// Restart once without reusing a code from the previous login page.
		session, err = doLogin(ctx, user, pass, "")
	}
	if err != nil {
		return nil, err
	}
	return attachLoginCredentials(session, user, pass), nil
}

// attachLoginCredentials keeps the credentials required for the final silent
// re-login when the provider's open token can no longer refresh a session.
func attachLoginCredentials(session *Session, username, password string) *Session {
	if session == nil {
		return nil
	}
	session.Username = strings.TrimSpace(username)
	session.Password = password
	return session
}

func doLogin(ctx context.Context, user, pass, validateCode string) (*Session, error) {
	var state *pan189LoginState
	if validateCode != "" {
		var ok bool
		state, ok = takePan189PendingLogin(user, pass)
		if !ok {
			return nil, errors.New("captcha_expired_189\n189 图形验证码已过期，请重新登录")
		}
	} else {
		deletePan189PendingLogin(user)
		var err error
		state, err = prepareLoginParam(ctx, user, pass)
		if err != nil {
			return nil, err
		}
	}

	// needcaptcha: anything other than "0" requires a graphical captcha.
	if validateCode == "" {
		need, err := needCaptcha(ctx, state)
		if err != nil {
			return nil, err
		}
		if need {
			image, err := fetchCaptchaImage(ctx, state)
			if err != nil {
				return nil, err
			}
			savePan189PendingLogin(state, image)
			return nil, &CaptchaError{CaptchaImage: image}
		}
	}

	toURL, err := loginSubmit(ctx, state, validateCode)
	if err != nil {
		return nil, err
	}
	deletePan189PendingLogin(user)
	return getSessionForPC(ctx, state, toURL, user)
}

// prepareLoginParam fetches the init params (captchaToken/lt/paramId/reqId)
// and the RSA public key, then encrypts the credentials (AList initLoginParam).
func prepareLoginParam(ctx context.Context, user, pass string) (*pan189LoginState, error) {
	hc, err := newPan189LoginClient()
	if err != nil {
		return nil, err
	}
	// 1) unifyLoginForPC
	ts := timestamp()
	unifyURL := fmt.Sprintf("%s/api/portal/unifyLoginForPC.action?appId=%s&clientType=%s&returnURL=%s&timeStamp=%d",
		webURL, appID, clientType, url.QueryEscape(returnURL), ts)
	resp, err := hc.Do(ctx, http.MethodGet, unifyURL, map[string]string{
		"User-Agent": ua189,
		"Accept":     "text/html,application/xhtml+xml",
	}, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		resp.Body.Close()
		return nil, fmt.Errorf("初始化 189 登录参数失败：HTTP %d", resp.StatusCode)
	}
	html, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	resp.Body.Close()
	page := string(html)
	captchaToken := pickMatch(page, `'captchaToken'\s*value='(.+?)'`)
	if captchaToken == "" {
		captchaToken = pickMatch(page, `captchaToken["']\s*value=["'](.+?)["']`)
	}
	lt := pickMatch(page, `\blt\s*=\s*"([0-9A-Fa-f]{16,})"`)
	paramID := pickMatch(page, `\bparamId\s*=\s*"([0-9A-Fa-f]{16,})"`)
	reqID := pickMatch(page, `reqId\s*=\s*"(.+?)"`)
	if lt == "" || paramID == "" || reqID == "" {
		return nil, errors.New("初始化 189 登录参数失败，请稍后重试")
	}

	// 2) encryptConf → RSA public key + prefix
	form := url.Values{}
	form.Set("appId", appID)
	cresp, err := hc.Do(ctx, http.MethodPost, authURL+"/api/logbox/config/encryptConf.do", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"User-Agent":   ua189,
		"Accept":       "application/json",
	}, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	if cresp.StatusCode < http.StatusOK || cresp.StatusCode >= http.StatusMultipleChoices {
		cresp.Body.Close()
		return nil, fmt.Errorf("获取 189 RSA 公钥失败：HTTP %d", cresp.StatusCode)
	}
	var conf struct {
		Data struct {
			PubKey string `json:"pubKey"`
			Pre    string `json:"pre"`
		} `json:"data"`
	}
	if err := json.NewDecoder(cresp.Body).Decode(&conf); err != nil {
		cresp.Body.Close()
		return nil, err
	}
	cresp.Body.Close()
	if conf.Data.PubKey == "" {
		return nil, errors.New("获取 189 RSA 公钥失败")
	}
	rsaUser, err := rsaEncrypt(conf.Data.PubKey, user)
	if err != nil {
		return nil, err
	}
	rsaPass, err := rsaEncrypt(conf.Data.PubKey, pass)
	if err != nil {
		return nil, err
	}
	return &pan189LoginState{
		User: user, PasswordProof: pan189PasswordProof(pass), Client: hc, CreatedAt: time.Now(),
		CaptchaToken: captchaToken, LT: lt, ParamID: paramID, ReqID: reqID,
		RsaUsername: conf.Data.Pre + rsaUser,
		RsaPassword: conf.Data.Pre + rsaPass,
		PublicKey:   conf.Data.PubKey, RsaPrefix: conf.Data.Pre,
		SMSCaptchaToken: pickMatch(page, `(?s)id=["']j-sms-captcha["'].*?name=['"]captchaToken['"]\s*value=['"]([^'"]+)`),
		PageKey:         firstNonEmpty(pickMatch(page, `\bpageKey\s*=\s*["']([^"']+)`), "normal"),
	}, nil
}

// needCaptcha reports whether the account requires a graphical captcha.
func needCaptcha(ctx context.Context, st *pan189LoginState) (bool, error) {
	if st == nil || st.Client == nil {
		return false, errors.New("189 登录会话已失效，请重新登录")
	}
	form := url.Values{}
	form.Set("appKey", appID)
	form.Set("accountType", accountType)
	form.Set("userName", st.RsaUsername)
	resp, err := st.Client.Do(ctx, http.MethodPost, authURL+"/api/logbox/oauth2/needcaptcha.do", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"REQID":        st.ReqID,
		"User-Agent":   ua189,
	}, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("检查 189 图形验证码失败：HTTP %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(body)) != "0", nil
}

// fetchCaptchaImage returns the captcha image as a base64 data URL.
func fetchCaptchaImage(ctx context.Context, st *pan189LoginState) (string, error) {
	if st == nil || st.Client == nil {
		return "", errors.New("189 登录会话已失效，请重新登录")
	}
	q := url.Values{}
	q.Set("token", st.CaptchaToken)
	q.Set("REQID", st.ReqID)
	q.Set("rnd", fmt.Sprintf("%d", timestamp()))
	u := authURL + "/api/logbox/oauth2/picCaptcha.do?" + q.Encode()
	resp, err := st.Client.Do(ctx, http.MethodGet, u, map[string]string{"User-Agent": ua189}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("获取 189 验证码图片失败：HTTP %d", resp.StatusCode)
	}
	if len(buf) <= 20 {
		return "", errors.New("获取 189 验证码图片失败")
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf), nil
}

// loginSubmit sends the RSA-encrypted credentials and returns the redirect URL.
func loginSubmit(ctx context.Context, st *pan189LoginState, validateCode string) (string, error) {
	if st == nil || st.Client == nil {
		return "", errors.New("189 登录会话已失效，请重新登录")
	}
	form := url.Values{}
	form.Set("appKey", appID)
	form.Set("accountType", accountType)
	form.Set("userName", st.RsaUsername)
	form.Set("epd", st.RsaPassword)
	form.Set("pageKey", st.PageKey)
	form.Set("validateCode", validateCode)
	form.Set("captchaToken", st.CaptchaToken)
	form.Set("returnUrl", returnURL)
	form.Set("dynamicCheck", "FALSE")
	if st.SMSLogin {
		form.Set("dynamicCheck", "TRUE")
		form.Set("smsValidateCode", validateCode)
		form.Set("validateCode", "")
	}
	form.Set("clientType", clientType)
	form.Set("cb_SaveName", "1")
	form.Set("isOauth2", "false")
	form.Set("state", "")
	form.Set("paramId", st.ParamID)

	resp, err := st.Client.Do(ctx, http.MethodPost, authURL+"/api/logbox/oauth2/loginSubmit.do", map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"REQID":        st.ReqID,
		"lt":           st.LT,
		"User-Agent":   ua189,
		"Accept":       "application/json;charset=UTF-8",
	}, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("189 登录失败：HTTP %d", resp.StatusCode)
	}
	var j map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		return "", errors.New("189 登录失败（响应异常）")
	}
	toURL := strVal(j, "toUrl")
	if toURL == "" {
		msg := firstNonEmpty(strVal(j, "msg"), strVal(j, "message"), strVal(j, "errorMsg"), strVal(j, "desc"), "189 登录失败（未返回 toUrl）")
		if code := strVal(j, "result"); code != "" {
			msg += "（错误码 " + code + "）"
		}
		if strings.Contains(msg, "刷新页面后重试") {
			return "", fmt.Errorf("%w：%s", errPan189LoginPageExpired, msg)
		}
		// SMS validation errors belong to the SMS session. The password-captcha
		// marker would clear the wrong frontend state and discard the service code.
		if !st.SMSLogin && isPan189CaptchaFailure(msg) {
			return "", errors.New("captcha_retry_189\n189 图形验证码不正确，请重新登录")
		}
		return "", errors.New(msg)
	}
	return toURL, nil
}

func isPan189CaptchaFailure(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "验证码") || strings.Contains(message, "captcha") || strings.Contains(message, "validatecode")
}

type pan189SMSState struct {
	Login           *pan189LoginState
	CaptchaRequired bool
	ValidateCode    string
	LastSent        time.Time
}

var pan189SMSMu sync.Mutex
var pan189SMSLogins = map[string]*pan189SMSState{}

func prunePan189SMSLogins() {
	for user, state := range pan189SMSLogins {
		if time.Since(state.Login.CreatedAt) > pan189LoginStateTTL {
			delete(pan189SMSLogins, user)
		}
	}
}

// RequestPan189SMS returns a captcha image when one is required before sending.
// An empty image with no error means the SMS was accepted by the service.
func RequestPan189SMS(ctx context.Context, username, validateCode string) (string, error) {
	username = strings.TrimSpace(username)
	if !regexp.MustCompile(`^1\d{10}$`).MatchString(username) {
		return "", errors.New("请输入有效的 11 位手机号")
	}
	pan189SMSMu.Lock()
	defer pan189SMSMu.Unlock()
	prunePan189SMSLogins()
	state := pan189SMSLogins[username]
	if state == nil {
		login, err := prepareLoginParam(ctx, username, "")
		if err != nil {
			return "", err
		}
		login.SMSLogin = true
		login.CaptchaToken = firstNonEmpty(login.SMSCaptchaToken, login.CaptchaToken)
		state = &pan189SMSState{Login: login}
		pan189SMSLogins[username] = state
	}
	if !state.LastSent.IsZero() && time.Since(state.LastSent) < time.Minute {
		return "", errors.New("短信验证码已发送，请 60 秒后再试")
	}
	st := state.Login
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "REQID": st.ReqID, "User-Agent": ua189}
	form := url.Values{"appKey": {appID}, "mobile": {st.RsaUsername}}
	resp, err := st.Client.Do(ctx, http.MethodPost, authURL+"/api/logbox/oauth2/smsNeedcaptcha.do", headers, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("检查天翼短信验证码失败：HTTP %d", resp.StatusCode)
	}
	switch strings.TrimSpace(string(body)) {
	case "0":
		state.CaptchaRequired = true
	case "1":
		state.CaptchaRequired = false
	default:
		return "", errors.New("检查天翼短信验证码失败（响应异常）")
	}
	validateCode = strings.TrimSpace(validateCode)
	if state.CaptchaRequired && validateCode == "" {
		return fetchCaptchaImage(ctx, st)
	}
	if state.CaptchaRequired {
		form.Set("captchaToken", st.CaptchaToken)
		form.Set("validateCode", validateCode)
	} else {
		form.Set("captchaToken", "")
		form.Set("validateCode", "")
		validateCode = ""
	}
	resp, err = st.Client.Do(ctx, http.MethodPost, authURL+"/api/logbox/oauth2/web/sendSmsCode.do", headers, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return "", fmt.Errorf("发送天翼短信验证码失败：HTTP %d（响应异常）", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || strVal(result, "result") != "0" {
		msg := firstNonEmpty(strVal(result, "msg"), "发送天翼短信验证码失败")
		if code := strVal(result, "result"); code != "" {
			msg += "（错误码 " + code + "）"
		}
		if strings.Contains(msg, "刷新页面后重试") {
			// Retrying with the same invalid page parameters cannot recover.
			// Reinitialize on the user's next send, without replaying an SMS.
			delete(pan189SMSLogins, username)
			return "", fmt.Errorf("%w：%s，请重新获取短信验证码", errPan189LoginPageExpired, msg)
		}
		return "", errors.New(msg)
	}
	state.LastSent = time.Now()
	state.ValidateCode = validateCode
	return "", nil
}

func loginPan189BySMS(ctx context.Context, username, smsCode string) (*Session, error) {
	username, smsCode = strings.TrimSpace(username), strings.TrimSpace(smsCode)
	if username == "" || smsCode == "" {
		return nil, errors.New("请输入手机号和短信验证码")
	}
	pan189SMSMu.Lock()
	defer pan189SMSMu.Unlock()
	prunePan189SMSLogins()
	state := pan189SMSLogins[username]
	if state == nil || state.LastSent.IsZero() {
		return nil, errors.New("短信登录会话已过期，请重新获取验证码")
	}
	st := *state.Login
	encrypted, err := rsaEncrypt(st.PublicKey, smsCode)
	if err != nil {
		return nil, err
	}
	st.RsaPassword = st.RsaPrefix + encrypted
	redirect, err := loginSubmit(ctx, &st, state.ValidateCode)
	if err != nil {
		if errors.Is(err, errPan189LoginPageExpired) {
			delete(pan189SMSLogins, username)
		}
		return nil, err
	}
	delete(pan189SMSLogins, username)
	// The one-time SMS code must never become a persisted account password.
	return getSessionForPC(ctx, &st, redirect, username)
}

// getSessionForPC exchanges the redirect URL for the API session key/secret.
func getSessionForPC(ctx context.Context, st *pan189LoginState, toURL, loginName string) (*Session, error) {
	if st == nil || st.Client == nil {
		return nil, errors.New("189 登录会话已失效，请重新登录")
	}
	u, _ := url.Parse(apiURL + "/getSessionForPC.action")
	q := u.Query()
	for k, v := range clientSuffix() {
		q.Set(k, v)
	}
	q.Set("redirectURL", toURL)
	u.RawQuery = q.Encode()

	resp, err := st.Client.Do(ctx, http.MethodPost, u.String(), map[string]string{
		"User-Agent": ua189,
		"Accept":     "application/json;charset=UTF-8",
		"Referer":    webURL,
	}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var failure map[string]json.RawMessage
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&failure) == nil {
			code := firstNonEmpty(strVal(failure, "res_code"), strVal(failure, "errorCode"))
			message := firstNonEmpty(strVal(failure, "res_message"), strVal(failure, "errorMsg"), strVal(failure, "message"))
			if code == "LoginRespIsNull" {
				message = "登录凭据未被服务端接受，请重新登录"
			}
			if code != "" || message != "" {
				return nil, fmt.Errorf("获取 189 Session 失败：HTTP %d（%s）", resp.StatusCode, strings.TrimSpace(code+" "+message))
			}
		}
		return nil, fmt.Errorf("获取 189 Session 失败：HTTP %d", resp.StatusCode)
	}
	var j map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		return nil, errors.New("获取 189 Session 失败")
	}
	resCode := strVal(j, "res_code")
	if resCode != "" && resCode != "0" && strVal(j, "sessionKey") == "" {
		return nil, errors.New(firstNonEmpty(strVal(j, "res_message"), "获取 189 Session 失败"))
	}
	s := &Session{
		SessionKey:          strVal(j, "sessionKey"),
		SessionSecret:       strVal(j, "sessionSecret"),
		FamilySessionKey:    strVal(j, "familySessionKey"),
		FamilySessionSecret: strVal(j, "familySessionSecret"),
		AccessToken:         strVal(j, "accessToken"),
		RefreshToken:        strVal(j, "refreshToken"),
		LoginName:           firstNonEmpty(strVal(j, "loginName"), loginName),
	}
	if s.SessionKey == "" || s.SessionSecret == "" {
		return nil, errors.New("189 Session 不完整")
	}
	return s, nil
}

func pickMatch(text, pattern string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// login189 implements the provider AuthFunc (账户+密码; optionally 家庭云).
// Form keys: username / password / cloud_type (personal|family) /
// validate_code (captcha retry).
func login189(ctx context.Context, req drive.AuthRequest) (*model.TokenInfo, error) {
	username := strings.TrimSpace(req.Config["username"])
	password := req.Config["password"]
	cloudType := strings.ToLower(strings.TrimSpace(req.Config["cloud_type"]))
	if cloudType != CloudFamily {
		cloudType = CloudPersonal
	}
	validateCode := req.Config["validate_code"]

	var session *Session
	var err error
	if req.Config["login_mode"] == "sms" {
		session, err = loginPan189BySMS(ctx, username, req.Config["sms_code"])
	} else {
		session, err = loginWithCreds(ctx, username, password, validateCode)
	}
	if err != nil {
		return nil, err
	}
	uid := session.LoginName
	if uid == "" {
		uid = username
	}

	if cloudType == CloudFamily {
		if session.FamilySessionKey == "" || session.FamilySessionSecret == "" {
			return nil, errors.New("该账号未开通家庭云，请先在官方 App 中创建或加入家庭")
		}
		families, err := getFamilyList(ctx, session)
		if err != nil {
			return nil, err
		}
		if len(families) == 0 {
			return nil, errors.New("该账号未加入任何家庭云，请先在官方 App 中创建或加入家庭")
		}
		family := families[0]
		for _, f := range families {
			if f.RemarkName != "" && strings.Contains(uid, f.RemarkName) {
				family = f
				break
			}
		}
		session.CloudType = CloudFamily
		session.FamilyID = family.FamilyID
		session.FamilyName = family.RemarkName
		if session.FamilyName == "" {
			session.FamilyName = "家庭云"
		}
	} else {
		session.CloudType = CloudPersonal
	}

	isFamily := session.CloudType == CloudFamily
	accountID := uid
	if isFamily {
		accountID = uid + "_family"
	}
	name := uid
	tok := &model.TokenInfo{
		TokenFrom:         providerID,
		AccessToken:       session.SessionKey,
		RefreshToken:      mustJSON(session),
		TokenType:         "Session",
		UserID:            model.BuildUserID(providerID, accountID),
		UserName:          name,
		NickName:          name,
		Name:              name,
		Avatar:            "",
		DefaultDriveID:    model.BuildDriveID(providerID, accountID),
		ProviderAccountID: accountID,
		ProviderRootID:    Pan189DefaultFolder,
	}
	return tok, nil
}
