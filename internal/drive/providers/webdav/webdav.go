// Package webdav implements the WebDAV drive provider (mounted storage).
package webdav

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	wc "mnemo-go/internal/provider/webdav"
)

const providerID = model.ProviderWebdav

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"mountedStorage":  true,
			"permanentDelete": true,
			"recycleBin":      false,
			"trashView":       false,
		}, func(c *drive.Capabilities) {
			c.SetUploadMode(drive.UploadModeDirect)
		}),
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// Driver implements drive.Driver for WebDAV. File ids are server paths.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return "/" }

func (d *Driver) ValidateConnection(ctx context.Context, conn *model.ConnConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client, err := wc.New(conn, 60*time.Second)
	if err != nil {
		return err
	}
	return client.CheckCollection(ctx)
}

func connAllowsPrivateNetwork(c drive.Context) bool {
	return c.Token != nil && c.Token.Conn != nil && c.Token.Conn.AllowPrivateNetwork
}

// RefreshAccount reads the optional RFC 4331 quota properties. Servers that
// do not implement quota discovery keep their capacity as unknown without
// triggering additional fallback scans.
func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if token == nil || token.Conn == nil {
		return nil, errors.New("webdav: 连接不存在，请重新连接")
	}
	client, err := clientOf(c)
	if err != nil {
		return token, err
	}
	used, total, err := client.Quota(ctx, "/")
	if err != nil {
		return token, err
	}
	if total > 0 {
		token.UsedSize = used
		token.TotalSize = total
		token.FreeSize = total - used
	}
	return token, nil
}

func clientOf(c drive.Context) (*wc.Client, error) {
	if c.Token == nil || c.Token.Conn == nil {
		return nil, errors.New("webdav: 连接不存在，请重新连接")
	}
	return wc.New(c.Token.Conn, 60*time.Second)
}

// pathOf normalizes a file id to a server path.
func pathOf(id string) string {
	if id == "" || id == "/" || id == "root" {
		return "/"
	}
	if !strings.HasPrefix(id, "/") {
		return "/" + id
	}
	return id
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	entries, err := client.List(ctx, pathOf(dirID))
	if err != nil {
		return nil, err
	}
	out := make([]model.File, 0, len(entries))
	for _, e := range entries {
		f := driveutil.NewFile(c.DriveID, e.Path, pathOf(dirID), e.Name, e.IsDir, e.Size, e.Modified.Unix())
		out = append(out, f)
	}
	return out, nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	p := pathOf(fileID)
	if p == "/" {
		name := "WebDAV"
		if c.Token != nil && c.Token.Conn != nil && c.Token.Conn.Name != "" {
			name = c.Token.Conn.Name
		}
		return driveutil.NewFile(c.DriveID, "/", "", name, true, 0, 0), nil
	}
	entry, err := client.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	f := driveutil.NewFile(c.DriveID, entry.Path, parentPath(entry.Path), entry.Name, entry.IsDir, entry.Size, entry.Modified.Unix())
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
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	p := pathOf(fileID)
	entry, err := client.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	if entry.IsDir {
		return nil, errors.New("文件夹不能直接下载")
	}
	downloadURL, err := client.DownloadURL(p)
	if err != nil {
		return nil, err
	}
	headers, requestAuth, err := client.DownloadAuth()
	if err != nil {
		return nil, err
	}
	mode, concurrency := "", 0
	if requestAuth != nil {
		// Digest nonce counts are request-specific. Only Digest needs ordered
		// requests; other authentication modes use the user's concurrency.
		mode, concurrency = "proxy", 1
	}
	return &model.DownloadURL{
		DriveID:             c.DriveID,
		FileID:              fileID,
		URL:                 downloadURL,
		Size:                entry.Size,
		Headers:             headers,
		RequestAuth:         requestAuth,
		DownloadMode:        mode,
		Concurrency:         concurrency,
		AllowPrivateNetwork: connAllowsPrivateNetwork(c),
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID:             c.DriveID,
		FileID:              fileID,
		Size:                u.Size,
		Headers:             u.Headers,
		RequestAuth:         u.RequestAuth,
		AllowPrivateNetwork: u.AllowPrivateNetwork,
		Qualities: []model.VideoQuality{{
			Quality: "origin", Label: "原画", Value: "origin",
			URL: u.URL, Headers: u.Headers, RequestAuth: u.RequestAuth, ForceProxy: true,
		}},
	}, nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	target := driveutil.JoinPath(pathOf(parentID), name)
	if err := client.Mkcol(ctx, target); err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	return &drive.MkdirResult{FileID: target}, nil
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	p := pathOf(fileID)
	info, err := client.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	target := driveutil.JoinPath(parentPath(p), name)
	if err := client.Move(ctx, p, target); err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: target, ParentFileID: parentPath(target), Name: name, IsDir: info.IsDir}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	// WebDAV has no recycle bin; permanent delete directly.
	return d.Delete(ctx, c, idsToRefs(fileIDs))
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, ref := range refs {
		if err := client.Delete(ctx, pathOf(ref.ID)); err == nil {
			ok = append(ok, ref.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", ref.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	return nil, drive.NotSupported("restore")
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	targetParent := pathOf(toParentID)
	var ok []string
	var failed []error
	for _, ref := range refs {
		source := pathOf(ref.ID)
		if davPathContains(source, targetParent) {
			failed = append(failed, fmt.Errorf("%s: webdav: 不能将目录移动到自身或子目录", ref.ID))
			continue
		}
		base := ref.ID
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		to := driveutil.JoinPath(targetParent, base)
		if err := client.Move(ctx, source, to); err == nil {
			ok = append(ok, ref.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", ref.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	client, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	targetParent := pathOf(toParentID)
	var ok []string
	var failed []error
	for _, ref := range refs {
		source := pathOf(ref.ID)
		if davPathContains(source, targetParent) {
			failed = append(failed, fmt.Errorf("%s: webdav: 不能将目录复制到自身或子目录", ref.ID))
			continue
		}
		base := ref.ID
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		to := driveutil.JoinPath(targetParent, base)
		if err := client.Copy(ctx, source, to); err == nil {
			ok = append(ok, ref.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", ref.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

// UploadOneFile performs a direct PUT upload. It honors ui.Info.ConflictPolicy
// when the target path already exists: refuse returns an error, rename uploads
// to a generated non-conflicting name, overwrite (the default) replaces it.
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || ui.Info.LocalFilePath == "" {
		return errors.New("webdav: 上传文件路径为空")
	}
	if err := validateName(ui.Info.Name); err != nil {
		return err
	}
	client, err := clientOf(c)
	if err != nil {
		return err
	}
	target := driveutil.JoinPath(pathOf(ui.Info.ParentFileID), ui.Info.Name)

	switch driveutil.ResolveConflictPolicy(ui.Info.ConflictPolicy) {
	case driveutil.ConflictRefuse:
		_, e := client.Stat(ctx, target)
		if e == nil {
			return errors.New("webdav: 目标文件已存在")
		}
		if !isNotFound(e) {
			return fmt.Errorf("webdav: 检查目标文件失败: %w", e)
		}
	case driveutil.ConflictRename:
		_, e := client.Stat(ctx, target)
		if e == nil {
			newName := driveutil.GenerateConflictName(ui.Info.Name)
			ui.Info.Name = newName
			target = driveutil.JoinPath(pathOf(ui.Info.ParentFileID), newName)
		} else if !isNotFound(e) {
			return fmt.Errorf("webdav: 检查目标文件失败: %w", e)
		}
	case driveutil.ConflictSkip:
		_, e := client.Stat(ctx, target)
		if e == nil {
			return nil
		}
		if !isNotFound(e) {
			return fmt.Errorf("webdav: 检查目标文件失败: %w", e)
		}
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
	// report read progress back to the upload UI
	pr := driveutil.NewProgressOnlyReader(f, size, func(read int64) {
		ui.ReportUploadProgress(read, size)
	})
	return client.Put(ctx, target, pr, size)
}

func idsToRefs(ids []string) []drive.FileRef {
	refs := make([]drive.FileRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, drive.FileRef{ID: id})
	}
	return refs
}

func parentPath(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

func davPathContains(source, targetParent string) bool {
	source = strings.TrimRight(pathOf(source), "/")
	targetParent = strings.TrimRight(pathOf(targetParent), "/")
	if source == "" {
		return false
	}
	if targetParent == "" {
		targetParent = "/"
	}
	return targetParent == source || strings.HasPrefix(targetParent, source+"/")
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, " 404")
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." {
		return errors.New("webdav: 名称为空或无效")
	}
	if strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errors.New("webdav: 名称不能包含路径分隔符或控制字符")
	}
	return nil
}
