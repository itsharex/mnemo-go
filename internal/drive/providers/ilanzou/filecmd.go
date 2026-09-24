package ilanzou

import (
	"context"
	"net/http"
	"strings"

	"mnemo-go/internal/drive"
)

// apiILanzouMkdir ports /file/folder/save.
func (d *Driver) mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	j, _, err := d.request(ctx, c, "/file/folder/save", requestOptions{
		method: http.MethodPost,
		body: map[string]any{
			"folderDesc": "",
			"folderId":   ToILanzouFolderId(parentID),
			"folderName": name,
		},
	})
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	id := firstListID(j["list"])
	if id == "" {
		if data := mapVal(j, "data"); data != nil {
			id = firstListID(data["list"])
		}
	}
	if id == "" {
		return &drive.MkdirResult{Error: "新建文件夹失败"}, nil
	}
	return &drive.MkdirResult{FileID: id}, nil
}

func firstListID(v any) string {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	if m, ok := arr[0].(map[string]any); ok {
		return strOf(m["id"])
	}
	return ""
}

// rename ports /file/folder/edit (folder) or /file/edit (file).
func (d *Driver) rename(ctx context.Context, c drive.Context, fileID, name string, isDir bool) error {
	pathName := "/file/edit"
	body := map[string]any{"fileDesc": "", "fileId": fileID, "fileName": name}
	if isDir {
		pathName = "/file/folder/edit"
		body = map[string]any{"folderDesc": "", "folderId": fileID, "folderName": name}
	}
	_, _, err := d.request(ctx, c, pathName, requestOptions{method: http.MethodPost, body: body})
	return err
}

// deleteBatch ports /file/delete with folderIds/fileIds CSV payloads.
func (d *Driver) deleteBatch(ctx context.Context, c drive.Context, refs []drive.FileRef, status int) ([]string, error) {
	var folderIDs, fileIDs []string
	for _, r := range refs {
		if r.IsDir != nil && *r.IsDir {
			folderIDs = append(folderIDs, r.ID)
		} else {
			fileIDs = append(fileIDs, r.ID)
		}
	}
	_, _, err := d.request(ctx, c, "/file/delete", requestOptions{
		method: http.MethodPost,
		body: map[string]any{
			"folderIds": strings.Join(folderIDs, ","),
			"fileIds":   strings.Join(fileIDs, ","),
			"status":    status,
		},
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// moveBatch ports /file/folder/move with folderIds/fileIds CSV payloads.
func (d *Driver) moveBatch(ctx context.Context, c drive.Context, refs []drive.FileRef, targetID string) ([]string, error) {
	var folderIDs, fileIDs []string
	for _, r := range refs {
		if r.IsDir != nil && *r.IsDir {
			folderIDs = append(folderIDs, r.ID)
		} else {
			fileIDs = append(fileIDs, r.ID)
		}
	}
	_, _, err := d.request(ctx, c, "/file/folder/move", requestOptions{
		method: http.MethodPost,
		body: map[string]any{
			"folderIds": strings.Join(folderIDs, ","),
			"fileIds":   strings.Join(fileIDs, ","),
			"targetId":  ToILanzouFolderId(targetID),
		},
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	return ids, nil
}
