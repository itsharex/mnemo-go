package ilanzou

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

func withNoThrottle(t *testing.T) {
	t.Helper()
	old := fetchMinInterval
	fetchMinInterval = 0
	t.Cleanup(func() { fetchMinInterval = old })
}

func TestDownloadResolverJSONAndTokenEncoding(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "appToken=user:token%2B%26") || r.URL.Query().Get("enable") != "1" {
			t.Errorf("incorrect resolver query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":{"url":"/cdn/file.bin"}}`))
	}))
	defer srv.Close()
	ILANZOU_CONF.Base = srv.URL
	info, _ := resolveILanzouDownload(context.Background(), "8", "9", "user:token+&", "device")
	if info.Error != "" || info.URL != srv.URL+"/cdn/file.bin" {
		t.Fatalf("resolver = %+v", info)
	}
}

func TestILanzouNumericIDsStayExact(t *testing.T) {
	var item listItem
	if err := json.Unmarshal([]byte(`{"fileId":9007199254740993,"folderId":9007199254740995}`), &item); err != nil {
		t.Fatal(err)
	}
	if item.FileID != 9007199254740993 || item.FolderID != 9007199254740995 {
		t.Fatalf("rounded IDs: %+v", item)
	}
}

func TestDownloadResolverDoesNotReturnErrorDocumentAsFile(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })
	for _, tc := range []struct {
		name, body        string
		wantURL, wantAuth bool
	}{
		{"untyped JSON", `{"url":"/cdn/file.bin"}`, true, false},
		{"expired session", `{"code":-1,"msg":"expired"}`, false, true},
		{"HTML error", `<html><body>file not found</body></html>`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			ILANZOU_CONF.Base = srv.URL
			info, auth := resolveILanzouDownload(context.Background(), "8", "9", "token", "device")
			if auth != tc.wantAuth || (info.Error == "") != tc.wantURL || (tc.wantURL && info.URL != srv.URL+"/cdn/file.bin") || (!tc.wantURL && info.URL != "") {
				t.Fatalf("result=%+v auth=%v", info, auth)
			}
		})
	}
}

// TestFileListPagination drives /record/file/list against a fake API and
// verifies the offset paging loop stops at totalPage.
func TestFileListPagination(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })

	var mu sync.Mutex
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()
		if offset == "1" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"list": []any{
					map[string]any{"fileType": 2, "folderId": 9, "folderName": "dir"},
					map[string]any{"fileType": 0, "fileId": 8, "fileName": "a.zip", "fileSize": 2},
				},
				"totalPage": 2,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "list": []any{}, "totalPage": 2})
	}))
	defer srv.Close()

	d := &Driver{}
	c := drive.Context{DriveID: "ilanzou:u", Token: &model.TokenInfo{RefreshToken: `{}`}}
	// point the API at the fake server via the package config
	ILANZOU_CONF.Base = srv.URL

	items, err := d.fileList(context.Background(), c, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].FolderName != "dir" || items[1].FileName != "a.zip" {
		t.Errorf("items wrong: %+v", items)
	}
	if len(offsets) != 2 || offsets[0] != "1" || offsets[1] != "2" {
		t.Errorf("offset sequence = %v, want [1 2]", offsets)
	}
	// pagination must not request beyond totalPage
	for _, o := range offsets {
		if o == "3" {
			t.Errorf("requested beyond totalPage: %v", offsets)
		}
	}
}

// TestLoginAndAccountMap drives the /login + /user/account/map flow.
func TestLoginAndAccountMap(t *testing.T) {
	withNoThrottle(t)
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUuid"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "uuid": "server-uuid-123"})
		case strings.HasSuffix(r.URL.Path, "/login"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["loginName"] != "user" || body["loginPwd"] != "pass" {
				http.Error(w, "bad credentials", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"appToken": "tok-123"},
			})
		case strings.HasSuffix(r.URL.Path, "/user/account/map"):
			if r.URL.Query().Get("appToken") != "tok-123" {
				http.Error(w, "missing appToken", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"map": map[string]any{
					"userId": "42", "account": "user@ilanzou",
					"totalSize": "10240", "vipSize": 2048, "rewardSize": "1024", "usedSize": "4096",
				},
			})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	old := ILANZOU_CONF.Base
	ILANZOU_CONF.Base = srv.URL
	defer func() { ILANZOU_CONF.Base = old }()

	login, err := ilanzouLogin(context.Background(), "user", "pass", "")
	if err != nil {
		t.Fatal(err)
	}
	if login.token != "tok-123" {
		t.Errorf("token = %q, want tok-123", login.token)
	}
	if login.uuid != "server-uuid-123" {
		t.Errorf("uuid = %q, want server-uuid-123", login.uuid)
	}
	if login.userId != "42" || login.account != "user@ilanzou" {
		t.Errorf("map = %+v", login)
	}
	if !login.hasQuota || login.totalSize != (10240+2048+1024)*1024 || login.usedSize != 4096*1024 {
		t.Errorf("quota = %+v", login)
	}
	if len(calls) != 3 {
		t.Errorf("calls = %v, want 3", calls)
	}
}

func TestRefreshAccountUpdatesQuotaFromAccountMap(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/user/account/map") {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"map": map[string]any{
				"totalSize": 1024, "vipSize": "2048", "rewardSize": 1024, "usedSize": 512,
			}},
		})
	}))
	defer srv.Close()
	ILANZOU_CONF.Base = srv.URL

	token := &model.TokenInfo{AccessToken: "token", DeviceID: "device", TotalSize: 1, UsedSize: 1, FreeSize: 0}
	updated, err := (&Driver{}).RefreshAccount(context.Background(), drive.Context{Token: token}, token)
	if err != nil {
		t.Fatal(err)
	}
	if updated != token || token.TotalSize != (1024+2048+1024)*1024 || token.UsedSize != 512*1024 || token.FreeSize != (1024+2048+1024-512)*1024 {
		t.Fatalf("updated quota = %+v", token)
	}
}

func TestILanzouQuotaRejectsInvalidValuesWithoutClearingCache(t *testing.T) {
	token := &model.TokenInfo{TotalSize: 4096, UsedSize: 1024, FreeSize: 3072}
	total, used, ok := ilanzouQuota(map[string]any{"map": map[string]any{"totalSize": "bad", "usedSize": 1}})
	if ok || total != 0 || used != 0 {
		t.Fatalf("invalid quota parsed as total=%d used=%d ok=%v", total, used, ok)
	}
	applyILanzouQuota(token, total, used, ok)
	if token.TotalSize != 4096 || token.UsedSize != 1024 || token.FreeSize != 3072 {
		t.Fatalf("invalid quota cleared cache: %+v", token)
	}
}

func TestRequestAutoReloginPersistsRotatedSession(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })

	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/record/file/list"):
			if r.URL.Query().Get("appToken") == "old-token" {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "msg": "token expired"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "list": []any{}, "totalPage": 1})
		case strings.HasSuffix(r.URL.Path, "/login"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["loginName"] != "user" || body["loginPwd"] != "pass" {
				http.Error(w, "bad credentials", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"appToken": "new-token"}})
		case strings.HasSuffix(r.URL.Path, "/user/account/map"):
			if r.URL.Query().Get("appToken") != "new-token" {
				http.Error(w, "missing new token", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "map": map[string]any{"userId": "42", "account": "user@ilanzou"}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ILANZOU_CONF.Base = srv.URL

	tok := &model.TokenInfo{
		TokenFrom: model.ProviderIlanzou, AccessToken: "old-token", DeviceID: "uuid-old",
		UserID: "ilanzou_42", ProviderAccountID: "42", DefaultDriveID: "ilanzou:42",
		RefreshToken: `{"username":"user","password":"pass","uuid":"uuid-old","token":"old-token","userId":"42","account":"user@ilanzou"}`,
	}
	c := drive.Context{UserID: tok.UserID, DriveID: tok.DefaultDriveID, Token: tok}
	j, login, err := (&Driver{}).request(context.Background(), c, "/record/file/list", requestOptions{method: http.MethodGet})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if login == nil || numOf(j["code"]) != 200 {
		t.Fatalf("request result = %#v, login = %#v", j, login)
	}
	if tok.AccessToken != "new-token" || tok.DeviceID != "uuid-old" {
		t.Fatalf("rotated session not applied: %+v", tok)
	}
	cr := parseCred(tok.RefreshToken)
	if cr == nil || cr.Token != "new-token" || cr.UUID != "uuid-old" || cr.UserID != "42" || cr.Account != "user@ilanzou" {
		t.Fatalf("rotated credentials not persisted: %+v", cr)
	}
	mu.Lock()
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	wantPaths := []string{"/proved/record/file/list", "/unproved/login", "/proved/user/account/map", "/proved/record/file/list"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("request paths = %v, want %v", gotPaths, wantPaths)
	}
	for i := range wantPaths {
		if gotPaths[i] != wantPaths[i] {
			t.Fatalf("request paths = %v, want %v", gotPaths, wantPaths)
		}
	}
}

func TestDownloadAndVideoPreviewAutoReloginAndUseCachedSize(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })

	var loginCalls int
	var redirectURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/file/redirect"):
			if r.URL.Query().Get("appToken") == "old-token" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("token expired"))
				return
			}
			w.Header().Set("Location", redirectURL)
			w.WriteHeader(http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/login"):
			loginCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"appToken": "new-token"}})
		case strings.HasSuffix(r.URL.Path, "/user/account/map"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "map": map[string]any{"userId": "42", "account": "user@ilanzou"}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	redirectURL = srv.URL + "/media/movie.mp4"
	ILANZOU_CONF.Base = srv.URL

	driveID := "ilanzou:42"
	drive.RememberFile("ilanzou_42", driveID, model.File{DriveID: driveID, FileID: "8", Name: "movie.mp4", Size: 4096})
	tok := &model.TokenInfo{
		TokenFrom: model.ProviderIlanzou, AccessToken: "old-token", DeviceID: "uuid-old",
		UserID: "ilanzou_42", ProviderAccountID: "42", DefaultDriveID: driveID,
		RefreshToken: `{"username":"user","password":"pass","uuid":"uuid-old","token":"old-token","userId":"42","account":"user@ilanzou"}`,
	}
	c := drive.Context{UserID: tok.UserID, DriveID: driveID, Token: tok}

	d := &Driver{}
	u, err := d.GetDownloadURL(context.Background(), c, "8", 0)
	if err != nil {
		t.Fatal(err)
	}
	if u.URL != srv.URL+"/media/movie.mp4" || u.Size != 4096 {
		t.Fatalf("download = %+v", u)
	}
	preview, err := d.GetVideoPreview(context.Background(), c, "8")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Qualities) != 1 || !preview.Qualities[0].ForceProxy || preview.Qualities[0].URL != u.URL {
		t.Fatalf("preview = %+v", preview)
	}
	if tok.AccessToken != "new-token" || loginCalls != 1 {
		t.Fatalf("session = %+v, login calls = %d", tok, loginCalls)
	}
}

type uploadRewriteTransport struct {
	base http.RoundTripper
	host string
}

func (t uploadRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if req.URL.Host != "upload.qiniup.com" {
		return base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = "http"
	u.Host = t.host
	clone.URL = &u
	return base.RoundTrip(clone)
}

func TestUploadOneFileUsesActualSizeAndCompletes(t *testing.T) {
	withNoThrottle(t)
	oldBase, oldClient := ILANZOU_CONF.Base, httpClient
	t.Cleanup(func() {
		ILANZOU_CONF.Base = oldBase
		httpClient = oldClient
	})

	var qiniuCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/7n/getUpToken"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "upToken": "up-token"})
		case r.URL.Path == "/" && r.Method == http.MethodPost:
			qiniuCalls++
			if err := r.ParseMultipartForm(32 << 20); err != nil || r.FormValue("token") != "up-token" {
				http.Error(w, "invalid multipart", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "commit-token"})
		case strings.HasSuffix(r.URL.Path, "/7n/results"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "list": []any{map[string]any{"status": 1, "fileId": "uploaded-1"}}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ILANZOU_CONF.Base = srv.URL
	httpClient = &http.Client{Transport: uploadRewriteTransport{host: strings.TrimPrefix(srv.URL, "http://")}}

	path := t.TempDir() + "/small.bin"
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path, ParentFileID: "0", Name: "small.bin", Size: 999,
	}, Upload: model.UploadState{DownState: "queued"}}
	c := drive.Context{UserID: "ilanzou_42", DriveID: "ilanzou:42", Token: &model.TokenInfo{
		AccessToken: "api-token", DeviceID: "uuid-42",
	}}
	if err := (&Driver{}).UploadOneFile(context.Background(), c, ui); err != nil {
		t.Fatal(err)
	}
	if ui.Info.Size != 4 || ui.Info.SizeStr != model.FormatBytes(4) || ui.Upload.DownSize != 4 || ui.Upload.FileID != "uploaded-1" || !ui.Upload.IsCompleted {
		t.Fatalf("upload state = %+v, info = %+v", ui.Upload, ui.Info)
	}
	if qiniuCalls != 1 {
		t.Fatalf("qiniu calls = %d, want 1", qiniuCalls)
	}
}

func TestRapidUploadByHashUsesGetUpToken(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/7n/getUpToken") {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "fileId": "rapid-1"})
	}))
	defer srv.Close()
	ILANZOU_CONF.Base = srv.URL

	result, err := (&Driver{}).RapidUploadByHash(context.Background(), drive.Context{
		UserID: "ilanzou_42", DriveID: "ilanzou:42",
		Token: &model.TokenInfo{AccessToken: "api-token", DeviceID: "uuid-42"},
	}, drive.RapidUploadRequest{ParentID: "9", FileName: "movie.mp4", Method: "md5", Hash: "d41d8cd98f00b204e9800998ecf8427e", Size: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Reuse || result.FileID != "rapid-1" || result.ParentID != "9" {
		t.Fatalf("rapid result = %+v", result)
	}
	if body["md5"] != "d41d8cd98f00b204e9800998ecf8427e" || body["fileName"] != "movie.mp4" || body["folderId"] != "9" {
		t.Fatalf("getUpToken body = %#v", body)
	}
}

func TestResolveTransferHashStreamsDownload(t *testing.T) {
	withNoThrottle(t)
	oldBase := ILANZOU_CONF.Base
	t.Cleanup(func() { ILANZOU_CONF.Base = oldBase })

	var redirectURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/file/redirect") {
			w.Header().Set("Location", redirectURL)
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.URL.Path == "/media/movie.mp4" {
			_, _ = w.Write([]byte("data"))
			return
		}
		http.Error(w, "unexpected path", http.StatusNotFound)
	}))
	defer srv.Close()
	redirectURL = srv.URL + "/media/movie.mp4"
	ILANZOU_CONF.Base = srv.URL

	hash, err := (&Driver{}).ResolveTransferHash(context.Background(), drive.Context{
		UserID: "ilanzou_42", DriveID: "ilanzou:42",
		Token: &model.TokenInfo{AccessToken: "api-token", DeviceID: "uuid-42"},
	}, "8", "md5", true)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "8d777f385d3dfec8815d20f7496026dc" {
		t.Fatalf("hash = %q", hash)
	}
}
