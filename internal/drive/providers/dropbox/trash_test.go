package dropbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

func TestDropboxTrashListsRestorableFilesAndRestoresCurrentRevision(t *testing.T) {
	var firstPage, continuation, restored int
	withDropboxTransport(t, dropboxRoundTripper(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		switch dropboxAPIPath(req) {
		case "/files/list_folder":
			firstPage++
			if body["recursive"] != true || body["include_deleted"] != true || body["path"] != "" {
				return nil, fmt.Errorf("unexpected list request: %v", body)
			}
			return dropboxResponse(req, 200, `{"entries":[{".tag":"deleted","name":"a.txt","path_lower":"/a.txt"},{".tag":"deleted","name":"目录","path_lower":"/folder"},{".tag":"file","name":"live.txt","path_lower":"/live.txt"}],"has_more":true,"cursor":"next"}`), nil
		case "/files/list_folder/continue":
			continuation++
			if body["cursor"] != "next" {
				return nil, fmt.Errorf("unexpected cursor: %v", body)
			}
			return dropboxResponse(req, 200, `{"entries":[{".tag":"deleted","name":"old.txt","path_lower":"/old.txt"},{".tag":"deleted","name":"expired.txt","path_lower":"/expired.txt"}],"has_more":false}`), nil
		case "/files/list_revisions":
			switch body["path"] {
			case "/a.txt":
				return dropboxResponse(req, 200, `{"is_deleted":true,"entries":[{"rev":"revision-a"}]}`), nil
			case "/folder":
				return dropboxResponse(req, 409, `{"error_summary":"path/not_file/"}`), nil
			case "/old.txt":
				return dropboxResponse(req, 200, `{"is_deleted":false,"entries":[{"rev":"old-revision"}]}`), nil
			case "/expired.txt":
				return dropboxResponse(req, 409, `{"error_summary":"path/not_found/"}`), nil
			default:
				return nil, fmt.Errorf("unexpected revision path: %v", body["path"])
			}
		case "/files/restore":
			restored++
			if body["path"] != "/a.txt" || body["rev"] != "revision-a" {
				return nil, fmt.Errorf("unexpected restore body: %v", body)
			}
			return dropboxResponse(req, 200, `{"id":"id:a","name":"a.txt"}`), nil
		default:
			return nil, fmt.Errorf("unexpected endpoint: %s", req.URL.Path)
		}
	}))
	c := drive.Context{DriveID: "dropbox:test", Token: &model.TokenInfo{AccessToken: "token"}}
	d := &Driver{}
	files, err := d.ListTrash(context.Background(), c, nil)
	if err != nil || len(files) != 1 || files[0].FileID != "/a.txt" || files[0].Name != "a.txt" || files[0].IsDir || firstPage != 1 || continuation != 1 {
		t.Fatalf("ListTrash() = %+v, %v, pages=%d/%d", files, err, firstPage, continuation)
	}
	ids, err := d.Restore(context.Background(), c, []string{files[0].FileID, "/old.txt"})
	if err == nil || !strings.Contains(err.Error(), "/old.txt") || len(ids) != 1 || ids[0] != "/a.txt" || restored != 1 {
		t.Fatalf("Restore() = %v, %v, restored=%d", ids, err, restored)
	}
}

func TestDropboxTrashRejectsRepeatedCursor(t *testing.T) {
	withDropboxTransport(t, dropboxRoundTripper(func(req *http.Request) (*http.Response, error) {
		return dropboxResponse(req, 200, `{"entries":[],"has_more":true,"cursor":"same"}`), nil
	}))
	c := drive.Context{Token: &model.TokenInfo{AccessToken: "token"}}
	if _, err := (&Driver{}).ListTrash(context.Background(), c, nil); err == nil || !strings.Contains(err.Error(), "游标") {
		t.Fatalf("expected repeated cursor error, got %v", err)
	}
}
