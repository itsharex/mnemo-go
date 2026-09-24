package pan139

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

func (d *Driver) familyDirectoryPath(ctx context.Context, account drive.Context, catalogID string) (string, error) {
	if _, err := d.discoverPan139FamilyCloudID(ctx, account); err != nil {
		return "", err
	}
	raw, err := d.familyPost(ctx, account, "/orchestration/familyCloud-rebuild/content/v1.2/queryContentList", pan139FamilyPayload(account, map[string]any{
		"catalogID":       catalogID,
		"contentSortType": 0,
		"sortDirection":   1,
		"pageInfo":        map[string]int{"pageNum": 1, "pageSize": 1},
	}))
	if err != nil {
		return "", err
	}
	var response struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("移动云盘家庭云目录路径响应无效: %w", err)
	}
	if strings.TrimSpace(response.Path) == "" {
		return "", errors.New("移动云盘家庭云目录路径为空")
	}
	return response.Path, nil
}

func (d *Driver) familyMkdir(ctx context.Context, account drive.Context, parentID, name string) (*drive.MkdirResult, error) {
	if strings.TrimSpace(name) == "" {
		return &drive.MkdirResult{Error: "文件夹名称不能为空"}, nil
	}
	path, err := d.familyDirectoryPath(ctx, account, parentID)
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	raw, err := d.familyPost(ctx, account, "/orchestration/familyCloud-rebuild/cloudCatalog/v1.0/createCloudDoc", map[string]any{
		"cloudID":     pan139FamilyCloudID(account),
		"docLibName":  name,
		"catalogType": 3,
		"path":        path,
		"commonAccountInfo": map[string]any{
			"account": accountOf(account), "accountType": 1,
		},
	})
	if err != nil {
		return &drive.MkdirResult{Error: err.Error()}, nil
	}
	var response struct {
		CatalogInfo struct {
			CatalogID pan139FlexString `json:"catalogID"`
		} `json:"catalogInfo"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return &drive.MkdirResult{Error: fmt.Sprintf("移动云盘家庭云创建文件夹响应无效: %v", err)}, nil
	}
	id := strings.TrimSpace(response.CatalogInfo.CatalogID.String())
	if id == "" || id == "root" {
		return &drive.MkdirResult{Error: "移动云盘家庭云未返回新文件夹 ID，请刷新列表确认结果"}, nil
	}
	return &drive.MkdirResult{FileID: pan139FileID(pan139FamilySpace, id)}, nil
}

func (d *Driver) familyUpload(ctx context.Context, account drive.Context, upload *model.UploadingUI, parentID string) error {
	file, err := os.Open(upload.Info.LocalFilePath)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	size := stat.Size()
	if size == 0 {
		return errors.New("移动云盘家庭云暂不支持上传空文件")
	}
	upload.Info.Size = size
	directoryPath, err := d.familyDirectoryPath(ctx, account, parentID)
	if err != nil {
		return err
	}
	raw, err := d.familyPost(ctx, account, "/orchestration/familyCloud-rebuild/content/v1.0/getFileUploadURL", pan139FamilyPayload(account, map[string]any{
		"fileCount": 1, "manualRename": 2, "operation": 0,
		"path": directoryPath, "seqNo": randomHex(16), "totalSize": size,
		"uploadContentList": []map[string]any{{"contentName": upload.Info.Name, "contentSize": size}},
	}))
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			ResultCode pan139FlexString `json:"resultCode"`
			ResultDesc any              `json:"resultDesc"`
		} `json:"result"`
		UploadResult struct {
			TaskID         string `json:"uploadTaskID"`
			RedirectionURL string `json:"redirectionUrl"`
		} `json:"uploadResult"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("移动云盘家庭云上传初始化响应无效: %w", err)
	}
	if response.Result.ResultCode.String() != "0" || response.UploadResult.TaskID == "" || response.UploadResult.RedirectionURL == "" {
		return fmt.Errorf("移动云盘家庭云上传初始化失败（code=%s）：%v", response.Result.ResultCode.String(), response.Result.ResultDesc)
	}
	partSize := pan139UploadPartSize
	if size > pan139LargeUploadThreshold {
		partSize = pan139LargePartSize
	}
	client := netx.NewClient(90 * time.Second)
	for start := int64(0); start < size; start += partSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+partSize, size)
		body := io.NewSectionReader(file, start, end-start)
		request, err := client.Req(ctx, http.MethodPost, response.UploadResult.RedirectionURL, body)
		if err != nil {
			return err
		}
		request.ContentLength = end - start
		quotedName := strconv.QuoteToASCII(upload.Info.Name)
		request.Header.Set("Content-Type", "text/plain;name="+quotedName[1:len(quotedName)-1])
		request.Header.Set("contentSize", strconv.FormatInt(size, 10))
		request.Header.Set("range", fmt.Sprintf("bytes=%d-%d", start, end-1))
		request.Header.Set("uploadtaskID", response.UploadResult.TaskID)
		request.Header.Set("rangeType", "0")
		result, err := netx.DoUpload(client.HTTP, request)
		if err != nil {
			return err
		}
		var receipt struct {
			ResultCode *int   `xml:"resultCode"`
			Message    string `xml:"msg"`
		}
		decodeErr := xml.NewDecoder(io.LimitReader(result.Body, 256*1024)).Decode(&receipt)
		result.Body.Close()
		if result.StatusCode < 200 || result.StatusCode >= 300 {
			return fmt.Errorf("移动云盘家庭云上传失败 HTTP %d", result.StatusCode)
		}
		if decodeErr != nil {
			return fmt.Errorf("移动云盘家庭云上传回执无效: %w", decodeErr)
		}
		if receipt.ResultCode == nil {
			return errors.New("移动云盘家庭云上传回执缺少结果码")
		}
		if *receipt.ResultCode != 0 {
			return fmt.Errorf("移动云盘家庭云上传失败（code=%d）：%s", *receipt.ResultCode, receipt.Message)
		}
		upload.ReportUploadProgress(end, size)
	}
	return nil
}

func (d *Driver) familyRename(ctx context.Context, account drive.Context, fileID, rawID, name string) (*drive.RenameResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("移动云盘家庭云文件名不能为空")
	}
	file, ok := drive.CachedFile(account.UserID, account.DriveID, fileID)
	if !ok || strings.TrimSpace(file.Path) == "" {
		return nil, errors.New("移动云盘家庭云文件路径不可用，请刷新目录后重试")
	}
	if file.IsDir {
		return nil, drive.NotSupported("移动云盘家庭云文件夹重命名")
	}
	_, err := d.familyPost(ctx, account, "/orchestration/familyCloud-rebuild/photoContent/v1.0/modifyContentInfo", map[string]any{
		"contentID": rawID, "contentName": name, "path": file.Path,
		"commonAccountInfo": map[string]any{"account": accountOf(account), "accountType": 1},
	})
	if err != nil {
		return nil, err
	}
	return &drive.RenameResult{FileID: fileID, Name: name}, nil
}
