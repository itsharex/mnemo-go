package ilanzou

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

func (d *Driver) ListTrash(ctx context.Context, c drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	var files []model.File
	seen := make(map[string]bool)
	for offset := 1; offset <= 1000; offset++ {
		response, _, err := d.request(ctx, c, "/record/recycle/list", requestOptions{
			method: http.MethodGet,
			query:  map[string]string{"offset": strconv.Itoa(offset), "limit": "60"},
		})
		if err != nil {
			return nil, err
		}
		data := mapVal(response, "data")
		if data == nil {
			return nil, errors.New("优享版蓝奏云回收站列表响应无效")
		}
		entries, ok := data["list"].([]any)
		if !ok {
			return nil, errors.New("优享版蓝奏云回收站文件列表无效")
		}
		items := rawItems(entries)
		if len(items) != len(entries) {
			return nil, errors.New("优享版蓝奏云回收站文件列表格式无效")
		}
		for _, item := range items {
			if item.FileType != 1 {
				item.FileType = 2
			}
			file := mapILanzouItem(item, c.DriveID, "trash")
			if file.FileID == "0" || file.Name == "" {
				return nil, errors.New("优享版蓝奏云回收站文件信息无效")
			}
			prefix := "file:"
			if file.IsDir {
				prefix = "folder:"
			}
			file.FileID = prefix + file.FileID
			if !seen[file.FileID] {
				seen[file.FileID] = true
				files = append(files, file)
			}
		}
		totalPage := int(numOf(data["totalPage"]))
		if totalPage < 1 || offset >= totalPage {
			return files, nil
		}
	}
	return nil, errors.New("优享版蓝奏云回收站分页超过安全上限")
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	var completed []string
	var failed []error
	for _, fileID := range fileIDs {
		fileIDsCSV, folderIDsCSV := "", ""
		id, isDir, err := parseRecycleID(fileID)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if isDir {
			folderIDsCSV = id
		} else {
			fileIDsCSV = id
		}
		_, _, err = d.request(ctx, c, "/file/resume", requestOptions{
			method: http.MethodPost,
			body:   map[string]any{"fileIds": fileIDsCSV, "folderIds": folderIDsCSV},
		})
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", fileID, err))
		} else {
			completed = append(completed, fileID)
		}
	}
	return completed, errors.Join(failed...)
}

func parseRecycleID(fileID string) (string, bool, error) {
	prefix, id, ok := strings.Cut(fileID, ":")
	if ok && (prefix == "file" || prefix == "folder") {
		if number, err := strconv.ParseUint(id, 10, 64); err == nil && number > 0 {
			return id, prefix == "folder", nil
		}
	}
	return "", false, fmt.Errorf("%q: 优享版蓝奏云回收站文件 ID 无效", fileID)
}
