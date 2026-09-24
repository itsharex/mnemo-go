package ilanzou

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

func TestILanzouRecycleListRestoreAndDeleteStatus(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })
	var offsets []string
	var restoreBodies []map[string]any
	var deleteBodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/record/recycle/list"):
			if request.Method != http.MethodGet || request.URL.Query().Get("limit") != "60" {
				t.Errorf("回收站列表请求: %s %s", request.Method, request.URL)
			}
			offsets = append(offsets, request.URL.Query().Get("offset"))
			if len(offsets) == 1 {
				_, _ = w.Write([]byte(`{"code":200,"data":{"list":[{"fileType":1,"fileId":9007199254740993,"fileName":"a.txt","fileSize":2},{"fileType":0,"folderId":"9007199254740995","folderName":"dir"}],"totalPage":2}}`))
			} else {
				_, _ = w.Write([]byte(`{"code":200,"data":{"list":[],"totalPage":2}}`))
			}
		case strings.HasSuffix(request.URL.Path, "/file/resume"):
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			restoreBodies = append(restoreBodies, body)
			_, _ = w.Write([]byte(`{"code":200}`))
		case strings.HasSuffix(request.URL.Path, "/file/delete"):
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			deleteBodies = append(deleteBodies, body)
			_, _ = w.Write([]byte(`{"code":200}`))
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ILANZOU_CONF.Base = srv.URL
	provider := &Driver{}
	account := drive.Context{DriveID: "ilanzou:abc", Token: &model.TokenInfo{AccessToken: "token"}}
	items, err := provider.ListTrash(context.Background(), account, nil)
	if err != nil || len(items) != 2 || items[0].FileID != "file:9007199254740993" || items[0].Size != 2048 || items[1].FileID != "folder:9007199254740995" || !items[1].IsDir || len(offsets) != 2 || offsets[1] != "2" {
		t.Fatalf("回收站列表 = %+v, err=%v, offsets=%v", items, err, offsets)
	}
	ids, err := provider.Restore(context.Background(), account, []string{items[0].FileID, items[1].FileID, "file:1,2"})
	if err == nil || len(ids) != 2 || len(restoreBodies) != 2 || restoreBodies[0]["fileIds"] != "9007199254740993" || restoreBodies[0]["folderIds"] != "" || restoreBodies[1]["folderIds"] != "9007199254740995" {
		t.Fatalf("恢复 = %v, err=%v, bodies=%+v", ids, err, restoreBodies)
	}
	_, err = provider.Trash(context.Background(), account, []string{"8"})
	if err != nil {
		t.Fatal(err)
	}
	file := false
	_, err = provider.Delete(context.Background(), account, []drive.FileRef{{ID: "9", IsDir: &file}})
	if err != nil || len(deleteBodies) != 2 || deleteBodies[0]["status"] != float64(0) || deleteBodies[1]["status"] != float64(-1) {
		t.Fatalf("删除参数 = %+v, err=%v", deleteBodies, err)
	}
	ids, err = provider.Delete(context.Background(), account, []drive.FileRef{{ID: items[0].FileID}, {ID: items[1].FileID}, {ID: "folder:1,2"}})
	if err == nil || len(ids) != 2 || ids[0] != items[0].FileID || ids[1] != items[1].FileID || len(deleteBodies) != 4 || deleteBodies[2]["fileIds"] != "9007199254740993" || deleteBodies[3]["folderIds"] != "9007199254740995" {
		t.Fatalf("回收站彻底删除 = %v, err=%v, bodies=%+v", ids, err, deleteBodies)
	}
}

func TestILanzouRecycleRejectsInvalidResponse(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/file/resume") {
			_, _ = w.Write([]byte(`{"code":400,"msg":"cannot restore"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"list":[{"fileType":1,"fileName":"missing id"}],"totalPage":1}}`))
	}))
	defer srv.Close()
	ILANZOU_CONF.Base = srv.URL
	provider := &Driver{}
	account := drive.Context{Token: &model.TokenInfo{AccessToken: "token"}}
	if _, err := provider.ListTrash(context.Background(), account, nil); err == nil {
		t.Fatal("无 ID 文件不能呈现为可恢复文件")
	}
	if _, err := provider.Restore(context.Background(), account, []string{"file:8"}); err == nil {
		t.Fatal("服务端恢复失败不能呈现成功")
	}
}
