package updater

import (
	"fmt"
	"os"
	"path/filepath"
)

// applyWindows runs the Inno Setup installer silently and lets it restart
// the application.
var launchWindowsInstaller = func(path string, args []string) error {
	return fmt.Errorf("Windows installer is unavailable on this platform")
}

func applyWindows(installerPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return launchWindowsInstaller(installerPath, installerArguments(filepath.Dir(executable)))
}

func installerArguments(installDir string) []string {
	return []string{"/SILENT", "/NORESTART", "/CLOSEAPPLICATIONS", "/NORESTARTAPPLICATIONS", "/MNEMORESTART=1", "/DIR=" + installDir}
}

// applyUnix reports the manual installation requirement without modifying files.
func applyUnix(archivePath string) error {
	// Replacing a running Wails binary/app bundle safely requires a platform
	// specific handoff process. Never report success while leaving the archive
	// untouched; the caller can surface this actionable error instead.
	return fmt.Errorf("automatic update installation is not supported on this platform; downloaded archive: %s", archivePath)
}
