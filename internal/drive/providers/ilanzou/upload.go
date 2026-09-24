package ilanzou

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const uploadPartSize = 8 * 1024 * 1024 // 单片 8MB

// fileMD5 computes the lowercase hex MD5 of the whole local file while
// reporting progress via ui.Upload.
func fileMD5(path string, ui *model.UploadingUI) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	buf := make([]byte, 256*1024)
	var read int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			read += int64(n)
			if ui != nil {
				ui.ReportUploadProgress(read, ui.Info.Size)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RapidUploadByHash probes the same /7n/getUpToken endpoint used by normal
// uploads. A response without an upToken and with a file id means the server
// already has the content and has created the remote entry.
func (d *Driver) RapidUploadByHash(ctx context.Context, c drive.Context, req drive.RapidUploadRequest) (*drive.RapidUploadResult, error) {
	if !strings.EqualFold(strings.TrimSpace(req.Method), "md5") {
		return &drive.RapidUploadResult{Message: "优享版蓝奏云仅支持 MD5 秒传"}, nil
	}
	hash := strings.ToLower(strings.TrimSpace(req.Hash))
	if len(hash) != md5.Size*2 {
		return &drive.RapidUploadResult{Message: "无效的 MD5 指纹"}, nil
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return &drive.RapidUploadResult{Message: "无效的 MD5 指纹"}, nil
	}
	if req.Size < 0 || strings.TrimSpace(req.FileName) == "" {
		return &drive.RapidUploadResult{Message: "秒传参数无效"}, nil
	}
	up, _, err := d.request(ctx, c, "/7n/getUpToken", requestOptions{
		method: http.MethodPost,
		body: map[string]any{
			"fileId":   "",
			"fileName": req.FileName,
			"fileSize": req.Size/1024 + 1,
			"folderId": ToILanzouFolderId(req.ParentID),
			"md5":      hash,
			"type":     1,
		},
	})
	if err != nil {
		return nil, err
	}
	fileID := responseString(up, "fileId")
	if fileID == "" {
		fileID = responseString(up, "id")
	}
	if responseString(up, "upToken") == "" && fileID != "" {
		return &drive.RapidUploadResult{
			Reuse: true, FileID: fileID, ParentID: req.ParentID, Message: "秒传命中",
		}, nil
	}
	return &drive.RapidUploadResult{ParentID: req.ParentID, Message: "未命中秒传"}, nil
}

// ResolveTransferHash returns an MD5 exposed by the metadata cache, or hashes
// the authenticated download stream when the provider does not expose one in
// its listing response. The latter is only performed when migration permits
// streaming because it intentionally consumes the full source file once.
func (d *Driver) ResolveTransferHash(ctx context.Context, c drive.Context, fileID, method string, allowStream bool) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(method), "md5") {
		return "", nil
	}
	if cached, ok := drive.CachedFile(c.UserID, c.DriveID, fileID); ok {
		if strings.EqualFold(strings.TrimSpace(cached.ContentHashName), "md5") {
			hash := strings.ToLower(strings.TrimSpace(cached.ContentHash))
			if len(hash) == md5.Size*2 {
				if _, err := hex.DecodeString(hash); err == nil {
					return hash, nil
				}
			}
		}
	}
	if !allowStream {
		return "", nil
	}
	download, err := d.GetDownloadURL(ctx, c, fileID, 0)
	if err != nil {
		return "", err
	}
	if download == nil || download.URL == "" {
		return "", errors.New("无法获取文件下载地址以计算 MD5")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
	if err != nil {
		return "", err
	}
	for key, value := range download.Headers {
		req.Header.Set(key, value)
	}
	client := *httpClient
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("计算 MD5 下载失败 HTTP %d", resp.StatusCode)
	}
	h := md5.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// qiniuPart is one completed multipart part.
type qiniuPart struct {
	PartNumber int    `json:"partNumber"`
	Etag       string `json:"etag"`
}

type qiniuUploadState struct {
	Key      string      `json:"key"`
	UploadID string      `json:"uploadId"`
	Parts    []qiniuPart `json:"parts"`
}

func saveQiniuUploadState(sessionKey string, state qiniuUploadState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return drive.SaveUploadSessionState(sessionKey, string(encoded), nil)
}

// qiniuJSON issues a JSON body request to upload.qiniup.com with the UpToken.
func qiniuJSON(ctx context.Context, method, rawURL, upToken string, body any) (map[string]any, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "UpToken "+upToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, nil
	}
	var j map[string]any
	if err := json.Unmarshal(text, &j); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("七牛响应异常: %s", truncate(string(text), 200))
	}
	return j, resp.StatusCode, nil
}

// qiniuPut PUTs a raw part body.
func qiniuPut(ctx context.Context, rawURL, upToken string, data []byte) (map[string]any, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "UpToken "+upToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := netx.DoUpload(httpClient, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, nil
	}
	var j map[string]any
	if err := json.Unmarshal(text, &j); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("七牛响应异常: %s", truncate(string(text), 200))
	}
	return j, resp.StatusCode, nil
}

// formatGoTokenTime mirrors the legacy formatGoTokenTime (Go time.Format
// "Mon Jan 02 2006 15:04:05 GMT-0700 (MST)" timezone-abbreviation format).
func formatGoTokenTime(t time.Time) string {
	return t.Format("Mon Jan 02 2006 15:04:05 GMT-0700 (MST)")
}

// UploadOneFile uploads one file: MD5 → getUpToken (可能秒传) → qiniu 直传
// (form ≤8MB / multipart >8MB) → /7n/results 确认轮询。
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil {
		return errors.New("优享版蓝奏云: 无效的上传任务")
	}
	ui.Upload.DownState = "uploading"
	ui.Upload.IsDowning = true

	md5hex, err := fileMD5(ui.Info.LocalFilePath, ui)
	if err != nil {
		return err
	}
	if md5hex == "" {
		return errors.New("计算 MD5 失败")
	}
	stat, err := os.Stat(ui.Info.LocalFilePath)
	if err != nil {
		return err
	}
	size := stat.Size()
	ui.Info.Size = size
	ui.Info.SizeStr = model.FormatBytes(size)
	folderID := ToILanzouFolderId(firstNonEmpty(ui.Info.ParentFileID, "0"))
	sessionKey := drive.UploadSessionKey(c.UserID, c.DriveID, folderID, ui.Info.Name+"|"+md5hex, size)
	up, _, err := d.request(ctx, c, "/7n/getUpToken", requestOptions{
		method: http.MethodPost,
		body: map[string]any{
			"fileId":   "",
			"fileName": ui.Info.Name,
			"fileSize": size/1024 + 1,
			"folderId": folderID,
			"md5":      md5hex,
			"type":     1,
		},
	})
	if err != nil {
		return err
	}
	upToken := strOf(up["upToken"])
	rapidFileID := strOf(firstOf(up, "fileId", "id"))
	if data := mapVal(up, "data"); data != nil {
		upToken = firstNonEmpty(upToken, strOf(data["upToken"]))
		rapidFileID = firstNonEmpty(rapidFileID, strOf(firstOf(data, "fileId", "id")))
	}
	if upToken == "" && rapidFileID != "" {
		// MD5 命中秒传：直接返回 fileId
		drive.ClearUploadSession(sessionKey)
		markUploadDone(ui, size, rapidFileID)
		return nil
	}
	if upToken == "" {
		return errors.New("获取上传凭证失败")
	}

	account := "user"
	if c.Token != nil {
		account = firstNonEmpty(c.Token.UserName, "user")
		if cr := parseCred(c.Token.RefreshToken); cr != nil && cr.Account != "" {
			account = cr.Account
		}
	}
	now := time.Now()
	key := fmt.Sprintf("disk/%d/%d/%d/%s/%016d", now.Year(), int(now.Month()), now.Day(), account, now.UnixMilli())
	var saved qiniuUploadState
	if encoded, _ := drive.LoadUploadSessionState(sessionKey); encoded != "" {
		if json.Unmarshal([]byte(encoded), &saved) != nil || saved.Key == "" || saved.UploadID == "" {
			drive.ClearUploadSession(sessionKey)
			saved = qiniuUploadState{}
		} else {
			key = saved.Key
		}
	}
	keyB64 := base64.RawURLEncoding.EncodeToString([]byte(key))

	var commitToken string
	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return err
	}
	defer f.Close()

	if size <= uploadPartSize {
		buff := make([]byte, size)
		if _, err := io.ReadFull(f, buff); err != nil && err != io.EOF {
			return err
		}
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("token", upToken)
		_ = mw.WriteField("key", key)
		_ = mw.WriteField("fname", ui.Info.Name)
		fw, err := mw.CreateFormFile("file", ui.Info.Name)
		if err != nil {
			return err
		}
		if _, err := fw.Write(buff); err != nil {
			return err
		}
		if err := mw.Close(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://upload.qiniup.com/", &body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := netx.DoUpload(httpClient, req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("上传失败 HTTP %d", resp.StatusCode)
		}
		var j map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
			return errors.New("上传失败: 七牛响应异常")
		}
		commitToken = responseString(j, "token")
		ui.ReportUploadProgress(size, size)
	} else {
		initURL := fmt.Sprintf("https://upload.qiniup.com/buckets/%s/objects/%s/uploads", ILANZOU_CONF.Bucket, keyB64)
		if saved.UploadID == "" {
			j, status, initErr := qiniuJSON(ctx, http.MethodPost, initURL, upToken, nil)
			if initErr != nil {
				return initErr
			}
			if status >= 400 {
				return fmt.Errorf("初始化分片上传失败 HTTP %d", status)
			}
			saved = qiniuUploadState{Key: key, UploadID: responseString(j, "uploadId")}
			if saved.UploadID == "" {
				return errors.New("初始化分片上传失败")
			}
			if err := saveQiniuUploadState(sessionKey, saved); err != nil {
				return err
			}
		}
		parts := make([]qiniuPart, 0, len(saved.Parts))
		uploaded := make(map[int]string, len(saved.Parts))
		for _, part := range saved.Parts {
			if part.PartNumber > 0 && part.Etag != "" {
				uploaded[part.PartNumber] = part.Etag
			}
		}
		partNum := int((size + uploadPartSize - 1) / uploadPartSize)
		for i := 1; i <= partNum; i++ {
			start := int64(i-1) * uploadPartSize
			cur := int64(uploadPartSize)
			if size-start < cur {
				cur = size - start
			}
			if etag := uploaded[i]; etag != "" {
				parts = append(parts, qiniuPart{PartNumber: i, Etag: etag})
				ui.ReportUploadProgress(start+cur, size)
				continue
			}
			buff := make([]byte, cur)
			if _, err := f.ReadAt(buff, start); err != nil && err != io.EOF {
				return err
			}
			partURL := fmt.Sprintf("%s/%s/%d", initURL, saved.UploadID, i)
			pj, status, err := qiniuPut(ctx, partURL, upToken, buff)
			if err != nil {
				return err
			}
			if status >= 400 {
				if status == http.StatusNotFound || status == http.StatusGone || status == 612 {
					drive.ClearUploadSession(sessionKey)
				}
				return fmt.Errorf("分片上传失败 %d", status)
			}
			etag := responseString(pj, "etag")
			if etag == "" {
				return errors.New("分片上传未返回 etag")
			}
			parts = append(parts, qiniuPart{PartNumber: i, Etag: etag})
			saved.Parts = append(saved.Parts, qiniuPart{PartNumber: i, Etag: etag})
			if err := saveQiniuUploadState(sessionKey, saved); err != nil {
				return err
			}
			ui.ReportUploadProgress(start+cur, size)
		}
		finURL := fmt.Sprintf("%s/%s", initURL, saved.UploadID)
		fj, status, err := qiniuJSON(ctx, http.MethodPost, finURL, upToken, map[string]any{
			"fnmae": ui.Info.Name, // 服务端字段拼写即 fnmae（历史沿用）
			"parts": parts,
		})
		if err != nil {
			return err
		}
		if status >= 400 {
			if status == http.StatusNotFound || status == http.StatusGone || status == 612 {
				drive.ClearUploadSession(sessionKey)
			}
			return fmt.Errorf("完成分片上传失败 HTTP %d", status)
		}
		commitToken = responseString(fj, "token")
		if commitToken != "" {
			drive.ClearUploadSession(sessionKey)
		}
	}
	if commitToken == "" {
		return errors.New("上传完成令牌为空")
	}
	ui.ReportUploadProgress(size, size)

	for i := 0; i < 10; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		results, _, err := d.request(ctx, c, "/7n/results", requestOptions{
			method:   http.MethodPost,
			unproved: true,
			query: map[string]string{
				"tokenList": commitToken,
				"tokenTime": formatGoTokenTime(time.Now()),
			},
		})
		if err != nil {
			return err
		}
		if row := firstResultRow(results); row != nil && numOf(row["status"]) == 1 {
			markUploadDone(ui, size, responseString(row, "fileId"))
			return nil
		}
		if i == 9 {
			break
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("上传确认超时")
}

func firstRow(v any) map[string]any {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	if m, ok := arr[0].(map[string]any); ok {
		return m
	}
	return nil
}

func firstResultRow(j map[string]any) map[string]any {
	if row := firstRow(j["list"]); row != nil {
		return row
	}
	if data := mapVal(j, "data"); data != nil {
		return firstRow(data["list"])
	}
	return nil
}

func responseString(j map[string]any, key string) string {
	value := strOf(j[key])
	if data := mapVal(j, "data"); data != nil {
		value = firstNonEmpty(value, strOf(data[key]))
	}
	return value
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && strOf(v) != "" {
			return v
		}
	}
	return ""
}

func markUploadDone(ui *model.UploadingUI, size int64, fileID string) {
	ui.ReportUploadProgress(size, size)
	ui.Upload.IsDowning = false
	ui.Upload.IsCompleted = true
	ui.Upload.DownState = "completed"
	if fileID != "" {
		ui.Upload.FileID = fileID
	}
}
