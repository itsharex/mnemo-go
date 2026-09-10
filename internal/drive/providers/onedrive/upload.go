package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// Graph simple upload limit (4 MiB).
const smallUploadLimit = 4 * 1024 * 1024

// Upload session chunk size: multiple of 320 KiB per Graph rules.
const sessionChunkSize = 10 * 1024 * 1024

func encodePathSegment(v string) string {
	return url.PathEscape(v)
}

// smallUploadPath builds the PUT content path for small files.
func smallUploadPath(parentID, fileName string) string {
	name := encodePathSegment(fileName)
	if parentID == "" || parentID == RootID {
		return "/me/drive/root:/" + name + ":/content"
	}
	return "/me/drive/items/" + url.PathEscape(parentID) + ":/" + name + ":/content"
}

// rawPut performs a PUT with an auth header.
func (c *client) rawPut(ctx context.Context, target string, body io.Reader) (string, error) {
	resp, err := c.http.DoUpload(ctx, http.MethodPut, target, c.headers(map[string]string{"Content-Type": "application/octet-stream"}), body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", graphError(data, resp.StatusCode)
	}
	var item struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &item)
	if strings.TrimSpace(item.ID) == "" {
		return "", errors.New("onedrive: upload response missing file id")
	}
	return item.ID, nil
}

// sessionUpload uploads a file via a Graph upload session with chunked PUTs,
// reporting progress through the upload UI model.
func (c *client) sessionUpload(ctx context.Context, dc drive.Context, f *os.File, parentID, name string, ui *model.UploadingUI, conflictBehavior string) error {
	if ui == nil {
		return errors.New("onedrive: 上传任务为空")
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()

	identity := strings.TrimSpace(ui.Info.SHA1)
	if identity == "" {
		identity = fmt.Sprintf("mtime:%d", info.ModTime().UnixNano())
	}
	// Include content identity and conflict behavior so a same-size replacement
	// or a changed policy cannot resume an unrelated Graph session.
	sessionName := name + "\x00" + conflictBehavior + "\x00" + identity
	sessionKey := drive.UploadSessionKey(dc.UserID, dc.DriveID, parentID, sessionName, size)
	// Reuse the remote session when its URL is still alive. A byte offset alone
	// is not enough because a newly-created Graph session has no uploaded data.
	savedSessionID, _ := drive.LoadUploadSessionState(sessionKey)
	var sess *UploadSessionItem
	var pos int64
	if savedSessionID != "" {
		candidate := &UploadSessionItem{UploadURL: savedSessionID}
		if qPos, qFile, completed, usable := querySessionPosition(ctx, candidate.UploadURL, c); usable {
			sess, pos = candidate, qPos
			if completed {
				if ui != nil {
					ui.Upload.FileID = qFile
				}
				drive.ClearUploadSession(sessionKey)
				return nil
			}
		}
	}
	if sess == nil {
		sess, err = c.CreateUploadSession(ctx, parentID, name, conflictBehavior)
		if err != nil {
			return err
		}
	}
	if sess == nil || strings.TrimSpace(sess.UploadURL) == "" {
		return errors.New("onedrive: upload session url missing")
	}

	// Query a newly-created session too; this keeps the handling consistent
	// when the provider returns an already-initialized session.
	if pos == 0 {
		if qPos, qFile, completed, usable := querySessionPosition(ctx, sess.UploadURL, c); usable {
			if completed {
				if ui != nil {
					ui.Upload.FileID = qFile
				}
				drive.ClearUploadSession(sessionKey)
				return nil
			}
			if qPos > 0 && qPos <= size {
				pos = qPos
			}
		}
	}

	buf := make([]byte, sessionChunkSize)
	for pos < size {
		n, err := f.ReadAt(buf, pos)
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			break
		}
		chunk := buf[:n]
		end := pos + int64(n) - 1
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, sess.UploadURL, bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Range", "bytes "+strconv.FormatInt(pos, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
		req.Header.Set("Content-Length", strconv.Itoa(n))
		resp, err := netx.DoUpload(c.http.HTTP, req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != 201 && resp.StatusCode != 202 && resp.StatusCode != 200 {
			// 416 → already uploaded; query position to advance
			if resp.StatusCode == 416 {
				if qPos, qFile, completed, usable := querySessionPosition(ctx, sess.UploadURL, c); usable && completed {
					if ui != nil {
						ui.Upload.FileID = qFile
					}
					drive.ClearUploadSession(sessionKey)
					return nil
				} else if usable && qPos > pos && qPos <= size {
					pos = qPos
					continue
				}
				return errors.New("onedrive: upload session rejected range and position is unknown")
			}
			return graphError(body, resp.StatusCode)
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			var item struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(body, &item) == nil && item.ID != "" {
				ui.Upload.FileID = item.ID
			}
		}
		pos += int64(n)
		// Persist progress incrementally (byte offset as single-element part list)
		_ = drive.SaveUploadSessionState(sessionKey, sess.UploadURL, []int{int(pos)})
		if ui != nil {
			ui.ReportUploadProgress(pos, size)
		}
	}
	drive.ClearUploadSession(sessionKey)
	return nil
}

// querySessionPosition queries the Graph upload session URL to discover the
// next expected byte range (resumable upload). Returns position, fileId (when
// the session is already complete), and a completed flag.
func querySessionPosition(ctx context.Context, uploadURL string, c *client) (pos int64, fileID string, completed, usable bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uploadURL, nil)
	if err != nil {
		return 0, "", false, false
	}
	resp, err := c.http.HTTP.Do(req)
	if err != nil {
		return 0, "", false, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// 200/201/202 with a file id = session complete
	if resp.StatusCode == 200 || resp.StatusCode == 201 || resp.StatusCode == 202 {
		var done struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body, &done) == nil && done.ID != "" {
			return 0, done.ID, true, true
		}
	}
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return 0, "", false, false
	}
	var data struct {
		NextExpectedRanges []string `json:"nextExpectedRanges"`
	}
	if json.Unmarshal(body, &data) != nil {
		return 0, "", false, true
	}
	if len(data.NextExpectedRanges) == 0 {
		return 0, "", false, true
	}
	first := data.NextExpectedRanges[0]
	if idx := indexByte(first, '-'); idx > 0 {
		first = first[:idx]
	}
	var p int64
	fmt.Sscanf(first, "%d", &p)
	if p > 0 {
		return p, "", false, true
	}
	return 0, "", false, true
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

var _ = json.Marshal
var _ = fmt.Sprint
var _ = errors.New
