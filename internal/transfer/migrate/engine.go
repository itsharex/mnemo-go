// Package migrate implements cross-drive file migration: copy/move files
// between two accounts by streaming (source download → target upload).
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/logging"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
	"mnemo-go/internal/store"
)

// Job is a type alias for model.MigrateJob so existing callers (app layer,
// frontend events) continue to reference migrate.Job.
type Job = model.MigrateJob

// OnProgress is invoked per file.
type OnProgress func(j *Job)

// Engine runs migration jobs.
type Engine struct {
	store      *store.Store
	onProgress OnProgress
	persistMu  sync.Mutex
	// cancels stores per-job cancel funcs so Cancel(jobID) can abort a Run.
	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
}

// NewEngine creates the migration engine. store may be nil (disables
// persistence and startup recovery).
func NewEngine(st *store.Store, onProgress OnProgress) *Engine {
	return &Engine{
		store:      st,
		onProgress: onProgress,
		cancels:    make(map[string]context.CancelFunc),
	}
}

// Cancel cancels the migration job with the given id.
// It is safe to call even if the job is unknown or already finished;
// in those cases it is a no-op.
func (e *Engine) Cancel(jobID string) {
	e.cancelsMu.Lock()
	cancel, ok := e.cancels[jobID]
	e.cancelsMu.Unlock()
	if ok {
		cancel()
	}
}

// CancelAll stops every active migration. It is used during application
// shutdown so no migration can continue after the Wails context is gone.
func (e *Engine) CancelAll() {
	e.cancelsMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(e.cancels))
	for _, cancel := range e.cancels {
		cancels = append(cancels, cancel)
	}
	e.cancelsMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// registerCancel stores the cancel func for a job and returns a derived
// context that is cancelled by Cancel(jobID) or parent cancellation.
func (e *Engine) registerCancel(parent context.Context, jobID string) (context.Context, context.CancelFunc, bool) {
	if parent == nil {
		parent = context.Background()
	}
	e.cancelsMu.Lock()
	if _, exists := e.cancels[jobID]; exists {
		e.cancelsMu.Unlock()
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	e.cancels[jobID] = cancel
	e.cancelsMu.Unlock()
	return ctx, cancel, true
}

// releaseCancel drops the cancel entry for a finished job.
func (e *Engine) releaseCancel(jobID string) {
	e.cancelsMu.Lock()
	delete(e.cancels, jobID)
	e.cancelsMu.Unlock()
}

// saveJob persists the job to the store (if configured).
func (e *Engine) saveJob(job *Job) error {
	if e.store == nil || job == nil {
		return nil
	}
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	err := e.store.SaveMigrateJob(job)
	if err != nil {
		logging.Warn("migration checkpoint persistence failed", "job_id", job.ID, "error", err)
	}
	return err
}

// RecoverUnfinished marks any persisted jobs that were still running/pending
// at last shutdown as canceled. This aligns with the legacy behavior of not
// silently resuming interrupted migrations. Should be called once at startup.
func (e *Engine) RecoverUnfinished() {
	if e.store == nil {
		return
	}
	jobs, err := e.store.ListMigrateJobs()
	if err != nil {
		return
	}
	for i := range jobs {
		j := &jobs[i]
		switch j.Status {
		case "running", "pending", "":
			j.Status = "canceled"
			j.Message = "recovered: job was interrupted; marked canceled on startup"
			_ = e.store.SaveMigrateJob(j)
			e.emit(j)
		}
	}
}

// Run migrates the given files. Each file is streamed through a temp file
// (or directly piped when the target provider supports streaming — see
// tryStreamMigrate).
func (e *Engine) Run(ctx context.Context, job *Job) error {
	if job == nil {
		return errors.New("migrate: nil job")
	}
	if job.ID == "" {
		return errors.New("migrate: job id is empty")
	}
	if err := ValidateEndpoints(job.SrcUser, job.SrcDrive, job.DstUser, job.DstDrive); err != nil {
		return err
	}
	ctx, cancel, registered := e.registerCancel(ctx, job.ID)
	if !registered {
		return fmt.Errorf("migrate: job %q is already running", job.ID)
	}
	defer func() {
		cancel()
		e.releaseCancel(job.ID)
	}()

	job.Total = int64(len(job.FileIDs))
	completedTopLevel := completedTopLevelCount(job)
	job.Processed = completedTopLevel
	job.Failed = 0
	// Byte counters describe the current run. Recomputing them prevents a
	// retry from double-counting bytes that completed before interruption.
	job.TotalBytes = 0
	job.ProcessedBytes = 0
	job.Status = "running"
	job.Message = ""
	if job.CreatedAt == 0 {
		job.CreatedAt = time.Now().Unix()
	}
	job.UpdatedAt = time.Now().Unix()
	if err := e.saveJob(job); err != nil {
		return err
	}
	e.emit(job)
	succeeded := completedTopLevel
	partial := false
	for _, fileID := range job.FileIDs {
		if ctx.Err() != nil {
			return e.finishCanceled(job, ctx.Err())
		}
		if jobHasID(job.CompletedFileIDs, fileID) {
			continue
		}
		err := e.migrateOne(ctx, job, fileID)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return e.finishCanceled(job, ctx.Err())
			}
			job.Failed += failureCount(err)
			partial = partial || isPartialError(err)
			job.Message = err.Error()
		} else {
			succeeded++
		}
		job.Processed++
		job.UpdatedAt = time.Now().Unix()
		e.saveJob(job)
		e.emit(job)
	}
	job.Status = migrationResultStatus(job.Failed, succeeded, partial)
	job.UpdatedAt = time.Now().Unix()
	e.saveJob(job)
	e.emit(job)
	return nil
}

// ValidateEndpoints rejects migrations whose source and target identify the
// same mounted drive. A same-drive subtree copy can recursively discover the
// files it just created; callers should use the provider's Move/Copy operation
// instead, where the provider can enforce its own parent relationship rules.
func ValidateEndpoints(srcUser, srcDrive, dstUser, dstDrive string) error {
	srcUser = strings.TrimSpace(srcUser)
	srcDrive = strings.TrimSpace(srcDrive)
	dstUser = strings.TrimSpace(dstUser)
	dstDrive = strings.TrimSpace(dstDrive)
	if srcUser == "" && srcDrive == "" && dstUser == "" && dstDrive == "" {
		// Keep the engine's empty-file-list smoke test and programmatic no-op
		// jobs backwards-compatible; the app binding validates real requests.
		return nil
	}
	if srcUser == "" || srcDrive == "" || dstUser == "" || dstDrive == "" {
		return errors.New("migrate: source and target account are incomplete")
	}
	if srcUser == dstUser && srcDrive == dstDrive {
		return errors.New("migrate: source and target cannot be the same drive; use the drive move operation")
	}
	return nil
}

func (e *Engine) finishCanceled(job *Job, cause error) error {
	if cause == nil {
		cause = context.Canceled
	}
	job.Status = "canceled"
	job.Message = cause.Error()
	job.UpdatedAt = time.Now().Unix()
	e.saveJob(job)
	e.emit(job)
	return cause
}

// migrateOne migrates a single file. It attempts strategies in order:
//  1. Rapid upload (hash-based秒传) if both source and target share a hash.
//  2. Stream pipe (source download Reader → target StreamUploader).
//  3. Spool (temp-file) fallback.
func (e *Engine) migrateOne(ctx context.Context, job *Job, fileID string) error {
	return e.migrateOneTo(ctx, job, fileID, job.DstParent)
}

func (e *Engine) migrateOneTo(ctx context.Context, job *Job, fileID, targetParent string) (resultErr error) {
	if jobHasID(job.CompletedFileIDs, fileID) {
		return nil
	}
	if job.Items == nil {
		job.Items = map[string]model.MigrateItem{}
	}
	previous := job.Items[fileID]
	item := model.MigrateItem{ID: fileID, Name: fileID, ParentID: targetParent, Status: "running", Verification: "unverified"}
	if job.Move && jobHasID(job.CopiedFileIDs, fileID) {
		item = previous
		item.ID, item.Status, item.Error = fileID, "running", ""
	}
	job.Items[fileID] = item
	defer func() {
		item.TargetID = job.Items[fileID].TargetID
		if resultErr != nil {
			item.Status = "failed"
			item.Error = resultErr.Error()
			if errors.Is(resultErr, context.Canceled) {
				item.Status = "canceled"
			}
		} else {
			item.Status = "completed"
		}
		job.Items[fileID] = item
		e.saveJob(job)
		e.emit(job)
	}()
	// resolve source file
	srcFile, err := drive.GetFileContext(ctx, job.SrcUser, job.SrcDrive, fileID)
	if err != nil {
		return err
	}
	if srcFile == nil {
		return errors.New("migrate: source file is empty")
	}
	if job.Move && jobHasID(job.CopiedFileIDs, srcFile.FileID) {
		if !srcFile.IsDir && (!item.SourceRecorded || item.Size != srcFile.Size || item.SourceTime != srcFile.Time || item.SourceHash != sourceHash(srcFile)) {
			return newPartialMigrationError(errors.New("来源文件已变化或缺少原始记录，已保留来源，请核对后重新迁移"), 1)
		}
		if err := e.finalizeMove(ctx, job, srcFile); err != nil {
			return err
		}
		e.markCompleted(job, srcFile.FileID)
		return nil
	}
	item.Name = srcFile.Name
	item.Size = srcFile.Size
	item.IsDir = srcFile.IsDir
	item.SourceRecorded, item.SourceTime, item.SourceHash = true, srcFile.Time, sourceHash(srcFile)
	job.Items[fileID] = item
	e.emit(job)
	if srcFile.IsDir {
		if err := e.migrateDir(ctx, job, srcFile, targetParent); err != nil {
			return err
		}
		return e.completeResource(ctx, job, srcFile)
	}
	// accumulate total bytes for progress
	job.TotalBytes += srcFile.Size
	progressStart := job.ProcessedBytes
	defer func() { e.emit(job) }()

	// 1) Try rapid upload (秒传).
	if migrated, err := e.tryRapidMigrate(ctx, job, srcFile, targetParent); migrated {
		if err == nil {
			item.Verification = "provider-hash"
			completeBytes(job, progressStart, srcFile.Size)
			item.Verification = "size"
			return e.completeResource(ctx, job, srcFile)
		}
		// rapid upload failed — fall through to stream/spool.
		job.ProcessedBytes = progressStart
	}

	// 2) Attempt streaming migration; fall back to spool on failure.
	if migrated, err := e.tryStreamMigrate(ctx, job, srcFile, targetParent); migrated {
		if err != nil {
			// streaming failed — fall back to spool.
			job.ProcessedBytes = progressStart
			if err := e.spoolMigrate(ctx, job, srcFile, targetParent); err != nil {
				job.ProcessedBytes = progressStart
				return err
			}
			completeBytes(job, progressStart, srcFile.Size)
			return e.completeResource(ctx, job, srcFile)
		}
		// streaming succeeded; handle move cleanup.
		item.Verification = "size"
		completeBytes(job, progressStart, srcFile.Size)
		return e.completeResource(ctx, job, srcFile)
	}
	// 3) streaming not supported by this provider — use spool.
	if err := e.spoolMigrate(ctx, job, srcFile, targetParent); err != nil {
		job.ProcessedBytes = progressStart
		return err
	}
	completeBytes(job, progressStart, srcFile.Size)
	item.Verification = "size"
	return e.completeResource(ctx, job, srcFile)
}

// completeResource records a successful target copy before attempting source
// deletion for a move. If cleanup is interrupted or rejected, a later retry
// performs only the cleanup and never uploads a second destination copy.
func (e *Engine) completeResource(ctx context.Context, job *Job, srcFile *model.File) error {
	if job.Move {
		if err := e.markCopied(job, srcFile.FileID); err != nil {
			return newPartialMigrationError(fmt.Errorf("目标已复制，但恢复记录保存失败，已保留来源: %w", err), 1)
		}
		if err := e.finalizeMove(ctx, job, srcFile); err != nil {
			return err
		}
	}
	e.markCompleted(job, srcFile.FileID)
	return nil
}

// tryRapidMigrate attempts a hash-based秒传: if both the source provider can
// provide a hash and the target provider supports rapid upload with the same
// hash method, it resolves the source hash and calls RapidUploadByHash on the
// target.
//
// Returns (attempted, error):
//   - attempted=false when no common hash method exists (caller falls through).
//   - attempted=true with nil error on success.
//   - attempted=true with non-nil error when秒传 was tried but failed (caller
//     falls through to stream/spool).
func (e *Engine) tryRapidMigrate(ctx context.Context, job *Job, srcFile *model.File, targetParent string) (bool, error) {
	srcProvider := drive.ResolveProvider(job.SrcUser, job.SrcDrive, "")
	dstProvider := drive.ResolveProvider(job.DstUser, job.DstDrive, "")
	if !rapidUploadAllowed(srcProvider, dstProvider) {
		return false, nil
	}

	srcCaps := drive.RegistryCaps(srcProvider)
	dstCaps := drive.RegistryCaps(dstProvider)

	// find a common hash method supported by both source (provide) and target (rapid).
	method := commonHashMethod(srcCaps.ProvideHashes, dstCaps.RapidUploadHashes)
	if method == "" {
		return false, nil
	}

	// resolve the source file's hash (allowStream=true lets the provider
	// download+hash when no precomputed fingerprint exists).
	hash, err := drive.ResolveTransferHashContext(ctx, job.SrcUser, job.SrcDrive, srcFile.FileID, method, true)
	if err != nil {
		return true, fmt.Errorf("rapid: resolve hash %s failed: %w", method, err)
	}
	if strings.TrimSpace(hash) == "" {
		return true, fmt.Errorf("rapid: resolve hash %s returned empty hash", method)
	}

	// attempt秒传 on the target.
	result, err := drive.RapidUploadByHashContext(ctx, job.DstUser, job.DstDrive, drive.RapidUploadRequest{
		ParentID:  targetParent,
		FileName:  srcFile.Name,
		Method:    method,
		Hash:      hash,
		Size:      srcFile.Size,
		Duplicate: 0, // rename
	})
	if err != nil {
		return true, fmt.Errorf("rapid: %w", err)
	}
	if result != nil && result.Reuse {
		if job.Items != nil {
			item := job.Items[srcFile.FileID]
			item.TargetID = result.FileID
			job.Items[srcFile.FileID] = item
		}
		return true, nil
	}
	if result != nil && result.FileID != "" {
		if job.Items != nil {
			item := job.Items[srcFile.FileID]
			item.TargetID = result.FileID
			job.Items[srcFile.FileID] = item
		}
		// some providers return a file id without setting Reuse=true.
		return true, nil
	}
	return true, fmt.Errorf("rapid: target did not accept hash %s", method)
}

// rapidUploadAllowed keeps providers with incompatible transfer semantics out
// of the cross-drive rapid-upload path. Yike exposes MD5 metadata for its own
// uploads, but it does not implement a target-side rapid-upload API.
func rapidUploadAllowed(srcProvider, dstProvider string) bool {
	return srcProvider != model.ProviderYike && dstProvider != model.ProviderYike
}

// commonHashMethod returns the first hash method that appears in both the
// source's ProvideHashes and the target's RapidUploadHashes lists.
// Returns "" when no common method exists.
func commonHashMethod(provide, rapid []string) string {
	rapidSet := make(map[string]bool, len(rapid))
	for _, r := range rapid {
		rapidSet[r] = true
	}
	for _, p := range provide {
		if rapidSet[p] {
			return p
		}
	}
	return ""
}

// tryStreamMigrate attempts to pipe the source download stream directly
// into the target upload via io.Pipe, avoiding a local temp file.
//
// It returns (supported, error):
//   - supported=false when the target provider does not expose a
//     StreamUploader capability, in which case the caller falls back to
//     spoolMigrate.
//   - supported=true with a non-nil error when streaming was attempted but
//     failed; the caller may still fall back to spoolMigrate.
func (e *Engine) tryStreamMigrate(ctx context.Context, job *Job, srcFile *model.File, targetParent string) (bool, error) {
	streamUploader, err := drive.StreamUploadHandlerContext(ctx, job.DstUser, job.DstDrive)
	if err != nil {
		// target provider does not implement StreamUploader.
		return false, nil
	}

	// resolve the source download URL.
	dl, err := drive.GetDownloadURLContext(ctx, job.SrcUser, job.SrcDrive, srcFile.FileID, 3600)
	if err != nil {
		return true, fmt.Errorf("stream: resolve download url: %w", err)
	}

	start := job.ProcessedBytes
	err = streamMigration(ctx, dl, job, func(reader io.Reader) error {
		return streamUploader(ctx, targetParent, srcFile.Name, srcFile.Size, reader)
	})
	if err == nil && srcFile.Size >= 0 && job.ProcessedBytes-start != srcFile.Size {
		err = fmt.Errorf("stream: size mismatch: expected %d, received %d", srcFile.Size, job.ProcessedBytes-start)
	}
	return true, err
}

func streamMigration(ctx context.Context, dl *model.DownloadURL, job *Job, upload func(io.Reader) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// set up the io.Pipe: the download goroutine writes to pw, the upload
	// reads from pr.
	pr, pw := io.Pipe()
	defer pr.Close()

	// goroutine: stream the download into the pipe writer.
	downloadErrCh := make(chan error, 1)
	go func() {
		err := downloadToCounted(ctx, dl, pw, job)
		_ = pw.CloseWithError(err)
		downloadErrCh <- err
	}()

	// upload from the pipe reader on the target.
	uploadErr := upload(pr)

	// An uploader may return before reading the body, even without an error.
	// Release blocked pipe writes and cancel any outstanding source request.
	_ = pr.CloseWithError(uploadErr)
	cancel()

	// wait for the download goroutine to complete.
	downloadErr := <-downloadErrCh

	if uploadErr != nil {
		return fmt.Errorf("stream: upload: %w", uploadErr)
	}
	if downloadErr != nil {
		return fmt.Errorf("stream: download: %w", downloadErr)
	}

	return nil
}

// spoolMigrate downloads the source to a temp file then uploads it to the
// target. This is the legacy/fallback path.
func (e *Engine) spoolMigrate(ctx context.Context, job *Job, srcFile *model.File, targetParent string) error {
	// resolve download url
	dl, err := drive.GetDownloadURLContext(ctx, job.SrcUser, job.SrcDrive, srcFile.FileID, 3600)
	if err != nil {
		return err
	}
	// stream to temp
	tmp, err := os.CreateTemp("", "mnemo-migrate-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := downloadToCounted(ctx, dl, tmp, job); err != nil {
		return err
	}
	if stat, err := tmp.Stat(); err != nil {
		return err
	} else if srcFile.Size >= 0 && stat.Size() != srcFile.Size {
		return fmt.Errorf("migrate: size mismatch: expected %d, received %d", srcFile.Size, stat.Size())
	}
	_ = tmp.Sync()
	// upload to target
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	ui := &model.UploadingUI{
		UploadID: srcFile.FileID,
		Info: model.UploadInfo{
			LocalFilePath: tmp.Name(), ParentFileID: targetParent,
			DriveID: job.DstDrive, Name: srcFile.Name, Size: srcFile.Size,
			ConflictPolicy: driveutil.ConflictPolicyRename,
		},
	}
	handler, err := drive.QueueUploadHandlerContext(ctx, job.DstUser, job.DstDrive)
	if err != nil {
		return err
	}
	if err := handler(ctx, ui); err != nil {
		return err
	}
	if job.Items != nil {
		item := job.Items[srcFile.FileID]
		item.TargetID = ui.Upload.FileID
		job.Items[srcFile.FileID] = item
	}
	return nil
}

// finalizeMove deletes the source file in move mode. If deletion fails it
// returns a partialError so the caller records a partial (not completed)
// status and surfaces a message — the migration is effectively a copy in
// that case.
func (e *Engine) finalizeMove(ctx context.Context, job *Job, srcFile *model.File) error {
	if !job.Move {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if srcFile.IsDir {
		children, err := drive.ListDirAllContext(ctx, job.SrcUser, job.SrcDrive, srcFile.FileID, nil)
		if err != nil {
			return newPartialMigrationError(fmt.Errorf("清理来源前重新检查目录失败: %w", err), 1)
		}
		if len(children) != 0 {
			return newPartialMigrationError(errors.New("来源目录仍有文件，已保留目录，请核对新增或未迁移内容"), 1)
		}
	} else {
		current, err := drive.GetFileFreshContext(ctx, job.SrcUser, job.SrcDrive, srcFile.FileID)
		if err != nil {
			return newPartialMigrationError(fmt.Errorf("清理来源前重新检查文件失败: %w", err), 1)
		}
		if current == nil || current.IsDir || current.Size != srcFile.Size || current.Time != srcFile.Time || sourceHash(current) != sourceHash(srcFile) {
			return newPartialMigrationError(errors.New("来源文件在复制期间变化，已保留来源，请核对后重新迁移"), 1)
		}
	}
	isDir := srcFile.IsDir
	refs := []drive.FileRef{{ID: srcFile.FileID, IsDir: &isDir}}
	provider := drive.ProviderOf(job.SrcUser, job.SrcDrive, "")
	caps := drive.RegistryCaps(provider)
	var (
		removed []string
		err     error
	)
	if caps.RecycleBin {
		removed, err = drive.TrashBatchContext(ctx, job.SrcUser, job.SrcDrive, []string{srcFile.FileID})
	} else {
		removed, err = drive.DeleteBatchContext(ctx, job.SrcUser, job.SrcDrive, refs)
	}
	if err != nil {
		msg := fmt.Sprintf("move: source upload succeeded but source cleanup failed for %q: %v", srcFile.Name, err)
		return newMigrationError(partialError(msg), 1)
	}
	if !containsID(removed, srcFile.FileID) {
		msg := fmt.Sprintf("move: source cleanup returned no matching id for %q", srcFile.Name)
		return newMigrationError(partialError(msg), 1)
	}
	return nil
}

// migrateDir recursively migrates a folder.
func (e *Engine) migrateDir(ctx context.Context, job *Job, dir *model.File, targetParent string) error {
	// Reuse a target directory created by an earlier interrupted attempt. The
	// checkpoint is written immediately after Mkdir succeeds, before listing
	// children, so a retry cannot create a duplicate folder tree.
	if existing := strings.TrimSpace(job.TargetDirectoryIDs[dir.FileID]); existing != "" {
		targetParent = existing
	} else {
		mk, err := drive.MkdirContext(ctx, job.DstUser, job.DstDrive, targetParent, dir.Name)
		if err != nil {
			return newMigrationError(err, 1)
		}
		if mk == nil {
			return newMigrationError(errors.New("migrate: target folder creation returned empty result"), 1)
		}
		if strings.TrimSpace(mk.Error) != "" {
			return newMigrationError(fmt.Errorf("migrate: create target folder %q: %s", dir.Name, mk.Error), 1)
		}
		if strings.TrimSpace(mk.FileID) == "" {
			return newMigrationError(fmt.Errorf("migrate: create target folder %q returned empty id", dir.Name), 1)
		}
		targetParent = mk.FileID
		e.markTargetDirectory(job, dir.FileID, targetParent)
	}
	// list source dir
	children, err := drive.ListDirAllContext(ctx, job.SrcUser, job.SrcDrive, dir.FileID, nil)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return err
		}
		return newPartialMigrationError(fmt.Errorf("migrate: target folder %q was created but source listing failed: %w", dir.Name, err), 1)
	}
	var childFailures int64
	var lastChildErr error
	for i := range children {
		child := children[i]
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if jobHasID(job.CompletedFileIDs, child.FileID) {
			continue
		}
		if err := e.migrateOneTo(ctx, job, child.FileID, targetParent); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			childFailures += failureCount(err)
			lastChildErr = err
		}
		e.emit(job)
	}
	if childFailures > 0 {
		if lastChildErr == nil {
			lastChildErr = errors.New("migrate: one or more directory children failed")
		}
		return newPartialMigrationError(fmt.Errorf("migrate: directory %q was partially copied and has failed children: %w", dir.Name, lastChildErr), childFailures)
	}
	return nil
}

func (e *Engine) emit(job *Job) {
	if e.onProgress != nil {
		e.onProgress(job)
	}
}

// partialError marks an error whose cause is a post-migration source-delete
// failure. The file was copied successfully but the move is incomplete.
type partialError string

func (p partialError) Error() string { return string(p) }

func isPartialError(err error) bool {
	var p partialError
	return errors.As(err, &p)
}

type migrationError struct {
	err      error
	failures int64
}

func (e *migrationError) Error() string { return e.err.Error() }
func (e *migrationError) Unwrap() error { return e.err }

func newMigrationError(err error, failures int64) error {
	if err == nil {
		return nil
	}
	if failures < 1 {
		failures = 1
	}
	return &migrationError{err: err, failures: failures}
}

func newPartialMigrationError(err error, failures int64) error {
	if err == nil {
		return nil
	}
	return newMigrationError(partialError(err.Error()), failures)
}

func migrationResultStatus(failed, succeeded int64, partial bool) string {
	if failed == 0 {
		return "completed"
	}
	if succeeded > 0 || partial {
		return "partial"
	}
	return "failed"
}

func failureCount(err error) int64 {
	var me *migrationError
	if errors.As(err, &me) && me.failures > 0 {
		return me.failures
	}
	return 1
}

func containsID(ids []string, want string) bool {
	return jobHasID(ids, want)
}

func jobHasID(ids []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == want {
			return true
		}
	}
	return false
}

func completedTopLevelCount(job *Job) int64 {
	if job == nil {
		return 0
	}
	var completed int64
	for _, id := range job.FileIDs {
		if jobHasID(job.CompletedFileIDs, id) {
			completed++
		}
	}
	return completed
}

func removeJobID(ids []string, want string) []string {
	out := ids[:0]
	for _, id := range ids {
		if !jobHasID([]string{id}, want) {
			out = append(out, id)
		}
	}
	return out
}

// markCopied persists the point at which a destination resource is durable.
func (e *Engine) markCopied(job *Job, fileID string) error {
	if job == nil {
		return nil
	}
	if !jobHasID(job.CopiedFileIDs, fileID) {
		job.CopiedFileIDs = append(job.CopiedFileIDs, fileID)
	}
	job.UpdatedAt = time.Now().Unix()
	if err := e.saveJob(job); err != nil {
		return err
	}
	e.emit(job)
	return nil
}

func sourceHash(file *model.File) string { return file.ContentHashName + ":" + file.ContentHash }

// markCompleted persists a resource that no longer needs any transfer or
// source-cleanup work. It is deliberately called for nested files too.
func (e *Engine) markCompleted(job *Job, fileID string) {
	if job == nil || jobHasID(job.CompletedFileIDs, fileID) {
		return
	}
	job.CompletedFileIDs = append(job.CompletedFileIDs, fileID)
	job.CopiedFileIDs = removeJobID(job.CopiedFileIDs, fileID)
	job.UpdatedAt = time.Now().Unix()
	e.saveJob(job)
	e.emit(job)
}

func (e *Engine) markTargetDirectory(job *Job, sourceID, targetID string) {
	if job == nil || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(targetID) == "" {
		return
	}
	if job.TargetDirectoryIDs == nil {
		job.TargetDirectoryIDs = make(map[string]string)
	}
	if job.TargetDirectoryIDs[sourceID] == targetID {
		return
	}
	job.TargetDirectoryIDs[sourceID] = targetID
	job.UpdatedAt = time.Now().Unix()
	e.saveJob(job)
	e.emit(job)
}

// downloadTo streams a download url into a writer.
func downloadTo(ctx context.Context, dl *model.DownloadURL, w io.Writer) error {
	return downloadToCounted(ctx, dl, w, nil)
}

// downloadToCounted is like downloadTo but accumulates the byte count into
// job.ProcessedBytes when job is non-nil.
func downloadToCounted(ctx context.Context, dl *model.DownloadURL, w io.Writer, job *Job) error {
	if dl == nil || strings.TrimSpace(dl.URL) == "" {
		return errors.New("migrate: empty download url")
	}
	hc := netx.NewClient(0) // honors global proxy; no total timeout for large files
	req, err := hc.Req(ctx, http.MethodGet, dl.URL, nil)
	if err != nil {
		return err
	}
	for key, value := range dl.Headers {
		req.Header.Set(key, value)
	}
	if dl.RequestAuth != nil {
		if err := dl.RequestAuth(req); err != nil {
			return err
		}
	}
	resp, err := hc.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// This path requests the entire file, without a Range or cache validator.
	// Partial, pending, and bodyless responses must never become target files.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("migrate: http %d", resp.StatusCode)
	}
	var counter io.Writer = w
	if job != nil {
		counter = &countingWriter{w: w, job: job, emit: func() {}}
	}
	_, err = io.Copy(counter, resp.Body)
	return err
}

// countingWriter wraps a writer and increments job.ProcessedBytes.
type countingWriter struct {
	w    io.Writer
	job  *Job
	emit func()
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.job.ProcessedBytes += int64(n)
	}
	return n, err
}

// completeBytes commits exactly one file's worth of progress after its target
// upload succeeds. Download strategies may have already reported incremental
// bytes, so assigning the final value avoids double-counting on success.
func completeBytes(job *Job, start, size int64) {
	if job == nil {
		return
	}
	if size < 0 {
		size = 0
	}
	job.ProcessedBytes = start + size
}
