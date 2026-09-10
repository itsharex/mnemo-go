package updater

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mnemo-go/internal/netx"
)

func TestParseChecksumsUsesOnlyValidSHA256Entries(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got := parseChecksums([]byte(valid + "  Mnemo-windows-x64-Setup.exe\ninvalid  ignored\n"))
	want := map[string]string{"Mnemo-windows-x64-Setup.exe": valid}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseChecksums() = %#v, want %#v", got, want)
	}
}

func TestIsNewerVersionComparesReleaseNumbers(t *testing.T) {
	cases := []struct {
		candidate string
		current   string
		want      bool
	}{
		{candidate: "v0.2.2", current: "v0.2.1", want: true},
		{candidate: "0.2.1", current: "v0.2.1", want: false},
		{candidate: "v0.2.0", current: "v0.2.1", want: false},
		{candidate: "v1.0", current: "v0.9.9", want: true},
		{candidate: "v0.3.0", current: "v0.3.0-rc.1", want: true},
		{candidate: "v0.3.0-rc.10", current: "v0.3.0-rc.2", want: true},
		{candidate: "v0.3.0-rc.1", current: "v0.3.0", want: false},
		{candidate: "nightly", current: "v0.3.0", want: false},
		{candidate: "v0.3.0-rc.01", current: "v0.2.0", want: false},
		{candidate: "v0.3.0+build.2", current: "v0.3.0+build.1", want: false},
	}
	for _, tc := range cases {
		if got := isNewerVersion(tc.candidate, tc.current); got != tc.want {
			t.Errorf("isNewerVersion(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}

func TestVerifyChecksumRequiresValidExpectedHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.exe")
	if err := os.WriteFile(path, []byte("installer"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"", strings.Repeat("z", 64), "abcd"} {
		ok, err := VerifyChecksum(path, expected)
		if err == nil || ok {
			t.Errorf("invalid digest accepted: %q %v", expected, err)
		}
	}
}

func TestChecksumParserRejectsInvalidHexAndSupportsBinaryMarker(t *testing.T) {
	hash := strings.Repeat("a", 64)
	got := parseChecksums([]byte(strings.Repeat("z", 64) + "  invalid.exe\n" + hash + " *valid.exe\n"))
	if !reflect.DeepEqual(got, map[string]string{"valid.exe": hash}) {
		t.Fatalf("checksums=%v", got)
	}
}

func TestFailedDownloadPreservesExistingDestination(t *testing.T) {
	netx.TestTransportHook = http.DefaultTransport
	t.Cleanup(func() { netx.TestTransportHook = nil })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "short")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "update.exe")
	if err := os.WriteFile(path, []byte("previous-verified-package"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Download(context.Background(), srv.URL, path, nil); err == nil {
		t.Fatal("truncated response accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "previous-verified-package" {
		t.Fatalf("existing package lost: %q %v", data, err)
	}
}

func TestFetchBodyRejectsOversizedManifest(t *testing.T) {
	netx.TestTransportHook = http.DefaultTransport
	t.Cleanup(func() { netx.TestTransportHook = nil })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "12345") }))
	defer srv.Close()
	if _, err := fetchBody(context.Background(), srv.URL, 4); err == nil {
		t.Fatal("oversized body silently truncated")
	}
}

func TestReleaseAssetSelectionMatchesPlatformAndRequiresIntegrity(t *testing.T) {
	for _, platform := range []struct{ os, arch, name string }{
		{"windows", "amd64", "Mnemo-windows-x64-Setup.exe"},
		{"windows", "arm64", "Mnemo-windows-arm64-Setup.exe"},
		{"darwin", "arm64", "Mnemo-macos-arm64.dmg"},
		{"linux", "amd64", "Mnemo-linux-x64.tar.gz"},
	} {
		t.Run(platform.os+platform.arch, func(t *testing.T) {
			asset := releaseAsset{Name: platform.name, URL: "https://github.com/" + repoOwner + "/" + repoName + "/releases/download/v0.3.0/" + platform.name, Size: 100, Digest: "sha256:" + strings.Repeat("a", 64)}
			rel := releaseInfo{Tag: "v0.3.0", Assets: []releaseAsset{asset}}
			info, err := selectRelease(context.Background(), rel, platform.os, platform.arch, "v0.2.2")
			if err != nil || info == nil || info.Name != platform.name || info.CanInstall != (platform.os == "windows") {
				t.Fatalf("selection: %+v %v", info, err)
			}
			rel.Assets[0].Digest = ""
			if _, err := selectRelease(context.Background(), rel, platform.os, platform.arch, "v0.2.2"); err == nil {
				t.Fatal("missing digest silently accepted")
			}
		})
	}
	if _, err := selectRelease(context.Background(), releaseInfo{Tag: "v0.3.0"}, "windows", "amd64", "v0.2.2"); err == nil {
		t.Fatal("missing package reported as latest")
	}
	if info, err := selectRelease(context.Background(), releaseInfo{Tag: "v0.4.0-beta.1", Prerelease: true}, "windows", "amd64", "v0.3.0"); err != nil || info != nil {
		t.Fatal("stable channel accepted prerelease")
	}
}

func TestUpdateAssetURLCannotEscapeReleaseRepository(t *testing.T) {
	name := "Mnemo-windows-x64-Setup.exe"
	for _, raw := range []string{"http://github.com/" + repoOwner + "/" + repoName + "/releases/download/v0.3.0/" + name, "https://example.com/" + name, "https://github.com/other/repo/releases/download/v0.3.0/" + name} {
		if trustedAssetURL(raw, "v0.3.0", name) {
			t.Errorf("untrusted URL accepted: %s", raw)
		}
	}
}

func TestInstallerArgumentsPreserveInstallDirectoryAndExplicitRestart(t *testing.T) {
	args := installerArguments("C:/Program Files/Mnemo")
	if args[len(args)-1] != "/DIR=C:/Program Files/Mnemo" {
		t.Fatalf("directory split: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "/MNEMORESTART=1") {
		t.Fatal("silent restart flag missing")
	}
}
