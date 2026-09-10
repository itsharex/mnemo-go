// Package pan139 implements the 139 cloud drive provider (AList-sourced
// personal_new API with mcloud signature headers).
package pan139

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const (
	routeURL                   = "https://user-njs.yun.139.com/user/route/qryRoutePolicy"
	refreshURL                 = "https://aas.caiyun.feixin.10086.cn:443/tellin/authTokenRefresh.do"
	mailHostURL                = "https://mail.10086.cn"
	mailLoginURL               = "https://mail.10086.cn/Login/Login.ashx"
	mailSMSURL                 = "https://mail.10086.cn/s"
	mailArtifactURL            = "https://smsrebuild1.mail.10086.cn/setting/s"
	thirdLoginURL              = "https://user-njs.yun.139.com/user/thirdlogin"
	ua                         = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	RootID                     = "pan139_root"
	pan139UploadPartSize       = int64(100 * 1024 * 1024)
	pan139LargePartSize        = int64(200 * 1024 * 1024)
	pan139LargeUploadThreshold = int64(30 * 1024 * 1024 * 1024)
	pan139MaxPartsPerRequest   = 100
	pan139SMSStateTTL          = 5 * time.Minute
	pan139SMSMinInterval       = time.Minute
	pan139ThirdLoginKey1       = "73634235495062495331515373756c734e7253306c673d3d"
	pan139ThirdLoginKey2       = "7150714477323633586746674c337538"
	pan139SMSPhonePublicKey    = "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtu3kxYStVjSCblI8+Fe6C1M4J6cnsGZ12a5Dnz/kg2dwsYbYFsXZJNVzsJjnch8Sy6/WuPKeZCPwjTodI+I2Uqm1B4cmR71mv79GCuWJOFQddO8Qtm8R76xkujUp2ugw3OyuTklv8CQslapDzzoZ2iEAc8jTmsqA6anvWBscaijbCQMmpQj/iOTyu68S+W04gUIImmE62dUf2dpxUcozV5bCXdu16ykf1Ks7M68u6NndO+QbpX++ZyJztc+cNlhumFRHL+rt3o5gDiEaA4C2Z5OyCYCa/MkkZbU6Y5SU7ei1QpVVNWn9pFEyF/NbqgyhwuTwBBr+/bOT4K6KbuYbLwIDAQAB"
)

const providerID = model.ProviderPan139

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"copy":            true,
			"createShare":     true,
			"shareExpiration": true,
			"combinedShare":   true,
			"shareHistory":    true,
			"recycleBin":      true,
			"permanentDelete": true,
		}, func(c *drive.Capabilities) {
			c.SetHashes([]string{"sha256"}, []string{"sha256"})
			c.SetShareExpirationOptions(0, 1, 7)
		}),
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "login_mode", Type: "select", Label: "登录方式", Required: true, Options: []drive.LoginOption{{Value: "password", Label: "账号密码"}, {Value: "sms", Label: "短信验证码"}}},
			{Key: "username", Type: "text", Label: "手机号/账号", Required: true},
			{Key: "password", Type: "password", Label: "密码", Required: false},
			{Key: "sms_code", Type: "text", Label: "短信验证码", Required: false},
			{Key: "authorization", Type: "text", Label: "Authorization（可选，粘贴直接登录）", Required: false},
			{Key: "mail_cookies", Type: "text", Label: "Cookie（可选）", Required: false},
		}},
		Auth:    authLogin,
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// ---- crypto helpers ----

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// aesCBCEncryptBase64Payload: iv||ciphertext base64 (random iv).
func aesCBCEncryptBase64Payload(plaintext, keyHex string) string {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return ""
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	iv := make([]byte, block.BlockSize())
	_, _ = rand.Read(iv)
	padded := pkcs7Pad([]byte(plaintext), block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	combined := append(iv, out...)
	return base64.StdEncoding.EncodeToString(combined)
}

// aesCBCDecryptFromBase64Payload reverses iv||ciphertext.
func aesCBCDecryptFromBase64Payload(b64, keyHex string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < 16 {
		return "", errors.New("pan139: ciphertext too short")
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := raw[:block.BlockSize()]
	ct := raw[block.BlockSize():]
	if len(ct) == 0 || len(ct)%block.BlockSize() != 0 {
		return "", errors.New("pan139: ciphertext block size invalid")
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	return string(pkcs7Unpad(out, block.BlockSize())), nil
}

// aesECBDecryptHex decrypts ECB hex ciphertext.
func aesECBDecryptHex(hexCipher, keyHex string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", err
	}
	ct, err := hex.DecodeString(hexCipher)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	if len(ct) == 0 || len(ct)%block.BlockSize() != 0 {
		return "", errors.New("pan139: ECB ciphertext block size invalid")
	}
	out := make([]byte, len(ct))
	for i := 0; i < len(ct); i += block.BlockSize() {
		block.Decrypt(out[i:i+block.BlockSize()], ct[i:i+block.BlockSize()])
	}
	return string(pkcs7Unpad(out, block.BlockSize())), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) []byte {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return data
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > blockSize || pad > len(data) {
		return data
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return data
		}
	}
	return data[:len(data)-pad]
}

// sortedJSONStringify renders JSON with sorted keys (AList style).
func sortedJSONStringify(v any) string {
	b, _ := json.Marshal(sortedValue(v))
	return string(b)
}

func sortedValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m := make(map[string]any, len(t))
		for _, k := range keys {
			m[k] = sortedValue(t[k])
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = sortedValue(x)
		}
		return out
	default:
		return v
	}
}

// calSign computes the mcloud sign (AList calSign).
func calSign(body, ts, randStr string) string {
	enc := encodeURIComponent139(body)
	chars := strings.Split(enc, "")
	sort.Strings(chars)
	b := base64.StdEncoding.EncodeToString([]byte(strings.Join(chars, "")))
	res := md5hex(b) + md5hex(ts+":"+randStr)
	return strings.ToUpper(md5hex(res))
}

func encodeURIComponent139(s string) string {
	r := url.QueryEscape(s)
	r = strings.ReplaceAll(r, "+", "%20")
	r = strings.ReplaceAll(r, "%21", "!")
	r = strings.ReplaceAll(r, "%27", "'")
	r = strings.ReplaceAll(r, "%28", "(")
	r = strings.ReplaceAll(r, "%29", ")")
	r = strings.ReplaceAll(r, "%2A", "*")
	return r
}

func formatTs() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// normalizeAuthorization removes an optional case-insensitive Basic scheme.
func normalizeAuthorization(authorization string) string {
	authorization = strings.TrimSpace(authorization)
	if len(authorization) > len("Basic") && strings.EqualFold(authorization[:len("Basic")], "Basic") {
		separator := authorization[len("Basic")]
		if separator == ' ' || separator == '\t' {
			return strings.TrimSpace(authorization[len("Basic"):])
		}
	}
	return authorization
}

var errPan139Expiration = errors.New("pan139: authorization expiration 无效")

// decodeAuthorization parses Basic base64(user:account:token|...|expiration).
func decodeAuthorization(authorization string) (raw, account, tokenPart, splits0 string, expiration int64, err error) {
	authorization = normalizeAuthorization(authorization)
	decoded, err := base64.StdEncoding.DecodeString(authorization)
	if err != nil {
		return "", "", "", "", 0, errors.New("pan139: authorization 无效")
	}
	splits := strings.Split(string(decoded), ":")
	if len(splits) < 3 {
		return "", "", "", "", 0, errors.New("pan139: authorization 无效")
	}
	account = splits[1]
	tokenPart = strings.Join(splits[2:], ":")
	if splits[0] == "" || account == "" || tokenPart == "" {
		return "", "", "", "", 0, errors.New("pan139: authorization 无效")
	}
	strs := strings.Split(tokenPart, "|")
	if len(strs) < 4 {
		return "", "", "", "", 0, errors.New("pan139: authorization token 无效")
	}
	// Cloud SSO tokens can append metadata after the fourth field.
	// The expiration stays at index 3 (the provider's reference client contract).
	expiration, err = strconv.ParseInt(strs[3], 10, 64)
	if err != nil || expiration <= 0 {
		return authorization, account, tokenPart, splits[0], 0, errPan139Expiration
	}
	return authorization, account, tokenPart, splits[0], expiration, nil
}

func encodeAuthorization(splits0, account, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(splits0 + ":" + account + ":" + token))
}

// refreshAuthorization refreshes a near-expiry token.
func refreshAuthorization(hc *netx.Client, authorization string) (string, error) {
	_, account, tokenPart, splits0, expiration, err := decodeAuthorization(authorization)
	if err != nil && !errors.Is(err, errPan139Expiration) {
		return "", err
	}
	remain := expiration - time.Now().UnixMilli()
	if remain > 15*24*60*60*1000 {
		return normalizeAuthorization(authorization), nil
	}
	if expiration > 0 && remain < 0 {
		return "", errors.New("authorization 已过期，请重新登录")
	}
	// Unknown expiry metadata is not proof of an invalid credential. Let the
	// refresh endpoint validate it once, without inventing a local expiry.
	var escapedToken, escapedAccount strings.Builder
	_ = xml.EscapeText(&escapedToken, []byte(tokenPart))
	_ = xml.EscapeText(&escapedAccount, []byte(account))
	reqBody := fmt.Sprintf("<root><token>%s</token><account>%s</account><clienttype>656</clienttype></root>", escapedToken.String(), escapedAccount.String())
	resp, err := hc.Do(context.Background(), http.MethodPost, refreshURL, map[string]string{"Content-Type": "application/xml", "User-Agent": ua}, strings.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if readErr != nil {
		return "", fmt.Errorf("读取 139 token 刷新响应失败：%w", readErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("刷新 139 token 失败：HTTP %d", resp.StatusCode)
	}
	ret := regexp.MustCompile(`<return>([^<]*)</return>`).FindStringSubmatch(string(body))
	tok := regexp.MustCompile(`<token>([^<]*)</token>`).FindStringSubmatch(string(body))
	if len(ret) < 2 || ret[1] != "0" || len(tok) < 2 || tok[1] == "" {
		desc := regexp.MustCompile(`<desc>([^<]*)</desc>`).FindStringSubmatch(string(body))
		msg := "刷新 139 token 失败"
		if len(desc) > 1 {
			msg += ": " + desc[1]
		}
		return "", errors.New(msg)
	}
	next := encodeAuthorization(splits0, account, html.UnescapeString(tok[1]))
	if _, _, _, _, _, err := decodeAuthorization(next); err != nil {
		return "", fmt.Errorf("139 刷新后的授权格式仍无法识别，请重新登录：%w", err)
	}
	return next, nil
}

// cred is the parsed 139 session.
type cred struct {
	authorization string
	account       string
	host          string
}

// loadCred refreshes and returns the session credentials.
func loadCred(hc *netx.Client, tok *model.TokenInfo) (*cred, error) {
	if tok == nil {
		return nil, errors.New("139 云盘未登录")
	}
	auth := normalizeAuthorization(tok.AccessToken)
	var stored struct {
		Authorization     string `json:"authorization"`
		Account           string `json:"account"`
		PersonalCloudHost string `json:"personalCloudHost"`
	}
	_ = json.Unmarshal([]byte(tok.RefreshToken), &stored)
	// Preserve login fallback fields and future metadata when updating routing.
	storedFields := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(tok.RefreshToken), &storedFields)
	if storedFields == nil {
		storedFields = map[string]json.RawMessage{}
	}
	if auth == "" {
		auth = normalizeAuthorization(stored.Authorization)
	}
	if auth == "" {
		return nil, errors.New("139 云盘未登录")
	}
	account := stored.Account
	authChanged := false
	next, err := refreshAuthorization(hc, auth)
	if err != nil {
		return nil, err
	}
	_, acc, _, _, _, err := decodeAuthorization(next)
	if err != nil {
		return nil, err
	}
	account = acc
	if next != auth {
		auth = next
		tok.AccessToken = next
		authChanged = true
	}
	host := strings.TrimSuffix(stored.PersonalCloudHost, "/")
	if host == "" {
		h, err := ensureHost(hc, auth, account)
		if err != nil {
			return nil, err
		}
		host = h
	}
	if authChanged || stored.Authorization != auth || stored.Account != account || stored.PersonalCloudHost != host {
		stored.Authorization = auth
		stored.Account = account
		stored.PersonalCloudHost = host
		storedFields["authorization"] = json.RawMessage(mustJSON(auth))
		storedFields["account"] = json.RawMessage(mustJSON(account))
		storedFields["personalCloudHost"] = json.RawMessage(mustJSON(host))
		tok.RefreshToken = mustJSON(storedFields)
	}
	return &cred{authorization: auth, account: account, host: host}, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ensureHost resolves the personal cloud host via the route API.
func ensureHost(hc *netx.Client, authorization, account string) (string, error) {
	body := map[string]any{
		"userInfo":    map[string]any{"userType": 1, "accountType": 1, "accountName": account},
		"modAddrType": 1,
	}
	bodyStr, _ := json.Marshal(body)
	randStr := randomHex(8)
	ts := formatTs()
	sign := calSign(string(bodyStr), ts, randStr)
	var res struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			RoutePolicyList []struct {
				ModName  string `json:"modName"`
				HTTPSURL string `json:"httpsUrl"`
			} `json:"routePolicyList"`
		} `json:"data"`
	}
	err := hc.PostJSON(context.Background(), routeURL, mcloudHeaders(authorization, ts, randStr, sign), body, &res)
	if err != nil {
		return "", err
	}
	if !res.Success {
		return "", errors.New(res.Message)
	}
	for _, item := range res.Data.RoutePolicyList {
		if item.ModName == "personal" && item.HTTPSURL != "" {
			return strings.TrimSuffix(item.HTTPSURL, "/"), nil
		}
	}
	return "", errors.New("pan139: personal cloud host 为空")
}

func mcloudHeaders(authorization, ts, randStr, sign string) map[string]string {
	return map[string]string{
		"Accept":                 "application/json, text/plain, */*",
		"Authorization":          "Basic " + authorization,
		"CMS-DEVICE":             "default",
		"Caller":                 "web",
		"Inner-Hcy-Router-Https": "1",
		"Mcloud-Channel":         "1000101",
		"Mcloud-Client":          "10701",
		"Mcloud-Route":           "001",
		"Mcloud-Sign":            fmt.Sprintf("%s,%s,%s", ts, randStr, sign),
		"Mcloud-Version":         "7.14.0",
		"X-Yun-Api-Version":      "v1",
		"X-Yun-App-Channel":      "10000034",
		"X-Yun-Svc-Type":         "1",
		"x-DeviceInfo":           "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||",
		"x-huawei-channelSrc":    "10000034",
		"x-inner-ntwk":           "2",
		"x-m4c-caller":           "PC",
		"x-m4c-src":              "10002",
		"x-SvcType":              "1",
		"x-yun-channel-source":   "10000034",
		"x-yun-client-info":      "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||dW5kZWZpbmVk||",
		"x-yun-module-type":      "100",
		"x-yun-svc-type":         "1",
		"Origin":                 "https://yun.139.com",
		"Referer":                "https://yun.139.com/w/",
		"User-Agent":             ua,
	}
}

// personalPost signs and posts a JSON body to the personal cloud API.
func (d *Driver) personalPost(ctx context.Context, c drive.Context, pathname string, data any) (json.RawMessage, error) {
	hc := netx.NewClient(60 * time.Second)
	cr, err := loadCred(hc, c.Token)
	if err != nil {
		return nil, err
	}
	return d.personalPostWithCred(ctx, hc, cr, pathname, data)
}

// personalPostWithCred reuses an already refreshed personal-cloud session.
// Account refresh needs this to avoid renewing the same token twice before a
// single low-frequency quota request.
func (d *Driver) personalPostWithCred(ctx context.Context, hc *netx.Client, cr *cred, pathname string, data any) (json.RawMessage, error) {
	if hc == nil || cr == nil {
		return nil, errors.New("pan139: 会话不存在")
	}
	bodyStr, _ := json.Marshal(data)
	randStr := randomHex(8)
	ts := formatTs()
	sign := calSign(string(bodyStr), ts, randStr)
	url := cr.host + "/" + strings.TrimPrefix(pathname, "/")
	headers := mcloudHeaders(cr.authorization, ts, randStr, sign)
	headers["Content-Type"] = "application/json;charset=UTF-8"
	resp, err := hc.Do(ctx, http.MethodPost, url, headers, strings.NewReader(string(bodyStr)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("pan139: API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var wrapper struct {
		Success *bool           `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Success != nil && !*wrapper.Success {
		message := strings.TrimSpace(wrapper.Message)
		if message == "" {
			message = "139 云盘 API 返回失败"
		}
		return nil, errors.New(message)
	}
	return wrapper.Data, nil
}

// file139 is a raw 139 file entry.
type file139 struct {
	FileID          pan139FlexString `json:"fileId"`
	Name            string           `json:"name"`
	CatalogID       pan139FlexString `json:"catalogId"`
	ParentCatalogID pan139FlexString `json:"parentCatalogId"`
	ParentFileID    pan139FlexString `json:"parentFileId"`
	Size            pan139FlexInt64  `json:"size"`
	UpdateTime      string           `json:"updateTime"`
	CreateTime      string           `json:"createTime"`
	UpdatedAt       any              `json:"updatedAt"`
	CreatedAt       any              `json:"createdAt"`
	ContentType     string           `json:"contentType"`
	Type            string           `json:"type"`
	CatalogName     string           `json:"catalogName"`
	ContentHash     string           `json:"contentHash"`
	ContentHashAlg  string           `json:"contentHashAlgorithm"`
	Path            string           `json:"path"`
	Star            int              `json:"star"`
	IsDir           bool             `json:"-"`
}

// listData is the list response data.
type listData struct {
	Items          []file139 `json:"items"`
	LegacyItems    []file139 `json:"dataList"`
	NextPageCursor string    `json:"nextPageCursor"`
}

// pan139FlexString accepts provider ids returned as either JSON strings or numbers.
type pan139FlexString string

func (s *pan139FlexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*s = pan139FlexString(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*s = pan139FlexString(number.String())
	return nil
}

func (s pan139FlexString) String() string { return string(s) }

// pan139FlexInt64 accepts file sizes returned as JSON numbers or strings.
type pan139FlexInt64 int64

func (n *pan139FlexInt64) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*n = 0
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err == nil {
			*n = pan139FlexInt64(value)
			return nil
		}
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return err
	}
	*n = pan139FlexInt64(parsed)
	return nil
}

// parsePan139Quota accepts the field spellings used by the personal-cloud
// getDiskInfo response. The endpoint has returned both JSON numbers and
// strings across service revisions.
func parsePan139Quota(raw json.RawMessage) (used, total int64, ok bool) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return 0, 0, false
	}
	if nested, exists := values["data"]; exists {
		var nestedValues map[string]json.RawMessage
		if json.Unmarshal(nested, &nestedValues) == nil {
			values = nestedValues
		}
	}
	if nested, exists := values["diskInfo"]; exists {
		return parsePan139Quota(nested)
	}
	// The user quota API reports diskSize/freeDiskSize in MiB, unlike file APIs.
	if free, hasFree := pan139QuotaInt64(values, "freeDiskSize"); hasFree {
		total, hasTotal := pan139QuotaInt64(values, "diskSize")
		if !hasTotal || total <= 0 || total > (1<<63-1)/(1<<20) || free < 0 || free > total {
			return 0, 0, false
		}
		return (total - free) * (1 << 20), total * (1 << 20), true
	}
	used, hasUsed := pan139QuotaInt64(values, "usedSize", "used", "useSize", "used_size")
	total, hasTotal := pan139QuotaInt64(values, "totalSize", "total", "diskSize", "total_size")
	if !hasTotal || total <= 0 {
		return 0, 0, false
	}
	if !hasUsed {
		if free, hasFree := pan139QuotaInt64(values, "freeSize", "free", "availableSize", "available"); hasFree {
			used = total - free
			hasUsed = true
		}
	}
	if !hasUsed {
		return 0, 0, false
	}
	if used < 0 {
		used = 0
	}
	if used > total {
		used = total
	}
	return used, total, true
}

func pan139QuotaInt64(values map[string]json.RawMessage, keys ...string) (int64, bool) {
	for _, key := range keys {
		raw, exists := values[key]
		if !exists || string(raw) == "null" {
			continue
		}
		var value pan139FlexInt64
		if err := json.Unmarshal(raw, &value); err == nil {
			return int64(value), true
		}
	}
	return 0, false
}

func applyPan139Quota(token *model.TokenInfo, used, total int64) {
	if token == nil || total <= 0 {
		return
	}
	if used < 0 {
		used = 0
	}
	if used > total {
		used = total
	}
	token.UsedSize = used
	token.TotalSize = total
	token.FreeSize = total - used
}

type pan139CreateData struct {
	FileID    pan139FlexString `json:"fileId"`
	CatalogID pan139FlexString `json:"catalogId"`
	Name      string           `json:"name"`
}

type pan139UploadPart struct {
	ParallelHashCtx struct {
		PartOffset int64 `json:"partOffset"`
	} `json:"parallelHashCtx"`
	PartNumber int   `json:"partNumber"`
	PartSize   int64 `json:"partSize"`
}

type pan139UploadPartURL struct {
	PartNumber int    `json:"partNumber"`
	UploadURL  string `json:"uploadUrl"`
}

type pan139UploadCreateData struct {
	FileID      pan139FlexString      `json:"fileId"`
	FileName    string                `json:"fileName"`
	UploadID    string                `json:"uploadId"`
	PartInfos   []pan139UploadPartURL `json:"partInfos"`
	RapidUpload bool                  `json:"rapidUpload"`
	Exist       bool                  `json:"exist"`
}

type pan139UploadURLsData struct {
	FileID    pan139FlexString      `json:"fileId"`
	UploadID  string                `json:"uploadId"`
	PartInfos []pan139UploadPartURL `json:"partInfos"`
}

type pan139UploadSession struct {
	FileID      string `json:"fileId"`
	UploadID    string `json:"uploadId"`
	ContentHash string `json:"contentHash"`
}

// ListPage lists one page.
func (d *Driver) ListPage(ctx context.Context, c drive.Context, parentID, marker string) ([]model.File, string, error) {
	raw, err := d.personalPost(ctx, c, "/file/list", map[string]any{
		"imageThumbnailStyleList": []string{"Small", "Large"},
		"orderBy":                 "updated_at",
		"orderDirection":          "DESC",
		"pageInfo": map[string]any{
			"pageCursor": marker,
			"pageSize":   100,
		},
		"parentFileId": uploadParentID(parentID),
	})
	if err != nil {
		return nil, "", err
	}
	var data listData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, "", fmt.Errorf("pan139: 文件列表响应无效: %w", err)
	}
	entries := data.Items
	if entries == nil {
		entries = data.LegacyItems
	}
	items := make([]model.File, 0, len(entries))
	for _, it := range entries {
		items = append(items, mapFile(it, c.DriveID, parentID))
	}
	nextMarker := strings.TrimSpace(data.NextPageCursor)
	if nextMarker == marker {
		nextMarker = ""
	}
	return items, nextMarker, nil
}

func accountOf(c drive.Context) string {
	if c.Token == nil {
		return ""
	}
	var stored struct {
		Authorization string `json:"authorization"`
		Account       string `json:"account"`
	}
	_ = json.Unmarshal([]byte(c.Token.RefreshToken), &stored)
	if stored.Account != "" {
		return stored.Account
	}
	for _, authorization := range []string{c.Token.AccessToken, stored.Authorization} {
		if _, account, _, _, _, err := decodeAuthorization(authorization); err == nil && account != "" {
			return account
		}
	}
	return ""
}

func normalizePan139IDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func containsPan139Root(ids []string) bool {
	for _, id := range ids {
		if id == "" || id == "/" || id == "root" || id == RootID || id == "0" {
			return true
		}
	}
	return false
}

// uploadParentID is the root representation used by the newer personal-cloud
// upload API. The legacy list API uses "root", while /file/create expects "/".
func uploadParentID(id string) string {
	if id == "" || id == "root" || id == "/" || id == RootID {
		return "/"
	}
	return id
}

func fileRequestID(id string) string {
	return uploadParentID(id)
}

// Detail returns one file.
func (d *Driver) Detail(ctx context.Context, c drive.Context, fileID string) (*file139, error) {
	raw, err := d.personalPost(ctx, c, "/file/get", map[string]any{"fileId": fileRequestID(fileID)})
	if err != nil {
		return nil, err
	}
	var it file139
	if err := json.Unmarshal(raw, &it); err != nil {
		return nil, fmt.Errorf("pan139: 文件详情响应无效: %w", err)
	}
	if it.FileID.String() == "" && it.CatalogID.String() == "" {
		return nil, errors.New("pan139: 文件详情缺少 fileId")
	}
	return &it, nil
}

// DownloadInfo returns the download URL.
func (d *Driver) DownloadInfo(ctx context.Context, c drive.Context, fileID string) (string, int64, error) {
	raw, err := d.personalPost(ctx, c, "/file/getDownloadUrl", map[string]any{"fileId": fileRequestID(fileID)})
	if err != nil {
		return "", 0, err
	}
	var res struct {
		CDNURL    string          `json:"cdnUrl"`
		CDNSwitch *bool           `json:"cdnSwitch"`
		URL       string          `json:"url"`
		FileName  string          `json:"fileName"`
		Size      pan139FlexInt64 `json:"size"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", 0, fmt.Errorf("pan139: 下载地址响应无效: %w", err)
	}
	if strings.TrimSpace(res.CDNURL) == "" && strings.TrimSpace(res.URL) == "" {
		return "", 0, errors.New("pan139: 下载地址为空")
	}
	useCDN := res.CDNSwitch != nil && *res.CDNSwitch
	if res.CDNSwitch == nil && strings.TrimSpace(res.URL) == "" {
		useCDN = true
	}
	if useCDN && strings.TrimSpace(res.CDNURL) != "" {
		return res.CDNURL, int64(res.Size), nil
	}
	if strings.TrimSpace(res.URL) == "" {
		return "", 0, errors.New("pan139: 下载地址为空（CDN 未启用）")
	}
	return res.URL, int64(res.Size), nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	raw, err := d.personalPost(ctx, c, "/file/create", map[string]any{
		"parentFileId":   uploadParentID(parentID),
		"name":           name,
		"description":    "",
		"type":           "folder",
		"fileRenameMode": "force_rename",
	})
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	var res pan139CreateData
	if err := json.Unmarshal(raw, &res); err != nil {
		return &drive.MkdirResult{Error: fmt.Sprintf("pan139: 创建文件夹响应无效: %v", err)}, nil
	}
	fileID := res.FileID.String()
	if fileID == "" {
		fileID = res.CatalogID.String()
	}
	if fileID == "" {
		return &drive.MkdirResult{Error: "pan139: 创建文件夹未返回 fileId"}, nil
	}
	return &drive.MkdirResult{FileID: fileID}, nil
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	_, err := d.personalPost(ctx, c, "/file/update", map[string]any{
		"fileId":      fileRequestID(fileID),
		"name":        name,
		"description": "",
	})
	if err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: fileID, Name: name}, nil
}

// ---- driver ----

// Driver implements drive.Driver for 139.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return RootID }

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	items, _, err := d.ListPage(ctx, c, dirID, "")
	return items, err
}

func (d *Driver) ListPaged(ctx context.Context, c drive.Context, dirID, marker string, _ *drive.ListOptions) (*drive.DirPage, error) {
	items, next, err := d.ListPage(ctx, c, dirID, marker)
	if err != nil {
		return nil, err
	}
	return &drive.DirPage{Items: items, NextMarker: next}, nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if fileID == RootID || fileID == "/" || fileID == "root" {
		return model.File{DriveID: c.DriveID, FileID: RootID, Name: "139 云盘", NameSearch: "139", IsDir: true, Icon: "iconfile-folder"}, nil
	}
	return d.GetFile(ctx, c, fileID)
}

func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	if fileID == RootID || fileID == "/" || fileID == "root" {
		return &model.File{DriveID: c.DriveID, FileID: RootID, Name: "139 云盘", NameSearch: "139", IsDir: true, Icon: "iconfile-folder"}, nil
	}
	it, err := d.Detail(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	parentID := it.ParentFileID.String()
	if parentID == "" {
		parentID = it.ParentCatalogID.String()
	}
	if parentID == "/" || parentID == "root" {
		parentID = RootID
	}
	f := mapFile(*it, c.DriveID, parentID)
	return &f, nil
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, _ int) (*model.DownloadURL, error) {
	u, size, err := d.DownloadInfo(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID: c.DriveID, FileID: fileID, URL: u, Size: size,
		Headers: map[string]string{
			"Referer":    "https://yun.139.com/",
			"Origin":     "https://yun.139.com",
			"User-Agent": ua,
		},
		DownloadMode: "proxy", Concurrency: 1,
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID: c.DriveID, FileID: fileID, Size: u.Size, Headers: u.Headers,
		Qualities: []model.VideoQuality{{Quality: "origin", Label: "原画", Value: "origin", URL: u.URL, Headers: u.Headers, ForceProxy: true}},
	}, nil
}

func (d *Driver) MkdirR(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	return d.Mkdir(ctx, c, parentID, name)
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	ids := normalizePan139IDs(fileIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	if containsPan139Root(ids) {
		return nil, errors.New("pan139: 根目录不支持移入回收站")
	}
	_, err := d.personalPost(ctx, c, "/recyclebin/batchTrash", map[string]any{"fileIds": ids})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		if id := strings.TrimSpace(r.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if containsPan139Root(ids) {
		return nil, errors.New("pan139: 根目录不支持永久删除")
	}
	_, err := d.personalPost(ctx, c, "/file/batchDelete", map[string]any{"fileIds": ids})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	return nil, drive.NotSupported("pan139 recycle restore")
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		if id := strings.TrimSpace(r.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if containsPan139Root(ids) {
		return nil, errors.New("pan139: 根目录不支持移动")
	}
	_, err := d.personalPost(ctx, c, "/file/batchMove", map[string]any{
		"fileIds":        ids,
		"toParentFileId": uploadParentID(toParentID),
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		if id := strings.TrimSpace(r.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if containsPan139Root(ids) {
		return nil, errors.New("pan139: 根目录不支持复制")
	}
	_, err := d.personalPost(ctx, c, "/file/batchCopy", map[string]any{
		"fileIds":        ids,
		"toParentFileId": uploadParentID(toParentID),
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// pan139UploadParts builds the part description expected by /file/create and
// /file/getUploadUrl. The root API accepts at most 100 part descriptions per
// URL request; the caller fetches additional batches as needed.
func pan139UploadParts(size int64) []pan139UploadPart {
	if size < 0 {
		size = 0
	}
	partSize := pan139UploadPartSize
	if size > pan139LargeUploadThreshold {
		partSize = pan139LargePartSize
	}
	partCount := size / partSize
	if size%partSize != 0 {
		partCount++
	}
	if partCount == 0 {
		partCount = 1
	}
	parts := make([]pan139UploadPart, 0, int(partCount))
	for i := int64(0); i < partCount; i++ {
		offset := i * partSize
		length := size - offset
		if length > partSize {
			length = partSize
		}
		if length < 0 {
			length = 0
		}
		part := pan139UploadPart{PartNumber: int(i + 1), PartSize: length}
		part.ParallelHashCtx.PartOffset = offset
		parts = append(parts, part)
	}
	return parts
}

func pan139ContentType(name string) string {
	contentType := mime.TypeByExtension(strings.ToLower(strings.TrimSpace(filepath.Ext(name))))
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}

func hashPan139File(ctx context.Context, f *os.File, ui *model.UploadingUI) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	info, _ := f.Stat()
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	h := sha256.New()
	buf := make([]byte, 1024*1024)
	var hashed int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			if _, writeErr := h.Write(buf[:n]); writeErr != nil {
				return "", writeErr
			}
			hashed += int64(n)
			if ui != nil {
				ui.ReportUploadProgress(hashed, size)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func encodePan139UploadSession(session pan139UploadSession) string {
	b, _ := json.Marshal(session)
	return string(b)
}

func decodePan139UploadSession(raw string) (pan139UploadSession, bool) {
	var session pan139UploadSession
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &session) != nil {
		return pan139UploadSession{}, false
	}
	if strings.TrimSpace(session.FileID) == "" || strings.TrimSpace(session.UploadID) == "" {
		return pan139UploadSession{}, false
	}
	return session, true
}

func mergePan139UploadURLs(urls map[int]string, parts []pan139UploadPartURL) {
	for _, part := range parts {
		if part.PartNumber > 0 && strings.TrimSpace(part.UploadURL) != "" {
			urls[part.PartNumber] = strings.TrimSpace(part.UploadURL)
		}
	}
}

func (d *Driver) getPan139UploadURLs(ctx context.Context, c drive.Context, fileID, uploadID string, parts []pan139UploadPart) (map[int]string, error) {
	urls := make(map[int]string, len(parts))
	for start := 0; start < len(parts); start += pan139MaxPartsPerRequest {
		end := start + pan139MaxPartsPerRequest
		if end > len(parts) {
			end = len(parts)
		}
		var response pan139UploadURLsData
		raw, err := d.personalPost(ctx, c, "/file/getUploadUrl", map[string]any{
			"fileId":    fileID,
			"uploadId":  uploadID,
			"partInfos": parts[start:end],
			"commonAccountInfo": map[string]any{
				"account":     accountOf(c),
				"accountType": 1,
			},
		})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, fmt.Errorf("pan139: 上传地址响应无效: %w", err)
		}
		if response.FileID.String() != "" && response.FileID.String() != fileID {
			return nil, errors.New("pan139: 上传地址返回了错误的 fileId")
		}
		if response.UploadID != "" && response.UploadID != uploadID {
			return nil, errors.New("pan139: 上传地址返回了错误的 uploadId")
		}
		mergePan139UploadURLs(urls, response.PartInfos)
	}
	return urls, nil
}

func putPan139UploadPart(ctx context.Context, hc *netx.Client, f *os.File, part pan139UploadPart, uploadURL string) error {
	body := io.NewSectionReader(f, part.ParallelHashCtx.PartOffset, part.PartSize)
	req, err := hc.Req(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		return err
	}
	req.ContentLength = part.PartSize
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Origin", "https://yun.139.com")
	req.Header.Set("Referer", "https://yun.139.com/")
	resp, err := hc.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("pan139: 分片 %d 上传失败 HTTP %d", part.PartNumber, resp.StatusCode)
	}
	return nil
}

// UploadOneFile uploads a file through 139's SHA-256 precreate protocol.
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || strings.TrimSpace(ui.Info.LocalFilePath) == "" {
		return errors.New("pan139: 上传文件路径为空")
	}
	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	ui.Info.Size = size
	contentHash, err := hashPan139File(ctx, f, ui)
	if err != nil {
		return err
	}
	parts := pan139UploadParts(size)
	sessionKey := drive.UploadSessionKey(c.UserID, c.DriveID, ui.Info.ParentFileID, ui.Info.Name, size)
	savedSessionID, savedParts := drive.LoadUploadSessionState(sessionKey)
	session, resumed := decodePan139UploadSession(savedSessionID)
	if !resumed || !strings.EqualFold(session.ContentHash, contentHash) {
		resumed = false
		session = pan139UploadSession{}
		savedParts = nil
	}

	created := pan139UploadCreateData{}
	if resumed {
		created.FileID = pan139FlexString(session.FileID)
		created.UploadID = session.UploadID
	} else {
		initialParts := parts
		if len(initialParts) > pan139MaxPartsPerRequest {
			initialParts = initialParts[:pan139MaxPartsPerRequest]
		}
		raw, err := d.personalPost(ctx, c, "/file/create", map[string]any{
			"contentHash":          contentHash,
			"contentHashAlgorithm": "SHA256",
			"contentType":          pan139ContentType(ui.Info.Name),
			"fileRenameMode":       "auto_rename",
			"name":                 ui.Info.Name,
			"parallelUpload":       false,
			"parentFileId":         uploadParentID(ui.Info.ParentFileID),
			"partInfos":            initialParts,
			"size":                 size,
			"type":                 "file",
		})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &created); err != nil {
			return fmt.Errorf("pan139: 上传初始化响应无效: %w", err)
		}
		if created.Exist || created.RapidUpload {
			ui.ReportUploadProgress(size, size)
			drive.ClearUploadSession(sessionKey)
			return nil
		}
		session = pan139UploadSession{
			FileID:      created.FileID.String(),
			UploadID:    created.UploadID,
			ContentHash: contentHash,
		}
		if session.FileID == "" || session.UploadID == "" {
			return errors.New("pan139: 上传初始化未返回 fileId 或 uploadId")
		}
		_ = drive.SaveUploadSessionState(sessionKey, encodePan139UploadSession(session), nil)
	}

	if session.FileID == "" {
		session.FileID = created.FileID.String()
	}
	if session.UploadID == "" {
		session.UploadID = created.UploadID
	}
	if session.FileID == "" || session.UploadID == "" {
		return errors.New("pan139: 上传会话缺少 fileId 或 uploadId")
	}
	uploadedSet := make(map[int]bool, len(savedParts))
	for _, partNumber := range savedParts {
		if partNumber >= 1 && partNumber <= len(parts) {
			uploadedSet[partNumber] = true
		}
	}
	uploaded := int64(0)
	for _, part := range parts {
		if uploadedSet[part.PartNumber] {
			uploaded += part.PartSize
		}
	}
	ui.ReportUploadProgress(uploaded, size)

	urls := make(map[int]string, len(created.PartInfos))
	mergePan139UploadURLs(urls, created.PartInfos)
	pending := make([]pan139UploadPart, 0, len(parts)-len(uploadedSet))
	for _, part := range parts {
		if size > 0 && !uploadedSet[part.PartNumber] && strings.TrimSpace(urls[part.PartNumber]) == "" {
			pending = append(pending, part)
		}
	}
	if len(pending) > 0 {
		moreURLs, err := d.getPan139UploadURLs(ctx, c, session.FileID, session.UploadID, pending)
		if err != nil {
			return err
		}
		for partNumber, uploadURL := range moreURLs {
			urls[partNumber] = uploadURL
		}
	}

	hc := netx.NewClient(10 * time.Minute)
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if uploadedSet[part.PartNumber] {
			continue
		}
		if size > 0 && strings.TrimSpace(urls[part.PartNumber]) == "" {
			return fmt.Errorf("pan139: 第 %d 个分片未返回上传地址", part.PartNumber)
		}
		if part.PartSize > 0 {
			if err := putPan139UploadPart(ctx, hc, f, part, urls[part.PartNumber]); err != nil {
				return err
			}
		}
		uploadedSet[part.PartNumber] = true
		uploaded += part.PartSize
		_ = drive.SaveUploadSessionState(sessionKey, encodePan139UploadSession(session), drive.SortedUniqueParts(uploadedSet))
		ui.ReportUploadProgress(uploaded, size)
	}

	if _, err := d.personalPost(ctx, c, "/file/complete", map[string]any{
		"contentHash":          contentHash,
		"contentHashAlgorithm": "SHA256",
		"fileId":               session.FileID,
		"uploadId":             session.UploadID,
	}); err != nil {
		return err
	}
	drive.ClearUploadSession(sessionKey)
	ui.ReportUploadProgress(size, size)
	return nil
}

// RapidUploadByHash probes/commits 139's SHA-256 precreate path. A miss is
// reported as Reuse=false so the migration engine can fall back to a normal
// download and upload; the provider API owns any temporary precreate session.
func (d *Driver) RapidUploadByHash(ctx context.Context, c drive.Context, req drive.RapidUploadRequest) (*drive.RapidUploadResult, error) {
	if !strings.EqualFold(strings.TrimSpace(req.Method), "sha256") {
		return &drive.RapidUploadResult{Reuse: false, Message: "139 云盘仅支持 SHA-256 秒传"}, nil
	}
	hashValue := strings.ToLower(strings.TrimSpace(req.Hash))
	if len(hashValue) != sha256.Size*2 {
		return &drive.RapidUploadResult{Reuse: false, Message: "无效的 SHA-256 指纹"}, nil
	}
	if _, err := hex.DecodeString(hashValue); err != nil {
		return &drive.RapidUploadResult{Reuse: false, Message: "无效的 SHA-256 指纹"}, nil
	}
	if req.Size < 0 {
		return nil, errors.New("pan139: 文件大小不能为负数")
	}
	parts := pan139UploadParts(req.Size)
	if len(parts) > pan139MaxPartsPerRequest {
		parts = parts[:pan139MaxPartsPerRequest]
	}
	raw, err := d.personalPost(ctx, c, "/file/create", map[string]any{
		"contentHash":          hashValue,
		"contentHashAlgorithm": "SHA256",
		"contentType":          pan139ContentType(req.FileName),
		"fileRenameMode":       "auto_rename",
		"name":                 req.FileName,
		"parallelUpload":       false,
		"parentFileId":         uploadParentID(req.ParentID),
		"partInfos":            parts,
		"size":                 req.Size,
		"type":                 "file",
	})
	if err != nil {
		return nil, err
	}
	var created pan139UploadCreateData
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, fmt.Errorf("pan139: 秒传响应无效: %w", err)
	}
	if !created.Exist && !created.RapidUpload {
		if created.FileID.String() != "" && created.UploadID != "" {
			key := drive.UploadSessionKey(c.UserID, c.DriveID, req.ParentID, req.FileName, req.Size)
			_ = drive.SaveUploadSessionState(key, encodePan139UploadSession(pan139UploadSession{
				FileID: created.FileID.String(), UploadID: created.UploadID, ContentHash: hashValue,
			}), nil)
		}
		return &drive.RapidUploadResult{Reuse: false, ParentID: req.ParentID, Message: "未命中秒传"}, nil
	}
	if created.FileID.String() != "" {
		key := drive.UploadSessionKey(c.UserID, c.DriveID, req.ParentID, req.FileName, req.Size)
		drive.ClearUploadSession(key)
	}
	return &drive.RapidUploadResult{
		Reuse:    true,
		FileID:   created.FileID.String(),
		ParentID: req.ParentID,
		Message:  "秒传命中",
	}, nil
}

// ResolveTransferHash reads the SHA-256 content fingerprint exposed by the
// newer /file/get endpoint for cross-drive migration.
func (d *Driver) ResolveTransferHash(ctx context.Context, c drive.Context, fileID, method string, _ bool) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(method), "sha256") {
		return "", nil
	}
	raw, err := d.personalPost(ctx, c, "/file/get", map[string]any{"fileId": fileRequestID(fileID)})
	if err != nil {
		return "", err
	}
	var detail struct {
		ContentHash          string `json:"contentHash"`
		ContentHashAlgorithm string `json:"contentHashAlgorithm"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		return "", fmt.Errorf("pan139: 文件指纹响应无效: %w", err)
	}
	if detail.ContentHashAlgorithm != "" && !strings.EqualFold(detail.ContentHashAlgorithm, "sha256") {
		return "", nil
	}
	hashValue := strings.ToLower(strings.TrimSpace(detail.ContentHash))
	if len(hashValue) != sha256.Size*2 {
		return "", nil
	}
	if _, err := hex.DecodeString(hashValue); err != nil {
		return "", nil
	}
	return hashValue, nil
}

func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if token == nil {
		return nil, errors.New("139 云盘未登录")
	}
	hc := netx.NewClient(60 * time.Second)
	cr, err := loadCred(hc, token)
	if err != nil {
		return nil, err
	}
	// The web client resolves older logins through status/query before querying
	// quota. Cache only the domain ID; never substitute the phone/account ID.
	userCred := *cr
	userCred.host = "https://user-njs.yun.139.com/user"
	stored := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(token.RefreshToken), &stored)
	var domainID pan139FlexString
	_ = json.Unmarshal(stored["userDomainId"], &domainID)
	if domainID.String() == "" {
		raw, queryErr := d.personalPostWithCred(ctx, hc, &userCred, "/status/query", map[string]any{})
		if queryErr != nil {
			return nil, fmt.Errorf("获取 139 容量账号信息失败：%w", queryErr)
		}
		var info struct {
			DomainID pan139FlexString `json:"userDomainId"`
		}
		if json.Unmarshal(raw, &info) != nil || info.DomainID.String() == "" {
			return nil, errors.New("139 未返回容量查询所需的 userDomainId")
		}
		domainID = info.DomainID
		if stored == nil {
			stored = map[string]json.RawMessage{}
		}
		stored["userDomainId"], _ = json.Marshal(domainID.String())
		token.RefreshToken = mustJSON(stored)
	}
	raw, err := d.personalPostWithCred(ctx, hc, &userCred, "/disk/quota/detail", map[string]any{"userDomainId": domainID.String()})
	if err != nil {
		return nil, fmt.Errorf("获取 139 云空间失败：%w", err)
	}
	used, total, ok := parsePan139Quota(raw)
	if !ok {
		return nil, errors.New("139 容量接口未返回有效空间信息")
	}
	applyPan139Quota(token, used, total)
	return token, nil
}

// mapFile converts a 139 entry to the unified model.
func mapFile(it file139, driveID, parentID string) model.File {
	fileID := it.FileID.String()
	if fileID == "" {
		fileID = it.CatalogID.String()
	}
	name := strings.TrimSpace(it.Name)
	if name == "" {
		name = strings.TrimSpace(it.CatalogName)
	}
	isDir := strings.EqualFold(strings.TrimSpace(it.Type), "folder") ||
		strings.EqualFold(strings.TrimSpace(it.ContentType), "folder") ||
		(fileID == it.CatalogID.String() && it.CatalogID.String() != "")
	timeUnix := parsePan139Time(it.UpdateTime, it.UpdatedAt)
	f := driveutil.NewFile(driveID, fileID, parentID, name, isDir, int64(it.Size), timeUnix)
	f.Path = it.Path
	if it.Star != 0 {
		f.Starred = true
	}
	if strings.TrimSpace(it.ContentHash) != "" {
		f.ContentHash = strings.TrimSpace(it.ContentHash)
		f.ContentHashName = strings.ToLower(strings.TrimSpace(it.ContentHashAlg))
		if f.ContentHashName == "" {
			f.ContentHashName = "sha256"
		}
	}
	return f
}

func parsePan139Time(primary string, fallback any) int64 {
	for _, value := range []any{primary, fallback} {
		switch v := value.(type) {
		case string:
			value := strings.TrimSpace(v)
			if value == "" {
				continue
			}
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
				if parsed, err := time.Parse(layout, value); err == nil {
					return parsed.Unix()
				}
			}
			if millis, err := strconv.ParseInt(value, 10, 64); err == nil {
				return pan139UnixValue(millis)
			}
		case float64:
			return pan139UnixValue(int64(v))
		case json.Number:
			if number, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
				return pan139UnixValue(number)
			}
		}
	}
	return 0
}

func pan139UnixValue(value int64) int64 {
	if value > 0 && value < 1_000_000_000_000 {
		return value
	}
	if value > 0 {
		return time.UnixMilli(value).Unix()
	}
	return 0
}

type pan139SMSRequiredError struct{}

func (pan139SMSRequiredError) Error() string {
	return "pan139_sms_required\n139 登录需要短信安全校验，请先获取短信验证码"
}

type pan139LoginState struct {
	operationMu   sync.Mutex
	Username      string
	RiskCode      string
	MailSID       string
	Password      string
	MailCookies   string
	Client        *netx.Client
	CreatedAt     time.Time
	LastSMSSentAt time.Time
}

var (
	pan139LoginStateMu sync.Mutex
	pan139LoginStates  = map[string]*pan139LoginState{}
)

func savePan139LoginState(state *pan139LoginState) {
	if state == nil || strings.TrimSpace(state.Username) == "" {
		return
	}
	pan139LoginStateMu.Lock()
	defer pan139LoginStateMu.Unlock()
	for user, item := range pan139LoginStates {
		if item == nil || time.Since(item.CreatedAt) > pan139SMSStateTTL {
			delete(pan139LoginStates, user)
		}
	}
	pan139LoginStates[state.Username] = state
}

func loadPan139LoginState(username string) *pan139LoginState {
	pan139LoginStateMu.Lock()
	defer pan139LoginStateMu.Unlock()
	state := pan139LoginStates[strings.TrimSpace(username)]
	if state == nil || time.Since(state.CreatedAt) > pan139SMSStateTTL {
		delete(pan139LoginStates, strings.TrimSpace(username))
		return nil
	}
	return state
}

func reservePan139SMSSend(username string) (*pan139LoginState, error) {
	username = strings.TrimSpace(username)
	pan139LoginStateMu.Lock()
	defer pan139LoginStateMu.Unlock()
	state := pan139LoginStates[username]
	if state == nil || time.Since(state.CreatedAt) > pan139SMSStateTTL {
		var err error
		state, err = newPan139LoginState(username, "", "")
		if err != nil {
			return nil, err
		}
		pan139LoginStates[username] = state
	}
	if !state.LastSMSSentAt.IsZero() {
		remaining := pan139SMSMinInterval - time.Since(state.LastSMSSentAt)
		if remaining > 0 {
			seconds := int((remaining + time.Second - 1) / time.Second)
			return nil, fmt.Errorf("139 验证码已发送，请 %d 秒后再试", seconds)
		}
	}
	state.LastSMSSentAt = time.Now()
	return state, nil
}

func deletePan139LoginState(username string) {
	pan139LoginStateMu.Lock()
	delete(pan139LoginStates, strings.TrimSpace(username))
	pan139LoginStateMu.Unlock()
}

// authLogin handles Authorization, password login, or the second-step SMS login.
func authLogin(ctx context.Context, req drive.AuthRequest) (*model.TokenInfo, error) {
	authorization := strings.TrimSpace(req.Config["authorization"])
	username := strings.TrimSpace(req.Config["username"])
	if authorization == "" {
		mode := strings.ToLower(strings.TrimSpace(req.Config["login_mode"]))
		if mode == "sms" || (req.Config["sms_code"] != "" && req.Config["password"] == "") {
			if username == "" || strings.TrimSpace(req.Config["sms_code"]) == "" {
				return nil, errors.New("pan139: 请输入账号和短信验证码")
			}
			var err error
			authorization, err = loginBySMS(ctx, username, strings.TrimSpace(req.Config["sms_code"]))
			if err != nil {
				return nil, err
			}
		} else {
			password := req.Config["password"]
			if username == "" || password == "" {
				return nil, errors.New("pan139: 请输入账号密码或 Authorization")
			}
			var err error
			authorization, err = loginByPassword(ctx, username, password, req.Config["mail_cookies"])
			if err != nil {
				return nil, err
			}
		}
	}
	hc := netx.NewClient(60 * time.Second)
	next, err := refreshAuthorization(hc, authorization)
	if err != nil {
		return nil, err
	}
	_, account, _, _, _, err := decodeAuthorization(next)
	if err != nil {
		return nil, err
	}
	host, err := ensureHost(hc, next, account)
	if err != nil {
		return nil, err
	}
	uid := account
	if uid == "" {
		uid = next
		if len(uid) > 16 {
			uid = uid[:16]
		}
	}
	name := username
	if name == "" {
		name = account
	}
	if name == "" {
		name = uid
	}
	stored := map[string]any{"authorization": next, "account": account, "personalCloudHost": host}
	if u := req.Config["username"]; u != "" {
		stored["username"] = u
		stored["password"] = req.Config["password"]
		stored["account"] = account
	}
	tok := &model.TokenInfo{
		TokenFrom:         providerID,
		AccessToken:       next,
		RefreshToken:      mustJSON(stored),
		TokenType:         "Basic",
		UserID:            model.BuildUserID(providerID, uid),
		UserName:          name,
		NickName:          name,
		Name:              name,
		ProviderAccountID: account,
		ProviderRootID:    "/",
	}
	return tok, nil
}

// loginByPassword performs the current mail.10086.cn password login flow.
// When the account is covered by the security upgrade, the server redirects
// to an SMS login state; the state is retained for SendPan139SMS/loginBySMS.
func loginByPassword(ctx context.Context, username, password, mailCookies string) (string, error) {
	if password == "" {
		return "", errors.New("pan139: 密码不能为空")
	}
	state, err := newPan139LoginState(username, password, mailCookies)
	if err != nil {
		return "", err
	}
	location, sid, err := submitPan139Login(ctx, state, false, "")
	if err != nil {
		if pan139RiskCode(location) == "S305" {
			deletePan139LoginState(username)
			return "", fmt.Errorf("pan139_sms_required\n%w", err)
		}
		return "", err
	}
	if sid == "" {
		if pan139NeedsSMS(location) {
			// The password is needed only for the first request. Do not retain it
			// while waiting for the user to enter the SMS second factor.
			state.Password = ""
			savePan139LoginState(state)
			return "", pan139SMSRequiredError{}
		}
		return "", errors.New("139 账密登录失败：服务器未返回登录会话")
	}
	deletePan139LoginState(username)
	return finishPan139Login(ctx, state, sid)
}

func newPan139LoginState(username, password, mailCookies string) (*pan139LoginState, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("pan139: 账号不能为空")
	}
	hc := netx.NewClient(60 * time.Second)
	hc.HTTP.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	hc.HTTP.Jar = jar
	mailURL, _ := url.Parse(mailLoginURL)
	initial := parseCookieMap(mailCookies)
	if mailURL != nil {
		cookies := make([]*http.Cookie, 0, len(initial))
		for name, value := range initial {
			if name != "" && value != "" {
				cookies = append(cookies, &http.Cookie{Name: name, Value: value, Path: "/"})
			}
		}
		jar.SetCookies(mailURL, cookies)
	}
	return &pan139LoginState{Username: username, Password: password, MailCookies: mailCookies, Client: hc, CreatedAt: time.Now()}, nil
}

func submitPan139Login(ctx context.Context, state *pan139LoginState, sms bool, smsCode string) (location, sid string, err error) {
	if state == nil || state.Client == nil {
		return "", "", errors.New("pan139: 登录会话不存在")
	}
	if sms && state.RiskCode != "S045" && state.RiskCode != "S046" && state.RiskCode != "" {
		return submitPan139SMSXML(ctx, state, smsCode)
	}
	// The browser first opens Login.ashx. This creates the fresh JSESSIONID
	// expected by the password endpoint and also refreshes stale login cookies.
	if !sms {
		preflight, reqErr := state.Client.Do(ctx, http.MethodGet, mailLoginURL, map[string]string{
			"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
			"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
			"Cache-Control":   "max-age=0",
			"Referer":         "https://mail.10086.cn/default.html",
			"User-Agent":      ua,
		}, nil)
		if reqErr != nil {
			return "", "", fmt.Errorf("139 登录预请求失败: %w", reqErr)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(preflight.Body, 4096))
		preflight.Body.Close()
		syncPan139JarCookies(state)
	}

	cguid := fmt.Sprint(time.Now().UnixMilli())
	form := url.Values{}
	form.Set("UserName", state.Username)
	form.Set("auto", "on")
	form.Set("clientId", "1003")
	form.Set("authType", "2")
	form.Set("version", "1.0")
	if sms {
		// The official form clears the SMS input after computing Password.
		// Sending both proofs can select the gateway's legacy password branch.
		form.Set("passOld", "")
		form.Set("Password", sha1Hex("fetion.com.cn:"+smsCode))
		form.Set("reqFrom", "3")
		form.Set("loginFailureUrl", mailHostURL+"/default.html?smsLogin=1")
	} else {
		form.Set("passOld", "")
		form.Set("Password", sha1Hex("fetion.com.cn:"+state.Password))
		form.Set("webIndexPagePwdLogin", "1")
		form.Set("pwdType", "1")
		form.Set("reqFrom", "0")
	}
	referer := fmt.Sprintf("https://mail.10086.cn/default.html?&s=1&v=0&u=%s&m=1&ec=S001&resource=indexLogin&clientid=1003&auto=on&cguid=%s&mtime=45",
		base64.StdEncoding.EncodeToString([]byte(state.Username)), cguid)
	query := url.Values{"_fv": {"4"}, "cguid": {cguid}, "resource": {"indexLogin"}, "_": {sha1Hex(state.Username)}}
	resp, reqErr := state.Client.Do(ctx, http.MethodPost, mailLoginURL+"?"+query.Encode(), map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"Content-Type":    "application/x-www-form-urlencoded",
		"Origin":          "https://mail.10086.cn",
		"Referer":         referer,
		"User-Agent":      ua,
	}, strings.NewReader(form.Encode()))
	if reqErr != nil {
		return "", "", fmt.Errorf("139 登录请求失败: %w", reqErr)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	location = resp.Header.Get("Location")
	if location == "" {
		location = pan139BodyRedirect(string(body))
	}
	state.MailCookies = mergePan139ResponseCookies(state.MailCookies, resp.Cookies())
	syncPan139JarCookies(state)
	if resp.StatusCode >= http.StatusBadRequest {
		return location, "", fmt.Errorf("139 登录失败：HTTP %d", resp.StatusCode)
	}
	if code := pan139RiskCode(location); code != "" {
		if code == "S305" {
			if sms {
				return location, "", errors.New("139 登录失败：S305 短信验证码错误，请重新获取")
			}
			return location, "", errors.New("139 登录失败：S305 尚未设置移动认证账号密码，可切换到短信验证码登录，或在 139 邮箱官网设置密码后重试")
		}
		if code == "S001" {
			return location, "", errors.New("139 登录失败：S001，服务端拒绝本次登录，请核对账号信息或切换短信登录")
		}
		if _, ok := pan139SMSScene(code); !ok {
			return location, "", fmt.Errorf("139 登录失败：%s", code)
		}
		state.RiskCode = code
		return location, "", nil
	}
	sid = extractPan139SID(location, resp)
	if sid == "" && pan139NeedsSMS(location) {
		return location, "", nil
	}
	if sid == "" && len(bytesTrimSpace(body)) > 0 {
		// Some gateways return the redirect as a plain HTML/JSON fragment.
		text := string(bytesTrimSpace(body))
		if pan139NeedsSMS(text) {
			state.RiskCode = pan139RiskCode(text)
			return text, "", nil
		}
	}
	return location, sid, nil
}

// Parse explicit redirects only; unrelated sid strings in a login page are not
// evidence of a successful login. Never execute returned JavaScript.
func pan139BodyRedirect(body string) string {
	var result struct {
		Code string `json:"code"`
		Var  struct {
			URL string `json:"loginSuccessUrl"`
		} `json:"var"`
	}
	if json.Unmarshal([]byte(body), &result) == nil && result.Code == "S_OK" {
		return result.Var.URL
	}
	pattern := `(?i)(?:(?:window\.|top\.|parent\.)?location(?:\.href)?\s*=\s*|(?:window\.|top\.|parent\.)?location\.replace\(\s*)["']([^"']+)["']`
	if match := regexp.MustCompile(pattern).FindStringSubmatch(body); len(match) > 1 {
		return html.UnescapeString(match[1])
	}
	return ""
}

func finishPan139Login(ctx context.Context, state *pan139LoginState, sid string) (string, error) {
	cookies := parseCookieMap(state.MailCookies)
	rmkey := cookies["RMKEY"]
	if rmkey == "" {
		return "", errors.New("139 登录成功但缺少 RMKEY，请重新登录或补充 Cookie")
	}
	artifact, err := getArtifactWithClient(ctx, state.Client, sid, rmkey)
	if err != nil {
		return "", err
	}
	return thirdPartyLogin(ctx, state.Client, state.Username, artifact)
}

// loginBySMS completes the security-upgrade login using the state retained by
// the preceding password attempt.
func loginBySMS(ctx context.Context, username, smsCode string) (string, error) {
	state := loadPan139LoginState(username)
	if state == nil {
		return "", errors.New("139 登录会话已过期，请重新获取短信验证码")
	}
	state.operationMu.Lock()
	defer state.operationMu.Unlock()
	if loadPan139LoginState(username) != state {
		return "", errors.New("139 登录会话已结束，请重新获取短信验证码")
	}
	if state.MailSID == "" {
		location, sid, err := submitPan139Login(ctx, state, true, smsCode)
		if err != nil {
			return "", err
		}
		if sid == "" {
			if pan139NeedsSMS(location) {
				return "", errors.New("139 短信验证码未通过，请检查验证码")
			}
			return "", errors.New("139 短信登录失败：服务器未返回登录会话")
		}
		state.MailSID = sid
	}
	// The SMS proof is single-use. Retain its mail session until cloud SSO
	// succeeds so a manual retry only repeats the ticket exchange.
	authorization, err := finishPan139Login(ctx, state, state.MailSID)
	if err != nil {
		return "", err
	}
	deletePan139LoginState(username)
	return authorization, nil
}

// RequestPan139SMS supports both direct SMS login and password risk verification.
func RequestPan139SMS(ctx context.Context, username string) (sendErr error) {
	state, err := reservePan139SMSSend(username)
	if err != nil {
		return err
	}
	state.operationMu.Lock()
	defer state.operationMu.Unlock()
	defer func() {
		if sendErr != nil {
			pan139LoginStateMu.Lock()
			state.LastSMSSentAt = time.Time{}
			pan139LoginStateMu.Unlock()
		}
	}()
	scene, ok := pan139SMSScene(state.RiskCode)
	if !ok && state.RiskCode != "" {
		return fmt.Errorf("139 登录校验场景不支持短信：%s", state.RiskCode)
	}
	encodedUser, err := rsaEncryptPan139LoginName(state.Username)
	if err != nil {
		return err
	}
	body := "<object>" +
		"<string name=\"loginName\">" + encodedUser + "</string>" +
		"<string name=\"fv\">4</string>" +
		"<string name=\"clientId\">1003</string>" +
		"<string name=\"eMode\">1</string>" +
		"<string name=\"loginFailureUrl\"></string>" +
		"<string name=\"loginSuccessUrl\"></string>" +
		"<string name=\"verifyCode\"></string>" +
		"<string name=\"version\">1.0</string>" +
		"<string name=\"scene\">" + strconv.Itoa(scene) + "</string>" +
		"</object>"
	method := "login:sendSmsCodeByScene"
	if state.RiskCode == "" {
		// The official SMS tab uses the standalone endpoint, not a password
		// risk scene. Its RSA loginName uses the existing phone public key.
		method = "login:sendSmsCode"
		body = "<object>" + pan139XMLField("loginName", encodedUser) +
			pan139XMLField("fv", "4") + pan139XMLField("clientId", "1003") +
			pan139XMLField("version", "1.0") + pan139XMLField("verifyCode", "") + "</object>"
	}
	cguid := fmt.Sprint(time.Now().UnixMilli())
	resp, err := state.Client.Do(ctx, http.MethodPost, mailSMSURL+"?func="+url.QueryEscape(method)+"&cguid="+url.QueryEscape(cguid), map[string]string{
		"Accept":       "text/javascript",
		"Content-Type": "application/xml",
		"Origin":       "https://mail.10086.cn",
		"Referer":      "https://mail.10086.cn/default.html",
		"User-Agent":   ua,
	}, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("139 获取短信验证码失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("139 获取短信验证码响应读取失败: %w", err)
	}
	state.MailCookies = mergePan139ResponseCookies(state.MailCookies, resp.Cookies())
	syncPan139JarCookies(state)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("139 获取短信验证码失败：HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(raw, &result) == nil && strings.EqualFold(result.Code, "S_OK") {
		return nil
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		text = "服务器未确认发送"
	}
	return errors.New("139 获取短信验证码失败：" + truncate(text, 120))
}

func rsaEncryptPan139LoginName(value string) (string, error) {
	der, err := base64.StdEncoding.DecodeString(pan139SMSPhonePublicKey)
	if err != nil {
		return "", err
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", err
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("139 登录公钥类型无效")
	}
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, rsaKey, []byte(value))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func pan139NeedsSMS(value string) bool {
	_, ok := pan139SMSScene(pan139RiskCode(value))
	return ok
}

func pan139SMSScene(code string) (int, bool) {
	switch code {
	case "PML401010062":
		return 2, true
	case "MW0016":
		return 4, true
	case "S025", "S035":
		return 1, true
	case "S045", "S046":
		return 5, true
	default:
		return 0, false
	}
}

func pan139RiskCode(value string) string {
	value = strings.TrimSpace(value)
	if u, err := url.Parse(value); err == nil {
		if code := u.Query().Get("ec"); code != "" {
			return strings.ToUpper(code)
		}
	}
	if _, ok := pan139SMSScene(strings.ToUpper(value)); ok {
		return strings.ToUpper(value)
	}
	if m := regexp.MustCompile(`(?i)(?:[?&]ec=|["']?code["']?\s*:\s*["'])([A-Z0-9]+)`).FindStringSubmatch(value); len(m) > 1 {
		return strings.ToUpper(m[1])
	}
	return ""
}

func pan139XMLField(name, value string) string {
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(value))
	return `<string name="` + name + `">` + escaped.String() + `</string>`
}

func submitPan139SMSXML(ctx context.Context, state *pan139LoginState, smsCode string) (string, string, error) {
	encodedUser, err := rsaEncryptPan139LoginName(state.Username)
	if err != nil {
		return "", "", err
	}
	body := "<object>" + pan139XMLField("clientId", "1003") + pan139XMLField("version", "4") +
		pan139XMLField("loginType", "0") + pan139XMLField("authType", "2") + pan139XMLField("loginName", encodedUser) +
		pan139XMLField("eMode", "1") + pan139XMLField("loginPassword", sha1Hex("fetion.com.cn:"+smsCode)) +
		pan139XMLField("createAutoLoginSecretKey", "1") + pan139XMLField("verifyCode", "") + pan139XMLField("verifyAgentId", "") +
		pan139XMLField("reqFrom", "3") + pan139XMLField("needWCookie", "1")
	if state.RiskCode == "MW0016" {
		body += pan139XMLField("pwdType", "1")
	}
	body += "</object>"
	resp, err := state.Client.Do(ctx, http.MethodPost, mailSMSURL+"?func="+url.QueryEscape("/login/inlogin.action")+"&cguid="+strconv.FormatInt(time.Now().UnixMilli(), 10), map[string]string{
		"Content-Type": "application/xml; charset=utf-8", "User-Agent": "okhttp/4.12.0", "Origin": mailHostURL, "Referer": "https://mail.10086.cn/default.html",
	}, strings.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("139 短信验证失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", "", err
	}
	state.MailCookies = mergePan139ResponseCookies(state.MailCookies, resp.Cookies())
	syncPan139JarCookies(state)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("139 短信验证失败：HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code    string `json:"code"`
		Summary string `json:"summary"`
		Var     struct {
			URL string `json:"loginSuccessUrl"`
		} `json:"var"`
	}
	if json.Unmarshal(data, &result) != nil || result.Code != "S_OK" {
		return "", "", fmt.Errorf("139 短信验证失败：%s %s", result.Code, truncate(result.Summary, 120))
	}
	sid := extractPan139SID(result.Var.URL, resp)
	if sid == "" {
		cookies := parseCookieMap(state.MailCookies)
		sid = cookies["Os_SSo_Sid"]
		if sid == "" {
			sid = cookies["sid"]
		}
	}
	if sid == "" {
		return "", "", errors.New("139 短信验证成功但未返回 sid")
	}
	return result.Var.URL, sid, nil
}

func extractPan139SID(location string, resp *http.Response) string {
	for _, value := range []string{location} {
		if m := regexp.MustCompile(`(?i)[?&]sid=([^&#]+)`).FindStringSubmatch(value); len(m) > 1 {
			decoded, _ := url.QueryUnescape(m[1])
			if decoded != "" {
				return decoded
			}
		}
	}
	if resp != nil {
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "Os_SSo_Sid" && cookie.Value != "" {
				return cookie.Value
			}
		}
		for _, header := range resp.Header.Values("Set-Cookie") {
			if m := regexp.MustCompile(`(?i)(?:^|;\s*)Os_SSo_Sid=([^;]+)`).FindStringSubmatch(header); len(m) > 1 {
				return m[1]
			}
		}
	}
	return ""
}

func mergePan139ResponseCookies(existing string, responseCookies []*http.Cookie) string {
	cookies := parseCookieMap(existing)
	for _, cookie := range responseCookies {
		if cookie != nil && cookie.Name != "" && cookie.Value != "" {
			cookies[cookie.Name] = cookie.Value
		}
	}
	return formatCookieMap(cookies)
}

func syncPan139JarCookies(state *pan139LoginState) {
	if state == nil || state.Client == nil || state.Client.HTTP == nil || state.Client.HTTP.Jar == nil {
		return
	}
	u, err := url.Parse(mailHostURL)
	if err != nil {
		return
	}
	state.MailCookies = mergePan139ResponseCookies(state.MailCookies, state.Client.HTTP.Jar.Cookies(u))
}

func bytesTrimSpace(value []byte) []byte { return []byte(strings.TrimSpace(string(value))) }

func getArtifact(ctx context.Context, sid, rmkey string) (string, error) {
	return getArtifactWithClient(ctx, netx.NewClient(60*time.Second), sid, rmkey)
}

func getArtifactWithClient(ctx context.Context, hc *netx.Client, sid, rmkey string) (string, error) {
	if hc == nil {
		return "", errors.New("139 artifact 请求客户端不存在")
	}
	urlValue := fmt.Sprintf("https://smsrebuild1.mail.10086.cn/setting/s?func=%s&sid=%s&cguid=%s",
		url.QueryEscape("umc:getArtifact"), url.QueryEscape(sid), fmt.Sprint(time.Now().UnixMilli()))
	resp, err := hc.Do(ctx, http.MethodPost, urlValue, map[string]string{
		"Host":       "smsrebuild1.mail.10086.cn",
		"Cookie":     "RMKEY=" + rmkey,
		"User-Agent": ua,
		"Accept":     "text/plain, */*",
	}, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("获取 artifact 失败：HTTP %d", resp.StatusCode)
	}
	text := string(body)
	// artifact is embedded in the response script; parse "getArtifact" value
	m := regexp.MustCompile(`(?s)artifact["']?\s*[:=]\s*["']([^"']+)`).FindStringSubmatch(text)
	if len(m) < 2 {
		m2 := regexp.MustCompile(`(?s)"data"\s*:\s*\{[^}]*"value"\s*:\s*"([^"]+)"`).FindStringSubmatch(text)
		if len(m2) < 2 {
			return "", errors.New("获取 artifact 失败")
		}
		return m2[1], nil
	}
	return m[1], nil
}

// thirdPartyLogin exchanges the artifact through the current encrypted mobile
// cloud SSO endpoint. The older Authorize.ashx endpoint is no longer reliable
// after the 139 security-login upgrade.
func thirdPartyLogin(ctx context.Context, hc *netx.Client, username, artifact string) (string, error) {
	body := map[string]any{
		"clientkey_decrypt": "l3TryM&Q+X7@dzwk)qP",
		"clienttype":        "886",
		"cpid":              "507",
		"dycpwd":            artifact,
		"extInfo":           map[string]any{"ifOpenAccount": "0"},
		"loginMode":         "0",
		"msisdn":            username,
		"pintype":           "13",
		"secinfo":           strings.ToUpper(sha1Hex("fetion.com.cn:" + artifact)),
		"version":           "20250901",
	}
	plain := sortedJSONStringify(body)
	payload := aesCBCEncryptBase64Payload(plain, pan139ThirdLoginKey1)
	if payload == "" {
		return "", errors.New("139 SSO 请求加密失败")
	}
	resp, err := hc.Do(ctx, http.MethodPost, thirdLoginURL, map[string]string{
		"Accept":              "application/json, text/plain, */*",
		"Content-Type":        "text/plain;charset=UTF-8",
		"User-Agent":          "okhttp/3.12.2",
		"hcy-cool-flag":       "1",
		"x-huawei-channelSrc": "10246600",
		"x-sdk-channelSrc":    "",
		"x-MM-Source":         "0",
		"x-UserAgent":         "android|23116PN5BC|android15|1.2.6|||1440x3200|10246600",
		"x-DeviceInfo":        "4|127.0.0.1|5|1.2.6|Xiaomi|23116PN5BC||02-00-00-00-00-00|android 15|1440x3200|android|||",
	}, strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("139 SSO 失败：HTTP %d", resp.StatusCode)
	}
	plainResponse := raw
	if len(bytesTrimSpace(raw)) == 0 {
		return "", errors.New("139 SSO 返回为空")
	}
	if first := bytesTrimSpace(raw)[0]; first != '{' {
		decoded, decodeErr := aesCBCDecryptFromBase64Payload(string(bytesTrimSpace(raw)), pan139ThirdLoginKey1)
		if decodeErr != nil {
			return "", fmt.Errorf("139 SSO 响应解密失败: %w", decodeErr)
		}
		plainResponse = []byte(decoded)
	}
	var envelope struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(plainResponse, &envelope); err != nil {
		return "", errors.New("139 SSO 响应格式无效")
	}
	if envelope.Data == "" {
		return "", errors.New("139 SSO 未返回授权数据")
	}
	inner, err := aesECBDecryptHex(envelope.Data, pan139ThirdLoginKey2)
	if err != nil {
		return "", fmt.Errorf("139 SSO 授权数据解密失败: %w", err)
	}
	var result struct {
		AuthToken    string `json:"authToken"`
		Account      string `json:"account"`
		UserDomainID string `json:"userDomainId"`
	}
	if err := json.Unmarshal([]byte(inner), &result); err != nil {
		return "", errors.New("139 SSO 授权数据格式无效")
	}
	if result.AuthToken == "" || result.Account == "" || result.UserDomainID == "" {
		return "", errors.New("139 SSO 授权信息不完整")
	}
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("pc:%s:%s", result.Account, result.AuthToken))), nil
}

// authorizeWithArtifact calls the authorize endpoint to get the Basic authorization.
func authorizeWithArtifact(ctx context.Context, sid, artifact string, cookies map[string]string, referer string) (string, error) {
	cguid := fmt.Sprint(time.Now().UnixMilli())
	urlValue := fmt.Sprintf("https://mail.10086.cn/Login/Authorize.ashx?sid=%s&func=%s&cguid=%s",
		url.QueryEscape(sid), url.QueryEscape("umc:authorize"), cguid)
	form := url.Values{}
	form.Set("func", "umc:authorize")
	form.Set("sid", sid)
	form.Set("cguid", cguid)
	form.Set("appId", "33000002")
	form.Set("redirect_uri", "https://yun.139.com/w/")
	form.Set("action", "http://yun.139.com/w/")
	form.Set("userType", "1")
	form.Set("artifact", artifact)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlValue, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://mail.10086.cn")
	req.Header.Set("Referer", "https://mail.10086.cn/")
	req.Header.Set("User-Agent", ua)
	if c := formatCookieMap(cookies); c != "" {
		req.Header.Set("Cookie", c)
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	m := regexp.MustCompile(`(?s)"authorization"\s*:\s*"([^"]+)"`).FindStringSubmatch(text)
	if len(m) < 2 {
		return "", errors.New("获取 authorization 失败：" + truncate(text, 120))
	}
	return m[1], nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func parseCookieMap(raw string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		i := strings.Index(part, "=")
		if i <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:i])
		value := strings.TrimSpace(part[i+1:])
		if name != "" {
			m[name] = value
		}
	}
	return m
}

func formatCookieMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, "; ")
}
