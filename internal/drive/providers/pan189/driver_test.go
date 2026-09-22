package pan189

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

func TestRegistration(t *testing.T) {
	reg, ok := drive.Get(model.ProviderPan189)
	if !ok {
		t.Fatal("pan189 not registered")
	}
	if reg.ID != model.ProviderPan189 {
		t.Fatalf("id = %q", reg.ID)
	}
	if reg.Meta.Label != "天翼云盘" {
		t.Fatalf("meta label = %q", reg.Meta.Label)
	}
	if reg.Auth == nil {
		t.Fatal("Auth must be wired for account+password login")
	}
	// login form must carry username/password
	keys := map[string]bool{}
	for _, f := range reg.Login.Fields {
		keys[f.Key] = true
	}
	if !keys["username"] || !keys["password"] {
		t.Fatalf("login fields missing username/password: %v", keys)
	}
	// factory builds a working driver
	d := reg.Factory()
	if d.ID() != model.ProviderPan189 {
		t.Fatalf("driver id = %q", d.ID())
	}
	if d.RootID() != PAN189Root {
		t.Fatalf("root = %q", d.RootID())
	}
}

func TestRootListsPersonalAndFamilyVirtualFolders(t *testing.T) {
	items, done, err := (&Driver{}).listPage(t.Context(), drive.Context{DriveID: "pan189:test"}, PAN189Root, 1)
	if err != nil || !done {
		t.Fatalf("root list error=%v done=%v", err, done)
	}
	if len(items) != 2 || items[0].FileID != PAN189PersonalRoot || items[0].Name != "个人云" || items[1].FileID != PAN189FamilyRoot || items[1].Name != "家庭云" {
		t.Fatalf("unexpected virtual roots: %+v", items)
	}
}

func TestCapabilities(t *testing.T) {
	caps := drive.RegistryCaps(model.ProviderPan189)
	// legacy pan189 overrides
	if !caps.Copy {
		t.Error("copy must be enabled (legacy: copy: true)")
	}
	if !caps.RecycleBin {
		t.Error("recycleBin must be enabled")
	}
	if !caps.PermanentDelete {
		t.Error("permanentDelete must be enabled")
	}
	if caps.Search {
		t.Error("search must stay disabled (legacy: search: false)")
	}
	if !caps.CreateShare || !caps.ShareExpiration || !caps.ShareHistory {
		t.Errorf("share capabilities = %+v", caps)
	}
	if caps.SharePassword {
		t.Error("天翼云盘分享提取码由服务端生成，不能声明自定义密码能力")
	}
	if got := caps.ShareExpirationOptions; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 7 {
		t.Errorf("shareExpirationOptions = %v, want [0 1 7]", got)
	}
	if caps.TrashView {
		t.Error("trashView must stay disabled")
	}
	// md5 hashes both directions
	if len(caps.ProvideHashes) != 1 || caps.ProvideHashes[0] != "md5" {
		t.Errorf("provideHashes = %v", caps.ProvideHashes)
	}
	if len(caps.RapidUploadHashes) != 1 || caps.RapidUploadHashes[0] != "md5" {
		t.Errorf("rapidUploadHashes = %v", caps.RapidUploadHashes)
	}
	// standard file baseline retained
	if !caps.Upload || !caps.Download || !caps.CreateFolder || !caps.Rename || !caps.Move {
		t.Error("standard file capabilities must remain enabled")
	}
}

func TestExpireTimeFromURL(t *testing.T) {
	// AWS-style signed URL
	aws := "https://dl.example.com/x?X-Amz-Date=20240102T030405Z&X-Amz-Expires=3600&sig=abc"
	// base 2024-01-02 03:04:05 UTC = 1704164645000 ms
	if got := expireTimeFromURL(aws); got != 1704164645000+3600*1000 {
		t.Fatalf("aws expire = %d", got)
	}
	// x-oss-expires epoch seconds
	oss := "https://dl.example.com/x?x-oss-expires=1700000000"
	if got := expireTimeFromURL(oss); got != 1700000000*1000 {
		t.Fatalf("oss expire = %d", got)
	}
	// plain expire query (seconds timestamp)
	if got := expireTimeFromURL("https://dl.example.com/x?expire=1700000000&e=1"); got != 1700000000*1000 {
		t.Fatalf("expire = %d", got)
	}
	// RFC3339 expire value
	if got := expireTimeFromURL("https://dl.example.com/x?expires=2024-01-02T03:04:05Z"); got != 1704164645000 {
		t.Fatalf("rfc3339 expire = %d", got)
	}
	// no expiry params → 0
	if got := expireTimeFromURL("https://dl.example.com/x?foo=bar"); got != 0 {
		t.Fatalf("no-expire = %d", got)
	}
	if got := expireTimeFromURL(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	// 189 download urls carry an expires-like query (present in practice)
	real := "https://d.pcs.189.cn/a?&expires=1710000000&x=1"
	if got := expireTimeFromURL(real); got != 1710000000*1000 {
		t.Fatalf("189-style expire = %d", got)
	}
}

func TestFollowRedirectDoesNotFetchDownloadBody(t *testing.T) {
	targetHit := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case targetHit <- struct{}{}:
		default:
		}
		// 如果错误地自动跟随跳转，会一直卡在模拟的大文件响应上，直到上下文取消。
		<-r.Context().Done()
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/large-file", http.StatusFound)
	}))
	defer origin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, ok := followRedirect(ctx, origin.URL)
	if !ok || got != target.URL+"/large-file" {
		t.Fatalf("followRedirect = %q, %v", got, ok)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("下载跳转探测耗时过长: %v", elapsed)
	}
	select {
	case <-targetHit:
		t.Fatal("下载跳转探测不应请求最终文件内容")
	default:
	}
}

func TestDriverImplementsInterface(t *testing.T) {
	var _ drive.Driver = (*Driver)(nil)
}

func TestListPagePreservesDistinctIDs(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	for _, cloud := range []string{CloudPersonal, CloudFamily} {
		t.Run(cloud, func(t *testing.T) {
			netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return pan189AuthResponse(req, http.StatusOK, nil, `{"fileListAO":{"folderList":[{"id":9007199254740992,"name":"A","parentId":-11},{"id":9007199254740993,"name":"B","parentId":-11},{"id":"folder-c","name":"C"}],"fileList":[{"id":9007199254740994,"name":"a.txt","size":12},{"id":"file-b","name":"b.txt"}]}}`), nil
			})
			sess := &Session{SessionKey: "key", SessionSecret: "secret", CloudType: cloud, FamilyID: "family", FamilySessionKey: "family-key", FamilySessionSecret: "family-secret"}
			dirID, space := PAN189PersonalRoot, spacePersonal
			if cloud == CloudFamily {
				dirID, space = PAN189FamilyRoot, spaceFamily
			}
			items, done, err := (&Driver{}).listPage(t.Context(), drive.Context{DriveID: "pan189:test", Token: &model.TokenInfo{AccessToken: sess.SessionKey, RefreshToken: mustJSON(sess)}}, dirID, 1)
			if err != nil || done || len(items) != 5 {
				t.Fatalf("listPage = %+v, %v, %v", items, done, err)
			}
			for i, want := range []string{"9007199254740992", "9007199254740993", "folder-c", "9007199254740994", "file-b"} {
				if items[i].FileID != pan189FileID(space, want) || items[i].ParentFileID != pan189FileID(space, Pan189DefaultFolder) || items[i].IsDir != (i < 3) {
					t.Errorf("item[%d] = %+v, want ID %q", i, items[i], want)
				}
			}
		})
	}
}

func TestRequestReloadsPersistedSessionBeforeUsingStaleClone(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	initialSession := &Session{
		SessionKey: "old-session", SessionSecret: "old-secret",
		AccessToken: "open-token", RefreshToken: "open-refresh",
	}
	persisted := &model.TokenInfo{AccessToken: initialSession.SessionKey, RefreshToken: mustJSON(initialSession)}
	var persistedMu sync.Mutex
	drive.SetTokenResolver(func(_, _ string) (*model.TokenInfo, error) {
		persistedMu.Lock()
		defer persistedMu.Unlock()
		return drive.CloneToken(persisted), nil
	})
	drive.SetTokenUpdater(func(_, _ string, token *model.TokenInfo) error {
		persistedMu.Lock()
		defer persistedMu.Unlock()
		persisted = drive.CloneToken(token)
		return nil
	})
	t.Cleanup(func() {
		drive.SetTokenResolver(nil)
		drive.SetTokenUpdater(nil)
	})

	var sessionExchanges int
	var sessionMu sync.Mutex
	netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/getSessionForPC.action":
			sessionMu.Lock()
			sessionExchanges++
			sessionMu.Unlock()
			return pan189AuthResponse(req, http.StatusOK, nil, `{"sessionKey":"new-session","sessionSecret":"new-secret"}`), nil
		case "/listFiles.action":
			if req.Header.Get("SessionKey") == "old-session" {
				return pan189AuthResponse(req, http.StatusOK, nil, `{"errorCode":"InvalidSessionKey"}`), nil
			}
			if req.Header.Get("SessionKey") != "new-session" {
				return nil, fmt.Errorf("unexpected session key %q", req.Header.Get("SessionKey"))
			}
			return pan189AuthResponse(req, http.StatusOK, nil, `{"fileListAO":{"folderList":[],"fileList":[]}}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL.Path)
		}
	})

	d := &Driver{}
	stale := func() drive.Context {
		return drive.Context{
			UserID: "pan189:session-reload", DriveID: "pan189:session-reload",
			Token: &model.TokenInfo{AccessToken: initialSession.SessionKey, RefreshToken: mustJSON(initialSession)},
		}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var requests sync.WaitGroup
	for range 2 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			_, _, err := d.listPage(t.Context(), stale(), PAN189PersonalRoot, 1)
			errs <- err
		}()
	}
	close(start)
	requests.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("stale clone was not reloaded: %v", err)
		}
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if sessionExchanges != 1 {
		t.Fatalf("session exchanges = %d, want 1", sessionExchanges)
	}
}

func TestBatchActionsPreserveFolderIdentity(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	c := drive.Context{UserID: "pan189:batch-test", DriveID: "batch-drive", Token: &model.TokenInfo{AccessToken: "skey", RefreshToken: mustJSON(sessionForTest())}}
	drive.RememberFile(c.UserID, c.DriveID, model.File{FileID: "folder", Name: "资料", IsDir: true})
	t.Cleanup(func() { drive.ClearFileMetaCache() })
	for _, action := range []string{"MOVE", "COPY", "DELETE"} {
		t.Run(action, func(t *testing.T) {
			checked := false
			netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if err := req.ParseForm(); err != nil {
					return nil, err
				}
				if req.URL.Path == "/batch/createBatchTask.action" {
					var items []fileRefItem
					if err := json.Unmarshal([]byte(req.Form.Get("taskInfos")), &items); err != nil {
						return nil, err
					}
					if len(items) != 1 || items[0].IsFolder != 1 || items[0].FileName != "资料" || req.Form.Get("type") != action {
						t.Errorf("batch payload = %+v, type = %q", items, req.Form.Get("type"))
					}
					return pan189AuthResponse(req, http.StatusOK, nil, `{"taskId":9007199254740993}`), nil
				}
				if req.URL.Path == "/batch/checkBatchTask.action" && req.Form.Get("taskId") == "9007199254740993" {
					checked = true
					return pan189AuthResponse(req, http.StatusOK, nil, `{"taskStatus":4}`), nil
				}
				return nil, fmt.Errorf("unexpected batch request %s", req.URL.Path)
			})
			d := &Driver{}
			var err error
			switch action {
			case "MOVE":
				_, err = d.Move(t.Context(), c, []drive.FileRef{{ID: "folder"}}, "target", "")
			case "COPY":
				_, err = d.Copy(t.Context(), c, []drive.FileRef{{ID: "folder"}}, "target", "")
			case "DELETE":
				_, err = d.Trash(t.Context(), c, []string{"folder"})
			}
			if err != nil || !checked {
				t.Fatalf("batch error = %v, completion checked = %v", err, checked)
			}
		})
	}
}

func TestListPageRejectsInvalidIdentity(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	for _, entries := range []string{
		`[{"name":"missing"}]`, `[{"id":null}]`, `[{"id":""}]`, `[{"id":" "}]`,
		`[{"id":{}}]`, `[{"id":true}]`, `[{"id":1.5}]`, `[{"id":1e3}]`,
		`[{"id":123},{"id":"123"}]`,
	} {
		t.Run(entries, func(t *testing.T) {
			netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return pan189AuthResponse(req, http.StatusOK, nil, `{"fileListAO":{"folderList":`+entries+`}}`), nil
			})
			items, _, err := (&Driver{}).listPage(t.Context(), drive.Context{Token: &model.TokenInfo{AccessToken: "skey", RefreshToken: mustJSON(sessionForTest())}}, PAN189PersonalRoot, 1)
			if err == nil || len(items) != 0 {
				t.Fatalf("invalid entries returned items=%+v error=%v", items, err)
			}
		})
	}
}

func TestGetInfoPseudoEntries(t *testing.T) {
	d := &Driver{}
	c := drive.Context{DriveID: "pan189:u"}
	for _, root := range []string{PAN189Root, "-11", "root", "/"} {
		info, err := d.GetInfo(t.Context(), c, root)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := info.(model.File)
		if !ok || !f.IsDir || f.FileID != PAN189Root {
			t.Fatalf("GetInfo(%q) = %+v", root, info)
		}
	}
	info, err := d.GetInfo(t.Context(), c, "file-1")
	if err != nil {
		t.Fatal(err)
	}
	f := info.(model.File)
	if f.IsDir || f.FileID != "file-1" {
		t.Fatalf("GetInfo(file-1) = %+v", info)
	}
}

func TestCreateShareUsesPersonalShareEndpoint(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	var requests int
	netx.TestTransportHook = pan189AuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodGet || req.URL.Host != "cloud.189.cn" || req.URL.Path != "/api/open/share/createShareLink.action" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL.String())
		}
		query := req.URL.Query()
		if query.Get("fileId") != "file-1" || query.Get("expireTime") != "7" || query.Get("shareType") != "3" || query.Get("noCache") == "" {
			return nil, fmt.Errorf("share query = %s", query.Encode())
		}
		if req.Header.Get("SessionKey") != "session-key" {
			return nil, fmt.Errorf("session key = %q", req.Header.Get("SessionKey"))
		}
		return pan189AuthResponse(req, http.StatusOK, nil, `{"res_code":0,"shareLinkList":[{"shareId":12345,"accessCode":"p4ss","accessUrl":"https://cloud.189.cn/t/test-share"}]}`), nil
	})

	sess := &Session{SessionKey: "session-key", SessionSecret: "session-secret", CloudType: CloudPersonal}
	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "pan189:user", DriveID: "pan189:user",
		Token: &model.TokenInfo{AccessToken: sess.SessionKey, RefreshToken: mustJSON(sess)},
	}, drive.ShareParams{FileIDs: []string{"file-1"}, ShareName: "测试分享", Expiration: "7"})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
	if item.ShareID != "12345" || item.ShareURL != "https://cloud.189.cn/t/test-share" || item.SharePwd != "p4ss" || item.FileID != "file-1" {
		t.Fatalf("share = %+v", item)
	}
}

func TestCreateShareRejectsFamilyCloud(t *testing.T) {
	sess := &Session{
		SessionKey: "session-key", SessionSecret: "session-secret", CloudType: CloudFamily,
		FamilySessionKey: "family-key", FamilySessionSecret: "family-secret",
	}
	_, err := (&Driver{}).CreateShare(t.Context(), drive.Context{Token: &model.TokenInfo{AccessToken: sess.SessionKey, RefreshToken: mustJSON(sess)}}, drive.ShareParams{FileIDs: []string{"file-1"}})
	if err == nil {
		t.Fatal("family cloud share must be rejected before a request")
	}
}
