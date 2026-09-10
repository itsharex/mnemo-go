package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mnemo-go/internal/logging"
	"mnemo-go/internal/store"
	"mnemo-go/internal/sync"
)

type activeSyncRun struct {
	cancel    context.CancelFunc
	startedAt int64
	source    string
}

func (a *App) isSyncRunning(id string) bool {
	a.syncRunMu.Lock()
	defer a.syncRunMu.Unlock()
	_, running := a.syncRuns[id]
	return running
}

func (a *App) beginSyncRun(parent context.Context, jobID, source string) (context.Context, func(error), error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, nil, errors.New("同步任务 ID 为空")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	run := &activeSyncRun{cancel: cancel, startedAt: time.Now().Unix(), source: source}

	a.syncRunMu.Lock()
	if a.syncRuns == nil {
		a.syncRuns = make(map[string]*activeSyncRun)
	}
	if _, exists := a.syncRuns[jobID]; exists {
		a.syncRunMu.Unlock()
		cancel()
		return nil, nil, fmt.Errorf("同步任务正在运行: %s", jobID)
	}
	a.syncRuns[jobID] = run
	a.syncRunMu.Unlock()
	a.emit("sync:state", map[string]any{"id": jobID, "running": true, "source": source, "startedAt": run.startedAt})

	finish := func(runErr error) {
		cancel()
		a.syncRunMu.Lock()
		if current := a.syncRuns[jobID]; current == run {
			delete(a.syncRuns, jobID)
		}
		a.syncRunMu.Unlock()
		status := "completed"
		if errors.Is(runErr, context.Canceled) {
			status = "canceled"
		} else if runErr != nil {
			status = "failed"
		}
		a.emit("sync:state", map[string]any{"id": jobID, "running": false, "status": status})
	}
	return ctx, finish, nil
}

func (a *App) executeSync(parent context.Context, cfg sync.Config, engine *sync.Engine, source string) (runErr error) {
	ctx, finish, err := a.beginSyncRun(parent, cfg.ID, source)
	if err != nil {
		return err
	}
	defer func() { finish(runErr) }()
	return engine.Run(ctx, cfg)
}

func (a *App) newSyncEngine(st sync.SnapshotStore, progress bool) *sync.Engine {
	var onProgress func(jobID string, done, total int)
	if progress {
		onProgress = func(jobID string, done, total int) {
			a.emit("sync:progress", map[string]any{"id": jobID, "done": done, "total": total})
		}
	}
	return sync.NewEngine(onProgress,
		sync.WithSnapshotStore(st),
		sync.WithLogger(func(jobID, event, detail string) {
			a.handleSyncLog(jobID, event, detail)
		}),
	)
}

// ListRunningSyncIDs returns both manually and scheduler-started jobs so a
// newly mounted frontend can reconstruct the current running state.
func (a *App) ListRunningSyncIDs() []string {
	a.syncRunMu.Lock()
	defer a.syncRunMu.Unlock()
	ids := make([]string, 0, len(a.syncRuns))
	for id := range a.syncRuns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// CancelSync cancels an active manual or scheduled sync job. The registry is
// cleared only after the worker exits, preventing an immediate overlapping run.
func (a *App) CancelSync(id string) bool {
	a.syncRunMu.Lock()
	run := a.syncRuns[id]
	a.syncRunMu.Unlock()
	if run == nil {
		return false
	}
	logging.Info("sync cancellation requested", "job_id", id, "source", run.source)
	run.cancel()
	return true
}

func (a *App) cancelAllSyncRuns() {
	a.syncRunMu.Lock()
	runs := make([]*activeSyncRun, 0, len(a.syncRuns))
	for _, run := range a.syncRuns {
		runs = append(runs, run)
	}
	a.syncRunMu.Unlock()
	for _, run := range runs {
		run.cancel()
	}
}

// ListSyncConfigs lists persisted sync jobs.
func (a *App) ListSyncConfigs() []sync.Config {
	st, err := a.storeOrError()
	if err != nil {
		logging.Warn("sync configurations unavailable", "error", err)
		return nil
	}
	list, err := st.ListSyncConfigs()
	if err != nil {
		logging.Warn("sync configurations load failed", "error", err)
		return nil
	}
	return list
}

// SaveSyncConfig persists a sync job.
func (a *App) SaveSyncConfig(cfg sync.Config) error {
	if a.isSyncRunning(cfg.ID) {
		return fmt.Errorf("同步任务正在运行，请先停止: %s", cfg.ID)
	}
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	if err := st.SaveSyncConfig(cfg); err != nil {
		logging.Error("sync configuration save failed", "job_id", cfg.ID, "error", err)
		return err
	}
	logging.Info("sync configuration saved", "job_id", cfg.ID, "direction", cfg.Direction, "enabled", cfg.Enabled)
	return nil
}

// DeleteSyncConfig removes a sync job.
func (a *App) DeleteSyncConfig(id string) error {
	if a.isSyncRunning(id) {
		return fmt.Errorf("同步任务正在运行，请先停止: %s", id)
	}
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	if err := st.DeleteSyncConfig(id); err != nil {
		logging.Error("sync configuration removal failed", "job_id", id, "error", err)
		return err
	}
	logging.Info("sync configuration removed", "job_id", id)
	return nil
}

// RunSync executes a sync job synchronously.
func (a *App) RunSync(id string) error {
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	cfg, err := st.GetSyncConfig(id)
	if err != nil {
		logging.Warn("sync run rejected", "job_id", id, "error", err)
		return err
	}
	eng := a.newSyncEngine(st, true)
	return a.executeSync(a.appContext(), cfg, eng, "manual")
}

// StartSyncScheduler launches the background scheduler for all enabled,
// interval-based sync jobs. The scheduler runs for the lifetime of the app
// (stopped by OnShutdown); the method returns an empty string on success
// so it is serializable through the Wails binding.
func (a *App) StartSyncScheduler() string {
	st, err := a.storeOrError()
	if err != nil {
		logging.Error("sync scheduler startup failed", "error", err)
		return fmt.Sprint(err)
	}
	stop := make(chan struct{})
	eng := a.newSyncEngine(st, false)
	eng.StartScheduler(stop, st.ListSyncConfigs, func(ctx context.Context, cfg sync.Config) error {
		return a.executeSync(ctx, cfg, eng, "scheduler")
	})
	// keep the stop channel for Shutdown; a.schedStop is closed on shutdown
	a.stateMu.Lock()
	previous := a.schedStop
	if previous != nil {
		close(previous)
	}
	a.schedStop = stop
	a.stateMu.Unlock()
	return ""
}

func (a *App) handleSyncLog(jobID, event, detail string) {
	a.emit("sync:log", map[string]any{"id": jobID, "event": event, "detail": detail})
	args := []any{"job_id", jobID, "event", event}
	if detail != "" {
		args = append(args, "detail", detail)
	}
	switch event {
	case "error", "scheduler_error":
		logging.Error("sync failed", args...)
	case "delete_error":
		logging.Error("sync deletion failed", args...)
	case "delete_cancelled":
		logging.Warn("sync deletion paused", args...)
	case "start":
		logging.Info("sync started", args...)
	case "complete":
		logging.Info("sync completed", args...)
	case "delete":
		logging.Info("sync deletion applied", args...)
	default:
		logging.Info("sync event", args...)
	}
}

var _ = store.Settings{}

func (a *App) PreviewSync(id string) (*sync.Plan, error) {
	st, err := a.storeOrError()
	if err != nil {
		return nil, err
	}
	if a.isSyncRunning(id) {
		return nil, fmt.Errorf("同步正在运行，请先停止")
	}
	cfg, err := st.GetSyncConfig(id)
	if err != nil {
		return nil, err
	}
	return a.newSyncEngine(st, false).Preview(a.appContext(), cfg)
}

func (a *App) RunSyncPlan(id, token string, choices map[string]string) (runErr error) {
	if token == "" {
		return fmt.Errorf("请先预览同步计划")
	}
	st, err := a.storeOrError()
	if err != nil {
		return err
	}
	cfg, err := st.GetSyncConfig(id)
	if err != nil {
		return err
	}
	ctx, finish, err := a.beginSyncRun(a.appContext(), id, "preview")
	if err != nil {
		return err
	}
	defer func() { finish(runErr) }()
	return a.newSyncEngine(st, true).ExecutePlan(ctx, cfg, token, choices)
}
