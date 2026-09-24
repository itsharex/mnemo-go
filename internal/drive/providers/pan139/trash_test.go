package pan139

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

func TestPan139TrashListsPagesAndRestoresPersonalFiles(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	authorization := encodeAuthorization("pc", "test-account", "token|a|b|4102444800000")
	requestCount := 0
	netx.TestTransportHook = pan139RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		if req.URL.Host != "api.139.test" || req.Header.Get("Authorization") != "Basic "+authorization {
			return nil, fmt.Errorf("unexpected host or authorization: %s", req.URL)
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			return nil, err
		}
		switch req.URL.Path {
		case "/recyclebin/list":
			var page struct {
				PageSize   int     `json:"pageSize"`
				PageCursor *string `json:"pageCursor"`
			}
			if err := json.Unmarshal(payload["pageInfo"], &page); err != nil || page.PageSize != 100 {
				return nil, fmt.Errorf("invalid list page: %s: %v", payload["pageInfo"], err)
			}
			if page.PageCursor == nil {
				return pan139Response(req, 200, nil, `{"success":true,"data":{"items":[{"fileId":"a","parentFileId":"/","name":"文件.txt","size":8,"type":"file"}],"nextPageCursor":"next"}}`), nil
			}
			if *page.PageCursor != "next" {
				return nil, fmt.Errorf("unexpected cursor: %s", *page.PageCursor)
			}
			return pan139Response(req, 200, nil, `{"success":true,"data":{"items":[{"fileId":"b","parentFileId":"folder","name":"目录","type":"folder"}],"nextPageCursor":""}}`), nil
		case "/recyclebin/batchRestore":
			var fileIDs []string
			if err := json.Unmarshal(payload["fileIds"], &fileIDs); err != nil || len(fileIDs) != 2 || fileIDs[0] != "a" || fileIDs[1] != "b" {
				return nil, fmt.Errorf("unexpected restore file IDs: %s: %v", payload["fileIds"], err)
			}
			return pan139Response(req, 200, nil, `{"success":true,"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected endpoint: %s", req.URL)
		}
	})
	c := drive.Context{DriveID: "pan139:test-account", Token: &model.TokenInfo{
		AccessToken: authorization, RefreshToken: mustJSON(map[string]string{
			"authorization": authorization, "account": "test-account", "personalCloudHost": "https://api.139.test",
		}),
	}}
	driver := &Driver{}
	items, err := driver.ListTrash(context.Background(), c, nil)
	if err != nil || len(items) != 2 || items[0].FileID != pan139FileID(pan139PersonalSpace, "a") || items[0].ParentFileID != Pan139PersonalRoot || items[1].ParentFileID != pan139FileID(pan139PersonalSpace, "folder") || !items[1].IsDir {
		t.Fatalf("ListTrash() = %+v, %v", items, err)
	}
	ids := []string{items[0].FileID, items[1].FileID}
	restored, err := driver.Restore(context.Background(), c, ids)
	if err != nil || strings.Join(restored, ",") != strings.Join(ids, ",") || requestCount != 3 {
		t.Fatalf("Restore() = %v, %v, requests=%d", restored, err, requestCount)
	}
}

func TestPan139TrashRejectsRepeatedCursorAndFamilyRestore(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	authorization := encodeAuthorization("pc", "test-account", "token|a|b|4102444800000")
	netx.TestTransportHook = pan139RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return pan139Response(req, 200, nil, `{"success":true,"data":{"items":[],"nextPageCursor":"loop"}}`), nil
	})
	c := drive.Context{Token: &model.TokenInfo{AccessToken: authorization, RefreshToken: mustJSON(map[string]string{"personalCloudHost": "https://api.139.test"})}}
	if _, err := (&Driver{}).ListTrash(context.Background(), c, nil); err == nil || !strings.Contains(err.Error(), "游标重复") {
		t.Fatalf("expected repeated cursor error, got %v", err)
	}
	if _, err := (&Driver{}).Restore(context.Background(), c, []string{pan139FileID(pan139FamilySpace, "family-file")}); err == nil || !strings.Contains(err.Error(), "家庭云") {
		t.Fatalf("expected unsupported family restore, got %v", err)
	}
}
