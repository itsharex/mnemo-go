package pan139

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

func (d *Driver) ListTrash(ctx context.Context, c drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	items := make([]model.File, 0)
	cursor := ""
	seen := map[string]bool{"": true}
	seenFiles := make(map[string]bool)
	for {
		var pageCursor any
		if cursor != "" {
			pageCursor = cursor
		}
		raw, err := d.personalPost(ctx, c, "/recyclebin/list", map[string]any{
			"pageInfo": map[string]any{"pageSize": 100, "pageCursor": pageCursor},
		})
		if err != nil {
			return nil, err
		}
		var page listData
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("移动云盘回收站列表响应无效: %w", err)
		}
		entries := page.Items
		if entries == nil {
			entries = page.LegacyItems
		}
		for _, entry := range entries {
			file := mapFile(entry, c.DriveID, "")
			if file.FileID == "" || file.Name == "" {
				return nil, errors.New("移动云盘回收站包含无效文件")
			}
			file.FileID = pan139FileID(pan139PersonalSpace, file.FileID)
			if seenFiles[file.FileID] {
				return nil, errors.New("移动云盘回收站包含重复的文件 ID")
			}
			seenFiles[file.FileID] = true
			file.ParentFileID = pan139FileID(pan139PersonalSpace, entry.ParentFileID.String())
			items = append(items, file)
		}
		cursor = strings.TrimSpace(page.NextPageCursor)
		if cursor == "" {
			return items, nil
		}
		if seen[cursor] {
			return nil, errors.New("移动云盘回收站分页游标重复")
		}
		seen[cursor] = true
	}
}
