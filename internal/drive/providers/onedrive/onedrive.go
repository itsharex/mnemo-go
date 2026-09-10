package onedrive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const providerID = model.ProviderOnedrive

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"favorite":            true,
			"search":              true,
			"createShare":         true,
			"shareExpiration":     true,
			"sharePassword":       true,
			"shareHistory":        true,
			"manageCreatedShares": true,
			"cancelCreatedShares": true,
			// Graph DELETE moves an item to the recycle bin. This provider does
			// not expose a permanent-delete endpoint through the current API.
			"recycleBin":      true,
			"permanentDelete": false,
		}, func(c *drive.Capabilities) {
			c.SetHashes([]string{"sha1", "quickxorhash"}, nil)
		}),
		Factory: func() drive.Driver { return &Driver{} },
		Auth:    authPKCE,
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "action", Type: "oauth", Label: "OAuth 授权"},
		}},
	})
}

// Driver implements drive.Driver for OneDrive.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return RootID }

func (d *Driver) SupportsRemoteFavorites(ctx context.Context, c drive.Context) (bool, error) {
	if c.Token != nil && c.Token.ProviderDriveType == "personal" {
		return false, nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return false, err
	}
	if c.Token.ProviderDriveType == "" {
		var info struct {
			DriveType string `json:"driveType"`
		}
		err := cl.getJSON(ctx, "/me/drive?$select=driveType", &info)
		if isGraphAuthenticationFailure(err) {
			cl, err = refreshedClientAfterGraphAuthFailure(ctx, c, err)
			if err == nil {
				err = cl.getJSON(ctx, "/me/drive?$select=driveType", &info)
			}
		}
		if err != nil {
			return false, err
		}
		switch info.DriveType {
		case "personal", "business", "documentLibrary":
			c.Token.ProviderDriveType = info.DriveType
		default:
			return false, errors.New("OneDrive 未返回有效账号类型，暂时无法确定收藏方式")
		}
	}
	switch c.Token.ProviderDriveType {
	case "personal":
		return false, nil
	case "business", "documentLibrary":
		return true, nil
	default:
		return false, errors.New("OneDrive 账号类型无效，暂时无法确定收藏方式")
	}
}

func (d *Driver) ListFavorites(ctx context.Context, c drive.Context) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	var current struct {
		ID string `json:"id"`
	}
	err = cl.getJSON(ctx, "/me/drive?$select=id", &current)
	if isGraphAuthenticationFailure(err) {
		cl, err = refreshedClientAfterGraphAuthFailure(ctx, c, err)
		if err == nil {
			err = cl.getJSON(ctx, "/me/drive?$select=id", &current)
		}
	}
	if err != nil {
		return nil, err
	}
	if current.ID == "" {
		return nil, errors.New("OneDrive 未返回当前空间 ID")
	}
	next := "/me/drive/following"
	seen := map[string]bool{}
	out := []model.File{}
	refreshed := false
	for next != "" {
		if seen[next] {
			return nil, errors.New("OneDrive 收藏列表分页游标重复")
		}
		seen[next] = true
		var page childrenResp
		err := cl.getJSON(ctx, next, &page)
		if !refreshed && isGraphAuthenticationFailure(err) {
			refreshed = true
			cl, err = refreshedClientAfterGraphAuthFailure(ctx, c, err)
			if err == nil {
				err = cl.getJSON(ctx, next, &page)
			}
		}
		if err != nil {
			return nil, err
		}
		if page.Value == nil {
			return nil, errors.New("OneDrive 收藏列表响应缺少 value")
		}
		for i := range page.Value {
			item := &page.Value[i]
			// The following feed also includes foreign shared drives, which this
			// mount cannot address using /me/drive/items. Keep account scope exact.
			if item.ParentReference == nil || item.ParentReference.DriveID == "" {
				return nil, errors.New("OneDrive 收藏项缺少所属空间")
			}
			if item.ParentReference.DriveID != current.ID {
				continue
			}
			f := mapItem(item, c.DriveID, item.ParentReference.ID)
			f.Starred = true
			out = append(out, f)
		}
		next = page.NextLink
	}
	return out, nil
}

func (d *Driver) Favorite(ctx context.Context, c drive.Context, ids []string, favorite bool) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	command := "follow"
	if !favorite {
		command = "unfollow"
	}
	done := []string{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return done, errors.New("OneDrive 收藏文件 ID 不能为空")
		}
		endpoint := "/me/drive/items/" + url.PathEscape(id) + "/" + command
		var result Item
		var out any
		if favorite {
			out = &result
		}
		err := cl.jsonDo(ctx, http.MethodPost, endpoint, nil, out)
		if isGraphAuthenticationFailure(err) {
			cl, err = refreshedClientAfterGraphAuthFailure(ctx, c, err)
			if err == nil {
				err = cl.jsonDo(ctx, http.MethodPost, endpoint, nil, out)
			}
		}
		if err != nil {
			return done, err
		}
		if favorite && result.ID != id {
			return done, errors.New("OneDrive 未确认文件收藏结果")
		}
		done = append(done, id)
	}
	return done, nil
}

func clientOf(c drive.Context) (*client, error) {
	if c.Token == nil || c.Token.AccessToken == "" {
		return nil, drive.ErrUnauthorized
	}
	return newClient(c.Token.AccessToken), nil
}

// isGraphAuthenticationFailure only matches the token failures for which a
// refresh-token retry is safe. Other 401 responses can be caused by tenant,
// scope, or administrator policy and must remain visible to the caller.
func isGraphAuthenticationFailure(err error) bool {
	var graphErr *graphAPIError
	return errors.As(err, &graphErr) &&
		graphErr.StatusCode == http.StatusUnauthorized &&
		strings.EqualFold(strings.TrimSpace(graphErr.Code), "InvalidAuthenticationToken")
}

// refreshedClientAfterGraphAuthFailure rotates a stale Graph access token once
// and returns a client built from the new value. Context.Token is a pointer, so
// the ops facade persists the rotated credentials after the original operation
// returns.
func refreshedClientAfterGraphAuthFailure(ctx context.Context, c drive.Context, requestErr error) (*client, error) {
	if !isGraphAuthenticationFailure(requestErr) {
		return nil, requestErr
	}
	if err := refreshOneDriveAccessToken(ctx, c.Token); err != nil {
		return nil, fmt.Errorf("onedrive: 目录请求鉴权失败（%v），刷新访问令牌失败: %w", requestErr, err)
	}
	return newClient(c.Token.AccessToken), nil
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, err := cl.List(ctx, dirID)
	if err != nil {
		cl, retryErr := refreshedClientAfterGraphAuthFailure(ctx, c, err)
		if retryErr != nil {
			return nil, retryErr
		}
		items, err = cl.List(ctx, dirID)
		if err != nil {
			return nil, fmt.Errorf("onedrive: 刷新令牌后目录请求仍失败: %w", err)
		}
	}
	return mapItems(items, c.DriveID, dirID), nil
}

func (d *Driver) ListPaged(ctx context.Context, c drive.Context, dirID, marker string, _ *drive.ListOptions) (*drive.DirPage, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, next, err := cl.ListPage(ctx, dirID, marker)
	if err != nil {
		cl, retryErr := refreshedClientAfterGraphAuthFailure(ctx, c, err)
		if retryErr != nil {
			return nil, retryErr
		}
		items, next, err = cl.ListPage(ctx, dirID, marker)
		if err != nil {
			return nil, fmt.Errorf("onedrive: 刷新令牌后目录请求仍失败: %w", err)
		}
	}
	return &drive.DirPage{Items: mapItems(items, c.DriveID, dirID), NextMarker: next}, nil
}

func (d *Driver) Search(ctx context.Context, c drive.Context, keyword string) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, err := cl.Search(ctx, keyword)
	if err != nil {
		return nil, err
	}
	return mapItems(items, c.DriveID, RootID), nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if fileID == RootID {
		f := driveutil_file(c.DriveID, RootID, "")
		return f, nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	item, err := cl.Detail(ctx, fileID)
	if err != nil {
		return nil, err
	}
	parent := RootID
	if item.ParentReference != nil && item.ParentReference.ID != "" {
		parent = item.ParentReference.ID
	}
	return mapItem(item, c.DriveID, parent), nil
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
	item, err := cl.Detail(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if item.Folder != nil {
		return nil, errors.New("文件夹不能直接下载")
	}
	var headers map[string]string
	// Graph's downloadUrl/content.downloadUrl are pre-signed URLs. They do
	// not need the Graph Bearer token, and forwarding it to another host is
	// both unnecessary and a credential-leak risk. Keep the token only for
	// the authenticated /content fallback.
	if item.DownloadURL == "" && item.ContentDownloadURL == "" {
		headers = map[string]string{"Authorization": "Bearer " + c.Token.AccessToken}
	}
	return &model.DownloadURL{
		DriveID:      c.DriveID,
		FileID:       fileID,
		URL:          cl.DownloadURL(item),
		Size:         item.Size,
		DownloadMode: "redirect",
		Headers:      headers,
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID: c.DriveID,
		FileID:  fileID,
		Size:    u.Size,
		Headers: u.Headers,
		Qualities: []model.VideoQuality{{
			Quality: "origin", Label: "原画", Value: "origin", URL: u.URL, Headers: u.Headers, ForceProxy: true,
		}},
	}, nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return cl.Mkdir(ctx, parentID, name)
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	item, err := cl.Rename(ctx, fileID, name)
	if err != nil {
		return nil, err
	}
	parent := RootID
	if item.ParentReference != nil && item.ParentReference.ID != "" {
		parent = item.ParentReference.ID
	}
	return &drive.RenameResult{FileID: item.ID, ParentFileID: parent, Name: item.Name, IsDir: item.Folder != nil}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, id := range fileIDs {
		if err := cl.Delete(ctx, id); err == nil {
			ok = append(ok, id)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", id, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	// OneDrive delete removes directly here.
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	return d.Trash(ctx, c, ids)
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	return nil, drive.NotSupported("restore")
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, r := range refs {
		if err := cl.Move(ctx, r.ID, toParentID); err == nil {
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
	var ok []string
	var failed []error
	for _, r := range refs {
		// fetch name for the copy target
		name := ""
		if item, err := cl.Detail(ctx, r.ID); err == nil {
			name = item.Name
		} else {
			failed = append(failed, fmt.Errorf("%s: 获取文件信息失败: %w", r.ID, err))
			continue
		}
		if err := cl.Copy(ctx, r.ID, toParentID, name); err == nil {
			ok = append(ok, r.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", r.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) CreateShare(ctx context.Context, c drive.Context, params drive.ShareParams) (*model.ShareItem, error) {
	if len(params.FileIDs) != 1 {
		return nil, errors.New("OneDrive 分享链接一次只能选择一个文件或文件夹")
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	item, err := cl.CreateLink(ctx, params.FileIDs[0], params.Expiration, params.Password)
	if err != nil || item == nil {
		return item, err
	}
	item.AccountID = c.UserID
	item.DriveID = c.DriveID
	item.FileID = params.FileIDs[0]
	item.FileIDList = []string{params.FileIDs[0]}
	if name := strings.TrimSpace(params.ShareName); name != "" {
		item.ShareName = name
	}
	return item, nil
}

// CancelShare removes the Graph permission returned by createLink, which
// immediately invalidates the corresponding anonymous sharing URL.
func (d *Driver) CancelShare(ctx context.Context, c drive.Context, share model.ShareHistoryEntry) error {
	if strings.TrimSpace(share.FileID) == "" {
		return errors.New("onedrive: 分享记录缺少文件标识")
	}
	if strings.TrimSpace(share.ShareID) == "" {
		return errors.New("onedrive: 分享记录缺少权限标识")
	}
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	return cl.DeletePermission(ctx, share.FileID, share.ShareID)
}

// UploadOneFile uploads one file (simple PUT for small, upload session for large).
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || strings.TrimSpace(ui.Info.LocalFilePath) == "" {
		return errors.New("OneDrive: 上传文件路径为空")
	}
	cl, err := clientOf(c)
	if err != nil {
		return err
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
	ui.Info.Size = info.Size()
	name := ui.Info.Name
	if strings.TrimSpace(name) == "" {
		return errors.New("OneDrive: 上传文件名为空")
	}
	parentID := ui.Info.ParentFileID
	behavior := oneDriveConflictBehavior(ui.Info.ConflictPolicy)
	if info.Size() <= smallUploadLimit {
		target := graphUploadTarget(graphHost+smallUploadPath(parentID, name), behavior)
		fileID, putErr := cl.rawPut(ctx, target, f)
		if putErr != nil {
			if driveutil.ResolveConflictPolicy(ui.Info.ConflictPolicy) == driveutil.ConflictSkip && isGraphConflict(putErr) {
				return nil
			}
			return putErr
		}
		ui.Upload.FileID = fileID
		return nil
	}
	sessionErr := cl.sessionUpload(ctx, c, f, parentID, name, ui, behavior)
	if sessionErr != nil && driveutil.ResolveConflictPolicy(ui.Info.ConflictPolicy) == driveutil.ConflictSkip && isGraphConflict(sessionErr) {
		return nil
	}
	return sessionErr
}

func oneDriveConflictBehavior(policy string) string {
	switch driveutil.ResolveConflictPolicy(policy) {
	case driveutil.ConflictRefuse, driveutil.ConflictSkip:
		return "fail"
	case driveutil.ConflictRename:
		return "rename"
	default:
		return "replace"
	}
}

func graphUploadTarget(raw, behavior string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("@microsoft.graph.conflictBehavior", behavior)
	u.RawQuery = q.Encode()
	return u.String()
}

func isGraphConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 409") ||
		strings.Contains(msg, "namealreadyexists") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "same name") ||
		strings.Contains(msg, "conflict")
}

// oneDriveScope is the OAuth scope used for token refresh.
const oneDriveScope = "Files.ReadWrite offline_access User.Read"

// refreshOneDriveToken exchanges a refresh_token for a fresh access_token via
// the Microsoft OAuth2 token endpoint.
func refreshOneDriveToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*model.TokenInfo, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	form.Set("scope", oneDriveScope)

	cl := netx.NewClient(60 * time.Second)
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := cl.PostForm(ctx, msTokenURL, nil, form, &raw); err != nil {
		return nil, err
	}
	if raw.AccessToken == "" {
		return nil, errors.New("onedrive: refresh returned no access_token")
	}
	return &model.TokenInfo{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresIn:    raw.ExpiresIn,
		TokenType:    raw.TokenType,
		ExpireTime:   time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second).UTC().Format(time.RFC3339),
	}, nil
}

// refreshOneDriveAccessToken renews only the OAuth session. It deliberately
// does not fetch /me or /me/drive: this helper is used by an in-flight file
// operation and must not turn one transparent retry into several background
// account requests.
func refreshOneDriveAccessToken(ctx context.Context, token *model.TokenInfo) error {
	if token == nil {
		return errors.New("OneDrive 未登录")
	}
	refreshToken := strings.TrimSpace(token.RefreshToken)
	if refreshToken == "" {
		return errors.New("onedrive: missing refresh_token")
	}
	configuredID := strings.TrimSpace(drive.Secret("onedrive_client_id"))
	clientID := strings.TrimSpace(token.DeviceID)
	// Older builds stored the literal "mnemo" instead of the client id.
	// Treat it as unset so existing accounts fall back to the configured or
	// bundled rclone-compatible application.
	if clientID == "" || clientID == "mnemo" {
		clientID = configuredID
	}
	clientID, clientSecret := resolveCredentials(clientID, "", configuredID, "")

	fresh, err := refreshOneDriveToken(ctx, clientID, clientSecret, refreshToken)
	if err != nil {
		return err
	}

	// Preserve fields that the token endpoint does not return.
	token.AccessToken = fresh.AccessToken
	if fresh.RefreshToken != "" {
		token.RefreshToken = fresh.RefreshToken
	}
	expiresIn := fresh.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = token.ExpiresIn
	}
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	token.ExpiresIn = expiresIn
	token.ExpireTime = time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
	if fresh.TokenType != "" {
		token.TokenType = fresh.TokenType
	}
	token.TokenFrom = providerID
	token.DeviceID = clientID
	return nil
}

// fetchOneDriveProfile queries /me and /me/drive to populate the token's
// UserName, quota, and drive id.
func fetchOneDriveProfile(ctx context.Context, accessToken string, tok *model.TokenInfo) {
	cl := newClient(accessToken)

	// /me — account display name
	var me struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := cl.getJSON(ctx, "/me", &me); err == nil {
		if me.UserPrincipalName != "" {
			tok.UserName = me.UserPrincipalName
		} else if me.DisplayName != "" {
			tok.UserName = me.DisplayName
		}
		if me.DisplayName != "" {
			tok.NickName = me.DisplayName
		}
		if me.ID != "" {
			tok.ProviderAccountID = me.ID
		}
	}

	// /me/drive — quota + drive id
	var driveInfo struct {
		ID        string `json:"id"`
		DriveType string `json:"driveType"`
		Quota     *struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
		} `json:"quota"`
	}
	if err := cl.getJSON(ctx, "/me/drive", &driveInfo); err == nil {
		if driveInfo.DriveType != "" {
			tok.ProviderDriveType = driveInfo.DriveType
		}
		if driveInfo.ID != "" {
			tok.DefaultDriveID = model.BuildDriveID(providerID, driveInfo.ID)
		}
		if driveInfo.Quota != nil {
			tok.TotalSize = driveInfo.Quota.Total
			tok.UsedSize = driveInfo.Quota.Used
			if tok.TotalSize > tok.UsedSize {
				tok.FreeSize = tok.TotalSize - tok.UsedSize
			}
		}
	}
}

// RefreshAccount renews the OneDrive OAuth access token using the stored
// refresh_token, then fetches account profile and drive quota to update the
// token metadata.
func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if err := refreshOneDriveAccessToken(ctx, token); err != nil {
		return nil, err
	}

	// update account info + quota (non-blocking on error)
	fetchOneDriveProfile(ctx, token.AccessToken, token)
	applyOneDriveIdentity(token)

	return token, nil
}

// ResolveTransferHash returns a hash already exposed by Microsoft Graph.
// It is used only when the migration target supports the same fingerprint.
func (d *Driver) ResolveTransferHash(ctx context.Context, c drive.Context, fileID, method string, _ bool) (string, error) {
	if method != "sha1" && method != "quickxorhash" {
		return "", nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return "", err
	}
	item, err := cl.Detail(ctx, fileID)
	if err != nil || item.File == nil || item.File.Hashes == nil {
		return "", err
	}
	if method == "sha1" {
		return item.File.Hashes.SHA1Hash, nil
	}
	return item.File.Hashes.QuickXorHash, nil
}

func mapItems(items []Item, driveID, parentID string) []model.File {
	out := make([]model.File, 0, len(items))
	for i := range items {
		out = append(out, mapItem(&items[i], driveID, parentID))
	}
	return out
}

// mapRootFile builds a synthetic root folder file.
func driveutil_file(driveID, fileID, name string) model.File {
	if name == "" {
		name = "OneDrive"
	}
	return model.File{
		DriveID: driveID, FileID: fileID, ParentFileID: "", Name: name,
		NameSearch: name, Category: "folder", IsDir: true, Icon: "iconfile-folder",
	}
}
