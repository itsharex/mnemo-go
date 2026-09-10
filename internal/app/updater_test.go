package app

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mnemo-go/internal/netx"
	"mnemo-go/internal/updater"
)

func waitUpdateRun(t *testing.T, run *updateDownloadRun) {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		t.Fatal("update worker did not stop")
	}
}

func updateFixture(t *testing.T, endpoint, body string) *App {
	t.Helper()
	hash := sha256.Sum256([]byte(body))
	return &App{dataDir: t.TempDir(), updateInfo: &updater.Info{Name: "Mnemo-windows-x64-Setup.exe", URL: endpoint, Size: int64(len(body)), SHA256: hex.EncodeToString(hash[:]), Version: "v0.3.0", CanInstall: true}}
}

func TestUpdateDownloadIsSingleFlightAndCancelable(t *testing.T) {
	netx.TestTransportHook = http.DefaultTransport
	t.Cleanup(func() { netx.TestTransportHook = nil })
	started := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	a := updateFixture(t, srv.URL, "package")
	path, err := a.DownloadUpdate(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	second, err := a.DownloadUpdate(srv.URL)
	if err != nil || second != path {
		t.Fatalf("duplicate: %q %v", second, err)
	}
	a.updateMu.Lock()
	run := a.updateRun
	a.updateMu.Unlock()
	if !a.CancelUpdate() {
		t.Fatal("cancel rejected")
	}
	waitUpdateRun(t, run)
	if calls.Load() != 1 || a.GetUpdateStatus().Phase != "canceled" {
		t.Fatalf("status=%+v calls=%d", a.GetUpdateStatus(), calls.Load())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("canceled package remains")
	}
}

func TestDownloadedUpdateRejectsTamperingBeforeInstallation(t *testing.T) {
	netx.TestTransportHook = http.DefaultTransport
	t.Cleanup(func() { netx.TestTransportHook = nil })
	const body = "verified-package"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer srv.Close()
	a := updateFixture(t, srv.URL, body)
	path, err := a.DownloadUpdate(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	a.updateMu.Lock()
	run := a.updateRun
	a.updateMu.Unlock()
	if run != nil {
		waitUpdateRun(t, run)
	}
	if state := a.GetUpdateStatus(); state.Phase != "done" || state.Path != path {
		t.Fatalf("state=%+v", state)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", len(body))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyUpdate(path); err == nil {
		t.Fatal("tampered installer accepted")
	}
	if a.updateApplying {
		t.Fatal("failed installation blocked future attempts")
	}
}

func TestApplyUpdateRejectsUnverifiedCacheFile(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	path := filepath.Join(updater.DownloadDir(a.dataDir), "unverified.exe")
	if err := os.WriteFile(path, []byte("not-an-installer"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyUpdate(path); err == nil {
		t.Fatal("unverified cache file accepted")
	}
}

func TestApplyUpdateInvalidatesMissingOrChangedPackage(t *testing.T) {
	for _, content := range []string{"", "changed-size"} {
		t.Run(content, func(t *testing.T) {
			a := updateFixture(t, "https://example.test/update.exe", "original")
			path := filepath.Join(updater.DownloadDir(a.dataDir), "package.exe")
			if content != "" {
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a.updateReady = &verifiedUpdate{info: cloneUpdateInfo(a.updateInfo), path: path}
			a.updateState = UpdateStatus{Phase: "done", Path: path, Info: updateResult(a.updateInfo)}
			if err := a.ApplyUpdate(path); err == nil {
				t.Fatal("invalid package accepted")
			}
			if a.updateReady != nil || a.GetUpdateStatus().Path != "" {
				t.Fatal("invalid package remains available for installation")
			}
		})
	}
}
