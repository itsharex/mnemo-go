// Package aliopen implements the Aliyun Drive Open API provider (AList-sourced).
package aliopen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	apiHost        = "https://openapi.alipan.com"
	nativeAPIHost  = "https://api.aliyundrive.com"
	profileAPIHost = "https://api.alipan.com"
	oauthDefault   = "https://api.alistgo.com/alist/ali_open/token"
	RootID         = "aliopen_root"
	BackupRoot     = "backup_root"
	ResourceRoot   = "resource_root"

	defaultDownloadExpireSec = 14400
)

const (
	aliOpenMiB = int64(1024 * 1024)
	aliOpenGiB = int64(1024 * 1024 * 1024)

	// Profile is presentation metadata only. Keep an unavailable profile from
	// adding a request to every startup, while still allowing a later retry.
	aliOpenProfileSuccessInterval = 30 * 24 * time.Hour
	aliOpenProfileRetryInterval   = 24 * time.Hour
)

const providerID = model.ProviderAliopen

// aliOpenRateLimiter mirrors the legacy provider limiter: at most two
// in-flight API calls and a small spacing between requests to reduce
// wind-control responses from the Open API.
type aliOpenRateLimiter struct {
	mu           sync.Mutex
	last         time.Time
	blockedUntil time.Time
	slots        chan struct{}
	interval     time.Duration
}

func newAliOpenRateLimiter(concurrency int, interval time.Duration) *aliOpenRateLimiter {
	if concurrency < 1 {
		concurrency = 1
	}
	return &aliOpenRateLimiter{
		slots:    make(chan struct{}, concurrency),
		interval: interval,
	}
}

func (r *aliOpenRateLimiter) run(ctx context.Context, fn func() error) error {
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.slots }()

	for {
		r.mu.Lock()
		now := time.Now()
		wait := r.interval - now.Sub(r.last)
		if blockedWait := r.blockedUntil.Sub(now); blockedWait > wait {
			wait = blockedWait
		}
		if wait <= 0 {
			r.last = now
			r.mu.Unlock()
			return fn()
		}
		r.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		}
	}
}

func (r *aliOpenRateLimiter) penalize(delay time.Duration) {
	if delay <= 0 {
		return
	}
	r.mu.Lock()
	until := time.Now().Add(delay)
	if until.After(r.blockedUntil) {
		r.blockedUntil = until
	}
	r.mu.Unlock()
}

var aliOpenLimiter = newAliOpenRateLimiter(2, 220*time.Millisecond)

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"favorite":            true,
			"search":              true,
			"createShare":         true,
			"manageCreatedShares": true,
			"cancelCreatedShares": true,
			"shareExpiration":     true,
			"sharePassword":       true,
			"combinedShare":       true,
			"shareHistory":        true,
			"importShare":         true,
			"copy":                true,
			"recycleBin":          true,
			"permanentDelete":     true,
			"trashView":           true,
			"trashRestore":        true,
		}, func(c *drive.Capabilities) {
			c.SetHashes([]string{"sha1"}, []string{"sha1"}).SetConflictPolicies("refuse", "rename", "skip", "overwrite")
		}),
		Auth: authRefreshToken,
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "refresh_token", Type: "text", Label: "Refresh Token", Required: true, Placeholder: "粘贴阿里云盘 Open refresh_token"},
			{Key: "client_id", Type: "text", Label: "Client ID（可选）", Required: false},
			{Key: "client_secret", Type: "password", Label: "Client Secret（可选）", Required: false},
		}},
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// Session is the raw credential JSON blob.
type Session struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	DriveID          string `json:"drive_id"`
	ResourceDriveID  string `json:"resource_drive_id"`
	BackupDriveID    string `json:"backup_drive_id"`
	UserID           string `json:"user_id,omitempty"`
	UserName         string `json:"user_name,omitempty"`
	NickName         string `json:"nick_name,omitempty"`
	Phone            string `json:"phone,omitempty"`
	Avatar           string `json:"avatar,omitempty"`
	ProfileCheckedAt int64  `json:"profile_checked_at,omitempty"`
	ClientID         string `json:"client_id,omitempty"`
	ClientSecret     string `json:"client_secret,omitempty"`
	OAuthTokenURL    string `json:"oauth_token_url,omitempty"`
}

// aliOpenFlexString accepts identifiers returned by the Open API as either
// JSON strings or JSON numbers. Default drive IDs are numeric for some apps.
type aliOpenFlexString string

func (s *aliOpenFlexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*s = aliOpenFlexString(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*s = aliOpenFlexString(number.String())
	return nil
}

func (s aliOpenFlexString) String() string { return string(s) }

type aliOpenTokenResponse struct {
	AccessToken     string            `json:"access_token"`
	RefreshToken    string            `json:"refresh_token"`
	UserID          string            `json:"user_id"`
	UserName        string            `json:"user_name"`
	NickName        string            `json:"nick_name"`
	Phone           string            `json:"phone"`
	Mobile          string            `json:"mobile"`
	Avatar          string            `json:"avatar"`
	DefaultDriveID  aliOpenFlexString `json:"default_drive_id"`
	ResourceDriveID aliOpenFlexString `json:"resource_drive_id"`
	BackupDriveID   aliOpenFlexString `json:"backup_drive_id"`
}

// aliOpenAccountProfile is returned by the consumer profile endpoint. Open
// token responses commonly contain only identifiers, while this endpoint
// exposes the display name / masked phone that should be shown to the user.
type aliOpenAccountProfile struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	NickName string `json:"nick_name"`
	Phone    string `json:"phone"`
	Mobile   string `json:"mobile"`
	Avatar   string `json:"avatar"`
}

func (s *Session) applyTokenProfile(res aliOpenTokenResponse) {
	if s == nil {
		return
	}
	if value := strings.TrimSpace(res.UserID); value != "" {
		s.UserID = value
	}
	if value := strings.TrimSpace(res.UserName); value != "" {
		s.UserName = value
	}
	if value := strings.TrimSpace(res.NickName); value != "" {
		s.NickName = value
	}
	if value := firstAliOpenProfileValue(res.Phone, res.Mobile); value != "" {
		s.Phone = value
	}
	if value := strings.TrimSpace(res.Avatar); value != "" {
		s.Avatar = value
	}
	// Keep an already resolved drive route stable. A fresh session receives its
	// default drive directly from the token response and avoids an extra call.
	if s.DriveID == "" {
		s.DriveID = strings.TrimSpace(res.DefaultDriveID.String())
	}
	if s.ResourceDriveID == "" {
		s.ResourceDriveID = strings.TrimSpace(res.ResourceDriveID.String())
	}
	if s.BackupDriveID == "" {
		s.BackupDriveID = strings.TrimSpace(res.BackupDriveID.String())
	}
}

func (s *Session) applyAccountProfile(profile aliOpenAccountProfile) {
	if s == nil {
		return
	}
	if value := strings.TrimSpace(profile.UserID); value != "" {
		s.UserID = value
	}
	if value := strings.TrimSpace(profile.UserName); value != "" {
		s.UserName = value
	}
	if value := strings.TrimSpace(profile.NickName); value != "" {
		s.NickName = value
	}
	if value := firstAliOpenProfileValue(profile.Phone, profile.Mobile); value != "" {
		s.Phone = value
	}
	if value := strings.TrimSpace(profile.Avatar); value != "" {
		s.Avatar = value
	}
}

func (s *Session) accountID() string {
	if s == nil {
		return ""
	}
	for _, value := range []string{s.UserID, s.DriveID} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (s *Session) displayName() string {
	if s == nil {
		return ""
	}
	if value := s.profileName(); value != "" {
		return value
	}
	return s.accountID()
}

// profileName returns only a human-facing value. The Open token's user_id and
// drive_id are stable identifiers, but must not keep winning over a later
// nickname/phone refresh in the account UI.
func (s *Session) profileName() string {
	if s == nil {
		return ""
	}
	for _, value := range []string{s.NickName, s.UserName, s.Phone} {
		if value = s.profileValue(value); value != "" {
			return value
		}
	}
	return ""
}

func (s *Session) profileValue(value string) string {
	if s == nil {
		return ""
	}
	value = strings.TrimSpace(value)
	if value == "" || value == strings.TrimSpace(s.UserID) || value == strings.TrimSpace(s.DriveID) {
		return ""
	}
	return value
}

func (s *Session) profileRefreshDue(now time.Time) bool {
	if s == nil || s.ProfileCheckedAt <= 0 {
		return s != nil
	}
	interval := aliOpenProfileRetryInterval
	if s.profileName() != "" {
		interval = aliOpenProfileSuccessInterval
	}
	return now.Sub(time.Unix(s.ProfileCheckedAt, 0)) >= interval
}

func applyAliOpenProfile(token *model.TokenInfo, sess *Session) {
	if token == nil || sess == nil {
		return
	}
	if value := strings.TrimSpace(sess.Avatar); value != "" {
		token.Avatar = value
	}
	name := sess.profileName()
	if name == "" {
		return
	}
	// Keep all three display fields coherent: the frontend prefers nick_name,
	// so an old identifier left there would otherwise hide a newly fetched name.
	userName := sess.profileValue(sess.UserName)
	nickName := sess.profileValue(sess.NickName)
	phone := sess.profileValue(sess.Phone)
	token.UserName = firstAliOpenProfileValue(userName, phone, name)
	token.NickName = firstAliOpenProfileValue(nickName, userName, phone, name)
	token.Name = name
}

func applyAliOpenQuota(token *model.TokenInfo, used, total int64) {
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

// Scope is the drive scope: backup or resource (shares).
type Scope string

const (
	ScopeBackup   Scope = "backup"
	ScopeResource Scope = "resource"
)

// FileRef is a parsed file id that includes the scope.
type FileRef struct {
	Scope Scope
	FID   string
}

func parseRef(fileID string) FileRef {
	switch fileID {
	case ResourceRoot:
		return FileRef{Scope: ScopeResource, FID: "root"}
	case BackupRoot:
		return FileRef{Scope: ScopeBackup, FID: "root"}
	}
	if strings.HasPrefix(fileID, "r:") {
		fid := fileID[2:]
		if fid == "" {
			fid = "root"
		}
		return FileRef{Scope: ScopeResource, FID: fid}
	}
	if strings.HasPrefix(fileID, "b:") {
		fid := fileID[2:]
		if fid == "" {
			fid = "root"
		}
		return FileRef{Scope: ScopeBackup, FID: fid}
	}
	return FileRef{Scope: ScopeBackup, FID: fileID}
}

func wrapRef(scope Scope, fid string) string {
	if fid == "" || fid == "root" {
		if scope == ScopeResource {
			return ResourceRoot
		}
		return BackupRoot
	}
	prefix := "b:"
	if scope == ScopeResource {
		prefix = "r:"
	}
	return prefix + fid
}

// aliFile is a raw API file item.
type aliFile struct {
	FileID          string `json:"file_id"`
	Name            string `json:"name"`
	ParentFileID    string `json:"parent_file_id"`
	Type            string `json:"type"`
	Size            int64  `json:"size"`
	UpdatedAt       string `json:"updated_at"`
	CreatedAt       string `json:"created_at"`
	ContentHash     string `json:"content_hash"`
	ContentHashName string `json:"content_hash_name"`
	Thumbnail       string `json:"thumbnail"`
	Category        string `json:"category"`
	Status          string `json:"status"`
	DownloadURL     string `json:"download_url"`
	Starred         bool   `json:"starred"`
}

type aliOpenDownloadResponse struct {
	URL             string            `json:"url"`
	Size            int64             `json:"size"`
	Headers         map[string]string `json:"headers"`
	Expiration      string            `json:"expiration"`
	StreamsURL      map[string]any    `json:"streamsUrl"`
	StreamsURLSnake map[string]any    `json:"streams_url"`
}

// client is an authenticated aliopen session.
type client struct {
	http    *netx.Client
	session *Session
	token   *model.TokenInfo
}

func clientOf(c drive.Context) (*client, error) {
	return clientOfContext(context.Background(), c)
}

func clientOfContext(ctx context.Context, c drive.Context) (*client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Token == nil {
		return nil, drive.ErrUnauthorized
	}
	sess := parseSession(c.Token)
	if sess == nil {
		return nil, errors.New("aliopen: invalid session")
	}
	cl := &client{http: netx.NewClient(60 * time.Second), session: sess, token: c.Token}
	if sess.AccessToken == "" {
		if err := cl.refresh(ctx, c.UserID); err != nil {
			return nil, err
		}
	}
	if sess.DriveID == "" {
		if err := cl.ensureDrive(ctx); err != nil {
			return nil, err
		}
	}
	cl.persistSession()
	return cl, nil
}

func parseSession(tok *model.TokenInfo) *Session {
	if tok == nil {
		return nil
	}
	var sess Session
	raw := tok.RefreshToken
	if raw == "" && tok.OpenAPIRefreshToken != "" {
		raw = tok.OpenAPIRefreshToken
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &sess); err == nil && (sess.RefreshToken != "" || sess.AccessToken != "") {
			if sess.AccessToken == "" && tok.AccessToken != "" {
				sess.AccessToken = tok.AccessToken
			}
			if sess.RefreshToken == "" && tok.OpenAPIRefreshToken != "" {
				sess.RefreshToken = tok.OpenAPIRefreshToken
			}
			return &sess
		}
		// Older builds persisted the refresh token itself instead of the JSON
		// session. Keep those accounts refreshable after migration.
		if len(strings.TrimSpace(raw)) > 20 {
			return &Session{AccessToken: tok.AccessToken, RefreshToken: strings.TrimSpace(raw)}
		}
	}
	if tok.AccessToken != "" {
		return &Session{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken}
	}
	return nil
}

func (c *client) persistSession() {
	if c == nil || c.token == nil || c.session == nil {
		return
	}
	c.token.AccessToken = c.session.AccessToken
	c.token.RefreshToken = mustJSON(c.session)
	c.token.OpenAPIAccessToken = c.session.AccessToken
	c.token.OpenAPIRefreshToken = c.session.RefreshToken
}

func (c *client) refresh(ctx context.Context, _ string) error {
	if err := c.refreshToken(ctx); err != nil {
		return err
	}
	if c.session.DriveID == "" {
		if err := c.ensureDrive(ctx); err != nil {
			return err
		}
	}
	c.persistSession()
	return nil
}

func (c *client) refreshToken(ctx context.Context) error {
	if strings.TrimSpace(c.session.RefreshToken) == "" {
		return drive.AuthExpired("阿里云盘 refresh_token 缺失")
	}
	url := c.session.OAuthTokenURL
	if c.session.ClientID != "" {
		url = apiHost + "/oauth/access_token"
	}
	if url == "" {
		url = oauthDefault
	}
	body := map[string]string{"grant_type": "refresh_token", "refresh_token": c.session.RefreshToken}
	if c.session.ClientID != "" {
		body["client_id"] = c.session.ClientID
		if c.session.ClientSecret != "" {
			body["client_secret"] = c.session.ClientSecret
		}
	}
	var resp *http.Response
	if err := aliOpenLimiter.run(ctx, func() error {
		var err error
		resp, err = c.http.Do(ctx, http.MethodPost, url, map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		}, netx.JSONBody(body))
		return err
	}); err != nil {
		return err
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		aliOpenLimiter.penalize(aliOpenRetryAfter(resp, 8*time.Second))
	}
	if resp.StatusCode >= 400 {
		requestErr := aliOpenRequestErrorOf(data, resp.StatusCode)
		code := strings.ToLower(strings.TrimSpace(aliOpenErrorBodyOf(data).Code))
		if code == "invalid_grant" || strings.Contains(code, "refreshtoken") || strings.Contains(code, "refresh_token") {
			return drive.AuthExpired("阿里云盘授权已过期，请重新登录")
		}
		return requestErr
	}
	var res aliOpenTokenResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return fmt.Errorf("aliopen: refresh response: %w", err)
	}
	if res.AccessToken == "" {
		return aliOpenRequestErrorOf(data, resp.StatusCode)
	}
	c.session.AccessToken = res.AccessToken
	if res.RefreshToken != "" {
		c.session.RefreshToken = res.RefreshToken
	}
	c.session.applyTokenProfile(res)
	c.persistSession()
	return nil
}

// refreshAccountProfile obtains display metadata only when it is due. It is
// deliberately best-effort: a provider's profile endpoint must not make an
// otherwise healthy token or quota refresh fail. The persisted timestamp keeps
// an unavailable endpoint from being retried on every application launch.
func (c *client) refreshAccountProfile(ctx context.Context) {
	if c == nil || c.http == nil || c.session == nil || !c.session.profileRefreshDue(time.Now()) {
		return
	}
	c.session.ProfileCheckedAt = time.Now().Unix()

	var resp *http.Response
	err := aliOpenLimiter.run(ctx, func() error {
		var requestErr error
		resp, requestErr = c.http.Do(ctx, http.MethodPost, profileAPIHost+"/v2/user/get", map[string]string{
			"Authorization": "Bearer " + c.session.AccessToken,
			"Content-Type":  "application/json",
			"Accept":        "application/json",
			"Origin":        "https://www.alipan.com",
			"Referer":       "https://www.alipan.com/",
		}, netx.JSONBody(map[string]any{}))
		return requestErr
	})
	if err != nil || resp == nil {
		return
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		aliOpenLimiter.penalize(aliOpenRetryAfter(resp, 8*time.Second))
	}
	if readErr != nil || resp.StatusCode >= http.StatusBadRequest {
		return
	}
	var profile aliOpenAccountProfile
	if json.Unmarshal(data, &profile) != nil {
		return
	}
	c.session.applyAccountProfile(profile)
}

func (c *client) ensureDrive(ctx context.Context) error {
	type driveInfo struct {
		DefaultDriveID  aliOpenFlexString `json:"default_drive_id"`
		ResourceDriveID aliOpenFlexString `json:"resource_drive_id"`
		BackupDriveID   aliOpenFlexString `json:"backup_drive_id"`
	}
	var info driveInfo
	if err := c.apiPost(ctx, "/adrive/v1.0/user/getDriveInfo", map[string]any{}, &info); err != nil {
		return err
	}
	driveID := strings.TrimSpace(info.DefaultDriveID.String())
	if driveID == "" {
		driveID = strings.TrimSpace(info.ResourceDriveID.String())
	}
	if driveID == "" {
		driveID = strings.TrimSpace(info.BackupDriveID.String())
	}
	if driveID == "" {
		return errors.New("aliopen: 获取 drive_id 失败")
	}
	c.session.DriveID = driveID
	c.session.ResourceDriveID = strings.TrimSpace(info.ResourceDriveID.String())
	c.session.BackupDriveID = strings.TrimSpace(info.BackupDriveID.String())
	c.persistSession()
	return nil
}

func (c *client) scopedDriveID(scope Scope) string {
	if scope == ScopeResource && c.session.ResourceDriveID != "" {
		return c.session.ResourceDriveID
	}
	if scope == ScopeBackup && c.session.BackupDriveID != "" {
		return c.session.BackupDriveID
	}
	return c.session.DriveID
}

// apiPost calls the Aliyun API with auth and rate limiting.
func (c *client) apiPost(ctx context.Context, path string, body any, out any) error {
	return c.apiPostWith(ctx, path, body, out, nil)
}

// apiPostWith is apiPost with extra headers (e.g. x-share-token).
func (c *client) apiPostWith(ctx context.Context, path string, body any, out any, extraHeaders map[string]string) error {
	return c.apiPostAtWithRetry(ctx, apiHost, path, body, out, extraHeaders, true)
}

// apiPostAt calls a compatible Ali endpoint. It is only used for a narrowly
// scoped fallback when the documented Open endpoint has been retired by an
// account region; auth refresh and throttling remain identical to apiPost.
func (c *client) apiPostAt(ctx context.Context, host, path string, body any, out any) error {
	return c.apiPostAtWithRetry(ctx, host, path, body, out, nil, true)
}

func (c *client) apiPostAtWithRetry(ctx context.Context, host, path string, body any, out any, extraHeaders map[string]string, allowRefresh bool) error {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		return errors.New("aliopen: API 地址为空")
	}
	hdrs := map[string]string{
		"Authorization": "Bearer " + c.session.AccessToken,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	for k, v := range extraHeaders {
		switch strings.ToLower(k) {
		case "authorization", "content-type", "accept":
			continue
		}
		hdrs[k] = v
	}
	var resp *http.Response
	if err := aliOpenLimiter.run(ctx, func() error {
		var err error
		resp, err = c.http.Do(ctx, http.MethodPost, host+path, hdrs, netx.JSONBody(body))
		return err
	}); err != nil {
		return err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if aliOpenAuthResponse(resp.StatusCode, data) {
		if !allowRefresh {
			return aliOpenRequestErrorOf(data, resp.StatusCode)
		}
		if err := c.refreshToken(ctx); err != nil {
			return err
		}
		return c.apiPostAtWithRetry(ctx, host, path, body, out, extraHeaders, false)
	}
	if aliOpenRateLimitResponse(resp.StatusCode, data) {
		fallback := 5 * time.Second
		if resp.StatusCode == http.StatusTooManyRequests {
			fallback = 5 * time.Second
		}
		aliOpenLimiter.penalize(aliOpenRetryAfter(resp, fallback))
	}
	if resp.StatusCode >= 400 {
		return aliOpenRequestErrorOf(data, resp.StatusCode)
	}
	if aliOpenAPIError(data) {
		return aliOpenRequestErrorOf(data, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

type aliOpenErrorBody struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// aliOpenRequestError keeps HTTP status and the provider error code separate
// from display text. Endpoint fallbacks must be driven by this structured
// information instead of by translated or reformatted error strings.
type aliOpenRequestError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *aliOpenRequestError) Error() string {
	if e == nil {
		return "aliopen: request failed"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("aliopen: http %d", e.StatusCode)
	}
	return "aliopen: request failed"
}

func aliOpenErrorBodyOf(data []byte) aliOpenErrorBody {
	var body aliOpenErrorBody
	_ = json.Unmarshal(data, &body)
	return body
}

func aliOpenRequestErrorOf(data []byte, status int) *aliOpenRequestError {
	body := aliOpenErrorBodyOf(data)
	return &aliOpenRequestError{
		StatusCode: status,
		Code:       strings.TrimSpace(body.Code),
		Message:    aliOpenErrorMessage(data, status),
	}
}

func aliOpenAuthResponse(status int, data []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	body := aliOpenErrorBodyOf(data)
	code := strings.ToLower(strings.TrimSpace(body.Code))
	return strings.Contains(code, "accesstoken") ||
		strings.Contains(code, "tokenexpired") ||
		strings.Contains(code, "invalid_token") ||
		code == "unauthorized"
}

func aliOpenRateLimitResponse(status int, data []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	body := aliOpenErrorBodyOf(data)
	text := strings.ToLower(body.Code + " " + body.Message + " " + body.Error)
	return strings.Contains(text, "limit") ||
		strings.Contains(text, "toomany") ||
		strings.Contains(text, "frequency") ||
		strings.Contains(text, "429")
}

func aliOpenAPIError(data []byte) bool {
	body := aliOpenErrorBodyOf(data)
	return strings.TrimSpace(body.Code) != "" && !strings.EqualFold(strings.TrimSpace(body.Code), "success")
}

func aliOpenErrorMessage(data []byte, status int) string {
	body := aliOpenErrorBodyOf(data)
	for _, msg := range []string{body.Message, body.ErrorDescription, body.Error, body.Code} {
		if strings.TrimSpace(msg) != "" {
			return strings.TrimSpace(msg)
		}
	}
	if status > 0 {
		return fmt.Sprintf("aliopen: http %d", status)
	}
	return "aliopen: request failed"
}

func aliOpenRetryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	if resp != nil {
		if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
			if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return fallback
}

// listResp is the paginated listing response.
type listResp struct {
	Items  []aliFile `json:"items"`
	Marker string    `json:"next_marker"`
}

// ListPage returns one page of a directory.
func (c *client) ListPage(ctx context.Context, scope Scope, parentID, marker string) ([]aliFile, string, error) {
	body := map[string]any{
		"parent_file_id": parentID,
		"drive_id":       c.scopedDriveID(scope),
		"limit":          200,
	}
	if marker != "" {
		body["marker"] = marker
	}
	// Also include images/playlists
	body["fields"] = "*"
	var resp listResp
	if err := c.apiPost(ctx, "/adrive/v1.0/openFile/list", body, &resp); err != nil {
		return nil, "", err
	}
	return resp.Items, resp.Marker, nil
}

// List fetches all pages.
func (c *client) List(ctx context.Context, scope Scope, parentID string) ([]aliFile, error) {
	var out []aliFile
	marker := ""
	seenMarkers := map[string]bool{}
	for {
		items, next, err := c.ListPage(ctx, scope, parentID, marker)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			break
		}
		if seenMarkers[next] {
			return nil, errors.New("aliopen: duplicate list cursor")
		}
		seenMarkers[next] = true
		marker = next
	}
	return out, nil
}

// Detail returns one file detail.
func (c *client) Detail(ctx context.Context, scope Scope, fileID string) (*aliFile, error) {
	var file aliFile
	if err := c.apiPost(ctx, "/adrive/v1.0/openFile/get", map[string]any{
		"file_id":  fileID,
		"drive_id": c.scopedDriveID(scope),
	}, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

// Search searches across a drive.
func (c *client) Search(ctx context.Context, scope Scope, keyword string) ([]aliFile, error) {
	var resp struct {
		Items  []aliFile `json:"items"`
		Marker string    `json:"next_marker"`
	}
	if err := c.apiPost(ctx, "/adrive/v1.0/openFile/search", map[string]any{
		"drive_id":                c.scopedDriveID(scope),
		"query":                   "name match \"" + keyword + "\"",
		"limit":                   200,
		"image_thumbnail_process": "image/resize,w_256/format,jpeg",
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// DownloadInfo gets the download URL and headers.
func (c *client) DownloadInfo(ctx context.Context, scope Scope, fileID string, expireSec int) (string, int64, map[string]string, int64, error) {
	var resp aliOpenDownloadResponse
	if expireSec <= 0 {
		expireSec = defaultDownloadExpireSec
	}
	if err := c.apiPost(ctx, "/adrive/v1.0/openFile/getDownloadUrl", map[string]any{
		"file_id":    fileID,
		"drive_id":   c.scopedDriveID(scope),
		"expire_sec": expireSec,
	}, &resp); err != nil {
		return "", 0, nil, 0, err
	}
	// Live Photo files can expose only streamsUrl/streams_url. Prefer the
	// playable MOV stream, then JPEG, and keep the normal URL as fallback.
	url := strings.TrimSpace(resp.URL)
	for _, streams := range []map[string]any{resp.StreamsURL, resp.StreamsURLSnake} {
		for _, key := range []string{"mov", "jpeg"} {
			if stream := aliOpenStreamURL(streams, key); stream != "" {
				url = stream
				break
			}
		}
		if url != strings.TrimSpace(resp.URL) {
			break
		}
	}
	if url == "" {
		return "", 0, nil, 0, errors.New("aliopen: 获取下载地址失败")
	}
	expireTime := parseAliOpenExpiration(resp.Expiration)
	if expireTime == 0 {
		expireTime = driveutil.GetExpiresTime(url)
	}
	return url, resp.Size, resp.Headers, expireTime, nil
}

func aliOpenStreamURL(streams map[string]any, key string) string {
	if len(streams) == 0 {
		return ""
	}
	value, ok := streams[key]
	if !ok {
		return ""
	}
	if raw, ok := value.(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

// Mkdir creates a folder.
func (c *client) Mkdir(ctx context.Context, scope Scope, parentID, name string) (*drive.MkdirResult, error) {
	var res struct {
		FileID string `json:"file_id"`
	}
	if err := c.apiPost(ctx, "/adrive/v1.0/openFile/create", map[string]any{
		"name":            name,
		"parent_file_id":  parentID,
		"drive_id":        c.scopedDriveID(scope),
		"type":            "folder",
		"check_name_mode": "refuse",
	}, &res); err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	return &drive.MkdirResult{FileID: res.FileID}, nil
}

// Rename patches the name.
func (c *client) Rename(ctx context.Context, scope Scope, fileID, name string) error {
	return c.apiPost(ctx, "/adrive/v1.0/openFile/update", map[string]any{
		"file_id":  fileID,
		"drive_id": c.scopedDriveID(scope),
		"name":     name,
	}, nil)
}

// Trash moves files to recycle bin.
func (c *client) Trash(ctx context.Context, scope Scope, fileIDs ...string) error {
	for _, id := range fileIDs {
		if err := c.apiPost(ctx, "/adrive/v1.0/openFile/recyclebin/trash", map[string]any{
			"file_id":  id,
			"drive_id": c.scopedDriveID(scope),
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *client) RecycleList(ctx context.Context, scope Scope) ([]aliFile, error) {
	var result []aliFile
	marker := ""
	seen := make(map[string]bool)
	for {
		body := map[string]any{"drive_id": c.scopedDriveID(scope), "limit": 200, "fields": "*"}
		if marker != "" {
			body["marker"] = marker
		}
		var page listResp
		err := c.apiPost(ctx, "/adrive/v1.0/openFile/recyclebin/list", body, &page)
		if aliOpenNotFound(err) {
			err = c.apiPostAt(ctx, nativeAPIHost, "/v2/recyclebin/list", body, &page)
		}
		if err != nil {
			return nil, err
		}
		if page.Items == nil {
			return nil, errors.New("阿里云盘回收站响应缺少 items")
		}
		result = append(result, page.Items...)
		if page.Marker == "" {
			return result, nil
		}
		if seen[page.Marker] {
			return nil, errors.New("阿里云盘回收站分页游标重复")
		}
		seen[page.Marker] = true
		marker = page.Marker
	}
}

func (c *client) RecycleRestore(ctx context.Context, scope Scope, fileID string) error {
	body := map[string]any{"drive_id": c.scopedDriveID(scope), "file_id": fileID}
	err := c.apiPost(ctx, "/adrive/v1.0/openFile/recyclebin/restore", body, nil)
	if aliOpenNotFound(err) {
		err = c.apiPostAt(ctx, nativeAPIHost, "/v2/recyclebin/restore", body, nil)
	}
	return err
}

// Delete permanently deletes.
func (c *client) Delete(ctx context.Context, scope Scope, fileIDs ...string) error {
	for _, id := range fileIDs {
		if err := c.apiPost(ctx, "/adrive/v1.0/openFile/delete", map[string]any{
			"file_id":  id,
			"drive_id": c.scopedDriveID(scope),
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

// Move moves files.
func (c *client) Move(ctx context.Context, scope Scope, fileID, toParentID string) error {
	return c.apiPost(ctx, "/adrive/v1.0/openFile/move", map[string]any{
		"file_id":           fileID,
		"drive_id":          c.scopedDriveID(scope),
		"to_parent_file_id": toParentID,
	}, nil)
}

// Copy copies files.
func (c *client) Copy(ctx context.Context, scope Scope, fileID, toParentID, toDriveID string) error {
	return c.apiPost(ctx, "/adrive/v1.0/openFile/copy", map[string]any{
		"file_id":           fileID,
		"drive_id":          c.scopedDriveID(scope),
		"to_parent_file_id": toParentID,
		"to_drive_id":       toDriveID,
	}, nil)
}

// CreateShare creates a share link.
func (c *client) CreateShare(ctx context.Context, scope Scope, fileIDs []string, shareName, expiration, password string) (*model.ShareItem, error) {
	body := map[string]any{
		"drive_id":     c.scopedDriveID(scope),
		"file_id_list": fileIDs,
		"share_name":   shareName,
		"share_pwd":    password,
		"expiration":   expiration,
		"description":  shareName,
	}
	var share struct {
		ShareID    string `json:"share_id"`
		ShareURL   string `json:"share_url"`
		ShareMsg   string `json:"share_msg"`
		Expiration string `json:"expiration"`
		Status     string `json:"status"`
		DriveID    string `json:"drive_id"`
	}
	err := c.apiPost(ctx, "/adrive/v1.0/openFile/createShareLink", body, &share)
	if err != nil && aliOpenNotFound(err) {
		// Some personal-drive regions no longer expose the legacy Open endpoint
		// but retain the compatible consumer route. Only fall back on a definite
		// not-found result: retrying another endpoint after any other failure
		// could create duplicate links.
		err = c.apiPostAt(ctx, nativeAPIHost, "/adrive/v2/share_link/create", body, &share)
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(share.ShareURL) == "" && strings.TrimSpace(share.ShareID) != "" {
		share.ShareURL = "https://www.alipan.com/s/" + share.ShareID
	}
	return &model.ShareItem{
		ShareID: share.ShareID, ShareURL: share.ShareURL, ShareMsg: share.ShareMsg,
		Expiration: share.Expiration, Status: share.Status, DriveID: share.DriveID,
		ShareName: shareName, SharePwd: password,
	}, nil
}

// CancelShare revokes a created share remotely. The legacy Open API is kept
// first for accounts where creation still uses it; regions that retired that
// route use the compatible consumer API instead.
func (c *client) CancelShare(ctx context.Context, shareID string) error {
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return errors.New("aliopen: 取消分享缺少分享标识")
	}
	body := map[string]any{"share_id": shareID}
	err := c.apiPost(ctx, "/adrive/v1.0/openFile/cancelShareLink", body, nil)
	if err != nil && aliOpenNotFound(err) {
		err = c.apiPostAt(ctx, nativeAPIHost, "/adrive/v2/share_link/cancel", body, nil)
	}
	return err
}

func aliOpenNotFound(err error) bool {
	var requestErr *aliOpenRequestError
	return errors.As(err, &requestErr) && requestErr.StatusCode == http.StatusNotFound
}

type aliOpenUploadPart struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url"`
}

type aliOpenUploadCreateOptions struct {
	PartSize        int64
	ContentHash     string
	PreHash         string
	ProofCode       string
	CheckNameMode   string
	LocalModifiedAt string
	LocalCreatedAt  string
}

type aliOpenUploadCreateResult struct {
	FileID       string              `json:"file_id"`
	UploadID     string              `json:"upload_id"`
	PartInfoList []aliOpenUploadPart `json:"part_info_list"`
	RapidUpload  bool                `json:"rapid_upload"`
	Exist        bool                `json:"exist"`
}

func aliOpenPartSize(size int64) int64 {
	switch {
	case size > 1024*aliOpenGiB:
		return 5 * aliOpenGiB
	case size > 768*aliOpenGiB:
		return 109951163
	case size > 512*aliOpenGiB:
		return 82463373
	case size > 384*aliOpenGiB:
		return 54975582
	case size > 256*aliOpenGiB:
		return 41231687
	case size > 128*aliOpenGiB:
		return 27487791
	default:
		return 20 * aliOpenMiB
	}
}

func aliOpenPartCount(size, partSize int64) int {
	if size <= 0 || partSize <= 0 {
		return 1
	}
	return int((size-1)/partSize + 1)
}

func aliOpenCheckNameMode(policy string) string {
	switch driveutil.ResolveConflictPolicy(policy) {
	case driveutil.ConflictRefuse:
		return "fail"
	case driveutil.ConflictRename:
		return "auto_rename"
	case driveutil.ConflictSkip:
		// Skip is checked before create. Keep the API conservative if a
		// concurrent writer creates the same name between those two calls.
		return "fail"
	default:
		return "ignore"
	}
}

func aliOpenCheckNameModeForDuplicate(duplicate int) string {
	switch duplicate {
	case 2:
		return "ignore"
	case 1:
		return "fail"
	default:
		return "auto_rename"
	}
}

func (c *client) hasNamedFile(ctx context.Context, scope Scope, parentID, name string) (bool, error) {
	items, err := c.List(ctx, scope, parentID)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// RapidUpload attempts sha1-based rapid upload (秒传).
func (c *client) RapidUpload(ctx context.Context, scope Scope, parentID, name string, size int64, sha1Str string) (*drive.RapidUploadResult, error) {
	return c.rapidUploadWithMode(ctx, scope, parentID, name, size, sha1Str, "ignore")
}

func (c *client) rapidUploadWithMode(ctx context.Context, scope Scope, parentID, name string, size int64, sha1Str, checkNameMode string) (*drive.RapidUploadResult, error) {
	var res struct {
		FileID       string `json:"file_id"`
		RapidUpload  bool   `json:"rapid_upload"`
		PartInfoList []struct {
			PartNumber int    `json:"part_number"`
			UploadURL  string `json:"upload_url"`
		} `json:"part_info_list"`
		Exist bool   `json:"exist"`
		Error string `json:"error"`
	}
	if err := c.apiPost(ctx, "/adrive/v1.0/openFile/create", map[string]any{
		"name":              name,
		"parent_file_id":    parentID,
		"drive_id":          c.scopedDriveID(scope),
		"type":              "file",
		"size":              size,
		"content_hash":      sha1Str,
		"content_hash_name": "sha1",
		"check_name_mode":   checkNameMode,
	}, &res); err != nil {
		return nil, err
	}
	// A miss still returns file_id for the pending upload object. Only the
	// explicit rapid_upload flag means the remote file is already complete.
	if res.RapidUpload || res.Exist {
		if strings.TrimSpace(res.FileID) == "" {
			return &drive.RapidUploadResult{Reuse: false, Message: "秒传响应缺少 file_id"}, nil
		}
		return &drive.RapidUploadResult{Reuse: true, FileID: res.FileID}, nil
	}
	// Do not expose the pending file id: the migration layer treats a file id
	// as an accepted transfer for compatibility with providers that return one
	// on a successful probe.
	return &drive.RapidUploadResult{Reuse: false, Message: "秒传未命中，需要上传"}, nil
}

// CreateUploadFile creates an upload entry and returns parts.
func (c *client) CreateUploadFile(ctx context.Context, scope Scope, parentID, name string, size int64, options aliOpenUploadCreateOptions) (aliOpenUploadCreateResult, error) {
	partSize := options.PartSize
	if partSize <= 0 {
		partSize = aliOpenPartSize(size)
	}
	partCount := aliOpenPartCount(size, partSize)
	partInfoList := make([]map[string]int, partCount)
	for i := range partInfoList {
		partInfoList[i] = map[string]int{"part_number": i + 1}
	}
	mode := strings.TrimSpace(options.CheckNameMode)
	if mode == "" {
		mode = "ignore"
	}
	body := map[string]any{
		"name":            name,
		"parent_file_id":  parentID,
		"drive_id":        c.scopedDriveID(scope),
		"type":            "file",
		"size":            size,
		"check_name_mode": mode,
		"part_info_list":  partInfoList,
	}
	if hash := strings.TrimSpace(options.ContentHash); hash != "" {
		body["content_hash"] = strings.ToUpper(hash)
		body["content_hash_name"] = "sha1"
	}
	if preHash := strings.TrimSpace(options.PreHash); preHash != "" {
		body["pre_hash"] = strings.ToUpper(preHash)
	}
	if proof := strings.TrimSpace(options.ProofCode); proof != "" {
		body["proof_version"] = "v1"
		body["proof_code"] = proof
	}
	if value := strings.TrimSpace(options.LocalModifiedAt); value != "" {
		body["local_modified_at"] = value
	}
	if value := strings.TrimSpace(options.LocalCreatedAt); value != "" {
		body["local_created_at"] = value
	}
	var result aliOpenUploadCreateResult
	if err := c.apiPost(ctx, "/adrive/v1.0/openFile/create", body, &result); err != nil {
		return aliOpenUploadCreateResult{}, err
	}
	return result, nil
}

func (c *client) fillUploadURLs(ctx context.Context, scope Scope, fileID, uploadID string, parts []aliOpenUploadPart, uploaded map[int]bool) error {
	missing := make([]map[string]int, 0, len(parts))
	for _, part := range parts {
		if !uploaded[part.PartNumber] && strings.TrimSpace(part.UploadURL) == "" {
			missing = append(missing, map[string]int{"part_number": part.PartNumber})
		}
	}
	for start := 0; start < len(missing); start += 100 {
		end := start + 100
		if end > len(missing) {
			end = len(missing)
		}
		var refreshed struct {
			PartInfoList []aliOpenUploadPart `json:"part_info_list"`
		}
		if err := c.apiPost(ctx, "/adrive/v1.0/openFile/getUploadUrl", map[string]any{
			"drive_id":       c.scopedDriveID(scope),
			"file_id":        fileID,
			"upload_id":      uploadID,
			"part_info_list": missing[start:end],
		}, &refreshed); err != nil {
			return err
		}
		urls := make(map[int]string, len(refreshed.PartInfoList))
		for _, part := range refreshed.PartInfoList {
			urls[part.PartNumber] = strings.TrimSpace(part.UploadURL)
		}
		for i := range parts {
			if url := urls[parts[i].PartNumber]; url != "" {
				parts[i].UploadURL = url
			}
		}
	}
	for _, part := range parts {
		if !uploaded[part.PartNumber] && strings.TrimSpace(part.UploadURL) == "" {
			return fmt.Errorf("aliopen: 分片 %d 无上传地址", part.PartNumber)
		}
	}
	return nil
}

func aliOpenPreHash(f *os.File, size int64) (string, error) {
	if f == nil || size <= 0 {
		return "", nil
	}
	length := size
	if length > 1024 {
		length = 1024
	}
	buf := make([]byte, int(length))
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.ToUpper(netx.SHA1Hex(buf[:n])), nil
}

func aliOpenProofCode(f *os.File, accessToken string, size int64) (string, error) {
	if f == nil || strings.TrimSpace(accessToken) == "" || size <= 0 {
		return "", nil
	}
	digest := netx.MD5Hex([]byte(accessToken))
	position, err := strconv.ParseUint(digest[:16], 16, 64)
	if err != nil {
		return "", err
	}
	start := int64(position % uint64(size))
	length := int64(8)
	if remain := size - start; remain < length {
		length = remain
	}
	buf := make([]byte, int(length))
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), nil
}

func aliOpenLocalFileTime(info os.FileInfo) string {
	if info == nil || info.ModTime().IsZero() {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano)
}

func aliOpenIsPreHashMatched(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "prehashmatched") || strings.Contains(message, "pre_hash_matched")
}

func encodeAliOpenUploadSession(uploadID, fileID string) string {
	b, _ := json.Marshal(map[string]string{"upload_id": uploadID, "file_id": fileID})
	return string(b)
}

func decodeAliOpenUploadSession(raw string) (uploadID, fileID string) {
	var state struct {
		UploadID string `json:"upload_id"`
		FileID   string `json:"file_id"`
	}
	if json.Unmarshal([]byte(raw), &state) == nil {
		return strings.TrimSpace(state.UploadID), strings.TrimSpace(state.FileID)
	}
	// Older builds persisted only upload_id. It cannot be completed without
	// file_id, so the caller will discard that stale state and create anew.
	return strings.TrimSpace(raw), ""
}

// CompleteUpload marks the upload as complete.
func (c *client) CompleteUpload(ctx context.Context, scope Scope, fileID, uploadID string) error {
	return c.apiPost(ctx, "/adrive/v1.0/openFile/complete", map[string]any{
		"file_id":   fileID,
		"drive_id":  c.scopedDriveID(scope),
		"upload_id": uploadID,
	}, nil)
}

// ResolveHash extracts the sha1 from a file's metadata.
func (c *client) ResolveHash(ctx context.Context, scope Scope, fileID string) string {
	file, err := c.Detail(ctx, scope, fileID)
	if err != nil {
		return ""
	}
	if file.ContentHashName == "sha1" && file.ContentHash != "" {
		return file.ContentHash
	}
	return ""
}

// GetSpaceInfo returns quota.
func (c *client) GetSpaceInfo(ctx context.Context) (used, total int64) {
	var raw json.RawMessage
	if err := c.apiPost(ctx, "/adrive/v1.0/user/getSpaceInfo", map[string]any{}, &raw); err != nil {
		return 0, 0
	}
	return parseAliOpenSpaceInfo(raw)
}

func parseAliOpenSpaceInfo(raw json.RawMessage) (used, total int64) {
	var response struct {
		PersonalSpaceInfo map[string]json.RawMessage `json:"personal_space_info"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return 0, 0
	}
	used, usedOK := aliOpenInt64(response.PersonalSpaceInfo, "used_size", "usedSize", "used")
	total, totalOK := aliOpenInt64(response.PersonalSpaceInfo, "total_size", "totalSize", "total")
	if !usedOK || !totalOK || total <= 0 {
		return 0, 0
	}
	if used < 0 {
		used = 0
	}
	if used > total {
		used = total
	}
	return used, total
}

func aliOpenInt64(values map[string]json.RawMessage, keys ...string) (int64, bool) {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok || string(raw) == "null" {
			continue
		}
		var value aliOpenFlexString
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(value.String()), 10, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// mapFile converts an API file to the unified model.
func mapFile(item *aliFile, driveID, parentID, scopePrefix string) model.File {
	isDir := item.Type == "folder"
	timeUnix := int64(0)
	if parsed, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
		timeUnix = parsed.Unix()
	}
	if timeUnix == 0 {
		if parsed, err := time.Parse(time.RFC3339, item.CreatedAt); err == nil {
			timeUnix = parsed.Unix()
		}
	}
	f := driveutil.NewFile(driveID, wrapRef(Scope(scopePrefix), item.FileID), wrapRef(Scope(scopePrefix), parentID), item.Name, isDir, item.Size, timeUnix)
	f.Thumbnail = item.Thumbnail
	f.ContentHash = item.ContentHash
	f.ContentHashName = item.ContentHashName
	f.Category = item.Category
	f.Description = item.Status
	f.Starred = item.Starred
	return f
}

// ---- Driver ----

// Driver implements drive.Driver for Aliyun Drive Open.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return RootID }

func (d *Driver) SupportsRemoteFavorites(context.Context, drive.Context) (bool, error) {
	return true, nil
}

func (d *Driver) ListFavorites(ctx context.Context, c drive.Context) ([]model.File, error) {
	cl, err := clientOfContext(ctx, c)
	if err != nil {
		return nil, err
	}
	scopes := []Scope{ScopeBackup}
	if cl.session.ResourceDriveID != "" && cl.session.ResourceDriveID != cl.scopedDriveID(ScopeBackup) {
		scopes = append(scopes, ScopeResource)
	}
	out := []model.File{}
	for _, scope := range scopes {
		marker := ""
		seen, seenIDs := map[string]bool{}, map[string]bool{}
		for {
			var page listResp
			if err := cl.apiPost(ctx, "/adrive/v1.0/openFile/starredList", map[string]any{
				"drive_id": cl.scopedDriveID(scope), "limit": 100, "marker": marker,
			}, &page); err != nil {
				return nil, err
			}
			if page.Items == nil {
				return nil, errors.New("阿里云盘收藏列表响应缺少 items")
			}
			for _, item := range page.Items {
				if item.FileID == "" || item.Name == "" || seenIDs[item.FileID] {
					return nil, errors.New("阿里云盘收藏列表包含空白或重复文件")
				}
				seenIDs[item.FileID] = true
				item.Starred = true
				out = append(out, mapFile(&item, c.DriveID, item.ParentFileID, string(scope)))
			}
			if page.Marker == "" {
				break
			}
			if seen[page.Marker] {
				return nil, errors.New("阿里云盘收藏列表分页游标重复")
			}
			seen[page.Marker] = true
			marker = page.Marker
		}
	}
	return out, nil
}

func (d *Driver) Favorite(ctx context.Context, c drive.Context, fileIDs []string, favorite bool) ([]string, error) {
	cl, err := clientOfContext(ctx, c)
	if err != nil {
		return nil, err
	}
	done := []string{}
	for _, id := range fileIDs {
		ref := parseRef(id)
		if strings.TrimSpace(ref.FID) == "" || ref.FID == "root" || id == RootID {
			return done, errors.New("阿里云盘不能收藏虚拟空间根目录")
		}
		if ref.Scope == ScopeResource && cl.session.ResourceDriveID == "" {
			return done, errors.New("阿里云盘未提供资源库空间")
		}
		var result aliFile
		if err := cl.apiPost(ctx, "/adrive/v1.0/openFile/update", map[string]any{
			"drive_id": cl.scopedDriveID(ref.Scope), "file_id": ref.FID, "starred": favorite,
		}, &result); err != nil {
			return done, err
		}
		if result.FileID != ref.FID {
			return done, errors.New("阿里云盘未确认文件收藏结果")
		}
		done = append(done, id)
	}
	return done, nil
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ref := parseRef(dirID)
	// Root: show virtual drive dirs
	if dirID == RootID || dirID == "root" {
		dirs := []model.File{virtualFile(c.DriveID, ScopeBackup)}
		// check if resource drive exists
		hasResource := cl.session.ResourceDriveID != ""
		if hasResource {
			dirs = append(dirs, virtualFile(c.DriveID, ScopeResource))
		}
		return dirs, nil
	}
	items, err := cl.List(ctx, ref.Scope, ref.FID)
	if err != nil {
		return nil, err
	}
	return mapFiles(items, c.DriveID, dirID, ref.Scope), nil
}

func (d *Driver) ListPaged(ctx context.Context, c drive.Context, dirID, marker string, _ *drive.ListOptions) (*drive.DirPage, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ref := parseRef(dirID)
	if dirID == RootID || dirID == "root" {
		dirs := []model.File{virtualFile(c.DriveID, ScopeBackup)}
		if cl.session.ResourceDriveID != "" {
			dirs = append(dirs, virtualFile(c.DriveID, ScopeResource))
		}
		return &drive.DirPage{Items: dirs}, nil
	}
	items, next, err := cl.ListPage(ctx, ref.Scope, ref.FID, marker)
	if err != nil {
		return nil, err
	}
	return &drive.DirPage{Items: mapFiles(items, c.DriveID, dirID, ref.Scope), NextMarker: next}, nil
}

func (d *Driver) Search(ctx context.Context, c drive.Context, keyword string) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	scopes := []Scope{ScopeBackup}
	if cl.session.ResourceDriveID != "" {
		scopes = append(scopes, ScopeResource)
	}
	var out []model.File
	var searchErrs []error
	for _, sc := range scopes {
		items, err := cl.Search(ctx, sc, keyword)
		if err != nil {
			searchErrs = append(searchErrs, fmt.Errorf("%s: %w", sc, err))
			continue
		}
		for i := range items {
			out = append(out, mapFile(&items[i], c.DriveID, wrapRef(sc, items[i].ParentFileID), string(sc)))
		}
	}
	if len(out) == 0 && len(searchErrs) > 0 {
		return nil, errors.Join(searchErrs...)
	}
	return out, nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	ref := parseRef(fileID)
	if fileID == RootID || fileID == "root" {
		return model.File{DriveID: c.DriveID, FileID: RootID, Name: "阿里云盘", NameSearch: "aliyun", IsDir: true, Icon: "iconfile-folder"}, nil
	}
	if fileID == BackupRoot || fileID == ResourceRoot {
		return virtualFile(c.DriveID, ref.Scope), nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	item, err := cl.Detail(ctx, ref.Scope, ref.FID)
	if err != nil {
		return nil, err
	}
	return mapFile(item, c.DriveID, wrapRef(ref.Scope, item.ParentFileID), string(ref.Scope)), nil
}

func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	v, err := d.GetInfo(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	f, ok := v.(model.File)
	if !ok {
		return nil, drive.ErrNotFound
	}
	return &f, nil
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, expireSec int) (*model.DownloadURL, error) {
	ref := parseRef(fileID)
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	url, size, headers, expireTime, err := cl.DownloadInfo(ctx, ref.Scope, ref.FID, expireSec)
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID: c.DriveID, FileID: fileID, URL: url, Size: size,
		ExpireTime: expireTime,
		Headers:    headers, DownloadMode: "redirect",
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID: c.DriveID, FileID: fileID, Size: u.Size, Headers: u.Headers,
		Qualities: []model.VideoQuality{{
			Quality: "origin", Label: "原画", Value: "origin", URL: u.URL, Headers: u.Headers, ForceProxy: true,
		}},
	}, nil
}

func parseAliOpenExpiration(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if value > 0 && value < 10_000_000_000 {
			return value * 1000
		}
		if value >= 10_000_000_000 {
			return value
		}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	ref := parseRef(parentID)
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return cl.Mkdir(ctx, ref.Scope, ref.FID, name)
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	ref := parseRef(fileID)
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	if err := cl.Rename(ctx, ref.Scope, ref.FID, name); err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: fileID, Name: name}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	scope, err := sameAliOpenScopeIDs(fileIDs...)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, id := range fileIDs {
		ref := parseRef(id)
		if err := cl.Trash(ctx, scope, ref.FID); err == nil {
			ok = append(ok, wrapRef(scope, ref.FID))
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", wrapRef(scope, ref.FID), err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	scope, err := sameAliOpenRefScope(refs, "")
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, r := range refs {
		ref := parseRef(r.ID)
		if err := cl.Delete(ctx, scope, ref.FID); err == nil {
			ok = append(ok, wrapRef(scope, ref.FID))
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", wrapRef(scope, ref.FID), err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	cl, err := clientOfContext(ctx, c)
	if err != nil {
		return nil, err
	}
	var completed []string
	var failed []error
	for _, id := range fileIDs {
		ref := parseRef(id)
		if ref.FID == "" || ref.FID == "root" {
			failed = append(failed, fmt.Errorf("%s: 回收站文件 ID 无效", id))
			continue
		}
		if err := cl.RecycleRestore(ctx, ref.Scope, ref.FID); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", id, err))
		} else {
			completed = append(completed, id)
		}
	}
	return completed, errors.Join(failed...)
}

func (d *Driver) ListTrash(ctx context.Context, c drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOfContext(ctx, c)
	if err != nil {
		return nil, err
	}
	var result []model.File
	var scopes = []Scope{ScopeBackup}
	if cl.session.ResourceDriveID != "" && cl.session.ResourceDriveID != cl.scopedDriveID(ScopeBackup) {
		scopes = append(scopes, ScopeResource)
	}
	for _, scope := range scopes {
		items, err := cl.RecycleList(ctx, scope)
		if err != nil {
			return nil, err
		}
		for index := range items {
			if items[index].FileID == "" || items[index].Name == "" {
				return nil, errors.New("阿里云盘回收站包含无效文件")
			}
			result = append(result, mapFile(&items[index], c.DriveID, items[index].ParentFileID, string(scope)))
		}
	}
	return result, nil
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	target := parseRef(toParentID)
	scope, err := sameAliOpenRefScope(refs, toParentID)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, r := range refs {
		ref := parseRef(r.ID)
		if err := cl.Move(ctx, scope, ref.FID, target.FID); err == nil {
			ok = append(ok, r.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", r.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	target := parseRef(toParentID)
	scope, err := sameAliOpenRefScope(refs, toParentID)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, r := range refs {
		ref := parseRef(r.ID)
		toDrive := cl.scopedDriveID(scope)
		if err := cl.Copy(ctx, scope, ref.FID, target.FID, toDrive); err == nil {
			ok = append(ok, r.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", r.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) CreateShare(ctx context.Context, c drive.Context, params drive.ShareParams) (*model.ShareItem, error) {
	if len(params.FileIDs) == 0 {
		return nil, errors.New("aliopen: 创建分享至少选择一个文件")
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	scope, err := sameAliOpenScopeIDs(params.FileIDs...)
	if err != nil {
		return nil, err
	}
	fids := make([]string, 0, len(params.FileIDs))
	for _, id := range params.FileIDs {
		ref := parseRef(id)
		fids = append(fids, ref.FID)
	}
	item, err := cl.CreateShare(ctx, scope, fids, params.ShareName, params.Expiration, params.Password)
	if err != nil {
		return nil, err
	}
	if item == nil || (item.ShareID == "" && item.ShareURL == "" && item.ShareMsg == "" && item.FullShareMsg == "") {
		return nil, errors.New("aliopen: 创建分享未返回链接")
	}
	item.AccountID = c.UserID
	item.DriveID = c.DriveID
	item.FileID = params.FileIDs[0]
	item.FileIDList = append([]string(nil), params.FileIDs...)
	return item, nil
}

func (d *Driver) CancelShare(ctx context.Context, c drive.Context, share model.ShareHistoryEntry) error {
	shareID := strings.TrimSpace(share.ShareID)
	if shareID == "" {
		shareID, _ = parseAliShareURL(share.ShareURL)
	}
	if shareID == "" {
		return errors.New("aliopen: 取消分享缺少分享标识")
	}
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	return cl.CancelShare(ctx, shareID)
}

func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || strings.TrimSpace(ui.Info.LocalFilePath) == "" {
		return errors.New("aliopen: 上传文件路径为空")
	}
	ref := parseRef(ui.Info.ParentFileID)
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, _ := f.Stat()
	if info == nil {
		return errors.New("aliopen: stat file failed")
	}
	size := info.Size()
	ui.Info.Size = size
	policy := driveutil.ResolveConflictPolicy(ui.Info.ConflictPolicy)
	if policy == driveutil.ConflictSkip {
		exists, err := cl.hasNamedFile(ctx, ref.Scope, ref.FID, ui.Info.Name)
		if err != nil {
			return err
		}
		if exists {
			ui.ReportUploadProgress(size, size)
			return nil
		}
	}

	contentHash := strings.ToUpper(strings.TrimSpace(ui.Info.SHA1))
	if contentHash == "" {
		contentHash, err = netx.HashFileWithProgress(ui.Info.LocalFilePath, netx.HashSHA1, func(read int64) {
			if ui != nil {
				ui.ReportUploadProgress(read, ui.Info.Size)
			}
		}, 0)
		if err != nil {
			return fmt.Errorf("aliopen: 计算文件 SHA1 失败: %w", err)
		}
		contentHash = strings.ToUpper(contentHash)
		ui.Info.SHA1 = contentHash
	}
	partSize := aliOpenPartSize(size)
	partCount := aliOpenPartCount(size, partSize)
	// Legacy name/size-only sessions cannot prove that their parts belong to
	// this content or conflict policy. Leave them unused rather than mixing data.
	sessionName := ui.Info.Name + "\x00" + aliOpenCheckNameMode(ui.Info.ConflictPolicy) + "\x00" + contentHash
	sessionKey := drive.UploadSessionKey(c.UserID, c.DriveID, ui.Info.ParentFileID, sessionName, size)
	savedSessionID, savedParts := drive.LoadUploadSessionState(sessionKey)
	uploadedSet := make(map[int]bool)
	uploadID, fileID := decodeAliOpenUploadSession(savedSessionID)
	if fileID == "" || uploadID == "" {
		fileID, uploadID = "", ""
	} else {
		for _, pn := range savedParts {
			if pn >= 1 && pn <= partCount {
				uploadedSet[pn] = true
			}
		}
	}

	parts := make([]aliOpenUploadPart, partCount)
	for i := range parts {
		parts[i].PartNumber = i + 1
	}
	if fileID == "" || uploadID == "" {
		preHash, preHashErr := aliOpenPreHash(f, size)
		if preHashErr != nil {
			return fmt.Errorf("aliopen: 计算 pre_hash 失败: %w", preHashErr)
		}
		localTime := aliOpenLocalFileTime(info)
		options := aliOpenUploadCreateOptions{
			PartSize:        partSize,
			ContentHash:     contentHash,
			PreHash:         preHash,
			CheckNameMode:   aliOpenCheckNameMode(ui.Info.ConflictPolicy),
			LocalModifiedAt: localTime,
			LocalCreatedAt:  localTime,
		}
		created, createErr := cl.CreateUploadFile(ctx, ref.Scope, ref.FID, ui.Info.Name, size, options)
		if createErr != nil && !aliOpenIsPreHashMatched(createErr) {
			return createErr
		}
		if createErr != nil || ((!created.RapidUpload && !created.Exist) && (created.FileID == "" || created.UploadID == "")) {
			proofCode, proofErr := aliOpenProofCode(f, cl.session.AccessToken, size)
			if proofErr != nil {
				return fmt.Errorf("aliopen: 计算 proof_code 失败: %w", proofErr)
			}
			options.PreHash = ""
			options.ProofCode = proofCode
			created, createErr = cl.CreateUploadFile(ctx, ref.Scope, ref.FID, ui.Info.Name, size, options)
			if createErr != nil {
				return createErr
			}
		}
		if created.RapidUpload || created.Exist {
			if strings.TrimSpace(created.FileID) == "" {
				return errors.New("aliopen: 秒传响应缺少 file_id")
			}
			ui.Upload.FileID = created.FileID
			ui.ReportUploadProgress(size, size)
			return nil
		}
		fileID, uploadID = strings.TrimSpace(created.FileID), strings.TrimSpace(created.UploadID)
		if fileID == "" || uploadID == "" {
			return errors.New("aliopen: 创建上传任务未返回 file_id 或 upload_id")
		}
		parts = created.PartInfoList
	}

	// The persisted record stores only the remote session id and completed
	// parts. Rebuild part metadata on resume because pre-signed URLs expire.
	byNumber := make(map[int]aliOpenUploadPart, len(parts))
	for _, part := range parts {
		if part.PartNumber >= 1 && part.PartNumber <= partCount {
			byNumber[part.PartNumber] = part
		}
	}
	parts = make([]aliOpenUploadPart, partCount)
	for i := range parts {
		parts[i].PartNumber = i + 1
		if part, ok := byNumber[i+1]; ok {
			parts[i].UploadURL = part.UploadURL
		}
	}
	// Persist the remote session before requesting pre-signed URLs so a
	// transient URL request failure can still resume the same upload task.
	_ = drive.SaveUploadSessionState(sessionKey, encodeAliOpenUploadSession(uploadID, fileID), drive.SortedUniqueParts(uploadedSet))
	if err := cl.fillUploadURLs(ctx, ref.Scope, fileID, uploadID, parts, uploadedSet); err != nil {
		return err
	}
	ui.Upload.FileID = fileID

	var uploadedSize int64
	for partNumber := range uploadedSet {
		start := int64(partNumber-1) * partSize
		if start >= size {
			continue
		}
		length := partSize
		if remain := size - start; remain < length {
			length = remain
		}
		uploadedSize += length
	}
	ui.ReportUploadProgress(uploadedSize, size)

	lastURLRefresh := time.Now()
	for i := range parts {
		part := &parts[i]
		start := int64(part.PartNumber-1) * partSize
		length := int64(0)
		if start < size {
			length = partSize
			if remain := size - start; remain < length {
				length = remain
			}
		}
		if !uploadedSet[part.PartNumber] && part.UploadURL != "" {
			if time.Since(lastURLRefresh) >= 50*time.Minute {
				var refreshed struct {
					PartInfoList []aliOpenUploadPart `json:"part_info_list"`
				}
				if refreshErr := cl.apiPost(ctx, "/adrive/v1.0/openFile/getUploadUrl", map[string]any{
					"drive_id":       cl.scopedDriveID(ref.Scope),
					"file_id":        fileID,
					"upload_id":      uploadID,
					"part_info_list": []map[string]int{{"part_number": part.PartNumber}},
				}, &refreshed); refreshErr != nil || len(refreshed.PartInfoList) == 0 || refreshed.PartInfoList[0].UploadURL == "" {
					if refreshErr != nil {
						return refreshErr
					}
					return fmt.Errorf("aliopen: 分片 %d 刷新上传地址失败", part.PartNumber)
				}
				part.UploadURL = strings.TrimSpace(refreshed.PartInfoList[0].UploadURL)
				lastURLRefresh = time.Now()
			}
			putPart := func(uploadURL string) (int, error) {
				body := io.NewSectionReader(f, start, length)
				req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
				if err != nil {
					return 0, err
				}
				req.ContentLength = length
				req.Header.Set("Content-Length", strconv.FormatInt(length, 10))
				resp, err := netx.DoUpload(cl.http.HTTP, req)
				if err != nil {
					return 0, err
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				status := resp.StatusCode
				resp.Body.Close()
				return status, nil
			}
			status, err := putPart(part.UploadURL)
			if err != nil {
				return err
			}
			if (status == http.StatusUnauthorized || status == http.StatusForbidden) && status >= 400 {
				// A persisted session can contain expired pre-signed URLs. Refresh
				// only the current part so a recoverable expiry does not discard the
				// whole upload session.
				var refreshed struct {
					PartInfoList []aliOpenUploadPart `json:"part_info_list"`
				}
				if refreshErr := cl.apiPost(ctx, "/adrive/v1.0/openFile/getUploadUrl", map[string]any{
					"drive_id":       cl.scopedDriveID(ref.Scope),
					"file_id":        fileID,
					"upload_id":      uploadID,
					"part_info_list": []map[string]int{{"part_number": part.PartNumber}},
				}, &refreshed); refreshErr == nil && len(refreshed.PartInfoList) > 0 && refreshed.PartInfoList[0].UploadURL != "" {
					part.UploadURL = strings.TrimSpace(refreshed.PartInfoList[0].UploadURL)
					status, err = putPart(part.UploadURL)
					if err != nil {
						return err
					}
				}
			}
			if status < 200 || status >= 300 {
				return fmt.Errorf("aliopen: 分片上传失败 HTTP %d", status)
			}
		}
		// Persist uploaded part number incrementally
		if !uploadedSet[part.PartNumber] {
			uploadedSet[part.PartNumber] = true
			_ = drive.SaveUploadSessionState(sessionKey, encodeAliOpenUploadSession(uploadID, fileID), drive.SortedUniqueParts(uploadedSet))
		}
		uploadedSize += length
		if ui != nil {
			ui.ReportUploadProgress(uploadedSize, size)
		}
	}
	err = cl.CompleteUpload(ctx, ref.Scope, fileID, uploadID)
	if err == nil {
		drive.ClearUploadSession(sessionKey)
	}
	return err
}

func (d *Driver) RapidUploadByHash(ctx context.Context, c drive.Context, req drive.RapidUploadRequest) (*drive.RapidUploadResult, error) {
	if req.Method != "sha1" {
		return nil, errors.New("aliopen: only sha1 supported")
	}
	ref := parseRef(req.ParentID)
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	if req.Duplicate == 1 {
		exists, err := cl.hasNamedFile(ctx, ref.Scope, ref.FID, req.FileName)
		if err != nil {
			return nil, err
		}
		if exists {
			return &drive.RapidUploadResult{Reuse: true, Message: "目标文件已存在，已跳过"}, nil
		}
	}
	return cl.rapidUploadWithMode(ctx, ref.Scope, ref.FID, req.FileName, req.Size, req.Hash, aliOpenCheckNameModeForDuplicate(req.Duplicate))
}

func (d *Driver) ResolveTransferHash(ctx context.Context, c drive.Context, fileID, method string, _ bool) (string, error) {
	if method != "sha1" {
		return "", nil
	}
	ref := parseRef(fileID)
	cl, err := clientOf(c)
	if err != nil {
		return "", err
	}
	return cl.ResolveHash(ctx, ref.Scope, ref.FID), nil
}

// ---- Share Import (importShare capability) ----

// shareListResp is the aliopen listByShare response.
type shareListResp struct {
	Items      []aliFile `json:"items"`
	NextMarker string    `json:"next_marker"`
}

// ImportShareSession implements drive.ShareImportDriver.
func (d *Driver) ImportShareSession(ctx context.Context, c drive.Context, shareURL, password string) (*drive.ShareImportSession, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	shareID, _ := parseAliShareURL(shareURL)
	if shareID == "" {
		return nil, errors.New("aliopen: 无效的分享链接")
	}
	// Step 1: getShareToken
	var tokenResp struct {
		ShareToken string `json:"share_token"`
	}
	body := map[string]any{"share_id": shareID}
	if password != "" {
		body["share_pwd"] = password
	}
	if err := cl.apiPost(ctx, "/adrive/v1.0/openFile/getShareToken", body, &tokenResp); err != nil {
		return nil, err
	}
	if tokenResp.ShareToken == "" {
		return nil, errors.New("获取分享凭证失败（链接无效、已取消或提取码错误）")
	}
	// Step 2: listByShare
	hdrs := map[string]string{"x-share-token": tokenResp.ShareToken}
	files, err := aliShareListAll(ctx, cl, shareID, tokenResp.ShareToken, "root", hdrs)
	if err != nil {
		return nil, err
	}
	return &drive.ShareImportSession{
		Provider:   providerID,
		ShareURL:   shareURL,
		ShareID:    shareID,
		Password:   password,
		ShareToken: tokenResp.ShareToken,
		RootFileID: "root",
		Files:      files,
	}, nil
}

func aliShareListAll(ctx context.Context, cl *client, shareID, shareToken, parentFileID string, hdrs map[string]string) ([]drive.ShareImportFile, error) {
	var out []drive.ShareImportFile
	marker := ""
	seenMarkers := map[string]bool{}
	for {
		body := map[string]any{
			"share_id":        shareID,
			"parent_file_id":  parentFileID,
			"limit":           100,
			"order_by":        "name",
			"order_direction": "ASC",
		}
		if marker != "" {
			body["marker"] = marker
		}
		var resp shareListResp
		if err := cl.apiPostWith(ctx, "/adrive/v1.0/openFile/listByShare", body, &resp, hdrs); err != nil {
			return nil, err
		}
		for i := range resp.Items {
			item := resp.Items[i]
			out = append(out, drive.ShareImportFile{
				FileID: item.FileID,
				Name:   item.Name,
				Size:   item.Size,
				IsDir:  item.Type == "folder",
			})
		}
		if resp.NextMarker == "" {
			break
		}
		if seenMarkers[resp.NextMarker] {
			return nil, errors.New("aliopen: duplicate share list cursor")
		}
		seenMarkers[resp.NextMarker] = true
		marker = resp.NextMarker
	}
	return out, nil
}

// SaveShare implements drive.ShareImportDriver.
func (d *Driver) SaveShare(ctx context.Context, c drive.Context, session *drive.ShareImportSession, fileIDs []string, toParentID string) ([]string, error) {
	if session == nil || session.Provider != providerID || strings.TrimSpace(session.ShareID) == "" || strings.TrimSpace(session.ShareToken) == "" {
		return nil, errors.New("aliopen: 分享会话无效或已过期")
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	// Determine target drive + parent
	targetDrive := cl.session.ResourceDriveID
	if targetDrive == "" {
		targetDrive = cl.session.DriveID
	}
	if targetDrive == "" {
		return nil, errors.New("aliopen: 缺少目标 drive_id")
	}
	parent := toParentID
	if parent == "" || parent == "root" || parent == "/" || parent == RootID {
		parent = "root"
	}
	ref := parseRef(parent)
	parent = ref.FID
	if parent == "" {
		parent = "root"
	}
	// If the target scope's drive_id differs from targetDrive, use the target drive
	if ref.Scope == ScopeResource && cl.session.ResourceDriveID != "" {
		targetDrive = cl.session.ResourceDriveID
	}
	hdrs := map[string]string{"x-share-token": session.ShareToken}
	var saved []string
	var failed []error
	for _, fileID := range fileIDs {
		body := map[string]any{
			"share_id":          session.ShareID,
			"file_id":           fileID,
			"to_drive_id":       targetDrive,
			"to_parent_file_id": parent,
			"auto_rename":       true,
		}
		if err := cl.apiPostWith(ctx, "/adrive/v1.0/openFile/copy", body, nil, hdrs); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", fileID, err))
			continue
		}
		saved = append(saved, fileID)
	}
	return saved, errors.Join(failed...)
}

// parseAliShareURL extracts share_id from an Aliyun share URL.
func parseAliShareURL(raw string) (shareID string, pwd string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "//") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	// https://www.alipan.com/s/{shareID}
	// https://www.aliyundrive.com/s/{shareID}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "s" {
		shareID = parts[1]
	}
	if shareID == "" && len(parts) > 0 {
		last := parts[len(parts)-1]
		if len(last) > 8 {
			shareID = last
		}
	}
	pwd = u.Query().Get("p")
	if pwd == "" {
		pwd = u.Query().Get("pwd")
	}
	return shareID, pwd
}

func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	sess := parseSession(token)
	if sess == nil {
		return nil, errors.New("aliopen: 会话不存在，请重新登录")
	}
	cl := &client{http: netx.NewClient(60 * time.Second), session: sess, token: token}
	if err := cl.refresh(ctx, c.UserID); err != nil {
		return nil, err
	}
	used, total := cl.GetSpaceInfo(ctx)
	// Avoid a separate profile request during a routine quota refresh. Account
	// identity comes from the refresh-token response, while the refresh path
	// stays low-frequency and predictable for provider risk control.
	cl.persistSession()
	applyAliOpenProfile(token, sess)
	applyAliOpenQuota(token, used, total)
	return token, nil
}

// helpers

func virtualFile(driveID string, scope Scope) model.File {
	if scope == ScopeResource {
		return model.File{DriveID: driveID, FileID: ResourceRoot, ParentFileID: RootID, Name: "资源盘", NameSearch: "资源盘 ziyuanpan", IsDir: true, Icon: "iconfile-folder", Description: "保存分享的文件在这里"}
	}
	return model.File{DriveID: driveID, FileID: BackupRoot, ParentFileID: RootID, Name: "备份盘", NameSearch: "备份盘 beifenpan", IsDir: true, Icon: "iconfile-folder", Description: "备份盘"}
}

func mapFiles(items []aliFile, driveID, parentID string, scope Scope) []model.File {
	out := make([]model.File, 0, len(items))
	for i := range items {
		out = append(out, mapFile(&items[i], driveID, parentID, string(scope)))
	}
	return out
}

func sameAliOpenScopeIDs(ids ...string) (Scope, error) {
	if len(ids) == 0 {
		return ScopeBackup, nil
	}
	scope := parseRef(ids[0]).Scope
	for _, id := range ids[1:] {
		if parseRef(id).Scope != scope {
			return "", errors.New("aliopen: 不能跨备份盘与资源盘操作")
		}
	}
	return scope, nil
}

func sameAliOpenRefScope(refs []drive.FileRef, targetID string) (Scope, error) {
	ids := make([]string, 0, len(refs)+1)
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	if targetID != "" {
		ids = append(ids, targetID)
	}
	return sameAliOpenScopeIDs(ids...)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// authRefreshToken handles aliyun open login with a refresh_token.
func authRefreshToken(ctx context.Context, req drive.AuthRequest) (*model.TokenInfo, error) {
	refreshToken := strings.TrimSpace(req.Config["refresh_token"])
	if refreshToken == "" {
		return nil, errors.New("aliopen: 请输入 refresh_token")
	}
	clientID := strings.TrimSpace(req.Config["client_id"])
	clientSecret := strings.TrimSpace(req.Config["client_secret"])

	sess := &Session{
		RefreshToken: refreshToken,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
	cl := &client{http: netx.NewClient(60 * time.Second), session: sess}
	if err := cl.refresh(ctx, ""); err != nil {
		return nil, err
	}
	used, total := cl.GetSpaceInfo(ctx)
	uid := sess.accountID()
	if uid == "" {
		return nil, errors.New("aliopen: 登录成功但未返回账号标识")
	}
	name := sess.displayName()
	if name == "" {
		name = uid
	}

	tok := &model.TokenInfo{
		TokenFrom:           providerID,
		AccessToken:         sess.AccessToken,
		RefreshToken:        mustJSON(sess),
		OpenAPIAccessToken:  sess.AccessToken,
		OpenAPIRefreshToken: sess.RefreshToken,
		TokenType:           "Bearer",
		UserID:              model.BuildUserID(providerID, uid),
		UserName:            firstAliOpenProfileValue(sess.UserName, sess.NickName, sess.Phone, name),
		NickName:            firstAliOpenProfileValue(sess.NickName, sess.UserName, sess.Phone, name),
		Name:                name,
		DefaultDriveID:      model.BuildDriveID(providerID, uid),
		ProviderAccountID:   uid,
		ProviderRootID:      "root",
		DeviceID:            "mnemo",
	}
	if sess.Avatar != "" {
		tok.Avatar = sess.Avatar
	}
	applyAliOpenQuota(tok, used, total)
	return tok, nil
}

func firstAliOpenProfileValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = json.NewDecoder
