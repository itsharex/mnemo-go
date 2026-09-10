// Package e2e contains end-to-end integration tests exercising the real drive
// pipeline (store → ops facade → provider driver → HTTP client) against a
// local in-process WebDAV server. No external services are needed.
package e2e

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/webdav"

	"mnemo-go/internal/drive"
	_ "mnemo-go/internal/drive/providers" // register all providers
	"mnemo-go/internal/model"
	webdavclient "mnemo-go/internal/provider/webdav"
	"mnemo-go/internal/store"
)

// startWebDAV spins up an in-process WebDAV server over a temp dir.
func startWebDAV(t *testing.T, dir string) (url string, stop func()) {
	t.Helper()
	handler := &webdav.Handler{
		FileSystem: webdav.Dir(dir),
		LockSystem: webdav.NewMemLS(),
	}
	srv := &http.Server{Handler: handler}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }
}

// TestWebDAVEndToEnd runs list/mkdir/upload/download/rename/move/copy/delete
// through the real drive facade against a live local WebDAV server.
func TestWebDAVEndToEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "mounted"), 0o755); err != nil {
		t.Fatal(err)
	}
	url, stop := startWebDAV(t, dir)
	defer stop()

	// 1. persist a mounted webdav account
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	uid := model.BuildUserID("webdav", "e2e")
	tok := &model.TokenInfo{
		TokenFrom: model.ProviderWebdav,
		UserID:    uid,
		UserName:  "e2e",
		Conn:      &model.ConnConfig{Endpoint: url, RootPath: "/mounted"},
	}
	driveID := model.BuildDriveID("webdav", "e2e")
	acc := &model.Account{UserID: uid, DriveID: driveID, Token: tok}
	if err := st.SaveAccount(acc); err != nil {
		t.Fatal(err)
	}
	drive.SetTokenResolver(func(userID, _ string) (*model.TokenInfo, error) {
		a, err := st.GetAccount(userID)
		if err != nil {
			return nil, err
		}
		return a.Token, nil
	})

	// 2. list root (empty)
	files, err := drive.ListDir(uid, driveID, "/", nil)
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("root should be empty, got %d", len(files))
	}

	// 3. mkdir
	mk, err := drive.Mkdir(uid, driveID, "/", "sub")
	if err != nil || mk == nil || mk.FileID == "" {
		t.Fatalf("mkdir: %v %+v", err, mk)
	}

	// 4. upload a file into sub
	payload := []byte("hello mnemo e2e")
	upPath := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(upPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	ui := &model.UploadingUI{
		UploadID: "up-1",
		Info: model.UploadInfo{
			LocalFilePath: upPath, ParentFileID: mk.FileID,
			DriveID: driveID, Name: "hello.txt", Size: int64(len(payload)),
		},
		Upload: model.UploadState{},
	}
	handler, err := drive.QueueUploadHandler(uid, driveID)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), ui); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// 5. list sub → hello.txt present
	files, err = drive.ListDir(uid, driveID, mk.FileID, nil)
	if err != nil {
		t.Fatalf("list sub: %v", err)
	}
	if len(files) != 1 || files[0].Name != "hello.txt" {
		t.Fatalf("expected hello.txt, got %+v", files)
	}
	if files[0].Size != int64(len(payload)) {
		t.Fatalf("size mismatch: %d", files[0].Size)
	}

	// Names containing URL control characters and Unicode must remain one path segment.
	specialName := "space # 中.txt"
	specialUI := &model.UploadingUI{
		UploadID: "up-special",
		Info: model.UploadInfo{
			LocalFilePath: upPath, ParentFileID: mk.FileID,
			DriveID: driveID, Name: specialName, Size: int64(len(payload)),
		},
	}
	if err := handler(context.Background(), specialUI); err != nil {
		t.Fatalf("special-name upload: %v", err)
	}
	files, err = drive.ListDir(uid, driveID, mk.FileID, nil)
	if err != nil || !hasFile(files, specialName) {
		t.Fatalf("special-name list: err=%v files=%+v", err, files)
	}

	// 6. download URL + fetch content
	dl, err := drive.GetDownloadURL(uid, driveID, files[0].FileID, 60)
	if err != nil {
		t.Fatalf("download url: %v", err)
	}
	if dl.DownloadMode == "proxy" || dl.Concurrency == 1 {
		t.Fatal("ordinary WebDAV downloads must not be forced to one connection")
	}
	resp, err := http.Get(dl.URL)
	if err != nil {
		t.Fatalf("get url: %v", err)
	}
	buf := make([]byte, len(payload))
	n, _ := resp.Body.Read(buf)
	resp.Body.Close()
	if string(buf[:n]) != string(payload) {
		t.Fatalf("download content mismatch: %q", buf[:n])
	}

	// 7. rename
	if _, err := drive.RenameBatch(uid, driveID, []drive.FileRef{{ID: files[0].FileID}}, []string{"renamed.txt"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	files, _ = drive.ListDir(uid, driveID, mk.FileID, nil)
	if !hasFile(files, "renamed.txt") {
		t.Fatalf("rename failed: %+v", files)
	}
	renamedID := fileIDByName(files, "renamed.txt")

	// 8. move to root
	rid, _ := drive.RootID(uid, driveID)
	if _, err := drive.MoveBatch(uid, driveID, []drive.FileRef{{ID: renamedID}}, rid, ""); err != nil {
		t.Fatalf("move: %v", err)
	}
	rootFiles, _ := drive.ListDir(uid, driveID, "/", nil)
	if !hasFile(rootFiles, "renamed.txt") {
		t.Fatalf("move failed, renamed.txt missing from root: %+v", names(rootFiles))
	}
	isDir := true
	if _, err := drive.MoveBatch(uid, driveID, []drive.FileRef{{ID: mk.FileID, IsDir: &isDir}}, mk.FileID, ""); err == nil {
		t.Fatal("moving a directory into itself must fail before the server request")
	}

	// 9. copy
	rootFileID := fileIDByName(rootFiles, "renamed.txt")
	if rootFileID == "" {
		t.Fatalf("copy source renamed.txt missing: %+v", names(rootFiles))
	}
	if _, err := drive.CopyBatch(uid, driveID, []drive.FileRef{{ID: rootFileID}}, mk.FileID, ""); err != nil {
		t.Fatalf("copy: %v", err)
	}
	subFiles, _ := drive.ListDir(uid, driveID, mk.FileID, nil)
	if !hasFile(subFiles, "renamed.txt") {
		t.Fatalf("copy failed: %+v", names(subFiles))
	}
	copiedID := fileIDByName(subFiles, "renamed.txt")

	// 10. delete both
	if _, err := drive.DeleteBatch(uid, driveID, []drive.FileRef{{ID: rootFileID}, {ID: copiedID}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rootFiles, _ = drive.ListDir(uid, driveID, "/", nil)
	subFiles, _ = drive.ListDir(uid, driveID, mk.FileID, nil)
	if hasFile(rootFiles, "renamed.txt") || hasFile(subFiles, "renamed.txt") {
		t.Fatalf("delete incomplete: root=%v sub=%v", names(rootFiles), names(subFiles))
	}
}

func TestWebDAVConnectionKeepsCollectionSlashAndReportsQuota(t *testing.T) {
	const response = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/dav/</D:href>
    <D:propstat><D:prop>
      <D:resourcetype><D:collection/></D:resourcetype>
      <D:quota-used-bytes>1024</D:quota-used-bytes>
      <D:quota-available-bytes>3072</D:quota-available-bytes>
    </D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
  </D:response>
</D:multistatus>`
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "PROPFIND" || r.URL.Path != "/dav/" {
			http.Error(w, "WebDAV collection URL must end with a slash", 530)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "app-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV"`)
			http.Error(w, "application password required", 530)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)

	conn := &model.ConnConfig{
		Endpoint: srv.URL + "/dav/", Username: "alice", Password: "app-password", RootPath: "/",
	}
	if err := drive.ValidateConnection(model.ProviderWebdav, conn); err != nil {
		t.Fatalf("validate WebDAV collection: %v", err)
	}
	client, err := webdavclient.New(conn, 0)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := client.Stat(context.Background(), "/")
	if err != nil || entry == nil || !entry.IsDir {
		t.Fatalf("stat collection = %#v, err = %v", entry, err)
	}
	used, total, err := client.Quota(context.Background(), "/")
	if err != nil || used != 1024 || total != 4096 {
		t.Fatalf("quota = %d/%d, err = %v", used, total, err)
	}
	if requests != 3 {
		t.Fatalf("request count = %d, want 3", requests)
	}
}

func TestWebDAVDigestAuthenticationNegotiatesOnceAndReusesChallenge(t *testing.T) {
	const response = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:"><D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	const challenge = `Digest realm="Mnemo", nonce="test-nonce", algorithm=SHA-256, qop="auth"`
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "PROPFIND" || r.URL.Path != "/dav/" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Digest ") {
			w.Header().Set("WWW-Authenticate", challenge)
			// Some WebDAV gateways use 530 instead of a standard 401 before
			// the challenge response. The client must retry only for this
			// explicit Digest challenge.
			http.Error(w, "Digest required", 530)
			return
		}
		params := digestTestParams(authorization)
		if params["username"] != "alice" || params["uri"] != "/dav/" || params["algorithm"] != "SHA-256" || params["qop"] != "auth" || params["nc"] == "" || params["cnonce"] == "" {
			http.Error(w, "invalid Digest authorization", http.StatusUnauthorized)
			return
		}
		hash := func(value string) string {
			sum := sha256.Sum256([]byte(value))
			return fmt.Sprintf("%x", sum[:])
		}
		ha1 := hash("alice:Mnemo:app-password")
		ha2 := hash("PROPFIND:/dav/")
		wantResponse := hash(ha1 + ":test-nonce:" + params["nc"] + ":" + params["cnonce"] + ":auth:" + ha2)
		if params["response"] != wantResponse {
			http.Error(w, "Digest response mismatch", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)

	conn := &model.ConnConfig{Endpoint: srv.URL + "/dav/", Username: "alice", Password: "app-password"}
	if err := drive.ValidateConnection(model.ProviderWebdav, conn); err != nil {
		t.Fatalf("validate Digest WebDAV: %v", err)
	}
	if requests != 2 {
		t.Fatalf("initial Digest negotiation used %d requests, want 2", requests)
	}
	client, err := webdavclient.New(conn, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stat(context.Background(), "/"); err != nil {
		t.Fatalf("cached Digest stat: %v", err)
	}
	if requests != 3 {
		t.Fatalf("cached Digest request count = %d, want 3", requests)
	}
	_, requestAuth, err := client.DownloadAuth()
	if err != nil || requestAuth == nil {
		t.Fatalf("DownloadAuth = %v, %v", requestAuth, err)
	}
	first, _ := http.NewRequest(http.MethodGet, srv.URL+"/dav/file.txt", nil)
	second, _ := http.NewRequest(http.MethodGet, srv.URL+"/dav/file.txt", nil)
	if err := requestAuth(first); err != nil {
		t.Fatalf("first download auth: %v", err)
	}
	if err := requestAuth(second); err != nil {
		t.Fatalf("second download auth: %v", err)
	}
	if first.Header.Get("Authorization") == second.Header.Get("Authorization") {
		t.Fatal("Digest download authorization must use a fresh nonce count per request")
	}
}

func digestTestParams(authorization string) map[string]string {
	params := make(map[string]string)
	for _, item := range strings.Split(strings.TrimSpace(strings.TrimPrefix(authorization, "Digest")), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			continue
		}
		params[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return params
}

func TestWebDAVConnectionErrorIncludesSafeServerDiagnostics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Digest realm="WebDAV"`)
		w.Header().Set("Location", "/correct/dav/")
		w.WriteHeader(530)
		_, _ = w.Write([]byte("use the WebDAV endpoint and an application password"))
	}))
	t.Cleanup(srv.Close)

	err := drive.ValidateConnection(model.ProviderWebdav, &model.ConnConfig{
		Endpoint: srv.URL + "/wrong/", Username: "alice", Password: "secret",
	})
	if err == nil {
		t.Fatal("expected WebDAV validation error")
	}
	message := err.Error()
	for _, want := range []string{"530", "application password", "Digest", "挑战参数不受支持", "/correct/dav/"} {
		if !strings.Contains(message, want) {
			t.Fatalf("diagnostic %q missing %q", message, want)
		}
	}
	if strings.Contains(message, "secret") {
		t.Fatalf("diagnostic leaked password: %q", message)
	}
}

func hasFile(files []model.File, name string) bool {
	for _, f := range files {
		if f.Name == name {
			return true
		}
	}
	return false
}

func fileIDByName(files []model.File, name string) string {
	for _, f := range files {
		if f.Name == name {
			return f.FileID
		}
	}
	return ""
}

func names(files []model.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}
