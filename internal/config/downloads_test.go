package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultDownloadDirUsesRedirectedLocationWithoutCreatingIt(t *testing.T) {
	previous := systemDownloadDir
	t.Cleanup(func() { systemDownloadDir = previous })
	want := filepath.Join(t.TempDir(), "迁移的下载目录")
	systemDownloadDir = func() (string, error) { return want, nil }
	got, err := ResolveDownloadDir("  ")
	if err != nil || got != want {
		t.Fatalf("resolved default = %q, %v; want %q", got, err, want)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("resolving a location created or accessed it: %v", err)
	}
}

func TestDefaultDownloadDirFallbackNeverUsesWorkingDirectory(t *testing.T) {
	previous := systemDownloadDir
	t.Cleanup(func() { systemDownloadDir = previous })
	systemDownloadDir = func() (string, error) { return "", errors.New("native lookup unavailable") }
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	got, err := DefaultDownloadDir()
	if err != nil || got != filepath.Join(home, "Downloads") {
		t.Fatalf("fallback = %q, %v", got, err)
	}
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOME", "")
	if got, err := DefaultDownloadDir(); err == nil || got != "" {
		t.Fatalf("missing home produced %q, %v", got, err)
	}
}

func TestNormalizeDownloadDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("MNEMO_DOWNLOAD_ROOT", home)
	t.Setenv("MNEMO_UNKNOWN_DOWNLOAD_ROOT", "")
	want := filepath.Join(home, "中文 下载")
	for _, raw := range []string{
		want, `"` + want + `"`, " '" + want + "' ",
		"~/中文 下载", `%MNEMO_DOWNLOAD_ROOT%/中文 下载`,
		"$MNEMO_DOWNLOAD_ROOT/中文 下载", "${MNEMO_DOWNLOAD_ROOT}/中文 下载",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := NormalizeDownloadDir(raw)
			if err != nil || got != want {
				t.Fatalf("NormalizeDownloadDir(%q) = %q, %v; want %q", raw, got, err, want)
			}
		})
	}
	for _, raw := range []string{"", "  ", `""`} {
		if got, err := NormalizeDownloadDir(raw); err != nil || got != "" {
			t.Fatalf("default preference changed: %q, %v", got, err)
		}
	}
	for _, raw := range []string{"Downloads", "../downloads", "~someone/Downloads", "%MNEMO_UNKNOWN_DOWNLOAD_ROOT%/Downloads", "${MNEMO_UNKNOWN_DOWNLOAD_ROOT}/Downloads", home + "\x00bad"} {
		if got, err := NormalizeDownloadDir(raw); err == nil {
			t.Errorf("invalid input %q accepted as %q", raw, got)
		}
	}
}

func TestNormalizeWindowsDownloadPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path rules")
	}
	for raw, want := range map[string]string{
		`D:/资料/Downloads/../下载`: `D:\资料\下载`,
		`\\server\share\我的下载`:   `\\server\share\我的下载`,
		`D:\100% complete`:      `D:\100% complete`,
	} {
		if got, err := NormalizeDownloadDir(raw); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{`D:Downloads`, `\Downloads`} {
		if got, err := NormalizeDownloadDir(raw); err == nil {
			t.Errorf("drive-relative path accepted: %q", got)
		}
	}
}

func TestEnsureDownloadDirCreatesAndChecksDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "中文 下载", "nested")
	if err := EnsureDownloadDir(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("write probe was not removed: %v, %v", entries, err)
	}
	blocked := filepath.Join(dir, "ordinary-file")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDownloadDir(blocked); err == nil {
		t.Fatal("regular file accepted as directory")
	}
	if err := EnsureDownloadDir("relative"); err == nil {
		t.Fatal("relative destination accepted")
	}
}

func TestDefaultDownloadDirCurrentSystem(t *testing.T) {
	dir, err := DefaultDownloadDir()
	if err != nil || !filepath.IsAbs(dir) {
		t.Fatalf("system Downloads = %q, %v", dir, err)
	}
	t.Logf("system Downloads: %s", dir)
}
