package transfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
)

func TestManagerConcurrency(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Shutdown()

	m.mu.Lock()
	got := m.maxConcurrent
	m.mu.Unlock()
	if got != 3 {
		t.Errorf("expected max concurrency 3, got %d", got)
	}
}

func TestDownloadReportsUnreadableSettingsBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	task := &model.DownloadTask{ID: "settings-error", Name: "file", URL: ":invalid", Status: "queued"}
	m.addDownloadTask(task, task.Name)
	m.runDownload(task)
	if task.Status != "failed" || !strings.Contains(task.Error, "读取下载设置失败") {
		t.Fatalf("status=%s error=%s", task.Status, task.Error)
	}
}

func TestManagerSetConcurrency(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	m, _ := NewManager(st, dir, nil)
	defer m.Shutdown()

	m.SetConcurrency(5)
	m.mu.Lock()
	got := m.maxConcurrent
	m.mu.Unlock()
	if got != 5 {
		t.Errorf("expected max concurrency 5, got %d", got)
	}
}

func TestManagerConcurrencyCanShrinkWithoutOverlappingSlots(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.acquireSlot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.acquireSlot(ctx); err != nil {
		t.Fatal(err)
	}
	m.SetConcurrency(1)

	acquired := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		if err := m.acquireSlot(ctx); err != nil {
			errs <- err
			return
		}
		close(acquired)
		m.releaseSlot()
	}()
	assertNotAcquired := func() {
		t.Helper()
		select {
		case <-acquired:
			t.Fatal("queued task acquired a slot above the reduced limit")
		case err := <-errs:
			t.Fatal(err)
		case <-time.After(30 * time.Millisecond):
		}
	}
	assertNotAcquired()
	m.releaseSlot()
	assertNotAcquired()
	m.releaseSlot()
	select {
	case <-acquired:
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("queued task did not acquire a slot after active work released")
	}
}

func TestManagerSetDir(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	m, _ := NewManager(st, dir, nil)
	defer m.Shutdown()

	newDir := dir + "/newdown"
	m.SetDir(newDir)
	m.mu.Lock()
	got := m.dir
	m.mu.Unlock()
	if got != newDir {
		t.Errorf("expected dir %s, got %s", newDir, got)
	}
}

func TestPerFileDownloadConcurrencyIsBounded(t *testing.T) {
	cases := []struct {
		configured int
		want       int
	}{
		{configured: 0, want: 3},
		{configured: 1, want: 1},
		{configured: 3, want: 3},
		{configured: 8, want: maxPerDownloadConnections},
	}
	for _, tc := range cases {
		if got := concurrencyFromSettings(store.Settings{MaxConcurrentDownloads: tc.configured}); got != tc.want {
			t.Errorf("concurrencyFromSettings(%d) = %d, want %d", tc.configured, got, tc.want)
		}
	}
}

func TestManagerShutdownCancelsContext(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	m, _ := NewManager(st, dir, nil)

	// root context should be active before shutdown
	if m.ctx == nil {
		t.Fatal("ctx is nil")
	}
	select {
	case <-m.ctx.Done():
		t.Fatal("ctx should not be done before Shutdown")
	default:
	}
	m.Shutdown()
	select {
	case <-m.ctx.Done():
		// expected: ctx is done after Shutdown
	default:
		t.Error("ctx should be done after Shutdown")
	}
}

func TestManagerLoadPersistedMarksPaused(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	// save a downloading task that should be restored as paused
	task := &model.DownloadTask{
		ID:     "dl-test1",
		UserID: "pikpak-test",
		Status: "downloading",
		Name:   "test.txt",
	}
	_ = st.SaveDownloadTask(task)

	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Shutdown()

	t2, ok := m.get("dl-test1")
	if !ok {
		t.Fatal("task not restored")
	}
	if t2.Status != "paused" {
		t.Errorf("expected paused, got %s", t2.Status)
	}
}

func TestManagerKeepTasksDisabledClearsFinishedHistory(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings := store.DefaultSettings()
	settings.KeepTasks = false
	if err := st.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"completed", "failed", "canceled"} {
		if err := st.SaveDownloadTask(&model.DownloadTask{ID: "finished-" + status, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveDownloadTask(&model.DownloadTask{ID: "paused", Status: "paused"}); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	if _, ok := m.get("paused"); !ok {
		t.Fatal("paused task should remain resumable when history retention is disabled")
	}
	if _, ok := m.get("finished-completed"); ok {
		t.Fatal("completed task history was restored despite KeepTasks=false")
	}
	list, err := st.ListDownloadTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "paused" {
		t.Fatalf("finished task history was not cleared: %#v", list)
	}
}

func TestManagerProgressPersistenceIsThrottled(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	task := &model.DownloadTask{ID: "dl-progress", Name: "payload.bin", Status: "downloading"}
	m.update(task)
	task.Downloaded = 128
	m.update(task)

	list, err := st.ListDownloadTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Downloaded != 0 {
		t.Fatalf("progress update should remain in memory before checkpoint: %#v", list)
	}

	m.mu.Lock()
	state := m.lastPersist[task.ID]
	state.at = time.Now().Add(-transferProgressPersistInterval)
	m.lastPersist[task.ID] = state
	m.mu.Unlock()
	m.update(task)

	list, err = st.ListDownloadTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Downloaded != 128 {
		t.Fatalf("checkpoint should persist latest progress: %#v", list)
	}
}

func TestManagerRemoveCompletedDoesNotRetainTombstone(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	task := &model.DownloadTask{ID: "dl-removed", Name: "done.bin", Status: "completed"}
	m.update(task)
	m.Remove(task.ID)

	m.mu.Lock()
	_, retained := m.removed[task.ID]
	m.mu.Unlock()
	if retained {
		t.Fatal("completed task removal should not retain a tombstone")
	}

	list, err := st.ListDownloadTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("removed task was persisted again: %#v", list)
	}
}

func TestManagerRemovedQueuedTaskDoesNotReviveDuringShutdown(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	task := &model.DownloadTask{ID: "dl-queued-removed", Name: "queued.bin", Status: "queued"}
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()
	m.Remove(task.ID)
	m.Shutdown()
	m.runDownload(task)

	m.mu.Lock()
	_, revived := m.tasks[task.ID]
	_, retained := m.removed[task.ID]
	m.mu.Unlock()
	if revived || retained {
		t.Fatalf("removed queued task was retained or revived: revived=%v retained=%v", revived, retained)
	}
}

// TestManagerConcurrentAccess verifies no data race when multiple goroutines
// access the manager simultaneously.
func TestManagerConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	m, _ := NewManager(st, dir, nil)
	defer m.Shutdown()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.SetConcurrency(n + 1)
			_ = m.List()
		}(i)
	}
	wg.Wait()
}

func TestSafeNameIsCrossPlatform(t *testing.T) {
	cases := map[string]string{
		"report.txt": "report.txt",
		"CON.txt":    "_CON.txt",
		"name. ":     "name",
		"../":        "download",
	}
	for input, want := range cases {
		if got := safeName(input); got != want {
			t.Errorf("safeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestManagerAssignsUniqueDownloadTargets(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := &model.DownloadTask{ID: "dl-unique-1", Name: "report.txt", Status: "queued"}
	second := &model.DownloadTask{ID: "dl-unique-2", Name: "report.txt", Status: "queued"}
	m.addDownloadTask(first, first.Name)
	m.addDownloadTask(second, second.Name)

	if want := filepath.Join(dir, "report (1).txt"); first.LocalPath != want {
		t.Fatalf("first LocalPath = %q, want %q", first.LocalPath, want)
	}
	if want := filepath.Join(dir, "report (2).txt"); second.LocalPath != want {
		t.Fatalf("second LocalPath = %q, want %q", second.LocalPath, want)
	}
	if first.LocalPath == second.LocalPath {
		t.Fatal("same-name downloads share a target path")
	}
}

func TestManagerAvoidsOrphanedPartialTarget(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	m, _ := NewManager(st, dir, nil)
	defer m.Shutdown()

	orphaned := filepath.Join(dir, "archive.zip.part")
	if err := os.WriteFile(orphaned, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := &model.DownloadTask{ID: "dl-partial", Name: "archive.zip", Status: "queued"}
	m.addDownloadTask(task, task.Name)
	if want := filepath.Join(dir, "archive (1).zip"); task.LocalPath != want {
		t.Fatalf("LocalPath = %q, want %q", task.LocalPath, want)
	}
}

func TestManagerDoesNotExposeDownloadSecrets(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	var emitted TaskEvent
	m, _ := NewManager(st, dir, func(event TaskEvent) { emitted = event })
	defer m.Shutdown()

	task := &model.DownloadTask{
		ID:      "dl-secret",
		Name:    "secret.bin",
		Status:  "paused",
		URL:     "https://download.example/file?signature=secret",
		Headers: map[string]string{"Authorization": "Bearer secret"},
		Error:   "password=secret https://download.example/file?signature=secret",
	}
	m.addDownloadTask(task, task.Name)

	if emitted.Task.URL != "" || emitted.Task.Headers != nil || strings.Contains(emitted.Task.Error, "secret") {
		t.Fatalf("event exposed download secret: %#v", emitted.Task)
	}
	listed := m.List()
	if len(listed) != 1 || listed[0].URL != "" || listed[0].Headers != nil || strings.Contains(listed[0].Error, "secret") {
		t.Fatalf("List exposed download secret: %#v", listed)
	}
	persisted, err := st.ListDownloadTasks()
	if err != nil || len(persisted) != 1 || persisted[0].URL != "" || persisted[0].Headers != nil || strings.Contains(persisted[0].Error, "secret") {
		t.Fatalf("store exposed download secret: tasks=%#v err=%v", persisted, err)
	}
}
