//go:build windows

package app

import (
	"errors"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/energye/systray"
	"golang.org/x/sys/windows"

	"mnemo-go/internal/logging"
)

// AcquireSingleInstance 通过命名互斥体保证单实例运行。已存在实例时通过命名
// 事件通知其唤起主窗口（兜底再用 FindWindow 跨进程置前）并返回 false。
func AcquireSingleInstance(windowTitle string) bool {
	name, err := windows.UTF16PtrFromString(`Local\MnemoGo.SingleInstance`)
	if err != nil {
		return true
	}
	_, err = windows.CreateMutex(nil, false, name)
	switch {
	case err == nil:
		return true
	case errors.Is(err, windows.ERROR_ALREADY_EXISTS):
		logging.Info("another instance detected, signaling existing window")
		if !signalExistingInstance() {
			activateExistingWindow(windowTitle)
		}
		return false
	default:
		logging.Warn("single instance mutex failed", "error", err)
		return true
	}
}

const showEventName = `Local\MnemoGo.ShowRequest`

// WatchShowRequests 第一实例监听「唤起主窗口」信号（二次启动/其他入口触发）。
func (a *App) WatchShowRequests() {
	name, err := windows.UTF16PtrFromString(showEventName)
	if err != nil {
		return
	}
	h, err := windows.CreateEvent(nil, 0, 0, name)
	if err != nil {
		logging.Warn("show-request event creation failed", "error", err)
		return
	}
	go func() {
		for {
			r, err := windows.WaitForSingleObject(h, windows.INFINITE)
			if r != windows.WAIT_OBJECT_0 || err != nil {
				return
			}
			a.ShowMainWindow()
		}
	}()
}

// signalExistingInstance 向已运行实例发送唤起信号；事件不存在（对方尚未
// 完成启动）时返回 false，由调用方走 FindWindow 兜底。
func signalExistingInstance() bool {
	name, err := windows.UTF16PtrFromString(showEventName)
	if err != nil {
		return false
	}
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h) == nil
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW      = user32.NewProc("FindWindowW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procIsIconic         = user32.NewProc("IsIconic")
	procBringWindowToTop = user32.NewProc("BringWindowToTop")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
)

func activateExistingWindow(title string) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		logging.Warn("existing instance window not found", "title", title)
		return
	}
	// SW_RESTORE 只对最小化有效；隐藏（托盘）窗口需要 SW_SHOW
	cmd := uintptr(5) // SW_SHOW
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		cmd = 9 // SW_RESTORE
	}
	procShowWindow.Call(hwnd, cmd)
	procBringWindowToTop.Call(hwnd)
	procSetForeground.Call(hwnd)
}

// SetupTray 启动系统托盘图标：左键点击或菜单「显示」恢复主窗口，
// 「退出」强制退出整个应用。Windows 消息循环必须锁定在固定 OS 线程上，
// 否则 goroutine 迁移后托盘窗口消息泵静默失效。
func TrayAvailable() bool { return true }

func (a *App) SetupTray(icon []byte) {
	go func() {
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(icon)
			systray.SetTooltip("Mnemo")
			systray.SetOnClick(func(systray.IMenu) { a.ShowMainWindow() })
			systray.AddMenuItem("显示 Mnemo", "显示主窗口").Click(func() { a.ShowMainWindow() })
			systray.AddMenuItem("打开预览窗口", "恢复后台音频及其他预览窗口").Click(func() { a.ShowPreviewWindows() })
			progressItem := systray.AddMenuItem("无下载任务", "查看下载状态")
			progressItem.Click(func() { a.ShowMainWindow() })
			go a.watchDownloadIndicator(progressItem)
			systray.AddSeparator()
			systray.AddMenuItem("退出 Mnemo", "完全退出").Click(func() { a.ForceQuit() })
		}, func() {})
	}()
}

// ITaskbarList3 must be created and used on the same COM apartment thread.
type nativeTaskbar struct{ vtable *[21]uintptr }

func (a *App) watchDownloadIndicator(menu *systray.MenuItem) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole := windows.NewLazySystemDLL("ole32.dll")
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	var taskbar *nativeTaskbar
	if int32(hr) >= 0 {
		defer ole.NewProc("CoUninitialize").Call()
		clsid, _ := windows.GUIDFromString("{56FDF344-FD6D-11D0-958A-006097C9A090}")
		iid, _ := windows.GUIDFromString("{EA1AFB91-9E28-4B86-90E9-9E9F8A5EEFAF}")
		hr, _, _ = ole.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&clsid)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&taskbar)))
		if int32(hr) < 0 {
			taskbar = nil
		}
	}
	call := func(index int, args ...uintptr) {
		if taskbar == nil {
			return
		}
		_, _, _ = syscall.SyscallN(taskbar.vtable[index], append([]uintptr{uintptr(unsafe.Pointer(taskbar))}, args...)...)
	}
	call(3)
	defer call(2)
	title := windows.StringToUTF16Ptr("Mnemo")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastLabel := ""
	for range ticker.C {
		ctx, ready := a.wailsContext()
		if !ready {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		indicator := summarizeDownloads(a.ListDownloads())
		if indicator.label != lastLabel {
			systray.SetTooltip(indicator.label)
			menu.SetTitle(indicator.label)
			lastLabel = indicator.label
		}
		hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
		if hwnd == 0 {
			continue
		}
		if indicator.state != 0 && indicator.state != 1 {
			// Windows amd64/arm64 use one native argument per ULONGLONG.
			if unsafe.Sizeof(uintptr(0)) == 8 {
				call(9, hwnd, uintptr(indicator.percent), 100)
			}
		}
		call(10, hwnd, indicator.state)
	}
}
