// Package guangya implements the 光鸭云盘 provider.
package guangya

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	accountHost = "https://account.guangyapan.com"
	apiHost     = "https://api.guangyapan.com"
	clientID    = "aMe-8VSlkrbQXpUR"
	RootID      = "guangya_root"
	ua          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

const providerID = model.ProviderGuangya

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"copy":                true,
			"createShare":         true,
			"manageCreatedShares": true,
			"cancelCreatedShares": true,
			"shareExpiration":     true,
			"sharePassword":       true,
			"combinedShare":       true,
			"shareHistory":        true,
			"recycleBin":          true,
			"trashView":           true,
			"trashRestore":        true,
			"permanentDelete":     false,
		}, func(c *drive.Capabilities) {
			c.SetHashes([]string{"md5"}, nil)
			c.SetShareExpirationOptions(0, 1, 7, 30)
		}),
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "phone", Type: "text", Label: "手机号", Required: false},
			{Key: "sms_code", Type: "text", Label: "短信验证码", Required: false},
			{Key: "refresh_token", Type: "text", Label: "Refresh Token（可选，直接登录）", Required: false},
		}},
		Auth:    authLogin,
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// Session is the guangya credential set.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	DeviceID     string `json:"device_id"`
	ClientID     string `json:"client_id"`
	RootFolderID string `json:"root_folder_id"`
	Phone        string `json:"phone,omitempty"`
}

// client is an authenticated guangya session.
type client struct {
	http      *netx.Client
	sess      *Session
	token     *model.TokenInfo
	refreshMu sync.Mutex
}

func newDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clientOf(c drive.Context) (*client, error) {
	if c.Token == nil {
		return nil, drive.ErrUnauthorized
	}
	sess := parseSession(c.Token)
	if sess == nil {
		return nil, errors.New("光鸭云盘未登录")
	}
	if sess.DeviceID == "" {
		sess.DeviceID = newDeviceID()
	}
	if sess.ClientID == "" {
		sess.ClientID = clientID
	}
	return &client{http: netx.NewClient(60 * time.Second), sess: sess, token: c.Token}, nil
}

func parseSession(tok *model.TokenInfo) *Session {
	if tok == nil {
		return nil
	}
	var s Session
	if tok.RefreshToken != "" {
		if err := json.Unmarshal([]byte(tok.RefreshToken), &s); err == nil && (s.AccessToken != "" || s.RefreshToken != "") {
			if s.AccessToken == "" {
				s.AccessToken = tok.AccessToken
			}
			return &s
		}
	}
	if tok.AccessToken != "" {
		return &Session{AccessToken: tok.AccessToken, DeviceID: newDeviceID(), ClientID: clientID}
	}
	return nil
}

// apiHeaders builds the account headers.
func (c *client) apiHeaders(extra map[string]string) map[string]string {
	h := map[string]string{
		"Content-Type": "application/json", "Accept": "application/json, text/plain, */*",
		"X-Client-Id": c.sess.ClientID, "X-Client-Version": "0.0.1",
		"X-Device-Id": c.sess.DeviceID, "X-Device-Model": "chrome%2F147.0.0.0",
		"X-Device-Name":   "PC-Chrome",
		"X-Device-Sign":   fmt.Sprintf("wdi10.%sxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", c.sess.DeviceID),
		"X-Net-Work-Type": "NONE", "X-OS-Version": "MacIntel", "X-Platform-Version": "1",
		"X-Protocol-Version": "301", "X-Provider-Name": "NONE", "X-SDK-Version": "9.0.2",
		// The web resource API checks these short-form device headers in
		// addition to the X-Device-* family for account asset requests.
		"Did": c.sess.DeviceID, "Dt": "4",
		"User-Agent": ua,
	}
	if c.sess.AccessToken != "" {
		h["Authorization"] = "Bearer " + c.sess.AccessToken
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// post calls the guangya API (rate limited 500ms).
func (c *client) post(ctx context.Context, path string, body any, out any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(500 * time.Millisecond):
	}
	resp, err := c.http.Do(ctx, http.MethodPost, apiHost+path, c.apiHeaders(nil), netx.JSONBody(body))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err := c.refresh(ctx); err != nil {
			return fmt.Errorf("光鸭登录已失效且刷新失败: %w", err)
		}
		resp, err = c.http.Do(ctx, http.MethodPost, apiHost+path, c.apiHeaders(nil), netx.JSONBody(body))
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return errors.New("guangya: http " + fmt.Sprint(resp.StatusCode) + ": " + truncate(data, 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// refresh renews the session once for a request that received 401/403 and
// updates the cloned TokenInfo so drive.ops can persist it after the call.
func (c *client) refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if c.sess == nil || strings.TrimSpace(c.sess.RefreshToken) == "" {
		return errors.New("缺少 refresh_token")
	}
	next, err := refreshSession(ctx, c.http, c.sess)
	if err != nil {
		return err
	}
	c.sess = next
	if c.token != nil {
		c.token.AccessToken = next.AccessToken
		c.token.RefreshToken = mustJSON(next)
		c.token.TokenType = "Bearer"
	}
	return nil
}

// File is a raw guangya file entry.
type File struct {
	FileID   string `json:"fileId"`
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"`
	ResType  int    `json:"resType"` // 2 = folder
	CTime    int64  `json:"cTime"`
	UTime    int64  `json:"uTime"`
	ParentID string `json:"parentId"`
	MD5      string `json:"md5,omitempty"`
}

// List returns files of a folder.
func (c *client) List(ctx context.Context, parentID string) ([]File, error) {
	return c.list(ctx, parentID, false)
}

func (c *client) list(ctx context.Context, parentID string, recycle bool) ([]File, error) {
	var out []File
	for page := 0; page < 200; page++ {
		var resp struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data *struct {
				List  []map[string]any `json:"list"`
				Total int              `json:"total"`
			} `json:"data"`
		}
		body := map[string]any{
			"parentId": parentID, "page": page, "pageSize": 100,
			"orderBy": 3, "sortType": 1, "fileTypes": []any{},
		}
		if recycle {
			body = map[string]any{"parentId": "", "page": page, "pageSize": 100, "dirType": 4, "orderBy": 10, "sortType": 0}
		}
		err := c.post(ctx, "/userres/v1/file/get_file_list", body, &resp)
		if err != nil {
			return nil, err
		}
		if recycle && resp.Code != 0 {
			return nil, fmt.Errorf("光鸭回收站列表失败（code=%d）：%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil {
			if recycle {
				return nil, errors.New("光鸭回收站列表响应缺少文件数据")
			}
			break
		}
		list := resp.Data.List
		for _, item := range list {
			if recycle && (str(item["fileId"], item["file_id"]) == "" || str(item["fileName"], item["name"]) == "") {
				return nil, errors.New("光鸭回收站列表包含无效文件")
			}
			out = append(out, File{
				FileID:   str(item["fileId"], item["file_id"]),
				FileName: str(item["fileName"], item["name"]),
				FileSize: num(item["fileSize"], item["size"]),
				ResType:  int(num(item["resType"], item["res_type"])),
				CTime:    int64(num(item["cTime"], item["ctime"])),
				UTime:    int64(num(item["uTime"], item["utime"])),
				ParentID: parentID,
			})
		}
		if len(list) < 100 {
			break
		}
		if resp.Data.Total > 0 && len(out) >= resp.Data.Total {
			break
		}
		if recycle && page == 199 {
			return nil, errors.New("光鸭回收站分页超过安全上限")
		}
	}
	return out, nil
}

// Detail returns one file detail (with md5).
func (c *client) Detail(ctx context.Context, fileID string) (*File, error) {
	var resp struct {
		Data *struct {
			List     []map[string]any `json:"list"`
			FileInfo map[string]any   `json:"fileInfo"`
		} `json:"data"`
	}
	err := c.post(ctx, "/userres/v1/file/get_file_detail", map[string]any{"fileId": fileID}, &resp)
	if err != nil {
		return nil, err
	}
	var item map[string]any
	if resp.Data != nil {
		if len(resp.Data.List) > 0 {
			item = resp.Data.List[0]
		} else {
			// The current API returns data.fileInfo; the old web client uses
			// this shape for the MD5 needed by cross-drive instant transfer.
			item = resp.Data.FileInfo
		}
	}
	if item != nil {
		return &File{
			FileID:   str(item["fileId"], item["file_id"], fileID),
			FileName: str(item["fileName"], item["name"]),
			FileSize: num(item["fileSize"], item["size"]),
			ResType:  int(num(item["resType"], item["res_type"])),
			CTime:    int64(num(item["cTime"], item["ctime"])),
			UTime:    int64(num(item["uTime"], item["utime"])),
			ParentID: str(item["parentId"], item["parent_id"]),
			MD5:      str(item["md5"], item["MD5"]),
		}, nil
	}
	return &File{FileID: fileID, FileName: fileID}, nil
}

// DownloadInfo resolves the download url.
func (c *client) DownloadInfo(ctx context.Context, fileID string) (string, error) {
	var resp struct {
		Data *struct {
			SignedURL string `json:"signedURL"`
			URL       string `json:"downloadUrl"`
		} `json:"data"`
	}
	err := c.post(ctx, "/nd.bizuserres.s/v1/get_res_download_url", map[string]any{"fileId": fileID}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Data == nil {
		return "", errors.New("guangya: 获取下载地址失败")
	}
	url := firstNonEmpty(resp.Data.SignedURL, resp.Data.URL)
	if url == "" {
		return "", errors.New("guangya: 获取下载地址失败")
	}
	return url, nil
}

// Mkdir creates a folder.
func (c *client) Mkdir(ctx context.Context, parentID, name string) (*drive.MkdirResult, error) {
	var resp struct {
		Data *struct {
			FileID string `json:"fileId"`
		} `json:"data"`
	}
	err := c.post(ctx, "/userres/v1/file/create_dir", map[string]any{"parentId": parentID, "dirName": name}, &resp)
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	if resp.Data != nil {
		return &drive.MkdirResult{FileID: resp.Data.FileID}, nil
	}
	return &drive.MkdirResult{Error: "创建文件夹失败"}, nil
}

// Rename renames a file/folder.
func (c *client) Rename(ctx context.Context, fileID, name string) error {
	return c.post(ctx, "/userres/v1/file/rename", map[string]any{"fileId": fileID, "fileName": name}, nil)
}

// Move moves files.
func (c *client) Move(ctx context.Context, fileIDs []string, toParentID string) error {
	return c.post(ctx, "/userres/v1/file/move_file", map[string]any{"fileIds": fileIDs, "parentId": toParentID}, nil)
}

// Copy copies files.
func (c *client) Copy(ctx context.Context, fileIDs []string, toParentID string) error {
	return c.post(ctx, "/userres/v1/file/copy_file", map[string]any{"fileIds": fileIDs, "parentId": toParentID}, nil)
}

// Delete sends the provider's delete task, which moves normal files to its recycle bin.
func (c *client) Delete(ctx context.Context, fileIDs []string) error {
	return c.post(ctx, "/userres/v1/file/delete_file", map[string]any{"fileIds": fileIDs}, nil)
}

// SpaceInfo returns quota from the web client's asset endpoint. The service
// returns byte counts as either JSON numbers or strings depending on
// account/region, so decode both forms.
func (c *client) SpaceInfo(ctx context.Context) (used, total int64) {
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := c.post(ctx, "/assets/v1/get_assets", map[string]any{}, &resp); err == nil && resp.Data != nil {
		return num(resp.Data["usedSpaceSize"], resp.Data["used_space_size"], resp.Data["usedSize"], resp.Data["used_size"]),
			num(resp.Data["totalSpaceSize"], resp.Data["total_space_size"], resp.Data["totalSize"], resp.Data["total_size"])
	}
	return 0, 0
}

func applySpaceInfo(token *model.TokenInfo, used, total int64) {
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
	if token.FreeSize < 0 {
		token.FreeSize = 0
	}
}

// ---- driver ----

// Driver implements drive.Driver for guangya.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return RootID }

func toID(id string) string {
	if id == "" || id == RootID || id == "root" || id == "/" {
		return ""
	}
	return id
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, err := cl.List(ctx, toID(dirID))
	if err != nil {
		return nil, err
	}
	out := make([]model.File, 0, len(items))
	for _, it := range items {
		out = append(out, mapFile(&it, c.DriveID, dirID))
	}
	return out, nil
}

func (d *Driver) ListTrash(ctx context.Context, c drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, err := cl.list(ctx, "", true)
	if err != nil {
		return nil, err
	}
	out := make([]model.File, 0, len(items))
	for _, item := range items {
		out = append(out, mapFile(&item, c.DriveID, "trash"))
	}
	return out, nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if toID(fileID) == "" {
		return model.File{DriveID: c.DriveID, FileID: RootID, Name: "光鸭云盘", NameSearch: "guangya", IsDir: true, Icon: "iconfile-folder"}, nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	it, err := cl.Detail(ctx, fileID)
	if err != nil {
		return nil, err
	}
	f := mapFile(it, c.DriveID, "")
	return f, nil
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

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, _ int) (*model.DownloadURL, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	u, err := cl.DownloadInfo(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID: c.DriveID, FileID: fileID, URL: u, DownloadMode: "proxy",
		Headers: map[string]string{
			"Referer":    "https://pan.quark.cn/",
			"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		},
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

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return cl.Mkdir(ctx, toID(parentID), name)
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	if err := cl.Rename(ctx, fileID, name); err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: fileID, Name: name}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	return d.Delete(ctx, c, idsToRefs(fileIDs))
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	if err := cl.Delete(ctx, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	if err := cl.Move(ctx, ids, toID(toParentID)); err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	if err := cl.Copy(ctx, ids, toID(toParentID)); err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) ResolveTransferHash(ctx context.Context, c drive.Context, fileID, method string, _ bool) (string, error) {
	if method != "md5" {
		return "", nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return "", err
	}
	it, err := cl.Detail(ctx, fileID)
	if err != nil {
		return "", err
	}
	return it.MD5, nil
}

func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if token == nil {
		return nil, errors.New("光鸭云盘未登录")
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	// Access tokens are short-lived. Renew before querying the API when a
	// refresh token is available, then persist the complete session back into
	// TokenInfo so the next request uses the new access token.
	if strings.TrimSpace(cl.sess.RefreshToken) != "" {
		if next, refreshErr := refreshSession(ctx, cl.http, cl.sess); refreshErr == nil {
			cl.sess = next
			token.AccessToken = next.AccessToken
			token.RefreshToken = mustJSON(next)
		} else {
			return nil, fmt.Errorf("光鸭云盘刷新凭据失败: %w", refreshErr)
		}
	}
	used, total := cl.SpaceInfo(ctx)
	applySpaceInfo(token, used, total)
	return token, nil
}

func mapFile(it *File, driveID, parentID string) model.File {
	isDir := it.ResType == 2
	f := driveutil.NewFile(driveID, it.FileID, parentID, it.FileName, isDir, it.FileSize, it.UTime)
	f.ContentHash = it.MD5
	if it.MD5 != "" {
		f.ContentHashName = "md5"
	}
	return f
}

// ---- auth ----

func normalizePhone(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), " ", "")
	if strings.HasPrefix(p, "+") {
		return p
	}
	if len(p) == 11 && p[0] == '1' {
		return "+86" + p
	}
	return p
}

// authLogin handles refresh_token or SMS login.
func authLogin(ctx context.Context, req drive.AuthRequest) (*model.TokenInfo, error) {
	refresh := strings.TrimSpace(req.Config["refresh_token"])
	if refresh != "" {
		return loginByRefreshToken(ctx, refresh)
	}
	phone := strings.TrimSpace(req.Config["phone"])
	code := strings.TrimSpace(req.Config["sms_code"])
	if phone == "" || code == "" {
		return nil, errors.New("光鸭云盘：请填写手机号和验证码（或 Refresh Token）")
	}
	return loginBySms(ctx, phone, code, req.Config["verification_id"], req.Config["device_id"], req.Config["captcha_token"])
}

func loginByRefreshToken(ctx context.Context, refresh string) (*model.TokenInfo, error) {
	hc := netx.NewClient(60 * time.Second)
	deviceID := newDeviceID()
	headers := map[string]string{
		"Content-Type": "application/json", "Accept": "application/json",
		"X-Client-Id": clientID, "X-Client-Version": "0.0.1", "X-Device-Id": deviceID,
	}
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	err := hc.PostJSON(ctx, accountHost+"/v1/auth/token", headers, map[string]any{
		"client_id": clientID, "grant_type": "refresh_token", "refresh_token": refresh,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.AccessToken == "" {
		return nil, errors.New("guangya: 刷新 token 失败")
	}
	sess := &Session{
		AccessToken: resp.AccessToken, RefreshToken: firstNonEmpty(resp.RefreshToken, refresh),
		DeviceID: deviceID, ClientID: clientID,
	}
	return enrichLoginToken(ctx, hc, sess), nil
}

func refreshSession(ctx context.Context, hc *netx.Client, sess *Session) (*Session, error) {
	if sess == nil || strings.TrimSpace(sess.RefreshToken) == "" {
		return sess, drive.AuthExpired("光鸭云盘 refresh_token 为空")
	}
	deviceID := firstNonEmpty(sess.DeviceID, newDeviceID())
	clientIDValue := firstNonEmpty(sess.ClientID, clientID)
	headers := map[string]string{
		"Content-Type": "application/json", "Accept": "application/json",
		"X-Client-Id": clientIDValue, "X-Client-Version": "0.0.1", "X-Device-Id": deviceID,
	}
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := hc.PostJSON(ctx, accountHost+"/v1/auth/token", headers, map[string]any{
		"client_id": clientIDValue, "grant_type": "refresh_token", "refresh_token": sess.RefreshToken,
	}, &resp); err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "invalid_grant") || strings.Contains(message, "invalid_token") || strings.Contains(message, "refresh_token") && (strings.Contains(message, "expired") || strings.Contains(message, "invalid")) {
			return nil, drive.AuthExpired("光鸭云盘授权已过期，请重新登录")
		}
		return nil, err
	}
	if strings.TrimSpace(resp.AccessToken) == "" {
		msg := firstNonEmpty(resp.ErrorDesc, resp.Error, "刷新光鸭 token 失败")
		normalized := strings.ToLower(msg)
		if strings.Contains(normalized, "invalid_grant") || strings.Contains(normalized, "invalid_token") || strings.Contains(normalized, "refresh_token") && (strings.Contains(normalized, "expired") || strings.Contains(normalized, "invalid")) {
			return nil, drive.AuthExpired("光鸭云盘授权已过期，请重新登录")
		}
		return nil, errors.New(msg)
	}
	return &Session{
		AccessToken: resp.AccessToken, RefreshToken: firstNonEmpty(resp.RefreshToken, sess.RefreshToken),
		DeviceID: deviceID, ClientID: clientIDValue, RootFolderID: sess.RootFolderID, Phone: sess.Phone,
	}, nil
}

// SendSms requests a verification code for a phone.
func SendSms(ctx context.Context, phone string) (verificationID, deviceID, captchaToken string, err error) {
	hc := netx.NewClient(60 * time.Second)
	deviceID = newDeviceID()
	// Guangya's current account API silently accepts requests without a
	// captcha in some environments, so initialization is best effort.
	captchaToken, _ = initCaptchaToken(ctx, hc, phone, deviceID)
	send := func(token string) (string, error) {
		headers := accountHeaders(deviceID, token)
		var resp struct {
			VerificationID string `json:"verification_id"`
			Error          string `json:"error"`
			ErrorDesc      string `json:"error_description"`
		}
		if err := hc.PostJSON(ctx, accountHost+"/v1/auth/verification", headers, map[string]any{
			"phone_number": normalizePhone(phone), "target": "ANY", "client_id": clientID,
		}, &resp); err != nil {
			return "", err
		}
		if resp.VerificationID != "" {
			return resp.VerificationID, nil
		}
		msg := firstNonEmpty(resp.ErrorDesc, resp.Error, "发送验证码失败")
		return "", errors.New(msg)
	}
	verificationID, err = send(captchaToken)
	if err != nil && isCaptchaError(err) {
		if refreshed, initErr := initCaptchaToken(ctx, hc, phone, deviceID); initErr == nil {
			captchaToken = refreshed
			verificationID, err = send(captchaToken)
		}
	}
	if err != nil {
		return "", "", "", err
	}
	return verificationID, deviceID, captchaToken, nil
}

func loginBySms(ctx context.Context, phone, code, verificationID, deviceID, captchaToken string) (*model.TokenInfo, error) {
	if deviceID == "" {
		deviceID = newDeviceID()
	}
	hc := netx.NewClient(60 * time.Second)
	headers := accountHeaders(deviceID, captchaToken)
	var verifyResp struct {
		VerificationToken string `json:"verification_token"`
	}
	if err := hc.PostJSON(ctx, accountHost+"/v1/auth/verification/verify", headers, map[string]any{
		"verification_id": verificationID, "verification_code": code, "client_id": clientID,
	}, &verifyResp); err != nil {
		return nil, err
	}
	if verifyResp.VerificationToken == "" {
		return nil, errors.New("guangya: 验证码校验失败")
	}
	var signResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	err := hc.PostJSON(ctx, accountHost+"/v1/auth/signin", headers, map[string]any{
		"verification_code": code, "verification_token": verifyResp.VerificationToken,
		"username": normalizePhone(phone), "client_id": clientID,
	}, &signResp)
	if err != nil {
		return nil, err
	}
	if signResp.AccessToken == "" {
		return nil, errors.New("guangya: 登录失败")
	}
	sess := &Session{
		AccessToken: signResp.AccessToken, RefreshToken: signResp.RefreshToken,
		DeviceID: deviceID, ClientID: clientID, Phone: normalizePhone(phone),
	}
	return enrichLoginToken(ctx, hc, sess), nil
}

func enrichLoginToken(ctx context.Context, hc *netx.Client, sess *Session) *model.TokenInfo {
	tok := buildToken(sess)
	cl := &client{http: hc, sess: sess, token: tok}
	used, total := cl.SpaceInfo(ctx)
	applySpaceInfo(tok, used, total)
	return tok
}

func accountHeaders(deviceID, captchaToken string) map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json", "Accept": "application/json",
		"X-Client-Id": clientID, "X-Client-Version": "0.0.1", "X-Device-Id": deviceID,
	}
	if strings.TrimSpace(captchaToken) != "" {
		headers["X-Captcha-Token"] = strings.TrimSpace(captchaToken)
	}
	return headers
}

func initCaptchaToken(ctx context.Context, hc *netx.Client, phone, deviceID string) (string, error) {
	var resp struct {
		CaptchaToken string `json:"captcha_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	e164 := normalizePhone(phone)
	err := hc.PostJSON(ctx, accountHost+"/v1/shield/captcha/init", accountHeaders(deviceID, ""), map[string]any{
		"client_id": clientID,
		"action":    "POST:/v1/auth/verification",
		"device_id": deviceID,
		"meta": map[string]string{
			"username": e164, "phone_number": e164, "VERIFICATION_PHONE": e164,
		},
	}, &resp)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.CaptchaToken) == "" {
		return "", errors.New(firstNonEmpty(resp.ErrorDesc, resp.Error, "初始化 captcha_token 失败"))
	}
	return strings.TrimSpace(resp.CaptchaToken), nil
}

func isCaptchaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "captcha_invalid") || strings.Contains(msg, "captcha_token expired")
}

func buildToken(sess *Session) *model.TokenInfo {
	uid := sess.Phone
	if uid == "" {
		uid = sess.DeviceID
		if len(uid) > 8 {
			uid = uid[:8]
		}
		if uid == "" {
			uid = "guangya"
		}
	}
	name := strings.TrimPrefix(uid, "+86")
	return &model.TokenInfo{
		TokenFrom:         providerID,
		AccessToken:       sess.AccessToken,
		RefreshToken:      mustJSON(sess),
		TokenType:         "Bearer",
		UserID:            model.BuildUserID(providerID, uid),
		UserName:          name,
		NickName:          name,
		Name:              name,
		ProviderAccountID: uid,
		ProviderRootID:    RootID,
	}
}

// ---- helpers ----

func str(vals ...any) string {
	for _, v := range vals {
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case float64:
			return fmt.Sprintf("%.0f", t)
		}
	}
	return ""
}

func num(vals ...any) int64 {
	for _, v := range vals {
		switch t := v.(type) {
		case float64:
			return int64(t)
		case float32:
			return int64(t)
		case int:
			return int64(t)
		case int64:
			return t
		case json.Number:
			if n, err := strconv.ParseInt(t.String(), 10, 64); err == nil {
				return n
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

func idsToRefs(ids []string) []drive.FileRef {
	refs := make([]drive.FileRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, drive.FileRef{ID: id})
	}
	return refs
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

// SendSms satisfies the app binding interface (sends a verification code).
func (d *Driver) SendSms(ctx context.Context, phone string) (verificationID, deviceID, captchaToken string, err error) {
	return SendSms(ctx, phone)
}
