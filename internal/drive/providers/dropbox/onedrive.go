package dropbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
)

// Driver implements drive.Driver for Dropbox.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return RootID }

func pathOf(ref drive.FileRef) string {
	return normalizeDropboxPath(ref.ID)
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, err := cl.List(ctx, dirID)
	if err != nil {
		return nil, err
	}
	return mapItems(items, c.DriveID, dirID), nil
}

func (d *Driver) ListPaged(ctx context.Context, c drive.Context, dirID, marker string, _ *drive.ListOptions) (*drive.DirPage, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	items, cursor, hasMore, err := cl.ListPage(ctx, dirID, marker)
	if err != nil {
		return nil, err
	}
	next := ""
	if hasMore {
		next = cursor
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
	out := make([]model.File, 0, len(items))
	for i := range items {
		out = append(out, mapItem(&items[i], c.DriveID, parentOf(items[i].PathDisplay)))
	}
	return out, nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if normalizeDropboxPath(fileID) == "" {
		return rootFile(c.DriveID), nil
	}
	return d.GetFile(ctx, c, fileID)
}

func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	m, err := cl.Detail(ctx, fileID)
	if err != nil {
		return nil, err
	}
	f := mapItem(m, c.DriveID, parentOf(m.PathDisplay))
	return &f, nil
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, _ int) (*model.DownloadURL, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	lnk, size, err := cl.TemporaryLink(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID: c.DriveID, FileID: fileID,
		ExpireTime:   timeNow().Add(4 * hour).Unix(),
		URL:          lnk,
		Size:         size,
		DownloadMode: "redirect",
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
			Quality: "origin", Label: "原画", Value: "origin", URL: u.URL,
			Headers: u.Headers, ForceProxy: true,
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
	info, err := cl.Detail(ctx, fileID)
	if err != nil {
		return nil, err
	}
	newID, err := cl.Rename(ctx, fileID, name)
	if err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: newID, ParentFileID: parentOf(info.PathDisplay), Name: name, IsDir: info.Tag == "folder"}, nil
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
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	var ok []string
	var failed []error
	for _, r := range refs {
		if err := cl.Delete(ctx, r.ID); err == nil {
			ok = append(ok, r.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", r.ID, err))
		}
	}
	return ok, errors.Join(failed...)
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
		if err := cl.Copy(ctx, r.ID, toParentID); err == nil {
			ok = append(ok, r.ID)
		} else {
			failed = append(failed, fmt.Errorf("%s: %w", r.ID, err))
		}
	}
	return ok, errors.Join(failed...)
}

func (d *Driver) CreateShare(ctx context.Context, c drive.Context, params drive.ShareParams) (*model.ShareItem, error) {
	if len(params.FileIDs) != 1 {
		return nil, errors.New("Dropbox 分享链接一次只能选择一个文件或文件夹")
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	item, err := cl.CreateSharedLink(ctx, params.FileIDs[0], params.Expiration, params.Password)
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

// CancelShare revokes the remote Dropbox link instead of only forgetting the
// local record.
func (d *Driver) CancelShare(ctx context.Context, c drive.Context, share model.ShareHistoryEntry) error {
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	return cl.RevokeSharedLink(ctx, share.ShareURL)
}

// UploadOneFile uploads one file.
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || ui.Info.LocalFilePath == "" {
		return errors.New("Dropbox: 上传文件路径为空")
	}
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	path := normalizeDropboxPath(ui.Info.ParentFileID)
	if path == "" || path == RootID {
		path = ""
	} else {
		path = stringsTrimRight(path, "/")
	}
	target := "/" + ui.Info.Name
	if path != "" {
		target = path + "/" + ui.Info.Name
	}
	policy := uploadPolicy{mode: "overwrite"}
	switch driveutil.ResolveConflictPolicy(ui.Info.ConflictPolicy) {
	case driveutil.ConflictRefuse:
		policy = uploadPolicy{mode: "add", strictConflict: true}
	case driveutil.ConflictRename:
		policy = uploadPolicy{mode: "add", autorename: true}
	case driveutil.ConflictSkip:
		if _, err := cl.Detail(ctx, target); err == nil {
			return nil
		} else if !strings.Contains(strings.ToLower(err.Error()), "not_found") {
			return err
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
	if size <= uploadSingleLimit {
		fileID, err := cl.UploadSmall(ctx, target, f, size, policy)
		if err != nil {
			return err
		}
		ui.Upload.FileID = fileID
		return nil
	}
	return cl.UploadSession(ctx, c, f, target, size, ui, policy)
}

func mapItems(items []Metadata, driveID, parentID string) []model.File {
	out := make([]model.File, 0, len(items))
	for i := range items {
		out = append(out, mapItem(&items[i], driveID, parentID))
	}
	return out
}

func rootFile(driveID string) model.File {
	return model.File{DriveID: driveID, FileID: RootID, Name: "Dropbox", NameSearch: "dropbox", Category: "folder", IsDir: true, Icon: "iconfile-folder"}
}

func stringsTrimRight(s, cut string) string {
	for stringsHasSuffix(s, cut) && len(s) > 1 {
		s = s[:len(s)-len(cut)]
	}
	return s
}

func stringsHasSuffix(s, cut string) bool {
	return len(s) >= len(cut) && s[len(s)-len(cut):] == cut
}
