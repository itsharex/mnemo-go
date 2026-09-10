package pan189

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"mnemo-go/internal/drive"
)

// fileRefItem is one entry of a batch task (AList sends name + isFolder).
type fileRefItem struct {
	FileID   string `json:"fileId"`
	FileName string `json:"fileName"`
	IsFolder int    `json:"isFolder"`
}

// batchRefs keeps the name and kind from the account-scoped listing snapshot.
// Explicit caller kinds take precedence over the cache.
func batchRefs(c drive.Context, refs []drive.FileRef) []fileRefItem {
	out := make([]fileRefItem, 0, len(refs))
	for _, r := range refs {
		isDir := 0
		name := r.ID
		if cached, ok := drive.CachedFile(c.UserID, c.DriveID, r.ID); ok {
			name = cached.Name
			if cached.IsDir {
				isDir = 1
			}
		}
		if r.IsDir != nil {
			isDir = 0
			if *r.IsDir {
				isDir = 1
			}
		}
		out = append(out, fileRefItem{FileID: r.ID, FileName: name, IsFolder: isDir})
	}
	return out
}

// createBatchTask starts an async batch task (MOVE/COPY/DELETE/CLEAR_RECYCLE).
func (d *Driver) createBatchTask(ctx context.Context, c drive.Context, taskType, targetFolderID string, items []fileRefItem, other map[string]string) (string, error) {
	sess, err := sessionOf(c.Token)
	if err != nil {
		return "", err
	}
	isFamily, familyID := cloudInfo(sess)
	form := map[string]string{"type": taskType}
	if b, err := json.Marshal(items); err == nil {
		form["taskInfos"] = string(b)
	}
	for k, v := range other {
		form[k] = v
	}
	if targetFolderID != "" {
		form["targetFolderId"] = targetFolderID
	}
	if isFamily {
		form["familyId"] = familyID
	}
	raw, err := d.request(ctx, c, apiURL+"/batch/createBatchTask.action", reqOptions{method: "POST", form: form})
	if err != nil {
		return "", err
	}
	var res struct {
		TaskID listEntryID `json:"taskId"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	if res.TaskID == "" {
		return "", errors.New("天翼批量操作未返回任务 ID")
	}
	return string(res.TaskID), nil
}

// waitBatchTask polls the task status: 4 = done, 2 = name conflict.
func (d *Driver) waitBatchTask(ctx context.Context, c drive.Context, taskType, taskID string, interval time.Duration) error {
	if taskID == "" {
		return nil
	}
	for i := 0; i < 60; i++ {
		raw, err := d.request(ctx, c, apiURL+"/batch/checkBatchTask.action", reqOptions{
			method: "POST",
			form:   map[string]string{"type": taskType, "taskId": taskID},
			// 任务查询始终用个人签名（AList 同款）
			family: boolPtr(false),
		})
		if err != nil {
			return err
		}
		var res struct {
			TaskStatus int `json:"taskStatus"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return err
		}
		switch res.TaskStatus {
		case 4:
			return nil
		case 2:
			return errors.New("目标位置存在冲突")
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("批量任务超时")
}

// runBatch starts a batch task and waits for completion.
func (d *Driver) runBatch(ctx context.Context, c drive.Context, taskType string, items []fileRefItem, targetFolderID string, interval time.Duration, extra map[string]string) ([]string, error) {
	taskID, err := d.createBatchTask(ctx, c, taskType, targetFolderID, items, extra)
	if err != nil {
		return nil, err
	}
	if err := d.waitBatchTask(ctx, c, taskType, taskID, interval); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.FileID)
	}
	return ids, nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	sess, err := sessionOf(c.Token)
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	isFamily, familyID := cloudInfo(sess)
	parent := toFolderID(parentID)
	var (
		rawURL string
		query  map[string]string
	)
	if isFamily {
		rawURL = apiURL + "/family/file/createFolder.action"
		query = map[string]string{"folderName": name, "relativePath": "", "familyId": familyID, "parentId": parent}
	} else {
		rawURL = apiURL + "/createFolder.action"
		query = map[string]string{"folderName": name, "relativePath": "", "parentFolderId": parent}
	}
	raw, err := d.request(ctx, c, rawURL, reqOptions{method: "POST", query: query})
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	var res struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(raw, &res)
	return &drive.MkdirResult{FileID: rawIDString(res.ID)}, nil
}

// renameOne renames a file or folder depending on isDir (file first, folder
// fallback in the driver).
func (d *Driver) renameOne(ctx context.Context, c drive.Context, fileID, name string, isDir bool) error {
	sess, err := sessionOf(c.Token)
	if err != nil {
		return err
	}
	isFamily, familyID := cloudInfo(sess)
	base := map[string]string{}
	if isDir {
		base = map[string]string{"folderId": toFolderID(fileID), "destFolderName": name}
	} else {
		base = map[string]string{"fileId": toFolderID(fileID), "destFileName": name}
	}
	path := "/renameFolder.action"
	if !isDir {
		path = "/renameFile.action"
	}
	var rawURL string
	method := "POST"
	if isFamily {
		rawURL = apiURL + "/family/file" + path
		base["familyId"] = familyID
		method = "GET"
	} else {
		rawURL = apiURL + path
	}
	_, err = d.request(ctx, c, rawURL, reqOptions{method: method, query: base})
	return err
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	err := d.renameOne(ctx, c, fileID, name, false)
	if err != nil {
		err = d.renameOne(ctx, c, fileID, name, true)
	}
	if err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: fileID, Name: name, IsDir: false}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	refs := make([]drive.FileRef, 0, len(fileIDs))
	for _, id := range fileIDs {
		refs = append(refs, drive.FileRef{ID: id})
	}
	return d.runBatch(ctx, c, "DELETE", batchRefs(c, refs), "", 250*time.Millisecond, nil)
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	items := batchRefs(c, refs)
	// DELETE 任务后补 CLEAR_RECYCLE 清空回收站（对齐 AList Delete 双任务）。
	ids, err := d.runBatch(ctx, c, "DELETE", items, "", 250*time.Millisecond, nil)
	if err != nil {
		return nil, err
	}
	if _, err := d.runBatch(ctx, c, "CLEAR_RECYCLE", items, "", 250*time.Millisecond, nil); err != nil {
		// 清空回收站失败不阻断删除结果（源文件已删除）
		_ = err
	}
	return ids, nil
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	items := batchRefs(c, refs)
	return d.runBatch(ctx, c, "MOVE", items, toFolderID(toParentID), 400*time.Millisecond, map[string]string{"targetFileName": ""})
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	items := batchRefs(c, refs)
	return d.runBatch(ctx, c, "COPY", items, toFolderID(toParentID), 1*time.Second, map[string]string{"targetFileName": ""})
}
