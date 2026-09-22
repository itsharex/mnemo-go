package lanzou

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"mnemo-go/internal/drive"
)

// downloadInfo is the resolved download result (mirrors the legacy shape).
type downloadInfo struct {
	URL     string
	Size    int64
	Error   string
	Headers map[string]string
}

// shareDownload is the resolved share-page download.
type shareDownload struct {
	url  string
	name string
}

// lanzouResolveShareDownload ports the full AList download chain:
// share page → (password down_p / iframe page) → ajaxm.php → dom/file jump →
// (optional verification ajax.php) → real direct link.
func lanzouResolveShareDownload(ctx context.Context, shareID, pwd, shareBase, cookie string) (shareDownload, error) {
	base := strings.TrimSuffix(shareBase, "/")
	page, err := fetchText(ctx, http.MethodGet, base+"/"+shareID, map[string]string{"user-agent": LANZOU_DEFAULT.UserAgent}, nil, cookie, false)
	if err != nil {
		return shareDownload{}, err
	}
	pageData := removeJSComment(removeNotes(page.text))
	if page.status == http.StatusNotFound {
		return shareDownload{}, errors.New("分享链接不存在或已失效")
	}
	if strings.Contains(pageData, "取消分享") || strings.Contains(pageData, "文件链接失效") {
		return shareDownload{}, errors.New("分享已取消或链接失效")
	}
	if strings.Contains(pageData, "文件不存在") {
		return shareDownload{}, errors.New("文件不存在")
	}

	var param map[string]string
	var baseURL, downloadURL, name string

	if strings.Contains(pageData, "pwdload") || strings.Contains(pageData, "passwddiv") {
		// password share: data lives in the down_p function
		fn, err := getJSFunctionByName(pageData, "down_p")
		if err != nil {
			return shareDownload{}, err
		}
		param, err = htmlJsonToMap(fn)
		if err != nil {
			return shareDownload{}, err
		}
		param["p"] = pwd
		fileID := firstCapture(fileIDRe, fn)
		if fileID == "" {
			fileID = firstCapture(fileIDRe, pageData)
		}
		if fileID == "" {
			return shareDownload{}, errors.New("页面中未找到文件 id")
		}
		text, err := fetchText(ctx, http.MethodPost, base+"/ajaxm.php?file="+fileID, map[string]string{
			"content-type": "application/x-www-form-urlencoded",
			"user-agent":   LANZOU_DEFAULT.UserAgent,
			"referer":      base + "/" + shareID,
		}, []byte(formEncode(param)), cookie, false)
		if err != nil {
			return shareDownload{}, err
		}
		resp, err := parseAjaxmResp(text.text)
		if err != nil {
			return shareDownload{}, err
		}
		name = resp.Inf
		if resp.Dom != "" {
			baseURL = resp.Dom + "/file"
		}
		downloadURL = baseURL + "/" + resp.URL
	} else {
		iframe := firstCapture(downPageRe, pageData)
		if iframe == "" {
			return shareDownload{}, errors.New("分享页中未找到下载页 iframe")
		}
		next, err := fetchText(ctx, http.MethodGet, base+iframe, map[string]string{
			"user-agent": LANZOU_DEFAULT.UserAgent,
			"referer":    base + "/" + shareID,
		}, nil, cookie, false)
		if err != nil {
			return shareDownload{}, err
		}
		nextData := removeJSComment(removeNotes(next.text))
		param, err = htmlJsonToMap(nextData)
		if err != nil {
			return shareDownload{}, err
		}
		fileID := firstCapture(fileIDRe, nextData)
		if fileID == "" {
			return shareDownload{}, errors.New("下载页中未找到文件 id")
		}
		text, err := fetchText(ctx, http.MethodPost, base+"/ajaxm.php?file="+fileID, map[string]string{
			"content-type": "application/x-www-form-urlencoded",
			"user-agent":   LANZOU_DEFAULT.UserAgent,
			"referer":      base + iframe,
		}, []byte(formEncode(param)), cookie, false)
		if err != nil {
			return shareDownload{}, err
		}
		resp, err := parseAjaxmResp(text.text)
		if err != nil {
			return shareDownload{}, err
		}
		if resp.Dom != "" {
			baseURL = resp.Dom + "/file"
		}
		downloadURL = baseURL + "/" + resp.URL
		if m := nameFindRe.FindStringSubmatch(pageData); len(m) > 1 {
			for _, v := range m[1:] {
				if v != "" {
					name = v
					break
				}
			}
		}
	}

	if downloadURL == "" {
		return shareDownload{}, errors.New("未解析到下载地址")
	}

	// follow the dom/file jump to the real direct link (NoRedirect)
	headers := map[string]string{
		"user-agent":      LANZOU_DEFAULT.UserAgent,
		"accept-language": "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
	}
	res2, err := fetchText(ctx, http.MethodGet, downloadURL, headers, nil, cookie, true)
	if err != nil {
		return shareDownload{}, err
	}
	location := res2.location
	if res2.status == 302 && location != "" {
		return shareDownload{url: location, name: name}, nil
	}
	if res2.bodySkipped {
		return shareDownload{url: downloadURL, name: name}, nil
	}

	// verification page: parse the form, el=2, POST baseUrl/ajax.php
	vParam, err := htmlJsonToMap(removeJSComment(removeNotes(res2.text)))
	if err != nil {
		return shareDownload{}, err
	}
	vParam["el"] = "2"
	lanzouSleep(2000)
	vres, err := fetchText(ctx, http.MethodPost, baseURL+"/ajax.php", map[string]string{
		"content-type": "application/x-www-form-urlencoded",
		"user-agent":   LANZOU_DEFAULT.UserAgent,
		"referer":      downloadURL,
	}, []byte(formEncode(vParam)), cookie, false)
	if err != nil {
		return shareDownload{}, err
	}
	var j map[string]any
	if json.Unmarshal([]byte(vres.text), &j) == nil {
		if direct := strOf(j["url"]); direct != "" {
			return shareDownload{url: direct, name: name}, nil
		}
	}
	if location != "" {
		return shareDownload{url: location, name: name}, nil
	}
	return shareDownload{}, fmt.Errorf("下载跳转验证失败: %s", truncate(vres.text, 120))
}

func formEncode(m map[string]string) string {
	form := url.Values{}
	for k, v := range m {
		form.Set(k, v)
	}
	return form.Encode()
}

func firstCapture(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func lanzouSleep(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// apiLanzouDownloadInfo: account file download — share info (task 22) then the
// full share chain. Errors are returned as a downloadInfo.Error like the legacy.
func (d *Driver) downloadInfo(ctx context.Context, c drive.Context, fileID string, isDir bool) downloadInfo {
	info, err := d.fileShare(ctx, c, fileID, isDir)
	if err != nil {
		return downloadInfo{Error: err.Error()}
	}
	fid := strOf(firstOf(info, "f_id", "fid", "id"))
	pwd := strOf(firstOf(info, "pwd"))
	if fid == "" {
		return downloadInfo{Error: "无法获取分享信息"}
	}
	size := lanzouSizeOf(firstOf(info, "size", "file_size", "filesize", "f_size"))
	cookie, _, _, _ := sessionOf(c)
	var (
		res       shareDownload
		shareBase string
		lastErr   error
	)
	for _, candidate := range lanzouShareBaseCandidates(strOf(firstOf(info, "isnewd"))) {
		resolved, err := lanzouResolveShareDownload(ctx, fid, pwd, candidate, cookie)
		if err == nil {
			res = resolved
			shareBase = candidate
			break
		}
		lastErr = err
		if !isUnavailableLanzouShare(err) {
			break
		}
	}
	if shareBase == "" {
		if lastErr == nil {
			lastErr = errors.New("未解析到下载地址")
		}
		return downloadInfo{Error: lastErr.Error()}
	}
	return downloadInfo{
		URL:  res.url,
		Size: size,
		Headers: map[string]string{
			"User-Agent":      LANZOU_DEFAULT.UserAgent,
			"Accept-Language": "zh-CN,zh;q=0.9",
			"Referer":         shareBase + "/",
		},
	}
}

func lanzouShareBaseCandidates(primary string) []string {
	seen := make(map[string]struct{}, 2)
	bases := make([]string, 0, 2)
	for _, candidate := range []string{primary, LANZOU_DEFAULT.ShareURL} {
		candidate = strings.TrimSuffix(strings.TrimSpace(candidate), "/")
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		bases = append(bases, candidate)
	}
	return bases
}

func isUnavailableLanzouShare(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "文件不存在") ||
		strings.Contains(message, "分享链接不存在") ||
		strings.Contains(message, "file not found") ||
		strings.Contains(message, "http 404")
}
