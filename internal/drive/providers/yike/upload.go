package yike

// 一刻上传 — 移植旧版 yike/upload.ts（AList baidu_photo Put 简化串行版）
// precreate → superfile2 分片 → create；目标为相册时再 add

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

const (
	yikeUploadPart = 1 << 22 // 4MiB
	yikeSlicePart  = 1 << 18 // 256KiB slice-md5
)

// getYikeTid mirrors legacy getYikeTid: 3 + unix 秒 + 7 位随机数。
func getYikeTid() string {
	return fmt.Sprintf("3%d%d", time.Now().Unix(), 1000000+time.Now().UnixNano()%9000000)
}

// parseAlbumID extracts the album id from an "album:" prefixed id.
func parseAlbumID(id string) string {
	if strings.HasPrefix(id, "album:") {
		return strings.TrimPrefix(id, "album:")
	}
	return ""
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// UploadOneFile mirrors legacy YikeUploadDisk.UploadOneFile.
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || ui.Info.LocalFilePath == "" {
		return errors.New("一刻相册：上传文件路径为空")
	}
	cl, err := clientOf(c)
	if err != nil {
		return err
	}

	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return errors.New("打开文件失败: " + err.Error())
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size <= 0 {
		return errors.New("不能上传 0 字节文件")
	}
	ui.Info.Size = size
	count := (size + yikeUploadPart - 1) / yikeUploadPart
	if count < 1 {
		count = 1
	}

	// 第一遍：全量 content-md5 + 每片 md5 列表 + 前 256KiB slice-md5
	contentH := md5.New()
	sliceH := md5.New()
	sliceLeft := int64(yikeSlicePart)
	blockList := make([]string, 0, count)
	for i := int64(0); i < count; i++ {
		if ui.IsUploadStopRequested() {
			return errors.New("已暂停")
		}
		start := i * yikeUploadPart
		cur := int64(yikeUploadPart)
		if size-start < cur {
			cur = size - start
		}
		buf := make([]byte, cur)
		if _, err := io.ReadFull(io.NewSectionReader(f, start, cur), buf); err != nil {
			return errors.New("读取上传文件失败: " + err.Error())
		}
		contentH.Write(buf)
		blockList = append(blockList, md5Hex(buf))
		if sliceLeft > 0 {
			take := sliceLeft
			if int64(len(buf)) < take {
				take = int64(len(buf))
			}
			sliceH.Write(buf[:take])
			sliceLeft -= take
		}
	}
	contentMD5 := hex.EncodeToString(contentH.Sum(nil))
	sliceMD5 := hex.EncodeToString(sliceH.Sum(nil))
	blockListJSON, _ := json.Marshal(blockList)
	pathName := "/" + ui.Info.Name

	form := url.Values{
		"autoinit":    {"1"},
		"isdir":       {"0"},
		"rtype":       {"1"},
		"ctype":       {"11"},
		"path":        {pathName},
		"size":        {strconv.FormatInt(size, 10)},
		"slice-md5":   {sliceMD5},
		"content-md5": {contentMD5},
		"block_list":  {string(blockListJSON)},
	}

	preURL := fileV1 + "/precreate"
	if cl.sess.Bdstoken != "" {
		preURL += "?bdstoken=" + url.QueryEscape(cl.sess.Bdstoken)
	}
	preRaw, err := cl.requestForm(ctx, preURL, nil, form)
	if err != nil {
		return err
	}
	var pre struct {
		ReturnType int    `json:"return_type"`
		UploadID   string `json:"uploadid"`
		BlockList  []int  `json:"block_list"`
		Data       struct {
			FsID int64  `json:"fsid"`
			Path string `json:"path"`
			Size int64  `json:"size"`
			MD5  string `json:"md5"`
		} `json:"data"`
	}
	if err := json.Unmarshal(preRaw, &pre); err != nil {
		return errors.New("一刻预上传响应解析失败")
	}

	albumID := parseAlbumID(ui.Info.ParentFileID)
	addToAlbum := func(fsid string) error {
		if albumID == "" || fsid == "" {
			return nil
		}
		listJSON, _ := json.Marshal([]map[string]string{{"fsid": fsid}})
		q := url.Values{
			"album_id": {albumID},
			"tid":      {getYikeTid()},
			"list":     {string(listJSON)},
		}
		if _, err := cl.request(ctx, http.MethodGet, albumAPI+"/addfile", q); err != nil {
			return errors.New("文件上传成功，但加入相册失败: " + err.Error())
		}
		return nil
	}

	if pre.ReturnType == 2 || pre.ReturnType == 3 {
		// 本地 MD5 命中秒传 / 已创建
		if pre.Data.FsID != 0 {
			if err := addToAlbum(strconv.FormatInt(pre.Data.FsID, 10)); err != nil {
				return err
			}
		}
		ui.ReportUploadProgress(size, size)
		return nil
	}

	if pre.ReturnType != 1 && pre.UploadID == "" {
		return errors.New("一刻预上传失败")
	}
	uploadID := pre.UploadID
	parts := pre.BlockList
	if len(parts) == 0 {
		parts = make([]int, count)
		for i := range parts {
			parts[i] = i
		}
	}

	for _, partseq := range parts {
		if partseq < 0 {
			continue
		}
		if ui.IsUploadStopRequested() {
			return errors.New("已暂停")
		}
		start := int64(partseq) * yikeUploadPart
		cur := int64(yikeUploadPart)
		if size-start < cur {
			cur = size - start
		}
		buf := make([]byte, cur)
		if _, err := io.ReadFull(io.NewSectionReader(f, start, cur), buf); err != nil {
			return errors.New("读取上传文件失败: " + err.Error())
		}

		q := url.Values{
			"method":   {"upload"},
			"path":     {pathName},
			"partseq":  {strconv.Itoa(partseq)},
			"uploadid": {uploadID},
			"app_id":   {"16051585"},
		}
		putURL := "https://c3.pcs.baidu.com/rest/2.0/pcs/superfile2?" + q.Encode()

		var formBuf bytes.Buffer
		mw := multipart.NewWriter(&formBuf)
		fw, err := mw.CreateFormFile("file", ui.Info.Name)
		if err != nil {
			return err
		}
		if _, err := fw.Write(buf); err != nil {
			return err
		}
		mw.Close()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, putURL, &formBuf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Cookie", cl.sess.Cookie)
		req.Header.Set("Referer", "https://photo.baidu.com/")
		req.Header.Set("User-Agent", ua)
		hc := &http.Client{Timeout: 5 * time.Minute}
		resp, err := netx.DoUpload(hc, req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("分片上传失败 HTTP %d", resp.StatusCode)
		}
		ui.ReportUploadProgress(start+cur, size)
	}

	form.Set("uploadid", uploadID)
	createURL := fileV1 + "/create"
	if cl.sess.Bdstoken != "" {
		createURL += "?bdstoken=" + url.QueryEscape(cl.sess.Bdstoken)
	}
	createdRaw, err := cl.requestForm(ctx, createURL, nil, form)
	if err != nil {
		return err
	}
	var created struct {
		Data struct {
			FsID int64 `json:"fsid"`
		} `json:"data"`
		FsID int64 `json:"fsid"`
	}
	_ = json.Unmarshal(createdRaw, &created)
	fsid := created.Data.FsID
	if fsid == 0 {
		fsid = created.FsID
	}
	if fsid == 0 {
		fsid = pre.Data.FsID
	}
	if fsid != 0 {
		if err := addToAlbum(strconv.FormatInt(fsid, 10)); err != nil {
			return err
		}
	}
	ui.ReportUploadProgress(size, size)
	return nil
}
