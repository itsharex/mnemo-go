package guangya

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mnemo-go/internal/drive"
)

func (d *Driver) Restore(ctx context.Context, account drive.Context, fileIDs []string) ([]string, error) {
	if len(fileIDs) == 0 {
		return []string{}, nil
	}
	for _, id := range fileIDs {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("光鸭回收站文件 ID 不能为空")
		}
	}
	cl, err := clientOf(account)
	if err != nil {
		return nil, err
	}
	var response struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			TaskID string `json:"taskId"`
		} `json:"data"`
	}
	if err := cl.post(ctx, "/userres/v1/file/recycle_file", map[string]any{"fileIds": fileIDs}, &response); err != nil {
		return nil, err
	}
	if response.Code != 0 || response.Data == nil || response.Data.TaskID == "" {
		return nil, fmt.Errorf("光鸭还原请求失败（code=%d）：%s", response.Code, response.Msg)
	}
	if err := cl.waitRecycleTask(ctx, response.Data.TaskID); err != nil {
		return nil, err
	}
	return fileIDs, nil
}

func (c *client) waitRecycleTask(ctx context.Context, taskID string) error {
	for attempt := 0; attempt < 120; attempt++ {
		var response struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data *struct {
				Status int `json:"status"`
				Detail struct {
					Code int    `json:"code"`
					Msg  string `json:"msg"`
				} `json:"detail"`
			} `json:"data"`
		}
		if err := c.post(ctx, "/userres/v1/get_task_status", map[string]string{"taskId": taskID}, &response); err != nil {
			return err
		}
		if response.Code != 0 {
			return fmt.Errorf("光鸭查询还原任务失败（code=%d）：%s", response.Code, response.Msg)
		}
		if response.Data == nil {
			return errors.New("光鸭还原任务响应缺少状态")
		}
		if response.Data.Detail.Code != 0 || response.Data.Status == 3 {
			return fmt.Errorf("光鸭还原任务失败（code=%d）：%s", response.Data.Detail.Code, response.Data.Detail.Msg)
		}
		if response.Data.Status == 2 {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("光鸭还原任务超时，请到回收站确认结果")
}
