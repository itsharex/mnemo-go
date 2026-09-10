package app

import (
	"context"
	"fmt"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"mnemo-go/internal/logging"
	"mnemo-go/internal/model"
)

type downloadIndicator struct {
	state   uintptr
	percent uint64
	label   string
}

func summarizeDownloads(tasks []model.DownloadTask) downloadIndicator {
	active, paused, failed := 0, 0, 0
	var done, total float64
	unknown := false
	for _, task := range tasks {
		switch task.Status {
		case "downloading", "queued":
			active++
		case "paused":
			paused++
		case "failed":
			failed++
		default:
			continue
		}
		if task.Status == "failed" {
			continue
		}
		if task.Size <= 0 {
			unknown = true
			continue
		}
		total += float64(task.Size)
		done += float64(max(0, min(task.Downloaded, task.Size)))
	}
	progress := downloadIndicator{label: "Mnemo · 无下载任务"}
	if total > 0 {
		progress.percent = uint64(done * 100 / total)
	}
	if active > 0 {
		progress.state = 2
		progress.label = fmt.Sprintf("Mnemo · 下载 %d%% · %d 个任务", progress.percent, active)
		if unknown {
			progress.state = 1
			progress.label = fmt.Sprintf("Mnemo · 正在下载 · %d 个任务", active)
		}
	} else if paused > 0 {
		progress.state = 8
		progress.label = fmt.Sprintf("Mnemo · 已暂停 %d%% · %d 个任务", progress.percent, paused)
	} else if failed > 0 {
		progress.state, progress.percent = 4, 100
		progress.label = fmt.Sprintf("Mnemo · %d 个下载失败", failed)
	}
	return progress
}

// ShowMainWindow 从托盘/隐藏状态恢复主窗口并置于前台。
func (a *App) ShowMainWindow() {
	ctx, ok := a.wailsContext()
	if !ok {
		// 唤起信号可能先于 OnStartup 到达（二次启动竞态），稍候重试
		for i := 0; i < 20 && !ok; i++ {
			time.Sleep(300 * time.Millisecond)
			ctx, ok = a.wailsContext()
		}
		if !ok {
			return
		}
	}
	logging.Debug("main window show requested")
	runtime.WindowUnminimise(ctx)
	runtime.WindowShow(ctx)
	// Windows 上前台焦点需要 always-on-top 抖动
	runtime.WindowSetAlwaysOnTop(ctx, true)
	runtime.WindowSetAlwaysOnTop(ctx, false)
}

// ForceQuit 绕过「关闭不退出」，真正退出应用（托盘菜单「退出」使用）。
func (a *App) ForceQuit() {
	a.forceQuit.Store(true)
	logging.Info("force quit requested", "source", "tray")
	if ctx, ok := a.wailsContext(); ok {
		runtime.Quit(ctx)
	}
}

// BeforeClose 实现 Wails OnBeforeClose：启用「关闭不退出」（默认）时隐藏
// 窗口到托盘而非退出；真正退出前若仍有下载任务，弹窗请用户确认。
// 返回 true 表示阻止关闭。
func (a *App) BeforeClose(ctx context.Context) bool {
	realQuit := a.forceQuit.Load()
	if TrayAvailable() && !realQuit {
		if st, err := a.storeOrError(); err == nil {
			if s, serr := st.GetSettings(); serr == nil && !s.CloseToTrayEnabled() {
				realQuit = true
			}
		}
	}
	if !realQuit {
		logging.Info("window close intercepted, hiding to tray")
		runtime.WindowHide(ctx)
		return true
	}
	if a.confirmQuitWithActiveDownloads(ctx) {
		return true // 用户在确认弹窗中取消
	}
	return false
}

// confirmQuitWithActiveDownloads 有进行中的下载任务时弹窗确认；
// 返回 true 表示用户取消退出。
func (a *App) confirmQuitWithActiveDownloads(ctx context.Context) bool {
	dl := a.downloadManager()
	if dl == nil {
		return false
	}
	active := 0
	for _, t := range dl.List() {
		if t.Status == "queued" || t.Status == "downloading" {
			active++
		}
	}
	if active == 0 {
		return false
	}
	logging.Info("quit blocked for confirmation", "active_downloads", active)
	// 窗口可能处于托盘隐藏状态，先唤回再弹窗
	runtime.WindowUnminimise(ctx)
	runtime.WindowShow(ctx)
	choice, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         "退出 Mnemo",
		Message:       fmt.Sprintf("还有 %d 个下载任务正在进行，退出后任务将暂停，下次启动可继续。\n确定要退出吗？", active),
		Buttons:       []string{"退出", "取消"},
		DefaultButton: "取消",
		CancelButton:  "取消",
	})
	if err != nil {
		logging.Warn("quit confirmation dialog failed", "error", err)
		return false // 弹窗失败不阻塞退出
	}
	if choice != "退出" {
		logging.Info("quit cancelled by user")
		a.forceQuit.Store(false) // 托盘触发的退出被取消时复位标记
		return true
	}
	return false
}
