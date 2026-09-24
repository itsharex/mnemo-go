package pan189

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/config"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
	"mnemo-go/internal/preview"
	"mnemo-go/internal/store"
)

func TestLiveFamilySessionResponseReadOnly(t *testing.T) {
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
	for _, account := range accounts {
		if account.Provider() != model.ProviderPan189 || account.Token == nil || account.Disabled {
			continue
		}
		session := ParseSession(account.Token.RefreshToken)
		if session == nil {
			t.Fatal("家庭账号会话缺失")
		}
		t.Logf("已存会话：家庭键=%t，开放令牌=%t，可账密重登=%t", session.FamilySessionKey != "", session.AccessToken != "", session.Username != "" && session.Password != "")
		if session.AccessToken == "" {
			return
		}
		endpoint, _ := url.Parse(apiURL + "/getSessionForPC.action")
		query := endpoint.Query()
		for key, value := range clientSuffix() {
			query.Set(key, value)
		}
		query.Set("appId", appID)
		query.Set("accessToken", session.AccessToken)
		endpoint.RawQuery = query.Encode()
		client := netx.NewClient(15 * time.Second)
		response, err := client.Do(t.Context(), http.MethodGet, endpoint.String(), map[string]string{"Accept": "application/json", "User-Agent": ua189, "X-Request-ID": randomRequestID()}, nil)
		if err != nil {
			t.Fatalf("会话请求网络错误类型: %T", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		if err != nil {
			t.Fatal(err)
		}
		shape := "其它"
		trimmed := strings.TrimSpace(string(body))
		if json.Valid(body) {
			shape = "JSON"
		} else if strings.HasPrefix(trimmed, "<") {
			shape = "HTML/XML"
		} else if len(body) == 0 {
			shape = "空响应"
		}
		t.Logf("会话响应：HTTP=%d，类型=%q，编码=%q，最终主机=%q，格式=%s", response.StatusCode, response.Header.Get("Content-Type"), response.Header.Get("Content-Encoding"), response.Request.URL.Host, shape)
		if shape == "HTML/XML" {
			var parsed struct {
				XMLName             xml.Name `xml:"userSession"`
				SessionKey          string   `xml:"sessionKey"`
				SessionSecret       string   `xml:"sessionSecret"`
				FamilySessionKey    string   `xml:"familySessionKey"`
				FamilySessionSecret string   `xml:"familySessionSecret"`
			}
			_ = xml.Unmarshal(body, &parsed)
			t.Logf("XML 会话字段：个人=%t，家庭=%t", parsed.SessionKey != "" && parsed.SessionSecret != "", parsed.FamilySessionKey != "" && parsed.FamilySessionSecret != "")
			tags := map[string]bool{}
			decoder := xml.NewDecoder(bytes.NewReader(body))
			for {
				token, err := decoder.Token()
				if err != nil {
					break
				}
				if start, ok := token.(xml.StartElement); ok {
					tags[start.Name.Local] = true
				}
			}
			var names []string
			for name := range tags {
				names = append(names, name)
			}
			sort.Strings(names)
			t.Logf("XML 元素名称（不含内容）：%v", names)
		}
		return
	}
	t.Skip("未找到可用的天翼云盘账号")
}

func TestLiveFamilyDownloadReadOnly(t *testing.T) {
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
	for _, account := range accounts {
		if account.Provider() != model.ProviderPan189 || account.Token == nil || account.Disabled {
			continue
		}
		token := *account.Token
		cloud := drive.Context{UserID: account.UserID, DriveID: account.DriveID, Token: &token}
		provider := &Driver{}
		items, err := provider.List(t.Context(), cloud, PAN189FamilyRoot, nil)
		if err != nil {
			t.Fatalf("家庭目录读取失败: %T", err)
		}
		files := make([]model.File, 0)
		folders := 0
		for _, item := range items {
			if !item.IsDir {
				files = append(files, item)
				continue
			}
			folders++
			if folders > 3 {
				continue
			}
			children, err := provider.List(t.Context(), cloud, item.FileID, nil)
			if err != nil {
				t.Fatalf("家庭子目录读取失败: %T", err)
			}
			for _, child := range children {
				if !child.IsDir {
					files = append(files, child)
				}
			}
		}
		t.Logf("家庭根目录文件=%d，目录=%d；检测文件=%d", len(items)-folders, folders, len(files))
		for index, item := range files {
			if index >= 10 {
				break
			}
			link, err := provider.GetDownloadURL(t.Context(), cloud, item.FileID, 0)
			if err != nil {
				t.Fatalf("家庭文件下载链接获取失败: %s", err)
			}
			headers := map[string]string{"Range": "bytes=0-0"}
			for key, value := range link.Headers {
				headers[key] = value
			}
			client := netx.NewClient(15 * time.Second)
			response, err := client.Do(t.Context(), http.MethodGet, link.URL, headers, nil)
			if err != nil {
				t.Fatalf("家庭文件首字节请求失败: %T", err)
			}
			defer response.Body.Close()
			t.Logf("文件序号 %d 下载响应：HTTP=%d，类型=%q，范围=%q，最终主机=%q", index+1, response.StatusCode, response.Header.Get("Content-Type"), response.Header.Get("Content-Range"), response.Request.URL.Host)
			server, err := preview.NewServer()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			localURL, err := server.PlaybackURL(preview.PlaybackSource{URL: link.URL, Headers: link.Headers})
			if err != nil {
				t.Fatalf("本地预览地址创建失败: %T", err)
			}
			localResponse, err := client.Do(t.Context(), http.MethodGet, localURL, map[string]string{"Range": "bytes=0-0"}, nil)
			if err != nil {
				t.Fatalf("本地预览首字节请求失败: %T", err)
			}
			defer localResponse.Body.Close()
			t.Logf("预览响应：HTTP=%d，类型=%q，范围=%q", localResponse.StatusCode, localResponse.Header.Get("Content-Type"), localResponse.Header.Get("Content-Range"))
			if index == 0 && item.Size > 0 && item.Size <= 16<<20 {
				fullResponse, err := client.Do(t.Context(), http.MethodGet, link.URL, link.Headers, nil)
				if err != nil {
					t.Fatalf("完整文件读取失败: %T", err)
				}
				count, copyErr := io.Copy(io.Discard, fullResponse.Body)
				fullResponse.Body.Close()
				if copyErr != nil {
					t.Fatalf("文件流读取失败: %T", copyErr)
				}
				t.Logf("完整读取：HTTP=%d，声明大小=%d，实际字节=%d", fullResponse.StatusCode, item.Size, count)
			}
		}
		if len(files) == 0 {
			t.Skip("家庭根目录及首层子目录没有文件，未进行字节测试")
		}
		return
	}
	t.Skip("未找到可用的天翼云盘账号")
}
