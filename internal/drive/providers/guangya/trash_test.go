package guangya

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

func TestGuangyaRestoreWaitsForSuccessfulTask(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	var paths []string
	netx.TestTransportHook = guangyaRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		if req.URL.Host != "api.guangyapan.com" || req.Method != http.MethodPost {
			return nil, fmt.Errorf("意外的恢复请求: %s %s", req.Method, req.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		switch req.URL.Path {
		case "/userres/v1/file/recycle_file":
			if ids, ok := body["fileIds"].([]any); !ok || len(ids) != 2 || ids[0] != "file-1" || ids[1] != "folder-2" {
				t.Errorf("恢复参数: %+v", body)
			}
			return guangyaResponse(req, 200, `{"code":0,"data":{"taskId":"task-1"}}`), nil
		case "/userres/v1/get_task_status":
			if body["taskId"] != "task-1" {
				t.Errorf("任务参数: %+v", body)
			}
			return guangyaResponse(req, 200, `{"code":0,"data":{"status":2}}`), nil
		}
		return nil, fmt.Errorf("意外的路径: %s", req.URL.Path)
	})
	account := drive.Context{Token: &model.TokenInfo{AccessToken: "access-token"}}
	ids, err := (&Driver{}).Restore(context.Background(), account, []string{"file-1", "folder-2"})
	if err != nil || strings.Join(ids, ",") != "file-1,folder-2" || len(paths) != 2 {
		t.Fatalf("恢复结果=%v, error=%v, paths=%v", ids, err, paths)
	}
}

func TestGuangyaRestoreRejectsFailedTasks(t *testing.T) {
	for _, testCase := range []struct {
		name, start, status string
	}{
		{"request failed", `{"code":403,"msg":"denied"}`, ""},
		{"missing task", `{"code":0,"data":{}}`, ""},
		{"task failed", `{"code":0,"data":{"taskId":"task"}}`, `{"code":0,"data":{"status":3,"detail":{"msg":"failed"}}}`},
		{"task detail failed", `{"code":0,"data":{"taskId":"task"}}`, `{"code":0,"data":{"status":2,"detail":{"code":32,"msg":"failed"}}}`},
		{"task status missing", `{"code":0,"data":{"taskId":"task"}}`, `{"code":0}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			previous := netx.TestTransportHook
			t.Cleanup(func() { netx.TestTransportHook = previous })
			netx.TestTransportHook = guangyaRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/get_task_status") {
					return guangyaResponse(req, 200, testCase.status), nil
				}
				return guangyaResponse(req, 200, testCase.start), nil
			})
			account := drive.Context{Token: &model.TokenInfo{AccessToken: "token"}}
			if ids, err := (&Driver{}).Restore(context.Background(), account, []string{"file-1"}); err == nil || len(ids) != 0 {
				t.Fatalf("失败任务被当作成功: %v, %v", ids, err)
			}
		})
	}
	if _, err := (&Driver{}).Restore(context.Background(), drive.Context{}, []string{""}); err == nil {
		t.Fatal("空 ID 不得触发请求")
	}
}

func TestGuangyaTrashListRejectsBusinessFailure(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	netx.TestTransportHook = guangyaRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return guangyaResponse(req, 200, `{"code":403,"msg":"denied"}`), nil
	})
	account := drive.Context{Token: &model.TokenInfo{AccessToken: "token"}}
	if files, err := (&Driver{}).ListTrash(context.Background(), account, nil); err == nil || len(files) != 0 {
		t.Fatalf("业务失败被当作空回收站: %+v, %v", files, err)
	}
}
