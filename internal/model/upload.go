package model

// UploadInfo describes the local file being uploaded.
type UploadInfo struct {
	LocalFilePath string `json:"localFilePath"`
	ParentFileID  string `json:"parent_file_id"`
	DriveID       string `json:"drive_id"`
	Path          string `json:"path"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	SizeStr       string `json:"sizeStr"`
	Icon          string `json:"icon"`
	IsDir         bool   `json:"isDir"`
	IsMiaoChuan   bool   `json:"isMiaoChuan"` // fingerprint秒传 flag
	SHA1          string `json:"sha1"`
	CRC64         string `json:"crc64"`

	// ConflictPolicy controls behavior when the target path already exists.
	// Supported: "" | "overwrite" (default, legacy) | "refuse" | "rename" | "skip".
	ConflictPolicy string `json:"conflictPolicy,omitempty"`
	// CleanupLocalFile marks an application-owned temporary source that may be
	// removed only after the remote upload has completed successfully.
	CleanupLocalFile bool `json:"cleanupLocalFile,omitempty"`
}

// UploadState is the mutable progress/status of an upload job.
type UploadState struct {
	DownState     string `json:"DownState"` // queued | uploading | completed | failed | stopped
	DownTime      int64  `json:"DownTime"`
	DownSize      int64  `json:"DownSize"`
	DownSpeed     int64  `json:"DownSpeed"`
	DownSpeedStr  string `json:"DownSpeedStr"`
	DownProcess   int    `json:"DownProcess"`
	IsStop        bool   `json:"IsStop"`
	IsDowning     bool   `json:"IsDowning"`
	IsCompleted   bool   `json:"IsCompleted"`
	IsFailed      bool   `json:"IsFailed"`
	FailedCode    int    `json:"failedCode"`
	FailedMessage string `json:"failedMessage"`
	AutoTry       int    `json:"AutoTry"`
	UploadID      string `json:"upload_id"`
	FileID        string `json:"file_id"`
	IsBreakExist  bool   `json:"IsBreakExist"`
}

// UploadingUI is the full upload job view model (mirrors legacy IUploadingUI).
type UploadingUI struct {
	UploadID string      `json:"UploadID"`
	UserID   string      `json:"user_id"`
	Info     UploadInfo  `json:"Info"`
	Upload   UploadState `json:"Upload"`

	progressReporter func(done int64, percent int) `json:"-"`
	stopRequested    func() bool                   `json:"-"`
}

// ConfigureUploadRuntime installs queue-owned callbacks on a worker-local
// upload view. Provider implementations must not retain the callbacks after
// UploadOneFile returns.
func (u *UploadingUI) ConfigureUploadRuntime(progress func(done int64, percent int), stopped func() bool) {
	if u == nil {
		return
	}
	u.progressReporter = progress
	u.stopRequested = stopped
}

// ReportUploadProgress updates the provider-local view and forwards an
// immutable progress sample to the queue. Keeping the provider view private
// avoids concurrent writes to the task object exposed to the UI.
func (u *UploadingUI) ReportUploadProgress(done, total int64) {
	if u == nil {
		return
	}
	if done < 0 {
		done = 0
	}
	percent := 0
	if total > 0 {
		if done > total {
			done = total
		}
		percent = int(done * 100 / total)
		if percent > 100 {
			percent = 100
		}
	} else if total == 0 && done == 0 {
		// A zero-byte file is complete as soon as the provider reports its
		// only progress sample.
		percent = 100
	}
	u.Upload.DownSize = done
	u.Upload.DownProcess = percent
	if u.progressReporter != nil {
		u.progressReporter(done, percent)
	}
}

// IsUploadStopRequested reports queue cancellation without requiring a
// provider to read the queue's shared mutable state.
func (u *UploadingUI) IsUploadStopRequested() bool {
	if u == nil {
		return true
	}
	if u.stopRequested != nil {
		return u.stopRequested()
	}
	return u.Upload.IsStop
}

// DownloadTask is a transfer-center download task.
type DownloadTask struct {
	ID          string            `json:"id"`
	UserID      string            `json:"user_id"`
	DriveID     string            `json:"drive_id"`
	Provider    string            `json:"provider"`
	FileID      string            `json:"file_id"`
	SourcePath  string            `json:"source_path,omitempty"`
	Name        string            `json:"name"`
	Size        int64             `json:"size"`
	Downloaded  int64             `json:"downloaded"`
	Speed       int64             `json:"speed"`
	Progress    int               `json:"progress"`
	Status      string            `json:"status"` // queued|downloading|paused|completed|failed|canceled
	LocalPath   string            `json:"localPath"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Error       string            `json:"error,omitempty"`
	Created     int64             `json:"created"`
	Updated     int64             `json:"updated"`
	Concurrency int               `json:"concurrency,omitempty"`
}

// OfflineTask is a provider-side (cloud) offline download task (PikPak).
type OfflineTask struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	DriveID     string `json:"drive_id"`
	TaskID      string `json:"task_id,omitempty"`
	FileID      string `json:"file_id,omitempty"`
	URL         string `json:"url,omitempty"`
	FileName    string `json:"file_name,omitempty"`
	Status      string `json:"status"`
	Progress    int    `json:"progress"`
	Message     string `json:"message,omitempty"`
	FileSize    int64  `json:"file_size,omitempty"`
	CreatedTime string `json:"created_time,omitempty"`
	UpdatedTime string `json:"updated_time,omitempty"`
	Created     int64  `json:"created"`
}

// MigrateJob is one cross-drive migration request.
type MigrateJob struct {
	Items     map[string]MigrateItem `json:"items,omitempty"`
	ID        string                 `json:"id"`
	SrcUser   string                 `json:"srcUser"`
	SrcDrive  string                 `json:"srcDrive"`
	FileIDs   []string               `json:"fileIDs"`
	DstUser   string                 `json:"dstUser"`
	DstDrive  string                 `json:"dstDrive"`
	DstParent string                 `json:"dstParent"`
	Move      bool                   `json:"move"`
	// Live progress
	Total     int64 `json:"total"`
	Processed int64 `json:"processed"`
	Failed    int64 `json:"failed"`
	// Byte-level progress (accumulated across files).
	TotalBytes     int64  `json:"totalBytes"`
	ProcessedBytes int64  `json:"processedBytes"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
	// Recovery checkpoints are persisted as each source resource reaches a
	// durable state. They let a later retry skip already-uploaded files and
	// reuse target directories instead of copying the whole tree again.
	//
	// CopiedFileIDs is only used by move jobs: the target copy exists but the
	// source cleanup has not finished yet. A retry then performs cleanup only.
	CompletedFileIDs   []string          `json:"completedFileIDs,omitempty"`
	CopiedFileIDs      []string          `json:"copiedFileIDs,omitempty"`
	TargetDirectoryIDs map[string]string `json:"targetDirectoryIDs,omitempty"`
	// Timestamps for persistence.
	CreatedAt int64 `json:"createdAt,omitempty"`
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

type MigrateItem struct {
	SourceRecorded bool   `json:"sourceRecorded,omitempty"`
	SourceTime     int64  `json:"sourceTime,omitempty"`
	SourceHash     string `json:"sourceHash,omitempty"`
	TargetID       string `json:"targetId,omitempty"`
	IsDir          bool   `json:"isDir"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	ParentID       string `json:"parentId"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Verification   string `json:"verification"`
	Size           int64  `json:"size"`
}
