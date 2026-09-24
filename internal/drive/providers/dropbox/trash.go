package dropbox

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
)

type dropboxRevisions struct {
	IsDeleted bool       `json:"is_deleted"`
	Entries   []Metadata `json:"entries"`
}

func (c *client) deletedFileRevision(ctx context.Context, path string) (string, bool, error) {
	var revisions dropboxRevisions
	if err := c.rpc(ctx, "/files/list_revisions", map[string]any{"path": path, "limit": 1}, &revisions); err != nil {
		if strings.Contains(err.Error(), "http 409: not_file/") || strings.Contains(err.Error(), "http 409: not_found/") {
			return "", false, nil
		}
		return "", false, err
	}
	if !revisions.IsDeleted || len(revisions.Entries) == 0 {
		return "", false, nil
	}
	if revisions.Entries[0].Rev == "" {
		return "", false, errors.New("Dropbox 文件版本缺少修订号")
	}
	return revisions.Entries[0].Rev, true, nil
}

func (c *client) restoreDeletedFile(ctx context.Context, path string) error {
	revision, deleted, err := c.deletedFileRevision(ctx, path)
	if err != nil {
		return err
	}
	if !deleted {
		return errors.New("文件已不在回收站或没有可恢复的版本")
	}
	return c.rpc(ctx, "/files/restore", map[string]string{"path": path, "rev": revision}, nil)
}

func (d *Driver) ListTrash(ctx context.Context, account drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(account)
	if err != nil {
		return nil, err
	}
	var files []model.File
	seenPaths := make(map[string]bool)
	seenCursors := make(map[string]bool)
	cursor := ""
	for {
		var page listFolderResp
		if cursor == "" {
			err = cl.rpc(ctx, "/files/list_folder", map[string]any{
				"path": "", "recursive": true, "include_deleted": true, "limit": 2000,
			}, &page)
		} else {
			err = cl.rpc(ctx, "/files/list_folder/continue", map[string]string{"cursor": cursor}, &page)
		}
		if err != nil {
			return nil, err
		}
		for _, item := range page.Entries {
			if item.Tag != "deleted" {
				continue
			}
			path := strings.TrimSpace(item.PathLower)
			if path == "" {
				path = strings.TrimSpace(item.PathDisplay)
			}
			if path == "" || seenPaths[strings.ToLower(path)] {
				continue
			}
			seenPaths[strings.ToLower(path)] = true
			if _, deleted, err := cl.deletedFileRevision(ctx, path); err != nil {
				return nil, fmt.Errorf("Dropbox 回收站查询 %s 失败: %w", path, err)
			} else if !deleted {
				continue
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = path[strings.LastIndex(path, "/")+1:]
			}
			file := driveutil.NewFile(account.DriveID, path, parentOf(path), name, false, 0, 0)
			file.Path = item.PathDisplay
			files = append(files, file)
		}
		if !page.HasMore {
			return files, nil
		}
		cursor = strings.TrimSpace(page.Cursor)
		if cursor == "" || seenCursors[cursor] {
			return nil, errors.New("Dropbox 回收站分页游标无效或重复")
		}
		seenCursors[cursor] = true
	}
}
