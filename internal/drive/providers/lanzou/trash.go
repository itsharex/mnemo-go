package lanzou

import (
	"context"
	"errors"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
)

var recycleFormHash = regexp.MustCompile(`name=["']formhash["']\s+value=["']([^"']+)["']`)

func (d *Driver) recycleRequest(ctx context.Context, c drive.Context, method string, query, form url.Values) (string, error) {
	cookie, _, _, baseURL := sessionOf(c)
	if cookie == "" {
		return "", drive.ErrUnauthorized
	}
	target := strings.TrimSuffix(baseURL, "/") + "/mydisk.php?" + query.Encode()
	var body []byte
	headers := map[string]string{"referer": strings.TrimSuffix(baseURL, "/") + "/mydisk.php", "user-agent": LANZOU_DEFAULT.UserAgent}
	if method == http.MethodPost {
		body = []byte(form.Encode())
		headers["content-type"] = "application/x-www-form-urlencoded"
	}
	result, err := fetchText(ctx, method, target, headers, body, cookie, false)
	if err != nil {
		return "", err
	}
	if result.status < 200 || result.status >= 300 {
		return "", fmt.Errorf("蓝奏回收站 HTTP %d", result.status)
	}
	return result.text, nil
}

func (d *Driver) ListTrash(ctx context.Context, c drive.Context, _ *drive.ListOptions) ([]model.File, error) {
	page, err := d.recycleRequest(ctx, c, http.MethodGet, url.Values{"item": {"recycle"}, "action": {"files"}}, nil)
	if err != nil {
		return nil, err
	}
	if !recycleFormHash.MatchString(page) {
		return nil, errors.New("蓝奏回收站页面无效，请检查登录状态")
	}
	return parseLanzouRecycle(page, c.DriveID)
}

func parseLanzouRecycle(page, driveID string) ([]model.File, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, err
	}
	items := make([]model.File, 0)
	seen := make(map[string]bool)
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "tr" {
			id, name, isDir := recycleRow(node)
			if id != "" && name != "" {
				prefix := "file:"
				if isDir {
					prefix = "folder:"
				}
				key := prefix + id
				if !seen[key] {
					seen[key] = true
					items = append(items, driveutil.NewFile(driveID, key, "trash", name, isDir, 0, 0))
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	return items, nil
}

func recycleRow(row *html.Node) (id, name string, isDir bool) {
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			attrs := make(map[string]string, len(node.Attr))
			for _, attr := range node.Attr {
				attrs[attr.Key] = attr.Val
			}
			if node.Data == "input" {
				if strings.HasPrefix(attrs["name"], "fl_sel_ids") || strings.HasPrefix(attrs["name"], "fd_sel_ids") {
					id = attrs["value"]
					isDir = strings.HasPrefix(attrs["name"], "fd_sel_ids")
				}
			}
			if node.Data == "a" {
				if id == "" {
					if link, err := url.Parse(attrs["href"]); err == nil && link.Query().Get("folder_id") != "" {
						id = link.Query().Get("folder_id")
						isDir = true
					}
				}
				if name == "" {
					name = strings.TrimSpace(recycleText(node))
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(row)
	return id, name, isDir
}

func recycleText(node *html.Node) string {
	if node.Type == html.TextNode {
		return stdhtml.UnescapeString(node.Data)
	}
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		text.WriteString(recycleText(child))
	}
	return text.String()
}

func (d *Driver) Restore(ctx context.Context, c drive.Context, fileIDs []string) ([]string, error) {
	var completed []string
	var failures []error
	for _, id := range fileIDs {
		action, key, value := "file_restore", "file_id", strings.TrimPrefix(id, "file:")
		if strings.HasPrefix(id, "folder:") {
			action, key, value = "folder_restore", "folder_id", strings.TrimPrefix(id, "folder:")
		}
		if value == "" || strings.ContainsAny(value, "?&/") {
			failures = append(failures, fmt.Errorf("%s: 回收站文件 ID 无效", id))
			continue
		}
		page, err := d.recycleRequest(ctx, c, http.MethodGet, url.Values{"item": {"recycle"}, "action": {action}, key: {value}}, nil)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		match := recycleFormHash.FindStringSubmatch(page)
		if len(match) != 2 {
			failures = append(failures, fmt.Errorf("%s: 无法获取蓝奏回收站表单", id))
			continue
		}
		response, err := d.recycleRequest(ctx, c, http.MethodPost, url.Values{"item": {"recycle"}}, url.Values{"action": {action}, "task": {action}, key: {value}, "formhash": {match[1]}})
		if err != nil || !strings.Contains(response, "恢复成功") {
			failures = append(failures, fmt.Errorf("%s: 蓝奏回收站恢复失败: %v", id, err))
			continue
		}
		completed = append(completed, id)
	}
	return completed, errors.Join(failures...)
}
