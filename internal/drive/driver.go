// Package drive defines the provider plugin contract: every cloud drive
// provider implements the Driver interface and registers itself through
// drive.Register. The rest of the application talks only to this package and
// to the ops facade.
package drive

import (
	"context"
	"io"

	"mnemo-go/internal/model"
)

// Context carries the acting account identity into every driver call.
type Context struct {
	UserID    string           `json:"userId"`
	DriveID   string           `json:"driveId"`
	TokenFrom string           `json:"tokenfrom,omitempty"`
	Token     *model.TokenInfo `json:"token,omitempty"`
	// TokenSnapshot is kept outside JSON and lets the facade persist only real
	// session changes made during a provider operation.
	TokenSnapshot *model.TokenInfo `json:"-"`
}

// Account returns the context's account id (provider-namespace stripped).
func (c Context) AccountID() string {
	if c.UserID == "" {
		return ""
	}
	return model.StripUserID(c.TokenFrom, c.UserID)
}

// ListOptions controls listing behaviour.
type ListOptions struct {
	Force   bool   `json:"force"`
	Trashed bool   `json:"trashed"`
	Search  string `json:"search,omitempty"`
}

// DirPage is one cursor-paginated page of a directory listing.
type DirPage struct {
	Items      []model.File `json:"items"`
	NextMarker string       `json:"nextMarker,omitempty"`
	Total      int64        `json:"total,omitempty"`
}

// MkdirResult reports the created folder id (file_id empty = failed with error).
type MkdirResult struct {
	FileID string `json:"file_id"`
	Error  string `json:"error"`
}

// RenameResult reports the renamed entry.
type RenameResult struct {
	FileID       string `json:"file_id"`
	ParentFileID string `json:"parent_file_id"`
	Name         string `json:"name"`
	IsDir        bool   `json:"isDir"`
}

// ShareParams carries share creation parameters.
type ShareParams struct {
	FileIDs []string `json:"fileIds"`
	// FileRefs optionally preserves whether each id is a folder. Providers
	// that support mixed file/folder shares can use it; FileIDs remains the
	// required backwards-compatible source of ids.
	FileRefs   []FileRef `json:"fileRefs,omitempty"`
	ShareName  string    `json:"shareName"`
	Expiration string    `json:"expiration,omitempty"`
	Password   string    `json:"password,omitempty"`
}

// FileRef optionally distinguishes folder vs file for batch write ops where
// the provider needs the kind (e.g. ILanZou).
type FileRef struct {
	ID    string `json:"id"`
	IsDir *bool  `json:"isDir,omitempty"`
}

// RapidUploadRequest is the fingerprint-based秒传 request on the target drive.
type RapidUploadRequest struct {
	ParentID  string `json:"parentId"`
	FileName  string `json:"fileName"`
	Method    string `json:"method"` // md5 | sha1
	Hash      string `json:"hash"`
	Size      int64  `json:"size"`
	Duplicate int    `json:"duplicate,omitempty"` // 1 skip / 2 overwrite / 0 rename
}

// RapidUploadResult reports the outcome of a fingerprint秒传 attempt.
type RapidUploadResult struct {
	Reuse    bool   `json:"reuse"`
	FileID   string `json:"fileId,omitempty"`
	ParentID string `json:"parentId,omitempty"`
	Message  string `json:"message,omitempty"`
}

// ShareImportFile is one entry in a parsed share listing.
type ShareImportFile struct {
	FileID string `json:"fileId"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	IsDir  bool   `json:"isDir"`
}

// ShareImportSession is the parsed state of a share link: the file listing
// plus provider-specific session tokens needed for the subsequent save
// (transfer) call. It is created by ImportShareSession and consumed by
// SaveShare.
type ShareImportSession struct {
	Provider      string            `json:"provider"`
	ShareURL      string            `json:"shareUrl"`
	ShareID       string            `json:"shareId"`
	Password      string            `json:"password,omitempty"`
	PassCodeToken string            `json:"passCodeToken,omitempty"` // pikpak
	ShareToken    string            `json:"shareToken,omitempty"`    // aliopen
	ShareKey      string            `json:"shareKey,omitempty"`      // pan123
	RootFileID    string            `json:"rootFileId,omitempty"`
	Files         []ShareImportFile `json:"files"`
	Extra         map[string]string `json:"extra,omitempty"`
}

// ShareImportDriver is the optional capability interface for importing
// (parsing + saving) external share links. Providers implement this when
// Capabilities.ImportShare is true; callers detect support via a type
// assertion on the Driver instance.
type ShareImportDriver interface {
	// ImportShareSession parses a share link (with optional password) and
	// returns the file listing plus session state needed for SaveShare.
	ImportShareSession(ctx context.Context, c Context, shareURL, password string) (*ShareImportSession, error)
	// SaveShare transfers the selected file ids from a previously parsed
	// share session into the account's folder toParentID.
	SaveShare(ctx context.Context, c Context, session *ShareImportSession, fileIDs []string, toParentID string) ([]string, error)
}

// ShareCancellationDriver is the optional capability interface for revoking a
// share that was created by this application. A provider must implement it
// before advertising CancelCreatedShares; removing only the local history is
// deliberately not treated as a successful cancellation.
type ShareCancellationDriver interface {
	CancelShare(ctx context.Context, c Context, share model.ShareHistoryEntry) error
}

// UploadHandler carries a concrete file upload job into the provider.
type UploadHandler func(ctx context.Context, ui *model.UploadingUI) error

// UploadValidationItem is the local metadata a provider needs to validate an
// upload before it enters the asynchronous queue.
type UploadValidationItem struct {
	Name string
	Size int64
}

// UploadValidator is an optional provider hook for account-specific upload
// policies such as an allowed extension set or a single-file size limit.
// Providers must repeat the same check in UploadOneFile so non-UI entry points
// cannot bypass it.
type UploadValidator interface {
	ValidateUpload(ctx context.Context, c Context, name string, size int64) error
}

// StreamUploader is an optional capability interface that providers implement
// to accept an upload directly from an io.Reader, avoiding a local temp-file
// spool. When the target provider does not implement StreamUploader, the
// migration engine falls back to the spool (temp-file) path.
//
// size is the expected content length (-1 when unknown). parentID is the
// target folder id; name is the destination file name.
type StreamUploader interface {
	UploadStream(ctx context.Context, c Context, parentID, name string, size int64, reader io.Reader) error
}

// ConnectionValidator is an optional provider hook used by mounted-storage
// accounts to verify configuration before it is persisted.
type ConnectionValidator interface {
	ValidateConnection(ctx context.Context, conn *model.ConnConfig) error
}

// WriteConnectionValidator is an optional, explicit capability for mounted
// providers that can verify a write and remove a uniquely named test object.
// It must never be called as part of the normal login validation path.
type WriteConnectionValidator interface {
	ValidateWriteConnection(ctx context.Context, conn *model.ConnConfig) error
}

// RemoteFavorites is optional. A provider must support both reading and
// modifying its native favorites before opting in. Account-specific modes
// (for example personal vs business OneDrive) are resolved by the provider.
type RemoteFavorites interface {
	SupportsRemoteFavorites(ctx context.Context, c Context) (bool, error)
	ListFavorites(ctx context.Context, c Context) ([]model.File, error)
}

// Driver is the plugin contract every provider implements.
// Only List is required; everything else is capability-gated and optional.
type Driver interface {
	ID() string
	Meta() Meta
	Capabilities() Capabilities

	// RootID returns the canonical root folder id for this provider.
	RootID() string

	// List returns the entries of a directory. opts.Search triggers search.
	List(ctx context.Context, c Context, dirID string, opts *ListOptions) ([]model.File, error)
	// ListPaged optionally returns one cursor page (first frame faster).
	ListPaged(ctx context.Context, c Context, dirID, marker string, opts *ListOptions) (*DirPage, error)
	// ListTrash optionally lists the recycle bin.
	ListTrash(ctx context.Context, c Context, opts *ListOptions) ([]model.File, error)
	// Search optionally performs server-side search.
	Search(ctx context.Context, c Context, keyword string) ([]model.File, error)

	// GetInfo returns raw provider detail (untyped, provider specific).
	GetInfo(ctx context.Context, c Context, fileID string) (any, error)
	// GetFile returns the mapped unified file model.
	GetFile(ctx context.Context, c Context, fileID string) (*model.File, error)
	// GetDownloadURL resolves a (possibly authenticated) download source.
	GetDownloadURL(ctx context.Context, c Context, fileID string, expireSec int) (*model.DownloadURL, error)
	// GetVideoPreview resolves playback sources (qualities + subtitles).
	GetVideoPreview(ctx context.Context, c Context, fileID string) (*model.VideoPreview, error)

	Mkdir(ctx context.Context, c Context, parentID, name string) (*MkdirResult, error)
	Rename(ctx context.Context, c Context, fileID, name string) (*RenameResult, error)
	Trash(ctx context.Context, c Context, fileIDs []string) ([]string, error)
	Delete(ctx context.Context, c Context, refs []FileRef) ([]string, error)
	Restore(ctx context.Context, c Context, fileIDs []string) ([]string, error)
	Move(ctx context.Context, c Context, refs []FileRef, toParentID, toParentDesc string) ([]string, error)
	Copy(ctx context.Context, c Context, refs []FileRef, toParentID, toParentDesc string) ([]string, error)
	Favorite(ctx context.Context, c Context, fileIDs []string, favorite bool) ([]string, error)
	CreateShare(ctx context.Context, c Context, params ShareParams) (*model.ShareItem, error)

	// UploadOneFile performs a single queue/direct upload job.
	UploadOneFile(ctx context.Context, c Context, ui *model.UploadingUI) error
	// RapidUploadByHash creates a file by content fingerprint on the target drive.
	RapidUploadByHash(ctx context.Context, c Context, req RapidUploadRequest) (*RapidUploadResult, error)
	// ResolveTransferHash computes/reads a content fingerprint for migration.
	ResolveTransferHash(ctx context.Context, c Context, fileID, method string, allowStream bool) (string, error)

	// RefreshAccount refreshes the session; returns nil when unsupported.
	RefreshAccount(ctx context.Context, c Context, token *model.TokenInfo) (*model.TokenInfo, error)
}
