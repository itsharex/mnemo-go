package config

import "golang.org/x/sys/windows"

func init() {
	systemDownloadDir = func() (string, error) {
		// DONT_VERIFY retrieves the redirected path even before the directory
		// exists or when its volume is offline. Do not request DEFAULT_PATH.
		return windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DONT_VERIFY)
	}
}
