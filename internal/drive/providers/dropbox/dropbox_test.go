package dropbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
	"mnemo-go/internal/store"
)

// Explicitly enabled, read-only account diagnostic. Never logs account data,
// access tokens, filenames, cursors or returned metadata.
func TestDropboxLiveListDiagnostic(t *testing.T) {
	dir := os.Getenv("MNEMO_DROPBOX_DIAGNOSTIC_ACCOUNT_DIR")
	if dir == "" {
		t.Skip("live diagnostic disabled")
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal("cannot open account store")
	}
	accounts, err := st.ListAccounts()
	if err != nil {
		t.Fatal("cannot decode account store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, account := range accounts {
		if account.Token == nil || account.Token.TokenFrom != "dropbox" {
			continue
		}
		d := &Driver{}
		c := drive.Context{Token: account.Token}
		files, err := d.List(ctx, c, "root", nil)
		if err != nil {
			t.Fatalf("UI root listing failed: %v", err)
		}
		t.Logf("UI root listing succeeded: %d entries", len(files))
		page, err := d.ListPaged(ctx, c, "root", "", nil)
		if err != nil {
			t.Fatalf("UI root paged listing failed: %v", err)
		}
		t.Logf("UI root paged listing succeeded: %d entries", len(page.Items))
		for _, file := range files {
			if !file.IsDir {
				continue
			}
			children, err := d.List(ctx, c, file.FileID, nil)
			if err != nil {
				t.Fatalf("child directory listing failed: %v", err)
			}
			t.Logf("child directory listing succeeded: %d entries", len(children))
			break
		}
		return
	}
	t.Fatal("no Dropbox account found")
}

type dropboxRoundTripper func(*http.Request) (*http.Response, error)

func (f dropboxRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func dropboxResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func withDropboxTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	old := netx.TestTransportHook
	netx.TestTransportHook = rt
	t.Cleanup(func() { netx.TestTransportHook = old })
}

func dropboxAPIPath(r *http.Request) string {
	return strings.TrimPrefix(r.URL.Path, "/2")
}

func TestDropboxListRefreshesExpiredAccessToken(t *testing.T) {
	tokenCalls, listCalls := 0, 0
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch dropboxAPIPath(r) {
		case "/oauth2/token":
			tokenCalls++
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			if r.Form.Get("refresh_token") != "refresh-old" || r.Form.Get("grant_type") != "refresh_token" {
				t.Errorf("unexpected refresh form")
			}
			return dropboxResponse(r, 200, `{"access_token":"access-new","refresh_token":"refresh-new","expires_in":14400}`), nil
		case "/files/list_folder":
			listCalls++
			if r.Header.Get("Authorization") == "Bearer access-old" {
				return dropboxResponse(r, 401, `{"error_summary":"expired_access_token/","error":{".tag":"expired_access_token"}}`), nil
			}
			if r.Header.Get("Authorization") != "Bearer access-new" {
				t.Errorf("wrong refreshed authorization")
			}
			return dropboxResponse(r, 200, `{"entries":[{".tag":"file","id":"id:one","name":"one.txt","path_display":"/one.txt"}],"has_more":false}`), nil
		default:
			return nil, fmt.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	tok := &model.TokenInfo{AccessToken: "access-old", RefreshToken: "refresh-old", DeviceID: builtinAppKey, UserID: "stable-user"}
	files, err := (&Driver{}).List(context.Background(), drive.Context{Token: tok}, RootID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "one.txt" || tokenCalls != 1 || listCalls != 2 {
		t.Fatalf("files=%v refresh=%d list=%d", files, tokenCalls, listCalls)
	}
	if tok.AccessToken != "access-new" || tok.RefreshToken != "refresh-new" || tok.ExpireTime == "" || tok.UserID != "stable-user" {
		t.Fatal("refreshed session not retained for persistence")
	}
}

func TestDropboxUploadChunkRefreshesTokenAndReplaysBytes(t *testing.T) {
	refreshes, uploads := 0, 0
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/oauth2/token" {
			refreshes++
			return dropboxResponse(r, 200, `{"access_token":"new","expires_in":14400}`), nil
		}
		uploads++
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != "chunk-data" {
			t.Errorf("replayed chunk = %q, error = %v", data, err)
		}
		if r.Header.Get("Authorization") == "Bearer old" {
			return dropboxResponse(r, 401, `{"error_summary":"expired_access_token/"}`), nil
		}
		return dropboxResponse(r, 200, `{"session_id":"session"}`), nil
	}))
	cl, err := clientOf(drive.Context{Token: &model.TokenInfo{AccessToken: "old", RefreshToken: "refresh", DeviceID: builtinAppKey}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := cl.sessionStart(context.Background(), []byte("chunk-data"))
	if err != nil || id != "session" || refreshes != 1 || uploads != 2 {
		t.Fatalf("id=%s error=%v refreshes=%d uploads=%d", id, err, refreshes, uploads)
	}
}

func TestDropboxAuthRecoveryIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name, summary, refresh              string
		refreshStatus, wantRPC, wantRefresh int
	}{
		{"still expired", "expired_access_token/", "refresh", 200, 2, 1},
		{"missing scope", "missing_scope/", "refresh", 200, 1, 0},
		{"no refresh token", "expired_access_token/", "", 200, 1, 0},
		{"revoked refresh token", "expired_access_token/", "refresh", 400, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpcs, refreshes := 0, 0
			withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/oauth2/token" {
					refreshes++
					return dropboxResponse(r, tc.refreshStatus, `{"access_token":"new","error":"invalid_grant"}`), nil
				}
				rpcs++
				return dropboxResponse(r, 401, `{"error_summary":"`+tc.summary+`"}`), nil
			}))
			cl, err := clientOf(drive.Context{Token: &model.TokenInfo{AccessToken: "old", RefreshToken: tc.refresh, DeviceID: builtinAppKey}})
			if err != nil {
				t.Fatal(err)
			}
			if err := cl.rpc(context.Background(), "/files/list_folder", nil, nil); err == nil {
				t.Fatal("expected auth failure")
			}
			if rpcs != tc.wantRPC || refreshes != tc.wantRefresh {
				t.Fatalf("rpc=%d refresh=%d", rpcs, refreshes)
			}
		})
	}
}

func TestCreateShareUsesDropboxSharingAPI(t *testing.T) {
	withDropboxTransport(t, dropboxRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api.dropboxapi.com" || dropboxAPIPath(req) != "/sharing/create_shared_link_with_settings" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer access-token" {
			return nil, fmt.Errorf("authorization = %q", req.Header.Get("Authorization"))
		}
		var body struct {
			Path     string `json:"path"`
			Settings struct {
				Visibility string `json:"requested_visibility"`
				Password   string `json:"link_password"`
				Expires    string `json:"expires"`
			} `json:"settings"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body.Path != "/movie.mkv" || body.Settings.Visibility != "password" || body.Settings.Password != "p4ss" || body.Settings.Expires != "2030-01-01T00:00:00Z" {
			return nil, fmt.Errorf("share body = %+v", body)
		}
		return dropboxResponse(req, http.StatusOK, `{"id":"sl-1","url":"https://www.dropbox.com/s/share-1","name":"movie.mkv","link_access_level":"password","expires":"2030-01-01T00:00:00Z"}`), nil
	}))

	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "dropbox:user", DriveID: "dropbox:user", Token: &model.TokenInfo{AccessToken: "access-token"},
	}, drive.ShareParams{FileIDs: []string{"/movie.mkv"}, Expiration: "2030-01-01T00:00:00Z", Password: "p4ss"})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if item.ShareID != "sl-1" || item.ShareURL != "https://www.dropbox.com/s/share-1" || item.FileID != "/movie.mkv" || item.AccountID != "dropbox:user" {
		t.Fatalf("share = %+v", item)
	}
}

func TestDropboxCancelShareRevokesRemoteLink(t *testing.T) {
	withDropboxTransport(t, dropboxRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api.dropboxapi.com" || dropboxAPIPath(req) != "/sharing/revoke_shared_link" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer access-token" {
			return nil, fmt.Errorf("authorization = %q", req.Header.Get("Authorization"))
		}
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["url"] != "https://www.dropbox.com/s/share-1" {
			return nil, fmt.Errorf("revoke body = %#v", body)
		}
		return dropboxResponse(req, http.StatusOK, ``), nil
	}))

	err := (&Driver{}).CancelShare(context.Background(), drive.Context{Token: &model.TokenInfo{AccessToken: "access-token"}}, model.ShareHistoryEntry{ShareURL: "https://www.dropbox.com/s/share-1"})
	if err != nil {
		t.Fatalf("CancelShare() error = %v", err)
	}
}

func TestDropboxRedirectURIUsesRcloneCompatibleDefault(t *testing.T) {
	spec, err := resolveRedirectURI(nil)
	if err != nil {
		t.Fatalf("resolve default redirect URI: %v", err)
	}
	if spec.raw != dbDefaultRedirectURI || spec.host != "localhost" || spec.port != 53682 || spec.path != "/" {
		t.Fatalf("unexpected default redirect spec: %#v", spec)
	}
	if got := spec.listenAddr(); got != "127.0.0.1:53682" {
		t.Fatalf("listen address = %q", got)
	}
}

func TestDropboxTokenNetworkFailureExplainsProxy(t *testing.T) {
	cause := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection blocked")}
	err := explainDropboxTokenExchangeError(cause)
	if !strings.Contains(err.Error(), "系统/应用代理") {
		t.Fatalf("network error = %v, want proxy guidance", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("network error does not retain cause: %v", err)
	}

	apiErr := errors.New("http 400: invalid_grant")
	if got := explainDropboxTokenExchangeError(apiErr); got != apiErr {
		t.Fatalf("API error was incorrectly classified as network failure: %v", got)
	}
}

func TestDropboxRedirectURIAllowsConfiguredLoopbackAndRejectsRemote(t *testing.T) {
	spec, err := resolveRedirectURI(map[string]string{"dropbox_redirect_uri": "http://127.0.0.1:4242/oauth/callback"})
	if err != nil {
		t.Fatalf("resolve custom redirect URI: %v", err)
	}
	if spec.raw != "http://127.0.0.1:4242/oauth/callback" || spec.path != "/oauth/callback" || spec.port != 4242 {
		t.Fatalf("unexpected custom redirect spec: %#v", spec)
	}
	if _, err := resolveRedirectURI(map[string]string{"dropbox_redirect_uri": "https://example.com/callback"}); err == nil {
		t.Fatal("remote redirect URI should be rejected")
	}
}

func TestDropboxCallbackValidationUsesConfiguredHostAndPath(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	spec := redirectSpec{host: "localhost", port: port, path: "/oauth/callback"}
	req := httptest.NewRequest(http.MethodGet, "http://localhost:"+fmt.Sprint(port)+"/oauth/callback?code=x&state=y", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if !validCallbackRequest(req, ln, spec) {
		t.Fatal("valid loopback callback was rejected")
	}
	req.Host = "127.0.0.1:" + fmt.Sprint(port)
	if validCallbackRequest(req, ln, spec) {
		t.Fatal("callback for a different configured host was accepted")
	}
}

func TestDropboxRefreshAccountUpdatesExpiryAndRetainsRotatingFields(t *testing.T) {
	tokenCalls := 0
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch dropboxAPIPath(r) {
		case "/oauth2/token":
			tokenCalls++
			if tokenCalls == 1 {
				return dropboxResponse(r, http.StatusOK, `{"access_token":"access-new","expires_in":120,"token_type":"Bearer"}`), nil
			}
			return dropboxResponse(r, http.StatusOK, `{"access_token":"access-new-2","token_type":"Bearer"}`), nil
		case "/users/get_current_account":
			return dropboxResponse(r, http.StatusOK, `{"account_id":"db-acct","email":"user@example.com","name":{"display_name":"Dropbox User"}}`), nil
		case "/users/get_space_usage":
			return dropboxResponse(r, http.StatusOK, `{"used":10,"allocation":{"allocated":100}}`), nil
		default:
			return dropboxResponse(r, http.StatusNotFound, `{}`), nil
		}
	}))

	d := &Driver{}
	tok := &model.TokenInfo{
		TokenFrom:    providerID,
		AccessToken:  "access-old",
		RefreshToken: "refresh-old",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		DeviceID:     "app-key",
	}
	got, err := d.RefreshAccount(context.Background(), drive.Context{Token: tok}, tok)
	if err != nil {
		t.Fatalf("RefreshAccount returned error: %v", err)
	}
	if got != tok || tok.AccessToken != "access-new" || tok.RefreshToken != "refresh-old" {
		t.Fatalf("refresh fields = %+v", tok)
	}
	if tok.UserID != model.BuildUserID(providerID, "db-acct") || tok.DefaultDriveID != model.BuildDriveID(providerID, "db-acct") {
		t.Fatalf("account identity = %q/%q", tok.UserID, tok.DefaultDriveID)
	}
	expiry, err := time.Parse(time.RFC3339, tok.ExpireTime)
	if err != nil {
		t.Fatalf("ExpireTime = %q: %v", tok.ExpireTime, err)
	}
	if expiry.Before(time.Now().Add(90*time.Second)) || expiry.After(time.Now().Add(150*time.Second)) {
		t.Fatalf("ExpireTime = %s, not near 120 seconds from now", expiry)
	}

	oldExpiry := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	tok.AccessToken = "access-old-2"
	tok.ExpiresIn = 7200
	tok.ExpireTime = oldExpiry
	if _, err := d.RefreshAccount(context.Background(), drive.Context{Token: tok}, tok); err != nil {
		t.Fatalf("RefreshAccount without expires_in returned error: %v", err)
	}
	if tok.RefreshToken != "refresh-old" || tok.ExpiresIn != 7200 || tok.ExpireTime != oldExpiry {
		t.Fatalf("omitted refresh fields were not retained: %+v", tok)
	}
}

func TestDropboxExistingSharedLinkUsesObjectPagination(t *testing.T) {
	continueCalls := 0
	modifyCalls := 0
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch dropboxAPIPath(r) {
		case "/sharing/create_shared_link_with_settings":
			return dropboxResponse(r, http.StatusConflict, `{"error_summary":"shared_link_already_exists/"}`), nil
		case "/sharing/list_shared_links":
			return dropboxResponse(r, http.StatusOK, `{"links":[{"id":"sl-1","url":"https://www.dropbox.com/s/one","name":"movie.mkv","link_access_level":"public"}],"has_more":true,"cursor":"cursor-1"}`), nil
		case "/sharing/list_shared_links/continue":
			continueCalls++
			return dropboxResponse(r, http.StatusOK, `{"links":[{"id":"sl-2","url":"https://www.dropbox.com/s/two","name":"other.mkv"}],"has_more":false}`), nil
		case "/sharing/modify_shared_link_settings":
			modifyCalls++
			return dropboxResponse(r, http.StatusOK, `{"id":"sl-1","url":"https://www.dropbox.com/s/one","name":"movie.mkv","link_access_level":"password","expires":"2026-06-01T08:00:00Z"}`), nil
		default:
			return dropboxResponse(r, http.StatusNotFound, `{}`), nil
		}
	}))

	share, err := newClient("access").CreateSharedLink(context.Background(), "/movie.mkv", "2026-06-01T08:00:00Z", "1234")
	if err != nil {
		t.Fatalf("CreateSharedLink returned error: %v", err)
	}
	if continueCalls != 1 || modifyCalls != 1 {
		t.Fatalf("shared-link calls = continue:%d modify:%d", continueCalls, modifyCalls)
	}
	if share.ShareID != "sl-1" || share.ShareURL != "https://www.dropbox.com/s/one" || share.SharePwd != "1234" || share.Expiration == "" {
		t.Fatalf("share = %+v", share)
	}
}

func TestDropboxListRetriesTransientServerError(t *testing.T) {
	calls := 0
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		if dropboxAPIPath(r) != "/files/list_folder" {
			return dropboxResponse(r, http.StatusNotFound, `{}`), nil
		}
		calls++
		if calls == 1 {
			resp := dropboxResponse(r, http.StatusInternalServerError, `{"error_summary":"internal_error/"}`)
			resp.Header.Set("Retry-After", "0")
			return resp, nil
		}
		return dropboxResponse(r, http.StatusOK, `{"entries":[{".tag":"file","id":"id:file","name":"file.txt","path_display":"/file.txt","size":4}],"has_more":false}`), nil
	}))

	items, err := newClient("access").List(context.Background(), RootID)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if calls != 2 || len(items) != 1 || items[0].ID != "id:file" {
		t.Fatalf("list calls/items = %d/%+v", calls, items)
	}
}

func TestDropboxListUsesConservativeFolderPayload(t *testing.T) {
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		if dropboxAPIPath(r) != "/files/list_folder" {
			return dropboxResponse(r, http.StatusNotFound, `{}`), nil
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["path"] != "" || body["include_mounted_folders"] != false || body["include_non_downloadable_files"] != false || body["limit"] != float64(2000) {
			return nil, fmt.Errorf("list payload = %#v", body)
		}
		return dropboxResponse(r, http.StatusOK, `{"entries":[],"has_more":false}`), nil
	}))

	if _, err := newClient("access").List(context.Background(), RootID); err != nil {
		t.Fatalf("List returned error: %v", err)
	}
}

func TestDropboxListNormalizesUIRootAndPreservesRealPaths(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"root", ""}, {RootID, ""}, {"", ""}, {"/", ""}, {"/root", "/root"}, {"/folder", "/folder"}, {"id:folder", "id:folder"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
				var payload struct {
					Path string `json:"path"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					return nil, err
				}
				if payload.Path != tc.want {
					return nil, fmt.Errorf("API path=%q, want %q", payload.Path, tc.want)
				}
				return dropboxResponse(r, 200, `{"entries":[],"has_more":false}`), nil
			}))
			if _, _, _, err := newClient("access").ListPage(context.Background(), tc.input, ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDropboxRootDestinations(t *testing.T) {
	for _, root := range []string{"root", RootID, "", "/"} {
		if got := joinTarget(root, "/source/file.txt"); got != "/file.txt" {
			t.Fatalf("joinTarget(%q)=%q", root, got)
		}
		if got := resolveCommandPath(root, "", ""); got != "" {
			t.Fatalf("root path=%q", got)
		}
	}
	if got := joinTarget("/root", "/source/file.txt"); got != "/root/file.txt" {
		t.Fatalf("real directory changed: %q", got)
	}
}

func TestDropboxUploadUsesUIRootAsAPIRoot(t *testing.T) {
	path := t.TempDir() + "/file.txt"
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		var arg struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg); err != nil {
			return nil, err
		}
		if arg.Path != "/file.txt" {
			return nil, fmt.Errorf("upload target=%q", arg.Path)
		}
		return dropboxResponse(r, 200, `{"id":"id:file"}`), nil
	}))
	ui := &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: path, ParentFileID: "root", Name: "file.txt"}}
	err := (&Driver{}).UploadOneFile(context.Background(), drive.Context{Token: &model.TokenInfo{AccessToken: "access"}}, ui)
	if err != nil || ui.Upload.FileID != "id:file" {
		t.Fatalf("upload failed: %v", err)
	}
}

func TestDropboxListServerErrorPreservesEndpointAndRequestID(t *testing.T) {
	calls := 0
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := dropboxResponse(r, http.StatusInternalServerError, `{"error_summary":"internal_error/"}`)
		resp.Header.Set("Retry-After", "0")
		resp.Header.Set("X-Dropbox-Request-Id", "dbx-request-123")
		return resp, nil
	}))

	_, err := newClient("access").List(context.Background(), RootID)
	if err == nil {
		t.Fatal("List unexpectedly succeeded")
	}
	if calls != rpcRetryAttempts {
		t.Fatalf("server error calls = %d, want %d", calls, rpcRetryAttempts)
	}
	for _, want := range []string{"/files/list_folder", "http 500", "request_id=dbx-request-123"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

func TestDropboxListDoesNotRetryClientError(t *testing.T) {
	calls := 0
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return dropboxResponse(r, http.StatusConflict, `{"error_summary":"path/not_found/"}`), nil
	}))

	_, err := newClient("access").List(context.Background(), RootID)
	if err == nil {
		t.Fatal("List unexpectedly succeeded")
	}
	if calls != 1 {
		t.Fatalf("client error calls = %d, want 1", calls)
	}
}

func TestDropboxUploadRequiresRemoteFileID(t *testing.T) {
	body := `{".tag":"file","name":"x.txt"}`
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		return dropboxResponse(r, http.StatusOK, body), nil
	}))

	policy := uploadPolicy{mode: "overwrite"}
	if _, err := newClient("access").UploadSmall(context.Background(), "/x.txt", strings.NewReader("data"), 4, policy); err == nil || !strings.Contains(err.Error(), "missing file id") {
		t.Fatalf("missing upload id error = %v", err)
	}

	body = `{".tag":"file","id":"id:x","name":"x.txt"}`
	path := t.TempDir() + "/x.txt"
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui := &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: path, Name: "x.txt"}}
	if err := (&Driver{}).UploadOneFile(context.Background(), drive.Context{Token: &model.TokenInfo{AccessToken: "access"}}, ui); err != nil {
		t.Fatalf("UploadOneFile returned error: %v", err)
	}
	if ui.Upload.FileID != "id:x" {
		t.Fatalf("UploadOneFile file id = %q", ui.Upload.FileID)
	}
}

func TestDropboxUploadSessionFinishRequiresRemoteFileID(t *testing.T) {
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		return dropboxResponse(r, http.StatusOK, `{}`), nil
	}))

	_, err := newClient("access").sessionFinish(context.Background(), "session", 0, []byte("data"), "/x.txt", uploadPolicy{mode: "overwrite"})
	if err == nil || !strings.Contains(err.Error(), "missing file id") {
		t.Fatalf("missing session file id error = %v", err)
	}
}

func TestDropboxUploadSessionKeyIncludesIdentityAndConflictPolicy(t *testing.T) {
	dc := drive.Context{UserID: "dropbox-user", DriveID: "dropbox-drive"}
	base := &model.UploadingUI{Info: model.UploadInfo{SHA1: "sha-a"}}
	overwrite := uploadPolicy{mode: "overwrite"}
	rename := uploadPolicy{mode: "add", autorename: true}
	first := dropboxUploadSessionKey(dc, "/movie.mkv", 100, base, overwrite, 1)
	second := dropboxUploadSessionKey(dc, "/movie.mkv", 100, &model.UploadingUI{Info: model.UploadInfo{SHA1: "sha-b"}}, overwrite, 1)
	third := dropboxUploadSessionKey(dc, "/movie.mkv", 100, base, rename, 1)
	fourth := dropboxUploadSessionKey(dc, "/movie.mkv", 100, nil, overwrite, 2)
	if first == second || first == third || first == fourth {
		t.Fatalf("upload session key does not isolate identity/policy: %q %q %q %q", first, second, third, fourth)
	}
}

func TestDropboxTemporaryLinkDoesNotCarryBearerToken(t *testing.T) {
	withDropboxTransport(t, dropboxRoundTripper(func(r *http.Request) (*http.Response, error) {
		if dropboxAPIPath(r) != "/files/get_temporary_link" {
			return dropboxResponse(r, http.StatusNotFound, `{}`), nil
		}
		return dropboxResponse(r, http.StatusOK, `{"link":"https://dl.dropboxusercontent.com/signed","metadata":{".tag":"file","size":4}}`), nil
	}))

	c := drive.Context{DriveID: "dropbox-drive", Token: &model.TokenInfo{AccessToken: "secret"}}
	dl, err := (&Driver{}).GetDownloadURL(context.Background(), c, "/x.txt", 0)
	if err != nil {
		t.Fatalf("GetDownloadURL returned error: %v", err)
	}
	if len(dl.Headers) != 0 {
		t.Fatalf("temporary link headers = %#v", dl.Headers)
	}
	preview, err := (&Driver{}).GetVideoPreview(context.Background(), c, "/x.txt")
	if err != nil {
		t.Fatalf("GetVideoPreview returned error: %v", err)
	}
	if len(preview.Headers) != 0 || len(preview.Qualities) != 1 || len(preview.Qualities[0].Headers) != 0 {
		t.Fatalf("preview headers = %+v", preview)
	}
}
