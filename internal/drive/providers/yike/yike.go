// Package yike implements the 一刻相册 (Baidu Photo) provider.
package yike

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const (
	apiBase  = "https://photo.baidu.com/youai"
	userAPI  = apiBase + "/user/v1"
	albumAPI = apiBase + "/album/v1"
	fileV1   = apiBase + "/file/v1"
	fileV2   = apiBase + "/file/v2"
	ua       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	RootID   = "yike_root"
)

const providerID = model.ProviderYike

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"createFolder":     true,
			"createDateFolder": false,
			"photoAlbum":       true,
			"recycleBin":       true,
			"permanentDelete":  false,
			"trashView":        true,
			"trashRestore":     true,
			"move":             false,
			"copy":             false,
		}, nil),
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "cookie", Type: "text", Label: "BDUSS / Cookie", Required: true, Placeholder: "粘贴 BDUSS 或完整 Cookie"},
		}},
		Auth:    authLogin,
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// Session is the yike credential set.
type Session struct {
	Cookie   string `json:"cookie"`
	UK       string `json:"uk,omitempty"`
	Bdstoken string `json:"bdstoken,omitempty"`
	YouaID   string `json:"youaId,omitempty"`
}

// client is an authenticated yike session.
type client struct {
	http  *netx.Client
	sess  *Session
	limit time.Duration
}

func clientOf(c drive.Context) (*client, error) {
	if c.Token == nil {
		return nil, drive.ErrUnauthorized
	}
	sess := parseSession(c.Token)
	if sess == nil || sess.Cookie == "" {
		return nil, drive.AuthExpired("一刻相册 Cookie 无效，请重新登录")
	}
	return &client{http: netx.NewClient(60 * time.Second), sess: sess, limit: 300 * time.Millisecond}, nil
}

func parseSession(tok *model.TokenInfo) *Session {
	if tok == nil {
		return nil
	}
	var s Session
	if tok.RefreshToken != "" {
		if err := json.Unmarshal([]byte(tok.RefreshToken), &s); err == nil && s.Cookie != "" {
			return &s
		}
	}
	cookie := normalizeCookie(tok.AccessToken)
	if cookie != "" {
		return &Session{Cookie: cookie}
	}
	return nil
}

func normalizeCookie(raw string) string {
	c := strings.TrimSpace(raw)
	if c == "" {
		return ""
	}
	if !strings.Contains(c, "=") && len(c) >= 16 {
		return "BDUSS=" + c
	}
	return c
}

// throttle enforces the 300ms per-request interval.
func (c *client) throttle() { time.Sleep(c.limit) }

func (c *client) headers() map[string]string {
	return map[string]string{
		"Cookie": c.sess.Cookie, "User-Agent": ua,
		"Referer": "https://photo.baidu.com/", "Accept": "application/json, text/plain, */*",
	}
}

// request performs an API call, auto-refreshing bdstoken on first need.
func (c *client) request(ctx context.Context, method, rawURL string, query url.Values) (json.RawMessage, error) {
	return c.do(ctx, method, rawURL, query, nil)
}

// requestForm posts a form-urlencoded body（上传 precreate/create 用，对齐旧版 form 选项）。
func (c *client) requestForm(ctx context.Context, rawURL string, query url.Values, form url.Values) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, rawURL, query, form)
}

func (c *client) do(ctx context.Context, method, rawURL string, query, form url.Values) (json.RawMessage, error) {
	c.throttle()
	if query != nil {
		rawURL += "?" + query.Encode()
	}
	headers := c.headers()
	var formBody io.Reader
	if form != nil {
		headers["content-type"] = "application/x-www-form-urlencoded"
		formBody = strings.NewReader(form.Encode())
	}
	resp, err := c.http.Do(ctx, method, rawURL, headers, formBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var wrapper struct {
		Errno    int    `json:"errno"`
		Errmsg   string `json:"errmsg"`
		ErrorMsg string `json:"error_msg"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("一刻相册 API 响应无效: %w", err)
	}
	errno := wrapper.Errno
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if errno == -6 || errno == 111 {
			return nil, drive.AuthExpired("一刻登录已失效，请重新粘贴 BDUSS/Cookie")
		}
		return nil, fmt.Errorf("一刻相册 API HTTP %d", resp.StatusCode)
	}
	if errno == 0 {
		return body, nil
	}
	if errno == -6 || errno == 111 {
		return nil, drive.AuthExpired("一刻登录已失效，请重新粘贴 BDUSS/Cookie")
	}
	msg := wrapper.Errmsg
	if msg == "" {
		msg = wrapper.ErrorMsg
	}
	if msg == "" {
		msg = fmt.Sprintf("yike: errno %d", errno)
	}
	return nil, errors.New(msg)
}

func (c *client) getuinfo(ctx context.Context) error {
	body, err := c.request(ctx, http.MethodGet, userAPI+"/getuinfo", nil)
	if err != nil {
		return err
	}
	var u struct {
		YouaID string `json:"youa_id"`
		UK     string `json:"uk"`
	}
	_ = json.Unmarshal(body, &u)
	c.sess.YouaID = u.YouaID
	c.sess.UK = u.UK
	return nil
}

func (c *client) getBdstoken(ctx context.Context) error {
	u := "https://pan.baidu.com/api/gettemplatevariable?fields=[%22bdstoken%22,%22token%22,%22uk%22]"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	resp, err := c.http.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Result struct {
			Bdstoken string `json:"bdstoken"`
		} `json:"result"`
		Bdstoken string `json:"bdstoken"`
	}
	_ = json.Unmarshal(body, &r)
	tok := r.Result.Bdstoken
	if tok == "" {
		tok = r.Bdstoken
	}
	if tok == "" {
		return errors.New("获取 bdstoken 失败")
	}
	c.sess.Bdstoken = tok
	return nil
}

// File is a raw yike item.
type File struct {
	Kind     string `json:"kind"` // album | file | albumfile
	FsID     string `json:"fsid"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MD5      string `json:"md5"`
	Mtime    int64  `json:"mtime"`
	Ctime    int64  `json:"ctime"`
	ThumbURL string `json:"thumburl"`
	AlbumID  string `json:"album_id"`
}

// listRoot returns albums + loose files.
func (c *client) listRoot(ctx context.Context) ([]File, error) {
	var out []File
	cursor := ""
	for {
		q := url.Values{}
		q.Set("need_amount", "1")
		q.Set("limit", "100")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		body, err := c.request(ctx, http.MethodGet, albumAPI+"/list", q)
		if err != nil {
			return nil, err
		}
		var r struct {
			List   []map[string]any `json:"list"`
			Cursor string           `json:"cursor"`
		}
		_ = json.Unmarshal(body, &r)
		for _, a := range r.List {
			id := str(first(a, "album_id", "albumId"))
			out = append(out, File{Kind: "album", FsID: id, AlbumID: id, Name: str(first(a, "title", "name")), Mtime: int64(num(a, "mtime")), ThumbURL: str(first(a, "cover_thumburl", "cover"))})
		}
		if r.Cursor == "" || r.Cursor == cursor {
			break
		}
		cursor = r.Cursor
	}
	// loose files
	cursor = ""
	for {
		q := url.Values{}
		q.Set("need_thumbnail", "1")
		q.Set("need_filter_hidden", "0")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		body, err := c.request(ctx, http.MethodGet, fileV1+"/list", q)
		if err != nil {
			return nil, err
		}
		var r struct {
			List   []map[string]any `json:"list"`
			Cursor string           `json:"cursor"`
		}
		_ = json.Unmarshal(body, &r)
		for _, f := range r.List {
			fsid := str(first(f, "fsid", "fs_id"))
			out = append(out, File{
				Kind: "file", FsID: fsid, Path: str(f["path"]),
				Name: fileNameFromPath(str(f["path"]), fsid), Size: int64(numVal(f["size"])),
				MD5: str(f["md5"]), Mtime: int64(numVal(first(f, "mtime", "server_mtime"))),
				ThumbURL: str(first(f, "thumburl", "thumbnail")),
			})
		}
		if r.Cursor == "" || r.Cursor == cursor {
			break
		}
		cursor = r.Cursor
	}
	return out, nil
}

// listAlbum returns files inside an album.
func (c *client) listAlbum(ctx context.Context, albumID string) ([]File, error) {
	var out []File
	cursor := ""
	for {
		q := url.Values{}
		q.Set("album_id", albumID)
		q.Set("need_amount", "1")
		q.Set("limit", "1000")
		q.Set("passwd", "")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		body, err := c.request(ctx, http.MethodGet, albumAPI+"/listfile", q)
		if err != nil {
			return nil, err
		}
		var r struct {
			List   []map[string]any `json:"list"`
			Cursor string           `json:"cursor"`
		}
		_ = json.Unmarshal(body, &r)
		for _, f := range r.List {
			fsid := str(first(f, "fsid", "fs_id"))
			out = append(out, File{
				Kind: "albumfile", FsID: fsid, AlbumID: albumID, Path: str(f["path"]),
				Name: fileNameFromPath(str(f["path"]), fsid), Size: int64(numVal(f["size"])),
				MD5: str(f["md5"]), Mtime: int64(numVal(f["mtime"])), ThumbURL: str(first(f, "thumburl", "thumbnail")),
			})
		}
		if r.Cursor == "" || r.Cursor == cursor {
			break
		}
		cursor = r.Cursor
	}
	return out, nil
}

// CreateAlbum creates an album.
func (c *client) CreateAlbum(ctx context.Context, name string) (*drive.MkdirResult, error) {
	q := url.Values{}
	q.Set("title", name)
	body, err := c.request(ctx, http.MethodPost, albumAPI+"/create", q)
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	var r struct {
		AlbumID string `json:"album_id"`
	}
	_ = json.Unmarshal(body, &r)
	if r.AlbumID == "" {
		return &drive.MkdirResult{Error: "创建相册失败"}, nil
	}
	return &drive.MkdirResult{FileID: "album:" + r.AlbumID}, nil
}

// RenameAlbum renames an album.
func (c *client) RenameAlbum(ctx context.Context, albumID, name string) error {
	q := url.Values{}
	q.Set("album_id", albumID)
	q.Set("title", name)
	_, err := c.request(ctx, http.MethodPost, albumAPI+"/settitle", q)
	return err
}

// Delete removes files (by fsid, with album context).
func (c *client) Delete(ctx context.Context, fileIDs []string) error {
	fsids := make([]string, 0, len(fileIDs))
	albumIDs := make([]string, 0, len(fileIDs))
	var errs []error
	for _, id := range fileIDs {
		if strings.HasPrefix(id, "album:") {
			albumIDs = append(albumIDs, strings.TrimPrefix(id, "album:"))
			continue
		}
		fsid := parseFsid(id)
		if fsid != "" {
			fsids = append(fsids, fsid)
		}
	}
	if len(fsids) > 0 {
		q := url.Values{}
		q.Set("fsid_list", strings.Join(fsids, ","))
		if _, err := c.request(ctx, http.MethodGet, fileV1+"/delete", q); err != nil {
			errs = append(errs, fmt.Errorf("删除照片失败: %w", err))
		}
	}
	for _, a := range albumIDs {
		q := url.Values{}
		q.Set("album_id", a)
		if _, err := c.request(ctx, http.MethodPost, albumAPI+"/delete", q); err != nil {
			errs = append(errs, fmt.Errorf("删除相册 %s 失败: %w", a, err))
		}
	}
	return errors.Join(errs...)
}

// DownloadInfo returns a dlink.
func (c *client) DownloadInfo(ctx context.Context, fileID string) (string, error) {
	fsid := parseFsid(fileID)
	if fsid == "" {
		return "", errors.New("文件夹不能直接下载")
	}
	q := url.Values{}
	q.Set("fsid", fsid)
	body, err := c.request(ctx, http.MethodPost, fileV2+"/download", q)
	if err != nil {
		return "", err
	}
	var r struct {
		Dlink string `json:"dlink"`
	}
	_ = json.Unmarshal(body, &r)
	if r.Dlink == "" {
		return "", errors.New("获取下载地址失败")
	}
	return r.Dlink, nil
}

func parseFsid(fileID string) string {
	v := fileID
	if strings.HasPrefix(v, "f:") {
		return v[2:]
	}
	if strings.HasPrefix(v, "af:") {
		parts := strings.Split(v, ":")
		if len(parts) > 2 {
			return parts[2]
		}
		return ""
	}
	if strings.HasPrefix(v, "album:") {
		return ""
	}
	return v
}

func fileNameFromPath(p, fallback string) string {
	if p == "" {
		return fallback
	}
	i := strings.LastIndex(p, "/")
	if i >= 0 {
		return p[i+1:]
	}
	return p
}

func first(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return ""
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return ""
	}
}

func num(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

func numVal(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// ---- driver ----

// Driver implements drive.Driver for yike.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return RootID }

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	var items []File
	if isRoot(dirID) {
		items, err = cl.listRoot(ctx)
	} else if albumID := strings.TrimPrefix(dirID, "album:"); albumID != dirID {
		items, err = cl.listAlbum(ctx, albumID)
	}
	if err != nil {
		return nil, err
	}
	out := make([]model.File, 0, len(items))
	for i := range items {
		out = append(out, mapFile(&items[i], c.DriveID, dirID))
	}
	return out, nil
}

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if isRoot(fileID) {
		return model.File{DriveID: c.DriveID, FileID: RootID, Name: "一刻相册", NameSearch: "yike", IsDir: true, Icon: "iconfile-folder"}, nil
	}
	isDir := strings.HasPrefix(fileID, "album:")
	return model.File{
		DriveID: c.DriveID, FileID: fileID, Name: strings.TrimPrefix(fileID, "album:"),
		IsDir: isDir, Category: map[bool]string{true: "folder", false: "other"}[isDir],
	}, nil
}

func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	v, err := d.GetInfo(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	f, ok := v.(model.File)
	if !ok {
		return nil, drive.ErrNotFound
	}
	return &f, nil
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, _ int) (*model.DownloadURL, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	u, err := cl.DownloadInfo(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID: c.DriveID, FileID: fileID, URL: u,
		Headers:      map[string]string{"User-Agent": ua, "Referer": "https://photo.baidu.com/"},
		DownloadMode: "proxy", Concurrency: 1,
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID: c.DriveID, FileID: fileID, Size: u.Size, Headers: u.Headers,
		Qualities: []model.VideoQuality{{Quality: "origin", Label: "原画", Value: "origin", URL: u.URL, Headers: u.Headers, ForceProxy: true}},
	}, nil
}

func (d *Driver) Mkdir(ctx context.Context, c drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	if !isRoot(parentID) {
		return &drive.MkdirResult{Error: "仅可在根目录创建相册"}, nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	return cl.CreateAlbum(ctx, name)
}

func (d *Driver) Rename(ctx context.Context, c drive.Context, fileID, name string) (*drive.RenameResult, error) {
	if !strings.HasPrefix(fileID, "album:") {
		return nil, nil
	}
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	albumID := strings.TrimPrefix(fileID, "album:")
	if err := cl.RenameAlbum(ctx, albumID, name); err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: fileID, ParentFileID: RootID, Name: name, IsDir: true}, nil
}

func (d *Driver) Trash(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	files := make([]drive.FileRef, 0, len(fileIDs))
	var failed []error
	for _, id := range fileIDs {
		if strings.HasPrefix(id, "album:") || parseFsid(id) == "" || isRoot(id) {
			failed = append(failed, fmt.Errorf("%s: 相册和根目录不支持移入照片回收站", id))
			continue
		}
		files = append(files, drive.FileRef{ID: id})
	}
	if len(files) == 0 {
		return nil, errors.Join(failed...)
	}
	completed, err := d.Delete(ctx, c, files)
	return completed, errors.Join(append(failed, err)...)
}

func (d *Driver) Delete(ctx context.Context, c drive.Context, refs []drive.FileRef) ([]string, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	if err := cl.Delete(ctx, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (d *Driver) Move(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	return nil, drive.NotSupported("move")
}

func (d *Driver) Copy(ctx context.Context, c drive.Context, refs []drive.FileRef, toParentID, _ string) ([]string, error) {
	return nil, drive.NotSupported("copy")
}

func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	cl, err := clientOf(c)
	if err != nil {
		return nil, err
	}
	if err := cl.getuinfo(ctx); err != nil {
		return nil, err
	}
	// bdstoken is only needed by some upload endpoints. The legacy client
	// deliberately allowed browsing/login when pan.baidu.com did not return it.
	_ = cl.getBdstoken(ctx)
	if token != nil {
		token.AccessToken = cl.sess.Cookie
		token.RefreshToken = mustJSON(cl.sess)
		if cl.sess.UK != "" {
			token.ProviderAccountID = cl.sess.UK
			token.UserID = model.BuildUserID(providerID, cl.sess.UK)
			token.DefaultDriveID = model.BuildDriveID(providerID, cl.sess.UK)
		}
	}
	return token, nil
}

// authLogin logs in with a BDUSS cookie.
func authLogin(ctx context.Context, req drive.AuthRequest) (*model.TokenInfo, error) {
	cookie := normalizeCookie(req.Config["cookie"])
	if cookie == "" {
		return nil, errors.New("一刻相册：请填写 BDUSS / Cookie")
	}
	cl := &client{http: netx.NewClient(60 * time.Second), sess: &Session{Cookie: cookie}, limit: 300 * time.Millisecond}
	if err := cl.getuinfo(ctx); err != nil {
		return nil, err
	}
	// RefreshAccount must keep a valid BDUSS session even when the optional
	// Baidu template-variable endpoint is temporarily unavailable.
	_ = cl.getBdstoken(ctx)
	uid := cl.sess.UK
	if uid == "" {
		sum := sha256.Sum256([]byte(cookie))
		uid = "cookie_" + hex.EncodeToString(sum[:6])
	}
	name := uid
	return &model.TokenInfo{
		TokenFrom:         providerID,
		AccessToken:       cookie,
		RefreshToken:      mustJSON(cl.sess),
		UserID:            model.BuildUserID(providerID, uid),
		UserName:          name,
		NickName:          name,
		Name:              name,
		ProviderAccountID: uid,
		ProviderRootID:    RootID,
	}, nil
}

func mapFile(item *File, driveID, parentID string) model.File {
	switch item.Kind {
	case "album":
		f := driveutil.NewFile(driveID, "album:"+item.AlbumID, parentID, item.Name, true, 0, item.Mtime)
		f.Thumbnail = item.ThumbURL
		return f
	default:
		fileID := item.FsID
		if item.Kind == "albumfile" && item.AlbumID != "" {
			fileID = "af:" + item.AlbumID + ":" + item.FsID
		} else {
			fileID = "f:" + item.FsID
		}
		f := driveutil.NewFile(driveID, fileID, parentID, item.Name, false, item.Size, item.Mtime)
		f.Thumbnail = item.ThumbURL
		hash := decryptYikeMd5(item.MD5)
		f.ContentHash = hash
		if hash != "" {
			f.ContentHashName = "md5"
		}
		return f
	}
}

func isRoot(id string) bool { return id == "" || id == RootID || id == "root" || id == "/" }

// decryptYikeMd5 mirrors alist DecryptMd5: a pure-hex value is returned as-is;
// otherwise each char is XORed with its position (position 9 uses charCode-103)
// and the result is re-ordered into 8-char blocks (8:16 0:8 24:32 16:24).
func decryptYikeMd5(encryptMd5 string) string {
	raw := encryptMd5
	if len(raw) != 32 {
		return ""
	}
	// pure hex -> already plain
	isHex := true
	for _, ch := range raw {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			isHex = false
			break
		}
	}
	if isHex {
		return raw
	}
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		ch := raw[i]
		var n int
		if i == 9 {
			n = int(ch) - 103
		} else {
			// parse hex digit
			switch {
			case ch >= '0' && ch <= '9':
				n = int(ch - '0')
			case ch >= 'a' && ch <= 'f':
				n = int(ch-'a') + 10
			case ch >= 'A' && ch <= 'F':
				n = int(ch-'A') + 10
			default:
				n = 0
			}
		}
		n = n ^ (15 & i)
		// to hex char
		if n >= 0 && n <= 9 {
			out[i] = byte('0' + n)
		} else {
			out[i] = byte('a' + n - 10)
		}
	}
	decrypted := string(out)
	return decrypted[8:16] + decrypted[0:8] + decrypted[24:32] + decrypted[16:24]
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
