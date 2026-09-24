package e2e

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/config"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/providers/pan139"
	"mnemo-go/internal/drive/providers/pan189"
	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
)

func TestLiveFamilyReadOnly(t *testing.T) {
	if os.Getenv("MNEMO_LIVE_FAMILY_READONLY") != "1" {
		t.Skip("仅在明确启用只读家庭云诊断时运行")
	}
	configDir, err := config.UserConfigDir("Mnemo")
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := store.Open(configDir)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := accountStore.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	drive.SetTokenResolver(func(userID, driveID string) (*model.TokenInfo, error) {
		account, err := accountStore.GetAccount(userID)
		if err != nil {
			return nil, err
		}
		return drive.CloneToken(account.Token), nil
	})
	t.Cleanup(func() { drive.SetTokenResolver(nil) })
	for _, provider := range []string{model.ProviderPan139, model.ProviderPan189} {
		attempts := 0
		for _, account := range accounts {
			if account.Provider() != provider || account.Token == nil || account.Disabled {
				continue
			}
			attempts++
			token := *account.Token
			cloud := drive.Context{UserID: account.UserID, DriveID: account.DriveID, Token: &token}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			var listErr error
			itemCount := 0
			if provider == model.ProviderPan139 {
				items, _, err := (&pan139.Driver{}).ListPage(ctx, cloud, pan139.Pan139FamilyRoot, "")
				itemCount, listErr = len(items), err
			} else {
				items, err := (&pan189.Driver{}).List(ctx, cloud, pan189.PAN189FamilyRoot, nil)
				itemCount, listErr = len(items), err
				if err == nil {
					for _, item := range items {
						if item.IsDir {
							continue
						}
						drive.RememberFile(cloud.UserID, cloud.DriveID, item)
						link, linkErr := drive.GetDownloadURL(cloud.UserID, cloud.DriveID, item.FileID, 3600)
						if linkErr != nil || link == nil || link.Size != item.Size {
							t.Fatalf("天翼家庭文件经统一下载入口失败：%v，大小匹配=%t", linkErr, link != nil && link.Size == item.Size)
						}
						file, fileErr := drive.GetFile(cloud.UserID, cloud.DriveID, item.FileID)
						if fileErr != nil || file.Name != item.Name {
							t.Fatalf("天翼家庭文件元数据未传至预览入口: %v", fileErr)
						}
						break
					}
				}
			}
			cancel()
			if listErr != nil {
				t.Logf("%s 家庭云账号序号 %d: %s", provider, attempts, familyDiagnosticCode(listErr))
				if provider == model.ProviderPan139 && strings.Contains(listErr.Error(), "未加入任何家庭云") {
					rootItems, _, rootErr := (&pan139.Driver{}).ListPage(t.Context(), cloud, pan139.RootID, "")
					if rootErr != nil {
						t.Fatalf("移动云直达个人根目录失败: %s", familyDiagnosticCode(rootErr))
					}
					for _, item := range rootItems {
						if item.FileID == pan139.Pan139PersonalRoot || item.FileID == pan139.Pan139FamilyRoot {
							t.Fatal("未加入家庭云时仍显示虚拟空间目录")
						}
					}
					t.Logf("移动云直达个人根目录成功，条目数量=%d", len(rootItems))
				}
			} else {
				t.Logf("%s 家庭云账号序号 %d: 列表成功，文件数量=%d", provider, attempts, itemCount)
			}
		}
		t.Logf("%s 可诊断账号数: %d", provider, attempts)
	}
}

func familyDiagnosticCode(err error) string {
	message := err.Error()
	if codes := regexp.MustCompile(`(?:code|resultCode)=[A-Za-z0-9_-]+`).FindAllString(message, -1); len(codes) > 0 {
		return strings.Join(codes, ", ")
	}
	for _, phrase := range []string{"家庭云未知异常", "App Not Exist", "响应不是有效 JSON", "会话缺失", "未加入任何家庭云"} {
		if strings.Contains(message, phrase) {
			return phrase
		}
	}
	return "其它错误（已隐藏响应内容）"
}
