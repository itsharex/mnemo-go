// Package config loads runtime configuration: secrets (OAuth client ids) and
// app directories.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Native implementations return the user's current (possibly redirected)
// Downloads folder. Other platforms retain the home/Downloads fallback.
var systemDownloadDir func() (string, error)

// DefaultDownloadDir resolves a location without creating it. In particular,
// an offline redirected folder must not silently redirect downloads elsewhere.
func DefaultDownloadDir() (string, error) {
	if systemDownloadDir != nil {
		if dir, err := systemDownloadDir(); err == nil && filepath.IsAbs(dir) {
			return filepath.Clean(dir), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", fmt.Errorf("无法确定系统下载目录，请在设置中选择绝对路径")
	}
	return filepath.Join(home, "Downloads"), nil
}

var downloadPathVariable = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_()]*)%|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// NormalizeDownloadDir keeps an empty value as the persistent "system default"
// preference, and expands explicitly configured paths without consulting cwd.
func NormalizeDownloadDir(raw string) (string, error) {
	dir := strings.TrimSpace(raw)
	if len(dir) >= 2 && ((dir[0] == '"' && dir[len(dir)-1] == '"') || (dir[0] == '\'' && dir[len(dir)-1] == '\'')) {
		dir = strings.TrimSpace(dir[1 : len(dir)-1])
	}
	if dir == "" {
		return "", nil
	}
	if strings.ContainsRune(dir, 0) {
		return "", fmt.Errorf("下载目录包含无效字符")
	}
	var expandErr error
	dir = downloadPathVariable.ReplaceAllStringFunc(dir, func(token string) string {
		groups := downloadPathVariable.FindStringSubmatch(token)
		name := groups[1] + groups[2] + groups[3]
		value, ok := os.LookupEnv(name)
		if !ok && name == "HOME" {
			var err error
			value, err = os.UserHomeDir()
			ok = err == nil
		}
		if !ok || value == "" {
			expandErr = fmt.Errorf("下载目录中的环境变量 %s 未设置", name)
			return token
		}
		return value
	})
	if expandErr != nil {
		return "", expandErr
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", fmt.Errorf("无法展开下载目录中的用户主目录")
		}
		if len(dir) == 1 {
			dir = home
		} else {
			dir = filepath.Join(home, dir[2:])
		}
	}
	dir = filepath.Clean(filepath.FromSlash(dir))
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("下载目录必须是绝对路径，也可使用 ~ 或环境变量")
	}
	return dir, nil
}

// ResolveDownloadDir converts a stored preference to the effective local path.
func ResolveDownloadDir(raw string) (string, error) {
	dir, err := NormalizeDownloadDir(raw)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return DefaultDownloadDir()
	}
	return dir, nil
}

// EnsureDownloadDir validates an already resolved path before saving a setting
// or enqueuing a task. The write probe is removed immediately.
func EnsureDownloadDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("下载目录不是有效的绝对路径")
	}
	if err := ensureWritableDir(dir); err != nil {
		return fmt.Errorf("下载目录不可用，请检查路径、磁盘连接和写入权限: %w", err)
	}
	return nil
}

// AppVersion is the application version. Should match wails.json and git tags.
const AppVersion = "0.4.0"

// Secrets holds OAuth application credentials, loaded from secrets.json in the
// data dir. Keys match the legacy app.
type Secrets struct {
	OnedriveClientID string `json:"onedrive_client_id"`
	DropboxAppKey    string `json:"dropbox_app_key"`
	// DropboxRedirectURI must exactly match the URI registered in the Dropbox
	// app console.  It is optional; the built-in rclone-compatible localhost
	// endpoint is used when it is empty.
	DropboxRedirectURI string `json:"dropbox_redirect_uri"`
}

// LoadSecrets reads secrets.json from dir if present.
func LoadSecrets(dir string) Secrets {
	var s Secrets
	dirs := []string{dir}
	// Packaged builds keep read-only OAuth configuration next to the
	// executable. This remains available when writable data is redirected to
	// the per-user fallback directory.
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "data"))
	}
	for _, candidate := range dirs {
		b, err := os.ReadFile(filepath.Join(candidate, "secrets.json"))
		if err != nil {
			continue
		}
		if json.Unmarshal(b, &s) == nil {
			return s
		}
	}
	return s
}

// UserConfigDir returns the per-user system config directory (e.g.
// %AppData%/Mnemo on Windows, ~/Library/Application Support/Mnemo on macOS).
// Login credentials (accounts.json) live here so they persist across installs
// and are independent of the install location.
func UserConfigDir(appName string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, appName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// DataDir returns the application data directory, co-located with the
// executable (installDir/data). All non-credential data — settings, tags,
// favorites, tasks, shares, sync configs, engine binaries, logs — lives here
// so it is kept with the installation and moves with it.
//
// fallbackDir is used when the executable directory is not writable (e.g.
// during development); it falls back to the user config dir.
func DataDir(appName, fallbackDir string) (string, error) {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exe), "data")
		if err := ensureWritableDir(dir); err == nil {
			return dir, nil
		}
	}
	// fallback: user config dir / data
	dir := filepath.Join(fallbackDir, "data")
	if err := ensureWritableDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mnemo-write-test-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	return os.Remove(name)
}

// UserDataDir is a legacy alias kept for compatibility; returns the user
// config dir. Prefer UserConfigDir + DataDir.
func UserDataDir(appName string) (string, error) {
	return UserConfigDir(appName)
}
