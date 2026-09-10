// Package transfer manages download/upload tasks: the segmented download
// manager, the upload queue and cross-drive migration.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/logging"
	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
	"mnemo-go/internal/transfer/dlengine"
)

// OnTaskEvent is called on every task state/progress change.
type OnTaskEvent func(ev TaskEvent)

// TaskEvent is pushed to the frontend via events.
type TaskEvent struct {
	Kind string             `json:"kind"` // download | upload | offline
	Task model.DownloadTask `json:"task"`
}

// Progress remains live in the UI, but task files only need a crash-recovery
// checkpoint every few seconds. Writing the whole JSON task list for every
// network tick creates unnecessary disk I/O and short-lived allocations.
const transferProgressPersistInterval = 5 * time.Second

type progressPersistState struct {
	status string
	at     time.Time
}

// Manager owns the active download queue.
type Manager struct {
	store              *store.Store
	mu                 sync.Mutex
	targetMu           sync.Mutex
	persistMu          sync.Mutex
	tasks              map[string]*model.DownloadTask
	cancels            map[string]context.CancelFunc
	removed            map[string]bool
	lastPersist        map[string]progressPersistState
	onEvent            OnTaskEvent
	dir                string
	stop               chan struct{}
	ctx                context.Context // root context for all downloads, canceled on Shutdown
	cancel             context.CancelFunc
	maxConcurrent      int
	activeConcurrent   int
	concurrencyChanged chan struct{}
	speedLimiter       *dlengine.SharedLimiter
	shutdownOnce       sync.Once
}

// NewManager creates a download manager.
func NewManager(st *store.Store, downloadDir string, onEvent OnTaskEvent) (*Manager, error) {
	if downloadDir == "" {
		home, _ := os.UserHomeDir()
		downloadDir = filepath.Join(home, "Downloads")
	}
	_ = os.MkdirAll(downloadDir, 0o755)
	maxConc := 3
	keepTasks := true
	settings, settingsErr := st.GetSettings()
	if settingsErr == nil {
		if settings.MaxConcurrentDownloads > 0 {
			maxConc = settings.MaxConcurrentDownloads
		}
		keepTasks = settings.KeepTasks
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	initialRate := int64(0)
	if settingsErr == nil {
		initialRate = settings.MaxDownloadSpeed
	}
	m := &Manager{
		store:              st,
		tasks:              map[string]*model.DownloadTask{},
		cancels:            map[string]context.CancelFunc{},
		removed:            map[string]bool{},
		lastPersist:        map[string]progressPersistState{},
		onEvent:            onEvent,
		dir:                downloadDir,
		stop:               make(chan struct{}),
		ctx:                rootCtx,
		maxConcurrent:      maxConc,
		concurrencyChanged: make(chan struct{}),
		cancel:             rootCancel,
		speedLimiter:       dlengine.NewSharedLimiter(initialRate),
	}
	if !keepTasks {
		if err := st.ClearDownloadTasks(); err != nil {
			logging.Warn("download task history cleanup failed", "error", err)
		}
	}
	// restore persisted tasks
	m.loadPersisted()
	return m, nil
}

// SetEventSink wires the event callback (used by the app layer).
func (m *Manager) SetEventSink(fn OnTaskEvent) {
	m.mu.Lock()
	m.onEvent = fn
	m.mu.Unlock()
}

// SetDir updates the download directory at runtime. Subsequent downloads use
// the new directory; existing tasks keep their already-assigned LocalPath.
func (m *Manager) SetDir(dir string) {
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	m.mu.Lock()
	m.dir = dir
	m.mu.Unlock()
}

// SetConcurrency changes the queue limit without replacing a semaphore under
// active work. Existing downloads keep their slot and queued work is woken to
// re-check the new limit, so settings updates cannot temporarily double the
// number of active transfers.
func (m *Manager) SetConcurrency(n int) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxConcurrent = n
	m.notifyConcurrencyLocked()
}

func (m *Manager) acquireSlot(ctx context.Context) error {
	for {
		m.mu.Lock()
		if m.activeConcurrent < m.maxConcurrent {
			m.activeConcurrent++
			m.mu.Unlock()
			return nil
		}
		changed := m.concurrencyChanged
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (m *Manager) releaseSlot() {
	m.mu.Lock()
	if m.activeConcurrent > 0 {
		m.activeConcurrent--
	}
	m.notifyConcurrencyLocked()
	m.mu.Unlock()
}

func (m *Manager) notifyConcurrencyLocked() {
	close(m.concurrencyChanged)
	m.concurrencyChanged = make(chan struct{})
}

func (m *Manager) loadPersisted() {
	list, err := m.store.ListDownloadTasks()
	if err != nil {
		logging.Warn("download task restore failed", "error", err)
		return
	}
	for i := range list {
		t := list[i]
		safeTask := safeDownloadTask(t)
		dirty := t.URL != "" || len(t.Headers) > 0 || t.Error != safeTask.Error
		t = safeTask
		if t.Status == "downloading" || t.Status == "queued" {
			t.Status = "paused" // interrupted on exit
			dirty = true
		}
		if t.UserID == "" && t.Status != "completed" && t.Status != "canceled" {
			t.Status = "failed"
			t.Error = "直链任务的地址和请求头不会落盘；应用重启后请重新添加任务"
			dirty = true
		}
		m.tasks[t.ID] = &t
		if dirty {
			if err := m.store.SaveDownloadTask(&t); err != nil {
				logging.Warn("download task security migration failed", "task_id", t.ID, "error", err)
			}
		}
	}
}

// List returns all tasks (active + persisted).
func (m *Manager) List() []model.DownloadTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.DownloadTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, safeDownloadTask(*t))
	}
	return out
}

func (m *Manager) get(id string) (*model.DownloadTask, bool) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	return t, ok
}

func (m *Manager) update(t *model.DownloadTask) {
	if t == nil {
		return
	}
	m.mu.Lock()
	if m.removed[t.ID] {
		m.mu.Unlock()
		return
	}
	m.tasks[t.ID] = t
	snapshot := *t
	shouldPersist := m.shouldPersistLocked(snapshot)
	event := m.onEvent
	m.mu.Unlock()
	if shouldPersist {
		// Store methods read-modify-write one JSON file. Serialize the whole
		// operation so concurrent task progress cannot lose another task's update.
		m.persistMu.Lock()
		persisted := safeDownloadTask(snapshot)
		if err := m.store.SaveDownloadTask(&persisted); err != nil {
			logging.Warn("download task persistence failed", "task_id", snapshot.ID, "status", snapshot.Status, "error", err)
		}
		m.persistMu.Unlock()
	}
	if event != nil {
		event(TaskEvent{Kind: "download", Task: safeDownloadTask(snapshot)})
	}
}

func safeDownloadTask(task model.DownloadTask) model.DownloadTask {
	task.URL = ""
	task.Headers = nil
	task.Error = logging.RedactText(task.Error)
	return task
}

// addDownloadTask atomically assigns a collision-free final path and publishes
// the task. Serializing assignment through targetMu prevents two concurrent
// enqueue calls from selecting the same .part/.state/final file set.
func (m *Manager) addDownloadTask(t *model.DownloadTask, requestedName string) {
	m.targetMu.Lock()
	m.mu.Lock()
	t.LocalPath = m.nextDownloadPathLocked(safeName(requestedName))
	m.mu.Unlock()
	m.update(t)
	m.targetMu.Unlock()
}

// nextDownloadPathLocked must be called with m.mu held.
func (m *Manager) nextDownloadPathLocked(fileName string) string {
	for index := 0; ; index++ {
		candidateName := fileName
		if index > 0 {
			candidateName = numberedDownloadName(fileName, index)
		}
		candidate := filepath.Join(m.dir, candidateName)
		if !m.downloadPathOccupiedLocked(candidate) {
			return candidate
		}
	}
}

func (m *Manager) downloadPathOccupiedLocked(candidate string) bool {
	for _, task := range m.tasks {
		if task != nil && sameDownloadPath(task.LocalPath, candidate) {
			return true
		}
	}
	for _, path := range []string{candidate, candidate + ".part", candidate + ".state.json"} {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func sameDownloadPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func numberedDownloadName(fileName string, index int) string {
	ext := filepath.Ext(fileName)
	stem := strings.TrimSuffix(fileName, ext)
	if stem == "" {
		stem = fileName
		ext = ""
	}
	return fmt.Sprintf("%s (%d)%s", stem, index, ext)
}

func (m *Manager) shouldPersistLocked(task model.DownloadTask) bool {
	now := time.Now()
	previous, ok := m.lastPersist[task.ID]
	terminal := task.Status == "completed" || task.Status == "failed" || task.Status == "canceled" || task.Status == "paused"
	if !ok || previous.status != task.Status || terminal || now.Sub(previous.at) >= transferProgressPersistInterval {
		m.lastPersist[task.ID] = progressPersistState{status: task.Status, at: now}
		return true
	}
	return false
}

// AddDownload enqueues a download from a drive file.
func (m *Manager) AddDownload(userID, driveID string, f model.File) (*model.DownloadTask, error) {
	provider := drive.ProviderOf(userID, driveID, "")
	// Keep the exact list-row snapshot available until the provider resolves
	// its authenticated URL. Some providers need metadata that is not present
	// in a later detail response.
	drive.RememberFile(userID, driveID, f)
	t := &model.DownloadTask{
		ID:       newID("dl"),
		UserID:   userID,
		DriveID:  driveID,
		Provider: provider,
		FileID:   f.FileID,
		Name:     f.Name,
		Size:     f.Size,
		Status:   "queued",
		Created:  time.Now().Unix(),
		Updated:  time.Now().Unix(),
	}
	m.addDownloadTask(t, f.Name)
	go m.runDownload(t)
	result := safeDownloadTask(*t)
	return &result, nil
}

// AddDownloadURL enqueues a download from a direct URL.
func (m *Manager) AddDownloadURL(name, url string, headers map[string]string) (*model.DownloadTask, error) {
	storedHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			storedHeaders[key] = value
		}
	}
	t := &model.DownloadTask{
		ID:      newID("dl"),
		Name:    name,
		URL:     url,
		Status:  "queued",
		Headers: storedHeaders,
		Created: time.Now().Unix(),
		Updated: time.Now().Unix(),
	}
	m.addDownloadTask(t, name)
	go m.runDownload(t)
	result := safeDownloadTask(*t)
	return &result, nil
}

func (m *Manager) runDownload(t *model.DownloadTask) {
	started := time.Now()
	logging.Info("download worker started", "task_id", t.ID, "name", t.Name)
	// wait for a concurrency slot before doing any work
	if err := m.acquireSlot(m.ctx); err != nil {
		// app shutting down before the task could start
		m.mu.Lock()
		shouldPause := !m.removed[t.ID] && m.tasks[t.ID] == t
		if shouldPause {
			t.Status = "paused"
			t.Updated = time.Now().Unix()
		}
		m.mu.Unlock()
		if shouldPause {
			m.update(t)
		}
		logging.Debug("download worker paused before start", "task_id", t.ID, "reason", err)
		return
	}
	defer m.releaseSlot()

	m.mu.Lock()
	if m.removed[t.ID] || m.tasks[t.ID] != t || t.Status != "queued" || m.ctx.Err() != nil {
		m.mu.Unlock()
		logging.Debug("download worker skipped", "task_id", t.ID, "reason", "task no longer queued")
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	m.cancels[t.ID] = cancel
	t.Status = "downloading"
	t.Updated = time.Now().Unix()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, t.ID)
		removed := m.removed[t.ID]
		cleanup := removed || t.Status == "canceled"
		if removed {
			delete(m.removed, t.ID)
		}
		m.mu.Unlock()
		if cleanup {
			removeDownloadTemp(t)
		}
	}()

	m.update(t)

	url := t.URL
	headers := t.Headers
	var requestAuth model.RequestAuthenticator
	s, settingsErr := m.store.GetSettings()
	if settingsErr != nil {
		m.mu.Lock()
		if !m.removed[t.ID] && t.Status == "downloading" {
			t.Status = "failed"
			t.Error = "读取下载设置失败: " + settingsErr.Error()
			t.Updated = time.Now().Unix()
		}
		m.mu.Unlock()
		m.update(t)
		return
	}
	opts := dlengine.Options{Concurrency: concurrencyFromSettings(s)}
	if s.MaxDownloadSpeed > 0 {
		opts.MaxSpeed = 0
		m.speedLimiter.SetRate(s.MaxDownloadSpeed)
	} else {
		m.speedLimiter.SetRate(0)
	}
	opts.Limiter = m.speedLimiter
	if url == "" && t.UserID != "" {
		u, err := drive.GetDownloadURL(t.UserID, t.DriveID, t.FileID, 14400)
		if err != nil {
			m.mu.Lock()
			if !m.removed[t.ID] && t.Status != "canceled" && t.Status != "paused" {
				t.Status = "failed"
				t.Error = err.Error()
			}
			t.Updated = time.Now().Unix()
			m.mu.Unlock()
			m.update(t)
			logging.Warn("download URL resolution failed", "task_id", t.ID, "error", err)
			return
		}
		if u == nil {
			m.mu.Lock()
			if !m.removed[t.ID] && t.Status != "canceled" && t.Status != "paused" {
				t.Status = "failed"
				t.Error = "下载地址为空"
			}
			t.Updated = time.Now().Unix()
			m.mu.Unlock()
			m.update(t)
			logging.Warn("download URL resolution returned empty result", "task_id", t.ID)
			return
		}
		url = u.URL
		headers = u.Headers
		requestAuth = u.RequestAuth
		m.mu.Lock()
		if t.Size == 0 {
			t.Size = u.Size
		}
		m.mu.Unlock()
		if u.ForceLocalProxy || u.DownloadMode == "proxy" {
			opts.Concurrency = 1
			if u.Concurrency > 1 {
				opts.Concurrency = u.Concurrency
			}
		}
	}
	if url == "" {
		m.mu.Lock()
		if !m.removed[t.ID] && t.Status != "canceled" && t.Status != "paused" {
			t.Status = "failed"
			t.Error = "无法获取下载地址"
		}
		t.Updated = time.Now().Unix()
		m.mu.Unlock()
		m.update(t)
		logging.Warn("download URL missing", "task_id", t.ID)
		return
	}

	m.mu.Lock()
	if t.Status == "canceled" || t.Status == "paused" || m.removed[t.ID] {
		m.mu.Unlock()
		return
	}
	t.URL = url
	t.Updated = time.Now().Unix()
	opts.ExpectedSize = t.Size
	m.mu.Unlock()
	opts.Headers = headers
	opts.RequestAuth = requestAuth
	m.update(t)
	logging.Info("download started", "task_id", t.ID)

	progress := func(p dlengine.Progress) {
		m.mu.Lock()
		t.Downloaded = p.Downloaded
		if p.Total > 0 {
			t.Size = p.Total
		}
		t.Speed = p.Speed
		t.Progress = p.Percent
		t.Updated = time.Now().Unix()
		m.mu.Unlock()
		m.update(t)
	}
	err := dlengine.Download(ctx, opts, url, t.LocalPath, progress)
	// Signed URLs from provider APIs can expire while a long download is
	// running. Re-resolve once for account-backed tasks and reuse the .part
	// file; direct URL tasks have no provider context and remain unchanged.
	if err != nil && t.UserID != "" && isExpiredDownloadError(err) && ctx.Err() == nil {
		if fresh, refreshErr := drive.GetDownloadURL(t.UserID, t.DriveID, t.FileID, 14400); refreshErr == nil && fresh != nil && fresh.URL != "" {
			url = fresh.URL
			opts.Headers = fresh.Headers
			opts.RequestAuth = fresh.RequestAuth
			m.mu.Lock()
			t.URL = url
			if fresh.Size > 0 {
				t.Size = fresh.Size
			}
			opts.ExpectedSize = t.Size
			m.mu.Unlock()
			if fresh.ForceLocalProxy || fresh.DownloadMode == "proxy" {
				opts.Concurrency = 1
				if fresh.Concurrency > 1 {
					opts.Concurrency = fresh.Concurrency
				}
			}
			m.update(t)
			err = dlengine.Download(ctx, opts, url, t.LocalPath, progress)
		}
	}

	m.mu.Lock()
	removed := m.removed[t.ID]
	if err != nil {
		if t.Status == "canceled" {
			// Cancel has already set the terminal state.
		} else if errors.Is(ctx.Err(), context.Canceled) || strings.Contains(err.Error(), "canceled") {
			// user paused or canceled
			if t.Status != "canceled" && !removed {
				t.Status = "paused"
			}
		} else if !removed {
			t.Status = "failed"
			t.Error = err.Error()
		}
	} else {
		if !removed && t.Status != "canceled" && t.Status != "paused" {
			t.Status = "completed"
			if t.Size <= 0 && t.Downloaded > 0 {
				t.Size = t.Downloaded
			}
			t.Downloaded = t.Size
			t.Progress = 100
		}
	}
	t.Updated = time.Now().Unix()
	m.mu.Unlock()
	m.update(t)
	logging.Info("download worker finished", "task_id", t.ID, "status", t.Status, "error_present", t.Error != "", "duration", logging.Duration(started))
}

func isExpiredDownloadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "http 401") || strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "range http 401") || strings.Contains(msg, "range http 403")
}

// Pause marks a task paused and cancels active download immediately.
func (m *Manager) Pause(id string) {
	m.mu.Lock()
	if cancel, ok := m.cancels[id]; ok {
		cancel()
	}
	t, ok := m.tasks[id]
	if ok && (t.Status == "downloading" || t.Status == "queued") {
		t.Status = "paused"
		t.Updated = time.Now().Unix()
		m.mu.Unlock()
		m.update(t)
		return
	}
	m.mu.Unlock()
}

// Resume re-queues a paused task.
func (m *Manager) Resume(id string) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	_, active := m.cancels[id]
	if ok && !active && (t.Status == "paused" || t.Status == "failed") {
		t.Status = "queued"
		t.Error = ""
		t.Updated = time.Now().Unix()
		m.mu.Unlock()
		m.update(t)
		go m.runDownload(t)
		return
	}
	m.mu.Unlock()
}

// Cancel stops and removes a task.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	_, active := m.cancels[id]
	if cancel, ok := m.cancels[id]; ok {
		cancel()
	}
	t, ok := m.tasks[id]
	if ok {
		t.Status = "canceled"
		t.Updated = time.Now().Unix()
		m.mu.Unlock()
		m.update(t)
		if !active {
			removeDownloadTemp(t)
		}
		return
	}
	m.mu.Unlock()
}

// Remove hard-deletes a task record (from memory and the store) immediately.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	m.removed[id] = true
	delete(m.lastPersist, id)
	if cancel, ok := m.cancels[id]; ok {
		cancel()
	}
	t, ok := m.tasks[id]
	_, active := m.cancels[id]
	delete(m.tasks, id)
	// A waiting task cannot revive itself once it has been removed from tasks.
	// Keep the tombstone only for an actively running task, whose progress
	// callback may still be in flight until runDownload exits.
	if !active {
		delete(m.removed, id)
	}
	m.mu.Unlock()
	if err := m.store.DeleteDownloadTask(id); err != nil {
		logging.Warn("download task removal persistence failed", "task_id", id, "error", err)
	}
	if ok && !active {
		removeDownloadTemp(t)
	}
	if m.onEvent != nil {
		m.onEvent(TaskEvent{Kind: "download", Task: model.DownloadTask{ID: id, Status: "removed"}})
	}
}

// Prioritize boosts one task: pause every other active (downloading/queued)
// task and (re)start this one so it gets the full bandwidth.
func (m *Manager) Prioritize(id string) {
	m.mu.Lock()
	var paused []*model.DownloadTask
	for oid, o := range m.tasks {
		if oid != id && (o.Status == "downloading" || o.Status == "queued") {
			if cancel, ok := m.cancels[oid]; ok {
				cancel()
			}
			o.Status = "paused"
			o.Updated = time.Now().Unix()
			paused = append(paused, o)
		}
	}
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	start := false
	if _, active := m.cancels[id]; !active && (t.Status == "paused" || t.Status == "failed" || t.Status == "canceled") {
		t.Status = "queued"
		t.Error = ""
		t.Updated = time.Now().Unix()
		start = true
	}
	m.mu.Unlock()
	for _, p := range paused {
		m.update(p)
	}
	m.update(t)
	if start {
		go m.runDownload(t)
	}
}

// ClearCompleted removes completed/canceled tasks.
func (m *Manager) ClearCompleted() {
	m.mu.Lock()
	for id, t := range m.tasks {
		if t.Status == "completed" || t.Status == "canceled" || t.Status == "failed" {
			delete(m.tasks, id)
			delete(m.lastPersist, id)
		}
	}
	m.mu.Unlock()
	if err := m.store.ClearDownloadTasks(); err != nil {
		logging.Warn("completed download task cleanup failed", "error", err)
	}
}

// Shutdown stops background work.
func (m *Manager) Shutdown() {
	m.shutdownOnce.Do(func() {
		// cancel the root context so goroutines waiting for a slot or in flight
		// unblock immediately, in addition to any in-flight task contexts.
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Lock()
		for _, c := range m.cancels {
			c()
		}
		m.mu.Unlock()
		close(m.stop)
	})
}

func removeDownloadTemp(t *model.DownloadTask) {
	if t == nil {
		return
	}
	_ = os.Remove(t.LocalPath + ".part")
	_ = os.Remove(t.LocalPath + ".state.json")
}

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), time.Now().UnixMilli()%1000)
}

func safeName(name string) string {
	if name == "" {
		return "download"
	}
	out := []rune{}
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			continue
		default:
			out = append(out, r)
		}
	}
	res := strings.TrimRight(string(out), " .")
	if res == "" || res == "." || res == ".." {
		return "download"
	}
	// Keep names valid on Windows even when the same download is later moved
	// between platforms. Device names remain reserved with an extension.
	stem := res
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "_" + res
	}
	return res
}
