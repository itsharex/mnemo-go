// Package updater checks GitHub releases for a newer version, downloads the
// platform installer/archive with progress, and applies it (silent install +
// restart on Windows; manual installation on macOS/Linux).
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mnemo-go/internal/config"
	"mnemo-go/internal/netx"
)

const (
	repoOwner      = "lllll081926i"
	repoName       = "mnemo-go"
	apiReleases    = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/releases/latest"
	ReleasePage    = "https://github.com/" + repoOwner + "/" + repoName + "/releases/latest"
	maxUpdateBytes = int64(2 << 30)
)

// Info describes an available update.
type Info struct {
	Name       string `json:"name"`
	CanInstall bool   `json:"canInstall"`
	Version    string `json:"version"`    // e.g. "v0.1.1" (tag name)
	URL        string `json:"url"`        // browser_download_url for this platform
	Size       int64  `json:"size"`       // asset size in bytes
	SHA256     string `json:"sha256"`     // expected checksum (from SHA256SUMS.txt)
	ReleaseURL string `json:"releaseUrl"` // html_url of the release
	Notes      string `json:"notes"`      // release body (unused in UI but available)
}

// assetSuffix returns the download asset filename suffix for the current platform.
func assetSuffix() string {
	switch runtime.GOOS {
	case "windows":
		if runtime.GOARCH == "arm64" {
			return "windows-arm64"
		}
		return "windows-x64"
	case "darwin":
		return "macos-arm64"
	default:
		if runtime.GOARCH == "arm64" {
			return "linux-arm64"
		}
		return "linux-x64"
	}
}
func currentVersion() string {
	return "v" + config.AppVersion
}

// httpClient shares the application proxy/runtime transport with provider
// requests. When no app proxy is configured, the default Go transport still
// honors HTTP_PROXY/HTTPS_PROXY/NO_PROXY.
func httpClient() *http.Client {
	return netx.NewClientWithSystemProxy(60 * time.Second).HTTP
}

func versionParts(value string) ([]int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" {
		return nil, false
	}
	value = strings.SplitN(value, "+", 2)[0]
	value = strings.SplitN(value, "-", 2)[0]
	fields := strings.Split(value, ".")
	if len(fields) == 0 || len(fields) > 3 {
		return nil, false
	}
	result := make([]int, len(fields))
	for i, field := range fields {
		if field == "" || len(field) > 7 || len(field) > 1 && field[0] == '0' {
			return nil, false
		}
		n := 0
		for _, r := range field {
			if r < '0' || r > '9' {
				return nil, false
			}
			n = n*10 + int(r-'0')
			if n > 1_000_000 {
				return nil, false
			}
		}
		result[i] = n
	}
	for len(result) < 3 {
		result = append(result, 0)
	}
	return result, true
}

func isNewerVersion(candidate, current string) bool {
	candidate = strings.TrimSpace(candidate)
	current = strings.TrimSpace(current)
	if strings.EqualFold(candidate, current) {
		return false
	}
	c, cp, cok := parsedVersion(candidate)
	p, pp, pok := parsedVersion(current)
	if !cok || !pok {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] != p[i] {
			return c[i] > p[i]
		}
	}
	if len(cp) == 0 {
		return len(pp) > 0
	}
	if len(pp) == 0 {
		return false
	}
	for i := 0; i < len(cp) && i < len(pp); i++ {
		if cp[i] == pp[i] {
			continue
		}
		cn, pn := numericIdentifier(cp[i]), numericIdentifier(pp[i])
		if cn && pn {
			if len(cp[i]) != len(pp[i]) {
				return len(cp[i]) > len(pp[i])
			}
			return cp[i] > pp[i]
		}
		if cn != pn {
			return !cn
		}
		return cp[i] > pp[i]
	}
	return len(cp) > len(pp)
}

func numericIdentifier(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func parsedVersion(value string) ([]int, []string, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	core, build, hasBuild := strings.Cut(value, "+")
	validIdentifiers := func(s string, numericZeros bool) bool {
		for _, id := range strings.Split(s, ".") {
			if id == "" || numericZeros && len(id) > 1 && id[0] == '0' && numericIdentifier(id) {
				return false
			}
			for _, r := range id {
				if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
					return false
				}
			}
		}
		return true
	}
	if hasBuild && !validIdentifiers(build, false) {
		return nil, nil, false
	}
	numbers, pre, hasPre := strings.Cut(core, "-")
	parts, ok := versionParts(numbers)
	if !ok || hasPre && !validIdentifiers(pre, true) {
		return nil, nil, false
	}
	if hasPre {
		return parts, strings.Split(pre, "."), true
	}
	return parts, nil, true
}

// Check queries GitHub for the latest release and returns an Info if a newer
// version is available. Returns nil Info (no error) when already up-to-date.
type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}
type releaseInfo struct {
	Tag        string         `json:"tag_name"`
	HTMLURL    string         `json:"html_url"`
	Body       string         `json:"body"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

func Check(ctx context.Context) (*Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiReleases, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Mnemo/"+config.AppVersion)
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("尚未发布正式版本，请稍后检查")
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("GitHub 请求受限，请稍后重试或在浏览器查看发布页")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("检查更新失败: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 {
		return nil, fmt.Errorf("发布信息超过大小限制")
	}
	var rel releaseInfo
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("发布信息格式无效: %w", err)
	}
	return selectRelease(ctx, rel, runtime.GOOS, runtime.GOARCH, currentVersion())
}

func assetNames(goos, arch string) []string {
	switch goos {
	case "windows":
		if arch == "amd64" {
			return []string{"Mnemo-windows-x64-Setup.exe"}
		}
		if arch == "arm64" {
			return []string{"Mnemo-windows-arm64-Setup.exe"}
		}
	case "darwin":
		if arch == "arm64" {
			return []string{"Mnemo-macos-arm64.dmg", "Mnemo-macos-arm64.tar.gz"}
		}
	case "linux":
		if arch == "amd64" {
			return []string{"Mnemo-linux-x64.tar.gz"}
		}
		if arch == "arm64" {
			return []string{"Mnemo-linux-arm64.tar.gz"}
		}
	}
	return nil
}

func trustedAssetURL(raw, tag, name string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		u.Path == "/"+repoOwner+"/"+repoName+"/releases/download/"+tag+"/"+name
}

func selectRelease(ctx context.Context, rel releaseInfo, goos, arch, current string) (*Info, error) {
	_, pre, valid := parsedVersion(rel.Tag)
	if !valid {
		return nil, fmt.Errorf("发布版本号无效: %s", rel.Tag)
	}
	if rel.Draft || rel.Prerelease || len(pre) > 0 || !isNewerVersion(rel.Tag, current) {
		return nil, nil
	}
	var selected *releaseAsset
	for _, name := range assetNames(goos, arch) {
		for i := range rel.Assets {
			if rel.Assets[i].Name == name {
				selected = &rel.Assets[i]
				break
			}
		}
		if selected != nil {
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("发现 %s，但尚无适合 %s/%s 的更新包，请查看发布页", rel.Tag, goos, arch)
	}
	if selected.Size <= 0 || selected.Size > maxUpdateBytes || !trustedAssetURL(selected.URL, rel.Tag, selected.Name) {
		return nil, fmt.Errorf("发布安装包信息无效")
	}
	digest := strings.TrimPrefix(selected.Digest, "sha256:")
	if !validSHA256(digest) {
		digest = ""
		for _, asset := range rel.Assets {
			if asset.Name != "SHA256SUMS.txt" {
				continue
			}
			if !trustedAssetURL(asset.URL, rel.Tag, asset.Name) {
				return nil, fmt.Errorf("校验文件地址无效")
			}
			checksums, err := fetchChecksums(ctx, asset.URL)
			if err != nil {
				return nil, fmt.Errorf("获取更新校验信息失败: %w", err)
			}
			digest = checksums[selected.Name]
			break
		}
	}
	if !validSHA256(digest) {
		return nil, fmt.Errorf("更新包缺少有效的 SHA-256 校验值，请稍后重试")
	}
	return &Info{Name: selected.Name, Version: rel.Tag, URL: selected.URL, Size: selected.Size, SHA256: strings.ToLower(digest),
		ReleaseURL: "https://github.com/" + repoOwner + "/" + repoName + "/releases/tag/" + url.PathEscape(rel.Tag), Notes: rel.Body, CanInstall: goos == "windows"}, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func fetchChecksums(ctx context.Context, rawURL string) (map[string]string, error) {
	b, err := fetchBody(ctx, rawURL, 2<<20)
	if err != nil {
		return nil, err
	}
	return parseChecksums(b), nil
}

func parseChecksums(b []byte) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && validSHA256(fields[0]) {
			name := strings.TrimPrefix(fields[1], "*")
			if filepath.Base(name) == name && !strings.ContainsAny(name, `/\`) {
				result[name] = strings.ToLower(fields[0])
			}
		}
	}
	return result
}

func fetchBody(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response exceeds size limit")
	}
	return b, nil
}

// Progress is emitted during download.
type Progress struct {
	Downloaded int64 `json:"downloaded"`
	Total      int64 `json:"total"`
}

// Download fetches the update to dest and calls onProgress with progress.
// Returns the path to the downloaded file.
func Download(ctx context.Context, rawURL, dest string, onProgress func(Progress)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	client := netx.NewClientWithSystemProxy(30 * time.Minute).HTTP
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	if resp.ContentLength > maxUpdateBytes {
		return "", fmt.Errorf("update package exceeds size limit")
	}
	out, err := os.CreateTemp(filepath.Dir(dest), ".mnemo-update-*")
	if err != nil {
		return "", err
	}
	tmpPath := out.Name()
	defer func() { out.Close(); os.Remove(tmpPath) }()
	total := resp.ContentLength
	var downloaded int64
	lastProgress := time.Time{}
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
			if downloaded > maxUpdateBytes {
				return "", fmt.Errorf("update package exceeds size limit")
			}
			if _, err := out.Write(buf[:n]); err != nil {
				return "", err
			}
			if onProgress != nil && time.Since(lastProgress) >= 200*time.Millisecond {
				onProgress(Progress{Downloaded: downloaded, Total: total})
				lastProgress = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if total >= 0 && downloaded != total {
		return "", fmt.Errorf("incomplete update download")
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return "", err
	}
	if onProgress != nil {
		onProgress(Progress{Downloaded: downloaded, Total: downloaded})
	}
	return dest, nil
}

// VerifyChecksum computes the SHA-256 of file and compares to expected.
func VerifyChecksum(path, expected string) (bool, error) {
	if !validSHA256(expected) {
		return false, fmt.Errorf("missing or invalid SHA-256 checksum")
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(got, expected), nil
}

// Apply launches the downloaded installer. The caller exits after a successful
// handoff on Windows; macOS/Linux require manual installation.
func Apply(installerPath string) error {
	switch runtime.GOOS {
	case "windows":
		return applyWindows(installerPath)
	default:
		return applyUnix(installerPath)
	}
}

// IsDownloadPath accepts only files created beneath the app's update cache.
func IsDownloadPath(dataDir, path string) bool {
	root, rootErr := filepath.Abs(DownloadDir(dataDir))
	target, targetErr := filepath.Abs(path)
	if rootErr != nil || targetErr != nil {
		return false
	}
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// DownloadDir returns a writable temp directory for the update download.
func DownloadDir(dataDir string) string {
	d := filepath.Join(dataDir, "updates")
	_ = os.MkdirAll(d, 0o755)
	return d
}
