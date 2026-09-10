package guangya

// 光鸭上传 — 移植旧版 guangya/upload.ts（AList：get_res_center_token → OSS multipart → wait task）

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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// calcPartSize mirrors legacy calcPartSize.
func calcPartSize(size int64) int64 {
	const mb = 1024 * 1024
	const gb = 1024 * mb
	switch {
	case size <= 100*mb:
		return 1 * mb
	case size <= 16*gb:
		return 2 * mb
	case size <= 160*gb:
		return 4 * mb
	default:
		return 8 * mb
	}
}

// normalizeEndpoint strips scheme-less values and a leading bucket. prefix
// (virtual-host style → path style base), mirroring legacy normalizeEndpoint.
func normalizeEndpoint(endpoint, bucket string) string {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return ""
	}
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		ep = "https://" + ep
	}
	if bucket != "" {
		if i := strings.Index(ep, "://"); i >= 0 {
			scheme, host := ep[:i+3], ep[i+3:]
			if strings.HasPrefix(host, bucket+".") {
				host = host[len(bucket)+1:]
			}
			ep = scheme + host
		}
	}
	return ep
}

// ossRegionFromEndpoint extracts oss-cn-xxx from an aliyuncs host.
func ossRegionFromEndpoint(endpoint string) string {
	if i := strings.Index(endpoint, "://"); i >= 0 {
		host := endpoint[i+3:]
		if j := strings.Index(host, "/"); j >= 0 {
			host = host[:j]
		}
		if k := strings.Index(strings.ToLower(host), ".aliyuncs.com"); k > 0 {
			if strings.HasPrefix(host, "oss-") {
				return host[:k]
			}
		}
	}
	return "oss-cn"
}

type resCenterToken struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		TaskID          string `json:"taskId"`
		ObjectPath      string `json:"objectPath"`
		BucketName      string `json:"bucketName"`
		EndPoint        string `json:"endPoint"`
		FullEndPoint    string `json:"fullEndPoint"`
		AccessKeyID     string `json:"accessKeyID"`
		SecretAccessKey string `json:"secretAccessKey"`
		SessionToken    string `json:"sessionToken"`
		Creds           *struct {
			AccessKeyID     string `json:"accessKeyID"`
			SecretAccessKey string `json:"secretAccessKey"`
			SessionToken    string `json:"sessionToken"`
		} `json:"creds"`
		UploadURL string `json:"uploadUrl"`
	} `json:"data"`
}

// waitUploadTask polls get_info_by_task_id until fileId appears (300×1s cap).
func waitUploadTask(ctx context.Context, c *client, taskID string) (string, error) {
	for i := 0; i < 300; i++ {
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				FileID string `json:"fileId"`
				Status int    `json:"status"`
			} `json:"data"`
		}
		if err := c.post(ctx, "/nd.bizuserres.s/v1/file/get_info_by_task_id", map[string]any{"taskId": taskID}, &out); err != nil {
			return "", err
		}
		if out.Data.FileID != "" {
			return out.Data.FileID, nil
		}
		allowed := map[int]bool{145: true, 146: true, 147: true, 155: true, 163: true, 0: true}
		if !allowed[out.Code] && out.Msg != "" && !strings.Contains(strings.ToLower(out.Msg), "success") {
			return "", errors.New(out.Msg)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", errors.New("上传任务超时")
}

// UploadOneFile mirrors legacy GuangyaUploadDisk.UploadOneFile.
func (d *Driver) UploadOneFile(ctx context.Context, c drive.Context, ui *model.UploadingUI) error {
	if ui == nil || ui.Info.LocalFilePath == "" {
		return errors.New("光鸭：上传文件路径为空")
	}
	cl, err := clientOf(c)
	if err != nil {
		return err
	}
	parentID := toID(ui.Info.ParentFileID)
	f, err := os.Open(ui.Info.LocalFilePath)
	if err != nil {
		return errors.New("打开文件失败: " + err.Error())
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	ui.Info.Size = info.Size()

	var tokenRes resCenterToken
	if err := cl.post(ctx, "/nd.bizuserres.s/v1/get_res_center_token", map[string]any{
		"capacity": 2,
		"name":     ui.Info.Name,
		"parentId": parentID,
		"res":      map[string]any{"fileSize": ui.Info.Size},
	}, &tokenRes); err != nil {
		return err
	}

	taskID := tokenRes.Data.TaskID
	if tokenRes.Code == 156 {
		// 秒传（服务端已有对象）
		if taskID == "" {
			return errors.New("秒传未返回 taskId")
		}
		if _, err := waitUploadTask(ctx, cl, taskID); err != nil {
			return err
		}
		ui.ReportUploadProgress(ui.Info.Size, ui.Info.Size)
		return nil
	}

	accessKey := firstNonEmptyStr(tokenRes.Data.AccessKeyID, credVal(tokenRes.Data.Creds, "id"))
	secretKey := firstNonEmptyStr(tokenRes.Data.SecretAccessKey, credVal(tokenRes.Data.Creds, "secret"))
	sessionToken := firstNonEmptyStr(tokenRes.Data.SessionToken, credVal(tokenRes.Data.Creds, "token"))
	bucket := tokenRes.Data.BucketName
	objectPath := tokenRes.Data.ObjectPath
	endpoint := normalizeEndpoint(firstNonEmptyStr(tokenRes.Data.FullEndPoint, tokenRes.Data.EndPoint), bucket)

	// 兼容旧直传 URL
	if (accessKey == "" || bucket == "" || objectPath == "") && tokenRes.Data.UploadURL != "" {
		return directPut(ctx, ui, f, tokenRes.Data.UploadURL, cl, taskID)
	}
	if accessKey == "" || secretKey == "" || bucket == "" || objectPath == "" || endpoint == "" {
		if tokenRes.Msg != "" {
			return errors.New(tokenRes.Msg)
		}
		return errors.New("上传凭证不完整")
	}
	return ossMultipart(ctx, ui, f, endpoint, bucket, objectPath, accessKey, secretKey, sessionToken, cl, taskID)
}

func credVal(c *struct {
	AccessKeyID     string `json:"accessKeyID"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
}, field string) string {
	if c == nil {
		return ""
	}
	switch field {
	case "id":
		return c.AccessKeyID
	case "secret":
		return c.SecretAccessKey
	default:
		return c.SessionToken
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// directPut is the legacy uploadUrl single-PUT fallback.
func directPut(ctx context.Context, ui *model.UploadingUI, f *os.File, uploadURL string, cl *client, taskID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, f)
	if err != nil {
		return err
	}
	req.ContentLength = ui.Info.Size
	hc := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
	}}
	resp, err := netx.DoUpload(hc, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("上传失败 HTTP %d", resp.StatusCode)
	}
	ui.ReportUploadProgress(ui.Info.Size, ui.Info.Size)
	if taskID != "" {
		if _, err := waitUploadTask(ctx, cl, taskID); err != nil {
			return err
		}
	}
	return nil
}

// ossMultipart uploads via S3 multipart (queueSize=2, partSize≥5MiB).
func ossMultipart(ctx context.Context, ui *model.UploadingUI, f *os.File, endpoint, bucket, key, ak, sk, token string, cl *client, taskID string) error {
	cli := s3.New(s3.Options{
		Region:       ossRegionFromEndpoint(endpoint),
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(ak, sk, token),
		UsePathStyle: false,
	})
	size := ui.Info.Size
	partSize := calcPartSize(size)
	if min := int64(5 * 1024 * 1024); partSize < min {
		partSize = min
	}

	mu, err := cli.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String("application/octet-stream"),
	})
	if err != nil {
		return errors.New("OSS 分片上传失败: " + err.Error())
	}
	uploadID := mu.UploadId
	abort := func() {
		_, _ = cli.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
			Bucket: aws.String(bucket), Key: aws.String(key), UploadId: uploadID,
		})
	}

	partCount := int((size + partSize - 1) / partSize)
	if partCount < 1 {
		partCount = 1
	}
	parts := make([]s3types.CompletedPart, partCount)
	sem := make(chan struct{}, 2) // queueSize 2
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var uploaded int64
	var progMu sync.Mutex
	fail := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}

	for i := 0; i < partCount; i++ {
		if ui.IsUploadStopRequested() {
			abort()
			return errors.New("已暂停")
		}
		idx := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			offset := int64(idx) * partSize
			cur := partSize
			if size-offset < cur {
				cur = size - offset
			}
			if cur <= 0 {
				cur = 0
			}
			body := io.NewSectionReader(f, offset, cur)
			out, err := cli.UploadPart(ctx, &s3.UploadPartInput{
				Bucket:     aws.String(bucket),
				Key:        aws.String(key),
				UploadId:   uploadID,
				PartNumber: aws.Int32(int32(idx + 1)),
				Body:       body,
			}, func(o *s3.Options) { o.HTTPClient = netx.UploadHTTPClient{Client: o.HTTPClient} })
			if err != nil {
				fail(err)
				return
			}
			parts[idx] = s3types.CompletedPart{
				ETag:       out.ETag,
				PartNumber: aws.Int32(int32(idx + 1)),
			}
			progMu.Lock()
			uploaded += cur
			ui.ReportUploadProgress(uploaded, size)
			progMu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		abort()
		if ui.IsUploadStopRequested() {
			return errors.New("已暂停")
		}
		return errors.New("OSS 分片上传失败: " + firstErr.Error())
	}

	if _, err := cli.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: uploadID,
		MultipartUpload: &s3types.CompletedMultipartUpload{
			Parts: parts,
		},
	}); err != nil {
		return errors.New("OSS 分片上传失败: " + err.Error())
	}
	ui.ReportUploadProgress(size, size)
	if taskID != "" {
		if _, err := waitUploadTask(ctx, cl, taskID); err != nil {
			return err
		}
	}
	return nil
}
