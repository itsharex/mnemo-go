package sync

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
