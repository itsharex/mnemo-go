package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/store"
	"os"
	"strings"
	"time"
)

type MigrationPreview struct {
	Files     int      `json:"files"`
	Bytes     int64    `json:"bytes"`
	Conflicts []string `json:"conflicts"`
	Warnings  []string `json:"warnings"`
}

func (a *App) PreviewMigration(srcUser, srcDrive, dstUser, dstDrive, dstParent string, ids []string) (*MigrationPreview, error) {
	ctx, cancel := context.WithTimeout(a.appContext(), 60*time.Second)
	defer cancel()
	out := &MigrationPreview{Conflicts: []string{}, Warnings: []string{}}
	items := []drive.UploadValidationItem{}
	seen := map[string]bool{}
	var walk func(string, string) error
	walk = func(id, parent string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[id] {
			return fmt.Errorf("来源包含重复或循环目录")
		}
		seen[id] = true
		if len(seen) > 20000 {
			return fmt.Errorf("预检超过 20000 项，请分批迁移")
		}
		file, err := drive.GetFileContext(ctx, srcUser, srcDrive, id)
		if err != nil {
			return err
		}
		if file == nil {
			return fmt.Errorf("来源文件不存在")
		}
		if parent != "" {
			existing, err := drive.ListDirAllContext(ctx, dstUser, dstDrive, parent, nil)
			if err != nil {
				return err
			}
			for _, target := range existing {
				if target.Name == file.Name {
					out.Conflicts = append(out.Conflicts, file.Name)
					break
				}
			}
		}
		if !file.IsDir {
			out.Files++
			out.Bytes += file.Size
			items = append(items, drive.UploadValidationItem{Name: file.Name, Size: file.Size})
			return nil
		}
		children, err := drive.ListDirAllContext(ctx, srcUser, srcDrive, id, nil)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := walk(child.FileID, ""); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range ids {
		if err := walk(id, dstParent); err != nil {
			return nil, err
		}
	}
	if err := drive.ValidateUploadItems(dstUser, dstDrive, items); err != nil {
		return nil, err
	}
	if st, err := a.storeOrError(); err == nil {
		if account, err := st.GetAccount(dstUser); err == nil && account != nil && account.Usage != nil && account.Usage.Size > 0 {
			if account.Usage.Size-account.Usage.Used < out.Bytes {
				out.Warnings = append(out.Warnings, "按最近容量记录，目标剩余空间可能不足")
			}
		}
	}
	return out, nil
}

type MigrationVerification struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// VerifyMigration is read-only: a verification never overwrites job checkpoints.
func (a *App) VerifyMigration(id string) ([]MigrationVerification, error) {
	st, err := a.storeOrError()
	if err != nil {
		return nil, err
	}
	jobs, err := st.ListMigrateJobs()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.appContext(), 2*time.Minute)
	defer cancel()
	for _, job := range jobs {
		if job.ID != id {
			continue
		}
		if job.Status == "running" || job.Status == "pending" {
			return nil, fmt.Errorf("请等待迁移结束后校验")
		}
		out := []MigrationVerification{}
		for _, item := range job.Items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if item.Status != "completed" || item.IsDir {
				continue
			}
			result := MigrationVerification{ID: item.ID, Status: "unverified", Detail: "无法唯一确定目标文件"}
			if item.TargetID == "" {
				result.Detail = "上传接口未返回目标 ID，无法可靠校验"
				out = append(out, result)
				continue
			}
			target, err := drive.GetFileContext(ctx, job.DstUser, job.DstDrive, item.TargetID)
			if err != nil || target == nil {
				out = append(out, result)
				continue
			}
			if target.Size != item.Size {
				result.Status = "mismatch"
				result.Detail = "目标大小与迁移记录不同"
				out = append(out, result)
				continue
			}
			result.Status = "size"
			result.Detail = "目标大小一致；未比较内容哈希"
			srcCaps := drive.RegistryCaps(drive.ProviderOf(job.SrcUser, job.SrcDrive, ""))
			dstCaps := drive.RegistryCaps(drive.ProviderOf(job.DstUser, job.DstDrive, ""))
			for _, method := range srcCaps.ProvideHashes {
				supported := false
				for _, candidate := range dstCaps.ProvideHashes {
					if candidate == method {
						supported = true
						break
					}
				}
				if !supported {
					continue
				}
				sourceHash, srcErr := drive.ResolveTransferHashContext(ctx, job.SrcUser, job.SrcDrive, item.ID, method, false)
				targetHash, dstErr := drive.ResolveTransferHashContext(ctx, job.DstUser, job.DstDrive, target.FileID, method, false)
				if srcErr != nil || dstErr != nil || sourceHash == "" || targetHash == "" {
					continue
				}
				if strings.EqualFold(sourceHash, targetHash) {
					result.Status = "hash"
					result.Detail = method + " 哈希一致"
				} else {
					result.Status = "mismatch"
					result.Detail = method + " 哈希不一致"
				}
				break
			}
			out = append(out, result)
		}
		return out, nil
	}
	return nil, fmt.Errorf("迁移任务不存在")
}

func (a *App) SearchCachedFiles(keyword string) ([]store.CachedSearchResult, error) {
	st, err := a.storeOrError()
	if err != nil {
		return nil, err
	}
	return st.SearchDirectoryCache(keyword)
}

// Preferences are supplied by the UI whitelist; account credentials are never read.
func (a *App) ExportPreferences(payload string) (string, error) {
	if len(payload) > 16*1024*1024 || !json.Valid([]byte(payload)) {
		return "", fmt.Errorf("备份格式无效或超过 16 MB")
	}
	ctx, ok := a.wailsContext()
	if !ok {
		return "", fmt.Errorf("桌面窗口未初始化")
	}
	path, err := runtime.SaveFileDialog(ctx, runtime.SaveDialogOptions{Title: "导出偏好", DefaultFilename: "mnemo-preferences.json", Filters: []runtime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}}})
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, []byte(payload), 0600)
}

func (a *App) ImportPreferences() (string, error) {
	ctx, ok := a.wailsContext()
	if !ok {
		return "", fmt.Errorf("桌面窗口未初始化")
	}
	path, err := runtime.OpenFileDialog(ctx, runtime.OpenDialogOptions{Title: "恢复偏好", Filters: []runtime.FileFilter{{DisplayName: "JSON", Pattern: "*.json"}}})
	if err != nil || path == "" {
		return "", err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if stat.Size() > 16*1024*1024 {
		return "", fmt.Errorf("备份超过 16 MB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("备份格式无效")
	}
	return string(data), nil
}
