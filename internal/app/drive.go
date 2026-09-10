package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/providers/pikpak"
	"mnemo-go/internal/logging"
	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
)

// ---- drive file operations (thin pass-through to the drive facade) ----

// SaveCloudTextFile writes text content to an isolated temporary file and
// waits until the corresponding remote upload reaches a terminal state. The
// ordinary upload queue remains asynchronous; only this user-facing save flow
// needs a definitive success/failure result.
func (a *App) SaveCloudTextFile(userID, driveID, parentID, fileName, content string) error {
	logging.Info("cloud text save requested", "account_id", redactID(userID), "drive_id", redactID(driveID), "file_name", fileName, "content_bytes", len(content))
	name := filepath.Base(strings.TrimSpace(fileName))
	if name == "." || name == ".." || name == "" || name == string(filepath.Separator) {
		return fmt.Errorf("文件名无效")
	}
	tmpPath, err := writeCloudTextUploadTemp(name, content)
	if err != nil {
		return err
	}
	uploads := a.uploadQueue()
	if uploads == nil {
		removeCloudTextUploadTemp(tmpPath)
		return fmt.Errorf("上传服务未启动")
	}
	parentID = canonicalUploadParent(userID, driveID, parentID)
	created := uploads.AddFiles(userID, driveID, parentID, "overwrite", []string{tmpPath})
	if len(created) == 0 {
		removeCloudTextUploadTemp(tmpPath)
		return fmt.Errorf("文件未能加入上传队列")
	}
	if !uploads.MarkCleanupOnSuccess(created[0].UploadID, tmpPath) {
		logging.Warn("cloud text temp cleanup marker could not be attached", "job_id", created[0].UploadID)
	}
	jobID := created[0].UploadID
	if _, waitErr := uploads.Wait(jobID, 30*time.Minute); waitErr != nil {
		logging.Warn("cloud text save failed", "job_id", jobID, "file_name", name, "error", waitErr)
		return fmt.Errorf("云端保存失败: %w", waitErr)
	}
	logging.Info("cloud text save completed", "job_id", jobID, "file_name", name)
	return nil
}

func writeCloudTextUploadTemp(fileName, content string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "mnemo_edit_")
	if err != nil {
		return "", fmt.Errorf("创建编辑临时目录失败: %w", err)
	}
	tmpPath := filepath.Join(tmpDir, fileName)
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		_ = os.Remove(tmpDir)
		return "", fmt.Errorf("写入编辑临时文件失败: %w", err)
	}
	return tmpPath, nil
}

func removeCloudTextUploadTemp(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(filepath.Dir(path))
}

// ListDir lists a directory.
func (a *App) ListDir(userID, driveID, dirID string) ([]model.File, error) {
	started := time.Now()
	logging.Debug("directory listing started", "account_id", redactID(userID), "drive_id", redactID(driveID), "directory_id", redactID(dirID))
	files, err := drive.ListDir(userID, driveID, dirID, nil)
	if err == nil {
		drive.RememberListedFiles(userID, driveID, dirID, files)
		logging.Debug("directory listing completed", "count", len(files), "duration", logging.Duration(started))
	} else {
		logging.Warn("directory listing failed", "provider", drive.ProviderOf(userID, driveID, ""), "error", err, "duration", logging.Duration(started))
	}
	return files, err
}

// ListDirPage lists one page.
func (a *App) ListDirPage(userID, driveID, dirID, marker string) (*drive.DirPage, error) {
	return drive.ListDirPage(userID, driveID, dirID, marker, nil)
}

// SearchFiles searches a drive.
func (a *App) SearchFiles(userID, driveID, keyword string) ([]model.File, error) {
	logging.Debug("file search started", "account_id", redactID(userID), "drive_id", redactID(driveID), "keyword_length", len(keyword))
	files, err := drive.SearchDir(userID, driveID, keyword)
	if err != nil {
		logging.Warn("file search failed", "error", err)
	} else {
		logging.Debug("file search completed", "count", len(files))
	}
	return files, err
}

// ListTrash lists the recycle bin.
func (a *App) ListTrash(userID, driveID string) ([]model.File, error) {
	return drive.ListTrash(userID, driveID, nil)
}

// GetFileDetail returns a file's unified model.
func (a *App) GetFileDetail(userID, driveID, fileID string) (*model.File, error) {
	return drive.GetFile(userID, driveID, fileID)
}

// GetDownloadURL returns a file's download url.
func (a *App) GetDownloadURL(userID, driveID, fileID string) (*model.DownloadURL, error) {
	logging.Debug("download URL resolution started", "account_id", redactID(userID), "file_id", redactID(fileID))
	result, err := drive.GetDownloadURL(userID, driveID, fileID, 14400)
	if err != nil {
		logging.Warn("download URL resolution failed", "error", err)
	} else if result != nil {
		logging.Debug("download URL resolved", "expire_time", result.ExpireTime)
	}
	return result, err
}

// GetVideoPreview returns playback sources.
func (a *App) GetVideoPreview(userID, driveID, fileID string) (*model.VideoPreview, error) {
	logging.Debug("video preview resolution started", "account_id", redactID(userID), "file_id", redactID(fileID))
	preview, err := drive.GetVideoPreview(userID, driveID, fileID)
	if err != nil {
		logging.Warn("video preview resolution failed", "error", err)
	} else if preview != nil {
		logging.Debug("video preview resolved", "quality_count", len(preview.Qualities))
	}
	return preview, err
}

// Mkdir creates a folder.
func (a *App) Mkdir(userID, driveID, parentID, name string) (*drive.MkdirResult, error) {
	return drive.Mkdir(userID, driveID, parentID, name)
}

// RenameFile renames a file.
func (a *App) RenameFile(userID, driveID, fileID, name string) (*drive.RenameResult, error) {
	res, err := drive.RenameBatch(userID, driveID, []drive.FileRef{{ID: fileID}}, []string{name})
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return &drive.RenameResult{FileID: fileID, Name: name}, nil
	}
	return &res[0], nil
}

// RenameBatch renames files in batch.
func (a *App) RenameBatch(userID, driveID string, fileRefs []drive.FileRef, names []string) ([]drive.RenameResult, error) {
	return drive.RenameBatch(userID, driveID, fileRefs, names)
}

// TrashFiles moves files to the recycle bin.
func (a *App) TrashFiles(userID, driveID string, fileIDs []string) ([]string, error) {
	return drive.TrashBatch(userID, driveID, fileIDs)
}

// DeleteFiles permanently deletes files.
func (a *App) DeleteFiles(userID, driveID string, fileIDs []string) ([]string, error) {
	refs := make([]drive.FileRef, 0, len(fileIDs))
	for _, id := range fileIDs {
		refs = append(refs, drive.FileRef{ID: id})
	}
	return drive.DeleteBatch(userID, driveID, refs)
}

// RestoreFiles restores files from trash.
func (a *App) RestoreFiles(userID, driveID string, fileIDs []string) ([]string, error) {
	return drive.RestoreBatch(userID, driveID, fileIDs)
}

// MoveFiles moves files to a folder.
func (a *App) MoveFiles(userID, driveID string, fileIDs []string, toParentID string) ([]string, error) {
	refs := make([]drive.FileRef, 0, len(fileIDs))
	for _, id := range fileIDs {
		refs = append(refs, drive.FileRef{ID: id})
	}
	return drive.MoveBatch(userID, driveID, refs, toParentID, "")
}

// CopyFiles copies files into a folder.
func (a *App) CopyFiles(userID, driveID string, fileIDs []string, toParentID string) ([]string, error) {
	refs := make([]drive.FileRef, 0, len(fileIDs))
	for _, id := range fileIDs {
		refs = append(refs, drive.FileRef{ID: id})
	}
	return drive.CopyBatch(userID, driveID, refs, toParentID, "")
}

// FavoriteFiles toggles favorite on files.
func (a *App) FavoriteFiles(userID, driveID string, favorite bool, fileIDs []string) ([]string, error) {
	return drive.FavoriteBatch(userID, driveID, favorite, fileIDs)
}

// CreateShare creates a share.
func (a *App) CreateShare(userID, driveID string, params drive.ShareParams) (*model.ShareItem, error) {
	logging.Info("share creation started", "account_id", redactID(userID), "file_count", len(params.FileIDs), "expiration", params.Expiration, "has_password", strings.TrimSpace(params.Password) != "")
	item, err := drive.CreateShare(userID, driveID, params)
	if err != nil {
		logging.Warn("share creation failed", "error", err)
	}
	if err == nil && shouldPersistShareHistory(item) {
		st, storeErr := a.storeOrError()
		if storeErr != nil {
			a.emit("share:history-error", map[string]string{"error": storeErr.Error()})
			return item, nil
		}
		if storeErr = st.SaveShareHistory(model.ShareHistoryEntry{
			ShareID: item.ShareID, AccountID: userID, DriveID: driveID,
			FileID: firstFileID(params.FileIDs), ShareURL: item.ShareURL,
			SharePwd: item.SharePwd, ShareName: item.ShareName, Provider: drive.ProviderOf(userID, driveID, ""),
		}); storeErr != nil {
			a.emit("share:history-error", map[string]string{"error": storeErr.Error()})
		}
	}
	if err == nil && item != nil {
		logging.Info("share creation completed", "share_id", redactID(item.ShareID))
	}
	return item, err
}

// CancelShare revokes a share on the provider before removing its local
// history item. It never presents a local-only deletion as a cloud revocation.
func (a *App) CancelShare(entry model.ShareHistoryEntry) error {
	userID := strings.TrimSpace(entry.AccountID)
	driveID := strings.TrimSpace(entry.DriveID)
	if userID == "" || driveID == "" {
		return fmt.Errorf("分享记录缺少账号信息，无法取消")
	}
	if err := validateShareRecordProvider(entry, drive.ProviderOf(userID, driveID, "")); err != nil {
		return err
	}
	logging.Info("share cancellation started", "account_id", redactID(userID), "share_id", redactID(entry.ShareID))
	if err := drive.CancelShare(userID, driveID, entry); err != nil {
		logging.Warn("share cancellation failed", "account_id", redactID(userID), "share_id", redactID(entry.ShareID), "error", err)
		return err
	}
	st, err := a.storeOrError()
	if err != nil {
		return fmt.Errorf("云端分享已取消，但无法清理本地记录: %w", err)
	}
	if err := st.DeleteShareHistory(userID, entry.ShareID, entry.ShareURL); err != nil {
		return fmt.Errorf("云端分享已取消，但本地记录清理失败: %w", err)
	}
	a.emit("share:history-changed", map[string]string{"account_id": userID, "share_id": entry.ShareID})
	logging.Info("share cancellation completed", "account_id", redactID(userID), "share_id", redactID(entry.ShareID))
	return nil
}

// validateShareRecordProvider rejects a tampered or stale history entry before
// it can be sent to a different provider's destructive cancellation endpoint.
// Empty Provider is retained for history records created by older versions.
func validateShareRecordProvider(entry model.ShareHistoryEntry, accountProvider string) error {
	recorded := strings.TrimSpace(entry.Provider)
	if recorded == "" || recorded == accountProvider {
		return nil
	}
	return fmt.Errorf("分享记录与账号网盘不匹配，无法取消")
}

// shouldPersistShareHistory excludes expiring bearer-style URLs. S3
// presigned links contain an access signature and become stale on expiry, so
// keeping them in the permanent local share log is both misleading and an
// unnecessary credential exposure.
func shouldPersistShareHistory(item *model.ShareItem) bool {
	return item != nil && strings.TrimSpace(item.SharePolicy) != "presigned"
}

// ListShareHistory lists persisted share history.
func (a *App) ListShareHistory(userID string) []model.ShareHistoryEntry {
	st, err := a.storeOrError()
	if err != nil {
		return nil
	}
	list, err := st.ListShareHistory(userID)
	if err != nil {
		return nil
	}
	return list
}

// ImportShare parses a share link and returns the file listing + session
// state for subsequent save. The session must be passed back to
// SaveImportedShare to transfer selected files.
func (a *App) ImportShare(userID, driveID, shareURL, password string) (*drive.ShareImportSession, error) {
	logging.Info("share import started", "account_id", redactID(userID), "url_host", urlHost(shareURL), "has_password", strings.TrimSpace(password) != "")
	session, err := drive.ImportShare(userID, driveID, shareURL, password)
	if err != nil {
		logging.Warn("share import failed", "error", err)
	} else {
		logging.Info("share import completed", "file_count", len(session.Files))
	}
	return session, err
}

// SaveImportedShare transfers selected files from a parsed share session
// into the account's folder toParentID.
func (a *App) SaveImportedShare(userID, driveID string, session *drive.ShareImportSession, fileIDs []string, toParentID string) ([]string, error) {
	return drive.SaveImportedShare(userID, driveID, session, fileIDs, toParentID)
}

// ---- local tags & favorites ----

// ListLocalTags lists local tags for an account.
func (a *App) ListLocalTags(userID, driveID string) []store.LocalTag {
	st, err := a.storeOrError()
	if err != nil {
		return nil
	}
	list, err := st.ListLocalTags(userID, driveID)
	if err != nil {
		return nil
	}
	return list
}

// SaveLocalTag upserts a local tag.
func (a *App) SaveLocalTag(t store.LocalTag) error {
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	return st.UpsertLocalTag(t)
}

// DeleteLocalTag removes a local tag.
func (a *App) DeleteLocalTag(userID, driveID, fileID string) error {
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	return st.DeleteLocalTag(userID, driveID, fileID)
}

func (a *App) lockFavorites(userID, driveID string) func() {
	value, _ := a.favoriteLocks.LoadOrStore(userID+"\x00"+driveID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ListFavorites merges native favorites with pre-existing local bookmarks.
func (a *App) ListFavorites(userID, driveID string) ([]store.Favorite, error) {
	// Preference backups read the existing local snapshot, including accounts
	// that are currently signed out. Do not make cloud requests for this call.
	if userID == "" && driveID == "" {
		st, err := a.storeOrError()
		if err != nil {
			return nil, err
		}
		return st.ListFavorites("", "")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(driveID) == "" {
		return nil, fmt.Errorf("收藏缺少账号或空间")
	}
	defer a.lockFavorites(userID, driveID)()
	st, err := a.storeOrError()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	files, supported, err := drive.ListRemoteFavorites(ctx, userID, driveID)
	if err != nil {
		return nil, err
	}
	if supported {
		remote := make([]store.Favorite, 0, len(files))
		seen := map[string]bool{}
		for _, f := range files {
			if f.FileID == "" || seen[f.FileID] {
				return nil, fmt.Errorf("云端收藏列表包含空白或重复文件 ID")
			}
			seen[f.FileID] = true
			f.DriveID, f.Starred = driveID, true
			f.DownloadURL, f.Thumbnail = "", "" // never persist expiring upstream access links
			remote = append(remote, store.Favorite{UserID: userID, DriveID: driveID, FileID: f.FileID, Name: f.Name, IsDir: f.IsDir, Source: "cloud", File: &f})
		}
		if err := st.ReplaceRemoteFavorites(userID, driveID, remote); err != nil {
			return nil, err
		}
	}
	return st.ListFavorites(userID, driveID)
}

// AddFavorite adds a favorite.
func (a *App) AddFavorite(userID, driveID string, f store.Favorite) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(driveID) == "" || strings.TrimSpace(f.FileID) == "" {
		return fmt.Errorf("收藏缺少账号、空间或文件 ID")
	}
	defer a.lockFavorites(userID, driveID)()
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cloud, err := drive.SetRemoteFavorite(ctx, userID, driveID, true, f.FileID)
	if err != nil {
		return err
	}
	f.UserID, f.DriveID, f.Source = userID, driveID, "local"
	if cloud {
		f.Source = "cloud"
	}
	f = normalizeFavorite(f)
	if err := st.AddFavorite(f); err != nil {
		return fmt.Errorf("保存收藏记录失败：%w", err)
	}
	a.emit("favorites:changed", map[string]string{"user_id": userID, "drive_id": driveID})
	return nil
}

// RestoreFavorite merges a backup as a local bookmark. Importing preferences
// must not replay cloud mutations or require credentials for removed accounts.
func (a *App) RestoreFavorite(f store.Favorite) error {
	if strings.TrimSpace(f.UserID) == "" || strings.TrimSpace(f.DriveID) == "" || strings.TrimSpace(f.FileID) == "" {
		return fmt.Errorf("收藏缺少账号、空间或文件 ID")
	}
	defer a.lockFavorites(f.UserID, f.DriveID)()
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	list, err := st.ListFavorites(f.UserID, f.DriveID)
	if err != nil {
		return err
	}
	for _, existing := range list {
		if existing.FileID == f.FileID {
			return nil
		}
	}
	f.Source = "local"
	if err := st.AddFavorite(normalizeFavorite(f)); err != nil {
		return err
	}
	a.emit("favorites:changed", map[string]string{"user_id": f.UserID, "drive_id": f.DriveID})
	return nil
}

func normalizeFavorite(f store.Favorite) store.Favorite {
	if f.File != nil {
		file := *f.File
		file.DriveID, file.FileID = f.DriveID, f.FileID
		file.Name, file.IsDir, file.Starred = f.Name, f.IsDir, true
		file.DownloadURL, file.Thumbnail = "", ""
		f.File = &file
	}
	return f
}

// RemoveFavorite removes a favorite.
func (a *App) RemoveFavorite(userID, driveID, fileID string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(driveID) == "" || strings.TrimSpace(fileID) == "" {
		return fmt.Errorf("收藏缺少账号、空间或文件 ID")
	}
	defer a.lockFavorites(userID, driveID)()
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	list, err := st.ListFavorites(userID, driveID)
	if err != nil {
		return err
	}
	local := false
	for _, f := range list {
		if f.FileID == fileID {
			local = f.Source != "cloud"
			break
		}
	}
	if !local {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := drive.SetRemoteFavorite(ctx, userID, driveID, false, fileID); err != nil {
			return err
		}
	}
	if err := st.RemoveFavorite(userID, driveID, fileID); err != nil {
		return err
	}
	a.emit("favorites:changed", map[string]string{"user_id": userID, "drive_id": driveID})
	return nil
}

// ---- offline download (PikPak cloud) ----

// OfflineDownload submits a PikPak cloud offline task.
func (a *App) OfflineDownload(userID, driveID, url, fileName string) (*model.OfflineTask, error) {
	return a.offlineCreate(userID, driveID, url, fileName)
}

// offlineCreate creates an offline task through the pikpak driver.
func (a *App) offlineCreate(userID, driveID, url, fileName string) (*model.OfflineTask, error) {
	ctx := context.Background()
	c, err := drive.BuildContext(userID, driveID, "")
	if err != nil {
		return nil, err
	}
	d, err := drive.DriverFor(c)
	if err != nil {
		return nil, err
	}
	type offlineDriver interface {
		OfflineCreate(ctx context.Context, url, fileName, parentID string) (taskID, fileID string, err error)
	}
	od, ok := d.(offlineDriver)
	if !ok {
		return nil, fmt.Errorf("当前网盘不支持云离线")
	}
	taskID, fileID, err := od.OfflineCreate(ctx, url, fileName, "")
	if err != nil {
		return nil, err
	}
	localID := taskID
	if localID == "" {
		localID = fileID
	}
	t := &model.OfflineTask{
		ID: localID, UserID: userID, DriveID: driveID,
		TaskID: taskID, FileID: fileID, URL: url, FileName: fileName,
		Status: "running",
	}
	if t.ID == "" {
		return nil, fmt.Errorf("云离线任务响应缺少任务或文件 ID")
	}
	st, err := a.storeOrError()
	if err != nil {
		return nil, err
	}
	if err := st.SaveOfflineTask(t); err != nil {
		return nil, err
	}
	return t, nil
}

// ListOfflineTasks lists offline tasks.
func (a *App) ListOfflineTasks(userID string) []model.OfflineTask {
	st, err := a.storeOrError()
	if err != nil {
		return nil
	}
	list, err := st.ListOfflineTasks()
	if err != nil {
		return nil
	}
	var out []model.OfflineTask
	for _, t := range list {
		if userID == "" || t.UserID == userID {
			out = append(out, t)
		}
	}
	return out
}

// RefreshOfflineTasks queries the provider for live offline task status and
// updates the local cache. Returns the refreshed list.
func (a *App) RefreshOfflineTasks(userID, driveID string) ([]model.OfflineTask, error) {
	ctx := context.Background()
	c, err := drive.BuildContext(userID, driveID, "")
	if err != nil {
		return nil, err
	}
	d, err := drive.DriverFor(c)
	if err != nil {
		return nil, err
	}
	type lister interface {
		OfflineList(ctx context.Context, c drive.Context) ([]pikpak.OfflineTask, error)
	}
	l, ok := d.(lister)
	if !ok {
		return nil, fmt.Errorf("当前网盘不支持云离线")
	}
	tasks, err := l.OfflineList(ctx, c)
	if err != nil {
		return nil, err
	}
	st, err := a.storeOrError()
	if err != nil {
		return nil, err
	}
	var out []model.OfflineTask
	for _, t := range tasks {
		localID := t.TaskID
		if localID == "" {
			localID = t.FileID
		}
		ot := model.OfflineTask{
			ID: localID, UserID: userID, DriveID: driveID,
			TaskID: t.TaskID, FileID: t.FileID, URL: t.URL, FileName: t.Name,
			Status: firstNonEmptyOfflineStatus(t.Phase, t.Status), Progress: t.Progress,
			Message: t.Message, FileSize: t.FileSize,
			CreatedTime: t.CreatedTime, UpdatedTime: t.UpdatedTime,
		}
		if ot.ID == "" {
			continue
		}
		if err := st.SaveOfflineTask(&ot); err != nil {
			return nil, err
		}
		out = append(out, ot)
	}
	return out, nil
}

func firstNonEmptyOfflineStatus(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

// DeleteOfflineTask cancels and removes a PikPak cloud offline task.
func (a *App) DeleteOfflineTask(userID, driveID, taskID string, deleteFiles bool) error {
	ctx := context.Background()
	c, err := drive.BuildContext(userID, driveID, "")
	if err != nil {
		return err
	}
	d, err := drive.DriverFor(c)
	if err != nil {
		return err
	}
	type deleter interface {
		OfflineDelete(ctx context.Context, c drive.Context, taskIDs []string, deleteFiles bool) error
	}
	del, ok := d.(deleter)
	if !ok {
		return fmt.Errorf("当前网盘不支持删除云离线任务")
	}
	if err := del.OfflineDelete(ctx, c, []string{taskID}, deleteFiles); err != nil {
		return err
	}
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	return st.DeleteOfflineTask(taskID)
}

func firstFileID(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

var _ = context.Background

// SendGuangyaSms requests a SMS verification code (guangya provider).
func (a *App) SendGuangyaSms(phone string) (map[string]string, error) {
	if !drive.IsRegistered(model.ProviderGuangya) {
		return nil, fmt.Errorf("光鸭云盘未注册")
	}
	type sender interface {
		SendSms(ctx context.Context, phone string) (verificationID, deviceID, captchaToken string, err error)
	}
	d := drive.New(model.ProviderGuangya)
	if s, ok := d.(sender); ok {
		vid, dev, capTok, err := s.SendSms(context.Background(), phone)
		if err != nil {
			return nil, err
		}
		return map[string]string{"verification_id": vid, "device_id": dev, "captcha_token": capTok}, nil
	}
	return nil, fmt.Errorf("光鸭云盘不支持发送验证码")
}
