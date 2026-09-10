package e2e

import (
	"context"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
	syncengine "mnemo-go/internal/sync"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
