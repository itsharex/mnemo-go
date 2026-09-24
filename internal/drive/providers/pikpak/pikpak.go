package pikpak

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// Driver implements drive.Driver for PikPak.
type Driver struct {
	drive.BaseDriver
}

var pikpakRefreshLocks = struct {
	sync.Mutex
	accounts map[string]*sync.Mutex
}{accounts: make(map[string]*sync.Mutex)}

func pikpakRefreshLock(c drive.Context) *sync.Mutex {
	accountID := strings.TrimSpace(c.UserID)
	if accountID == "" {
		accountID = strings.TrimSpace(c.DriveID)
	}
	if accountID == "" && c.Token != nil {
		accountID = strings.TrimSpace(c.Token.ProviderAccountID)
	}
	if accountID == "" {
		accountID = "__unknown__"
	}
	pikpakRefreshLocks.Lock()
	defer pikpakRefreshLocks.Unlock()
	lock := pikpakRefreshLocks.accounts[accountID]
	if lock == nil {
		lock = &sync.Mutex{}
		pikpakRefreshLocks.accounts[accountID] = lock
	}
	return lock
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return RootID }

func (d *Driver) SupportsRemoteFavorites(context.Context, drive.Context) (bool, error) {
	return true, nil
}

func (d *Driver) ListFavorites(ctx context.Context, c drive.Context) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, err := cl.ListFavorites(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.File, 0, len(items))
	for i := range items {
		f := mapFile(&items[i], c.DriveID, items[i].ParentID)
		f.Starred = true
		out = append(out, f)
	}
	return out, nil
}

func clientOf(c drive.Context) (*client, error) {
	if c.Token == nil || c.Token.AccessToken == "" {
		return nil, drive.ErrUnauthorized
	}
	deviceID := c.Token.DeviceID
	if deviceID == "" {
		deviceID = c.Token.UserName
	}
	accountID := strings.TrimSpace(c.Token.ProviderAccountID)
	if accountID == "" {
		accountID = model.StripUserID(providerID, c.Token.UserID)
	}
	cl := newClient(c.Token.AccessToken, deviceID, accountID)
	if cl.deviceID == "" {
		cl.deviceID = c.Token.DeviceID
	}
	return cl, nil
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return listPages(ctx, cl, c, dirID, false)
}

func (d *Driver) ListPaged(ctx context.Context, c drive.Context, dirID, marker string, _ *drive.ListOptions) (*drive.DirPage, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	rooted := rootID(dirID)
	items, next, err := cl.ListPage(ctx, rooted, marker, false)
	if err != nil {
		return nil, err
	}
	return &drive.DirPage{Items: mapFiles(items, c.DriveID, rooted), NextMarker: next}, nil
}

func listPages(ctx context.Context, cl *client, c drive.Context, dirID string, trashed bool) ([]model.File, error) {
	items, err := cl.List(ctx, rootID(dirID), trashed)
	if err != nil {
		return nil, err
	}
	return mapFiles(items, c.DriveID, rootID(dirID)), nil
}

func (d *Driver) ListTrash(ctx context.Context, c drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return listPages(ctx, cl, c, RootID, true)
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if fileID == RootID {
		return model.File{DriveID: c.DriveID, FileID: RootID, Name: "网盘文件", NameSearch: "pikpak", IsDir: true, Icon: "iconfile-folder"}, nil
	}
	return d.GetFile(ctx, c, fileID)
}

func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	item, err := cl.Detail(ctx, fileID)
	if err != nil {
		return nil, err
	}
	f := mapFile(item, c.DriveID, firstNonEmpty(item.ParentID, "pikpak_root"))
	return &f, nil
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, _ int) (*model.DownloadURL, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	u, size, err := cl.DownloadURL(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID: c.DriveID, FileID: fileID, URL: u, Size: size,
		DownloadMode: "redirect",
		Headers:      map[string]string{"Authorization": "Bearer " + c.Token.AccessToken},
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	p, err := cl.PlayInfo(ctx, fileID)
	if err != nil {
		return nil, err
	}
	p.DriveID = c.DriveID
	p.FileID = fileID
	return p, nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	res, err := cl.Mkdir(ctx, rootID(parentID), name)
	if err != nil {
		return nil, err
	}
	return &drive.MkdirResult{FileID: res.FileID, Error: res.Error}, nil
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	item, err := cl.Detail(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if err := cl.Rename(ctx, fileID, name); err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: fileID, Name: name, ParentFileID: item.ParentID, IsDir: item.Kind == "drive#folder"}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	if err := cl.Trash(ctx, fileIDs); err != nil {
		return nil, err
	}
	return fileIDs, nil
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ids := refIDs(refs)
	if err := cl.Delete(ctx, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	if err := cl.Restore(ctx, fileIDs); err != nil {
		return nil, err
	}
	return fileIDs, nil
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ids := refIDs(refs)
	if err := cl.Move(ctx, ids, rootID(toParentID)); err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ids := refIDs(refs)
	if err := cl.Copy(ctx, ids, rootID(toParentID)); err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Favorite(ctx context.Context, c drive.Context, fileIDs []string, favorite bool) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	if err := cl.Star(ctx, fileIDs, favorite); err != nil {
		return nil, err
	}
	return fileIDs, nil
}

func (d *Driver) CreateShare(ctx context.Context, c drive.Context, params drive.ShareParams) (*model.ShareItem, error) {
	if len(params.FileIDs) == 0 {
		return nil, errors.New("pikpak: 创建分享至少选择一个文件")
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	res, err := cl.CreateShare(ctx, params.FileIDs, params.ShareName, params.Password, params.Expiration)
	if err != nil {
		return nil, err
	}
	expiration := res.Expiration
	if expiration == "" {
		expiration = params.Expiration
	}
	fileIDs := res.FileIDs
	if len(fileIDs) == 0 {
		fileIDs = append([]string(nil), params.FileIDs...)
	}
	return &model.ShareItem{
		AccountID: c.UserID, DriveID: c.DriveID,
		ShareID: res.ShareID, ShareURL: res.ShareURL, SharePwd: res.PassCode,
		ShareName: params.ShareName, SharePolicy: "public", Expiration: expiration,
		FileID: fileIDs[0], FileIDList: fileIDs, ShareMsg: "创建成功",
	}, nil
}

// CancelShare invalidates the remote PikPak sharing link before the app drops
// its local history record.
func (d *Driver) CancelShare(ctx context.Context, c drive.Context, share model.ShareHistoryEntry) error {
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	return cl.DeleteShare(ctx, share.ShareID)
}

func (c *client) findUploadConflict(ctx context.Context, parentID, name string) (*File, error) {
	items, err := c.List(ctx, rootID(parentID), false)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if !items[i].Trashed && items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, nil
}

func pikpakConflictName(name string, index int) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s (%d)%s", stem, index, ext)
}

// prepareUploadTarget applies the shared conflict policy before hashing. This
// avoids expensive GCID work for refuse/skip and mirrors the legacy trash-based
// overwrite behavior.
func (d *Driver) prepareUploadTarget(ctx context.Context, cl *client, ui *model.UploadingUI) (bool, error) {
	name := strings.TrimSpace(ui.Info.Name)
	if name == "" {
		name = filepath.Base(ui.Info.LocalFilePath)
		ui.Info.Name = name
	}
	policy := driveutil.ResolveConflictPolicy(ui.Info.ConflictPolicy)
	for index := 1; ; index++ {
		conflict, err := cl.findUploadConflict(ctx, ui.Info.ParentFileID, name)
		if err != nil {
			return false, err
		}
		if conflict == nil {
			ui.Info.Name = name
			return true, nil
		}
		switch policy {
		case driveutil.ConflictRefuse:
			return false, fmt.Errorf("目标目录已存在同名文件：%s", conflict.Name)
		case driveutil.ConflictSkip:
			return false, nil
		case driveutil.ConflictRename:
			name = pikpakConflictName(name, index)
		default:
			if err := cl.Trash(ctx, []string{conflict.ID}); err != nil {
				return false, fmt.Errorf("处理同名文件失败：%w", err)
			}
			return true, nil
		}
	}
}

func markPikPakUploadComplete(ui *model.UploadingUI, fileID string) {
	ui.Upload.FileID = fileID
	ui.ReportUploadProgress(ui.Info.Size, ui.Info.Size)
	ui.Upload.IsDowning = false
	ui.Upload.IsCompleted = true
	ui.Upload.IsFailed = false
	ui.Upload.DownState = "completed"
	ui.Upload.FailedMessage = ""
}

func markPikPakUploadFailed(ui *model.UploadingUI, err error) {
	if ui == nil {
		return
	}
	ui.Upload.IsDowning = false
	ui.Upload.IsCompleted = false
	ui.Upload.IsFailed = true
	ui.Upload.DownState = "failed"
	if err != nil {
		ui.Upload.FailedMessage = err.Error()
	}
}

func cleanupPikPakUpload(cl *client, fileID string) error {
	if cl == nil || fileID == "" {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return cl.Delete(cleanupCtx, []string{fileID})
}

// UploadOneFile uploads one file (GCID + create + optional OSS PUT).
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || ui.Info.LocalFilePath == "" {
		return errors.New("pikpak: 上传文件路径为空")
	}
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	shouldUpload, err := d.prepareUploadTarget(ctx, cl, ui)
	if err != nil {
		markPikPakUploadFailed(ui, err)
		return err
	}
	if !shouldUpload {
		return nil
	}
	ui.Upload.DownState = "uploading"
	ui.Upload.IsDowning = true
	ui.Upload.IsFailed = false
	ui.Upload.IsCompleted = false
	gcid, err := computeGCID(ui.Info.LocalFilePath, ui)
	if err != nil {
		markPikPakUploadFailed(ui, err)
		return err
	}
	info, err := os.Stat(ui.Info.LocalFilePath)
	if err != nil {
		markPikPakUploadFailed(ui, err)
		return err
	}
	ui.Info.Size = info.Size()
	body := map[string]any{
		"kind": "drive#file", "name": ui.Info.Name, "size": ui.Info.Size,
		"hash": gcid, "upload_type": "UPLOAD_TYPE_RESUMABLE",
		"resumable":   map[string]any{"provider": "PROVIDER_ALIYUN"},
		"folder_type": "NORMAL",
	}
	if parentID := apiParentID(ui.Info.ParentFileID); parentID != "" {
		body["parent_id"] = parentID
	}
	var res struct {
		UploadType string `json:"upload_type"`
		Resumable  *struct {
			Params *pikpakOSSParams `json:"params"`
		} `json:"resumable"`
		File *struct {
			ID string `json:"id"`
		} `json:"file"`
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	// Use a client where we can pass X-Captcha-Token if needed; basic flow:
	if err := cl.jsonDo(ctx, httpMethodPost(), "/drive/v1/files", body, &res, nil); err != nil {
		markPikPakUploadFailed(ui, err)
		return err
	}
	fileID := ""
	if res.File != nil {
		fileID = res.File.ID
	}
	if fileID == "" {
		fileID = res.ID
	}
	if fileID == "" {
		err := errors.New("pikpak: upload create missing file id")
		markPikPakUploadFailed(ui, err)
		return err
	}
	ui.Upload.FileID = fileID
	// No resumable params => rapid upload completed server side.
	if res.Resumable == nil {
		markPikPakUploadComplete(ui, fileID)
		return nil
	}
	params := res.Resumable.Params
	if err := ossPut(ctx, c, ui.Info.LocalFilePath, params, ui); err != nil {
		cleanupErr := cleanupPikPakUpload(cl, fileID)
		markPikPakUploadFailed(ui, err)
		if cleanupErr != nil {
			return fmt.Errorf("%w；清理远端残留失败：%v", err, cleanupErr)
		}
		return err
	}
	markPikPakUploadComplete(ui, fileID)
	return nil
}

func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if token == nil {
		return nil, drive.AuthExpired("PikPak 未登录")
	}
	lock := pikpakRefreshLock(c)
	lock.Lock()
	defer lock.Unlock()
	previousRefresh := token.RefreshToken
	previousAccess := token.AccessToken
	if err := drive.ReloadToken(c); err != nil {
		return nil, err
	}
	if token.AccessToken != "" && (token.RefreshToken != previousRefresh || token.AccessToken != previousAccess) {
		return token, nil
	}
	refresh := strings.TrimSpace(token.RefreshToken)
	access := token.AccessToken
	if refresh == "" {
		return nil, drive.AuthExpired("PikPak 缺少 refresh token，请重新登录")
	}
	deviceID := token.DeviceID
	if deviceID == "" {
		deviceID = token.UserName
	}
	hc := netx.NewClientWithSystemProxy(60 * time.Second)
	auth, err := refreshToken(ctx, hc, deviceID, refresh)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "invalid_grant") || strings.Contains(message, "invalid refresh") || strings.Contains(message, "refresh token") && strings.Contains(message, "expired") {
			if reloadErr := drive.ReloadToken(c); reloadErr == nil && token.AccessToken != "" && (token.RefreshToken != refresh || token.AccessToken != access) {
				return token, nil
			}
			return nil, drive.AuthExpired("PikPak 登录已失效，请重新登录")
		}
		return nil, err
	}
	token.AccessToken = auth.AccessToken
	// PikPak may omit refresh_token or expires_in when rotation is not
	// required. Never erase a still-valid persisted session field in that case.
	if strings.TrimSpace(auth.RefreshToken) != "" {
		token.RefreshToken = auth.RefreshToken
	}
	if auth.ExpiresIn > 0 {
		token.ExpiresIn = auth.ExpiresIn
	}
	if auth.TokenType != "" {
		token.TokenType = auth.TokenType
	}
	if err := drive.PersistToken(c); err != nil {
		return nil, err
	}
	if cl := newClient(auth.AccessToken, deviceID, token.ProviderAccountID); cl != nil {
		token.UsedSize, token.TotalSize = cl.About(ctx)
	}
	return token, nil
}

func rootID(id string) string {
	if id == "" || id == "root" || id == RootID || id == "/" {
		return RootID
	}
	return id
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func refIDs(refs []drive.FileRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.ID)
	}
	return out
}

// mapFile converts a PikPak file to the unified model.
func mapFile(item *File, driveID, parentID string) model.File {
	isDir := item.Kind == "drive#folder"
	timeUnix := int64(0)
	if parsed, err := time.Parse(time.RFC3339, item.ModifiedTime); err == nil {
		timeUnix = parsed.Unix()
	}
	if timeUnix == 0 {
		if parsed, err := time.Parse(time.RFC3339, item.CreatedTime); err == nil {
			timeUnix = parsed.Unix()
		}
	}
	f := driveutil.NewFile(driveID, item.ID, parentID, item.Name, isDir, item.Size, timeUnix)
	if !isDir {
		f.Thumbnail = item.Thumbnail
	}
	f.Starred = item.Starred
	f.Ext = item.FileExtension
	f.Category = driveutil.GuessCategory(item.Name)
	f.Description = item.Phase
	if item.WebContentLink != "" {
		f.DownloadURL = item.WebContentLink
	}
	return f
}

func mapFiles(items []File, driveID, parentID string) []model.File {
	out := make([]model.File, 0, len(items))
	for i := range items {
		out = append(out, mapFile(&items[i], driveID, parentID))
	}
	return out
}

func httpMethodPost() string { return "POST" }

// ---- Share Import (importShare capability) ----

// shareInfoResp is the GET /drive/v1/share response.
type shareInfoResp struct {
	ShareStatus   string `json:"share_status"`
	PassCodeToken string `json:"pass_code_token"`
	FileID        string `json:"file_id"`
	ID            string `json:"id"`
}

// shareDetailResp is the GET /drive/v1/share/detail response.
type shareDetailResp struct {
	Files           []File `json:"files"`
	NextPageToken   string `json:"next_page_token"`
	ShareStatus     string `json:"share_status"`
	ShareStatusText string `json:"share_status_text"`
}

// ImportShareSession implements drive.ShareImportDriver.
func (d *Driver) ImportShareSession(ctx context.Context, c drive.Context, shareURL, password string) (*drive.ShareImportSession, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	shareID, passCode := parsePikPakShareURL(shareURL)
	if password != "" {
		passCode = password
	}
	if shareID == "" {
		return nil, errors.New("pikpak: 无效的分享链接")
	}
	// Step 1: GET /drive/v1/share → pass_code_token
	q := url.Values{}
	q.Set("share_id", shareID)
	if passCode != "" {
		q.Set("pass_code", passCode)
	}
	var info shareInfoResp
	if err := cl.get(ctx, "/drive/v1/share", q, &info); err != nil {
		return nil, err
	}
	switch info.ShareStatus {
	case "PASS_CODE_EMPTY":
		return nil, errors.New("该分享需要访问密码")
	case "PASS_CODE_ERROR":
		return nil, errors.New("访问密码错误")
	}
	rootFileID := info.FileID
	if rootFileID == "" {
		rootFileID = info.ID
	}
	if rootFileID == "" {
		rootFileID = "root"
	}
	// Step 2: GET /drive/v1/share/detail → list files
	files, err := pikpakShareListAll(ctx, cl, shareID, info.PassCodeToken, rootFileID)
	if err != nil {
		return nil, err
	}
	return &drive.ShareImportSession{
		Provider:      providerID,
		ShareURL:      shareURL,
		ShareID:       shareID,
		Password:      passCode,
		PassCodeToken: info.PassCodeToken,
		RootFileID:    rootFileID,
		Files:         files,
	}, nil
}

func pikpakShareListAll(ctx context.Context, cl *client, shareID, passCodeToken, parentID string) ([]drive.ShareImportFile, error) {
	var out []drive.ShareImportFile
	pageToken := ""
	seen := map[string]bool{}
	for {
		q := url.Values{}
		q.Set("share_id", shareID)
		q.Set("parent_id", parentID)
		q.Set("pass_code_token", passCodeToken)
		q.Set("thumbnail_size", "SIZE_LARGE")
		q.Set("with_audit", "true")
		q.Set("limit", "100")
		q.Set("filters", `{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`)
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}
		var resp shareDetailResp
		if err := cl.get(ctx, "/drive/v1/share/detail", q, &resp); err != nil {
			return nil, err
		}
		if resp.ShareStatus != "" && resp.ShareStatus != "OK" {
			if resp.ShareStatusText != "" {
				return nil, errors.New(resp.ShareStatusText)
			}
			return nil, fmt.Errorf("pikpak: 分享状态 %s", resp.ShareStatus)
		}
		for i := range resp.Files {
			f := resp.Files[i]
			out = append(out, drive.ShareImportFile{
				FileID: f.ID,
				Name:   f.Name,
				Size:   f.Size,
				IsDir:  strings.Contains(f.Kind, "folder"),
			})
		}
		if resp.NextPageToken == "" {
			break
		}
		if seen[resp.NextPageToken] {
			return nil, errors.New("pikpak: 分享文件分页游标重复")
		}
		seen[resp.NextPageToken] = true
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// SaveShare implements drive.ShareImportDriver.
func (d *Driver) SaveShare(ctx context.Context, c drive.Context, session *drive.ShareImportSession, fileIDs []string, toParentID string) ([]string, error) {
	if session == nil || (session.Provider != "" && session.Provider != providerID) || strings.TrimSpace(session.ShareID) == "" || strings.TrimSpace(session.PassCodeToken) == "" {
		return nil, errors.New("pikpak: 分享会话无效")
	}
	if len(fileIDs) == 0 {
		return nil, errors.New("pikpak: 至少选择一个分享文件")
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	parent := toParentID
	if parent == "" || parent == "root" || parent == RootID || parent == "*" {
		parent = ""
	}
	body := map[string]any{
		"share_id":        session.ShareID,
		"pass_code_token": session.PassCodeToken,
		"file_ids":        fileIDs,
		"parent_id":       parent,
	}
	if err := cl.jsonDo(ctx, http.MethodPost, "/drive/v1/share/restore", body, nil, nil); err != nil {
		return nil, err
	}
	return fileIDs, nil
}

// parsePikPakShareURL extracts share_id from a PikPak share URL.
func parsePikPakShareURL(raw string) (shareID, passCode string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	// url.Parse treats a link without a scheme as a path. Normalize it so
	// `mypikpak.com/s/<id>` behaves the same as the copied HTTPS link.
	normalized := raw
	if !strings.Contains(normalized, "://") && !strings.HasPrefix(normalized, "//") {
		normalized = "https://" + normalized
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "", ""
	}
	query := u.Query()
	for _, key := range []string{"share_id", "shareId", "shareid"} {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			shareID = value
			break
		}
	}
	// https://mypikpak.com/drive/sharing/share/{shareID}?pass_code=xxx
	// https://mypikpak.com/s/{shareID}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		switch strings.ToLower(strings.TrimSpace(parts[i])) {
		case "share", "s":
			if shareID == "" {
				shareID = strings.TrimSpace(parts[i+1])
			}
		}
	}
	for _, key := range []string{"pass_code", "passcode", "pwd", "password"} {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			passCode = value
			break
		}
	}
	return shareID, passCode
}

// OfflineCreate submits a cloud offline task (magnet/link) to PikPak servers.
func (d *Driver) OfflineCreate(ctx context.Context, c drive.Context, url, fileName, parentID string) (taskID, fileID string, err error) {
	cl, err := clientOf(c)
	if err != nil {
		return "", "", err
	}
	return cl.OfflineCreate(ctx, url, fileName, parentID)
}

// OfflineList returns PikPak offline tasks.
func (d *Driver) OfflineList(ctx context.Context, c drive.Context) ([]OfflineTask, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return cl.OfflineList(ctx)
}

// OfflineFind locates one task by task id or file id.
func (d *Driver) OfflineFind(ctx context.Context, c drive.Context, taskID, fileID string) (*OfflineTask, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return cl.FindOfflineTask(ctx, taskID, fileID)
}

// OfflineDelete cancels and removes offline tasks.
func (d *Driver) OfflineDelete(ctx context.Context, c drive.Context, taskIDs []string, deleteFiles bool) error {
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	return cl.OfflineDelete(ctx, taskIDs, deleteFiles)
}
