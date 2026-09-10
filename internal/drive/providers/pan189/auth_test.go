package pan189

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mnemo-go/internal/drive"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/netx"
)

type pan189AuthRoundTripFunc func(*http.Request) (*http.Response, error)

func TestPan189SMSRefreshRotatesTokensAndIsBounded(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			old := netx.TestTransportHook
			t.Cleanup(func() { netx.TestTransportHook = old })
			sessions, refreshes := 0, 0
			netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/oauth2/refreshToken.do" {
					refreshes++
					if err := req.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if req.Form.Get("refreshToken") != "refresh-old" || req.Form.Get("grantType") != "refresh_token" || req.Form.Get("clientId") != appID {
						t.Fatalf("invalid refresh form")
					}
					return pan189AuthResponse(req, 200, nil, `{"accessToken":"access-new","refreshToken":"refresh-new"}`), nil
				}
				sessions++
				if sessions == 1 || rejected {
					return pan189AuthResponse(req, 400, nil, `{"errorCode":"UserInvalidOpenToken"}`), nil
				}
				if req.URL.Query().Get("accessToken") != "access-new" {
					t.Fatal("old access token reused")
				}
				return pan189AuthResponse(req, 200, nil, `{"sessionKey":"key-new","sessionSecret":"secret-new"}`), nil
			})
			original := &Session{AccessToken: "access-old", RefreshToken: "refresh-old", FamilyID: "family"}
			next, err := (&Driver{}).refreshSession(context.Background(), nil, original)
			if rejected {
				if err == nil {
					t.Fatal("expected rejection")
				}
			} else if err != nil || next.AccessToken != "access-new" || next.RefreshToken != "refresh-new" || next.SessionKey != "key-new" || next.FamilyID != "family" {
				t.Fatalf("refresh failed: %+v, %v", next, err)
			}
			if sessions != 2 || refreshes != 1 {
				t.Fatalf("unbounded/missing refresh: sessions=%d refreshes=%d", sessions, refreshes)
			}
			if original.AccessToken != "access-old" {
				t.Fatal("original session mutated")
			}
		})
	}
}

func (fn pan189AuthRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func pan189AuthResponse(req *http.Request, status int, headers http.Header, body string) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestPan189LoginReusesCookieSessionAcrossCaptchaRetry(t *testing.T) {
	oldTransport := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = oldTransport })
	loginStateMu.Lock()
	pendingLogins = map[string]*pan189LoginState{}
	lastCaptcha = ""
	loginStateMu.Unlock()
	t.Cleanup(func() {
		loginStateMu.Lock()
		pendingLogins = map[string]*pan189LoginState{}
		lastCaptcha = ""
		loginStateMu.Unlock()
	})

	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubKey := base64.StdEncoding.EncodeToString(pubDER)

	const user = "189-test-user"
	const password = "password"
	counts := map[string]int{}
	netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		key := req.URL.Host + req.URL.Path
		counts[key]++
		switch key {
		case "cloud.189.cn/api/portal/unifyLoginForPC.action":
			headers := make(http.Header)
			headers.Add("Set-Cookie", "cloud_bootstrap=cloud-cookie; Domain=.cloud.189.cn; Path=/; Secure")
			page := `<input 'captchaToken' value='captcha-token'><script>var lt = "0123456789abcdef"; var paramId = "abcdef0123456789"; var reqId = "request-id";</script>`
			return pan189AuthResponse(req, http.StatusOK, headers, page), nil

		case "open.e.189.cn/api/logbox/config/encryptConf.do":
			headers := make(http.Header)
			headers.Add("Set-Cookie", "auth_session=auth-cookie; Domain=.e.189.cn; Path=/; Secure")
			return pan189AuthResponse(req, http.StatusOK, headers, fmt.Sprintf(`{"data":{"pubKey":%q,"pre":"PRE"}}`, pubKey)), nil

		case "open.e.189.cn/api/logbox/oauth2/needcaptcha.do":
			if !strings.Contains(req.Header.Get("Cookie"), "auth_session=auth-cookie") {
				return nil, fmt.Errorf("needcaptcha lost the encryptConf cookie")
			}
			return pan189AuthResponse(req, http.StatusOK, nil, "1"), nil

		case "open.e.189.cn/api/logbox/oauth2/picCaptcha.do":
			if !strings.Contains(req.Header.Get("Cookie"), "auth_session=auth-cookie") {
				return nil, fmt.Errorf("captcha image lost the encryptConf cookie")
			}
			return pan189AuthResponse(req, http.StatusOK, nil, "012345678901234567890123456789"), nil

		case "open.e.189.cn/api/logbox/oauth2/loginSubmit.do":
			if !strings.Contains(req.Header.Get("Cookie"), "auth_session=auth-cookie") {
				return nil, fmt.Errorf("loginSubmit lost the encryptConf cookie")
			}
			body, readErr := io.ReadAll(req.Body)
			if readErr != nil {
				return nil, readErr
			}
			form, parseErr := url.ParseQuery(string(body))
			if parseErr != nil {
				return nil, parseErr
			}
			if form.Get("validateCode") != "ABCD" || form.Get("captchaToken") != "captcha-token" || form.Get("paramId") != "abcdef0123456789" {
				return nil, fmt.Errorf("loginSubmit form = %s", form.Encode())
			}
			if form.Get("epd") == "" || form.Get("password") != "" || form.Get("dynamicCheck") != "FALSE" {
				return nil, errors.New("password login did not use the current epd protocol")
			}
			return pan189AuthResponse(req, http.StatusOK, nil, `{"toUrl":"https://cloud.189.cn/login/success?ticket=ticket"}`), nil

		case "api.cloud.189.cn/getSessionForPC.action":
			if !strings.Contains(req.Header.Get("Cookie"), "cloud_bootstrap=cloud-cookie") {
				return nil, fmt.Errorf("getSessionForPC lost the cloud.189.cn cookie")
			}
			return pan189AuthResponse(req, http.StatusOK, nil, `{"res_code":"0","sessionKey":"session-key","sessionSecret":"session-secret","loginName":"189-test-user"}`), nil
		default:
			return nil, fmt.Errorf("unexpected 189 auth request %s %s", req.Method, req.URL.String())
		}
	})

	_, err = loginWithCreds(context.Background(), user, password, "")
	var captchaErr *CaptchaError
	if !errors.As(err, &captchaErr) {
		t.Fatalf("first login error = %v, want CaptchaError", err)
	}
	if captchaErr.CaptchaImage == "" {
		t.Fatal("CaptchaError did not carry the image")
	}
	if counts["cloud.189.cn/api/portal/unifyLoginForPC.action"] != 1 || counts["open.e.189.cn/api/logbox/config/encryptConf.do"] != 1 {
		t.Fatalf("initial request counts = %#v", counts)
	}

	session, err := loginWithCreds(context.Background(), user, password, "ABCD")
	if err != nil {
		t.Fatalf("captcha retry login error = %v", err)
	}
	if session.SessionKey != "session-key" || session.SessionSecret != "session-secret" {
		t.Fatalf("session = %#v", session)
	}
	if session.Username != user || session.Password != password {
		t.Fatalf("attached credentials = %q/%q", session.Username, session.Password)
	}
	if counts["cloud.189.cn/api/portal/unifyLoginForPC.action"] != 1 || counts["open.e.189.cn/api/logbox/config/encryptConf.do"] != 1 || counts["open.e.189.cn/api/logbox/oauth2/needcaptcha.do"] != 1 || counts["open.e.189.cn/api/logbox/oauth2/picCaptcha.do"] != 1 {
		t.Fatalf("captcha retry restarted the login flow: %#v", counts)
	}
	if counts["open.e.189.cn/api/logbox/oauth2/loginSubmit.do"] != 1 || counts["api.cloud.189.cn/getSessionForPC.action"] != 1 {
		t.Fatalf("completion request counts = %#v", counts)
	}
	loginStateMu.Lock()
	_, pending := pendingLogins[user]
	loginStateMu.Unlock()
	if pending {
		t.Fatal("successful captcha retry left a pending login state")
	}
}

func TestPan189CaptchaFailureMarker(t *testing.T) {
	for _, message := range []string{"验证码错误", "captcha invalid", "validateCode mismatch"} {
		if !isPan189CaptchaFailure(message) {
			t.Fatalf("isPan189CaptchaFailure(%q) = false", message)
		}
	}
	if isPan189CaptchaFailure("账号或密码错误") {
		t.Fatal("ordinary credential failure was classified as a captcha failure")
	}
}

func TestPan189SMSCodeFailurePreservesServiceMessage(t *testing.T) {
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return pan189AuthResponse(req, http.StatusOK, nil, `{"result":20102,"msg":"短信验证码错误"}`), nil
	})
	_, err := loginSubmit(context.Background(), &pan189LoginState{Client: netx.NewClient(0), SMSLogin: true}, "")
	if err == nil || !strings.Contains(err.Error(), "短信验证码错误") || !strings.Contains(err.Error(), "20102") || strings.Contains(err.Error(), "captcha_retry_189") {
		t.Fatalf("SMS validation error was lost or treated as password captcha failure: %v", err)
	}
}

func TestPan189SessionHTTPErrorPreservesServiceCode(t *testing.T) {
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return pan189AuthResponse(req, http.StatusBadRequest, nil, `{"res_message":"loginResp is null","res_code":"LoginRespIsNull"}`), nil
	})
	_, err := getSessionForPC(context.Background(), &pan189LoginState{Client: netx.NewClient(0)}, "https://cloud.189.cn/login?ticket=test", "test")
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "LoginRespIsNull") {
		t.Fatalf("session error = %v, want HTTP status and service code", err)
	}
}

func TestPan189RefreshPageRebuildsLoginOnlyOnce(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(der)
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	for _, alwaysExpired := range []bool{false, true} {
		initializations, submits := 0, 0
		netx.TestTransportHook = pan189AuthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := ""
			switch r.URL.Path {
			case "/api/portal/unifyLoginForPC.action":
				initializations++
				body = `<input 'captchaToken' value='captcha'><script>var lt = "0123456789abcdef"; var paramId = "abcdef0123456789"; var reqId = "req";</script>`
			case "/api/logbox/config/encryptConf.do":
				body = fmt.Sprintf(`{"data":{"pubKey":%q,"pre":"PRE"}}`, pub)
			case "/api/logbox/oauth2/needcaptcha.do":
				body = "0"
			case "/api/logbox/oauth2/loginSubmit.do":
				submits++
				body = `{"toUrl":"https://cloud.189.cn/login?ticket=test"}`
				if submits == 1 || alwaysExpired {
					body = `{"msg":"刷新页面后重试"}`
				}
			case "/getSessionForPC.action":
				body = `{"sessionKey":"key","sessionSecret":"secret"}`
			default:
				return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
			}
			return pan189AuthResponse(r, 200, nil, body), nil
		})
		session, err := loginWithCreds(context.Background(), "test-refresh-user", "password", "")
		if initializations != 2 || submits != 2 || (err != nil) != alwaysExpired || (!alwaysExpired && session.SessionKey != "key") {
			t.Fatalf("inits=%d submits=%d session=%v err=%v", initializations, submits, session, err)
		}
	}
}

func TestPan189SMSSendDiscardsExpiredPage(t *testing.T) {
	const user = "13800138000"
	old := pan189SMSLogins
	t.Cleanup(func() { pan189SMSLogins = old })
	hc := netx.NewClient(time.Second)
	sends := 0
	hc.HTTP.Transport = pan189AuthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "smsNeedcaptcha.do") {
			return pan189AuthResponse(r, 200, nil, "1"), nil
		}
		sends++
		return pan189AuthResponse(r, 200, nil, `{"result":-2,"msg":"刷新页面后重试"}`), nil
	})
	pan189SMSLogins = map[string]*pan189SMSState{user: {Login: &pan189LoginState{User: user, Client: hc, CreatedAt: time.Now()}}}
	_, err := RequestPan189SMS(context.Background(), user, "")
	if err == nil || !strings.Contains(err.Error(), "-2") {
		t.Fatalf("missing provider error code: %v", err)
	}
	if pan189SMSLogins[user] != nil {
		t.Fatal("expired login page retained for the next SMS request")
	}
	if sends != 1 {
		t.Fatalf("SMS send replayed %d times", sends)
	}
}

func TestPan189SMSLoginUsesCaptchaSessionAndNeverStoresOTP(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pub := base64.StdEncoding.EncodeToString(der)
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old; pan189SMSLogins = map[string]*pan189SMSState{} })
	pan189SMSLogins = map[string]*pan189SMSState{}
	inits, sends := 0, 0
	netx.TestTransportHook = pan189AuthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/api/portal/unifyLoginForPC.action":
			inits++
			body = `<input 'captchaToken' value='password-captcha'><p id="j-sms-captcha"><input name='captchaToken' value='sms-captcha'></p><script>var lt = "0123456789abcdef"; var paramId = "abcdef0123456789"; var reqId = "req"; var pageKey = "normal";</script>`
		case "/api/logbox/config/encryptConf.do":
			body = fmt.Sprintf(`{"data":{"pubKey":%q,"pre":"PRE"}}`, pub)
		case "/api/logbox/oauth2/smsNeedcaptcha.do":
			body = "0"
		case "/api/logbox/oauth2/picCaptcha.do":
			if r.URL.Query().Get("token") != "sms-captcha" {
				t.Fatal("used password captcha for SMS")
			}
			body = "012345678901234567890123456789"
		case "/api/logbox/oauth2/web/sendSmsCode.do":
			sends++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("captchaToken") != "sms-captcha" || r.Form.Get("validateCode") != "ABCD" {
				t.Fatalf("SMS send form=%v", r.Form)
			}
			return pan189AuthResponse(r, 200, http.Header{"Set-Cookie": {"sms_session=retained; Path=/; Secure"}}, `{"result":0}`), nil
		case "/api/logbox/oauth2/loginSubmit.do":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("dynamicCheck") != "TRUE" || r.Form.Get("smsValidateCode") != "ABCD" || r.Form.Get("captchaToken") != "sms-captcha" || r.Form.Get("password") != "" {
				t.Fatalf("SMS submit form=%v", r.Form)
			}
			if !strings.Contains(r.Header.Get("Cookie"), "sms_session=retained") {
				t.Fatal("lost SMS cookies")
			}
			cipher, err := hex.DecodeString(strings.TrimPrefix(r.Form.Get("epd"), "PRE"))
			if err != nil {
				t.Fatal(err)
			}
			otp, err := rsa.DecryptPKCS1v15(rand.Reader, key, cipher)
			if err != nil || string(otp) != "123456" {
				t.Fatalf("wrong encrypted OTP: %s %v", otp, err)
			}
			body = `{"toUrl":"https://cloud.189.cn/login?ticket=test"}`
		case "/getSessionForPC.action":
			body = `{"sessionKey":"key","sessionSecret":"secret","accessToken":"access","refreshToken":"refresh"}`
		default:
			return nil, fmt.Errorf("unexpected request %s", r.URL.Path)
		}
		return pan189AuthResponse(r, 200, nil, body), nil
	})
	image, err := RequestPan189SMS(context.Background(), "13800138000", "")
	if err != nil || image == "" || sends != 0 {
		t.Fatalf("image=%s err=%v sends=%d", image, err, sends)
	}
	image, err = RequestPan189SMS(context.Background(), "13800138000", "ABCD")
	if err != nil || image != "" || sends != 1 || inits != 1 {
		t.Fatalf("image=%s err=%v sends=%d inits=%d", image, err, sends, inits)
	}
	if _, err := RequestPan189SMS(context.Background(), "13800138000", "ABCD"); err == nil {
		t.Fatal("missing resend cooldown")
	}
	token, err := login189(context.Background(), drive.AuthRequest{Config: map[string]string{"username": "13800138000", "login_mode": "sms", "sms_code": "123456"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token.RefreshToken, "123456") || strings.Contains(token.RefreshToken, `"password"`) {
		t.Fatal("persisted OTP as account password")
	}
	if _, err := loginPan189BySMS(context.Background(), "13800138000", "123456"); err == nil {
		t.Fatal("reused completed SMS session")
	}
}
