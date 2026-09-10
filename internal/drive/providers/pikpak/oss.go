package pikpak

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// pikpakOSSParams matches the snake_case fields returned by
// /drive/v1/files. It must stay separate from mounted-drive ConnConfig.
type pikpakOSSParams struct {
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	Key             string `json:"key"`
	SecurityToken   string `json:"security_token"`
}

// ossPut uploads a file to the Alibaba OSS endpoint provided by PikPak's
// create upload response. It uses the STS credentials (access_key, secret_key,
// security_token) to sign the PUT request.
func ossPut(ctx context.Context, c drive.Context, localPath string, params *pikpakOSSParams, ui *model.UploadingUI) error {
	if params == nil || params.Endpoint == "" || params.Bucket == "" || params.Key == "" || params.AccessKeyID == "" || params.AccessKeySecret == "" {
		return errors.New("pikpak: oss params missing")
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}

	accessKey := params.AccessKeyID
	secretKey := params.AccessKeySecret
	bucket := params.Bucket
	objectKey := params.Key
	securityToken := params.SecurityToken

	endpoint := strings.TrimRight(strings.TrimSpace(params.Endpoint), "/")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	url := fmt.Sprintf("https://%s.%s/%s", bucket, endpoint, strings.TrimPrefix(objectKey, "/"))
	contentType := mime.TypeByExtension(filepath.Ext(objectKey))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	date := time.Now().UTC().Format(http.TimeFormat)

	// Build OSS signature
	canonicalResource := "/" + bucket + "/" + strings.TrimPrefix(objectKey, "/")
	stringToSign := "PUT\n\n" + contentType + "\n" + date + "\nx-oss-security-token:" + securityToken + "\n" + canonicalResource

	mac := hmac.New(sha1.New, []byte(secretKey))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	authorization := "OSS " + accessKey + ":" + signature

	var body io.Reader = f
	if ui != nil {
		body = driveutil.NewProgressReader(f, info.Size(), func(read int64) {
			ui.ReportUploadProgress(read, info.Size())
		})
	}

	// Use netx so proxy and test transports apply to the OSS leg as well.
	hc := netx.NewClientWithSystemProxy(600 * time.Second)
	req, err := hc.Req(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Date", date)
	req.Header.Set("Authorization", authorization)
	req.Header.Set("x-oss-security-token", securityToken)
	req.ContentLength = info.Size()
	resp, err := hc.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pikpak: oss put %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if ui != nil {
		ui.ReportUploadProgress(info.Size(), info.Size())
		ui.Upload.IsCompleted = true
	}
	return nil
}
