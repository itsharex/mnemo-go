package pan139

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

func TestParsePan139QuotaSupportsResponseVariants(t *testing.T) {
	used, total, ok := parsePan139Quota([]byte(`{"usedSize":"12","totalSize":100}`))
	if !ok || used != 12 || total != 100 {
		t.Fatalf("string quota = %d/%d ok=%v, want 12/100 true", used, total, ok)
	}

	used, total, ok = parsePan139Quota([]byte(`{"data":{"used":120,"diskSize":"100"}}`))
	if !ok || used != 100 || total != 100 {
		t.Fatalf("nested clamped quota = %d/%d ok=%v, want 100/100 true", used, total, ok)
	}

	used, total, ok = parsePan139Quota([]byte(`{"freeSize":"80","totalSize":"100"}`))
	if !ok || used != 20 || total != 100 {
		t.Fatalf("derived free quota = %d/%d ok=%v, want 20/100 true", used, total, ok)
	}

	if _, _, ok = parsePan139Quota([]byte(`{"usedSize":"12"}`)); ok {
		t.Fatal("quota without a total size should not be accepted")
	}
}

func TestPan139S305ExplainsAccountPasswordRequirement(t *testing.T) {
	hc := netx.NewClient(time.Second)
	hc.HTTP.Transport = pan139RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return pan139Response(r, 302, http.Header{"Location": {"https://mail.10086.cn/default.html?ec=S305"}}, ""), nil
	})
	hc.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_, _, err := submitPan139Login(context.Background(), &pan139LoginState{Client: hc, Username: "test", Password: "test"}, false, "")
	if err == nil || !strings.Contains(err.Error(), "移动认证账号密码") {
		t.Fatalf("error=%v", err)
	}
}

func TestPan139AuthorizationExpirationUsesFourthTokenField(t *testing.T) {
	expires := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	for _, suffix := range []string{"", "|metadata", "|metadata|0"} {
		token := fmt.Sprintf("token|a|b|%d%s", expires, suffix)
		auth := encodeAuthorization("pc", "13800138000", token)
		_, account, gotToken, _, gotExpiry, err := decodeAuthorization(auth)
		if err != nil || account != "13800138000" || gotToken != token || gotExpiry != expires {
			t.Fatalf("extended authorization decode: expiry=%d err=%v", gotExpiry, err)
		}
	}
	_, _, _, _, _, err := decodeAuthorization(encodeAuthorization("pc", "13800138000", "token|a|b|123invalid"))
	if err == nil {
		t.Fatal("accepted malformed expiration")
	}
}

func TestPan139StandaloneSMSSendsWithoutPasswordAndReusesCookies(t *testing.T) {
	resetPan139LoginStatesForTest(t)
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	sends := 0
	netx.TestTransportHook = pan139RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/s":
			sends++
			if r.URL.Query().Get("func") != "login:sendSmsCode" {
				t.Fatalf("wrong SMS endpoint: %s", r.URL)
			}
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "scene") || strings.Contains(string(body), "13800138000") {
				t.Fatalf("unexpected SMS body: %s", body)
			}
			return pan139Response(r, 200, http.Header{"Set-Cookie": {"JSESSIONID=sms-session; Path=/; Secure"}}, `{"code":"S_OK"}`), nil
		case "/Login/Login.ashx":
			if r.Method != http.MethodPost {
				t.Fatal("direct SMS must not submit a password preflight")
			}
			if !strings.Contains(r.Header.Get("Cookie"), "JSESSIONID=sms-session") {
				t.Fatal("lost SMS session cookie")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("reqFrom") != "3" || r.Form.Get("passOld") != "" || r.Form.Get("Password") != sha1Hex("fetion.com.cn:123456") {
				t.Fatalf("incorrect SMS submission: %v", r.Form)
			}
			if r.Form.Get("loginFailureUrl") != mailHostURL+"/default.html?smsLogin=1" || r.URL.Query().Get("_") != sha1Hex("13800138000") {
				t.Fatal("SMS login must retain the official form's mode and account digest")
			}
			return pan139Response(r, 302, http.Header{"Location": {"https://mail.10086.cn/?sid=sms-session-id"}}, ""), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", r.URL)
		}
	})
	if err := RequestPan139SMS(context.Background(), "13800138000"); err != nil {
		t.Fatal(err)
	}
	if err := RequestPan139SMS(context.Background(), "13800138000"); err == nil {
		t.Fatal("missing resend cooldown")
	}
	state := loadPan139LoginState("13800138000")
	if state == nil || state.Password != "" {
		t.Fatal("SMS requires or retains password")
	}
	_, sid, err := submitPan139Login(context.Background(), state, true, "123456")
	if err != nil || sid != "sms-session-id" || sends != 1 {
		t.Fatalf("sid=%s err=%v sends=%d", sid, err, sends)
	}
}

func TestApplyPan139QuotaPreservesLastKnownValueOnMissingQuota(t *testing.T) {
	token := &model.TokenInfo{UsedSize: 2, TotalSize: 10, FreeSize: 8}
	applyPan139Quota(token, 0, 0)
	if token.UsedSize != 2 || token.TotalSize != 10 || token.FreeSize != 8 {
		t.Fatalf("missing quota replaced last known values: %#v", token)
	}
}

func TestPan139SMSPublicKeyEncryptsAccountName(t *testing.T) {
	ciphertext, err := rsaEncryptPan139LoginName("13800138000")
	if err != nil {
		t.Fatalf("rsaEncryptPan139LoginName() error = %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatalf("encrypted account is not base64: %v", err)
	}
	if len(raw) != 256 {
		t.Fatalf("encrypted account length = %d, want 256-byte RSA ciphertext", len(raw))
	}
}

type pan139RoundTripFunc func(*http.Request) (*http.Response, error)

func (fn pan139RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func pan139Response(req *http.Request, status int, headers http.Header, body string) *http.Response {
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

func TestPan139DownloadRoutesUsingAccountAndPreservesCredentials(t *testing.T) {
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	account := "13800138000"
	authorization := encodeAuthorization("pc", account, fmt.Sprintf("token|a|b|%d", time.Now().Add(30*24*time.Hour).UnixMilli()))
	netx.TestTransportHook = pan139RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user/route/qryRoutePolicy":
			var body struct {
				UserInfo struct {
					AccountName string `json:"accountName"`
				} `json:"userInfo"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			if body.UserInfo.AccountName != account {
				return pan139Response(r, 200, nil, `{"success":false,"message":"账号不存在"}`), nil
			}
			return pan139Response(r, 200, nil, `{"success":true,"data":{"routePolicyList":[{"modName":"personal","httpsUrl":"https://personal.139.test"}]}}`), nil
		case "/file/getDownloadUrl":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			if body["fileId"] != "9007199254740993" {
				t.Errorf("file id lost precision: %s", body["fileId"])
			}
			return pan139Response(r, 200, nil, `{"success":true,"data":{"cdnUrl":"https://cdn.139.test/file?sign=a%2Bb","size":"42"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", r.URL)
		}
	})
	tok := &model.TokenInfo{AccessToken: authorization, RefreshToken: `{"username":"13800138000","password":"test-password","mailCookies":"RMKEY=test","customField":{"keep":true}}`}
	u, err := (&Driver{}).GetDownloadURL(context.Background(), drive.Context{Token: tok}, "9007199254740993", 0)
	if err != nil {
		t.Fatal(err)
	}
	if u.URL != "https://cdn.139.test/file?sign=a%2Bb" || u.Size != 42 {
		t.Fatalf("download=%+v", u)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(tok.RefreshToken), &saved); err != nil {
		t.Fatal(err)
	}
	if string(saved["account"]) != `"13800138000"` || string(saved["password"]) != `"test-password"` || len(saved["customField"]) == 0 {
		t.Fatal("session update lost account identity or saved credentials")
	}
}

func TestPan139DownloadHonorsCDNSwitch(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"disabled", `{"cdnSwitch":false,"cdnUrl":"https://cdn.test/stale","url":"https://origin.test/file"}`, "https://origin.test/file"},
		{"enabled", `{"cdnSwitch":true,"cdnUrl":"https://cdn.test/file","url":"https://origin.test/file"}`, "https://cdn.test/file"},
		{"missing flag with origin", `{"cdnUrl":"https://cdn.test/stale","url":"https://origin.test/file"}`, "https://origin.test/file"},
		{"legacy cdn only", `{"cdnUrl":"https://cdn.test/file"}`, "https://cdn.test/file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := netx.TestTransportHook
			t.Cleanup(func() { netx.TestTransportHook = old })
			netx.TestTransportHook = pan139RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return pan139Response(r, 200, nil, `{"success":true,"data":`+tc.data+`}`), nil
			})
			tok := &model.TokenInfo{AccessToken: encodeAuthorization("pc", "13800138000", fmt.Sprintf("t|a|b|%d", time.Now().Add(30*24*time.Hour).UnixMilli())), RefreshToken: `{"personalCloudHost":"https://personal.test"}`}
			got, _, err := (&Driver{}).DownloadInfo(context.Background(), drive.Context{Token: tok}, "file")
			if err != nil || got != tc.want {
				t.Fatalf("url=%s err=%v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestPan139RiskSceneUsesSMSXMLLogin(t *testing.T) {
	for _, tc := range []struct{ code, scene string }{{"PML401010062", "2"}, {"MW0016", "4"}, {"S025", "1"}, {"S035", "1"}} {
		t.Run(tc.code, func(t *testing.T) {
			resetPan139LoginStatesForTest(t)
			old := netx.TestTransportHook
			t.Cleanup(func() { netx.TestTransportHook = old })
			netx.TestTransportHook = pan139RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return pan139Response(r, 200, nil, ""), nil
				}
				if r.URL.Path == "/Login/Login.ashx" {
					return pan139Response(r, 302, http.Header{"Location": []string{"https://mail.10086.cn/default.html?ec=" + tc.code}}, ""), nil
				}
				data, _ := io.ReadAll(r.Body)
				switch r.URL.Query().Get("func") {
				case "login:sendSmsCodeByScene":
					if !strings.Contains(string(data), `name="scene">`+tc.scene+`</string>`) {
						t.Errorf("wrong SMS scene")
					}
					return pan139Response(r, 200, http.Header{"Set-Cookie": []string{"RMKEY=updated; Path=/"}}, `{"code":"S_OK"}`), nil
				case "/login/inlogin.action":
					if !strings.Contains(string(data), `name="loginPassword">`+sha1Hex("fetion.com.cn:123456")) || strings.Contains(string(data), "123456") {
						t.Error("wrong SMS proof")
					}
					if !strings.Contains(r.Header.Get("Cookie"), "RMKEY=updated") {
						t.Error("SMS session cookie not retained")
					}
					return pan139Response(r, 200, nil, `{"code":"S_OK","var":{"loginSuccessUrl":"https://mail.10086.cn/main?sid=verified-sid"}}`), nil
				default:
					return nil, fmt.Errorf("unexpected request %s", r.URL)
				}
			})
			_, err := loginByPassword(context.Background(), "13800138000", "password", "")
			var needSMS pan139SMSRequiredError
			if !errors.As(err, &needSMS) {
				t.Fatalf("expected SMS challenge, got %v", err)
			}
			if err := RequestPan139SMS(context.Background(), "13800138000"); err != nil {
				t.Fatal(err)
			}
			state := loadPan139LoginState("13800138000")
			_, sid, err := submitPan139Login(context.Background(), state, true, "123456")
			if err != nil || sid != "verified-sid" {
				t.Fatalf("sid=%q err=%v", sid, err)
			}
		})
	}
}

func resetPan139LoginStatesForTest(t *testing.T) {
	t.Helper()
	pan139LoginStateMu.Lock()
	pan139LoginStates = map[string]*pan139LoginState{}
	pan139LoginStateMu.Unlock()
	t.Cleanup(func() {
		pan139LoginStateMu.Lock()
		pan139LoginStates = map[string]*pan139LoginState{}
		pan139LoginStateMu.Unlock()
	})
}

func pan139ECBEncryptForTest(t *testing.T, plaintext, keyHex string) string {
	t.Helper()
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padded := pkcs7Pad([]byte(plaintext), block.BlockSize())
	ciphertext := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += block.BlockSize() {
		block.Encrypt(ciphertext[offset:offset+block.BlockSize()], padded[offset:offset+block.BlockSize()])
	}
	return hex.EncodeToString(ciphertext)
}

func TestPan139PasswordUpgradeUsesOneCookieSessionForSMS(t *testing.T) {
	resetPan139LoginStatesForTest(t)
	oldTransport := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = oldTransport })

	const username = "13800138000"
	const password = "password"
	const smsCode = "123456"
	const sid = "sid-from-sms"

	inner, err := json.Marshal(map[string]string{
		"authToken":    "auth-token",
		"account":      "cloud-account",
		"userDomainId": "domain-id",
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]string{
		"data": pan139ECBEncryptForTest(t, string(inner), pan139ThirdLoginKey2),
	})
	if err != nil {
		t.Fatal(err)
	}
	encryptedEnvelope := aesCBCEncryptBase64Payload(string(envelope), pan139ThirdLoginKey1)
	if encryptedEnvelope == "" {
		t.Fatal("test SSO envelope encryption failed")
	}

	netx.TestTransportHook = pan139RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "mail.10086.cn" && req.URL.Path == "/Login/Login.ashx" && req.Method == http.MethodGet:
			headers := make(http.Header)
			headers.Add("Set-Cookie", "JSESSIONID=fresh-session; Path=/; Secure")
			headers.Add("Set-Cookie", "RMKEY=rm-key; Path=/; Secure")
			return pan139Response(req, http.StatusOK, headers, "login"), nil

		case req.URL.Host == "mail.10086.cn" && req.URL.Path == "/Login/Login.ashx" && req.Method == http.MethodPost:
			if !strings.Contains(req.Header.Get("Cookie"), "JSESSIONID=fresh-session") {
				return nil, errors.New("password/SMS request did not retain the preflight JSESSIONID")
			}
			if !strings.Contains(req.Header.Get("Referer"), "cguid=") {
				return nil, errors.New("password/SMS request is missing cguid referer")
			}
			body, readErr := io.ReadAll(req.Body)
			if readErr != nil {
				return nil, readErr
			}
			values, parseErr := url.ParseQuery(string(body))
			if parseErr != nil {
				return nil, parseErr
			}
			if values.Get("UserName") != username {
				return nil, fmt.Errorf("username = %q, want %q", values.Get("UserName"), username)
			}
			headers := make(http.Header)
			switch values.Get("reqFrom") {
			case "0":
				if values.Get("Password") != sha1Hex("fetion.com.cn:"+password) {
					return nil, errors.New("password request does not use the expected hash")
				}
				headers.Set("Location", "https://mail.10086.cn/default.html?ec=S046")
				return pan139Response(req, http.StatusFound, headers, ""), nil
			case "3":
				if values.Get("passOld") != "" || values.Get("Password") != sha1Hex("fetion.com.cn:"+smsCode) {
					return nil, errors.New("SMS request does not contain the expected code fields")
				}
				headers.Set("Location", "https://mail.10086.cn/default.html?sid="+sid)
				return pan139Response(req, http.StatusFound, headers, ""), nil
			default:
				return nil, fmt.Errorf("unexpected reqFrom %q", values.Get("reqFrom"))
			}

		case req.URL.Host == "mail.10086.cn" && req.URL.Path == "/s" && req.URL.Query().Get("func") == "login:sendSmsCodeByScene":
			body, readErr := io.ReadAll(req.Body)
			if readErr != nil {
				return nil, readErr
			}
			if !strings.Contains(string(body), "name=\"scene\">5<") || !strings.Contains(string(body), "name=\"loginName\">") {
				return nil, errors.New("SMS send request is missing the account-scene payload")
			}
			return pan139Response(req, http.StatusOK, nil, `{"code":"S_OK"}`), nil

		case req.URL.Host == "smsrebuild1.mail.10086.cn" && req.URL.Path == "/setting/s":
			if !strings.Contains(req.Header.Get("Cookie"), "RMKEY=rm-key") {
				return nil, errors.New("artifact request is missing RMKEY")
			}
			return pan139Response(req, http.StatusOK, nil, `{"artifact":"artifact-value"}`), nil

		case req.URL.Host == "user-njs.yun.139.com" && req.URL.Path == "/user/thirdlogin":
			body, readErr := io.ReadAll(req.Body)
			if readErr != nil {
				return nil, readErr
			}
			if strings.TrimSpace(string(body)) == "" {
				return nil, errors.New("thirdlogin request body is empty")
			}
			return pan139Response(req, http.StatusOK, nil, encryptedEnvelope), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL.String())
		}
	})

	_, err = loginByPassword(context.Background(), username, password, "")
	var smsRequired pan139SMSRequiredError
	if !errors.As(err, &smsRequired) {
		t.Fatalf("password login error = %v, want SMS-required error", err)
	}
	state := loadPan139LoginState(username)
	if state == nil {
		t.Fatal("password login did not retain the SMS continuation state")
	}
	if state.Password != "" {
		t.Fatal("SMS continuation state retained the account password")
	}

	if err := RequestPan139SMS(context.Background(), username); err != nil {
		t.Fatalf("RequestPan139SMS() error = %v", err)
	}
	if err := RequestPan139SMS(context.Background(), username); err == nil || !strings.Contains(err.Error(), "秒后再试") {
		t.Fatalf("duplicate RequestPan139SMS() error = %v, want local cooldown", err)
	}
	authorization, err := loginBySMS(context.Background(), username, smsCode)
	if err != nil {
		t.Fatalf("loginBySMS() error = %v", err)
	}
	wantAuthorization := "cGM6Y2xvdWQtYWNjb3VudDphdXRoLXRva2Vu"
	if authorization != wantAuthorization {
		t.Fatalf("authorization = %q, want %q", authorization, wantAuthorization)
	}
	if loadPan139LoginState(username) != nil {
		t.Fatal("completed SMS login left a reusable password login state")
	}
}

func TestCreateShareUsesPersonalOutlinkAPI(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	authorization := encodeAuthorization("test", "13800138000", "token|a|b|4102444800000|metadata")
	var requests int
	netx.TestTransportHook = pan139RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodPost || req.URL.Host != "api.139.test" || req.URL.Path != "/orchestration/personalCloud-rebuild/outlink/v1.0/getOutLink" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL.String())
		}
		if req.Header.Get("Authorization") != "Basic "+authorization {
			return nil, fmt.Errorf("authorization = %q", req.Header.Get("Authorization"))
		}
		var payload struct {
			GetOutLinkReq struct {
				Period  int      `json:"period"`
				CAIDLst []string `json:"caIDLst"`
				COIDLst []string `json:"coIDLst"`
				Encrypt int      `json:"encrypt"`
			} `json:"getOutLinkReq"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if payload.GetOutLinkReq.Period != 7 || strings.Join(payload.GetOutLinkReq.CAIDLst, ",") != "folder-1" || strings.Join(payload.GetOutLinkReq.COIDLst, ",") != "file-1" || payload.GetOutLinkReq.Encrypt != 1 {
			return nil, fmt.Errorf("outlink payload = %+v", payload.GetOutLinkReq)
		}
		return pan139Response(req, http.StatusOK, nil, `{"success":true,"data":{"getOutLinkRes":{"getOutLinkResSet":[{"linkID":"share-139","linkUrl":"https://yun.139.com/w/i/share-139","passwd":"a1b2"}]}}}`), nil
	})

	folder := true
	token := &model.TokenInfo{
		AccessToken:  authorization,
		RefreshToken: mustJSON(map[string]string{"authorization": authorization, "account": "13800138000", "personalCloudHost": "https://api.139.test"}),
	}
	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{UserID: "pan139:13800138000", DriveID: "pan139:13800138000", Token: token}, drive.ShareParams{
		FileIDs:    []string{"file-1", "folder-1"},
		FileRefs:   []drive.FileRef{{ID: "file-1"}, {ID: "folder-1", IsDir: &folder}},
		ShareName:  "测试分享",
		Expiration: "7",
	})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
	if item.ShareID != "share-139" || item.ShareURL != "https://yun.139.com/w/i/share-139" || item.SharePwd != "a1b2" || len(item.FileIDList) != 2 {
		t.Fatalf("share = %+v", item)
	}
}

func TestPan139ShareExpirationRejectsUnsupportedDuration(t *testing.T) {
	_, err := (&Driver{}).CreateShare(context.Background(), drive.Context{}, drive.ShareParams{FileIDs: []string{"file-1"}, Expiration: time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)})
	if err == nil || !strings.Contains(err.Error(), "1 天、7 天或永久") {
		t.Fatalf("unsupported expiration error = %v", err)
	}
}
