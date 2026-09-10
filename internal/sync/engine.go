// Package sync implements two-way local ⇄ drive synchronization (config-driven).
package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/transfer/dlengine"
)

// Config describes one sync job.
type Config struct {
	ConflictPolicy    string `json:"conflictPolicy,omitempty"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	UserID            string `json:"user_id"`
	DriveID           string `json:"drive_id"`
	LocalDir          string `json:"local_dir"`
	RemoteDir         string `json:"remote_dir"`
	RemoteName        string `json:"remote_name"`
	Direction         string `json:"direction"` // two-way | push | pull
	Enabled           bool   `json:"enabled"`
	IntervalMin       int    `json:"intervalMin"`       // scheduler interval in minutes; <=0 means no scheduling
	DeletePropagation bool   `json:"deletePropagation"` // propagate deletions across sides
}

// Entry is one synced file record.
type Entry struct {
	Scope         string `json:"scope,omitempty"`
	ModTimeNano   int64  `json:"modTimeNano,omitempty"`
	LocalTimeNano int64  `json:"localTimeNano,omitempty"`
	Paired        bool   `json:"paired,omitempty"`
	LocalSize     int64  `json:"localSize,omitempty"`
	LocalTime     int64  `json:"localTime,omitempty"`
	RemoteSize    int64  `json:"remoteSize,omitempty"`
	RemoteTime    int64  `json:"remoteTime,omitempty"`
	LocalPath     string `json:"localPath"`
	RemoteID      string `json:"remoteId"`
	RemoteName    string `json:"remoteName"`
	Size          int64  `json:"size"`
	ModTime       int64  `json:"modTime"`
	Hash          string `json:"hash,omitempty"`
	IsDir         bool   `json:"isDir"`
}

// SnapshotStore persists sync snapshots (last-sync file lists) so the engine
// can detect cross-side deletions on the next run. The store package provides
// a JSON-backed implementation; the interface keeps the engine testable.
type SnapshotStore interface {
	SaveSyncSnapshot(id string, entries []Entry) error
	LoadSyncSnapshot(id string) ([]Entry, error)
	ClearSyncSnapshot(id string) error
}

// LogFunc is the logger callback for key sync events (start/finish/conflict/delete).
type LogFunc func(jobID, event, detail string)

// Engine executes sync jobs.
type Engine struct {
	onProgress func(jobID string, done, total int)
	snapshots  SnapshotStore
	logger     LogFunc
}

// NewEngine creates the sync engine.
// snapshots and logger are optional (nil-safe); snapshots are required for
// delete propagation.
func NewEngine(onProgress func(jobID string, done, total int), opts ...func(*Engine)) *Engine {
	e := &Engine{onProgress: onProgress}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// WithSnapshotStore sets the snapshot store on the engine.
func WithSnapshotStore(s SnapshotStore) func(*Engine) {
	return func(e *Engine) { e.snapshots = s }
}

// WithLogger sets the logger callback on the engine.
func WithLogger(fn LogFunc) func(*Engine) {
	return func(e *Engine) { e.logger = fn }
}

// log is a nil-safe helper.
func (e *Engine) log(jobID, event, detail string) {
	if e.logger != nil {
		e.logger(jobID, event, detail)
	}
}

// Run executes one sync job.
func (e *Engine) Run(ctx context.Context, cfg Config) error {
	e.log(cfg.ID, "start", fmt.Sprintf("direction=%s", cfg.Direction))
	var err error
	switch cfg.Direction {
	case "push":
		err = e.ExecutePlan(ctx, cfg, "", nil)
	case "pull":
		err = e.ExecutePlan(ctx, cfg, "", nil)
	case "", "two-way":
		err = e.ExecutePlan(ctx, cfg, "", nil)
	default:
		err = fmt.Errorf("sync: invalid direction %q", cfg.Direction)
	}
	if err != nil {
		e.log(cfg.ID, "error", err.Error())
		return err
	}
	e.log(cfg.ID, "complete", "ok")
	return nil
}

// remoteTree recursively lists all files under remoteDir, preserving the
// relative path (slash-joined) in RemoteName. It recurses into IsDir entries
// returned by drive.ListDir so that nested subdirectories are not skipped.
func remoteTree(ctx context.Context, cfg Config) ([]Entry, error) {
	var out []Entry
	visited := map[string]bool{}
	var walk func(parentID, relPrefix string) error
	walk = func(parentID, relPrefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if visited[parentID] {
			return fmt.Errorf("sync: repeated remote directory %s", parentID)
		}
		visited[parentID] = true
		if len(visited) > 100000 || strings.Count(relPrefix, "/") > 128 {
			return fmt.Errorf("sync: remote tree exceeds scan limit")
		}
		files, err := drive.ListDirAllContext(ctx, cfg.UserID, cfg.DriveID, parentID, nil)
		if err != nil {
			return err
		}
		for _, f := range files {
			rel := f.Name
			if relPrefix != "" {
				rel = relPrefix + "/" + f.Name
			}
			if f.IsDir {
				// recurse into subdirectory, carrying the relative path forward
				if err := walk(f.FileID, rel); err != nil {
					return err
				}
				continue
			}
			out = append(out, Entry{
				RemoteID:   f.FileID,
				RemoteName: rel,
				Size:       f.Size,
				ModTime:    f.Time,
				Hash:       f.ContentHashName + ":" + f.ContentHash,
				IsDir:      false,
			})
		}
		return nil
	}
	if err := walk(cfg.RemoteDir, ""); err != nil {
		return nil, err
	}
	return out, nil
}

// scanLocalFiles returns a complete local snapshot. Walk errors must be
// propagated: treating an unreadable directory as empty could otherwise turn
// a transient disk/permission failure into remote deletions.
func scanLocalFiles(root string) ([]Entry, error) {
	var local []Entry
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil {
			return fmt.Errorf("scan local path %s: missing file information", path)
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("sync directory contains symbolic link: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve local relative path %s: %w", path, err)
		}
		local = append(local, Entry{
			LocalPath:   path,
			RemoteName:  filepath.ToSlash(rel),
			Size:        info.Size(),
			ModTime:     info.ModTime().Unix(),
			ModTimeNano: info.ModTime().UnixNano(),
			IsDir:       false,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan local directory %s: %w", root, err)
	}
	return local, nil
}

// safeLocalPath maps an untrusted remote relative path into root. Besides
// blocking absolute and parent paths, it rejects existing symlink components
// so a remote file cannot escape through a link inside the sync directory.
func safeLocalPath(root, remoteName string) (string, error) {
	if strings.TrimSpace(remoteName) == "" {
		return "", errors.New("remote path is empty")
	}
	if strings.IndexByte(remoteName, 0) >= 0 {
		return "", errors.New("remote path contains NUL")
	}

	localName := filepath.FromSlash(remoteName)
	for _, part := range strings.Split(localName, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".mnemo-sync-") {
			return "", fmt.Errorf("remote path contains an ambiguous or reserved component: %q", remoteName)
		}
		base, _, _ := strings.Cut(part, ".")
		if runtime.GOOS == "windows" && (!filepath.IsLocal(part) || !filepath.IsLocal(strings.TrimRight(base, " ")) || strings.ContainsAny(part, `<>:"|?*`) || strings.TrimRight(part, ". ") != part) {
			return "", fmt.Errorf("remote path contains an invalid Windows name: %q", remoteName)
		}
	}
	if strings.HasPrefix(remoteName, "/") || strings.HasPrefix(remoteName, `\`) || filepath.IsAbs(localName) || filepath.VolumeName(localName) != "" {
		return "", fmt.Errorf("remote path must be relative: %q", remoteName)
	}
	cleanName := filepath.Clean(localName)
	if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("remote path escapes sync directory: %q", remoteName)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve sync directory: %w", err)
	}
	target := filepath.Join(rootAbs, cleanName)
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("remote path escapes sync directory: %q", remoteName)
	}

	current := rootAbs
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			break
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect local path %s: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("remote path crosses symbolic link: %q", remoteName)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("remote path parent is not a directory: %q", remoteName)
		}
	}
	return target, nil
}

// push uploads newer local files to the drive, preserving nested directory
// structure on the remote side.
func (e *Engine) push(ctx context.Context, cfg Config) error {
	remoteFiles, err := remoteTree(ctx, cfg)
	if err != nil {
		return err
	}
	remoteByName := map[string]Entry{}
	for _, r := range remoteFiles {
		remoteByName[r.RemoteName] = r
	}

	local, err := scanLocalFiles(cfg.LocalDir)
	if err != nil {
		return err
	}

	// find local files missing or newer than remote
	needUpload := []Entry{}
	for _, l := range local {
		r, found := remoteByName[l.RemoteName]
		if !found {
			needUpload = append(needUpload, l)
			continue
		}
		// upload if local is newer (ModTime) or size differs
		if l.ModTime > r.ModTime || l.Size != r.Size {
			needUpload = append(needUpload, l)
		}
	}

	total := len(needUpload)
	for i, l := range needUpload {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := e.uploadFile(ctx, cfg, l); err != nil {
			return err
		}
		if e.onProgress != nil {
			e.onProgress(cfg.ID, i+1, total)
		}
	}

	// delete propagation: files in the snapshot but absent locally now →
	// remove them from the remote side.
	if cfg.DeletePropagation && e.snapshots != nil {
		snap, snapErr := e.snapshots.LoadSyncSnapshot(cfg.ID)
		if snapErr != nil {
			return fmt.Errorf("load sync snapshot: %w", snapErr)
		}
		localByName := map[string]Entry{}
		for _, l := range local {
			localByName[l.RemoteName] = l
		}
		var toDelete []Entry
		for _, s := range snap {
			if _, exists := localByName[s.RemoteName]; !exists {
				toDelete = append(toDelete, s)
			}
		}
		if len(toDelete) > 0 {
			if ok := e.guardDelete(cfg.ID, len(toDelete), len(snap)); !ok {
				return fmt.Errorf("remote deletion paused by safety threshold: %d of %d files", len(toDelete), len(snap))
			} else {
				if err := e.propagateRemoteDeletes(ctx, cfg, toDelete); err != nil {
					return err
				}
			}
		}
	}

	// persist the new snapshot of local files for next run's delete detection
	if e.snapshots != nil {
		if err := e.snapshots.SaveSyncSnapshot(cfg.ID, local); err != nil {
			return fmt.Errorf("save sync snapshot: %w", err)
		}
	}
	return nil
}

// ensureRemoteDir walks the slash-relative path components and creates each
// directory level on the remote drive as needed, returning the leaf FileID.
func ensureRemoteDir(ctx context.Context, cfg Config, relDir string) (string, error) {
	parentID := cfg.RemoteDir
	if relDir == "" || relDir == "." {
		return parentID, nil
	}
	parts := strings.Split(filepath.ToSlash(relDir), "/")
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		// check if it already exists under parentID
		existing, err := drive.ListDirContext(ctx, cfg.UserID, cfg.DriveID, parentID, nil)
		if err != nil {
			return "", err
		}
		found := ""
		for _, f := range existing {
			if f.IsDir && f.Name == part {
				found = f.FileID
				break
			}
		}
		if found != "" {
			parentID = found
			continue
		}
		res, err := drive.MkdirContext(ctx, cfg.UserID, cfg.DriveID, parentID, part)
		if err != nil {
			return "", err
		}
		if res.Error != "" {
			return "", &driveError{op: "Mkdir", msg: res.Error}
		}
		parentID = res.FileID
	}
	return parentID, nil
}

type driveError struct {
	op  string
	msg string
}

func (e *driveError) Error() string { return e.op + ": " + e.msg }

func (e *Engine) uploadFile(ctx context.Context, cfg Config, entry Entry) error {
	// resolve/create remote parent directory so nested paths are preserved
	relDir := filepath.Dir(entry.RemoteName)
	parentID, err := ensureRemoteDir(ctx, cfg, relDir)
	if err != nil {
		return err
	}
	ui := &model.UploadingUI{
		UploadID: entry.LocalPath,
		Info: model.UploadInfo{
			LocalFilePath: entry.LocalPath,
			ParentFileID:  parentID,
			DriveID:       cfg.DriveID,
			Name:          filepath.Base(entry.LocalPath),
			Size:          entry.Size,
		},
	}
	handler, err := drive.QueueUploadHandlerContext(ctx, cfg.UserID, cfg.DriveID)
	if err != nil {
		return err
	}
	return handler(ctx, ui)
}

// pull downloads remote files missing or changed locally, recursing into
// remote subdirectories and preserving relative path structure locally.
func (e *Engine) pull(ctx context.Context, cfg Config) error {
	remoteFiles, err := remoteTree(ctx, cfg)
	if err != nil {
		return err
	}
	remoteByName := map[string]Entry{}
	for _, r := range remoteFiles {
		remoteByName[r.RemoteName] = r
	}
	total := len(remoteFiles)
	var failures []error
	for i, f := range remoteFiles {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// preserve relative path structure locally
		localPath, pathErr := safeLocalPath(cfg.LocalDir, f.RemoteName)
		if pathErr != nil {
			failures = append(failures, fmt.Errorf("reject remote file %q: %w", f.RemoteName, pathErr))
			continue
		}
		localModTime, localSize := int64(0), int64(-1)
		if info, statErr := os.Stat(localPath); statErr == nil {
			localModTime = info.ModTime().Unix()
			localSize = info.Size()
		}
		// only download if remote is newer or size differs
		if f.ModTime <= localModTime && f.Size == localSize {
			continue
		}
		u, err := drive.GetDownloadURLContext(ctx, cfg.UserID, cfg.DriveID, f.RemoteID, 14400)
		if err != nil {
			failures = append(failures, fmt.Errorf("resolve download URL for %s: %w", f.RemoteName, err))
			continue
		}
		// ensure local parent directory exists
		if mkErr := os.MkdirAll(filepath.Dir(localPath), 0o755); mkErr != nil {
			failures = append(failures, fmt.Errorf("create local directory for %s: %w", f.RemoteName, mkErr))
			continue
		}
		if err := downloadTo(ctx, u, localPath); err != nil {
			failures = append(failures, fmt.Errorf("download %s: %w", f.RemoteName, err))
			continue
		}
		if e.onProgress != nil {
			e.onProgress(cfg.ID, i+1, total)
		}
	}
	// A partial or unsafe pull must not propagate deletions or advance the
	// snapshot. Doing either would turn a download/path failure into data loss.
	if len(failures) > 0 {
		return errors.Join(failures...)
	}

	// delete propagation: files in the snapshot but absent on the remote now →
	// remove them locally.
	if cfg.DeletePropagation && e.snapshots != nil {
		snap, snapErr := e.snapshots.LoadSyncSnapshot(cfg.ID)
		if snapErr != nil {
			return fmt.Errorf("load sync snapshot: %w", snapErr)
		}
		var toDelete []Entry
		for _, s := range snap {
			if _, exists := remoteByName[s.RemoteName]; !exists {
				toDelete = append(toDelete, s)
			}
		}
		if len(toDelete) > 0 {
			if ok := e.guardDelete(cfg.ID, len(toDelete), len(snap)); !ok {
				return fmt.Errorf("local deletion paused by safety threshold: %d of %d files", len(toDelete), len(snap))
			} else {
				if err := e.propagateLocalDeletes(cfg, toDelete); err != nil {
					return err
				}
			}
		}
	}

	// persist the new snapshot of remote files for next run's delete detection
	if e.snapshots != nil {
		if err := e.snapshots.SaveSyncSnapshot(cfg.ID, remoteFiles); err != nil {
			return fmt.Errorf("save sync snapshot: %w", err)
		}
	}
	return nil
}

// twoWay is a conservative mirror: upload new local, download missing remote.
func (e *Engine) twoWay(ctx context.Context, cfg Config) error {
	if err := e.push(ctx, cfg); err != nil {
		return err
	}
	return e.pull(ctx, cfg)
}

// downloadTo streams a download url to a local file via the segmented engine.
// It accepts the caller's context so downloads can be cancelled reliably.
func downloadTo(ctx context.Context, u *model.DownloadURL, path string) error {
	if u == nil || u.URL == "" {
		return fmt.Errorf("sync: download URL is empty")
	}
	opts := dlengine.Options{}
	opts.RequestAuth = u.RequestAuth
	if u.ForceLocalProxy || u.DownloadMode == "proxy" {
		opts.Concurrency = 1
	}
	if u.Headers != nil {
		opts.Headers = u.Headers
	}
	// single-stream download for sync simplicity
	opts.Concurrency = 1
	return dlengine.Download(ctx, opts, u.URL, path, nil)
}

// guardDelete enforces the safety threshold: if the number of files to
// delete exceeds 50% of the snapshot total, deletions are cancelled to
// prevent catastrophic data loss from a transient listing failure.
func (e *Engine) guardDelete(jobID string, deleteCount, snapTotal int) bool {
	if snapTotal == 0 {
		return false
	}
	ratio := float64(deleteCount) / float64(snapTotal)
	if ratio > 0.5 {
		e.log(jobID, "delete_cancelled", fmt.Sprintf("delete count %d exceeds 50%% of snapshot %d", deleteCount, snapTotal))
		return false
	}
	return true
}

// propagateRemoteDeletes trashes/deletes the given remote files that no
// longer exist locally. It uses the drive trash batch for safety.
func (e *Engine) propagateRemoteDeletes(ctx context.Context, cfg Config, toDelete []Entry) error {
	ids := make([]string, 0, len(toDelete))
	names := make([]string, 0, len(toDelete))
	for _, d := range toDelete {
		if d.RemoteID != "" {
			ids = append(ids, d.RemoteID)
			names = append(names, d.RemoteName)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var removed []string
	var err error
	caps := drive.RegistryCaps(drive.ProviderOf(cfg.UserID, cfg.DriveID, ""))
	if caps.RecycleBin {
		removed, err = drive.TrashBatchContext(ctx, cfg.UserID, cfg.DriveID, ids)
	} else {
		refs := make([]drive.FileRef, 0, len(ids))
		isDir := false
		for _, id := range ids {
			refs = append(refs, drive.FileRef{ID: id, IsDir: &isDir})
		}
		removed, err = drive.DeleteBatchContext(ctx, cfg.UserID, cfg.DriveID, refs)
	}
	if err != nil {
		e.log(cfg.ID, "delete_error", fmt.Sprintf("remote trash failed: %v", err))
		return fmt.Errorf("delete remote files: %w", err)
	}
	for _, id := range ids {
		found := false
		for _, result := range removed {
			if result == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("remote deletion was not confirmed: %s", id)
		}
	}
	e.log(cfg.ID, "delete", fmt.Sprintf("removed remote files: %v", names))
	return nil
}

// propagateLocalDeletes removes local files that no longer exist on the
// remote side.
func (e *Engine) propagateLocalDeletes(cfg Config, toDelete []Entry) error {
	names := make([]string, 0, len(toDelete))
	var failures []error
	for _, d := range toDelete {
		localPath, pathErr := safeLocalPath(cfg.LocalDir, d.RemoteName)
		if pathErr != nil {
			failures = append(failures, fmt.Errorf("reject local deletion %q: %w", d.RemoteName, pathErr))
			continue
		}
		if err := os.Remove(localPath); err != nil {
			failures = append(failures, fmt.Errorf("delete local file %s: %w", d.RemoteName, err))
			continue
		}
		names = append(names, d.RemoteName)
	}
	if len(names) > 0 {
		e.log(cfg.ID, "delete", fmt.Sprintf("removed local files: %v", names))
	}
	return errors.Join(failures...)
}

// StartScheduler launches a background goroutine that periodically runs all
// enabled sync jobs at the given IntervalMin interval. It blocks until stop
// is closed. When IntervalMin <= 0 the scheduler is a no-op and returns
// immediately.
func (e *Engine) StartScheduler(stop <-chan struct{}, configs func() ([]Config, error), runners ...func(context.Context, Config) error) {
	run := e.Run
	if len(runners) > 0 && runners[0] != nil {
		run = runners[0]
	}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Re-read the config every minute so jobs can be added, removed, or
		// have their interval changed without restarting the application.
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		lastRun := make(map[string]time.Time)
		running := make(map[string]bool)
		var runningMu sync.Mutex
		for {
			select {
			case <-stop:
				cancel()
				return
			case <-ticker.C:
				runList, err := configs()
				if err != nil {
					continue
				}
				seen := make(map[string]bool, len(runList))
				now := time.Now()
				for _, c := range runList {
					if !c.Enabled || c.IntervalMin <= 0 {
						continue
					}
					seen[c.ID] = true
					runningMu.Lock()
					isRunning := running[c.ID]
					runningMu.Unlock()
					if isRunning || now.Sub(lastRun[c.ID]) < time.Duration(c.IntervalMin)*time.Minute {
						continue
					}
					lastRun[c.ID] = now
					runningMu.Lock()
					running[c.ID] = true
					runningMu.Unlock()
					job := c
					go func() {
						if err := run(ctx, job); err != nil {
							e.log(job.ID, "scheduler_error", err.Error())
						}
						// The scheduler goroutine owns this map; this callback is
						// only used as a best-effort guard for the next tick.
						runningMu.Lock()
						delete(running, job.ID)
						runningMu.Unlock()
					}()
				}
				for id := range lastRun {
					if !seen[id] {
						delete(lastRun, id)
						runningMu.Lock()
						delete(running, id)
						runningMu.Unlock()
					}
				}
			}
		}
	}()
}

var _ = time.Now
var _ = strings.TrimSpace
