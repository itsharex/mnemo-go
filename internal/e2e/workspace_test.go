package e2e

import (
	"context"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
	syncengine "mnemo-go/internal/sync"
	"mnemo-go/internal/transfer/migrate"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/webdav"
)

func TestMigrationMovePreservesChangesMadeDuringCopy(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmtBool(directory), func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			selected := "/a.txt"
			if directory {
				selected = "/folder"
				if err := os.Mkdir(filepath.Join(source, "folder"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(source, "a.txt")
			if directory {
				file = filepath.Join(source, "folder", "a.txt")
			}
			if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			sourceURL, stop := startWebDAV(t, source)
			defer stop()
			handler := &webdav.Handler{FileSystem: webdav.Dir(target), LockSystem: webdav.NewMemLS()}
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					once.Do(func() {
						changed := file
						if directory {
							changed = filepath.Join(source, "folder", "new.txt")
						}
						if err := os.WriteFile(changed, []byte("new-content-during-copy"), 0600); err != nil {
							t.Error(err)
						}
					})
				}
				handler.ServeHTTP(w, r)
			}))
			defer srv.Close()
			drive.SetTokenResolver(func(user, _ string) (*model.TokenInfo, error) {
				endpoint := sourceURL
				if user == "webdav_target" {
					endpoint = srv.URL
				}
				return &model.TokenInfo{Conn: &model.ConnConfig{Endpoint: endpoint, RootPath: "/"}}, nil
			})
			defer drive.SetTokenResolver(nil)
			job := &migrate.Job{ID: "safe-move", SrcUser: "webdav_source", SrcDrive: "webdav:source", DstUser: "webdav_target", DstDrive: "webdav:target", DstParent: "/", FileIDs: []string{selected}, Move: true}
			if err := migrate.NewEngine(nil, nil).Run(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if job.Status != "partial" {
				t.Fatalf("move must stop source cleanup after change: %+v", job)
			}
			preserved := file
			if directory {
				preserved = filepath.Join(source, "folder", "new.txt")
			}
			data, err := os.ReadFile(preserved)
			if err != nil || string(data) != "new-content-during-copy" {
				t.Fatalf("source change lost: %q %v", data, err)
			}
		})
	}
}

func TestSyncDownloadPreservesLocalEditsDuringTransfer(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmtBool(existing), func(t *testing.T) {
			remoteDir, localDir := t.TempDir(), t.TempDir()
			localPath := filepath.Join(localDir, "a.txt")
			if err := os.WriteFile(filepath.Join(remoteDir, "a.txt"), []byte("remote-content"), 0600); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.WriteFile(localPath, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			handler := &webdav.Handler{FileSystem: webdav.Dir(remoteDir), LockSystem: webdav.NewMemLS()}
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					once.Do(func() {
						if err := os.WriteFile(localPath, []byte("user-edit-during-download"), 0600); err != nil {
							t.Error(err)
						}
					})
				}
				handler.ServeHTTP(w, r)
			}))
			defer srv.Close()
			uid, did := model.BuildUserID("webdav", "concurrent"), model.BuildDriveID("webdav", "concurrent")
			tok := &model.TokenInfo{TokenFrom: model.ProviderWebdav, UserID: uid, Conn: &model.ConnConfig{Endpoint: srv.URL, RootPath: "/"}}
			drive.SetTokenResolver(func(string, string) (*model.TokenInfo, error) { return tok, nil })
			defer drive.SetTokenResolver(nil)
			cfg := syncengine.Config{ID: "concurrent", UserID: uid, DriveID: did, LocalDir: localDir, RemoteDir: "/", Direction: "pull"}
			eng := syncengine.NewEngine(nil)
			plan, err := eng.Preview(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			err = eng.ExecutePlan(context.Background(), cfg, plan.Token, nil)
			if err == nil || !strings.Contains(err.Error(), "变化") {
				t.Fatalf("concurrent local edit must stop replacement: %v", err)
			}
			data, err := os.ReadFile(localPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "user-edit-during-download" {
				t.Fatalf("local edit overwritten: %q", data)
			}
		})
	}
}

func fmtBool(value bool) string {
	if value {
		return "existing-file"
	}
	return "new-file"
}

func TestSyncPreviewConflictAndStalePlanAgainstWebDAV(t *testing.T) {
	remoteDir, localDir := t.TempDir(), t.TempDir()
	endpoint, stop := startWebDAV(t, remoteDir)
	defer stop()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid := model.BuildUserID("webdav", "sync-plan")
	did := model.BuildDriveID("webdav", "sync-plan")
	tok := &model.TokenInfo{TokenFrom: model.ProviderWebdav, UserID: uid, Conn: &model.ConnConfig{Endpoint: endpoint, RootPath: "/"}}
	drive.SetTokenResolver(func(string, string) (*model.TokenInfo, error) { return tok, nil })
	defer drive.SetTokenResolver(nil)
	cfg := syncengine.Config{ID: "plan", UserID: uid, DriveID: did, LocalDir: localDir, RemoteDir: "/", Direction: "two-way", ConflictPolicy: "keep-both"}
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(dir, name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	write(localDir, "a.txt", "local-original")
	write(remoteDir, "a.txt", "remote-version")
	eng := syncengine.NewEngine(nil, syncengine.WithSnapshotStore(st))
	plan, err := eng.Preview(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Action != "conflict" {
		t.Fatalf("expected conflict: %+v", plan)
	}
	if read(remoteDir, "a.txt") != "remote-version" {
		t.Fatal("preview mutated remote")
	}
	write(localDir, "a.txt", "local-edited-after-preview")
	if err := eng.ExecutePlan(context.Background(), cfg, plan.Token, nil); err == nil || !strings.Contains(err.Error(), "重新预览") {
		t.Fatalf("stale preview must fail: %v", err)
	}
	if read(remoteDir, "a.txt") != "remote-version" {
		t.Fatal("stale plan mutated remote")
	}
	plan, err = eng.Preview(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(context.Background(), cfg, plan.Token, nil); err != nil {
		t.Fatal(err)
	}
	if read(remoteDir, "a.txt") != "local-edited-after-preview" {
		t.Fatal("local winner was not uploaded")
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		t.Fatal(err)
	}
	copies := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "a.txt.remote-") {
			copies++
			if read(localDir, entry.Name()) != "remote-version" || read(remoteDir, entry.Name()) != "remote-version" {
				t.Fatal("both conflict versions must be preserved")
			}
		}
	}
	if copies != 1 {
		t.Fatalf("conflict copies=%d", copies)
	}
	plan, err = eng.Preview(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("unchanged paired files must not resync: %+v", plan.Changes)
	}
	write(remoteDir, "a.txt", "new-remote-data")
	plan, err = eng.Preview(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Action != "download" {
		t.Fatalf("remote-only edit must download: %+v", plan.Changes)
	}
	if err := eng.ExecutePlan(context.Background(), cfg, plan.Token, nil); err != nil {
		t.Fatal(err)
	}
	if read(localDir, "a.txt") != "new-remote-data" {
		t.Fatal("remote edit not downloaded")
	}
}
