package drive

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"mnemo-go/internal/model"
)

// TokenResolver resolves the stored session for an account. It is wired by the
// app layer (which owns the store) to keep drive agnostic of persistence.
type TokenResolver func(userID, driveID string) (*model.TokenInfo, error)

var tokenResolver TokenResolver

// SetTokenResolver installs the store-backed token resolver (called once at
// startup by the app wiring).
func SetTokenResolver(fn TokenResolver) { tokenResolver = fn }

// TokenUpdater persists a provider session after an operation transparently
// refreshes or re-logins. The app layer owns the backing store.
type TokenUpdater func(userID, driveID string, token *model.TokenInfo) error

var tokenUpdater TokenUpdater

func SetTokenUpdater(fn TokenUpdater) { tokenUpdater = fn }

// OperationErrorHandler lets the app react to account-scoped provider errors
// without coupling the drive facade to persistence or UI events. Providers
// must wrap ErrUnauthorized only when credentials are definitively unusable.
type OperationErrorHandler func(userID, driveID string, err error)

var operationErrorHandler OperationErrorHandler

// SetOperationErrorHandler installs the application-owned account error hook.
func SetOperationErrorHandler(fn OperationErrorHandler) { operationErrorHandler = fn }

// CloneToken prevents concurrent provider calls from mutating the store's
// in-memory account object before the refreshed session is persisted.
func CloneToken(tok *model.TokenInfo) *model.TokenInfo {
	if tok == nil {
		return nil
	}
	out := *tok
	if tok.Raw != nil {
		out.Raw = append([]byte(nil), tok.Raw...)
	}
	if tok.Conn != nil {
		conn := *tok.Conn
		out.Conn = &conn
	}
	return &out
}

func persistToken(c Context) error {
	if tokenUpdater == nil || c.Token == nil {
		return nil
	}
	if c.TokenSnapshot != nil && reflect.DeepEqual(c.TokenSnapshot, c.Token) {
		return nil
	}
	return tokenUpdater(c.UserID, c.DriveID, CloneToken(c.Token))
}

func withTokenPersist(opErr error, c Context) error {
	opErr = classifyCodedAuthenticationError(opErr)
	result := errors.Join(opErr, persistToken(c))
	if opErr != nil && operationErrorHandler != nil {
		operationErrorHandler(c.UserID, c.DriveID, opErr)
	}
	return result
}

// classifyCodedAuthenticationError recognizes structured provider/SDK codes.
// It intentionally excludes generic HTTP 403 and codes such as AccessDenied:
// those can describe object policy or scope problems without invalidating the
// account. AWS Smithy API errors implement ErrorCode, so this also covers S3
// ExpiredToken/InvalidAccessKeyId without coupling the facade to the S3 SDK.
func classifyCodedAuthenticationError(err error) error {
	if err == nil || errors.Is(err, ErrUnauthorized) {
		return err
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(coded.ErrorCode())) {
	case "invalid_grant", "invalid_token", "expired_token", "expiredtoken", "invalidtoken", "invalidaccesskeyid", "tokenrefreshrequired":
		return errors.Join(AuthExpired("网盘登录凭据已失效"), err)
	default:
		return err
	}
}

// SecretResolver returns the OAuth client credentials for a provider by key
// (e.g. "onedrive_client_id", "dropbox_app_key"). It is wired by the app
// layer so providers can read secrets during RefreshAccount without depending
// on the config package.
type SecretResolver func(key string) string

var secretResolver SecretResolver

// SetSecretResolver installs the app-backed secret resolver (called once at
// startup by the app wiring).
func SetSecretResolver(fn SecretResolver) { secretResolver = fn }

// Secret returns the configured value for key, or "" when unset.
func Secret(key string) string {
	if secretResolver == nil {
		return ""
	}
	return secretResolver(key)
}

// ---- Upload session persistence (resumable uploads) ----

// UploadSessionStore abstracts per-key upload session persistence so the
// drive package stays free of store imports. The app layer wires it at
// startup via SetUploadSessionStore.
type UploadSessionStore interface {
	SaveUploadSession(key string, partNumbers []int) error
	LoadUploadSession(key string) []int
	ClearUploadSession(key string)
}

type uploadSessionStateStore interface {
	SaveUploadSessionState(key, sessionID string, partNumbers []int) error
	LoadUploadSessionState(key string) (string, []int)
}

var uploadSessionStore UploadSessionStore
var uploadSessionState uploadSessionStateStore

// SetUploadSessionStore installs the store-backed upload session persistence
// (called once at startup by the app wiring).
func SetUploadSessionStore(s UploadSessionStore) {
	uploadSessionStore = s
	if state, ok := s.(uploadSessionStateStore); ok {
		uploadSessionState = state
	} else {
		uploadSessionState = nil
	}
}

// SaveUploadSession persists uploaded part numbers for a session key.
func SaveUploadSession(key string, partNumbers []int) error {
	if uploadSessionStore == nil {
		return nil
	}
	return uploadSessionStore.SaveUploadSession(key, partNumbers)
}

// SaveUploadSessionState persists a provider session id with completed parts.
func SaveUploadSessionState(key, sessionID string, partNumbers []int) error {
	if uploadSessionState != nil {
		return uploadSessionState.SaveUploadSessionState(key, sessionID, partNumbers)
	}
	return SaveUploadSession(key, partNumbers)
}

// LoadUploadSession returns the persisted uploaded part numbers for a key,
// or nil when no session exists.
func LoadUploadSession(key string) []int {
	if uploadSessionStore == nil {
		return nil
	}
	return uploadSessionStore.LoadUploadSession(key)
}

// LoadUploadSessionState loads a provider session id and completed parts.
func LoadUploadSessionState(key string) (string, []int) {
	if uploadSessionState != nil {
		return uploadSessionState.LoadUploadSessionState(key)
	}
	return "", LoadUploadSession(key)
}

// ClearUploadSession removes the persisted session for a key.
func ClearUploadSession(key string) {
	if uploadSessionStore == nil {
		return
	}
	uploadSessionStore.ClearUploadSession(key)
}

// UploadSessionKey computes a stable hash key from the tuple
// userID:driveID:parentID:name:size, suitable for deduplicating resume state.
func UploadSessionKey(userID, driveID, parentID, name string, size int64) string {
	raw := userID + ":" + driveID + ":" + parentID + ":" + name + ":" + formatSize(size)
	h := sha1.Sum([]byte(raw))
	return hex.EncodeToString(h[:])
}

func formatSize(n int64) string { return strconv.FormatInt(n, 10) }

// SortedUniqueParts deduplicates and sorts part numbers for stable persistence.
func SortedUniqueParts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// BuildContext resolves the provider + session for an account.
func BuildContext(userID, driveID, tokenFrom string) (Context, error) {
	provider := ResolveProvider(userID, driveID, tokenFrom)
	if provider == model.ProviderUnknown {
		return Context{}, ErrUnknownProvider
	}
	c := Context{UserID: userID, DriveID: driveID, TokenFrom: provider}
	if tokenResolver != nil {
		tok, err := tokenResolver(userID, driveID)
		if err != nil {
			return Context{}, fmt.Errorf("drive: load account session: %w", err)
		}
		if tok != nil {
			c.Token = CloneToken(tok)
			c.TokenSnapshot = CloneToken(tok)
			if c.TokenFrom == "" || c.TokenFrom == model.ProviderUnknown {
				c.TokenFrom = tok.TokenFrom
			}
		}
	}
	return c, nil
}

// DriverFor returns a configured driver instance for an account context.
func DriverFor(c Context) (Driver, error) {
	reg, ok := Get(c.TokenFrom)
	if !ok {
		// fall back to user id prefix resolution if tokenfrom not filled
		provider := ResolveProvider(c.UserID, c.DriveID, c.TokenFrom)
		reg, ok = Get(provider)
		if !ok {
			return nil, ErrUnknownProvider
		}
		c.TokenFrom = provider
	}
	return reg.Factory(), nil
}

func driverAndCtx(userID, driveID string) (Driver, Context, error) {
	c, err := BuildContext(userID, driveID, "")
	if err != nil {
		return nil, c, err
	}
	d, err := DriverFor(c)
	if err != nil {
		return nil, c, err
	}
	return d, c, nil
}

// ListDir lists a directory (search when opts.Search is set).
func ListDir(userID, driveID, dirID string, opts *ListOptions) (files []model.File, err error) {
	return ListDirContext(context.Background(), userID, driveID, dirID, opts)
}

// ListDirContext is the cancellation-aware variant used by long-running
// workers. The Wails-compatible ListDir wrapper remains for existing callers.
func ListDirContext(ctx context.Context, userID, driveID, dirID string, opts *ListOptions) (files []model.File, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	if opts != nil && opts.Search != "" {
		return d.Search(ctx, c, opts.Search)
	}
	return d.List(ctx, c, dirID, opts)
}

// ListDirPage lists one cursor page.
func ListDirPage(userID, driveID, dirID, marker string, opts *ListOptions) (page *DirPage, err error) {
	return ListDirPageContext(context.Background(), userID, driveID, dirID, marker, opts)
}

// ListDirPageContext is the cancellation-aware cursor-page variant.
func ListDirPageContext(ctx context.Context, userID, driveID, dirID, marker string, opts *ListOptions) (page *DirPage, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.ListPaged(ctx, c, dirID, marker, opts)
}

// ListDirAll consumes cursor pages when a provider exposes pagination. The
// guard prevents a broken provider cursor from hanging a migration forever.
func ListDirAll(userID, driveID, dirID string, opts *ListOptions) ([]model.File, error) {
	return ListDirAllContext(context.Background(), userID, driveID, dirID, opts)
}

// ListDirAllContext consumes cursor pages while honoring cancellation.
func ListDirAllContext(ctx context.Context, userID, driveID, dirID string, opts *ListOptions) ([]model.File, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var out []model.File
	marker := ""
	seen := map[string]bool{}
	for page := 0; page < 10000; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := ListDirPageContext(ctx, userID, driveID, dirID, marker, opts)
		if err != nil {
			if page == 0 {
				return ListDirContext(ctx, userID, driveID, dirID, opts)
			}
			return nil, err
		}
		if p == nil {
			return out, nil
		}
		out = append(out, p.Items...)
		if p.NextMarker == "" {
			return out, nil
		}
		if seen[p.NextMarker] {
			return nil, errors.New("drive: duplicate list cursor")
		}
		seen[p.NextMarker] = true
		marker = p.NextMarker
	}
	return nil, errors.New("drive: list pagination exceeded limit")
}

// SearchDir performs server-side search.
func SearchDir(userID, driveID, keyword string) (files []model.File, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Search(context.Background(), c, keyword)
}

// ListTrash lists the recycle bin.
func ListTrash(userID, driveID string, opts *ListOptions) (files []model.File, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.ListTrash(context.Background(), c, opts)
}

// GetFileInfo returns raw provider detail.
func GetFileInfo(userID, driveID, fileID string) (info any, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.GetInfo(context.Background(), c, fileID)
}

// RefreshAccount refreshes an account's quota + profile from the provider and
// returns the updated token (or the original on unsupported/error). Silent +
// low-frequency caller (frontend avatar/quota popover refresh).
func RefreshAccount(userID, driveID string) (token *model.TokenInfo, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	tok := c.Token
	if tok == nil {
		return nil, ErrUnknownProvider
	}
	token, err = d.RefreshAccount(context.Background(), c, tok)
	if token != nil && token != c.Token {
		c.Token = token
	}
	err = withTokenPersist(err, c)
	return token, err
}

// ValidateUploadItems applies an optional provider upload policy before local
// files are placed in the asynchronous queue. Providers without a policy are
// intentionally a no-op.
func ValidateUploadItems(userID, driveID string, items []UploadValidationItem) (err error) {
	if len(items) == 0 {
		return nil
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return err
	}
	defer func() { err = withTokenPersist(err, c) }()
	validator, ok := d.(UploadValidator)
	if !ok {
		return nil
	}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return errors.New("drive: 上传文件名为空")
		}
		if item.Size < 0 {
			return fmt.Errorf("drive: 上传文件大小无效: %s", name)
		}
		if validationErr := validator.ValidateUpload(context.Background(), c, name, item.Size); validationErr != nil {
			return validationErr
		}
	}
	return nil
}

// ValidateConnection lets mounted-storage providers verify a connection
// before the app persists it as an account.
func ValidateConnection(provider string, conn *model.ConnConfig) error {
	d, ok := Get(provider)
	if !ok {
		return ErrUnknownProvider
	}
	validator, ok := d.Factory().(ConnectionValidator)
	if !ok {
		return NotSupported("validateConnection")
	}
	return validator.ValidateConnection(context.Background(), conn)
}

// ValidateWriteConnection performs an explicitly requested mounted-storage
// write check. Providers must use an isolated temporary object and clean it up
// before returning; this is intentionally separate from login validation.
func ValidateWriteConnection(provider string, conn *model.ConnConfig) error {
	d, ok := Get(provider)
	if !ok {
		return ErrUnknownProvider
	}
	validator, ok := d.Factory().(WriteConnectionValidator)
	if !ok {
		return NotSupported("validateWriteConnection")
	}
	return validator.ValidateWriteConnection(context.Background(), conn)
}

// GetFile returns the unified file model (from cache if present).
func GetFile(userID, driveID, fileID string) (file *model.File, err error) {
	return GetFileContext(context.Background(), userID, driveID, fileID)
}

// GetFileContext resolves a file while honoring cancellation.
func GetFileContext(ctx context.Context, userID, driveID, fileID string) (file *model.File, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f, ok := fileCache.Get(userID, driveID, fileID); ok {
		return &f, nil
	}
	return GetFileFreshContext(ctx, userID, driveID, fileID)
}

// GetFileFreshContext bypasses the UI snapshot cache for verification and
// destructive-operation checks. Provider errors must never fall back to cache.
func GetFileFreshContext(ctx context.Context, userID, driveID, fileID string) (file *model.File, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached, ok := fileCache.Get(userID, driveID, fileID); ok && cached.ParentFileID != "" {
		files, err := ListDirAllContext(ctx, userID, driveID, cached.ParentFileID, nil)
		if err != nil {
			return nil, err
		}
		for _, candidate := range files {
			if candidate.FileID == fileID || candidate.IsDir && strings.TrimRight(candidate.FileID, "/") == strings.TrimRight(fileID, "/") {
				remember(userID, driveID, fileID, &candidate)
				return &candidate, nil
			}
		}
		return nil, ErrNotFound
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	file, err = d.GetFile(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	remember(userID, driveID, fileID, file)
	return file, nil
}

// GetDownloadURL resolves a download source.
func GetDownloadURL(userID, driveID, fileID string, expireSec int) (url *model.DownloadURL, err error) {
	return GetDownloadURLContext(context.Background(), userID, driveID, fileID, expireSec)
}

// GetDownloadURLContext is the cancellation-aware download URL resolver.
func GetDownloadURLContext(ctx context.Context, userID, driveID, fileID string, expireSec int) (url *model.DownloadURL, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.GetDownloadURL(ctx, c, fileID, expireSec)
}

// GetVideoPreview resolves playback sources.
func GetVideoPreview(userID, driveID, fileID string) (preview *model.VideoPreview, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.GetVideoPreview(context.Background(), c, fileID)
}

// Mkdir creates a folder.
func Mkdir(userID, driveID, parentID, name string) (result *MkdirResult, err error) {
	return MkdirContext(context.Background(), userID, driveID, parentID, name)
}

// MkdirContext is the cancellation-aware folder creation variant.
func MkdirContext(ctx context.Context, userID, driveID, parentID, name string) (result *MkdirResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Mkdir(ctx, c, parentID, name)
}

// RenameBatch renames files one-to-one with names.
func RenameBatch(userID, driveID string, fileRefs []FileRef, names []string) (out []RenameResult, err error) {
	if len(fileRefs) != len(names) {
		return nil, errors.New("drive: rename refs and names length mismatch")
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	out = make([]RenameResult, 0, len(fileRefs))
	var renameErrs []error
	for i, ref := range fileRefs {
		name := names[i]
		r, err := d.Rename(context.Background(), c, ref.ID, name)
		if err != nil {
			out = append(out, RenameResult{FileID: "", ParentFileID: "", Name: name})
			renameErrs = append(renameErrs, fmt.Errorf("file %s: %w", ref.ID, err))
			continue
		}
		if r != nil {
			out = append(out, *r)
			continue
		}
		out = append(out, RenameResult{FileID: "", ParentFileID: "", Name: name})
		renameErrs = append(renameErrs, fmt.Errorf("file %s: provider returned an empty rename result", ref.ID))
	}
	return out, errors.Join(renameErrs...)
}

// TrashBatch moves files to the recycle bin.
func TrashBatch(userID, driveID string, fileIDs []string) (ids []string, err error) {
	return TrashBatchContext(context.Background(), userID, driveID, fileIDs)
}

// TrashBatchContext is the cancellation-aware recycle-bin variant.
func TrashBatchContext(ctx context.Context, userID, driveID string, fileIDs []string) (ids []string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Trash(ctx, c, fileIDs)
}

// DeleteBatch permanently deletes files (skips trash where implemented).
func DeleteBatch(userID, driveID string, fileIDs []FileRef) (ids []string, err error) {
	return DeleteBatchContext(context.Background(), userID, driveID, fileIDs)
}

// DeleteBatchContext permanently deletes files while honoring cancellation.
func DeleteBatchContext(ctx context.Context, userID, driveID string, fileIDs []FileRef) (ids []string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Delete(ctx, c, fileIDs)
}

// RestoreBatch restores files from the recycle bin.
func RestoreBatch(userID, driveID string, fileIDs []string) (ids []string, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Restore(context.Background(), c, fileIDs)
}

// MoveBatch moves files to a target folder.
func MoveBatch(userID, driveID string, fileIDs []FileRef, toParentID, toParentDesc string) (ids []string, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Move(context.Background(), c, fileIDs, toParentID, toParentDesc)
}

// CopyBatch copies files into a target folder.
func CopyBatch(userID, driveID string, fileIDs []FileRef, toParentID, toParentDesc string) (ids []string, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Copy(context.Background(), c, fileIDs, toParentID, toParentDesc)
}

// ListRemoteFavorites distinguishes unsupported providers from failed requests.
func ListRemoteFavorites(ctx context.Context, userID, driveID string) (files []model.File, supported bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	p, ok := d.(RemoteFavorites)
	if !ok {
		return nil, false, nil
	}
	supported, err = p.SupportsRemoteFavorites(ctx, c)
	if err != nil || !supported {
		return nil, supported, err
	}
	files, err = p.ListFavorites(ctx, c)
	return
}

// SetRemoteFavorite returns supported=false only when the provider/account
// explicitly has no native favorites. Network or permission errors never
// silently downgrade an operation to local-only success.
func SetRemoteFavorite(ctx context.Context, userID, driveID string, favorite bool, fileID string) (supported bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(fileID) == "" {
		return false, errors.New("收藏文件 ID 不能为空")
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return false, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	p, ok := d.(RemoteFavorites)
	if !ok {
		return false, nil
	}
	supported, err = p.SupportsRemoteFavorites(ctx, c)
	if err != nil || !supported {
		return supported, err
	}
	ids, err := d.Favorite(ctx, c, []string{fileID}, favorite)
	if err != nil {
		return true, err
	}
	for _, id := range ids {
		if id == fileID {
			return true, nil
		}
	}
	return true, fmt.Errorf("云端未确认文件 %s 的收藏操作", fileID)
}

// FavoriteBatch is the legacy remote-only facade. Local fallback belongs to
// the app layer, which has access to file metadata and persistent bookmarks.
func FavoriteBatch(userID, driveID string, favorite bool, fileIDs []string) (ids []string, err error) {
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.Favorite(context.Background(), c, fileIDs, favorite)
}

// CreateShare creates a share.
func CreateShare(userID, driveID string, params ShareParams) (share *model.ShareItem, err error) {
	if len(params.FileIDs) == 0 {
		return nil, errors.New("drive: 创建分享至少选择一个文件")
	}
	for _, fileID := range params.FileIDs {
		if strings.TrimSpace(fileID) == "" {
			return nil, errors.New("drive: 分享文件 ID 不能为空")
		}
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.CreateShare(context.Background(), c, params)
}

// CancelShare revokes a provider-managed share. Providers that only generate
// an expiring URL must not advertise this capability because such URLs cannot
// be individually revoked once issued.
func CancelShare(userID, driveID string, share model.ShareHistoryEntry) (err error) {
	if strings.TrimSpace(share.AccountID) == "" {
		share.AccountID = userID
	}
	if strings.TrimSpace(share.DriveID) == "" {
		share.DriveID = driveID
	}
	if strings.TrimSpace(share.ShareID) == "" && strings.TrimSpace(share.ShareURL) == "" {
		return errors.New("drive: 分享标识不能为空")
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return err
	}
	if !d.Capabilities().CancelCreatedShares {
		return errors.New("该网盘不支持取消已创建的分享")
	}
	canceller, ok := d.(ShareCancellationDriver)
	if !ok {
		return errors.New("该网盘未实现分享取消接口")
	}
	defer func() { err = withTokenPersist(err, c) }()
	return canceller.CancelShare(context.Background(), c, share)
}

// RapidUploadByHash attempts fingerprint秒传 on the target drive.
func RapidUploadByHash(userID, driveID string, req RapidUploadRequest) (result *RapidUploadResult, err error) {
	return RapidUploadByHashContext(context.Background(), userID, driveID, req)
}

// RapidUploadByHashContext attempts fingerprint upload while honoring cancellation.
func RapidUploadByHashContext(ctx context.Context, userID, driveID string, req RapidUploadRequest) (result *RapidUploadResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.RapidUploadByHash(ctx, c, req)
}

// ResolveTransferHash computes/reads a content fingerprint.
func ResolveTransferHash(userID, driveID, fileID, method string, allowStream bool) (hash string, err error) {
	return ResolveTransferHashContext(context.Background(), userID, driveID, fileID, method, allowStream)
}

// ResolveTransferHashContext computes a transfer fingerprint while honoring cancellation.
func ResolveTransferHashContext(ctx context.Context, userID, driveID, fileID, method string, allowStream bool) (hash string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return "", err
	}
	defer func() { err = withTokenPersist(err, c) }()
	return d.ResolveTransferHash(ctx, c, fileID, method, allowStream)
}

// QueueUploadHandler returns the driver's single-file upload handler for an
// account, or nil when the provider exposes none.
func QueueUploadHandler(userID, driveID string) (func(ctx context.Context, ui *model.UploadingUI) error, error) {
	return QueueUploadHandlerContext(context.Background(), userID, driveID)
}

// QueueUploadHandlerContext resolves a provider handler while honoring the
// caller's setup context. The returned handler continues to receive its own
// operation context as before.
func QueueUploadHandlerContext(ctx context.Context, userID, driveID string) (func(context.Context, *model.UploadingUI) error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, ui *model.UploadingUI) error {
		opErr := d.UploadOneFile(ctx, c, ui)
		return withTokenPersist(opErr, c)
	}, nil
}

// StreamUploadHandler returns a function that streams an upload from an
// io.Reader into the target provider, or (nil, ErrNotImplemented) when the
// provider does not implement the StreamUploader capability.
func StreamUploadHandler(userID, driveID string) (func(ctx context.Context, parentID, name string, size int64, reader io.Reader) error, error) {
	return StreamUploadHandlerContext(context.Background(), userID, driveID)
}

// StreamUploadHandlerContext resolves a streaming upload handler while honoring setup cancellation.
func StreamUploadHandlerContext(ctx context.Context, userID, driveID string) (func(context.Context, string, string, int64, io.Reader) error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	su, ok := d.(StreamUploader)
	if !ok {
		return nil, ErrNotImplemented
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, parentID, name string, size int64, reader io.Reader) error {
		opErr := su.UploadStream(ctx, c, parentID, name, size, reader)
		return withTokenPersist(opErr, c)
	}, nil
}

// ImportShare parses a share link and returns the file listing + session
// state. The session must be passed back to SaveImportedShare to transfer
// selected files. Returns ErrNotImplemented when the provider does not
// support share import.
func ImportShare(userID, driveID, shareURL, password string) (session *ShareImportSession, err error) {
	if strings.TrimSpace(shareURL) == "" {
		return nil, errors.New("drive: 分享链接不能为空")
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	sd, ok := d.(ShareImportDriver)
	if !ok {
		return nil, ErrNotImplemented
	}
	return sd.ImportShareSession(context.Background(), c, shareURL, password)
}

// SaveImportedShare transfers selected files from a parsed share session
// into the account's folder toParentID. Returns the provider-side ids of
// the saved files.
func SaveImportedShare(userID, driveID string, session *ShareImportSession, fileIDs []string, toParentID string) (ids []string, err error) {
	if session == nil || strings.TrimSpace(session.Provider) == "" {
		return nil, errors.New("drive: 分享会话无效")
	}
	if len(fileIDs) == 0 {
		return nil, errors.New("drive: 至少选择一个分享文件")
	}
	for _, fileID := range fileIDs {
		if strings.TrimSpace(fileID) == "" {
			return nil, errors.New("drive: 分享文件 ID 不能为空")
		}
	}
	d, c, err := driverAndCtx(userID, driveID)
	if err != nil {
		return nil, err
	}
	defer func() { err = withTokenPersist(err, c) }()
	sd, ok := d.(ShareImportDriver)
	if !ok {
		return nil, ErrNotImplemented
	}
	if session.Provider != c.TokenFrom {
		return nil, fmt.Errorf("drive: 分享来源网盘与目标账号不匹配")
	}
	allowed := make(map[string]struct{}, len(session.Files))
	for _, file := range session.Files {
		if file.FileID != "" {
			allowed[file.FileID] = struct{}{}
		}
	}
	for _, fileID := range fileIDs {
		if _, exists := allowed[fileID]; !exists {
			return nil, fmt.Errorf("drive: 分享文件不属于当前分享会话: %s", fileID)
		}
	}
	return sd.SaveShare(context.Background(), c, session, fileIDs, toParentID)
}

// RootID returns the provider root id for an account.
func RootID(userID, driveID string) (string, error) {
	provider := ResolveProvider(userID, driveID, "")
	if m := GetMeta(provider); m.RootKey != "" {
		return m.RootKey, nil
	}
	return provider + "_root", nil
}

// ProviderOf returns the resolved provider for an account context.
func ProviderOf(userID, driveID, tokenFrom string) string {
	return ResolveProvider(userID, driveID, tokenFrom)
}
