package yike

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

type yikeRoundTripFunc func(*http.Request) (*http.Response, error)

func (handler yikeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return handler(request)
}

func yikeResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Body: io.NopCloser(strings.NewReader(body)),
		Header: make(http.Header), Request: request,
	}
}

func TestYikeTrashListRestoreAndDeleteRequest(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	listRequests, restoreRequests, deleteRequests := 0, 0, 0
	netx.TestTransportHook = yikeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "photo.baidu.com" || request.Header.Get("Cookie") != "BDUSS=test-cookie" {
			return nil, fmt.Errorf("请求地址或 Cookie 不符: %s", request.URL)
		}
		query := request.URL.Query()
		switch request.URL.Path {
		case "/youai/file/v1/listrecycle":
			listRequests++
			if request.Method != http.MethodGet || query.Get("need_thumbnail") != "1" {
				return nil, fmt.Errorf("回收站列表请求无效: %s", request.URL)
			}
			if query.Get("cursor") == "" {
				return yikeResponse(request, 200, `{"errno":0,"list":[{"fsid":9007199254740993,"path":"/photos/hi.jpg","size":42,"mtime":1728000000}],"cursor":"next"}`), nil
			}
			if query.Get("cursor") != "next" {
				return nil, fmt.Errorf("游标无效: %s", request.URL)
			}
			return yikeResponse(request, 200, `{"errno":0,"list":[{"fsid":"1000","name":"second.jpg"}],"cursor":""}`), nil
		case "/youai/file/v1/restore":
			restoreRequests++
			if request.Method != http.MethodGet || query.Get("fsid_list") != "9007199254740993" {
				return nil, fmt.Errorf("恢复请求无效: %s", request.URL)
			}
			return yikeResponse(request, 200, `{"errno":0}`), nil
		case "/youai/file/v1/delete":
			deleteRequests++
			if request.Method != http.MethodGet || query.Get("fsid_list") != "1000" {
				return nil, fmt.Errorf("移入回收站请求无效: %s", request.URL)
			}
			return yikeResponse(request, 200, `{"errno":0}`), nil
		default:
			return nil, fmt.Errorf("多余请求: %s", request.URL)
		}
	})
	account := drive.Context{DriveID: "yike:test", Token: &model.TokenInfo{RefreshToken: mustJSON(Session{Cookie: "BDUSS=test-cookie"})}}
	provider := &Driver{}
	files, err := provider.ListTrash(context.Background(), account, nil)
	if err != nil || len(files) != 2 || files[0].FileID != "f:9007199254740993" || files[0].Name != "hi.jpg" || files[0].Size != 42 || files[1].FileID != "f:1000" || listRequests != 2 {
		t.Fatalf("ListTrash() = %+v, %v, requests=%d", files, err, listRequests)
	}
	ids, err := provider.Restore(context.Background(), account, []string{files[0].FileID, "album:9"})
	if err == nil || !strings.Contains(err.Error(), "文件 ID 无效") || len(ids) != 1 || ids[0] != files[0].FileID || restoreRequests != 1 {
		t.Fatalf("Restore() = %v, %v, requests=%d", ids, err, restoreRequests)
	}
	if ids, err := provider.Trash(context.Background(), account, []string{files[1].FileID}); err != nil || len(ids) != 1 || deleteRequests != 1 {
		t.Fatalf("Trash() = %v, %v, requests=%d", ids, err, deleteRequests)
	}
	if _, err := provider.Trash(context.Background(), account, []string{"album:9"}); err == nil || deleteRequests != 1 {
		t.Fatalf("相册不得伪装成可恢复的回收站删除: %v, requests=%d", err, deleteRequests)
	}
}

func TestYikeTrashRejectsBadPagesAndHTTPFailures(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	account := drive.Context{Token: &model.TokenInfo{RefreshToken: mustJSON(Session{Cookie: "BDUSS=test-cookie"})}}
	for _, testCase := range []struct {
		name   string
		status int
		body   string
	}{
		{"无列表", 200, `{"errno":0}`},
		{"HTTP 失败", 500, `{"errno":0,"list":[]}`},
		{"无效 JSON", 200, `<html>网关错误</html>`},
		{"循环游标", 200, `{"errno":0,"list":[],"cursor":"loop"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			netx.TestTransportHook = yikeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return yikeResponse(request, testCase.status, testCase.body), nil
			})
			if _, err := (&Driver{}).ListTrash(context.Background(), account, nil); err == nil {
				t.Fatal("预期拒绝无效响应")
			}
		})
	}
}
