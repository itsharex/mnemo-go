package pan189

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const providerID = model.ProviderPan189

func init() {
	drive.Register(drive.Registration{
		ID:   providerID,
		Meta: drive.GetMeta(providerID),
		Caps: drive.NewCapabilities(providerID, map[string]bool{
			"copy":            true,
			"createShare":     true,
			"shareExpiration": true,
			"shareHistory":    true,
			"recycleBin":      true,
			"permanentDelete": true,
			"trashRestore":    false,
		}, func(c *drive.Capabilities) {
			c.SetHashes([]string{"md5"}, []string{"md5"})
			c.SetShareExpirationOptions(0, 1, 7)
		}),
		Auth: login189,
		Login: drive.LoginConfig{Fields: []drive.LoginField{
			{Key: "login_mode", Type: "select", Label: "登录方式", Required: true, Options: []drive.LoginOption{{Value: "password", Label: "账号密码"}, {Value: "sms", Label: "短信验证码"}}},
			{Key: "username", Type: "text", Label: "手机号/邮箱", Required: true},
			{Key: "password", Type: "password", Label: "密码", Required: true},
			{Key: "sms_code", Type: "text", Label: "短信验证码", Required: false},
			{Key: "cloud_type", Type: "select", Label: "云空间", Required: false, Options: []drive.LoginOption{
				{Value: CloudPersonal, Label: "个人云"},
				{Value: CloudFamily, Label: "家庭云"},
			}},
			{Key: "validate_code", Type: "text", Label: "图形验证码（需要时填写）", Required: false, Hint: "登录提示需要验证码时，输入图片中的字符后重试"},
		}},
		Factory: func() drive.Driver { return &Driver{} },
	})
}

// Driver implements drive.Driver for the 189 cloud drive.
type Driver struct {
	drive.BaseDriver
}

func (d *Driver) ID() string                       { return providerID }
func (d *Driver) Meta() drive.Meta                 { return drive.GetMeta(providerID) }
func (d *Driver) Capabilities() drive.Capabilities { return drive.RegistryCaps(providerID) }
func (d *Driver) RootID() string                   { return PAN189Root }

// ---- listing ----

// listPage fetches one page (pageNum starts at 1). done mirrors the legacy
// "no items → last page" heuristic.
func (d *Driver) listPage(ctx context.Context, c drive.Context, dirID string, pageNum int) ([]model.File, bool, error) {
	sess, err := sessionOf(c.Token)
	if err != nil {
		return nil, true, err
	}
	isFamily, familyID := cloudInfo(sess)
	parent := toFolderID(dirID)
	var (
		rawURL string
		query  map[string]string
	)
	if isFamily {
		rawURL = apiURL + "/family/file/listFiles.action"
		query = map[string]string{
			"folderId": parent, "fileType": "0", "mediaAttr": "0", "iconOption": "5",
			"pageNum": strconv.Itoa(pageNum), "pageSize": "100", "familyId": familyID,
			"orderBy": "1", "descending": "false",
		}
	} else {
		rawURL = apiURL + "/listFiles.action"
		query = map[string]string{
			"folderId": parent, "fileType": "0", "mediaAttr": "0", "iconOption": "5",
			"pageNum": strconv.Itoa(pageNum), "pageSize": "100", "recursive": "0",
			"orderBy": "filename", "descending": "false",
		}
	}
	raw, err := d.request(ctx, c, rawURL, reqOptions{method: "GET", query: query})
	if err != nil {
		return nil, true, err
	}
	var res struct {
		FileListAO *struct {
			FileList   []json.RawMessage `json:"fileList"`
			FolderList []json.RawMessage `json:"folderList"`
		} `json:"fileListAO"`
	}
	_ = json.Unmarshal(raw, &res)
	items := make([]model.File, 0)
	if res.FileListAO == nil {
		return items, true, nil
	}
	for _, rf := range res.FileListAO.FolderList {
		var f struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			LastOpTime string `json:"lastOpTime"`
			CreateDate string `json:"createDate"`
			ParentID   string `json:"parentId"`
		}
		_ = json.Unmarshal(rf, &f)
		items = append(items, mapFile(pan189File{
			ID: f.ID, Name: f.Name, LastOpTime: f.LastOpTime, CreateDate: f.CreateDate,
			ParentID: f.ParentID, IsFolder: true,
		}, c.DriveID, parent))
	}
	for _, rf := range res.FileListAO.FileList {
		var f struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Size       int64  `json:"size"`
			MD5        string `json:"md5"`
			LastOpTime string `json:"lastOpTime"`
			CreateDate string `json:"createDate"`
			Icon       struct {
				SmallURL string `json:"smallUrl"`
				LargeURL string `json:"largeUrl"`
			} `json:"icon"`
		}
		_ = json.Unmarshal(rf, &f)
		items = append(items, mapFile(pan189File{
			ID: f.ID, Name: f.Name, Size: f.Size, MD5: f.MD5,
			LastOpTime: f.LastOpTime, CreateDate: f.CreateDate,
			SmallURL: f.Icon.SmallURL, LargeURL: f.Icon.LargeURL,
		}, c.DriveID, parent))
	}
	done := len(res.FileListAO.FolderList) == 0 && len(res.FileListAO.FileList) == 0
	return items, done, nil
}

// listAll walks pages up to 200 (mirrors legacy apiPan189FileList).
func (d *Driver) listAll(ctx context.Context, c drive.Context, dirID string) ([]model.File, error) {
	all := make([]model.File, 0, 128)
	for pageNum := 1; pageNum <= 200; pageNum++ {
		items, done, err := d.listPage(ctx, c, dirID, pageNum)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if done {
			break
		}
	}
	return all, nil
}

func (d *Driver) List(ctx context.Context, c drive.Context, dirID string, _ *drive.ListOptions) ([]model.File, error) {
	return d.listAll(ctx, c, dirID)
}

func (d *Driver) ListPaged(ctx context.Context, c drive.Context, dirID, marker string, _ *drive.ListOptions) (*drive.DirPage, error) {
	pageNum := pageMarker(marker)
	items, done, err := d.listPage(ctx, c, dirID, pageNum)
	if err != nil {
		return nil, err
	}
	return &drive.DirPage{Items: items, NextMarker: markerNext(pageNum, done)}, nil
}

// pageMarker parses a page cursor; invalid/empty maps to page 1.
func pageMarker(marker string) int {
	if n, err := strconv.Atoi(marker); err == nil && n > 0 {
		return n
	}
	return 1
}

// markerNext returns the next page marker, or "" when the page is the last.
func markerNext(pageNum int, done bool) string {
	if done {
		return ""
	}
	return strconv.Itoa(pageNum + 1)
}

// ---- detail ----

func (d *Driver) GetInfo(ctx context.Context, c drive.Context, fileID string) (any, error) {
	if fileID == PAN189Root || fileID == "-11" || fileID == "root" || fileID == "/" {
		f := driveutil.NewFile(c.DriveID, PAN189Root, "", "天翼云盘", true, 0, 0)
		f.Icon = "iconfile-folder"
		return f, nil
	}
	f := driveutil.NewFile(c.DriveID, fileID, "", fileID, false, 0, 0)
	return f, nil
}

func (d *Driver) GetFile(ctx context.Context, c drive.Context, fileID string) (*model.File, error) {
	info, err := d.GetInfo(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	if f, ok := info.(model.File); ok {
		return &f, nil
	}
	return nil, errors.New("pan189: 无法解析文件信息")
}

// ---- download / preview ----

// expireTimeFromURL derives the URL expiry from its query params
// (mirrors legacy GetExpiresTime).
func expireTimeFromURL(downloadURL string) int64 {
	if unescaped, err := url.QueryUnescape(downloadURL); err == nil {
		downloadURL = unescaped
	}
	if downloadURL == "" {
		return 0
	}
	u, err := url.Parse(downloadURL)
	if err != nil {
		return 0
	}
	params := map[string]string{}
	for k, vals := range u.Query() {
		lower := strings.ToLower(k)
		if _, seen := params[lower]; !seen && len(vals) > 0 {
			params[lower] = vals[0]
		}
	}
	if amzDate := params["x-amz-date"]; amzDate != "" {
		if amzExpires, err := strconv.ParseInt(params["x-amz-expires"], 10, 64); err == nil && amzExpires > 0 {
			if m := regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z?$`).FindStringSubmatch(amzDate); len(m) == 7 {
				y, _ := strconv.Atoi(m[1])
				mo, _ := strconv.Atoi(m[2])
				da, _ := strconv.Atoi(m[3])
				h, _ := strconv.Atoi(m[4])
				mi, _ := strconv.Atoi(m[5])
				se, _ := strconv.Atoi(m[6])
				if base := time.Date(y, time.Month(mo), da, h, mi, se, 0, time.UTC).UnixMilli(); base > 0 {
					return base + amzExpires*1000
				}
			}
		}
	}
	for _, key := range []string{"x-oss-expires", "expire", "expires", "expires_at", "exp", "e"} {
		value := params[key]
		if value == "" {
			continue
		}
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			if n >= 1_000_000_000 {
				if n < 10_000_000_000 {
					return n * 1000
				}
				return n
			}
			continue
		}
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func (d *Driver) downloadInfo(ctx context.Context, c drive.Context, fileID string) (string, int64, error) {
	id := toFolderID(fileID)
	if id == Pan189DefaultFolder {
		return "", 0, errors.New("文件夹不能直接下载")
	}
	sess, err := sessionOf(c.Token)
	if err != nil {
		return "", 0, err
	}
	isFamily, familyID := cloudInfo(sess)
	var (
		rawURL string
		query  map[string]string
	)
	if isFamily {
		rawURL = apiURL + "/family/file/getFileDownloadUrl.action"
		query = map[string]string{"fileId": id, "familyId": familyID}
	} else {
		rawURL = apiURL + "/getFileDownloadUrl.action"
		query = map[string]string{"fileId": id, "dt": "3", "flag": "1"}
	}
	raw, err := d.request(ctx, c, rawURL, reqOptions{method: "GET", query: query})
	if err != nil {
		return "", 0, err
	}
	var res struct {
		FileDownloadURL string `json:"fileDownloadUrl"`
	}
	_ = json.Unmarshal(raw, &res)
	dlURL := strings.ReplaceAll(res.FileDownloadURL, "&amp;", "&")
	dlURL = regexp.MustCompile(`^http://`).ReplaceAllString(dlURL, "https://")
	if dlURL == "" {
		return "", 0, errors.New("获取下载地址失败")
	}
	// Follow a single 302 redirect manually (legacy behaviour).
	if loc, ok := followRedirect(ctx, dlURL); ok {
		dlURL = loc
	}
	return dlURL, 0, nil
}

// followRedirect performs one GET with redirects disabled and returns the
// Location when the server replies 3xx.
func followRedirect(ctx context.Context, rawURL string) (string, bool) {
	hc := netx.NewClient(60 * time.Second)
	resp, err := hc.Do(ctx, http.MethodGet, rawURL, map[string]string{"User-Agent": "Mozilla/5.0"}, nil)
	if err != nil {
		return "", false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.Header.Get("Location"), true
	}
	return "", false
}

func (d *Driver) GetDownloadURL(ctx context.Context, c drive.Context, fileID string, _ int) (*model.DownloadURL, error) {
	u, size, err := d.downloadInfo(ctx, c, fileID)
	if err != nil {
		return nil, err
	}
	return &model.DownloadURL{
		DriveID:      c.DriveID,
		FileID:       fileID,
		ExpireTime:   expireTimeFromURL(u),
		URL:          u,
		Size:         size,
		Headers:      map[string]string{"User-Agent": ua189, "Referer": webURL + "/"},
		DownloadMode: "proxy",
		Concurrency:  1,
	}, nil
}

func (d *Driver) GetVideoPreview(ctx context.Context, c drive.Context, fileID string) (*model.VideoPreview, error) {
	u, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return nil, err
	}
	return &model.VideoPreview{
		DriveID: c.DriveID, FileID: fileID, Size: u.Size, Headers: u.Headers,
		Qualities: []model.VideoQuality{{
			Quality: "origin", Label: "原画", Value: "origin",
			URL: u.URL, Headers: u.Headers, ForceProxy: true,
		}},
	}, nil
}

// mapFile converts a raw list entry to the unified model. The root folder
// (-11) is surfaced as pan189_root; md5 is carried as the content hash.
func mapFile(it pan189File, driveID, parentID string) model.File {
	parent := displayParent(parentID)
	timeUnix := parseTime(it.LastOpTime, it.CreateDate)
	f := driveutil.NewFile(driveID, it.ID, parent, it.Name, it.IsFolder, it.Size, timeUnix)
	if !it.IsFolder {
		f.ContentHash = it.MD5
		if it.MD5 != "" {
			f.ContentHashName = "md5"
		}
		if it.SmallURL != "" {
			f.Thumbnail = it.SmallURL
		} else {
			f.Thumbnail = it.LargeURL
		}
	}
	return f
}

// parseTime parses "2006-01-02 15:04:05" (+08:00) or RFC3339, defaulting to
// now on failure (mirrors legacy parseTime).
func parseTime(lastOpTime, createDate string) int64 {
	src := lastOpTime
	if src == "" {
		src = createDate
	}
	if src == "" {
		return time.Now().Unix()
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, src, time.FixedZone("+0800", 8*60*60)); err == nil {
			return t.Unix()
		}
	}
	return time.Now().Unix()
}
