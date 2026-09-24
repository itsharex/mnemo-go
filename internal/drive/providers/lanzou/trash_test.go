package lanzou

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrashListAndRestore(t *testing.T) {
	withNoThrottle(t)
	var restored string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mydisk.php" || r.Header.Get("Cookie") != "ylogin=test" {
			t.Errorf("unexpected recycle request: %s %s", r.URL, r.Header.Get("Cookie"))
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			restored = r.PostForm.Get("file_id")
			if r.PostForm.Get("formhash") != "token" || r.PostForm.Get("task") != "file_restore" || restored != "123" {
				t.Errorf("restore form: %v", r.PostForm)
			}
			fmt.Fprint(w, "恢复成功")
			return
		}
		if r.URL.Query().Get("action") == "file_restore" {
			fmt.Fprint(w, `<input name="formhash" value="token">`)
			return
		}
		fmt.Fprint(w, `<input name="formhash" value="token"><table><tr><td><input name="fl_sel_ids[]" value="123"></td><td><a href="#">report &amp; notes.txt</a></td></tr><tr><td><input name="fd_sel_ids[]" value="45"></td><td><a href="?item=recycle&amp;action=folder_restore&amp;folder_id=45">资料</a></td></tr></table>`)
	}))
	defer server.Close()
	ctx := testCtx("lanzou:test", server.URL, "ylogin=test")
	items, err := (&Driver{}).ListTrash(context.Background(), ctx, nil)
	if err != nil || len(items) != 2 || items[0].FileID != "file:123" || items[0].Name != "report & notes.txt" || items[1].FileID != "folder:45" || !items[1].IsDir {
		t.Fatalf("trash = %+v, %v", items, err)
	}
	ids, err := (&Driver{}).Restore(context.Background(), ctx, []string{items[0].FileID})
	if err != nil || strings.Join(ids, ",") != "file:123" || restored != "123" {
		t.Fatalf("restore = %v, %v", ids, err)
	}
}

func TestTrashSendsDeletionTask(t *testing.T) {
	withNoThrottle(t)
	var tasks []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/doupload.php" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		tasks = append(tasks, r.Form.Get("task"))
		_ = json.NewEncoder(w).Encode(map[string]any{"zt": 1})
	}))
	defer server.Close()
	ctx := testCtx("lanzou:test", server.URL, "ylogin=test")
	ids, err := (&Driver{}).Trash(t.Context(), ctx, []string{"file-1"})
	if err != nil || len(ids) != 1 || ids[0] != "file-1" || strings.Join(tasks, ",") != "6" {
		t.Fatalf("trash = %v, %v, tasks = %v", ids, err, tasks)
	}
}
