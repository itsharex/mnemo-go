package guangya

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

type guangyaRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn guangyaRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func guangyaResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestListTrashUsesRecycleDirectory(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	netx.TestTransportHook = guangyaRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/userres/v1/file/get_file_list" {
			return nil, fmt.Errorf("unexpected path: %s", req.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["dirType"] != float64(4) || body["parentId"] != "" {
			t.Errorf("recycle list body: %+v", body)
		}
		return guangyaResponse(req, http.StatusOK, `{"data":{"list":[{"fileId":"deleted-1","fileName":"测试.txt","fileSize":3}],"total":1}}`), nil
	})
	session := &Session{AccessToken: "access-token", RefreshToken: "refresh-token", DeviceID: "device-test"}
	items, err := (&Driver{}).ListTrash(t.Context(), drive.Context{DriveID: "guangya:test", Token: &model.TokenInfo{AccessToken: session.AccessToken, RefreshToken: mustJSON(session)}}, nil)
	if err != nil || len(items) != 1 || items[0].FileID != "deleted-1" || items[0].Name != "测试.txt" {
		t.Fatalf("trash items = %+v, %v", items, err)
	}
}

func TestCreateShareUsesGuangyaShareAPI(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	var requests int
	netx.TestTransportHook = guangyaRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodPost || req.URL.Host != "api.guangyapan.com" || req.URL.Path != "/nd.bizuserres.s/v1/share_file" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL.String())
		}
		if req.Header.Get("Authorization") != "Bearer access-token" || req.Header.Get("X-Device-Id") != "device-test" {
			return nil, fmt.Errorf("share request auth headers are incomplete")
		}
		var body struct {
			FileIDs          []string `json:"fileIds"`
			Title            string   `json:"title"`
			ValidateDuration int      `json:"validateDuration"`
			ShareType        int      `json:"shareType"`
			Code             string   `json:"code"`
			AutoFillCode     bool     `json:"autoFillCode"`
			DownloadType     int      `json:"downloadType"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if strings.Join(body.FileIDs, ",") != "file-1,folder-1" || body.Title != "测试分享" || body.ValidateDuration != 7 || body.ShareType != 1 || body.Code != "p4ss" || !body.AutoFillCode || body.DownloadType != 1 {
			return nil, fmt.Errorf("share body = %+v", body)
		}
		return guangyaResponse(req, http.StatusOK, `{"code":0,"data":{"shareId":"share-guangya","shareUrl":"https://www.guangyapan.com/s/share-guangya","code":"p4ss"}}`), nil
	})

	sess := &Session{AccessToken: "access-token", RefreshToken: "refresh-token", DeviceID: "device-test", ClientID: "client-test"}
	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "guangya:user", DriveID: "guangya:user",
		Token: &model.TokenInfo{AccessToken: sess.AccessToken, RefreshToken: mustJSON(sess)},
	}, drive.ShareParams{
		FileIDs: []string{"file-1", "folder-1"}, ShareName: "测试分享", Expiration: "7", Password: "p4ss",
	})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
	if item.ShareID != "share-guangya" || item.ShareURL != "https://www.guangyapan.com/s/share-guangya" || item.SharePwd != "p4ss" || len(item.FileIDList) != 2 || item.SharePolicy != "public" {
		t.Fatalf("share = %+v", item)
	}
}

func TestGuangyaCancelShareDeletesRemoteShare(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	netx.TestTransportHook = guangyaRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api.guangyapan.com" || req.URL.Path != guangyaDeleteSharePath {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL.String())
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if strings.Join(body.IDs, ",") != "share-guangya" {
			return nil, fmt.Errorf("delete body = %+v", body)
		}
		return guangyaResponse(req, http.StatusOK, `{"code":0,"data":{}}`), nil
	})

	sess := &Session{AccessToken: "access-token", RefreshToken: "refresh-token", DeviceID: "device-test", ClientID: "client-test"}
	err := (&Driver{}).CancelShare(context.Background(), drive.Context{
		UserID: "guangya:user", DriveID: "guangya:user",
		Token: &model.TokenInfo{AccessToken: sess.AccessToken, RefreshToken: mustJSON(sess)},
	}, model.ShareHistoryEntry{ShareID: "share-guangya"})
	if err != nil {
		t.Fatalf("CancelShare() error = %v", err)
	}
}

func TestGuangyaShareDurationRejectsInvalidValue(t *testing.T) {
	if _, err := guangyaShareDuration("not-a-time"); err == nil {
		t.Fatal("invalid duration must be rejected")
	}
	if _, err := guangyaShareDuration("-1"); err == nil {
		t.Fatal("negative duration must be rejected")
	}
}
