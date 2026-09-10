package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	"mnemo-go/internal/config"
	"mnemo-go/internal/updater"
)

type CheckUpdateResult struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	Version        string `json:"version"`
	URL            string `json:"url"`
	Size           int64  `json:"size"`
	Notes          string `json:"notes"`
	ReleaseURL     string `json:"releaseUrl"`
	CanInstall     bool   `json:"canInstall"`
}

type UpdateStatus struct {
	Revision   uint64             `json:"revision"`
	Phase      string             `json:"phase"`
	Info       *CheckUpdateResult `json:"info"`
	Downloaded int64              `json:"downloaded"`
	Total      int64              `json:"total"`
	Path       string             `json:"path"`
	Error      string             `json:"error"`
}

type updateDownloadRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	info   *updater.Info
	path   string
}
type verifiedUpdate struct {
	info *updater.Info
	path string
}

func cloneUpdateInfo(info *updater.Info) *updater.Info {
	if info == nil {
		return nil
	}
	copy := *info
	return &copy
}

func updateResult(info *updater.Info) *CheckUpdateResult {
	result := &CheckUpdateResult{CurrentVersion: config.AppVersion, ReleaseURL: updater.ReleasePage, CanInstall: goruntime.GOOS == "windows"}
	if info != nil {
		result.Available = true
		result.Version, result.URL, result.Size, result.Notes, result.ReleaseURL, result.CanInstall =
			info.Version, info.URL, info.Size, info.Notes, info.ReleaseURL, info.CanInstall
	}
	return result
}

// CheckUpdate serializes checks so an older response cannot replace newer metadata.
func (a *App) CheckUpdate() (*CheckUpdateResult, error) {
	a.updateCheckMu.Lock()
	defer a.updateCheckMu.Unlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 45*time.Second)
	defer cancel()
	info, err := updater.Check(ctx)
	if err != nil {
		return updateResult(nil), err
	}
	a.updateMu.Lock()
	a.updateInfo = cloneUpdateInfo(info)
	if a.updateRun == nil && a.updateReady == nil && !a.updateApplying {
		a.updateState = UpdateStatus{Revision: a.updateState.Revision + 1, Phase: "idle", Info: updateResult(info)}
		if info != nil {
			a.updateState.Phase = "available"
		}
	}
	a.updateMu.Unlock()
	return updateResult(info), nil
}

// GetUpdateStatus reconstructs the dialog after it was closed during a download.
func (a *App) GetUpdateStatus() UpdateStatus {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.updateStatusLocked()
}

func (a *App) updateStatusLocked() UpdateStatus {
	status := a.updateState
	if status.Phase == "" {
		status.Phase = "idle"
	}
	if status.Info == nil {
		status.Info = updateResult(a.updateInfo)
	} else {
		copy := *status.Info
		status.Info = &copy
	}
	return status
}

func (a *App) publishUpdate(run *updateDownloadRun, change func(*UpdateStatus)) {
	a.updateMu.Lock()
	if run != nil && a.updateRun != run {
		a.updateMu.Unlock()
		return
	}
	change(&a.updateState)
	a.updateState.Revision++
	status := a.updateStatusLocked()
	a.updateMu.Unlock()
	a.emit("update:state", status)
}

func (a *App) DownloadUpdate(downloadURL string) (string, error) {
	a.updateMu.Lock()
	if a.updateApplying {
		a.updateMu.Unlock()
		return "", fmt.Errorf("正在安装更新")
	}
	if run := a.updateRun; run != nil {
		a.updateMu.Unlock()
		if downloadURL == "" || downloadURL == run.info.URL {
			return run.path, nil
		}
		return "", fmt.Errorf("已有更新正在下载")
	}
	info := cloneUpdateInfo(a.updateInfo)
	if ready := a.updateReady; ready != nil && a.updateState.Phase == "done" && (downloadURL == "" || downloadURL == ready.info.URL) {
		a.updateMu.Unlock()
		return ready.path, nil
	}
	a.updateMu.Unlock()
	if info == nil || downloadURL != "" && downloadURL != info.URL {
		if _, err := a.CheckUpdate(); err != nil {
			return "", err
		}
		a.updateMu.Lock()
		info = cloneUpdateInfo(a.updateInfo)
		a.updateMu.Unlock()
	}
	if info == nil || info.URL == "" {
		return "", fmt.Errorf("当前没有可用更新")
	}
	if downloadURL != "" && downloadURL != info.URL {
		return "", fmt.Errorf("更新地址与已检查的版本不一致")
	}
	root := updater.DownloadDir(a.dataDirectory())
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	// Every run gets its own directory, so cancellation or a second release
	// cannot overwrite another verified package.
	runDir, err := os.MkdirTemp(root, "package-")
	if err != nil {
		return "", err
	}
	if info.Name == "" || filepath.Base(info.Name) != info.Name {
		os.Remove(runDir)
		return "", fmt.Errorf("更新包名称无效")
	}
	path := filepath.Join(runDir, info.Name)
	ctx, cancel := context.WithCancel(a.appContext())
	run := &updateDownloadRun{cancel: cancel, done: make(chan struct{}), info: info, path: path}
	a.updateMu.Lock()
	if a.updateRun != nil || a.updateApplying {
		a.updateMu.Unlock()
		cancel()
		os.Remove(runDir)
		return "", fmt.Errorf("已有更新正在处理")
	}
	a.updateReady = nil
	a.updateRun = run
	a.updateState = UpdateStatus{Revision: a.updateState.Revision + 1, Phase: "downloading", Info: updateResult(info), Total: info.Size}
	status := a.updateStatusLocked()
	a.updateMu.Unlock()
	a.emit("update:state", status)
	go a.downloadUpdate(ctx, run)
	return path, nil
}

func (a *App) downloadUpdate(ctx context.Context, run *updateDownloadRun) {
	defer close(run.done)
	defer run.cancel()
	_, err := updater.Download(ctx, run.info.URL, run.path, func(p updater.Progress) {
		a.publishUpdate(run, func(s *UpdateStatus) {
			s.Downloaded = p.Downloaded
			if p.Total > 0 {
				s.Total = p.Total
			}
		})
	})
	if err == nil {
		a.publishUpdate(run, func(s *UpdateStatus) { s.Phase = "verifying" })
		if stat, statErr := os.Stat(run.path); statErr != nil {
			err = statErr
		} else if stat.Size() != run.info.Size {
			err = fmt.Errorf("更新包大小与发布记录不一致")
		}
		if err == nil {
			var ok bool
			ok, err = updater.VerifyChecksum(run.path, run.info.SHA256)
			if err == nil && !ok {
				err = fmt.Errorf("更新包 SHA-256 校验失败，请重新下载")
			}
		}
	}
	a.updateMu.Lock()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if a.updateRun != run {
		a.updateMu.Unlock()
		return
	}
	a.updateRun = nil
	a.updateState.Revision++
	if err != nil {
		a.updateState.Phase = "error"
		a.updateState.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			a.updateState.Phase = "canceled"
			a.updateState.Error = ""
		}
		os.Remove(run.path)
		os.Remove(filepath.Dir(run.path))
	} else {
		a.updateReady = &verifiedUpdate{info: cloneUpdateInfo(run.info), path: run.path}
		a.updateState.Phase = "done"
		a.updateState.Path = run.path
		a.updateState.Downloaded, a.updateState.Total = run.info.Size, run.info.Size
	}
	status := a.updateStatusLocked()
	a.updateMu.Unlock()
	a.emit("update:state", status)
}

func (a *App) CancelUpdate() bool {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if a.updateRun == nil {
		return false
	}
	a.updateRun.cancel()
	return true
}

// ApplyUpdate accepts only the current process's verified package and verifies
// it again immediately before the platform handoff.
func (a *App) ApplyUpdate(path string) (applyErr error) {
	a.updateMu.Lock()
	ready := a.updateReady
	if a.updateApplying || a.updateRun != nil {
		a.updateMu.Unlock()
		return fmt.Errorf("更新正在处理中")
	}
	if ready == nil || path != ready.path || !updater.IsDownloadPath(a.dataDirectory(), path) {
		a.updateMu.Unlock()
		return fmt.Errorf("更新包未经验证，请重新下载")
	}
	if !ready.info.CanInstall {
		a.updateMu.Unlock()
		return fmt.Errorf("请打开下载目录，手动安装此平台的更新包")
	}
	a.updateApplying = true
	a.updateMu.Unlock()
	packageValid := false
	defer func() {
		if applyErr != nil {
			a.updateMu.Lock()
			a.updateApplying = false
			if !packageValid {
				a.updateReady = nil
				a.updateState.Path = ""
			}
			a.updateState.Phase = "error"
			a.updateState.Error = applyErr.Error()
			a.updateState.Revision++
			status := a.updateStatusLocked()
			a.updateMu.Unlock()
			a.emit("update:state", status)
		}
	}()
	stat, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() != ready.info.Size {
		return fmt.Errorf("更新包已变化，请重新下载")
	}
	ok, err := updater.VerifyChecksum(path, ready.info.SHA256)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("更新包校验失败，请重新下载")
	}
	packageValid = true
	if err := updater.Apply(path); err != nil {
		return err
	}
	a.publishUpdate(nil, func(s *UpdateStatus) { s.Phase = "applying"; s.Error = "" })
	a.ForceQuit()
	return nil
}
