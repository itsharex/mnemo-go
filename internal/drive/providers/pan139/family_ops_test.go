package pan139

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

func testPan139FamilyContext() drive.Context {
	authorization := encodeAuthorization("pc", "13800138000", fmt.Sprintf("token|a|b|%d", time.Now().Add(30*24*time.Hour).UnixMilli()))
	return drive.Context{UserID: "pan139:test", DriveID: "pan139:test", Token: &model.TokenInfo{
		AccessToken: authorization,
		RefreshToken: mustJSON(map[string]string{
			"authorization": authorization, "account": "13800138000",
			"personalCloudHost": "https://personal.test", "familyCloudId": "family-1",
		}),
	}}
}

func TestPan139FamilyMkdirUsesDirectoryPath(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	var paths []string
	netx.TestTransportHook = pan139RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.Header.Get("X-Yun-Svc-Type") != "2" {
			return nil, fmt.Errorf("缺少家庭云签名: %s", request.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		switch request.URL.Path {
		case "/orchestration/familyCloud-rebuild/content/v1.2/queryContentList":
			if body["catalogID"] != "folder-1" {
				return nil, fmt.Errorf("目录 ID = %v", body["catalogID"])
			}
			return pan139Response(request, 200, nil, `{"success":true,"data":{"path":"/root/folder-1","cloudContentList":[],"totalCount":0}}`), nil
		case "/orchestration/familyCloud-rebuild/cloudCatalog/v1.0/createCloudDoc":
			if body["cloudID"] != "family-1" || body["docLibName"] != "新目录" || body["path"] != "/root/folder-1" {
				return nil, fmt.Errorf("创建参数错误: %+v", body)
			}
			return pan139Response(request, 200, nil, `{"success":true,"data":{"catalogInfo":{"catalogID":"folder-2"}}}`), nil
		default:
			return nil, fmt.Errorf("意外请求: %s", request.URL)
		}
	})
	result, err := (&Driver{}).Mkdir(context.Background(), testPan139FamilyContext(), pan139FileID(pan139FamilySpace, "folder-1"), "新目录")
	if err != nil || result == nil || result.Error != "" || result.FileID != pan139FileID(pan139FamilySpace, "folder-2") || len(paths) != 2 {
		t.Fatalf("创建目录 = %+v, %v, requests=%v", result, err, paths)
	}
}

func TestPan139FamilyMkdirRejectsMissingCatalogID(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	netx.TestTransportHook = pan139RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/queryContentList") {
			return pan139Response(request, 200, nil, `{"success":true,"data":{"path":"/root"}}`), nil
		}
		return pan139Response(request, 200, nil, `{"success":true,"data":{"catalogInfo":{}}}`), nil
	})
	result, err := (&Driver{}).Mkdir(context.Background(), testPan139FamilyContext(), Pan139FamilyRoot, "新目录")
	if err != nil || result == nil || result.FileID != "" || result.Error == "" {
		t.Fatalf("缺失目录 ID 被当作成功: %+v, %v", result, err)
	}
}

func TestPan139FamilyDownloadUsesListedPath(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	netx.TestTransportHook = pan139RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/orchestration/familyCloud-rebuild/content/v1.0/getFileDownLoadURL" {
			return nil, fmt.Errorf("意外请求: %s", request.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["contentID"] != "file-1" || body["path"] != "/root/folder-1" {
			return nil, fmt.Errorf("下载路径错误: %+v", body)
		}
		return pan139Response(request, 200, nil, `{"success":true,"data":{"downloadURL":"https://download.example/file","size":42}}`), nil
	})
	account := testPan139FamilyContext()
	fileID := pan139FileID(pan139FamilySpace, "file-1")
	drive.RememberFile(account.UserID, account.DriveID, model.File{FileID: fileID, Path: "/root/folder-1"})
	url, size, err := (&Driver{}).DownloadInfo(context.Background(), account, fileID)
	if err != nil || url != "https://download.example/file" || size != 42 {
		t.Fatalf("家庭云下载 = %q, %d, %v", url, size, err)
	}
}

func TestPan139FamilyDownloadRejectsMissingPath(t *testing.T) {
	account := testPan139FamilyContext()
	fileID := pan139FileID(pan139FamilySpace, "uncached-file")
	if _, _, err := (&Driver{}).DownloadInfo(context.Background(), account, fileID); err == nil || !strings.Contains(err.Error(), "刷新目录") {
		t.Fatalf("缺少目录路径时应要求刷新，而非发送空路径: %v", err)
	}
}

func TestPan139FamilyRenameFileUsesListedPath(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	var requests int
	netx.TestTransportHook = pan139RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Path != "/orchestration/familyCloud-rebuild/photoContent/v1.0/modifyContentInfo" {
			return nil, fmt.Errorf("意外请求: %s", request.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["contentID"] != "file-1" || body["contentName"] != "新名字.txt" || body["path"] != "/root/folder-1" {
			return nil, fmt.Errorf("重命名请求错误: %+v", body)
		}
		return pan139Response(request, 200, nil, `{"success":true,"data":{}}`), nil
	})
	account := testPan139FamilyContext()
	fileID := pan139FileID(pan139FamilySpace, "file-1")
	drive.RememberFile(account.UserID, account.DriveID, model.File{FileID: fileID, Path: "/root/folder-1"})
	result, err := (&Driver{}).Rename(context.Background(), account, fileID, "新名字.txt")
	if err != nil || result == nil || result.FileID != fileID || requests != 1 {
		t.Fatalf("文件重命名 = %+v, %v, requests=%d", result, err, requests)
	}
	folderID := pan139FileID(pan139FamilySpace, "folder-1")
	drive.RememberFile(account.UserID, account.DriveID, model.File{FileID: folderID, Path: "/root", IsDir: true})
	if _, err := (&Driver{}).Rename(context.Background(), account, folderID, "不能改名"); err == nil || requests != 1 {
		t.Fatalf("文件夹重命名不应误调用文件接口: %v", err)
	}
}

func TestPan139FamilyUploadUsesFamilyEndpointAndChecksReceipt(t *testing.T) {
	for _, testCase := range []struct {
		name, receipt string
		wantError     bool
	}{
		{"success", `<result><resultCode>0</resultCode><msg>ok</msg></result>`, false},
		{"failed receipt", `<result><resultCode>7</resultCode><msg>denied</msg></result>`, true},
		{"missing result code", `<result><msg>unknown</msg></result>`, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			previous := netx.TestTransportHook
			t.Cleanup(func() { netx.TestTransportHook = previous })
			var uploads int
			netx.TestTransportHook = pan139RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host == "upload.test" {
					uploads++
					body, err := io.ReadAll(request.Body)
					if err != nil || string(body) != "abc" || request.Method != http.MethodPost || request.Header.Get("range") != "bytes=0-2" || request.Header.Get("uploadtaskID") != "task-1" || request.Header.Get("Content-Type") != "text/plain;name=hello.txt" {
						return nil, fmt.Errorf("上传分片错误 body=%q, request=%+v, err=%v", body, request, err)
					}
					return pan139Response(request, 200, nil, testCase.receipt), nil
				}
				if strings.HasSuffix(request.URL.Path, "/queryContentList") {
					return pan139Response(request, 200, nil, `{"success":true,"data":{"path":"/root/folder-1"}}`), nil
				}
				if !strings.HasSuffix(request.URL.Path, "/getFileUploadURL") {
					return nil, fmt.Errorf("意外请求: %s", request.URL)
				}
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					return nil, err
				}
				if body["path"] != "/root/folder-1" || body["cloudID"] != "family-1" || body["totalSize"] != float64(3) || body["seqNo"] == "" {
					return nil, fmt.Errorf("家庭云上传初始化错误: %+v", body)
				}
				return pan139Response(request, 200, nil, `{"success":true,"data":{"result":{"resultCode":"0"},"uploadResult":{"uploadTaskID":"task-1","redirectionUrl":"https://upload.test/file"}}}`), nil
			})
			localPath := filepath.Join(t.TempDir(), "hello.txt")
			if err := os.WriteFile(localPath, []byte("abc"), 0600); err != nil {
				t.Fatal(err)
			}
			upload := &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: localPath, ParentFileID: pan139FileID(pan139FamilySpace, "folder-1"), Name: "hello.txt"}}
			err := (&Driver{}).UploadOneFile(context.Background(), testPan139FamilyContext(), upload)
			if (err != nil) != testCase.wantError || uploads != 1 {
				t.Fatalf("家庭云上传 err=%v, requests=%d", err, uploads)
			}
		})
	}
}

func TestPan139FamilyUploadRejectsEmptyFile(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(localPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	upload := &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: localPath, ParentFileID: Pan139FamilyRoot, Name: "empty.txt"}}
	if err := (&Driver{}).UploadOneFile(context.Background(), testPan139FamilyContext(), upload); err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Fatalf("空文件应在无效范围请求前拒绝: %v", err)
	}
}
