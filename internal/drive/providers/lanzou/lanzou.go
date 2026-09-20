package lanzou

import (
	"context"
	"errors"
	"fmt"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
)

const providerID = model.ProviderLanzou

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"search":          false,
			"createShare":     true,
			"sharePassword":   false,
			"shareHistory":    true,
			"copy":            false,
			"move":            true,
			"recycleBin":      false,
			"permanentDelete": true,
			"trashView":       false,
		}, nil),
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "cookie", Type: "text", Label: "蓝奏云 Cookie", Required: false, Hint: "粘贴 Cookie 直接登录"},
			{Key: "username", Type: "text", Label: "账号", Required: false},
			{Key: "password", Type: "password", Label: "密码", Required: false},
			{Key: "upload_tier", Type: "select", Label: "会员等级", Options: []drive.LoginOption{
				{Value: "v0", Label: "V0（100 MB）"},
				{Value: "v1", Label: "V1（200 MB）"},
				{Value: "v2", Label: "V2（300 MB）"},
				{Value: "v3", Label: "V3（550 MB）"},
			}},
		}},
		Auth:    authLogin,
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// Driver implements drive.Driver for 蓝奏云.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return LANZOU_ROOT }

// rootID maps root sentinels to LANZOU_ROOT (legacy isDriveProviderRootId).
func rootID(id string) string {
	if id == "" || id == LANZOU_ROOT || id == "root" || id == "/" {
		return LANZOU_ROOT
	}
	return id
}

func isRootSentinel(id string) bool {
	return id == "" || id == LANZOU_ROOT || id == "root" || id == "/" || id == "-1"
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	parent := rootID(dirID)
	parentAPI := "-1"
	if parent != LANZOU_ROOT {
		parentAPI = parent
	}
	items, err := d.fileList(ctx, c, parentAPI)
	if err != nil {
		return nil, err
	}
	out := make([]model.File, 0, len(items))
	for _, it := range items {
		out = append(out, mapLanzouItem(it, c.DriveID, parentAPI))
	}
	return out, nil
}

// GetInfo returns raw provider detail (root pseudo entry or a best-effort stub,
// matching the legacy cache-miss behaviour).
func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if isRootSentinel(fileID) {
		return *rootFile(c), nil
	}
	f, err := d.GetFile(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	return *f, nil
}

// GetFile returns the unified file model. The 蓝奏 API has no detail endpoint
// and the legacy adapter served this from the frontend meta cache only; on a
// cache miss we return the same stub the legacy GetInfo produced.
func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	if isRootSentinel(fileID) {
		return rootFile(c), nil
	}
	f := &model.File{
		DriveID:    c.DriveID,
		FileID:     fileID,
		Name:       fileID,
		NameSearch: fileID,
		IsDir:      false,
		Category:   "file",
	}
	return f, nil
}

func rootFile(c drive.Context) *model.File {
	return &model.File{
		DriveID:    c.DriveID,
		FileID:     LANZOU_ROOT,
		Name:       "蓝奏云",
		NameSearch: "蓝奏云",
		Category:   "folder",
		Icon:       "iconfile-folder",
		IsDir:      true,
	}
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, _ int) (*model.DownloadURL, error) {
	info := d.downloadInfo(ctx, c, fileID, false)
	if info.Error != "" {
		return nil, errors.New(info.Error)
	}
	if info.URL == "" {
		return nil, errors.New("获取下载地址失败")
	}
	return &model.DownloadURL{
		DriveID:      c.DriveID,
		FileID:       fileID,
		ExpireTime:   driveutil.GetExpiresTime(info.URL),
		URL:          info.URL,
		Size:         info.Size,
		Headers:      info.Headers,
		DownloadMode: "proxy",
		Concurrency:  1,
	}, nil
}

// GetVideoPreview reuses the download source as an origin-quality proxy stream.
func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID: c.DriveID, FileID: fileID, Size: u.Size, Headers: u.Headers,
		Qualities: []model.VideoQuality{{
			Quality: "origin", Label: "原画", Value: "origin", URL: u.URL,
			Headers: u.Headers, ForceProxy: true,
		}},
	}, nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	return d.mkdir(ctx, c, parentID, name)
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	return d.renameFile(ctx, c, fileID, name)
}

// Trash: 蓝奏 has no recycle bin; the legacy adapter returned [].
func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	return []string{}, nil
}

// Delete permanently removes files/folders (task 6 / task 3), guessing the
// kind from the ref, the meta cache, or a file-first fallback like the legacy.
func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	var ok []string
	var failed []error
	for _, ref := range refs {
		isDir := false
		known := false
		if ref.IsDir != nil {
			isDir = *ref.IsDir
			known = true
		} else if k, kk := drive.Lookup(c.UserID, c.DriveID, ref.ID); kk {
			isDir = k
			known = true
		}
		if known {
			if err := d.removeItem(ctx, c, ref.ID, isDir); err != nil {
				failed = append(failed, fmt.Errorf("%s: %w", ref.ID, err))
				continue
			}
			ok = append(ok, ref.ID)
			continue
		}
		if err := d.removeItem(ctx, c, ref.ID, false); err != nil {
			if err2 := d.removeItem(ctx, c, ref.ID, true); err2 != nil {
				failed = append(failed, fmt.Errorf("%s: %w", ref.ID, err2))
				continue
			}
		}
		ok = append(ok, ref.ID)
	}
	return ok, errors.Join(failed...)
}

// Move moves files only (蓝奏 cannot move folders; AList parity).
func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	target := rootID(toParentID)
	if target == LANZOU_ROOT {
		target = "-1"
	}
	var ok []string
	for _, ref := range refs {
		isDir := false
		if ref.IsDir != nil {
			isDir = *ref.IsDir
		} else if cachedDir, known := drive.Lookup(c.UserID, c.DriveID, ref.ID); known {
			isDir = cachedDir
		}
		if isDir {
			return nil, errors.New("蓝奏暂不支持移动文件夹")
		}
		if err := d.moveFile(ctx, c, ref.ID, target); err != nil {
			return nil, err
		}
		ok = append(ok, ref.ID)
	}
	return ok, nil
}

// Copy: AList 蓝奏 web 不支持服务端复制（legacy 返回空数组）。
func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	return []string{}, nil
}

// RefreshAccount validates the cookie; for account users an expired cookie is
// re-logged-in with the stored credentials and the token is updated.
func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if token == nil {
		return nil, drive.AuthExpired("蓝奏未登录")
	}
	cr := parseLanzouCred(token.RefreshToken)
	baseURL := LANZOU_DEFAULT.BaseURL
	cookie := token.AccessToken
	if cr != nil {
		if cr.BaseURL != "" {
			baseURL = cr.BaseURL
		}
		if cr.Cookie != "" && (cookie == "" || cookie == "cookie") {
			cookie = cr.Cookie
		}
	}
	uid, _ := lanzouGetVeiAndUid(ctx, cookie, baseURL)
	if uid != "" {
		return token, nil
	}
	if cr == nil || cr.Type != "account" || cr.Account == "" || cr.Password == "" {
		return nil, drive.AuthExpired("蓝奏 Cookie 已失效，请重新登录")
	}
	_, _, _, err := d.reloginAccount(ctx, c, baseURL)
	if err != nil {
		return nil, err
	}
	return token, nil
}
