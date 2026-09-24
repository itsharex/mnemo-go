package yike

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
)

func yikeRecycleString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func yikeRecycleInt(raw json.RawMessage) int64 {
	value, err := strconv.ParseInt(yikeRecycleString(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (d *Driver) ListTrash(ctx context.Context, account drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(account)
	if err != nil {
		return nil, err
	}
	var files []model.File
	cursor := ""
	seenCursors := make(map[string]bool)
	seenFiles := make(map[string]bool)
	for {
		query := url.Values{"need_thumbnail": {"1"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		body, err := cl.request(ctx, http.MethodGet, fileV1+"/listrecycle", query)
		if err != nil {
			return nil, err
		}
		var page struct {
			List   json.RawMessage `json:"list"`
			Cursor string          `json:"cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil || len(page.List) == 0 {
			return nil, errors.New("一刻相册回收站列表响应无效")
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(page.List, &entries); err != nil {
			return nil, fmt.Errorf("一刻相册回收站文件列表无效: %w", err)
		}
		for _, entry := range entries {
			fsid := yikeRecycleString(entry["fsid"])
			if fsid == "" {
				fsid = yikeRecycleString(entry["fs_id"])
			}
			name := yikeRecycleString(entry["name"])
			path := yikeRecycleString(entry["path"])
			if name == "" {
				name = fileNameFromPath(path, fsid)
			}
			if fsid == "" || name == "" || seenFiles[fsid] {
				return nil, errors.New("一刻相册回收站包含无效或重复的文件 ID")
			}
			seenFiles[fsid] = true
			modified := entry["mtime"]
			if len(modified) == 0 {
				modified = entry["server_mtime"]
			}
			file := driveutil.NewFile(account.DriveID, "f:"+fsid, RootID, name, false, yikeRecycleInt(entry["size"]), yikeRecycleInt(modified))
			file.Path = path
			file.Thumbnail = yikeRecycleString(entry["thumburl"])
			files = append(files, file)
		}
		cursor = strings.TrimSpace(page.Cursor)
		if cursor == "" {
			return files, nil
		}
		if seenCursors[cursor] {
			return nil, errors.New("一刻相册回收站分页游标重复")
		}
		seenCursors[cursor] = true
	}
}

func (d *Driver) Restore(ctx context.Context, account drive.Context, fileIDs []string) ([]string, error) {
	cl, err := clientOf(account)
	if err != nil {
		return nil, err
	}
	var completed []string
	var failed []error
	for _, id := range fileIDs {
		fsid := parseFsid(id)
		if !strings.HasPrefix(id, "f:") || fsid == "" || strings.ContainsAny(fsid, ",&?/") {
			failed = append(failed, fmt.Errorf("%s: 一刻相册回收站文件 ID 无效", id))
			continue
		}
		query := url.Values{"fsid_list": {fsid}}
		if _, err := cl.request(ctx, http.MethodGet, fileV1+"/restore", query); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", id, err))
		} else {
			completed = append(completed, id)
		}
	}
	return completed, errors.Join(failed...)
}
