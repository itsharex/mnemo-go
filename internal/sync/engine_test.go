package sync

import (
	"context"
	"fmt"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type changingRemoteDriver struct {
	drive.Driver
	files             []model.File
	uploaded, deleted []string
}

var syncTestRemote *changingRemoteDriver

func init() {
	drive.Register(drive.Registration{ID: "webdav", Caps: drive.Capabilities{Upload: true, Download: true, RecycleBin: true, UploadConflictPolicies: []string{"overwrite"}}, Factory: func() drive.Driver { return syncTestRemote }})
}
func (d *changingRemoteDriver) ListPaged(ctx context.Context, c drive.Context, id, marker string, opts *drive.ListOptions) (*drive.DirPage, error) {
	return &drive.DirPage{Items: append([]model.File(nil), d.files...)}, nil
}
func (d *changingRemoteDriver) UploadOneFile(ctx context.Context, c drive.Context, u *model.UploadingUI) error {
	d.uploaded = append(d.uploaded, u.Info.Name)
	return nil
}
func (d *changingRemoteDriver) Trash(ctx context.Context, c drive.Context, ids []string) ([]string, error) {
	d.deleted = append(d.deleted, ids...)
	return ids, nil
}

type testSyncSnapshots struct{ entries []Entry }

func (s *testSyncSnapshots) LoadSyncSnapshot(string) ([]Entry, error) { return s.entries, nil }
func (s *testSyncSnapshots) SaveSyncSnapshot(_ string, entries []Entry) error {
	s.entries = entries
	return nil
}
func (s *testSyncSnapshots) ClearSyncSnapshot(string) error { s.entries = nil; return nil }

func TestSyncRejectsRemoteDeletionAndNewFileRaces(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		t.Run(fmt.Sprint(deletion), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("local"), 0600); err != nil {
				t.Fatal(err)
			}
			d := &changingRemoteDriver{files: []model.File{{FileID: "a", Name: "a.txt", Size: 3, Time: 1}}}
			cfg := Config{ID: "test", UserID: "webdav:test", DriveID: "webdav:test", LocalDir: root, RemoteDir: "root", Direction: "two-way", ConflictPolicy: "local", DeletePropagation: true}
			snap := &testSyncSnapshots{}
			if deletion {
				d.files = append(d.files, model.File{FileID: "b", Name: "b.txt", Size: 3, Time: 1})
				for _, f := range d.files {
					snap.entries = append(snap.entries, Entry{Scope: configScope(cfg), RemoteName: f.Name, Paired: true, LocalSize: 3, LocalTime: 1, RemoteSize: 3, RemoteTime: 1, Hash: ":"})
				}
			} else if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("local"), 0600); err != nil {
				t.Fatal(err)
			}
			syncTestRemote = d
			e := NewEngine(func(_ string, done, total int) {
				if done == 1 {
					if deletion {
						d.files[1].ContentHash = "changed"
					} else {
						d.files = append(d.files, model.File{FileID: "new-b", Name: "b.txt", Size: 5, Time: 1})
					}
				}
			}, WithSnapshotStore(snap))
			err := e.ExecutePlan(context.Background(), cfg, "", nil)
			if err == nil || len(d.deleted) != 0 || len(d.uploaded) != 1 {
				t.Fatalf("remote change was not protected: uploads=%v deletes=%v err=%v", d.uploaded, d.deleted, err)
			}
		})
	}
}
func TestSyncRejectsRemoteChangeAfterEarlierTransfer(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("local"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := &changingRemoteDriver{files: []model.File{{FileID: "a", Name: "a.txt", Size: 3, Time: 1}, {FileID: "b", Name: "b.txt", Size: 3, Time: 1}}}
	syncTestRemote = d
	e := NewEngine(func(_ string, done, total int) {
		if done == 1 {
			d.files[1].Time = 999
		}
	})
	err := e.ExecutePlan(context.Background(), Config{ID: "test", UserID: "webdav:test", DriveID: "webdav:test", LocalDir: root, RemoteDir: "root", Direction: "two-way", ConflictPolicy: "local"}, "", nil)
	if err == nil || len(d.uploaded) != 1 || d.uploaded[0] != "a.txt" {
		t.Fatalf("remote edit overwritten: uploads=%v err=%v", d.uploaded, err)
	}
}

func TestRunRejectsUnknownDirectionBeforeAccessingFiles(t *testing.T) {
	err := NewEngine(nil).Run(context.Background(), Config{Direction: "pul"})
	if err == nil || !strings.Contains(err.Error(), "direction") {
		t.Fatalf("invalid direction must be rejected explicitly, got %v", err)
	}
}

func TestGuardDeleteThreshold(t *testing.T) {
	eng := NewEngine(nil)
	// guardDelete protects against deleting more than 50% of snapshot entries.
	// With a snapshot of 10 entries, deleting 4 should be allowed, 6 blocked.
	// 4 deletions (< 50%) → allowed
	if !eng.guardDelete("t1", 4, 10) {
		t.Error("guardDelete(10,4) should allow")
	}
	// 6 deletions (> 50%) → blocked
	if eng.guardDelete("t1", 6, 10) {
		t.Error("guardDelete(10,6) should block")
	}
	// exactly 50% → allowed (ratio > 0.5 blocked, 0.5 is not > 0.5)
	if !eng.guardDelete("t1", 5, 10) {
		t.Error("guardDelete(10,5) should allow (ratio==0.5 not >0.5)")
	}
	// empty snapshot → block
	if eng.guardDelete("t1", 1, 0) {
		t.Error("guardDelete(0,1) should block")
	}
}

func TestConfigFields(t *testing.T) {
	cfg := Config{ID: "t1", Enabled: true, IntervalMin: 10, DeletePropagation: true}
	if cfg.IntervalMin != 10 {
		t.Error("IntervalMin not set")
	}
	if !cfg.DeletePropagation {
		t.Error("DeletePropagation not set")
	}
}

func TestSafeLocalPathRejectsTraversalAndAllowsNestedFile(t *testing.T) {
	root := t.TempDir()
	path, err := safeLocalPath(root, "album/2026/photo.jpg")
	if err != nil {
		t.Fatalf("safeLocalPath(nested): %v", err)
	}
	want := filepath.Join(root, "album", "2026", "photo.jpg")
	if path != want {
		t.Fatalf("safeLocalPath = %q, want %q", path, want)
	}

	for _, name := range []string{"../outside.txt", "album/../../outside.txt", "/absolute.txt"} {
		if _, err := safeLocalPath(root, name); err == nil {
			t.Errorf("safeLocalPath(%q) should reject unsafe path", name)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := safeLocalPath(root, `C:\\outside.txt`); err == nil {
			t.Error("safeLocalPath should reject Windows volume path")
		}
	}
}

func TestSafeLocalPathRejectsExistingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := safeLocalPath(root, "linked/outside.txt"); err == nil {
		t.Fatal("safeLocalPath should reject a path crossing a symbolic link")
	}
}

func TestScanLocalFilesReturnsRootError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := scanLocalFiles(missing); err == nil || !strings.Contains(err.Error(), "scan local directory") {
		t.Fatalf("scanLocalFiles should expose root scan failure, got %v", err)
	}
}

func TestSafeLocalPathRejectsAliasesAndReservedNames(t *testing.T) {
	for _, name := range []string{"a/../b", "a//b", "./b", ".mnemo-sync-temp", "folder/.mnemo-sync-temp"} {
		if _, err := safeLocalPath(t.TempDir(), name); err == nil {
			t.Errorf("accepted ambiguous/reserved path %q", name)
		}
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"file.txt:stream", "NUL.txt", "folder/file.", "folder/file "} {
			if _, err := safeLocalPath(t.TempDir(), name); err == nil {
				t.Errorf("accepted Windows alias %q", name)
			}
		}
	}
}

func TestPropagateLocalDeletesCannotEscapeRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "sync")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(nil)
	err := eng.propagateLocalDeletes(Config{LocalDir: root}, []Entry{{RemoteName: "../outside.txt"}})
	if err == nil {
		t.Fatal("propagateLocalDeletes should reject traversal")
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("outside file was removed: %v", statErr)
	}
}
