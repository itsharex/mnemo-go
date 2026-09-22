package pan189

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// uploadBase returns the personal/family upload root.
func (d *Driver) uploadBase(ctx context.Context, c drive.Context, parentID string) (string, error) {
	sess, err := sessionOf(c.Token)
	if err != nil {
		return "", err
	}
	isFamily, _ := cloudInfoForID(sess, parentID)
	base := uploadURL + "/person"
	if isFamily {
		base = uploadURL + "/family"
	}
	return base, nil
}

// uploadInitResult is the initMultiUpload response.
type uploadInitResult struct {
	UploadFileID   string
	FileDataExists int
}

// initMultiUpload creates the upload session (AList initMultiUpload).
func (d *Driver) initMultiUpload(ctx context.Context, c drive.Context, base, parentID, fileName string, size, slice int64, count int, fileMD5, sliceMD5 string) (*uploadInitResult, error) {
	sess, err := sessionOf(c.Token)
	if err != nil {
		return nil, err
	}
	isFamily, familyID := cloudInfoForID(sess, parentID)
	parentFolderID := toFolderID(parentID)
	params := map[string]string{
		"parentFolderId": parentFolderID,
		"fileName":       url.QueryEscape(fileName),
		"fileSize":       itoa(size),
		"sliceSize":      itoa(slice),
	}
	// 单片带 fileMd5/sliceMd5（服务端 fileDataExists 秒传判定），多片只带
	// lazyCheck（两者组合会令秒传判定失效）。
	if count > 1 {
		params["lazyCheck"] = "1"
	} else {
		params["fileMd5"] = fileMD5
		params["sliceMd5"] = sliceMD5
	}
	if familyID != "" {
		params["familyId"] = familyID
	}
	raw, err := d.request(ctx, c, base+"/initMultiUpload", reqOptions{method: "GET", params: params, family: boolPtr(isFamily)})
	if err != nil {
		return nil, err
	}
	var res struct {
		Data struct {
			UploadFileID   json.RawMessage `json:"uploadFileId"`
			FileDataExists int             `json:"fileDataExists"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &res)
	return &uploadInitResult{
		UploadFileID:   rawIDString(res.Data.UploadFileID),
		FileDataExists: res.Data.FileDataExists,
	}, nil
}

// commitMultiUploadFile finalises the upload (AList commitMultiUploadFile).
func (d *Driver) commitMultiUploadFile(ctx context.Context, c drive.Context, base, parentID, uploadFileID, fileMD5, sliceMD5 string, overwrite bool) (string, error) {
	opertype := "1"
	if overwrite {
		opertype = "3"
	}
	sess, err := sessionOf(c.Token)
	if err != nil {
		return "", err
	}
	isFamily, _ := cloudInfoForID(sess, parentID)
	raw, err := d.request(ctx, c, base+"/commitMultiUploadFile", reqOptions{
		method: "GET",
		params: map[string]string{
			"uploadFileId": uploadFileID,
			"fileMd5":      fileMD5,
			"sliceMd5":     sliceMD5,
			"lazyCheck":    "1",
			"isLog":        "0",
			"opertype":     opertype,
		},
		family: boolPtr(isFamily),
	})
	if err != nil {
		return "", err
	}
	var res struct {
		Data struct {
			FileID string `json:"fileId"`
			File   struct {
				UserFileID string `json:"userFileId"`
			} `json:"file"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.Data.File.UserFileID != "" {
		return res.Data.File.UserFileID, nil
	}
	return res.Data.FileID, nil
}

// getUploadURLs fetches one part's pre-signed PUT url + headers. The response
// maps the requested part index to its entry under uploadUrls/data.
func (d *Driver) getUploadURLs(ctx context.Context, c drive.Context, base, parentID, uploadFileID, partInfo string, partIndex int) (string, map[string]string, error) {
	sess, err := sessionOf(c.Token)
	if err != nil {
		return "", nil, err
	}
	isFamily, _ := cloudInfoForID(sess, parentID)
	raw, err := d.request(ctx, c, base+"/getMultiUploadUrls", reqOptions{
		method: "GET",
		params: map[string]string{"uploadFileId": uploadFileID, "partInfo": partInfo},
		family: boolPtr(isFamily),
	})
	if err != nil {
		return "", nil, err
	}
	var payload struct {
		UploadURLs map[string]json.RawMessage `json:"uploadUrls"`
		Data       map[string]json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(raw, &payload)
	readEntry := func(m map[string]json.RawMessage) (string, map[string]string, bool) {
		for _, key := range []string{"partNumber_" + itoa(int64(partIndex)), itoa(int64(partIndex))} {
			if rawEntry, ok := m[key]; ok {
				var e struct {
					RequestURL    string `json:"requestURL"`
					RequestHeader string `json:"requestHeader"`
				}
				_ = json.Unmarshal(rawEntry, &e)
				if e.RequestURL != "" {
					return e.RequestURL, parseHeaderMap(e.RequestHeader), true
				}
			}
		}
		// single-entry fallback
		if len(m) == 1 {
			for _, rawEntry := range m {
				var e struct {
					RequestURL    string `json:"requestURL"`
					RequestHeader string `json:"requestHeader"`
				}
				_ = json.Unmarshal(rawEntry, &e)
				if e.RequestURL != "" {
					return e.RequestURL, parseHeaderMap(e.RequestHeader), true
				}
			}
		}
		return "", nil, false
	}
	if u, h, ok := readEntry(payload.UploadURLs); ok {
		return u, h, nil
	}
	if u, h, ok := readEntry(payload.Data); ok {
		return u, h, nil
	}
	return "", nil, errors.New("获取分片" + itoa(int64(partIndex)) + "上传地址失败")
}

// parseHeaderMap decodes the requestHeader=k=v&... string into a header map.
func parseHeaderMap(s string) map[string]string {
	h := map[string]string{}
	for _, kv := range strings.Split(s, "&") {
		i := strings.Index(kv, "=")
		if i > 0 {
			h[kv[:i]] = kv[i+1:]
		}
	}
	return h
}

// putPart streams one chunk to the pre-signed URL with the client suffix.
func putPart(ctx context.Context, rawURL string, headers map[string]string, chunk []byte) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	q := u.Query()
	for k, v := range clientSuffix() {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	hc := netx.NewClient(120 * time.Second)
	req, err := hc.Req(ctx, http.MethodPut, u.String(), bytes.NewReader(chunk))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := netx.DoUpload(hc.HTTP, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return errors.New("分片上传失败 HTTP " + itoa(int64(resp.StatusCode)) + ": " + truncateStr(string(body), 200))
	}
	return nil
}

// fileMD5Of computes the whole-file MD5 plus per-slice md5 hex + partInfo.
// Returns uppercase whole-file md5, per-slice md5 hex list and partInfo list.
// Progress is reported through the upload runtime callback during hashing.
func fileMD5Of(r io.ReaderAt, size, slice int64, count int, ui *model.UploadingUI) (string, []string, []string, error) {
	var fileSum = md5.New()
	sliceHexs := make([]string, 0, count)
	partInfos := make([]string, 0, count)
	var hashed int64
	for i := 1; i <= count; i++ {
		start := (int64(i) - 1) * slice
		cur := slice
		if remain := size - start; remain < cur {
			cur = remain
		}
		chunk := make([]byte, cur)
		if _, err := r.ReadAt(chunk, start); err != nil && err != io.EOF {
			return "", nil, nil, err
		}
		_, _ = fileSum.Write(chunk)
		sliceSum := md5Sum(chunk)
		sliceHexs = append(sliceHexs, strings.ToUpper(hexEncode(sliceSum)))
		partInfos = append(partInfos, itoa(int64(i))+"-"+base64StdEncode(sliceSum))
		hashed += cur
		if ui != nil {
			ui.ReportUploadProgress(hashed, size)
		}
	}
	return strings.ToUpper(hexEncode(fileSum.Sum(nil))), sliceHexs, partInfos, nil
}

// pan189UploadSessionKey includes the content hash so replacing a local file
// with another file of the same name and size cannot reuse the old remote
// upload session.
func pan189UploadSessionKey(c drive.Context, parentID, name string, size int64, fileMD5 string) string {
	nameWithHash := strings.TrimSpace(name) + "\x00" + strings.ToUpper(strings.TrimSpace(fileMD5))
	return drive.UploadSessionKey(c.UserID, c.DriveID, parentID, nameWithHash, size)
}

func restorePan189UploadState(sessionID string, partNumbers []int, totalParts int) (string, map[int]bool) {
	completed := make(map[int]bool, len(partNumbers))
	for _, partNumber := range partNumbers {
		if partNumber >= 1 && partNumber <= totalParts {
			completed[partNumber] = true
		}
	}
	return strings.TrimSpace(sessionID), completed
}

func pan189UploadedBytes(size, slice int64, completed map[int]bool) int64 {
	var uploaded int64
	for partNumber := range completed {
		start := (int64(partNumber) - 1) * slice
		if start < 0 || start >= size {
			continue
		}
		partSize := slice
		if remain := size - start; remain < partSize {
			partSize = remain
		}
		uploaded += partSize
	}
	return uploaded
}

// UploadOneFile uploads a single file in slices
// (initMultiUpload → getMultiUploadUrls → PUT → commitMultiUploadFile).
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || ui.Info.LocalFilePath == "" {
		return errors.New("pan189: 上传文件路径为空")
	}
	if ui.Info.IsDir {
		return errors.New("pan189: 不支持上传文件夹")
	}
	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	slice := partSize(size)
	count := 1
	if size > 0 {
		count = int((size + slice - 1) / slice)
	}
	if count < 1 {
		count = 1
	}
	parentID := ui.Info.ParentFileID
	fileName := ui.Info.Name

	// 单遍预计算 整文件MD5 + 各分片MD5 + partInfo（对齐 AList FastUpload）。
	fileMD5Hex, sliceMD5Hexs, partInfos, err := fileMD5Of(f, size, slice, count, ui)
	if err != nil {
		return err
	}
	if fileMD5Hex == "" {
		return errors.New("计算 MD5 失败")
	}
	// 多分片：sliceMd5 = MD5(各分片 hex 列表 join '\n')。
	sliceMD5Hex := fileMD5Hex
	if count > 1 {
		sliceMD5Hex = strings.ToUpper(hexEncode(md5Sum([]byte(strings.Join(sliceMD5Hexs, "\n")))))
	}

	base, err := d.uploadBase(ctx, c, parentID)
	if err != nil {
		return err
	}

	// Resume before initializing a new remote session. The service creates a
	// new uploadFileId on every init request, so loading state afterwards can
	// never resume the saved session.
	sessionKey := pan189UploadSessionKey(c, parentID, fileName, size, fileMD5Hex)
	savedSessionID, savedParts := drive.LoadUploadSessionState(sessionKey)
	uploadFileID, uploadedSet := restorePan189UploadState(savedSessionID, savedParts, count)
	if uploadFileID == "" {
		initRes, err := d.initMultiUpload(ctx, c, base, parentID, fileName, size, slice, count, fileMD5Hex, sliceMD5Hex)
		if err != nil {
			return err
		}
		uploadFileID = initRes.UploadFileID
		if uploadFileID == "" {
			return errors.New("初始化上传失败")
		}
		if initRes.FileDataExists == 1 {
			// 服务端按 MD5 命中已存在文件，免上传提交。
			fileID, err := d.commitMultiUploadFile(ctx, c, base, parentID, uploadFileID, fileMD5Hex, sliceMD5Hex, false)
			if err != nil {
				return err
			}
			drive.ClearUploadSession(sessionKey)
			space, _ := pan189SpaceID(parentID)
			ui.Upload.FileID = pan189FileID(space, fileID)
			setUploadState(ui, size, size)
			return nil
		}
		// Persist the remote session immediately so a stop or process exit
		// before the first part still has something to resume.
		_ = drive.SaveUploadSessionState(sessionKey, uploadFileID, nil)
	}

	uploaded := pan189UploadedBytes(size, slice, uploadedSet)
	setUploadState(ui, uploaded, size)

	for i := 1; i <= count; i++ {
		partInfo := partInfos[i-1]
		start := (int64(i) - 1) * slice
		cur := slice
		if remain := size - start; remain < cur {
			cur = remain
		}
		// Skip already-uploaded parts (resume)
		if uploadedSet[i] {
			continue
		}
		uploadURLStr, headers, err := d.getUploadURLs(ctx, c, base, parentID, uploadFileID, partInfo, i)
		if err != nil {
			return err
		}
		chunk := make([]byte, cur)
		if _, err := f.ReadAt(chunk, start); err != nil && err != io.EOF {
			return err
		}
		if err := putPart(ctx, uploadURLStr, headers, chunk); err != nil {
			return err
		}
		uploadedSet[i] = true
		_ = drive.SaveUploadSessionState(sessionKey, uploadFileID, drive.SortedUniqueParts(uploadedSet))
		uploaded += cur
		setUploadState(ui, uploaded, size)
	}

	fileID, err := d.commitMultiUploadFile(ctx, c, base, parentID, uploadFileID, fileMD5Hex, sliceMD5Hex, false)
	if err != nil {
		return err
	}
	drive.ClearUploadSession(sessionKey)
	space, _ := pan189SpaceID(parentID)
	ui.Upload.FileID = pan189FileID(space, fileID)
	setUploadState(ui, size, size)
	return nil
}

func setUploadState(ui *model.UploadingUI, done, total int64) {
	if ui == nil || total <= 0 {
		return
	}
	ui.ReportUploadProgress(done, total)
}
