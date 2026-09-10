package updater

import (
	"strings"

	"golang.org/x/sys/windows"
)

func init() {
	launchWindowsInstaller = func(path string, args []string) error {
		file, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = windows.EscapeArg(arg)
		}
		parameters, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
		if err != nil {
			return err
		}
		// ShellExecute requests UAC elevation for the machine-wide installer;
		// cancellation is returned to the dialog and leaves Mnemo running.
		return windows.ShellExecute(0, windows.StringToUTF16Ptr("runas"), file, parameters, nil, windows.SW_SHOWNORMAL)
	}
}
