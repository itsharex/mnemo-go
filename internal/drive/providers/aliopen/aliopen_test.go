package aliopen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

type aliOpenRoundTripperFunc func(*http.Request) (*http.Response, error)

type testUploadSessions struct {
	key, id string
	parts   []int
}

func (s *testUploadSessions) SaveUploadSession(string, []int) error { return nil }
func (s *testUploadSessions) LoadUploadSession(string) []int        { return nil }
func (s *testUploadSessions) ClearUploadSession(string)             {}
func (s *testUploadSessions) SaveUploadSessionState(key, id string, parts []int) error {
	s.key, s.id, s.parts = key, id, parts
	return nil
}
func (s *testUploadSessions) LoadUploadSessionState(key string) (string, []int) {
	if key == s.key {
		return s.id, s.parts
	}
	return "", nil
}

func TestUploadResumePreservesIDsAndSeparatesReplacementContent(t *testing.T) {
	old, limiter := netx.TestTransportHook, aliOpenLimiter
	aliOpenLimiter = newAliOpenRateLimiter(2, 0)
	t.Cleanup(func() { netx.TestTransportHook = old; aliOpenLimiter = limiter; drive.SetUploadSessionStore(nil) })
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	dc := drive.Context{UserID: "aliopen:test", DriveID: "mounted", Token: &model.TokenInfo{RefreshToken: mustJSON(Session{AccessToken: "test", DriveID: "drive"})}}
	sessions := &testUploadSessions{}
	drive.SetUploadSessionStore(sessions)
	creates, puts, completes := 0, 0, 0
	failComplete := true
	netx.TestTransportHook = aliOpenRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		response := "{}"
		if r.Method == http.MethodPut {
			puts++
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				return nil, err
			}
		} else {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			switch r.URL.Path {
			case "/adrive/v1.0/openFile/create":
				creates++
				response = `{"file_id":"file-id","upload_id":"upload-id","part_info_list":[{"part_number":1,"upload_url":"https://upload.example/part"}]}`
			case "/adrive/v1.0/openFile/complete":
				completes++
				if body["file_id"] != "file-id" || body["upload_id"] != "upload-id" {
					t.Errorf("swapped resume IDs: %v", body)
				}
				if failComplete {
					return nil, errors.New("interrupted before completion")
				}
			default:
				t.Fatalf("unexpected request %s", r.URL.Path)
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	upload := func(policy string) error {
		return (&Driver{}).UploadOneFile(context.Background(), dc, &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: path, ParentFileID: "b:root", Name: "file.txt", ConflictPolicy: policy}})
	}
	if err := upload("overwrite"); err == nil {
		t.Fatal("expected interrupted completion")
	}
	failComplete = false
	if err := upload("overwrite"); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || puts != 1 || completes != 2 {
		t.Fatalf("unchanged file did not resume: %d/%d/%d", creates, puts, completes)
	}
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := upload("overwrite"); err != nil {
		t.Fatal(err)
	}
	if creates != 2 || puts != 2 {
		t.Fatal("same-size replacement reused old parts")
	}
	if err := upload("rename"); err != nil {
		t.Fatal(err)
	}
	if creates != 3 || puts != 3 {
		t.Fatal("changed conflict policy reused old session")
	}
}

func (f aliOpenRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFavoritesReadBothDrivesAndUpdateCorrectScope(t *testing.T) {
	d := &Driver{}
	p, ok := any(d).(drive.RemoteFavorites)
	if !ok {
		t.Fatal("Ali Open native favorites not implemented")
	}
	old := netx.TestTransportHook
	oldLimiter := aliOpenLimiter
	aliOpenLimiter = newAliOpenRateLimiter(2, 0)
	t.Cleanup(func() { netx.TestTransportHook = old; aliOpenLimiter = oldLimiter })
	reads, writes := 0, 0
	netx.TestTransportHook = aliOpenRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "openapi.alipan.com" || r.Method != http.MethodPost {
			t.Fatalf("unexpected API %s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response := ""
		switch r.URL.Path {
		case "/adrive/v1.0/openFile/starredList":
			reads++
			if body["limit"] != float64(100) {
				t.Fatalf("wrong page size: %+v", body)
			}
			if body["drive_id"] == "backup" {
				if body["marker"] == "next" {
					response = `{"items":[],"next_marker":""}`
				} else {
					response = `{"items":[{"file_id":"same","parent_file_id":"folder","name":"photo.jpg","size":42,"type":"file"}],"next_marker":"next"}`
				}
			} else if body["drive_id"] == "resource" {
				response = `{"items":[{"file_id":"same","parent_file_id":"root","name":"Folder","type":"folder"}]}`
			} else {
				t.Fatalf("wrong drive: %+v", body)
			}
		case "/adrive/v1.0/openFile/update":
			writes++
			want := "backup"
			if writes == 2 {
				want = "resource"
			}
			if body["drive_id"] != want || body["file_id"] != "same" || body["starred"] != (writes == 1) {
				t.Fatalf("wrong favorite update: %+v", body)
			}
			if _, renamed := body["name"]; renamed {
				t.Fatal("favorite must not rename file")
			}
			response = `{"file_id":"same"}`
		default:
			t.Fatalf("unexpected endpoint %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	c := drive.Context{UserID: "aliopen_favorites", DriveID: "mounted", Token: &model.TokenInfo{RefreshToken: mustJSON(Session{AccessToken: "token", DriveID: "resource", BackupDriveID: "backup", ResourceDriveID: "resource"})}}
	files, err := p.ListFavorites(context.Background(), c)
	if err != nil || len(files) != 2 || reads != 3 {
		t.Fatalf("favorites=%+v, reads=%d, %v", files, reads, err)
	}
	if files[0].FileID != "b:same" || files[0].ParentFileID != "b:folder" || files[0].Size != 42 || !files[0].Starred || files[1].FileID != "r:same" || !files[1].IsDir {
		t.Fatalf("lost scope/metadata: %+v", files)
	}
	for i, id := range []string{"b:same", "r:same"} {
		ids, err := d.Favorite(context.Background(), c, []string{id}, i == 0)
		if err != nil || len(ids) != 1 || ids[0] != id {
			t.Fatalf("favorite=%v, %v", ids, err)
		}
	}
}

func TestFavoritesRejectPartialAliDriveSnapshotAndUnconfirmedUpdate(t *testing.T) {
	old, limiter := netx.TestTransportHook, aliOpenLimiter
	aliOpenLimiter = newAliOpenRateLimiter(2, 0)
	t.Cleanup(func() { netx.TestTransportHook = old; aliOpenLimiter = limiter })
	c := drive.Context{Token: &model.TokenInfo{RefreshToken: mustJSON(Session{AccessToken: "token", DriveID: "backup", ResourceDriveID: "resource"})}}
	for _, body := range []string{`{}`, `{"items":[],"next_marker":"repeat"}`, `{"items":[{"file_id":"","name":"broken"}]}`, `{"code":"Forbidden","message":"permission denied"}`} {
		t.Run(body, func(t *testing.T) {
			netx.TestTransportHook = aliOpenRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				response := body
				if request["drive_id"] == "backup" {
					response = `{"items":[{"file_id":"ok","name":"ok.jpg"}]}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
			})
			files, err := (&Driver{}).ListFavorites(context.Background(), c)
			if err == nil || len(files) != 0 {
				t.Fatal("partial cloud snapshot was accepted")
			}
		})
	}
	netx.TestTransportHook = aliOpenRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	if ids, err := (&Driver{}).Favorite(context.Background(), c, []string{"b:file"}, true); err == nil || len(ids) != 0 {
		t.Fatal("missing update confirmation accepted")
	}
	netx.TestTransportHook = aliOpenRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("canceled operation sent request")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Driver{}).ListFavorites(ctx, drive.Context{Token: &model.TokenInfo{RefreshToken: "expired"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled favorites error=%v", err)
	}
}

func TestRefreshTokenStoresProfileAndNumericDriveID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token":"access-next",
			"refresh_token":"refresh-next",
			"user_id":"user-001",
			"user_name":"ali-user",
			"nick_name":"阿里昵称",
			"avatar":"https://example.invalid/avatar.png",
			"default_drive_id":10001
		}`))
	}))
	defer server.Close()

	token := &model.TokenInfo{}
	sess := &Session{RefreshToken: "refresh-old", OAuthTokenURL: server.URL}
	cl := &client{http: netx.NewClient(5 * time.Second), session: sess, token: token}
	if err := cl.refreshToken(context.Background()); err != nil {
		t.Fatalf("refreshToken() error = %v", err)
	}

	if sess.AccessToken != "access-next" || sess.RefreshToken != "refresh-next" {
		t.Fatalf("session tokens = %#v", sess)
	}
	if sess.UserID != "user-001" || sess.DriveID != "10001" {
		t.Fatalf("session identity = %#v", sess)
	}
	if sess.displayName() != "阿里昵称" || sess.accountID() != "user-001" {
		t.Fatalf("profile resolution = name %q id %q", sess.displayName(), sess.accountID())
	}
	if token.RefreshToken == "" || token.OpenAPIAccessToken != "access-next" {
		t.Fatalf("persisted token = %#v", token)
	}
}

func TestRefreshAccountProfileReplacesIdentifierDisplayFields(t *testing.T) {
	previous := netx.TestTransportHook
	var calls int
	netx.TestTransportHook = aliOpenRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.Hostname() != "api.alipan.com" || req.URL.Path != "/v2/user/get" {
			t.Fatalf("profile request = %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer profile-access" {
			t.Fatalf("profile authorization = %q", req.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"user_id":"user-001", "user_name":"138***8000",
				"nick_name":"阿里昵称", "phone":"13800138000",
				"avatar":"https://example.invalid/profile.png"
			}`)),
			Request: req,
		}, nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	token := &model.TokenInfo{UserName: "user-001", NickName: "user-001", Name: "user-001"}
	sess := &Session{AccessToken: "profile-access", UserID: "user-001", UserName: "user-001", NickName: "user-001"}
	cl := &client{http: netx.NewClient(5 * time.Second), session: sess, token: token}
	cl.refreshAccountProfile(context.Background())
	applyAliOpenProfile(token, sess)

	if calls != 1 || sess.ProfileCheckedAt == 0 {
		t.Fatalf("profile fetch calls/check time = %d/%d", calls, sess.ProfileCheckedAt)
	}
	if token.Name != "阿里昵称" || token.UserName != "138***8000" || token.NickName != "阿里昵称" {
		t.Fatalf("profile did not replace identifier fields: %#v", token)
	}
	if token.Avatar != "https://example.invalid/profile.png" {
		t.Fatalf("profile avatar = %q", token.Avatar)
	}

	cl.refreshAccountProfile(context.Background())
	if calls != 1 {
		t.Fatalf("fresh profile should be cached, calls = %d", calls)
	}
}

func TestApplyAliOpenProfileSkipsIdentifierFallbacks(t *testing.T) {
	token := &model.TokenInfo{UserName: "user-001", NickName: "user-001", Name: "user-001"}
	sess := &Session{UserID: "user-001", UserName: "138***8000", NickName: "user-001", Phone: "13800138000"}
	applyAliOpenProfile(token, sess)
	if token.Name != "138***8000" || token.UserName != "138***8000" || token.NickName != "138***8000" {
		t.Fatalf("identifier fallback still won: %#v", token)
	}
}

func TestParseAliOpenSpaceInfoSupportsNumbersAndStrings(t *testing.T) {
	used, total := parseAliOpenSpaceInfo([]byte(`{
		"personal_space_info":{"used_size":"12","total_size":100}
	}`))
	if used != 12 || total != 100 {
		t.Fatalf("string quota = %d/%d, want 12/100", used, total)
	}

	used, total = parseAliOpenSpaceInfo([]byte(`{
		"personal_space_info":{"usedSize":120,"totalSize":100}
	}`))
	if used != 100 || total != 100 {
		t.Fatalf("clamped numeric quota = %d/%d, want 100/100", used, total)
	}

	used, total = parseAliOpenSpaceInfo([]byte(`{"personal_space_info":{"total_size":"bad"}}`))
	if used != 0 || total != 0 {
		t.Fatalf("invalid quota = %d/%d, want 0/0", used, total)
	}
}

func TestApplyAliOpenQuotaPreservesLastKnownValueOnMissingQuota(t *testing.T) {
	token := &model.TokenInfo{UsedSize: 2, TotalSize: 10, FreeSize: 8}
	applyAliOpenQuota(token, 0, 0)
	if token.UsedSize != 2 || token.TotalSize != 10 || token.FreeSize != 8 {
		t.Fatalf("missing quota replaced last known values: %#v", token)
	}
}

func TestCreateShareUsesAliOpenAPI(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	netx.TestTransportHook = aliOpenRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "openapi.alipan.com" || req.URL.Path != "/adrive/v1.0/openFile/createShareLink" {
			t.Errorf("request = %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("authorization = %q", req.Header.Get("Authorization"))
		}
		var body struct {
			DriveID    string   `json:"drive_id"`
			FileIDs    []string `json:"file_id_list"`
			ShareName  string   `json:"share_name"`
			SharePwd   string   `json:"share_pwd"`
			Expiration string   `json:"expiration"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.DriveID != "drive-1" || strings.Join(body.FileIDs, ",") != "file-1,file-2" || body.ShareName != "测试分享" || body.SharePwd != "p4ss" || body.Expiration != "2030-01-01T00:00:00Z" {
			t.Errorf("share body = %+v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"share_id":"share-ali","share_url":"https://www.aliyundrive.com/s/share-ali","share_msg":"分享链接","expiration":"2030-01-01T00:00:00Z","status":"enabled","drive_id":"drive-1"}`)),
			Request:    req,
		}, nil
	})

	sess := &Session{AccessToken: "access-token", DriveID: "drive-1"}
	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "aliopen:user", DriveID: "aliopen:user",
		Token: &model.TokenInfo{AccessToken: sess.AccessToken, RefreshToken: mustJSON(sess)},
	}, drive.ShareParams{FileIDs: []string{"b:file-1", "b:file-2"}, ShareName: "测试分享", Expiration: "2030-01-01T00:00:00Z", Password: "p4ss"})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if item.ShareID != "share-ali" || item.ShareURL != "https://www.aliyundrive.com/s/share-ali" || item.FileID != "b:file-1" || len(item.FileIDList) != 2 || item.AccountID != "aliopen:user" {
		t.Fatalf("share = %+v", item)
	}
	if !(&Driver{}).Capabilities().CombinedShare {
		t.Fatal("Ali Open supports multi-file shares and must advertise combinedShare")
	}
}

func TestCreateShareFallsBackToNativeRouteOnlyAfterNotFound(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	var calls []string
	netx.TestTransportHook = aliOpenRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.Host+req.URL.Path)
		switch {
		case req.URL.Host == "openapi.alipan.com" && req.URL.Path == "/adrive/v1.0/openFile/createShareLink":
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":"NotFound","message":"not found"}`)),
				Request:    req,
			}, nil
		case req.URL.Host == "api.aliyundrive.com" && req.URL.Path == "/adrive/v2/share_link/create":
			var body struct {
				DriveID string   `json:"drive_id"`
				FileIDs []string `json:"file_id_list"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode fallback body: %v", err)
			}
			if body.DriveID != "drive-1" || strings.Join(body.FileIDs, ",") != "file-1" {
				t.Errorf("fallback share body = %+v", body)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"share_id":"share-fallback"}`)),
				Request:    req,
			}, nil
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
			return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
		}
	})

	sess := &Session{AccessToken: "access-token", DriveID: "drive-1"}
	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "aliopen:user", DriveID: "aliopen:user",
		Token: &model.TokenInfo{AccessToken: sess.AccessToken, RefreshToken: mustJSON(sess)},
	}, drive.ShareParams{FileIDs: []string{"b:file-1"}})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if strings.Join(calls, ",") != "openapi.alipan.com/adrive/v1.0/openFile/createShareLink,api.aliyundrive.com/adrive/v2/share_link/create" {
		t.Fatalf("calls = %v", calls)
	}
	if item.ShareID != "share-fallback" || item.ShareURL != "https://www.alipan.com/s/share-fallback" {
		t.Fatalf("fallback share = %+v", item)
	}
}

func TestAliOpenCancelShareFallsBackToNativeRoute(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	var calls []string
	netx.TestTransportHook = aliOpenRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.Host+req.URL.Path)
		switch {
		case req.URL.Host == "openapi.alipan.com" && req.URL.Path == "/adrive/v1.0/openFile/cancelShareLink":
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":"NotFound","message":"not found"}`)),
				Request:    req,
			}, nil
		case req.URL.Host == "api.aliyundrive.com" && req.URL.Path == "/adrive/v2/share_link/cancel":
			var body struct {
				ShareID string `json:"share_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode cancel body: %v", err)
			}
			if body.ShareID != "share-ali" {
				t.Errorf("cancel body = %+v", body)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
			return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
		}
	})

	sess := &Session{AccessToken: "access-token", DriveID: "drive-1"}
	err := (&Driver{}).CancelShare(context.Background(), drive.Context{
		UserID: "aliopen:user", DriveID: "aliopen:user",
		Token: &model.TokenInfo{AccessToken: sess.AccessToken, RefreshToken: mustJSON(sess)},
	}, model.ShareHistoryEntry{ShareID: "share-ali"})
	if err != nil {
		t.Fatalf("CancelShare() error = %v", err)
	}
	if strings.Join(calls, ",") != "openapi.alipan.com/adrive/v1.0/openFile/cancelShareLink,api.aliyundrive.com/adrive/v2/share_link/cancel" {
		t.Fatalf("calls = %v", calls)
	}
	if !(&Driver{}).Capabilities().CancelCreatedShares {
		t.Fatal("Ali Open must advertise cancelCreatedShares")
	}
}

func TestAliOpenNotFoundRequiresStructuredStatus(t *testing.T) {
	if !aliOpenNotFound(aliOpenRequestErrorOf([]byte(`{"code":"NotFound","message":"not found"}`), http.StatusNotFound)) {
		t.Fatal("structured HTTP 404 should enable the narrow endpoint fallback")
	}
	if aliOpenNotFound(errors.New("aliopen: http 404: not found")) {
		t.Fatal("formatted error text alone must not enable an endpoint fallback")
	}
	if aliOpenNotFound(aliOpenRequestErrorOf([]byte(`{"code":"NotFound","message":"not found"}`), http.StatusOK)) {
		t.Fatal("provider error code without HTTP 404 must not enable an endpoint fallback")
	}
}
