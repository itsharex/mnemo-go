package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	s3pkg "mnemo-go/internal/drive/providers/s3"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// TestYikeListMock exercises the yike driver against a mock photo.baidu.com.
func TestYikeListMock(t *testing.T) {
	mock := MockAPI(t, "photo.baidu.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/youai/user/v1/getuinfo":
			json.NewEncoder(w).Encode(map[string]any{"errno": 0, "youa_id": "12345", "uk": "12345"})
		case "/youai/album/v1/list":
			json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []map[string]any{
				{"album_id": "a1", "title": "旅行", "ctime": 1700000000},
			}, "cursor": ""})
		case "/youai/file/v1/list":
			json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []map[string]any{}, "cursor": ""})
		default:
			json.NewEncoder(w).Encode(map[string]any{"errno": 0})
		}
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "yike", &model.TokenInfo{
		AccessToken: "BDUSS=abc", TokenFrom: "yike", UserID: "yike_test",
		RefreshToken: `{"cookie":"BDUSS=abc","uk":"12345"}`,
	})

	names := listNames(t, uid, did, "yike_root")
	if len(names) != 1 || names[0] != "旅行" {
		t.Fatalf("yike names: %v", names)
	}
}

func TestYikeLoginAllowsMissingBdstoken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/youai/user/v1/getuinfo" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "youa_id": "12345", "uk": "12345"})
			return
		}
		// Simulate a temporarily unavailable optional pan.baidu.com endpoint.
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	host := stripSchemeHost(srv.URL)
	netx.TestTransportHook = yikeAuthRewriteRT{mockHost: host}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("yike")
	if !ok || reg.Auth == nil {
		t.Fatal("yike auth is not registered")
	}
	tok, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{"cookie": "BDUSS=valid"}})
	if err != nil {
		t.Fatalf("login should not depend on bdstoken: %v", err)
	}
	if tok == nil || tok.ProviderAccountID != "12345" {
		t.Fatalf("unexpected yike token: %+v", tok)
	}
	var sess map[string]any
	if err := json.Unmarshal([]byte(tok.RefreshToken), &sess); err != nil {
		t.Fatal(err)
	}
	if _, exists := sess["bdstoken"]; exists && sess["bdstoken"] != "" {
		t.Fatalf("unexpected bdstoken: %v", sess["bdstoken"])
	}
}

type yikeAuthRewriteRT struct{ mockHost string }

func (r yikeAuthRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != "photo.baidu.com" && req.URL.Hostname() != "pan.baidu.com" {
		return http.DefaultTransport.RoundTrip(req)
	}
	u := *req.URL
	u.Scheme = "http"
	u.Host = r.mockHost
	req2 := req.Clone(req.Context())
	req2.URL = &u
	req2.RequestURI = ""
	return http.DefaultTransport.RoundTrip(req2)
}

func TestOneDriveLoginMock(t *testing.T) {
	tokenForms := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/common/oauth2/v2.0/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			tokenForms <- r.Form
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "onedrive-access",
				"refresh_token": "onedrive-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
			})
		case "/v1.0/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "account-1", "displayName": "OneDrive User", "userPrincipalName": "user@example.com",
			})
		case "/v1.0/me/drive":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "drive-1", "quota": map[string]any{"total": 1000, "used": 250},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = oneDriveAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("onedrive")
	if !ok || reg.Auth == nil {
		t.Fatal("onedrive auth is not registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callbackResult := make(chan error, 1)
	var authURL string
	tok, err := reg.Auth(ctx, drive.AuthRequest{
		Config: map[string]string{},
		Open: func(raw string) error {
			authURL = raw
			parsed, parseErr := url.Parse(raw)
			if parseErr != nil {
				return parseErr
			}
			go func() {
				callbackResult <- sendOneDriveCallback(ctx, parsed.Query().Get("redirect_uri"), parsed.Query().Get("state"))
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("OneDrive login: %v", err)
	}
	if tok == nil || tok.AccessToken != "onedrive-access" || tok.RefreshToken != "onedrive-refresh" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	if tok.ProviderAccountID != "account-1" || tok.UserID != "onedrive_account-1" || tok.DefaultDriveID != "onedrive:drive-1" {
		t.Fatalf("unexpected account identity: %+v", tok)
	}
	if tok.DeviceID != "b15665d9-eda6-4092-8539-0eec376afd59" {
		t.Fatalf("unexpected OAuth client id: %q", tok.DeviceID)
	}
	select {
	case callbackErr := <-callbackResult:
		if callbackErr != nil {
			t.Fatalf("OAuth callback: %v", callbackErr)
		}
	case <-ctx.Done():
		t.Fatalf("OAuth callback result: %v", ctx.Err())
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != tok.DeviceID || q.Get("prompt") != "select_account" {
		t.Fatalf("auth URL account selection fields: %v", q)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("auth URL PKCE fields: %v", q)
	}
	if q.Get("scope") != "Files.ReadWrite offline_access User.Read" {
		t.Fatalf("auth URL scope: %q", q.Get("scope"))
	}
	select {
	case form := <-tokenForms:
		if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != tok.DeviceID {
			t.Fatalf("token form: %v", form)
		}
		if form.Get("client_secret") != "qtyfaBBYA403=unZUP40~_#" || form.Get("code_verifier") == "" {
			t.Fatalf("token credentials/PKCE form: %v", form)
		}
	case <-ctx.Done():
		t.Fatalf("token form result: %v", ctx.Err())
	}
}

func TestOneDriveRefreshMock(t *testing.T) {
	tokenForms := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/common/oauth2/v2.0/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse refresh form: %v", err)
			}
			tokenForms <- r.Form
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-access", "expires_in": 1800, "token_type": "Bearer"})
		case "/v1.0/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "account-1", "displayName": "OneDrive User"})
		case "/v1.0/me/drive":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "drive-1", "quota": map[string]any{"total": 2000, "used": 500}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = oneDriveAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("onedrive")
	if !ok {
		t.Fatal("onedrive provider is not registered")
	}
	tok := &model.TokenInfo{
		TokenFrom: "onedrive", AccessToken: "expired-access", RefreshToken: "stored-refresh",
		TokenType: "Bearer", ExpiresIn: 1, DeviceID: "b15665d9-eda6-4092-8539-0eec376afd59",
		ProviderAccountID: "account-1", UserID: "onedrive_account-1", DefaultDriveID: "onedrive:drive-1",
	}
	driver := reg.Factory()
	got, err := driver.RefreshAccount(context.Background(), drive.Context{UserID: tok.UserID, DriveID: tok.DefaultDriveID, Token: tok}, tok)
	if err != nil {
		t.Fatalf("OneDrive refresh: %v", err)
	}
	if got != tok || tok.AccessToken != "refreshed-access" || tok.RefreshToken != "stored-refresh" || tok.ExpiresIn != 1800 {
		t.Fatalf("unexpected refreshed token: %+v", tok)
	}
	if tok.UserID != "onedrive_account-1" || tok.DefaultDriveID != "onedrive:drive-1" || tok.UsedSize != 500 || tok.TotalSize != 2000 {
		t.Fatalf("refresh changed account identity/quota incorrectly: %+v", tok)
	}
	select {
	case form := <-tokenForms:
		if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "stored-refresh" {
			t.Fatalf("refresh form: %v", form)
		}
		if form.Get("client_id") != tok.DeviceID || form.Get("client_secret") != "qtyfaBBYA403=unZUP40~_#" {
			t.Fatalf("refresh credentials form: %v", form)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh token form not observed")
	}
}

func TestOneDriveRefreshMockPreservesExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/common/oauth2/v2.0/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse refresh form: %v", err)
		}
		// Some OAuth providers omit expires_in when only rotating the access token.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-access",
			"token_type":   "Bearer",
		})
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = oneDriveAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("onedrive")
	if !ok {
		t.Fatal("onedrive provider is not registered")
	}
	tok := &model.TokenInfo{
		TokenFrom: "onedrive", AccessToken: "expired-access", RefreshToken: "stored-refresh",
		TokenType: "Bearer", ExpiresIn: 7200, DeviceID: "b15665d9-eda6-4092-8539-0eec376afd59",
	}
	if _, err := reg.Factory().RefreshAccount(context.Background(), drive.Context{UserID: "onedrive_user", Token: tok}, tok); err != nil {
		t.Fatalf("OneDrive refresh: %v", err)
	}
	if tok.AccessToken != "refreshed-access" || tok.RefreshToken != "stored-refresh" || tok.ExpiresIn != 7200 {
		t.Fatalf("refresh did not preserve token fields: %+v", tok)
	}
}

func TestOneDriveSearchEscapesODataKeyword(t *testing.T) {
	seenPath := make(chan string, 1)
	MockAPI(t, "graph.microsoft.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.0/me/drive/root/search(q='O''Reilly & plan')" {
			t.Errorf("search path = %q, want OData-escaped keyword", r.URL.Path)
		}
		seenPath <- r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": []map[string]any{{"id": "search-file", "name": "O'Reilly plan.txt", "file": map[string]any{}}},
		})
	}))

	uid, did, _ := SeedAccount(t, "onedrive", &model.TokenInfo{
		TokenFrom: "onedrive", AccessToken: "search-access", RefreshToken: "search-refresh",
		UserID: "onedrive_search_test", DefaultDriveID: "onedrive:drive-search",
	})
	files, err := drive.SearchDir(uid, did, "O'Reilly & plan")
	if err != nil {
		t.Fatalf("OneDrive search: %v", err)
	}
	if len(files) != 1 || files[0].Name != "O'Reilly plan.txt" {
		t.Fatalf("unexpected OneDrive search result: %#v", files)
	}
	select {
	case <-seenPath:
	case <-time.After(5 * time.Second):
		t.Fatal("OneDrive search request was not observed")
	}
}

func TestOneDriveUploadConflictSkipAndResponseValidation(t *testing.T) {
	phase := 0
	mock := MockAPI(t, "graph.microsoft.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1.0/me/drive/root:/same.txt:/content" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("@microsoft.graph.conflictBehavior"); got != "fail" && phase == 0 {
			t.Errorf("conflict behavior = %q, want fail", got)
		}
		if phase == 0 {
			phase++
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":    "nameAlreadyExists",
					"message": "An item with the same name already exists.",
				},
			})
			return
		}
		// A 2xx response without an item id must not be reported as a completed
		// upload: the queue needs the id to refresh its file state.
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))

	uid, did, _ := SeedAccount(t, "onedrive", &model.TokenInfo{
		TokenFrom: "onedrive", AccessToken: "onedrive-upload-access", RefreshToken: "upload-refresh",
		UserID: "onedrive_upload_test", DefaultDriveID: "onedrive:upload-drive",
	})
	path := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(path, []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := drive.Context{UserID: uid, DriveID: did, Token: &model.TokenInfo{AccessToken: "onedrive-upload-access"}}
	driver := drive.New("onedrive")

	skip := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path, ParentFileID: "onedrive_root", DriveID: did, Name: "same.txt", ConflictPolicy: "skip",
	}}
	if err := driver.UploadOneFile(context.Background(), ctx, skip); err != nil {
		t.Fatalf("skip conflict should succeed: %v", err)
	}

	overwrite := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path, ParentFileID: "onedrive_root", DriveID: did, Name: "same.txt", ConflictPolicy: "overwrite",
	}}
	if err := driver.UploadOneFile(context.Background(), ctx, overwrite); err == nil || !strings.Contains(err.Error(), "missing file id") {
		t.Fatalf("missing upload id should fail explicitly, got %v", err)
	}
	_ = mock
}

func TestDropboxLoginMock(t *testing.T) {
	tokenForms := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			tokenForms <- r.Form
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "dropbox-access", "refresh_token": "dropbox-refresh",
				"expires_in": 14400, "token_type": "bearer",
			})
		case "/2/users/get_current_account":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"account_id": "db-account-1", "email": "user@example.com",
				"name": map[string]any{"display_name": "Dropbox User"},
			})
		case "/2/users/get_space_usage":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"used": 300, "allocation": map[string]any{"allocated": 1200},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = dropboxAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("dropbox")
	if !ok || reg.Auth == nil {
		t.Fatal("dropbox auth is not registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	callbackResult := make(chan error, 1)
	var authURL string
	tok, err := reg.Auth(ctx, drive.AuthRequest{
		Config: map[string]string{},
		Open: func(raw string) error {
			authURL = raw
			parsed, parseErr := url.Parse(raw)
			if parseErr != nil {
				return parseErr
			}
			go func() {
				callbackResult <- sendOneDriveCallback(ctx, parsed.Query().Get("redirect_uri"), parsed.Query().Get("state"))
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Dropbox login: %v", err)
	}
	if tok == nil || tok.AccessToken != "dropbox-access" || tok.RefreshToken != "dropbox-refresh" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	if tok.ProviderAccountID != "db-account-1" || tok.UserID != "dropbox_db-account-1" || tok.DefaultDriveID != "dropbox:db-account-1" {
		t.Fatalf("unexpected account identity: %+v", tok)
	}
	if tok.DeviceID != "5jcck7diasz0rqy" || tok.UserName != "Dropbox User" || tok.TotalSize != 1200 || tok.UsedSize != 300 {
		t.Fatalf("unexpected account profile: %+v", tok)
	}
	select {
	case callbackErr := <-callbackResult:
		if callbackErr != nil {
			t.Fatalf("OAuth callback: %v", callbackErr)
		}
	case <-ctx.Done():
		t.Fatalf("OAuth callback result: %v", ctx.Err())
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != tok.DeviceID || q.Get("token_access_type") != "offline" || q.Get("state") == "" {
		t.Fatalf("auth URL fields: %v", q)
	}
	if q.Get("code_challenge") != "" || q.Get("code_challenge_method") != "" {
		t.Fatalf("rclone confidential client must not use PKCE: %v", q)
	}
	select {
	case form := <-tokenForms:
		if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != tok.DeviceID {
			t.Fatalf("token form: %v", form)
		}
		if form.Get("client_secret") != "1n9m04y2zx7bf26" || form.Get("code_verifier") != "" {
			t.Fatalf("token credentials form: %v", form)
		}
	case <-ctx.Done():
		t.Fatalf("token form result: %v", ctx.Err())
	}
}

func TestDropboxRefreshMockPreservesExpiry(t *testing.T) {
	tokenForms := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse refresh form: %v", err)
			}
			tokenForms <- r.Form
			// Dropbox may omit refresh_token/expires_in when rotating only the access token.
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "dropbox-refreshed", "token_type": "bearer"})
		case "/2/users/get_current_account":
			_ = json.NewEncoder(w).Encode(map[string]any{"account_id": "db-account-1", "name": map[string]any{"display_name": "Dropbox User"}})
		case "/2/users/get_space_usage":
			_ = json.NewEncoder(w).Encode(map[string]any{"used": 450, "allocation": map[string]any{"allocated": 1500}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = dropboxAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("dropbox")
	if !ok {
		t.Fatal("dropbox provider is not registered")
	}
	tok := &model.TokenInfo{
		TokenFrom: "dropbox", AccessToken: "expired", RefreshToken: "stored-refresh",
		TokenType: "Bearer", ExpiresIn: 14400, DeviceID: "5jcck7diasz0rqy",
		ProviderAccountID: "db-account-1", UserID: "dropbox_db-account-1", DefaultDriveID: "dropbox:db-account-1",
	}
	got, err := reg.Factory().RefreshAccount(context.Background(), drive.Context{UserID: tok.UserID, DriveID: tok.DefaultDriveID, Token: tok}, tok)
	if err != nil {
		t.Fatalf("Dropbox refresh: %v", err)
	}
	if got != tok || tok.AccessToken != "dropbox-refreshed" || tok.RefreshToken != "stored-refresh" || tok.ExpiresIn != 14400 {
		t.Fatalf("refresh did not preserve token fields: %+v", tok)
	}
	if tok.UserID != "dropbox_db-account-1" || tok.DefaultDriveID != "dropbox:db-account-1" || tok.UsedSize != 450 || tok.TotalSize != 1500 {
		t.Fatalf("refresh changed account identity/quota incorrectly: %+v", tok)
	}
	select {
	case form := <-tokenForms:
		if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "stored-refresh" {
			t.Fatalf("refresh form: %v", form)
		}
		if form.Get("client_id") != tok.DeviceID || form.Get("client_secret") != "1n9m04y2zx7bf26" {
			t.Fatalf("refresh credentials form: %v", form)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh token form not observed")
	}
}

func TestDropboxMoveCopyAllowSharedFolder(t *testing.T) {
	seen := make(chan map[string]any, 2)
	MockAPI(t, "api.dropboxapi.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/files/move_v2" && r.URL.Path != "/2/files/copy_v2" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Dropbox %s body: %v", r.URL.Path, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["allow_shared_folder"] != true {
			t.Errorf("Dropbox %s body missing allow_shared_folder: %#v", r.URL.Path, body)
		}
		seen <- body
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))

	uid, did, _ := SeedAccount(t, "dropbox", &model.TokenInfo{
		TokenFrom: "dropbox", AccessToken: "dropbox-file-access", RefreshToken: "dropbox-file-refresh",
		UserID: "dropbox_file_ops_test", DefaultDriveID: "dropbox:drive-file-ops",
	})
	refs := []drive.FileRef{{ID: "/source/movie.mp4"}}
	if _, err := drive.MoveBatch(uid, did, refs, "/shared-target", ""); err != nil {
		t.Fatalf("Dropbox move: %v", err)
	}
	if _, err := drive.CopyBatch(uid, did, refs, "/shared-target", ""); err != nil {
		t.Fatalf("Dropbox copy: %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case body := <-seen:
			if body["to_path"] != "/shared-target/movie.mp4" {
				t.Fatalf("Dropbox operation target = %#v", body["to_path"])
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Dropbox move/copy request was not observed")
		}
	}
}

type dropboxAuthRewriteRT struct{ mockHost string }

func (r dropboxAuthRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != "api.dropboxapi.com" {
		return http.DefaultTransport.RoundTrip(req)
	}
	u := *req.URL
	u.Scheme = "http"
	u.Host = r.mockHost
	req2 := req.Clone(req.Context())
	req2.URL = &u
	req2.RequestURI = ""
	return http.DefaultTransport.RoundTrip(req2)
}

type oneDriveAuthRewriteRT struct{ mockHost string }

func (r oneDriveAuthRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != "login.microsoftonline.com" && req.URL.Hostname() != "graph.microsoft.com" {
		return http.DefaultTransport.RoundTrip(req)
	}
	u := *req.URL
	u.Scheme = "http"
	u.Host = r.mockHost
	req2 := req.Clone(req.Context())
	req2.URL = &u
	req2.RequestURI = ""
	return http.DefaultTransport.RoundTrip(req2)
}

func sendOneDriveCallback(ctx context.Context, redirectURI, state string) error {
	if strings.TrimSpace(redirectURI) == "" || strings.TrimSpace(state) == "" {
		return fmt.Errorf("callback parameters missing")
	}
	u, err := url.Parse(redirectURI)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("code", "authorization-code")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("callback http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			return readErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestGuangyaListMock exercises the guangya driver against a mock api.guangyapan.com.
func TestGuangyaListMock(t *testing.T) {
	mock := MockAPI(t, "api.guangyapan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userres/v1/file/get_file_list" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"list": []map[string]any{
						{"fileId": "f1", "fileName": "photo.jpg", "fileSize": 800, "resType": 1, "cTime": 1700000000, "uTime": 1700000000},
						{"fileId": "f2", "fileName": "photos", "fileSize": 0, "resType": 2, "cTime": 1700000000, "uTime": 1700000000},
					},
					"total": 2,
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "guangya", &model.TokenInfo{
		AccessToken: "test-token", TokenFrom: "guangya", UserID: "guangya_test",
		RefreshToken: `{"access_token":"test-token","device_id":"dev","client_id":"aMe-8VSlkrbQXpUR"}`,
	})

	names := listNames(t, uid, did, "guangya_root")
	if len(names) != 2 || names[0] != "photo.jpg" || names[1] != "photos" {
		t.Fatalf("guangya names: %v", names)
	}
}

func TestGuangyaDownloadUsesLegacyEndpointAndSignedURL(t *testing.T) {
	var paths []string
	mock := MockAPI(t, "api.guangyapan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/nd.bizuserres.s/v1/get_res_download_url" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode guangya download body: %v", err)
		}
		if body["fileId"] != "guangya-file-1" {
			t.Errorf("guangya download body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"signedURL": "https://cdn.example.test/guangya-file-1.mp4"},
		})
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "guangya", &model.TokenInfo{
		AccessToken: "guangya-download-access", TokenFrom: "guangya", UserID: "guangya_download_test",
		RefreshToken: `{"access_token":"guangya-download-access","refresh_token":"guangya-download-refresh","device_id":"download-device","client_id":"aMe-8VSlkrbQXpUR"}`,
	})
	download, err := drive.GetDownloadURL(uid, did, "guangya-file-1", 3600)
	if err != nil {
		t.Fatalf("guangya download: %v", err)
	}
	if download.URL != "https://cdn.example.test/guangya-file-1.mp4" || download.DownloadMode != "proxy" {
		t.Fatalf("guangya download = %#v", download)
	}
	if len(paths) != 1 || paths[0] != "/nd.bizuserres.s/v1/get_res_download_url" {
		t.Fatalf("guangya download paths = %v", paths)
	}
}

func TestGuangyaResolveTransferHashUsesFileInfo(t *testing.T) {
	mock := MockAPI(t, "api.guangyapan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/userres/v1/file/get_file_detail" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode guangya detail body: %v", err)
		}
		if body["fileId"] != "guangya-hash-file" {
			t.Errorf("guangya detail body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"fileInfo": map[string]any{
					"fileId": "guangya-hash-file", "fileName": "hash.bin", "fileSize": 12,
					"resType": 1, "md5": "0123456789abcdef0123456789abcdef",
				},
			},
		})
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "guangya", &model.TokenInfo{
		AccessToken: "guangya-hash-access", TokenFrom: "guangya", UserID: "guangya_hash_test",
		RefreshToken: `{"access_token":"guangya-hash-access","refresh_token":"guangya-hash-refresh","device_id":"hash-device","client_id":"aMe-8VSlkrbQXpUR"}`,
	})
	hash, err := drive.ResolveTransferHash(uid, did, "guangya-hash-file", "md5", false)
	if err != nil {
		t.Fatalf("guangya resolve hash: %v", err)
	}
	if hash != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("guangya hash = %q", hash)
	}
}

func TestGuangyaUploadUsesLocalFileSizeInPrecreate(t *testing.T) {
	var fileSize float64
	mock := MockAPI(t, "api.guangyapan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nd.bizuserres.s/v1/get_res_center_token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Res struct {
				FileSize float64 `json:"fileSize"`
			} `json:"res"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode guangya upload body: %v", err)
		}
		fileSize = body.Res.FileSize
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 400, "msg": "missing upload credentials"})
	}))
	_ = mock

	data := []byte("guangya upload size")
	path := filepath.Join(t.TempDir(), "size.txt")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	uid, did, _ := SeedAccount(t, "guangya", &model.TokenInfo{
		AccessToken: "guangya-upload-access", TokenFrom: "guangya", UserID: "guangya_upload_size_test",
		RefreshToken: `{"access_token":"guangya-upload-access","refresh_token":"guangya-upload-refresh","device_id":"upload-device","client_id":"aMe-8VSlkrbQXpUR"}`,
	})
	handler, err := drive.QueueUploadHandler(uid, did)
	if err != nil {
		t.Fatal(err)
	}
	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path, ParentFileID: "guangya_root", DriveID: did, Name: "size.txt",
	}}
	if err := handler(context.Background(), ui); err == nil {
		t.Fatal("guangya upload unexpectedly succeeded without upload credentials")
	}
	if fileSize != float64(len(data)) || ui.Info.Size != int64(len(data)) {
		t.Fatalf("guangya upload sizes = request %v, ui %d, want %d", fileSize, ui.Info.Size, len(data))
	}
}

func TestGuangyaRequestRefreshPersistsSession(t *testing.T) {
	var refreshCalls, listCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/token":
			refreshCalls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode guangya refresh body: %v", err)
			}
			if body["grant_type"] != "refresh_token" || body["refresh_token"] != "stored-refresh" {
				t.Errorf("unexpected guangya refresh body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refreshed-access",
				"refresh_token": "rotated-refresh",
			})
		case "/userres/v1/file/get_file_list":
			listCalls++
			if r.Header.Get("Authorization") != "Bearer refreshed-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"list":  []map[string]any{{"fileId": "f1", "fileName": "after-refresh.txt", "fileSize": 1, "resType": 1}},
					"total": 1,
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = guangyaAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	uid, did, st := SeedAccount(t, "guangya", &model.TokenInfo{
		AccessToken: "expired-access", TokenFrom: "guangya", UserID: "guangya_refresh_test",
		RefreshToken: `{"access_token":"expired-access","refresh_token":"stored-refresh","device_id":"dev","client_id":"aMe-8VSlkrbQXpUR"}`,
	})
	drive.SetTokenUpdater(func(userID, _ string, token *model.TokenInfo) error {
		acc, err := st.GetAccount(userID)
		if err != nil {
			return err
		}
		acc.Token = token
		return st.SaveAccount(acc)
	})
	t.Cleanup(func() { drive.SetTokenUpdater(nil) })
	names := listNames(t, uid, did, "guangya_root")
	if len(names) != 1 || names[0] != "after-refresh.txt" {
		t.Fatalf("guangya names after refresh: %v", names)
	}
	if refreshCalls != 1 || listCalls != 2 {
		t.Fatalf("guangya refresh flow calls: refresh=%d list=%d", refreshCalls, listCalls)
	}
	acc, err := st.GetAccount(uid)
	if err != nil {
		t.Fatalf("load refreshed guangya account: %v", err)
	}
	if acc.Token.AccessToken != "refreshed-access" || !strings.Contains(acc.Token.RefreshToken, "rotated-refresh") {
		t.Fatalf("refreshed guangya session was not persisted: %+v", acc.Token)
	}
}

func TestAliOpenRequestRefreshPersistsSession(t *testing.T) {
	var refreshCalls, listCalls int
	mock := MockAPI(t, "openapi.alipan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/access_token":
			refreshCalls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode aliopen refresh body: %v", err)
			}
			if body["grant_type"] != "refresh_token" || body["refresh_token"] != "stored-refresh" {
				t.Errorf("unexpected aliopen refresh body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refreshed-access",
				"refresh_token": "rotated-refresh",
			})
		case "/adrive/v1.0/openFile/list":
			listCalls++
			if r.Header.Get("Authorization") == "Bearer expired-access" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "AccessTokenInvalid", "message": "expired"})
				return
			}
			if r.Header.Get("Authorization") != "Bearer refreshed-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items":       []map[string]any{{"file_id": "ali-file-1", "name": "after-refresh.txt", "parent_file_id": "root", "type": "file", "size": 1}},
				"next_marker": "",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock

	uid, did, st := SeedAccount(t, "aliopen", &model.TokenInfo{
		AccessToken: "expired-access", TokenFrom: "aliopen", UserID: "aliopen_refresh_test",
		RefreshToken: `{"access_token":"expired-access","refresh_token":"stored-refresh","drive_id":"drive-1","client_id":"test-client"}`,
	})
	drive.SetTokenUpdater(func(userID, _ string, token *model.TokenInfo) error {
		acc, err := st.GetAccount(userID)
		if err != nil {
			return err
		}
		acc.Token = token
		return st.SaveAccount(acc)
	})
	t.Cleanup(func() { drive.SetTokenUpdater(nil) })

	names := listNames(t, uid, did, "b:root")
	if len(names) != 1 || names[0] != "after-refresh.txt" {
		t.Fatalf("aliopen names after refresh: %v", names)
	}
	if refreshCalls != 1 || listCalls != 2 {
		t.Fatalf("aliopen refresh flow calls: refresh=%d list=%d", refreshCalls, listCalls)
	}
	acc, err := st.GetAccount(uid)
	if err != nil {
		t.Fatalf("load refreshed aliopen account: %v", err)
	}
	if acc.Token.AccessToken != "refreshed-access" || !strings.Contains(acc.Token.RefreshToken, "rotated-refresh") {
		t.Fatalf("refreshed aliopen session was not persisted: %+v", acc.Token)
	}
}

func TestAliOpenRequestRefreshesBusinessTokenError(t *testing.T) {
	var refreshCalls, listCalls int
	mock := MockAPI(t, "openapi.alipan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/access_token":
			refreshCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "business-refreshed-access",
				"refresh_token": "business-rotated-refresh",
			})
		case "/adrive/v1.0/openFile/list":
			listCalls++
			if listCalls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "AccessTokenInvalid", "message": "expired"})
				return
			}
			if r.Header.Get("Authorization") != "Bearer business-refreshed-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"file_id": "ali-file-2", "name": "business-code.txt", "parent_file_id": "root", "type": "file", "size": 1}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "aliopen", &model.TokenInfo{
		AccessToken: "business-expired-access", TokenFrom: "aliopen", UserID: "aliopen_business_refresh_test",
		RefreshToken: `{"access_token":"business-expired-access","refresh_token":"business-stored-refresh","drive_id":"drive-2","client_id":"test-client"}`,
	})
	names := listNames(t, uid, did, "b:root")
	if len(names) != 1 || names[0] != "business-code.txt" {
		t.Fatalf("aliopen business-code names: %v", names)
	}
	if refreshCalls != 1 || listCalls != 2 {
		t.Fatalf("aliopen business-code refresh flow calls: refresh=%d list=%d", refreshCalls, listCalls)
	}
}

func TestAliOpenDownloadPassesExpiryAndMapsExpiration(t *testing.T) {
	var expireSecs []int
	expiration := time.Now().Add(45 * time.Minute).UTC().Format(time.RFC3339)
	mock := MockAPI(t, "openapi.alipan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/adrive/v1.0/openFile/getDownloadUrl" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode aliopen download body: %v", err)
		}
		secs, ok := body["expire_sec"].(float64)
		if !ok {
			t.Fatalf("aliopen download body missing expire_sec: %#v", body)
		}
		expireSecs = append(expireSecs, int(secs))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url": "https://cdn.example.test/aliopen-origin.mp4?expire=1999999999",
			"streamsUrl": map[string]string{
				"mov":  "https://cdn.example.test/aliopen-live.mov?expire=1999999999",
				"jpeg": "https://cdn.example.test/aliopen-live.jpeg?expire=1999999999",
			},
			"size":       42,
			"expiration": expiration,
		})
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "aliopen", &model.TokenInfo{
		AccessToken: "ali-download-access", TokenFrom: "aliopen", UserID: "aliopen_download_test",
		RefreshToken: `{"access_token":"ali-download-access","refresh_token":"ali-download-refresh","drive_id":"drive-download"}`,
	})

	download, err := drive.GetDownloadURL(uid, did, "b:file-1", 3600)
	if err != nil {
		t.Fatalf("aliopen download: %v", err)
	}
	if download.URL != "https://cdn.example.test/aliopen-live.mov?expire=1999999999" || download.Size != 42 {
		t.Fatalf("aliopen download = %#v", download)
	}
	wantExpiration, err := time.Parse(time.RFC3339, expiration)
	if err != nil {
		t.Fatal(err)
	}
	if download.ExpireTime != wantExpiration.UnixMilli() {
		t.Fatalf("aliopen expiration = %d, want %d", download.ExpireTime, wantExpiration.UnixMilli())
	}

	preview, err := drive.GetVideoPreview(uid, did, "b:file-1")
	if err != nil {
		t.Fatalf("aliopen preview: %v", err)
	}
	if preview == nil || len(preview.Qualities) != 1 || preview.Qualities[0].URL == "" {
		t.Fatalf("aliopen preview = %#v", preview)
	}
	if len(expireSecs) != 2 || expireSecs[0] != 3600 || expireSecs[1] != 14400 {
		t.Fatalf("aliopen expire_sec calls = %v, want [3600 14400]", expireSecs)
	}
}

func TestAliOpenUploadFetchesMissingPartURLs(t *testing.T) {
	policies := drive.RegistryCaps("aliopen").UploadConflictPolicies
	for _, want := range []string{"refuse", "rename", "skip", "overwrite"} {
		found := false
		for _, policy := range policies {
			if policy == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("aliopen conflict policies = %v, missing %q", policies, want)
		}
	}
	var paths []string
	mock := MockAPI(t, "openapi.alipan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/adrive/v1.0/openFile/create":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode aliopen create body: %v", err)
			}
			if _, ok := body["part_info_list"]; !ok {
				t.Fatalf("create body missing part_info_list: %#v", body)
			}
			if body["check_name_mode"] != "fail" {
				t.Fatalf("create check_name_mode = %#v, want fail", body["check_name_mode"])
			}
			wantHash := strings.ToUpper(netx.SHA1Hex([]byte("hello aliopen")))
			if body["content_hash"] != wantHash || body["content_hash_name"] != "sha1" {
				t.Fatalf("create content hash = %#v/%#v, want %s/sha1", body["content_hash"], body["content_hash_name"], wantHash)
			}
			if body["pre_hash"] != wantHash {
				t.Fatalf("create pre_hash = %#v, want %s", body["pre_hash"], wantHash)
			}
			if _, ok := body["local_modified_at"].(string); !ok {
				t.Fatalf("create missing local_modified_at: %#v", body)
			}
			if _, ok := body["local_created_at"].(string); !ok {
				t.Fatalf("create missing local_created_at: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_id": "ali-upload-file", "upload_id": "ali-upload-session",
				"part_info_list": []map[string]any{{"part_number": 1}},
			})
		case "/adrive/v1.0/openFile/getUploadUrl":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"part_info_list": []map[string]any{{"part_number": 1, "upload_url": "https://openapi.alipan.com/upload/1"}},
			})
		case "/upload/1":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read aliopen part: %v", err)
			}
			if string(body) != "hello aliopen" {
				t.Errorf("aliopen part body = %q", body)
			}
			w.WriteHeader(http.StatusOK)
		case "/adrive/v1.0/openFile/complete":
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "ali-upload-file"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock

	path := t.TempDir() + "/hello.txt"
	if err := os.WriteFile(path, []byte("hello aliopen"), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, did, _ := SeedAccount(t, "aliopen", &model.TokenInfo{
		AccessToken: "ali-upload-access", TokenFrom: "aliopen", UserID: "aliopen_upload_test",
		RefreshToken: `{"access_token":"ali-upload-access","refresh_token":"ali-upload-refresh","drive_id":"drive-upload"}`,
	})
	handler, err := drive.QueueUploadHandler(uid, did)
	if err != nil {
		t.Fatal(err)
	}
	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path, ParentFileID: "b:root", DriveID: did, Name: "hello.txt", ConflictPolicy: "refuse",
	}}
	if err := handler(context.Background(), ui); err != nil {
		t.Fatalf("AliOpen UploadOneFile: %v", err)
	}
	if ui.Upload.DownSize != int64(len("hello aliopen")) || ui.Upload.DownProcess != 100 {
		t.Fatalf("aliopen upload progress = %d/%d", ui.Upload.DownSize, ui.Upload.DownProcess)
	}
	wantPaths := []string{
		"/adrive/v1.0/openFile/create",
		"/adrive/v1.0/openFile/getUploadUrl",
		"/upload/1",
		"/adrive/v1.0/openFile/complete",
	}
	if len(paths) != len(wantPaths) {
		t.Fatalf("aliopen upload request paths = %v, want %v", paths, wantPaths)
	}
	for i := range wantPaths {
		if paths[i] != wantPaths[i] {
			t.Fatalf("aliopen upload request paths = %v, want %v", paths, wantPaths)
		}
	}
}

func TestAliOpenRejectsMixedDriveScopes(t *testing.T) {
	var apiCalls int
	mock := MockAPI(t, "openapi.alipan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "aliopen", &model.TokenInfo{
		AccessToken: "ali-scope-access", TokenFrom: "aliopen", UserID: "aliopen_scope_test",
		RefreshToken: `{"access_token":"ali-scope-access","refresh_token":"ali-scope-refresh","drive_id":"drive-backup","resource_drive_id":"drive-resource"}`,
	})
	refs := []drive.FileRef{{ID: "b:file-1"}, {ID: "r:file-2"}}
	if _, err := drive.CopyBatch(uid, did, refs, "b:root", ""); err == nil || !strings.Contains(err.Error(), "不能跨备份盘与资源盘") {
		t.Fatalf("mixed-scope copy error = %v", err)
	}
	if _, err := drive.CreateShare(uid, did, drive.ShareParams{FileIDs: []string{"b:file-1", "r:file-2"}}); err == nil || !strings.Contains(err.Error(), "不能跨备份盘与资源盘") {
		t.Fatalf("mixed-scope share error = %v", err)
	}
	if apiCalls != 0 {
		t.Fatalf("mixed-scope validation made %d API calls", apiCalls)
	}
}

func TestGuangyaSendSmsUsesCaptchaToken(t *testing.T) {
	var initCalls, sendCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/shield/captcha/init":
			initCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"captcha_token": "captcha-1"})
		case "/v1/auth/verification":
			sendCalls++
			if r.Header.Get("X-Captcha-Token") != "captcha-1" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "captcha_invalid"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"verification_id": "verification-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = guangyaAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	d := drive.New("guangya")
	sender, ok := d.(interface {
		SendSms(context.Context, string) (string, string, string, error)
	})
	if !ok {
		t.Fatal("guangya driver does not expose SendSms")
	}
	verificationID, _, captchaToken, err := sender.SendSms(context.Background(), "138 0013 8000")
	if err != nil {
		t.Fatalf("send guangya sms: %v", err)
	}
	if verificationID != "verification-1" || captchaToken != "captcha-1" {
		t.Fatalf("unexpected sms result: verification=%q captcha=%q", verificationID, captchaToken)
	}
	if initCalls != 1 || sendCalls != 1 {
		t.Fatalf("unexpected captcha flow calls: init=%d send=%d", initCalls, sendCalls)
	}
}

func TestGuangyaSmsLoginForwardsCaptchaToken(t *testing.T) {
	var verifyCalls, signInCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/assets/v1/get_assets" && r.Header.Get("X-Captcha-Token") != "captcha-login" {
			t.Fatalf("captcha header on %s = %q", r.URL.Path, r.Header.Get("X-Captcha-Token"))
		}
		switch r.URL.Path {
		case "/v1/auth/verification/verify":
			verifyCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"verification_token": "verification-token"})
		case "/v1/auth/signin":
			signInCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "sms-access", "refresh_token": "sms-refresh",
			})
		case "/assets/v1/get_assets":
			if r.Header.Get("Did") != "dev-login" || r.Header.Get("Dt") != "4" {
				t.Fatalf("asset headers Did/Dt = %q/%q", r.Header.Get("Did"), r.Header.Get("Dt"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"usedSpaceSize": "12", "totalSpaceSize": "100"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = guangyaAuthRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("guangya")
	if !ok || reg.Auth == nil {
		t.Fatal("guangya auth is not registered")
	}
	tok, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{
		"phone": "13800138000", "sms_code": "123456", "verification_id": "vid",
		"device_id": "dev-login", "captcha_token": "captcha-login",
	}})
	if err != nil {
		t.Fatalf("guangya sms login: %v", err)
	}
	if tok.AccessToken != "sms-access" || tok.UsedSize != 12 || tok.TotalSize != 100 || verifyCalls != 1 || signInCalls != 1 {
		t.Fatalf("unexpected guangya login: token=%+v verify=%d signin=%d", tok, verifyCalls, signInCalls)
	}
}

type guangyaAuthRewriteRT struct{ mockHost string }

func (r guangyaAuthRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != "api.guangyapan.com" && req.URL.Hostname() != "account.guangyapan.com" {
		return http.DefaultTransport.RoundTrip(req)
	}
	u := *req.URL
	u.Scheme = "http"
	u.Host = r.mockHost
	req2 := req.Clone(req.Context())
	req2.URL = &u
	req2.RequestURI = ""
	return http.DefaultTransport.RoundTrip(req2)
}

// TestPan139ListMock exercises the pan139 driver against a mock API host.
// The personalCloudHost is stored in the token and points at the mocked host,
// so /file/list is served by the same mock.
func TestPan139ListMock(t *testing.T) {
	// Cloud SSO expiry is field 4; metadata can follow it.
	authorization := "Basic " + base64Std("user:account:tok|a|b|4102444800000|metadata")

	mock := MockAPI(t, "api.mail.10086.cn", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file/list" {
			var request struct {
				ParentFileID string `json:"parentFileId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode list body: %v", err)
			}
			if request.ParentFileID != "/" {
				t.Errorf("parentFileId = %q, want /", request.ParentFileID)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"items": []map[string]any{
						{"fileId": "f1", "name": "data.zip", "size": 900, "updateTime": "2026-01-01 00:00:00", "contentType": "application/zip"},
						{"fileId": "f2", "name": "backup", "size": 0, "updateTime": "2026-01-01 00:00:00", "contentType": "folder"},
					},
					"nextPageCursor": "",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "pan139", &model.TokenInfo{
		AccessToken: authorization, TokenFrom: "pan139", UserID: "pan139_test",
		RefreshToken: `{"authorization":"` + authorization + `","account":"account","personalCloudHost":"https://api.mail.10086.cn"}`,
	})

	names := listNames(t, uid, did, "pan139_personal_root")
	if len(names) != 2 || names[0] != "data.zip" {
		t.Fatalf("pan139 names: %v", names)
	}
}

// TestPan139UploadMock verifies the newer SHA-256 precreate upload protocol:
// create -> presigned PUT -> complete.
func TestPan139UploadMock(t *testing.T) {
	authorization := "Basic " + base64Std("user:account:tok|a|b|4102444800000|metadata")
	var paths []string
	var createBody map[string]any
	mock := MockAPI(t, "api.mail.10086.cn", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/file/create":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"fileId": "file-1", "uploadId": "upload-1", "fileName": "hello.txt",
					"partInfos": []map[string]any{{"partNumber": 1, "uploadUrl": "https://api.mail.10086.cn/upload/1"}},
				},
			})
		case "/upload/1":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read upload body: %v", err)
			}
			if string(body) != "hello 139" {
				t.Errorf("uploaded body = %q", body)
			}
			w.WriteHeader(http.StatusOK)
		case "/file/complete":
			json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock

	data := []byte("hello 139")
	path := t.TempDir() + "/hello.txt"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	uid, did, _ := SeedAccount(t, "pan139", &model.TokenInfo{
		AccessToken: authorization, TokenFrom: "pan139", UserID: "pan139_upload_test",
		RefreshToken: `{"authorization":"` + authorization + `","account":"account","personalCloudHost":"https://api.mail.10086.cn"}`,
	})
	handler, err := drive.QueueUploadHandler(uid, did)
	if err != nil {
		t.Fatal(err)
	}
	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path, ParentFileID: "pan139_root", DriveID: did, Name: "hello.txt",
	}}
	if err := handler(context.Background(), ui); err != nil {
		t.Fatalf("UploadOneFile: %v", err)
	}
	if ui.Upload.DownSize != int64(len(data)) || ui.Upload.DownProcess != 100 {
		t.Fatalf("progress = %d/%d", ui.Upload.DownSize, ui.Upload.DownProcess)
	}
	want := sha256.Sum256(data)
	if got, _ := createBody["contentHash"].(string); got != hex.EncodeToString(want[:]) {
		t.Fatalf("contentHash = %q", got)
	}
	if got, _ := createBody["parentFileId"].(string); got != "/" {
		t.Fatalf("parentFileId = %q", got)
	}
	wantPaths := []string{"/file/create", "/upload/1", "/file/complete"}
	if len(paths) != len(wantPaths) {
		t.Fatalf("request paths = %v, want %v", paths, wantPaths)
	}
	for i := range wantPaths {
		if paths[i] != wantPaths[i] {
			t.Fatalf("request paths = %v, want %v", paths, wantPaths)
		}
	}
}

// TestPan139FileOperationsMock verifies the新版 personal-cloud file APIs used
// by listing, detail, download, folder and batch management operations.
func TestPan139FileOperationsMock(t *testing.T) {
	drive.ClearFileMetaCache()
	authorization := "Basic " + base64Std("user:account:tok|a|b|4102444800000|metadata")
	var paths []string
	mock := MockAPI(t, "api.mail.10086.cn", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/file/list":
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"items":          []map[string]any{{"fileId": 101, "name": "movie.mp4", "size": "9", "type": "file", "updatedAt": "2026-01-01T00:00:00Z", "contentHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "contentHashAlgorithm": "SHA256"}},
					"nextPageCursor": "cursor-2",
				},
			})
		case "/file/get":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"fileId": 101, "name": "movie.mp4", "size": "9", "type": "file", "parentFileId": "/",
				"contentHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "contentHashAlgorithm": "SHA256",
			}})
		case "/file/getDownloadUrl":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"cdnUrl": "https://cdn.example.com/movie.mp4", "size": "9", "fileName": "movie.mp4"}})
		case "/file/create":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			if request["parentFileId"] != "/" || request["type"] != "folder" {
				t.Errorf("create body = %#v", request)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"fileId": 202, "name": "docs"}})
		case "/file/update", "/file/batchMove", "/file/batchCopy", "/recyclebin/batchTrash", "/file/batchDelete":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	uid, did, _ := SeedAccount(t, "pan139", &model.TokenInfo{
		AccessToken: authorization, TokenFrom: "pan139", UserID: "pan139_ops_test",
		RefreshToken: `{"authorization":"` + authorization + `","account":"account","personalCloudHost":"https://api.mail.10086.cn"}`,
	})

	page, err := drive.ListDirPage(uid, did, "pan139_personal_root", "", nil)
	if err != nil || page == nil || len(page.Items) != 1 || page.NextMarker != "cursor-2" {
		t.Fatalf("list page = %#v, err=%v", page, err)
	}
	file, err := drive.GetFile(uid, did, "102")
	if err != nil || file.ContentHashName != "sha256" || file.ContentHash == "" {
		t.Fatalf("file = %#v, err=%v", file, err)
	}
	download, err := drive.GetDownloadURL(uid, did, "101", 3600)
	if err != nil || download.URL != "https://cdn.example.com/movie.mp4" || download.Size != 9 {
		t.Fatalf("download = %#v, err=%v", download, err)
	}
	folder, err := drive.Mkdir(uid, did, "pan139_personal_root", "docs")
	if err != nil || folder.FileID != "pan139:personal:202" || folder.Error != "" {
		t.Fatalf("mkdir = %#v, err=%v", folder, err)
	}
	if _, err := drive.RenameBatch(uid, did, []drive.FileRef{{ID: "101"}}, []string{"renamed.mp4"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := drive.MoveBatch(uid, did, []drive.FileRef{{ID: "101"}}, "pan139_personal_root", ""); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := drive.CopyBatch(uid, did, []drive.FileRef{{ID: "101"}}, "pan139_personal_root", ""); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if _, err := drive.TrashBatch(uid, did, []string{"101"}); err != nil {
		t.Fatalf("trash: %v", err)
	}
	if _, err := drive.DeleteBatch(uid, did, []drive.FileRef{{ID: "101"}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wantPaths := []string{"/file/list", "/file/get", "/file/getDownloadUrl", "/file/create", "/file/update", "/file/batchMove", "/file/batchCopy", "/recyclebin/batchTrash", "/file/batchDelete"}
	if len(paths) != len(wantPaths) {
		t.Fatalf("request paths = %v, want %v", paths, wantPaths)
	}
	for i := range wantPaths {
		if paths[i] != wantPaths[i] {
			t.Fatalf("request paths = %v, want %v", paths, wantPaths)
		}
	}
}

// TestS3ListMock exercises the s3 driver against a mock S3-compatible server.
func TestS3ValidatePrefixSkipsUnnecessaryHeadBucket(t *testing.T) {
	var methods []string
	mock := MockAPI(t, "s3-validate.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusForbidden)
		case http.MethodGet:
			if r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "allowed/" {
				http.Error(w, "unexpected validation query", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>test-bucket</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	_ = mock
	s3pkg.TransportOverride = netx.TestTransportHook
	t.Cleanup(func() { s3pkg.TransportOverride = nil })

	err := drive.ValidateConnection(model.ProviderS3, &model.ConnConfig{
		Endpoint: "s3-validate.example.com", Username: "key", Password: "secret",
		Bucket: "test-bucket", BasePath: "/allowed/",
	})
	if err != nil {
		t.Fatalf("S3 prefix-scoped validation: %v", err)
	}
	if strings.Join(methods, ",") != "GET" {
		t.Fatalf("validation methods = %v, want one prefix GET", methods)
	}
}

func TestS3ListMock(t *testing.T) {
	s3XML := `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>test-bucket</Name>
  <Prefix></Prefix>
  <KeyCount>2</KeyCount>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>report.pdf</Key>
    <LastModified>2026-01-01T00:00:00.000Z</LastModified>
    <ETag>&quot;abc&quot;</ETag>
    <Size>2048</Size>
    <StorageClass>STANDARD</StorageClass>
  </Contents>
  <CommonPrefixes>
    <Prefix>docs/</Prefix>
  </CommonPrefixes>
</ListBucketResult>`
	mock := MockAPI(t, "s3.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(s3XML))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock
	// route the AWS SDK's own transport to the mock as well
	s3pkg.TransportOverride = netx.TestTransportHook
	t.Cleanup(func() { s3pkg.TransportOverride = nil })

	uid, did, _ := SeedAccount(t, "s3", &model.TokenInfo{
		TokenFrom: "s3", UserID: "s3_test",
		Conn: &model.ConnConfig{
			Endpoint: "s3.example.com", Username: "key", Password: "secret",
			Bucket: "test-bucket",
		},
	})

	names := listNames(t, uid, did, "/")
	if len(names) != 2 {
		t.Fatalf("s3 names: %v", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if !seen["report.pdf"] || !seen["docs"] {
		t.Fatalf("s3 names: %v", names)
	}
}

func TestS3ListRejectsRepeatedContinuationToken(t *testing.T) {
	const s3XML = `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>test-bucket</Name>
  <IsTruncated>true</IsTruncated>
  <NextContinuationToken>loop</NextContinuationToken>
</ListBucketResult>`
	mock := MockAPI(t, "s3-loop.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(s3XML))
	}))
	_ = mock
	s3pkg.TransportOverride = netx.TestTransportHook
	t.Cleanup(func() { s3pkg.TransportOverride = nil })

	uid, did, _ := SeedAccount(t, "s3", &model.TokenInfo{
		TokenFrom: "s3", UserID: "s3_loop_test",
		Conn: &model.ConnConfig{
			Endpoint: "s3-loop.example.com", Username: "key", Password: "secret",
			Bucket: "test-bucket",
		},
	})
	_, err := drive.ListDir(uid, did, "/", nil)
	if err == nil || !strings.Contains(err.Error(), "游标重复") {
		t.Fatalf("expected repeated cursor error, got %v", err)
	}
}

func TestS3DownloadExpireTimeUsesMilliseconds(t *testing.T) {
	mock := MockAPI(t, "s3-expire.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "42")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock
	s3pkg.TransportOverride = netx.TestTransportHook
	t.Cleanup(func() { s3pkg.TransportOverride = nil })

	uid, did, _ := SeedAccount(t, "s3", &model.TokenInfo{
		TokenFrom: "s3", UserID: "s3_expire_test",
		Conn: &model.ConnConfig{Endpoint: "s3-expire.example.com", Username: "key", Password: "secret", Bucket: "test-bucket"},
	})
	started := time.Now().UnixMilli()
	download, err := drive.GetDownloadURL(uid, did, "/video.mp4", 3600)
	if err != nil {
		t.Fatalf("s3 download: %v", err)
	}
	wantMin := started + 3599*1000
	wantMax := started + 3601*1000
	if download.ExpireTime < wantMin || download.ExpireTime > wantMax {
		t.Fatalf("s3 expire_time = %d, want milliseconds around [%d, %d]", download.ExpireTime, wantMin, wantMax)
	}
}

func TestS3GetInfoHandlesHeadNotFound(t *testing.T) {
	mock := MockAPI(t, "s3-info.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Query().Get("prefix") == "dir/" {
				_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>test-bucket</Name><IsTruncated>false</IsTruncated>
  <Contents><Key>dir/child.txt</Key><Size>1</Size></Contents>
</ListBucketResult>`)
				return
			}
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>test-bucket</Name><IsTruncated>false</IsTruncated>
</ListBucketResult>`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock
	s3pkg.TransportOverride = netx.TestTransportHook
	t.Cleanup(func() { s3pkg.TransportOverride = nil })

	uid, did, _ := SeedAccount(t, "s3", &model.TokenInfo{
		TokenFrom: "s3", UserID: "s3_info_test",
		Conn: &model.ConnConfig{Endpoint: "s3-info.example.com", Username: "key", Password: "secret", Bucket: "test-bucket"},
	})
	if _, err := drive.GetFileInfo(uid, did, "/missing.txt"); !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("missing object error = %v, want drive.ErrNotFound", err)
	}
	raw, err := drive.GetFileInfo(uid, did, "/dir")
	if err != nil {
		t.Fatalf("directory info: %v", err)
	}
	f, ok := raw.(model.File)
	if !ok || !f.IsDir || f.Name != "dir" {
		t.Fatalf("directory info = %#v", raw)
	}
}

func TestS3UploadRenameSkipsExistingCandidates(t *testing.T) {
	var uploadedKey string
	mock := MockAPI(t, "s3-upload.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.EscapedPath(), "/test-bucket/")
		key, _ = url.PathUnescape(key)
		switch r.Method {
		case http.MethodHead:
			if key == "same.txt" || key == "same (1).txt" {
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>test-bucket</Name><IsTruncated>false</IsTruncated>
</ListBucketResult>`)
		case http.MethodPut:
			uploadedKey = key
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	s3pkg.TransportOverride = netx.TestTransportHook
	t.Cleanup(func() { s3pkg.TransportOverride = nil })

	uid, did, _ := SeedAccount(t, "s3", &model.TokenInfo{
		TokenFrom: "s3", UserID: "s3_upload_test",
		Conn: &model.ConnConfig{Endpoint: "s3-upload.example.com", Username: "key", Password: "secret", Bucket: "test-bucket"},
	})
	localPath := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(localPath, []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := drive.QueueUploadHandler(uid, did)
	if err != nil {
		t.Fatalf("QueueUploadHandler: %v", err)
	}
	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: localPath, ParentFileID: "/", Name: "same.txt", ConflictPolicy: "rename",
	}}
	if err := handler(context.Background(), ui); err != nil {
		t.Fatalf("rename upload: %v", err)
	}
	if ui.Info.Name != "same (2).txt" || uploadedKey != "same (2).txt" {
		t.Fatalf("renamed upload name/key = %q/%q", ui.Info.Name, uploadedKey)
	}
}

func TestS3CopyEncodesSpecialObjectName(t *testing.T) {
	const sourceKey = "a b?#中文.txt"
	var copySourceHeader string
	mock := MockAPI(t, "s3-copy.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.EscapedPath(), "/test-bucket/")
		key, _ = url.PathUnescape(key)
		switch r.Method {
		case http.MethodHead:
			if key == sourceKey {
				w.Header().Set("Content-Length", "3")
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>test-bucket</Name><IsTruncated>false</IsTruncated>
</ListBucketResult>`)
		case http.MethodPut:
			copySourceHeader = r.Header.Get("X-Amz-Copy-Source")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	s3pkg.TransportOverride = netx.TestTransportHook
	t.Cleanup(func() { s3pkg.TransportOverride = nil })

	uid, did, _ := SeedAccount(t, "s3", &model.TokenInfo{
		TokenFrom: "s3", UserID: "s3_copy_test",
		Conn: &model.ConnConfig{Endpoint: "s3-copy.example.com", Username: "key", Password: "secret", Bucket: "test-bucket"},
	})
	ids, err := drive.CopyBatch(uid, did, []drive.FileRef{{ID: "/" + sourceKey}}, "/dest", "")
	if err != nil || len(ids) != 1 {
		t.Fatalf("copy result = %#v, err = %v", ids, err)
	}
	decoded, err := url.PathUnescape(copySourceHeader)
	if err != nil {
		t.Fatalf("decode CopySource %q: %v", copySourceHeader, err)
	}
	if decoded != "/test-bucket/"+sourceKey {
		t.Fatalf("CopySource = %q (decoded %q), want %q", copySourceHeader, decoded, "/test-bucket/"+sourceKey)
	}
	for _, encoded := range []string{"%20", "%3F", "%23", "%E4%B8%AD"} {
		if !strings.Contains(copySourceHeader, encoded) {
			t.Fatalf("CopySource %q missing encoded segment %q", copySourceHeader, encoded)
		}
	}
}

func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

var _ = drive.ErrNotFound
