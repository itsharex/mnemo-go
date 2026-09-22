package pan189

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

const pan189CreateSharePath = "/api/open/share/createShareLink.action"

func (d *Driver) CreateShare(ctx context.Context, c drive.Context, params drive.ShareParams) (*model.ShareItem, error) {
	if len(params.FileIDs) != 1 || strings.TrimSpace(params.FileIDs[0]) == "" {
		return nil, errors.New("天翼云盘分享一次只能选择一个文件或文件夹")
	}
	if strings.TrimSpace(params.Password) != "" {
		return nil, errors.New("天翼云盘分享提取码由服务端生成，暂不支持自定义")
	}
	if _, err := sessionOf(c.Token); err != nil {
		return nil, err
	}
	space, rawFileID := pan189SpaceID(params.FileIDs[0])
	if space == spaceFamily {
		return nil, errors.New("天翼家庭云暂不支持创建公开分享链接")
	}
	expireTime, err := pan189ShareExpireTime(params.Expiration)
	if err != nil {
		return nil, err
	}
	fileID := strings.TrimSpace(rawFileID)
	raw, err := d.request(ctx, c, webURL+pan189CreateSharePath, reqOptions{
		method: "GET",
		query: map[string]string{
			"fileId":     fileID,
			"expireTime": strconv.Itoa(expireTime),
			"shareType":  "3",
			"noCache":    strconv.FormatInt(time.Now().UnixNano(), 10),
		},
		headers: map[string]string{"Accept": "application/json;charset=UTF-8"},
	})
	if err != nil {
		return nil, err
	}
	var response struct {
		ShareLinkList []struct {
			ShareID    json.RawMessage `json:"shareId"`
			AccessCode string          `json:"accessCode"`
			AccessURL  string          `json:"accessUrl"`
			URL        string          `json:"url"`
		} `json:"shareLinkList"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, errors.New("天翼云盘创建分享响应无效")
	}
	if len(response.ShareLinkList) == 0 {
		return nil, errors.New("天翼云盘创建分享未返回结果")
	}
	link := response.ShareLinkList[0]
	shareURL := normalizePan189ShareURL(firstNonEmpty(link.AccessURL, link.URL))
	if shareURL == "" {
		return nil, errors.New("天翼云盘创建分享未返回链接")
	}
	shareID := rawIDString(link.ShareID)
	if shareID == "" {
		shareID = shareURL
	}
	name := strings.TrimSpace(params.ShareName)
	if name == "" {
		name = "天翼云盘分享"
	}
	return &model.ShareItem{
		AccountID:   c.UserID,
		DriveID:     c.DriveID,
		ShareID:     shareID,
		ShareURL:    shareURL,
		SharePwd:    strings.TrimSpace(link.AccessCode),
		ShareName:   name,
		SharePolicy: "public",
		Expiration:  params.Expiration,
		FileID:      fileID,
		FileIDList:  []string{fileID},
		ShareMsg:    "创建成功",
	}, nil
}

func pan189ShareExpireTime(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" || value == "-1" || value == "2099" {
		return 2099, nil
	}
	if days, err := strconv.Atoi(value); err == nil {
		if days == 1 || days == 7 {
			return days, nil
		}
		return 0, errors.New("天翼云盘分享只支持 1 天、7 天或永久有效")
	}
	target, err := parsePan189ShareTime(value)
	if err != nil {
		return 0, errors.New("天翼云盘分享有效期格式无效")
	}
	remaining := time.Until(target)
	switch {
	case remaining <= 0:
		return 0, errors.New("天翼云盘分享有效期已过期")
	case remaining <= 24*time.Hour:
		return 1, nil
	case remaining <= 7*24*time.Hour:
		return 7, nil
	default:
		return 0, errors.New("天翼云盘分享只支持 1 天、7 天或永久有效")
	}
}

func parsePan189ShareTime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid time")
}

func normalizePan189ShareURL(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	if strings.HasPrefix(value, "/") {
		return webURL + value
	}
	return value
}
