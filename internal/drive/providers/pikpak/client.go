// Package pikpak implements the PikPak provider (api-drive.mypikpak.com).
package pikpak

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const (
	apiHost  = "https://api-drive.mypikpak.com"
	userHost = "https://user.mypikpak.com"
	RootID   = "pikpak_root"

	clientID      = "YUMx5nI8ZU8Ap8pm"
	clientVersion = "2.0.0"
	packageName   = "mypikpak.com"
	redirectURI   = "xlaccsdk01://xbase.cloud/callback?state=harbor"
	userAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:129.0) Gecko/20100101 Firefox/129.0"
)

// captchaSalts mirrors the reference client's salt chain for captcha_sign.
var captchaSalts = []string{
	"C9qPpZLN8ucRTaTiUMWYS9cQvWOE", "+r6CQVxjzJV6LCV", "F", "pFJRC",
	"9WXYIDGrwTCz2OiVlgZa90qpECPD6olt", "/750aCr4lm/Sly/c", "RB+DT/gZCrbV", "",
	"CyLsf7hdkIRxRm215hl", "7xHvLi2tOYP0Y92b", "ZGTXXxu8E/MIWaEDB+Sm/", "1UI3",
	"E7fP5Pfijd+7K+t6Tg/NhuLq0eEUVChpJSkrKxpO", "ihtqpG6FMt65+Xk+tWUH2", "NhXXU9rg4XXdzo7u5o",
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// captchaSign builds the 1.<md5-chain> signature.
func captchaSign(deviceID, timestamp string) string {
	sign := clientID + clientVersion + packageName + deviceID + timestamp
	for _, salt := range captchaSalts {
		sign = md5hex(sign + salt)
	}
	return "1." + sign
}

// CaptchaMeta is the meta object sent to captcha/init.
type CaptchaMeta struct {
	CaptchaSign   string `json:"captcha_sign"`
	ClientVersion string `json:"client_version"`
	PackageName   string `json:"package_name"`
	Timestamp     string `json:"timestamp"`
	UserID        string `json:"user_id,omitempty"`
	Email         string `json:"email,omitempty"`
	PhoneNumber   string `json:"phone_number,omitempty"`
	Username      string `json:"username,omitempty"`
}

func loginCaptchaMeta(username string) CaptchaMeta {
	u := strings.TrimSpace(username)
	meta := CaptchaMeta{ClientVersion: clientVersion, PackageName: packageName}
	if strings.Contains(u, "@") && strings.Contains(u, ".") {
		meta.Email = u
	} else if isPhone(u) {
		meta.PhoneNumber = strings.ReplaceAll(strings.ReplaceAll(u, " ", ""), "-", "")
	} else {
		meta.Username = u
	}
	return meta
}

func isPhone(s string) bool {
	if len(s) < 6 || len(s) > 18 {
		return false
	}
	for _, r := range s {
		if r == '+' {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// client is an authenticated PikPak session.
type client struct {
	http        *netx.Client
	accessToken string
	deviceID    string
	accountID   string
}

const pikpakFileDetailTTL = 45 * time.Second

type pikpakFileDetailCacheEntry struct {
	item      File
	expiresAt time.Time
}

var pikpakFileDetailCache = struct {
	sync.Mutex
	items map[string]pikpakFileDetailCacheEntry
}{items: make(map[string]pikpakFileDetailCacheEntry)}

type pikpakVIPCacheEntry struct {
	isVIP     bool
	expiresAt time.Time
}

var pikpakVIPCache = struct {
	sync.Mutex
	items map[string]pikpakVIPCacheEntry
}{items: make(map[string]pikpakVIPCacheEntry)}

func newClient(accessToken, deviceID, accountID string) *client {
	return &client{
		http:        netx.NewClientWithSystemProxy(90 * time.Second),
		accessToken: accessToken,
		deviceID:    deviceID,
		accountID:   strings.TrimSpace(accountID),
	}
}

func (c *client) headers(extra map[string]string) map[string]string {
	h := map[string]string{
		"User-Agent":       userAgent,
		"Accept":           "application/json",
		"Referer":          "https://mypikpak.com/",
		"X-Client-Id":      clientID,
		"X-Device-Id":      c.deviceID,
		"X-Client-Version": clientVersion,
	}
	if c.accessToken != "" {
		h["Authorization"] = "Bearer " + c.accessToken
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// jsonDo performs an authenticated JSON call against the drive API.
func (c *client) jsonDo(ctx context.Context, method, path string, body any, out any, extra map[string]string) error {
	action := method + ":" + path
	err := c.jsonDoOnce(ctx, method, path, body, out, withCaptchaToken(extra, cachedAPICaptchaToken(c, action)))
	if err != nil && isCaptchaError(err) {
		// Exchange the cached token (when present) for a fresh action token and retry once.
		tok, terr := apiCaptchaToken(ctx, c, action, true)
		if terr == nil && tok != "" {
			return c.jsonDoOnce(ctx, method, path, body, out, withCaptchaToken(extra, tok))
		}
	}
	return err
}

// jsonDoOnce performs a single authenticated JSON call without captcha retry.
func (c *client) jsonDoOnce(ctx context.Context, method, path string, body any, out any, extra map[string]string) error {
	target := apiURL(path)
	var reader io.Reader
	if body != nil {
		reader = netx.JSONBody(body)
	}
	resp, err := c.http.Do(ctx, method, target, c.headers(extra), reader)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return parseAPIErrorWithRetry(data, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if out == nil {
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// isCaptchaError reports whether err is a PikPak captcha challenge that can be
// retried after acquiring a fresh X-Captcha-Token.
func isCaptchaError(err error) bool {
	if err == nil {
		return false
	}
	var captchaErr *pikpakCaptchaError
	if errors.As(err, &captchaErr) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "captcha_required") || strings.Contains(s, "captcha_invalid") || strings.Contains(s, "验证失败")
}

func isCaptchaRequiredError(err error) bool {
	var captchaErr *pikpakCaptchaError
	if errors.As(err, &captchaErr) {
		return captchaErr.reason == "captcha_required"
	}
	return strings.Contains(errString(err), "captcha_required")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type pikpakCaptchaError struct{ reason string }

func (e *pikpakCaptchaError) Error() string {
	switch e.reason {
	case "captcha_invalid", "captcha_required":
		return "PikPak captcha validation failed; retry sign-in"
	default:
		return "PikPak captcha validation failed"
	}
}

// get performs a GET with query params.
func (c *client) get(ctx context.Context, path string, q url.Values, out any) error {
	action := http.MethodGet + ":" + path
	err := c.getOnce(ctx, path, q, out, withCaptchaToken(nil, cachedAPICaptchaToken(c, action)))
	if err != nil && isCaptchaError(err) {
		tok, terr := apiCaptchaToken(ctx, c, action, true)
		if terr == nil && tok != "" {
			return c.getOnce(ctx, path, q, out, withCaptchaToken(nil, tok))
		}
	}
	return err
}

func withCaptchaToken(extra map[string]string, token string) map[string]string {
	if token == "" && len(extra) == 0 {
		return nil
	}
	merged := make(map[string]string, len(extra)+1)
	for k, v := range extra {
		merged[k] = v
	}
	if token != "" {
		merged["X-Captcha-Token"] = token
	}
	return merged
}

func (c *client) getOnce(ctx context.Context, path string, q url.Values, out any, extra map[string]string) error {
	target := apiURL(path)
	if q != nil {
		target += "?" + q.Encode()
	}
	resp, err := c.http.Do(ctx, http.MethodGet, target, c.headers(extra), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return parseAPIErrorWithRetry(data, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func apiURL(path string) string {
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		return path
	}
	return apiHost + "/" + strings.TrimPrefix(path, "/")
}

const (
	pikpakMinRateLimitSeconds        = 30
	pikpakRiskControlCooldownSeconds = 10 * 60
)

// PikPakAccessProhibitedError marks a provider-side risk-control block. It is
// deliberately distinct from ordinary authentication failures so callers do
// not immediately repeat the same request and extend the block.
type PikPakAccessProhibitedError struct {
	RetryAfterSeconds int
}

func (e *PikPakAccessProhibitedError) Error() string {
	return fmt.Sprintf("PikPak access was prohibited by provider risk control; retry after %d seconds", e.retryAfterSeconds())
}

func (e *PikPakAccessProhibitedError) retryAfterSeconds() int {
	seconds := e.RetryAfterSeconds
	if seconds < pikpakRiskControlCooldownSeconds {
		seconds = pikpakRiskControlCooldownSeconds
	}
	return seconds
}

// RetryAfter keeps provider-side risk control on the affected account only,
// while preventing a quick manual retry from extending the block.
func (e *PikPakAccessProhibitedError) RetryAfter() time.Duration {
	return time.Duration(e.retryAfterSeconds()) * time.Second
}

// PikPakRateLimitError tells the UI how long the provider asks the user to
// wait before trying the request again. The server has returned both 429 and
// provider-specific "too many" payloads, so status alone is not sufficient.
type PikPakRateLimitError struct {
	RetryAfterSeconds int
}

func (e *PikPakRateLimitError) Error() string {
	seconds := e.RetryAfterSeconds
	if seconds < pikpakMinRateLimitSeconds {
		seconds = pikpakMinRateLimitSeconds
	}
	return fmt.Sprintf("PikPak login requests are rate limited; retry after %d seconds", seconds)
}

// RetryAfter exposes the provider-supplied cooldown to generic callers such
// as the account refresh cache. Keeping the typed duration on the error means
// callers do not need to parse localized error text to avoid an early retry.
func (e *PikPakRateLimitError) RetryAfter() time.Duration {
	seconds := e.RetryAfterSeconds
	if seconds < pikpakMinRateLimitSeconds {
		seconds = pikpakMinRateLimitSeconds
	}
	return time.Duration(seconds) * time.Second
}

func parseAPIError(data []byte, status int) error {
	return parseAPIErrorWithRetry(data, status, "")
}

func parseAPIErrorWithRetry(data []byte, status int, retryAfter string) error {
	var e struct {
		Error            string          `json:"error"`
		ErrorDescription string          `json:"error_description"`
		Message          string          `json:"message"`
		Reason           string          `json:"reason"`
		Code             int             `json:"code"`
		ErrorCode        int             `json:"error_code"`
		RetryAfter       json.RawMessage `json:"retry_after"`
		RetryAfterSecond json.RawMessage `json:"retry_after_seconds"`
	}
	_ = json.Unmarshal(data, &e)
	detail := strings.ToLower(strings.Join([]string{e.Error, e.ErrorDescription, e.Message, e.Reason, string(data)}, " "))
	if status == http.StatusTooManyRequests || e.Code == http.StatusTooManyRequests || e.ErrorCode == http.StatusTooManyRequests ||
		strings.Contains(detail, "too_many") || strings.Contains(detail, "too many") ||
		strings.Contains(detail, "too_frequent") || strings.Contains(detail, "too frequent") || strings.Contains(detail, "request_frequency") ||
		strings.Contains(detail, "too_fluent") || strings.Contains(detail, "too fluent") || strings.Contains(detail, "too fast") ||
		strings.Contains(detail, "rate_limit") || strings.Contains(detail, "rate limited") ||
		strings.Contains(detail, "请求频繁") || strings.Contains(detail, "操作频繁") {
		seconds := pikpakMinRateLimitSeconds
		for _, raw := range []json.RawMessage{e.RetryAfter, e.RetryAfterSecond} {
			if value := parseRetryAfterSeconds(raw); value > seconds {
				seconds = value
			}
		}
		if value := parseRetryAfterHeader(retryAfter); value > seconds {
			seconds = value
		}
		return &PikPakRateLimitError{RetryAfterSeconds: seconds}
	}
	if strings.Contains(detail, "accessprohibited") || strings.Contains(detail, "access_prohibited") ||
		strings.Contains(detail, "access prohibited") {
		seconds := pikpakRiskControlCooldownSeconds
		for _, raw := range []json.RawMessage{e.RetryAfter, e.RetryAfterSecond} {
			if value := parseRetryAfterSeconds(raw); value > seconds {
				seconds = value
			}
		}
		if value := parseRetryAfterHeader(retryAfter); value > seconds {
			seconds = value
		}
		return &PikPakAccessProhibitedError{RetryAfterSeconds: seconds}
	}
	// 常见错误友好化（对齐旧版 parsePikPakError）
	switch strings.ToLower(e.Error) {
	case "invalid_account_or_password":
		return errors.New("PikPak invalid account or password")
	case "captcha_invalid", "captcha_required":
		return &pikpakCaptchaError{reason: strings.ToLower(e.Error)}
	}
	msg := e.ErrorDescription
	if msg == "" {
		msg = e.Message
	}
	if msg == "" {
		msg = e.Error
	}
	if msg == "" {
		msg = fmt.Sprintf("pikpak: http %d", status)
	}
	if e.Reason != "" {
		msg = e.Reason + ": " + msg
	}
	return errors.New(msg)
}

func parseRetryAfterSeconds(raw json.RawMessage) int {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err == nil && seconds > 0 {
		return int(seconds + 0.999999)
	}
	if when, err := http.ParseTime(value); err == nil {
		remaining := time.Until(when)
		if remaining > 0 {
			return int((remaining + time.Second - 1) / time.Second)
		}
	}
	return 0
}

func parseRetryAfterHeader(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	return parseRetryAfterSeconds(json.RawMessage(strconv.Quote(value)))
}

// File is a raw PikPak drive file item.
type File struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	Size           int64          `json:"size"`
	ParentID       string         `json:"parent_id"`
	CreatedTime    string         `json:"created_time"`
	ModifiedTime   string         `json:"modified_time"`
	Trashed        bool           `json:"trashed"`
	Starred        bool           `json:"starred"`
	Thumbnail      string         `json:"thumbnail_link"`
	WebContentLink string         `json:"web_content_link"`
	Medias         []Media        `json:"medias"`
	Phase          string         `json:"phase"`
	FileExtension  string         `json:"file_extension"`
	MimeType       string         `json:"mime_type"`
	Duration       any            `json:"duration"`
	Params         map[string]any `json:"params"`
	Links          *struct {
		ApplicationOctetStream *struct {
			URL string `json:"url"`
		} `json:"application/octet-stream"`
	} `json:"links"`
}

// PikPak encodes 64-bit byte counts as strings on its live API, while some
// endpoints return JSON numbers. Decode both without a float64 intermediate.
type apiInt64 int64

func (n *apiInt64) UnmarshalJSON(data []byte) error {
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	if string(data) == "null" {
		*n = 0
		return nil
	}
	value, err := number.Int64()
	if err != nil {
		return fmt.Errorf("pikpak: invalid integer: %w", err)
	}
	*n = apiInt64(value)
	return nil
}

func (f *File) UnmarshalJSON(data []byte) error {
	type plainFile File
	var decoded struct {
		plainFile
		Size apiInt64 `json:"size"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*f = File(decoded.plainFile)
	f.Size = int64(decoded.Size)
	return nil
}

// Media is a video media stream entry.
type Media struct {
	MediaName      string `json:"media_name"`
	ResolutionName string `json:"resolution_name"`
	IsOrigin       bool   `json:"is_origin"`
	Category       string `json:"category"`
	NeedMoreQuota  bool   `json:"need_more_quota"`
	IsVisible      *bool  `json:"is_visible"`
	Priority       int    `json:"priority"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Link           *struct {
		URL  string `json:"url"`
		Type string `json:"type"`
	} `json:"link"`
	Video *MediaVideo `json:"video"`
}

type MediaVideo struct {
	VideoType string `json:"video_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Duration  any    `json:"duration"`
}

// listResp is the paginated file listing.
type listResp struct {
	Files         []File `json:"files"`
	NextPageToken string `json:"next_page_token"`
}

// ListPage lists one page.
func (c *client) ListPage(ctx context.Context, parentID, pageToken string, trashed bool) ([]File, string, error) {
	q := url.Values{}
	q.Set("limit", "100")
	q.Set("thumbnail_size", "SIZE_LARGE")
	q.Set("with_audit", "false")
	if trashed {
		q.Set("parent_id", "*")
		q.Set("filters", `{"trashed":{"eq":true}}`)
	} else {
		q.Set("filters", `{"trashed":{"eq":false},"phase":{"eq":"PHASE_TYPE_COMPLETE"}}`)
		if parentID != "" && parentID != RootID && parentID != "*" {
			q.Set("parent_id", parentID)
		}
	}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	var resp listResp
	if err := c.get(ctx, "/drive/v1/files", q, &resp); err != nil {
		return nil, "", err
	}
	return resp.Files, resp.NextPageToken, nil
}

// List fetches all pages.
func (c *client) List(ctx context.Context, parentID string, trashed bool) ([]File, error) {
	var out []File
	token := ""
	seen := map[string]bool{}
	for {
		items, next, err := c.ListPage(ctx, parentID, token, trashed)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			break
		}
		if seen[next] {
			return nil, errors.New("pikpak: duplicate page token")
		}
		seen[next] = true
		token = next
	}
	return out, nil
}

func (c *client) detailCacheKey(fileID string) string {
	return strings.Join([]string{c.deviceID, c.accountID, md5hex(c.accessToken), fileID}, "\x00")
}

func (c *client) detailOnce(ctx context.Context, fileID, usage string) (*File, error) {
	var f File
	q := url.Values{"_magic": {"2021"}, "usage": {usage}, "thumbnail_size": {"SIZE_LARGE"}}
	if err := c.get(ctx, "/drive/v1/files/"+url.PathEscape(fileID), q, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func fileHasPlayableLink(f *File) bool {
	if originalLink(f) != "" || f.WebContentLink != "" {
		return true
	}
	for _, media := range f.Medias {
		if media.Link != nil && media.Link.URL != "" {
			return true
		}
	}
	return false
}

func linksExpireSoon(f *File) bool {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	urls := []string{originalLink(f), f.WebContentLink}
	for _, media := range f.Medias {
		if media.Link != nil {
			urls = append(urls, media.Link.URL)
		}
	}
	for _, raw := range urls {
		if expiresAt := driveutil.GetExpiresTime(raw); expiresAt > 0 && expiresAt <= deadline {
			return true
		}
	}
	return false
}

// Detail keeps short-lived metadata and signed links per account. The cache
// key includes the access token so a refreshed session cannot reuse an old
// account's links.
func (c *client) Detail(ctx context.Context, fileID string) (*File, error) {
	return c.detailWithUsage(ctx, fileID, "FETCH")
}

func (c *client) detailWithUsage(ctx context.Context, fileID, usage string) (*File, error) {
	key := c.detailCacheKey(fileID) + "\x00" + usage
	now := time.Now()
	pikpakFileDetailCache.Lock()
	entry, ok := pikpakFileDetailCache.items[key]
	if ok && entry.expiresAt.After(now) && !linksExpireSoon(&entry.item) {
		item := entry.item
		pikpakFileDetailCache.Unlock()
		return &item, nil
	}
	if ok {
		delete(pikpakFileDetailCache.items, key)
	}
	pikpakFileDetailCache.Unlock()

	item, err := c.detailOnce(ctx, fileID, usage)
	if err != nil {
		return nil, err
	}
	if !fileHasPlayableLink(item) {
		timer := time.NewTimer(400 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if retry, retryErr := c.detailOnce(ctx, fileID, usage); retryErr == nil {
			item = retry
		}
	}
	pikpakFileDetailCache.Lock()
	pikpakFileDetailCache.items[key] = pikpakFileDetailCacheEntry{item: *item, expiresAt: time.Now().Add(pikpakFileDetailTTL)}
	pikpakFileDetailCache.Unlock()
	return item, nil
}

// About returns quota.
func (c *client) About(ctx context.Context) (used, total int64) {
	var resp struct {
		Quota struct {
			Limit apiInt64  `json:"limit"`
			Used  apiInt64  `json:"used"`
			Usage *apiInt64 `json:"usage"`
		} `json:"quota"`
	}
	if err := c.get(ctx, "/drive/v1/about", nil, &resp); err == nil {
		if resp.Quota.Usage != nil {
			return int64(*resp.Quota.Usage), int64(resp.Quota.Limit)
		}
		return int64(resp.Quota.Used), int64(resp.Quota.Limit)
	}
	return 0, 0
}

func originalLink(f *File) string {
	if f == nil {
		return ""
	}
	if f.Links != nil && f.Links.ApplicationOctetStream != nil && f.Links.ApplicationOctetStream.URL != "" {
		return f.Links.ApplicationOctetStream.URL
	}
	return f.WebContentLink
}

func fidFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Query().Get("fid")
}

// bestDownloadLink mirrors the legacy provider's same-fid media selection.
func bestDownloadLink(f *File) string {
	original := originalLink(f)
	originalFID := fidFromURL(original)
	if originalFID == "" {
		return original
	}
	firstSameFID := ""
	for _, media := range f.Medias {
		if media.Link == nil || !strings.HasPrefix(media.Link.URL, "http") || fidFromURL(media.Link.URL) != originalFID {
			continue
		}
		if firstSameFID == "" {
			firstSameFID = media.Link.URL
		}
		if media.IsOrigin || media.Category == "category_origin" {
			return media.Link.URL
		}
	}
	if firstSameFID != "" {
		return firstSameFID
	}
	return original
}

func originMediaLink(f *File) string {
	original := originalLink(f)
	originalFID := fidFromURL(original)
	for _, media := range f.Medias {
		if media.Link == nil || !media.IsOrigin || !strings.HasPrefix(media.Link.URL, "http") {
			continue
		}
		if originalFID == "" || fidFromURL(media.Link.URL) == originalFID {
			return media.Link.URL
		}
	}
	return original
}

// DownloadURL resolves the direct download URL.
func (c *client) DownloadURL(ctx context.Context, fileID string) (string, int64, error) {
	f, err := c.Detail(ctx, fileID)
	if err != nil {
		// Authentication, permission and transport failures are authoritative.
		// A secondary endpoint's 404 must not disguise them as a missing file.
		return "", 0, err
	}
	if direct := bestDownloadLink(f); direct != "" {
		return direct, f.Size, nil
	}
	var resp struct {
		URL  string   `json:"url"`
		Size apiInt64 `json:"size"`
	}
	if err := c.get(ctx, "/drive/v1/files/"+url.PathEscape(fileID)+"/download?redirect=false", nil, &resp); err != nil {
		// fall back to detail web_content_link
		f, err2 := c.Detail(ctx, fileID)
		if err2 != nil {
			return "", 0, err
		}
		if direct := bestDownloadLink(f); direct != "" {
			return direct, f.Size, nil
		}
		if f.WebContentLink == "" {
			return "", 0, errors.New("pikpak: no download url")
		}
		return f.WebContentLink, f.Size, nil
	}
	if resp.URL == "" {
		return "", 0, errors.New("pikpak: no download url")
	}
	return resp.URL, int64(resp.Size), nil
}

// VipInfo returns whether the account has a VIP.
func (c *client) VipInfo(ctx context.Context) bool {
	cacheKey := strings.Join([]string{c.deviceID, c.accountID}, "\x00")
	now := time.Now()
	pikpakVIPCache.Lock()
	if entry, ok := pikpakVIPCache.items[cacheKey]; ok && entry.expiresAt.After(now) {
		pikpakVIPCache.Unlock()
		return entry.isVIP
	}
	pikpakVIPCache.Unlock()
	var resp struct {
		Data *struct {
			Status string `json:"status"`
			Type   string `json:"type"`
			Expire string `json:"expire"`
		} `json:"data"`
		Vip struct {
			Identity int `json:"identity"`
		} `json:"vip"`
	}
	if err := c.get(ctx, "/drive/v1/privilege/vip", nil, &resp); err != nil {
		return false
	}
	isVIP := resp.Vip.Identity > 0
	if resp.Data != nil {
		expires, _ := time.Parse(time.RFC3339, resp.Data.Expire)
		kind := strings.ToLower(strings.TrimSpace(resp.Data.Type))
		isVIP = strings.EqualFold(resp.Data.Status, "ok") && ((kind != "" && kind != "novip") || expires.After(now))
	}
	pikpakVIPCache.Lock()
	pikpakVIPCache.items[cacheKey] = pikpakVIPCacheEntry{isVIP: isVIP, expiresAt: time.Now().Add(10 * time.Minute)}
	pikpakVIPCache.Unlock()
	return isVIP
}

// PlayInfo resolves video transcode qualities.
func (c *client) PlayInfo(ctx context.Context, fileID string) (*model.VideoPreview, error) {
	f, err := c.detailWithUsage(ctx, fileID, "CACHE")
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("file_id", fileID)
	q.Set("_", strconv.FormatInt(time.Now().UnixMilli(), 10))
	var resp struct {
		MediaPlayInfo *struct {
			TranscodeList []struct {
				TemplateID     string      `json:"template_id"`
				ResolutionName string      `json:"resolution_name"`
				MediaName      string      `json:"media_name"`
				URL            string      `json:"url"`
				Width          int         `json:"width"`
				Height         int         `json:"height"`
				Status         string      `json:"status"`
				Video          *MediaVideo `json:"video"`
				Audio          *struct {
					AudioType string `json:"audio_type"`
				} `json:"audio"`
			} `json:"transcode_list"`
		} `json:"media_play_info"`
	}
	// The detail response is the primary playback source, as in the legacy
	// client. A missing secondary endpoint must not hide valid media links.
	hasTranscode := false
	for _, media := range f.Medias {
		if media.Link != nil && media.Link.URL != "" && !media.IsOrigin && media.Category != "category_origin" && (media.IsVisible == nil || *media.IsVisible) {
			hasTranscode = true
			break
		}
	}
	if !hasTranscode {
		_ = c.get(ctx, "/drive/v1/files/"+url.PathEscape(fileID)+"/video/play_info", q, &resp)
	}
	isVip := c.VipInfo(ctx)
	preview := &model.VideoPreview{FileID: fileID, Size: f.Size, Duration: fileDurationSeconds(f)}
	for _, media := range f.Medias {
		if media.Link == nil || media.Link.URL == "" || media.IsVisible != nil && !*media.IsVisible || media.IsOrigin || media.Category == "category_origin" {
			continue
		}
		height, width := media.Height, media.Width
		kind := media.Link.Type
		if media.Video != nil {
			if height == 0 {
				height = media.Video.Height
			}
			if width == 0 {
				width = media.Video.Width
			}
			if kind == "" {
				kind = media.Video.VideoType
			}
		}
		height = resolutionHeight(height, media.ResolutionName, media.MediaName)
		if !isVip && (media.NeedMoreQuota || height > 720) {
			continue
		}
		tier := qualityTier(height)
		preview.Qualities = append(preview.Qualities, model.VideoQuality{
			HTML: tier.html, Quality: tier.quality, Label: tier.html, Value: tier.quality,
			Height: height, Width: width, URL: media.Link.URL, Type: streamType(media.Link.URL, kind),
		})
	}
	if resp.MediaPlayInfo != nil {
		for _, tc := range resp.MediaPlayInfo.TranscodeList {
			if tc.URL == "" || tc.Status != "" && tc.Status != "success" {
				continue
			}
			height := tc.Height
			width := tc.Width
			streamTypeName := ""
			if tc.Video != nil {
				if height == 0 {
					height = tc.Video.Height
				}
				if width == 0 {
					width = tc.Video.Width
				}
				streamTypeName = tc.Video.VideoType
			}
			height = resolutionHeight(height, tc.ResolutionName, tc.MediaName, tc.TemplateID)
			if !isVip && height > 720 {
				continue
			}
			tier := qualityTier(height)
			preview.Qualities = append(preview.Qualities, model.VideoQuality{
				HTML: tier.html, Quality: tier.quality, Height: height, Width: width,
				Label: tier.html, Value: tier.quality, URL: tc.URL,
				Type: streamType(tc.URL, streamTypeName),
			})
		}
	}
	// origin
	if len(preview.Qualities) > 0 {
		preview.CurrentQuality = preview.Qualities[0].Value
	}
	origin := originMediaLink(f)
	if origin != "" {
		q := model.VideoQuality{HTML: "原画", Quality: "Origin", Label: "原画", Value: "Origin", URL: origin, Type: streamType(origin, f.MimeType), ForceProxy: true}
		if isVip {
			preview.Qualities = append([]model.VideoQuality{q}, preview.Qualities...)
		} else {
			preview.Qualities = append(preview.Qualities, q)
		}
	}
	if len(preview.Qualities) == 0 {
		return nil, errors.New("pikpak: no playable quality")
	}
	if preview.CurrentQuality == "" {
		preview.CurrentQuality = preview.Qualities[0].Value
	}
	return preview, nil
}

type tierInfo struct{ quality, html string }

var (
	resolutionPixelPattern = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]{3,4})\s*p(?:\b|$)`)
	resolutionKPattern     = regexp.MustCompile(`(?i)(?:^|[^0-9])([248])\s*k(?:\b|$)`)
)

func resolutionHeight(height int, labels ...string) int {
	if height > 0 {
		return height
	}
	label := strings.ToLower(strings.Join(labels, " "))
	if match := resolutionPixelPattern.FindStringSubmatch(label); len(match) == 2 {
		if value, err := strconv.Atoi(match[1]); err == nil {
			return value
		}
	}
	if match := resolutionKPattern.FindStringSubmatch(label); len(match) == 2 {
		if value, err := strconv.Atoi(match[1]); err == nil {
			return value * 512
		}
	}
	return 0
}

func durationSeconds(raw any) int64 {
	value := 0.0
	switch v := raw.(type) {
	case float64:
		value = v
	case float32:
		value = float64(v)
	case int:
		value = float64(v)
	case int64:
		value = float64(v)
	case json.Number:
		value, _ = v.Float64()
	case string:
		value, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	if value <= 0 {
		return 0
	}
	if value > 100000 {
		value /= 1000
	}
	return int64(value + 0.5)
}

func fileDurationSeconds(f *File) int64 {
	if f == nil {
		return 0
	}
	best := durationSeconds(f.Duration)
	for _, media := range f.Medias {
		if media.Video != nil {
			if value := durationSeconds(media.Video.Duration); value > best {
				best = value
			}
		}
	}
	if value := durationSeconds(f.Params["duration"]); value > best {
		best = value
	}
	return best
}

func qualityTier(height int) tierInfo {
	switch {
	case height >= 2000:
		return tierInfo{"QHD", "2560p"}
	case height >= 1000:
		return tierInfo{"FHD", "1080P"}
	case height >= 700:
		return tierInfo{"HD", "720P"}
	case height >= 500:
		return tierInfo{"SD", "540P"}
	default:
		return tierInfo{"LD", "480P"}
	}
}

func streamType(rawURL, t string) string {
	if stream := streamTypeHint(t); stream != "" {
		return stream
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		if stream := streamTypePath(parsed.Path); stream != "" {
			return stream
		}
	}
	return "mp4"
}

func streamTypeHint(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	switch value {
	case "hls", "m3u8", "application/vnd.apple.mpegurl", "application/x-mpegurl":
		return "hls"
	case "dash", "mpd", "application/dash+xml":
		return "dash"
	case "ts", "mpegts", "video/mp2t", "video/mpegts", "video/x-mpegts":
		return "ts"
	case "webm", "video/webm":
		return "webm"
	case "mkv", "matroska", "matroska,webm", "video/x-matroska":
		return "mkv"
	case "avi", "video/x-msvideo":
		return "avi"
	case "flv", "video/x-flv":
		return "flv"
	case "wmv", "video/x-ms-wmv":
		return "wmv"
	case "rm", "rmvb":
		return value
	case "video/vnd.rn-realvideo":
		return "rmvb"
	case "m2ts", "mts", "mpeg", "mpg", "mov", "m4v", "3gp":
		return value
	case "video/mpeg":
		return "mpeg"
	case "video/quicktime":
		return "mov"
	case "video/3gpp":
		return "3gp"
	default:
		return ""
	}
}

func streamTypePath(value string) string {
	path := strings.ToLower(value)
	for _, container := range []string{"m3u8", "mpd", "m2ts", "webm", "mkv", "avi", "flv", "wmv", "rmvb", "mpeg", "mts", "mpg", "mov", "m4v", "3gp", "ts", "rm"} {
		if strings.HasSuffix(path, "."+container) {
			switch container {
			case "m3u8":
				return "hls"
			case "mpd":
				return "dash"
			case "m2ts", "mts":
				return "ts"
			default:
				return container
			}
		}
	}
	return ""
}

func apiParentID(parentID string) string {
	switch strings.TrimSpace(parentID) {
	case "", "root", RootID, "*", "/":
		return ""
	default:
		return parentID
	}
}

// Mkdir creates a folder. PikPak uses the same /files endpoint for folder
// creation; the action-specific create_folder endpoint is not part of the
// legacy API used by the desktop and AList clients.
func (c *client) Mkdir(ctx context.Context, parentID, name string) (*driveMkdirResult, error) {
	var res struct {
		File File   `json:"file"`
		ID   string `json:"id"`
	}
	body := map[string]any{
		"kind": "drive#folder",
		"name": name,
	}
	if parent := apiParentID(parentID); parent != "" {
		body["parent_id"] = parent
	}
	if err := c.jsonDo(ctx, http.MethodPost, "/drive/v1/files", body, &res, nil); err != nil {
		return &driveMkdirResult{Error: err.Error()}, nil
	}
	fileID := res.File.ID
	if fileID == "" {
		fileID = res.ID
	}
	return &driveMkdirResult{FileID: fileID}, nil
}

type driveMkdirResult struct {
	FileID string `json:"file_id"`
	Error  string `json:"error"`
}

// batchOp performs a files:batch* call and waits for every async task to
// finish. A batch response can contain more than one task; waiting only for
// the first one leaves the UI reporting success while other files are still
// being moved or copied.
// PikPak returns a list of async tasks in the response; we poll /drive/v1/tasks
// until each completes (phase != PHASE_TYPE_PENDING/running) or a timeout is
// reached. This mirrors the legacy waitForPikPakTask behavior.
func (c *client) batchOp(ctx context.Context, command string, body map[string]any) error {
	var resp struct {
		Tasks []struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
			Phase  string `json:"phase"`
			Status string `json:"status"`
		} `json:"tasks"`
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
		Task   *struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
		} `json:"task"`
	}
	if err := c.jsonDo(ctx, http.MethodPost, "/drive/v1/files:"+command, body, &resp, nil); err != nil {
		return err
	}
	taskIDs := make([]string, 0, len(resp.Tasks)+2)
	seen := make(map[string]struct{}, len(resp.Tasks)+2)
	appendTaskID := func(taskID string) {
		if taskID == "" {
			return
		}
		if _, ok := seen[taskID]; ok {
			return
		}
		seen[taskID] = struct{}{}
		taskIDs = append(taskIDs, taskID)
	}
	appendTaskID(resp.TaskID)
	appendTaskID(resp.ID)
	if resp.Task != nil {
		appendTaskID(resp.Task.TaskID)
		appendTaskID(resp.Task.ID)
	}
	for _, task := range resp.Tasks {
		appendTaskID(task.TaskID)
		appendTaskID(task.ID)
	}
	if len(taskIDs) == 0 {
		return nil
	}
	for _, taskID := range taskIDs {
		if err := c.waitForTasks(ctx, taskID); err != nil {
			return err
		}
	}
	return nil
}

// waitForTasks polls a PikPak async task until it reaches a terminal phase.
func (c *client) waitForTasks(ctx context.Context, taskID string) error {
	if taskID == "" {
		return nil
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		task, err := c.findTask(ctx, taskID)
		if err != nil {
			return fmt.Errorf("pikpak: query task %s: %w", taskID, err)
		}
		if task == nil {
			return fmt.Errorf("pikpak: task %s not found", taskID)
		}
		phase := task.Phase
		if phase == "" {
			phase = task.Status
		}
		switch phase {
		case "PHASE_TYPE_COMPLETE", "complete", "completed":
			return nil
		case "PHASE_TYPE_ERROR", "error", "failed":
			if task.Message != "" {
				return fmt.Errorf("pikpak: task %s failed: %s", taskID, task.Message)
			}
			return fmt.Errorf("pikpak: task %s failed", taskID)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("pikpak: task %s timeout", taskID)
}

// findTask queries the task endpoint used by file batch operations. Offline
// tasks are exposed through a different filtered collection.
func (c *client) findTask(ctx context.Context, taskID string) (*OfflineTask, error) {
	var raw json.RawMessage
	if err := c.get(ctx, "/drive/v1/tasks/"+url.PathEscape(taskID), nil, &raw); err != nil {
		return nil, err
	}
	var task OfflineTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, err
	}
	if task.TaskID != "" || task.Phase != "" || task.Status != "" {
		return &task, nil
	}
	var envelope struct {
		Task json.RawMessage `json:"task"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Task) == 0 || string(envelope.Task) == "null" {
		return &task, nil
	}
	if err := json.Unmarshal(envelope.Task, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// Rename renames a file.
func (c *client) Rename(ctx context.Context, fileID, name string) error {
	return c.jsonDo(ctx, http.MethodPatch, "/drive/v1/files/"+url.PathEscape(fileID), map[string]any{"name": name}, nil, nil)
}

// Trash moves ids to trash.
func (c *client) Trash(ctx context.Context, ids []string) error {
	return c.batchOp(ctx, "batchTrash", map[string]any{"ids": ids})
}

// Delete permanently deletes ids.
func (c *client) Delete(ctx context.Context, ids []string) error {
	return c.batchOp(ctx, "batchDelete", map[string]any{"ids": ids})
}

// Restore restores ids from trash.
func (c *client) Restore(ctx context.Context, ids []string) error {
	return c.batchOp(ctx, "batchUntrash", map[string]any{"ids": ids})
}

// Move moves ids to a parent.
func (c *client) Move(ctx context.Context, ids []string, toParentID string) error {
	to := map[string]any{}
	if parent := apiParentID(toParentID); parent != "" {
		to["parent_id"] = parent
	}
	return c.batchOp(ctx, "batchMove", map[string]any{
		"ids": ids,
		"to":  to,
	})
}

// Copy copies ids to a parent.
func (c *client) Copy(ctx context.Context, ids []string, toParentID string) error {
	to := map[string]any{}
	if parent := apiParentID(toParentID); parent != "" {
		to["parent_id"] = parent
	}
	return c.batchOp(ctx, "batchCopy", map[string]any{
		"ids": ids,
		"to":  to,
	})
}

// Star sets starred state.
func (c *client) Star(ctx context.Context, ids []string, starred bool) error {
	command := "star"
	if !starred {
		command = "unstar"
	}
	return c.jsonDo(ctx, http.MethodPost, "/drive/v1/files:"+command, map[string]any{"ids": ids}, nil, nil)
}

// ShareResult is a created share.
type ShareResult struct {
	ShareID    string   `json:"share_id"`
	ShareURL   string   `json:"share_url"`
	PassCode   string   `json:"pass_code"`
	Expiration string   `json:"expiration"`
	FileIDs    []string `json:"file_ids"`
}

func (r *ShareResult) UnmarshalJSON(data []byte) error {
	var raw struct {
		ShareID    string   `json:"share_id"`
		ID         string   `json:"id"`
		ShareURL   string   `json:"share_url"`
		ShareLink  string   `json:"share_link"`
		PassCode   string   `json:"pass_code"`
		Passcode   string   `json:"passcode"`
		Expiration string   `json:"expiration"`
		FileIDs    []string `json:"file_ids"`
		FileIDList []string `json:"file_id_list"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.ShareID = firstNonEmpty(raw.ShareID, raw.ID)
	r.ShareURL = firstNonEmpty(raw.ShareURL, raw.ShareLink)
	r.PassCode = firstNonEmpty(raw.PassCode, raw.Passcode)
	r.Expiration = raw.Expiration
	r.FileIDs = raw.FileIDs
	if len(r.FileIDs) == 0 {
		r.FileIDs = raw.FileIDList
	}
	return nil
}

// CreateShare creates a share.
func (c *client) CreateShare(ctx context.Context, fileIDs []string, _ string, passCode string, expiration string) (*ShareResult, error) {
	shareTo := "publiclink"
	passCodeOption := "NOT_REQUIRED"
	if strings.TrimSpace(passCode) != "" {
		shareTo = "encryptedlink"
		passCodeOption = "REQUIRED"
	}
	body := map[string]any{
		"file_ids":         fileIDs,
		"share_to":         shareTo,
		"expiration_days":  pikpakExpirationDays(expiration),
		"pass_code_option": passCodeOption,
	}
	var res ShareResult
	if err := c.jsonDo(ctx, http.MethodPost, "/drive/v1/share", body, &res, nil); err != nil {
		return nil, err
	}
	if res.ShareID == "" || res.ShareURL == "" {
		return nil, errors.New("pikpak: 创建分享未返回有效链接")
	}
	return &res, nil
}

// DeleteShare revokes a PikPak share through the provider's batch endpoint.
// This is a single request and intentionally has no blind endpoint fallback:
// cancellation is destructive and must never be retried against a different
// endpoint after an ambiguous response.
func (c *client) DeleteShare(ctx context.Context, shareID string) error {
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return errors.New("pikpak: 分享标识为空")
	}
	return c.jsonDo(ctx, http.MethodPost, "/drive/v1/share:batchDelete", map[string]any{"ids": []string{shareID}}, nil, nil)
}

func pikpakExpirationDays(expiration string) int {
	value := strings.TrimSpace(expiration)
	if value == "" {
		return -1
	}
	if days, err := strconv.Atoi(value); err == nil && days > 0 {
		return days
	}
	target, err := time.Parse(time.RFC3339, value)
	if err != nil {
		target, err = time.Parse("2006-01-02", value)
	}
	if err != nil {
		return -1
	}
	remaining := time.Until(target)
	if remaining <= 0 {
		return 1
	}
	return int((remaining + 24*time.Hour - 1) / (24 * time.Hour))
}

// OfflineCreate submits an offline download task (magnet/link). PikPak uses
// the file creation API with UPLOAD_TYPE_URL; the /tasks?type=offline shape is
// not compatible with the legacy web client.
func (c *client) OfflineCreate(ctx context.Context, urlValue, fileName, parentID string) (taskID, fileID string, err error) {
	var res struct {
		Task   OfflineTask     `json:"task"`
		TaskID string          `json:"task_id"`
		FileID string          `json:"file_id"`
		File   json.RawMessage `json:"file"`
		ID     string          `json:"id"`
	}
	body := map[string]any{
		"kind":        "drive#file",
		"upload_type": "UPLOAD_TYPE_URL",
		"url":         map[string]any{"url": urlValue},
	}
	if strings.TrimSpace(fileName) != "" {
		body["name"] = fileName
	}
	if parent := apiParentID(parentID); parent != "" {
		body["parent_id"] = parent
		body["folder_type"] = ""
	} else {
		body["folder_type"] = "DOWNLOAD"
	}
	if err := c.jsonDo(ctx, http.MethodPost, "/drive/v1/files", body, &res, nil); err != nil {
		return "", "", err
	}
	taskID = firstNonEmpty(res.Task.TaskID, res.TaskID, res.ID)
	fileID = res.FileID
	if fileID == "" {
		fileID, _, _ = offlineResourceInfo(res.File)
	}
	if taskID == "" && fileID == "" {
		return "", "", errors.New("pikpak: 离线任务响应缺少任务或文件 ID")
	}
	return taskID, fileID, nil
}

// OfflineTask is a raw offline task.
type OfflineTask struct {
	TaskID      string `json:"task_id"`
	FileID      string `json:"file_id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	Phase       string `json:"phase"`
	Message     string `json:"message"`
	FileSize    int64  `json:"file_size,omitempty"`
	CreatedTime string `json:"created_time,omitempty"`
	UpdatedTime string `json:"updated_time,omitempty"`
	URL         string `json:"url,omitempty"`
}

type offlineTaskWire struct {
	ID                string                     `json:"id"`
	TaskID            string                     `json:"task_id"`
	FileID            string                     `json:"file_id"`
	FileName          string                     `json:"file_name"`
	Name              string                     `json:"name"`
	Status            string                     `json:"status"`
	Phase             string                     `json:"phase"`
	Progress          json.RawMessage            `json:"progress"`
	Message           string                     `json:"message"`
	Error             string                     `json:"error"`
	ErrorDescription  string                     `json:"error_description"`
	FileSize          json.RawMessage            `json:"file_size"`
	CreatedTime       string                     `json:"created_time"`
	UpdatedTime       string                     `json:"updated_time"`
	ReferenceResource json.RawMessage            `json:"reference_resource"`
	File              json.RawMessage            `json:"file"`
	Params            map[string]json.RawMessage `json:"params"`
}

func offlineRawString(raw json.RawMessage) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

func offlineRawInt64(raw json.RawMessage) int64 {
	value, err := strconv.ParseFloat(offlineRawString(raw), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int64(value)
}

func offlineProgress(raw json.RawMessage) int {
	value, err := strconv.ParseFloat(offlineRawString(raw), 64)
	if err != nil || value <= 0 {
		return 0
	}
	if value <= 1 {
		value *= 100
	}
	if value > 100 {
		value = 100
	}
	return int(value)
}

func offlineResourceInfo(raw json.RawMessage) (id, name string, size int64) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "", "", 0
	}
	var resource struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Size json.RawMessage `json:"size"`
	}
	if json.Unmarshal(raw, &resource) == nil && (resource.ID != "" || resource.Name != "" || len(resource.Size) > 0) {
		return resource.ID, resource.Name, offlineRawInt64(resource.Size)
	}
	return offlineRawString(raw), "", 0
}

func (t *OfflineTask) UnmarshalJSON(data []byte) error {
	var raw offlineTaskWire
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	resourceID, resourceName, resourceSize := offlineResourceInfo(raw.ReferenceResource)
	fileID, fileName, fileSize := offlineResourceInfo(raw.File)
	t.TaskID = firstNonEmpty(raw.TaskID, raw.ID)
	t.FileID = firstNonEmpty(raw.FileID, resourceID, fileID)
	t.Name = firstNonEmpty(raw.Name, raw.FileName, resourceName, fileName)
	t.Status = raw.Status
	t.Phase = raw.Phase
	if t.Phase == "" {
		t.Phase = t.Status
	}
	if t.Status == "" {
		t.Status = t.Phase
	}
	t.Progress = offlineProgress(raw.Progress)
	if t.Phase == "PHASE_TYPE_COMPLETE" {
		t.Progress = 100
	}
	t.Message = firstNonEmpty(raw.Message, raw.ErrorDescription, raw.Error)
	t.FileSize = offlineRawInt64(raw.FileSize)
	if t.FileSize == 0 {
		t.FileSize = resourceSize
	}
	if t.FileSize == 0 {
		t.FileSize = fileSize
	}
	t.CreatedTime = raw.CreatedTime
	t.UpdatedTime = raw.UpdatedTime
	if raw.Params != nil {
		t.URL = offlineRawString(raw.Params["url"])
	}
	return nil
}

// OfflineList returns offline tasks.
func (c *client) OfflineList(ctx context.Context) ([]OfflineTask, error) {
	const filters = `{"phase":{"in":"PHASE_TYPE_RUNNING,PHASE_TYPE_ERROR,PHASE_TYPE_COMPLETE,PHASE_TYPE_PENDING"}}`
	var out []OfflineTask
	pageToken := ""
	seenTokens := map[string]struct{}{}
	for {
		q := url.Values{}
		q.Set("type", "offline")
		q.Set("thumbnail_size", "SIZE_SMALL")
		q.Set("limit", "10000")
		q.Set("filters", filters)
		q.Set("with", "reference_resource")
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		var resp struct {
			Tasks         []OfflineTask `json:"tasks"`
			NextPageToken string        `json:"next_page_token"`
		}
		if err := c.get(ctx, "/drive/v1/tasks", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Tasks...)
		if resp.NextPageToken == "" {
			break
		}
		if _, ok := seenTokens[resp.NextPageToken]; ok {
			return nil, errors.New("pikpak: offline task page token repeated")
		}
		seenTokens[resp.NextPageToken] = struct{}{}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// OfflineDelete cancels and removes an offline download task. deleteFiles=true
// also removes any partially downloaded file.
func (c *client) OfflineDelete(ctx context.Context, taskIDs []string, deleteFiles bool) error {
	if len(taskIDs) == 0 {
		return nil
	}
	q := url.Values{}
	q.Set("task_ids", strings.Join(taskIDs, ","))
	q.Set("delete_files", strconv.FormatBool(deleteFiles))
	target := apiHost + "/drive/v1/tasks?" + q.Encode()
	resp, err := c.http.Do(ctx, http.MethodDelete, target, c.headers(nil), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return parseAPIErrorWithRetry(b, resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	return nil
}

// FindOfflineTask searches tasks for one matching task/file id.
func (c *client) FindOfflineTask(ctx context.Context, taskID, fileID string) (*OfflineTask, error) {
	tasks, err := c.OfflineList(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if (taskID != "" && tasks[i].TaskID == taskID) || (fileID != "" && tasks[i].FileID == fileID) {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

var _ = netx.DefaultUA
