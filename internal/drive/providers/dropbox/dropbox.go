// Package dropbox implements the Dropbox provider (API v2).
package dropbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const (
	apiHost           = "https://api.dropboxapi.com/2"
	contentHost       = "https://content.dropboxapi.com/2"
	RootID            = "dropbox_root"
	uploadSingleLimit = 150 * 1024 * 1024 // Dropbox single upload cap
	sessionChunkSize  = 8 * 1024 * 1024
	rpcRetryAttempts  = 3
	maxErrorBodyBytes = 8 * 1024
	maxErrorDetailLen = 200
)

const providerID = model.ProviderDropbox

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"search":              true,
			"createShare":         true,
			"shareExpiration":     true,
			"sharePassword":       true,
			"shareHistory":        true,
			"manageCreatedShares": true,
			"cancelCreatedShares": true,
			"recycleBin":          true,
		}, func(c *drive.Capabilities) {
			c.SetHashes([]string{"dropbox"}, nil)
		}),
		Factory: func() drive.Driver { return &Driver{} },
		Auth:    authPKCE,
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "action", Type: "oauth", Label: "OAuth 授权"},
		}},
	})
}

// Metadata is a raw Dropbox file/folder entry.
type Metadata struct {
	Tag            string `json:".tag"`
	Name           string `json:"name"`
	ID             string `json:"id"`
	PathLower      string `json:"path_lower"`
	PathDisplay    string `json:"path_display"`
	Rev            string `json:"rev"`
	Size           int64  `json:"size"`
	ServerModified string `json:"server_modified"`
	ContentHash    string `json:"content_hash"`
}

// client is an authenticated Dropbox session.
type client struct {
	http    *netx.Client
	token   string
	session *model.TokenInfo
}

func newClient(token string) *client {
	return &client{http: netx.NewClientWithSystemProxy(90 * time.Second), token: token}
}

func clientOf(c drive.Context) (*client, error) {
	if c.Token == nil || c.Token.AccessToken == "" {
		return nil, drive.ErrUnauthorized
	}
	cl := newClient(c.Token.AccessToken)
	cl.session = c.Token
	return cl, nil
}

func (c *client) renew(ctx context.Context) error {
	if c.session == nil || strings.TrimSpace(c.session.RefreshToken) == "" {
		return fmt.Errorf("%w: Dropbox 授权已过期，请重新授权此账号", drive.ErrUnauthorized)
	}
	if err := renewDropboxSession(ctx, c.session); err != nil {
		return err
	}
	c.token = c.session.AccessToken
	return nil
}

func expiredDropboxAccessToken(status int, body []byte) bool {
	if status != http.StatusUnauthorized {
		return false
	}
	var result struct {
		Summary string `json:"error_summary"`
		Error   struct {
			Tag string `json:".tag"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &result) != nil {
		return false
	}
	return result.Error.Tag == "expired_access_token" || strings.TrimSuffix(result.Summary, "/") == "expired_access_token"
}

// rpc posts a JSON body to an RPC endpoint and decodes the JSON response.
func (c *client) rpc(ctx context.Context, endpoint string, body any, out any) error {
	refreshed := false
	headers := map[string]string{
		"Authorization": "Bearer " + c.token,
		"Content-Type":  "application/json",
	}
	for attempt := 0; attempt < rpcRetryAttempts; attempt++ {
		resp, err := c.http.Do(ctx, http.MethodPost, apiHost+endpoint, headers, netx.JSONBody(body))
		if err != nil {
			return fmt.Errorf("dropbox: %s request failed: %w", endpoint, err)
		}

		reader := io.Reader(resp.Body)
		if resp.StatusCode >= http.StatusBadRequest {
			reader = io.LimitReader(resp.Body, maxErrorBodyBytes)
		}
		data, readErr := io.ReadAll(reader)
		status := resp.StatusCode
		requestID := compactDropboxDetail(resp.Header.Get("X-Dropbox-Request-Id"))
		delay := retryAfter(resp, attempt)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("dropbox: %s response read failed: %w", endpoint, readErr)
		}
		if status < http.StatusBadRequest {
			if out == nil {
				return nil
			}
			return json.Unmarshal(data, out)
		}

		apiErr := newDropboxRPCError(endpoint, status, requestID, data)
		if !refreshed && expiredDropboxAccessToken(status, data) {
			refreshed = true
			if err := c.renew(ctx); err != nil {
				return errors.Join(apiErr, err)
			}
			headers["Authorization"] = "Bearer " + c.token
			attempt-- // Authentication recovery has its own one-retry budget.
			continue
		}
		if !retryableDropboxStatus(status) || attempt == rpcRetryAttempts-1 {
			return apiErr
		}
		if err := waitRetry(ctx, delay); err != nil {
			return err
		}
	}
	return errors.New("dropbox: RPC request failed")
}

func newDropboxRPCError(endpoint string, status int, requestID string, body []byte) error {
	message := fmt.Sprintf("dropbox: %s http %d", endpoint, status)
	var errBody struct {
		ErrorSummary string `json:"error_summary"`
	}
	if json.Unmarshal(body, &errBody) == nil {
		if summary := compactDropboxDetail(strings.TrimPrefix(errBody.ErrorSummary, "path/")); summary != "" {
			message += ": " + summary
		}
	}
	if requestID != "" {
		message += " (request_id=" + requestID + ")"
	}
	return errors.New(message)
}

func compactDropboxDetail(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) <= maxErrorDetailLen {
		return value
	}
	return string([]rune(value)[:maxErrorDetailLen]) + "…"
}

type listFolderResp struct {
	Entries []Metadata `json:"entries"`
	Cursor  string     `json:"cursor"`
	HasMore bool       `json:"has_more"`
}

// ListPage lists one page of a folder.
func (c *client) ListPage(ctx context.Context, parentID, cursor string) ([]Metadata, string, bool, error) {
	if cursor != "" {
		var resp listFolderResp
		if err := c.rpc(ctx, "/files/list_folder/continue", map[string]any{"cursor": cursor}, &resp); err != nil {
			return nil, "", false, err
		}
		return filterDeleted(resp.Entries), resp.Cursor, resp.HasMore, nil
	}
	path := normalizeDropboxPath(parentID)
	var resp listFolderResp
	err := c.rpc(ctx, "/files/list_folder", map[string]any{
		// Root sentinels must be converted to an empty API path. In particular,
		// the frontend's default "root" can cause Dropbox to return HTTP 500.
		"path": path, "recursive": false, "include_media_info": false,
		"include_deleted": false, "include_has_explicit_shared_members": false,
		"include_mounted_folders": false, "include_non_downloadable_files": false,
		"limit": 2000,
	}, &resp)
	if err != nil {
		return nil, "", false, err
	}
	return filterDeleted(resp.Entries), resp.Cursor, resp.HasMore, nil
}

func filterDeleted(items []Metadata) []Metadata {
	out := items[:0]
	for _, it := range items {
		if it.Tag != "deleted" {
			out = append(out, it)
		}
	}
	return out
}

// List fetches every page of a folder.
func (c *client) List(ctx context.Context, parentID string) ([]Metadata, error) {
	var out []Metadata
	seen := map[string]bool{}
	cursor := ""
	for {
		items, next, hasMore, err := c.ListPage(ctx, parentID, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if !hasMore || next == "" {
			break
		}
		if seen[next] {
			return nil, errors.New("dropbox: duplicate cursor")
		}
		seen[next] = true
		cursor = next
	}
	return out, nil
}

// Detail returns metadata for a path.
func (c *client) Detail(ctx context.Context, path string) (*Metadata, error) {
	var m Metadata
	if err := c.rpc(ctx, "/files/get_metadata", map[string]any{
		"path": path, "include_media_info": true, "include_deleted": false,
		"include_has_explicit_shared_members": false,
	}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Search uses search_v2 and flattens matches.
func (c *client) Search(ctx context.Context, query string) ([]Metadata, error) {
	var out []Metadata
	var cursor string
	seen := map[string]bool{}
	for {
		var resp struct {
			Matches []struct {
				Metadata *struct {
					Metadata Metadata `json:"metadata"`
				} `json:"metadata"`
			} `json:"matches"`
			HasMore bool   `json:"has_more"`
			Cursor  string `json:"cursor"`
		}
		var err error
		if cursor == "" {
			err = c.rpc(ctx, "/files/search_v2", map[string]any{
				"query": query, "max_results": 1000, "options": map[string]any{"path": "", "filename_only": false},
			}, &resp)
		} else {
			if seen[cursor] {
				break
			}
			seen[cursor] = true
			err = c.rpc(ctx, "/files/search/continue_v2", map[string]any{"cursor": cursor}, &resp)
		}
		if err != nil {
			return nil, err
		}
		for _, m := range resp.Matches {
			if m.Metadata != nil && m.Metadata.Metadata.Tag != "deleted" {
				out = append(out, m.Metadata.Metadata)
			}
		}
		if !resp.HasMore || resp.Cursor == "" {
			break
		}
		cursor = resp.Cursor
	}
	return out, nil
}

// TemporaryLink returns a ~4h temporary download link.
func (c *client) TemporaryLink(ctx context.Context, path string) (string, int64, error) {
	var resp struct {
		Link     string   `json:"link"`
		Metadata Metadata `json:"metadata"`
	}
	if err := c.rpc(ctx, "/files/get_temporary_link", map[string]any{"path": path}, &resp); err != nil {
		return "", 0, err
	}
	return resp.Link, resp.Metadata.Size, nil
}

// Mkdir creates a folder.
func (c *client) Mkdir(ctx context.Context, parent, name string) (*drive.MkdirResult, error) {
	var resp struct {
		Metadata Metadata `json:"metadata"`
	}
	path := resolveCommandPath(parent, "", "")
	if path == "" {
		path = "/" + name
	} else {
		path = strings.TrimRight(path, "/") + "/" + name
	}
	if err := c.rpc(ctx, "/files/create_folder_v2", map[string]any{"path": path, "autorename": true}, &resp); err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	id := resp.Metadata.ID
	if id == "" {
		id = resp.Metadata.PathDisplay
	}
	return &drive.MkdirResult{FileID: id}, nil
}

// Rename moves path to new name in place (autorename).
func (c *client) Rename(ctx context.Context, path, name string) (string, error) {
	to := renameTarget(path, name)
	var resp struct {
		Metadata Metadata `json:"metadata"`
	}
	if err := c.rpc(ctx, "/files/move_v2", map[string]any{"from_path": path, "to_path": to, "autorename": true}, &resp); err != nil {
		return "", err
	}
	return resp.Metadata.ID, nil
}

// Delete removes a path.
func (c *client) Delete(ctx context.Context, path string) error {
	var resp struct{}
	return c.rpc(ctx, "/files/delete_v2", map[string]any{"path": path}, &resp)
}

// Move moves entries into a target folder.
func (c *client) Move(ctx context.Context, from, targetParent string) error {
	to := joinTarget(targetParent, from)
	var resp struct{}
	return c.rpc(ctx, "/files/move_v2", map[string]any{
		"from_path": from, "to_path": to, "autorename": true,
		"allow_shared_folder": true,
	}, &resp)
}

// Copy copies entries into a target folder.
func (c *client) Copy(ctx context.Context, from, targetParent string) error {
	to := joinTarget(targetParent, from)
	var resp struct{}
	return c.rpc(ctx, "/files/copy_v2", map[string]any{
		"from_path": from, "to_path": to, "autorename": true,
		"allow_shared_folder": true,
	}, &resp)
}

// sharedLinkMetadata is a create_shared_link response.
type sharedLinkMetadata struct {
	URL          string `json:"url"`
	ID           string `json:"id"`
	PathLower    string `json:"path_lower"`
	Name         string `json:"name"`
	LinkAccess   string `json:"link_access_level"`
	Expires      string `json:"expires"`
	LinkPassword string `json:"link_password"`
}

type sharedLinksPage struct {
	Links   []sharedLinkMetadata `json:"links"`
	Cursor  string               `json:"cursor"`
	HasMore bool                 `json:"has_more"`
}

func (c *client) listSharedLinks(ctx context.Context, path string) ([]sharedLinkMetadata, error) {
	var links []sharedLinkMetadata
	seen := map[string]bool{}
	cursor := ""
	for {
		endpoint := "/sharing/list_shared_links"
		body := map[string]any{"path": path, "direct_only": true}
		if cursor != "" {
			endpoint = "/sharing/list_shared_links/continue"
			body = map[string]any{"cursor": cursor}
		}
		var page sharedLinksPage
		if err := c.rpc(ctx, endpoint, body, &page); err != nil {
			return nil, err
		}
		links = append(links, page.Links...)
		if !page.HasMore || page.Cursor == "" {
			return links, nil
		}
		if seen[page.Cursor] {
			return nil, errors.New("dropbox: duplicate shared-link cursor")
		}
		seen[page.Cursor] = true
		cursor = page.Cursor
	}
}

// CreateSharedLink creates or reuses a public share link for a path.
func (c *client) CreateSharedLink(ctx context.Context, path, expiration, password string) (*model.ShareItem, error) {
	settings := map[string]any{"requested_visibility": "public"}
	if expiration != "" {
		settings["expires"] = expiration
	}
	if password != "" {
		settings["requested_visibility"] = "password"
		settings["link_password"] = password
	}
	body := map[string]any{"path": path, "settings": settings}
	var link sharedLinkMetadata
	err := c.rpc(ctx, "/sharing/create_shared_link_with_settings", body, &link)
	if err != nil {
		// fall back to listing existing links and modifying
		if existing, err2 := c.listSharedLinks(ctx, path); err2 == nil && len(existing) > 0 {
			link = existing[0]
			if link.URL == "" {
				return nil, err
			}
			if expiration != "" || password != "" {
				settings := map[string]any{}
				if expiration != "" {
					settings["expires"] = expiration
				}
				if password != "" {
					settings["requested_visibility"] = "password"
					settings["link_password"] = password
				} else {
					settings["requested_visibility"] = "public"
				}
				mod := map[string]any{"url": link.URL, "settings": settings}
				if modifyErr := c.rpc(ctx, "/sharing/modify_shared_link_settings", mod, &link); modifyErr != nil {
					return nil, fmt.Errorf("dropbox: 修改已有分享设置失败: %w", modifyErr)
				}
			}
			return mapSharedLink(link, path, password), nil
		}
		return nil, err
	}
	if strings.TrimSpace(link.URL) == "" {
		return nil, errors.New("dropbox: 分享接口未返回链接")
	}
	return mapSharedLink(link, path, password), nil
}

// RevokeSharedLink permanently disables a Dropbox shared link. The API uses
// its URL rather than the opaque link id, so the persisted history keeps both
// values but the URL is authoritative for this operation.
func (c *client) RevokeSharedLink(ctx context.Context, shareURL string) error {
	shareURL = strings.TrimSpace(shareURL)
	if shareURL == "" {
		return errors.New("dropbox: 分享链接为空")
	}
	return c.rpc(ctx, "/sharing/revoke_shared_link", map[string]any{"url": shareURL}, nil)
}

func mapSharedLink(l sharedLinkMetadata, path, pwd string) *model.ShareItem {
	shareID := l.ID
	if shareID == "" {
		shareID = l.URL
	}
	if strings.TrimSpace(pwd) == "" {
		pwd = l.LinkPassword
	}
	return &model.ShareItem{
		ShareID: shareID, ShareURL: l.URL, SharePwd: pwd,
		ShareName: l.Name, SharePolicy: l.LinkAccess, Expiration: l.Expires,
		FileID: path,
	}
}

// ---- content upload ----

// UploadSmall PUTs a file ≤150MB.
func (c *client) UploadSmall(ctx context.Context, path string, r io.Reader, size int64, policy uploadPolicy) (string, error) {
	arg, _ := json.Marshal(map[string]any{
		"path": path, "mode": policy.mode, "autorename": policy.autorename, "mute": false,
		"strict_conflict": policy.strictConflict,
	})
	headers := map[string]string{
		"Authorization":   "Bearer " + c.token,
		"Content-Type":    "application/octet-stream",
		"Dropbox-API-Arg": string(arg),
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if seeker, ok := r.(io.Seeker); ok {
				if _, err := seeker.Seek(0, io.SeekStart); err != nil {
					return "", err
				}
			}
		}
		resp, err := c.http.DoUpload(ctx, http.MethodPost, contentHost+"/files/upload", headers, r)
		if err != nil {
			return "", err
		}
		data, _ := io.ReadAll(resp.Body)
		status := resp.StatusCode
		delay := retryAfter(resp, attempt)
		resp.Body.Close()
		if status < 400 {
			var item Metadata
			if err := json.Unmarshal(data, &item); err != nil {
				return "", fmt.Errorf("dropbox: upload response decode failed: %w", err)
			}
			if strings.TrimSpace(item.ID) == "" {
				return "", errors.New("dropbox: upload response missing file id")
			}
			return item.ID, nil
		}
		if !retryableDropboxStatus(status) || attempt == 2 {
			return "", fmt.Errorf("dropbox: upload http %d: %s", status, strings.TrimSpace(string(data)))
		}
		if err := waitRetry(ctx, delay); err != nil {
			return "", err
		}
	}
	return "", errors.New("dropbox: upload failed")
}

// UploadSession streams a file >150MB via upload session chunks. The session
// id and acknowledged byte offset are persisted so a process restart can
// continue the same remote session instead of duplicating data.
func (c *client) UploadSession(ctx context.Context, dc drive.Context, f *os.File, path string, size int64, ui *model.UploadingUI, policy uploadPolicy) error {
	if size <= 0 {
		return errors.New("dropbox: upload session empty")
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	key := dropboxUploadSessionKey(dc, path, size, ui, policy, info.ModTime().UnixNano())
	savedID, savedParts := drive.LoadUploadSessionState(key)
	startOffset := int64(0)
	if savedID != "" && len(savedParts) > 0 && savedParts[0] >= 0 {
		startOffset = int64(savedParts[0])
		if startOffset > size {
			startOffset = 0
			savedID = ""
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		sessID := savedID
		pos := startOffset
		buf := make([]byte, sessionChunkSize)
		for pos < size {
			if ui != nil && ui.IsUploadStopRequested() {
				return errors.New("已暂停")
			}
			n, err := f.ReadAt(buf, pos)
			if err != nil && err != io.EOF {
				if attempt == 0 && savedID != "" {
					break
				}
				return err
			}
			if n == 0 {
				return errors.New("dropbox: upload session ended before file completion")
			}
			chunk := buf[:n]
			isLast := pos+int64(n) >= size
			if sessID == "" {
				sessID, err = c.sessionStart(ctx, chunk)
				if err != nil {
					if attempt == 0 && savedID != "" {
						break
					}
					return err
				}
				_ = drive.SaveUploadSessionState(key, sessID, []int{int(pos + int64(n))})
			} else if isLast {
				var fileID string
				fileID, err = c.sessionFinish(ctx, sessID, pos, chunk, path, policy)
				if err == nil {
					if ui != nil {
						ui.Upload.FileID = fileID
						ui.ReportUploadProgress(size, size)
					}
					drive.ClearUploadSession(key)
					return nil
				}
				if attempt == 0 && savedID != "" {
					break
				}
				return err
			} else {
				err = c.sessionAppend(ctx, sessID, pos, chunk)
				if err != nil {
					if attempt == 0 && savedID != "" {
						break
					}
					return err
				}
			}
			pos += int64(n)
			if ui != nil {
				ui.ReportUploadProgress(pos, size)
			}
			_ = drive.SaveUploadSessionState(key, sessID, []int{int(pos)})
			if isLast {
				fileID, finishErr := c.sessionFinish(ctx, sessID, pos-int64(n), chunk, path, policy)
				if finishErr != nil {
					if attempt == 0 && savedID != "" {
						break
					}
					return finishErr
				}
				if ui != nil {
					ui.Upload.FileID = fileID
					ui.ReportUploadProgress(size, size)
				}
				drive.ClearUploadSession(key)
				return nil
			}
		}
		if attempt == 0 && savedID != "" {
			drive.ClearUploadSession(key)
			savedID = ""
			startOffset = 0
			continue
		}
	}
	return errors.New("dropbox: upload session failed")
}

func dropboxUploadSessionKey(dc drive.Context, path string, size int64, ui *model.UploadingUI, policy uploadPolicy, modTime int64) string {
	identity := ""
	if ui != nil {
		identity = strings.TrimSpace(ui.Info.SHA1)
	}
	if identity == "" {
		identity = fmt.Sprintf("mtime:%d", modTime)
	}
	sessionName := strings.Join([]string{
		path,
		policy.mode,
		strconv.FormatBool(policy.autorename),
		strconv.FormatBool(policy.strictConflict),
		identity,
	}, "\x00")
	return drive.UploadSessionKey(dc.UserID, dc.DriveID, "", sessionName, size)
}

func (c *client) sessionStart(ctx context.Context, chunk []byte) (string, error) {
	var resp struct {
		SessionID string `json:"session_id"`
	}
	arg, _ := json.Marshal(map[string]any{"close": false})
	if err := c.contentJSON(ctx, "/files/upload_session/start", arg, chunk, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.SessionID) == "" {
		return "", errors.New("dropbox: upload session response missing session_id")
	}
	return resp.SessionID, nil
}

func (c *client) sessionAppend(ctx context.Context, sessID string, offset int64, chunk []byte) error {
	arg, _ := json.Marshal(map[string]any{
		"cursor": map[string]any{"session_id": sessID, "offset": offset},
	})
	return c.contentJSON(ctx, "/files/upload_session/append_v2", arg, chunk, nil)
}

func (c *client) sessionFinish(ctx context.Context, sessID string, offset int64, chunk []byte, path string, policy uploadPolicy) (string, error) {
	arg, _ := json.Marshal(map[string]any{
		"cursor": map[string]any{"session_id": sessID, "offset": offset},
		"commit": map[string]any{"path": path, "mode": policy.mode, "autorename": policy.autorename, "mute": false, "strict_conflict": policy.strictConflict},
	})
	var item Metadata
	if err := c.contentJSON(ctx, "/files/upload_session/finish", arg, chunk, &item); err != nil {
		return "", err
	}
	if strings.TrimSpace(item.ID) == "" {
		return "", errors.New("dropbox: upload session response missing file id")
	}
	return item.ID, nil
}

func (c *client) contentJSON(ctx context.Context, endpoint string, apiArg []byte, chunk []byte, out any) error {
	refreshed := false
	headers := map[string]string{
		"Authorization": "Bearer " + c.token,
		"Content-Type":  "application/octet-stream",
	}
	if apiArg != nil {
		headers["Dropbox-API-Arg"] = string(apiArg)
	}
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.http.DoUpload(ctx, http.MethodPost, contentHost+endpoint, headers, bytesReader(chunk))
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(resp.Body)
		status := resp.StatusCode
		delay := retryAfter(resp, attempt)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("dropbox: %s response read failed: %w", endpoint, readErr)
		}
		if status < 400 {
			if out != nil {
				return json.Unmarshal(data, out)
			}
			return nil
		}
		if !refreshed && expiredDropboxAccessToken(status, data) {
			refreshed = true
			if err := c.renew(ctx); err != nil {
				return err
			}
			headers["Authorization"] = "Bearer " + c.token
			attempt--
			continue
		}
		if !retryableDropboxStatus(status) || attempt == 2 {
			return fmt.Errorf("dropbox: %s http %d: %s", endpoint, status, strings.TrimSpace(string(data)))
		}
		if err := waitRetry(ctx, delay); err != nil {
			return err
		}
	}
	return nil
}

type uploadPolicy struct {
	mode           string
	autorename     bool
	strictConflict bool
}

func retryableDropboxStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func retryAfter(resp *http.Response, attempt int) time.Duration {
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return time.Duration(1<<attempt) * 500 * time.Millisecond
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// ---- helpers ----

func normalizeDropboxPath(path string) string {
	if path == "" || path == "root" || path == RootID || path == "/" {
		return ""
	}
	return path
}

// resolveCommandPath maps a file id to a Dropbox path.
func resolveCommandPath(fileID, description, path string) string {
	if normalizeDropboxPath(fileID) == "" {
		return ""
	}
	if path != "" {
		return path
	}
	if strings.HasPrefix(fileID, "/") {
		return fileID
	}
	return fileID
}

// renameTarget computes the target path for a rename.
func renameTarget(path, name string) string {
	parent := ""
	if i := strings.LastIndex(path, "/"); i > 0 {
		parent = path[:i]
	}
	if parent == "" {
		return "/" + name
	}
	return parent + "/" + name
}

func joinTarget(targetParent, from string) string {
	targetParent = normalizeDropboxPath(targetParent)
	base := from
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if targetParent == "" || targetParent == RootID {
		return "/" + base
	}
	return strings.TrimRight(targetParent, "/") + "/" + base
}

// mapItem converts a Dropbox entry to the unified file model.
func mapItem(item *Metadata, driveID, parentID string) model.File {
	isDir := item.Tag == "folder"
	path := item.PathDisplay
	if path == "" {
		path = item.PathLower
	}
	timeUnix := int64(0)
	if parsed, err := time.Parse(time.RFC3339, item.ServerModified); err == nil {
		timeUnix = parsed.Unix()
	}
	f := driveutil.NewFile(driveID, fileIDOf(item), parentID, item.Name, isDir, item.Size, timeUnix)
	f.Path = path
	f.ContentHash = item.ContentHash
	if item.ContentHash != "" {
		f.ContentHashName = "dropbox"
	}
	f.Description = encodeDescription(item)
	return f
}

func fileIDOf(item *Metadata) string {
	if item.ID != "" {
		return item.ID
	}
	if item.PathDisplay != "" {
		return item.PathDisplay
	}
	return item.PathLower
}

func encodeDescription(item *Metadata) string {
	parts := []string{}
	p := item.PathDisplay
	if p == "" {
		p = item.PathLower
	}
	if p != "" {
		parts = append(parts, "dropbox_path:"+p)
	}
	if item.Rev != "" {
		parts = append(parts, "dropbox_rev:"+item.Rev)
	}
	if item.ContentHash != "" {
		parts = append(parts, "dropbox_hash:"+item.ContentHash)
	}
	return strings.Join(parts, ";")
}

func parentOf(path string) string {
	if path == "" {
		return RootID
	}
	parent := strings.TrimRight(path, "/")
	if i := strings.LastIndex(parent, "/"); i > 0 {
		return parent[:i]
	}
	return RootID
}

// refreshDropboxToken exchanges a refresh_token for a fresh access_token via
// the Dropbox OAuth2 token endpoint.
func refreshDropboxToken(ctx context.Context, appKey, appSecret, refreshToken string) (*model.TokenInfo, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", appKey)
	if appSecret != "" {
		form.Set("client_secret", appSecret)
	}

	cl := netx.NewClientWithSystemProxy(60 * time.Second)
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := cl.PostForm(ctx, dbTokenURL, nil, form, &raw); err != nil {
		return nil, err
	}
	if raw.AccessToken == "" {
		return nil, errors.New("dropbox: refresh returned no access_token")
	}
	return &model.TokenInfo{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresIn:    raw.ExpiresIn,
		TokenType:    raw.TokenType,
		ExpireTime:   dropboxExpireTime(raw.ExpiresIn),
	}, nil
}

func dropboxExpireTime(expiresIn int64) string {
	if expiresIn <= 0 {
		return ""
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
}

// fetchDropboxProfile queries users/get_current_account and
// users/get_space_usage to populate the token's UserName, avatar, and quota.
func fetchDropboxProfile(ctx context.Context, accessToken string, tok *model.TokenInfo) {
	cl := newClient(accessToken)

	// account info
	var acct struct {
		AccountID       string `json:"account_id"`
		Email           string `json:"email"`
		ProfilePhotoURL string `json:"profile_photo_url"`
		Name            *struct {
			DisplayName string `json:"display_name"`
		} `json:"name"`
	}
	if err := cl.rpc(ctx, "/users/get_current_account", nil, &acct); err == nil {
		if acct.Name != nil && acct.Name.DisplayName != "" {
			tok.UserName = acct.Name.DisplayName
			tok.NickName = acct.Name.DisplayName
		} else if acct.Email != "" {
			tok.UserName = acct.Email
		}
		if acct.ProfilePhotoURL != "" {
			tok.Avatar = acct.ProfilePhotoURL
		}
		if acct.AccountID != "" {
			tok.ProviderAccountID = acct.AccountID
		}
	}

	// space usage
	var space struct {
		Used       int64 `json:"used"`
		Allocation *struct {
			Allocated int64 `json:"allocated"`
		} `json:"allocation"`
	}
	if err := cl.rpc(ctx, "/users/get_space_usage", nil, &space); err == nil {
		tok.UsedSize = space.Used
		if space.Allocation != nil {
			tok.TotalSize = space.Allocation.Allocated
			if tok.TotalSize > tok.UsedSize {
				tok.FreeSize = tok.TotalSize - tok.UsedSize
			}
		}
	}
}

// RefreshAccount renews the Dropbox OAuth access token using the stored
// refresh_token, then fetches account profile and space usage to update the
// token metadata.
func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if err := renewDropboxSession(ctx, token); err != nil {
		return nil, err
	}
	fetchDropboxProfile(ctx, token.AccessToken, token)
	applyDropboxIdentity(token)
	return token, nil
}

// Renew only OAuth fields during ordinary file operations. Profile/quota RPCs
// belong to explicit account refreshes, and must not recurse into auth recovery.
func renewDropboxSession(ctx context.Context, token *model.TokenInfo) error {
	if token == nil {
		return errors.New("Dropbox 未登录")
	}
	refreshToken := strings.TrimSpace(token.RefreshToken)
	if refreshToken == "" {
		return errors.New("dropbox: missing refresh_token")
	}
	configuredKey := strings.TrimSpace(drive.Secret("dropbox_app_key"))
	appKey := strings.TrimSpace(token.DeviceID)
	if appKey == "" {
		appKey = configuredKey
	}
	appKey, appSecret := resolveCredentials(appKey, "", configuredKey, "")

	fresh, err := refreshDropboxToken(ctx, appKey, appSecret, refreshToken)
	if err != nil {
		return err
	}

	// preserve fields not returned by the token endpoint
	token.AccessToken = fresh.AccessToken
	if fresh.RefreshToken != "" {
		token.RefreshToken = fresh.RefreshToken
	}
	if fresh.ExpiresIn > 0 {
		token.ExpiresIn = fresh.ExpiresIn
		token.ExpireTime = fresh.ExpireTime
	} else if token.ExpiresIn <= 0 {
		token.ExpiresIn = 14400
		if strings.TrimSpace(token.ExpireTime) == "" {
			token.ExpireTime = dropboxExpireTime(token.ExpiresIn)
		}
	}
	if fresh.TokenType != "" {
		token.TokenType = fresh.TokenType
	}
	token.TokenFrom = providerID
	token.DeviceID = appKey

	return nil
}

// ResolveTransferHash returns Dropbox's content hash from metadata. Dropbox
// hashes are provider-specific, so this is useful only to a target declaring
// the same "dropbox" fingerprint type.
func (d *Driver) ResolveTransferHash(ctx context.Context, c drive.Context, fileID, method string, _ bool) (string, error) {
	if method != "dropbox" {
		return "", nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return "", err
	}
	item, err := cl.Detail(ctx, fileID)
	if err != nil {
		return "", err
	}
	return item.ContentHash, nil
}

// applyDropboxIdentity keeps multiple Dropbox accounts isolated by the
// provider account_id, with a stable fallback for a temporary profile error.
func applyDropboxIdentity(tok *model.TokenInfo) {
	if tok == nil {
		return
	}
	id := strings.TrimSpace(tok.ProviderAccountID)
	if id == "" {
		sum := sha256.Sum256([]byte(tok.RefreshToken + "|" + tok.AccessToken))
		id = hex.EncodeToString(sum[:8])
	}
	tok.ProviderAccountID = id
	tok.UserID = model.BuildUserID(providerID, id)
	if strings.TrimSpace(tok.DefaultDriveID) == "" {
		tok.DefaultDriveID = model.BuildDriveID(providerID, id)
	}
}
