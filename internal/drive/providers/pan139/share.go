package pan139

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

const pan139CreateSharePath = "/orchestration/personalCloud-rebuild/outlink/v1.0/getOutLink"

type pan139ShareLink struct {
	LinkID     pan139FlexString `json:"linkID"`
	ObjID      pan139FlexString `json:"objID"`
	LinkURL    string           `json:"linkUrl"`
	LinkURLMin string           `json:"linkUrlMin"`
	Passwd     string           `json:"passwd"`
}

func (d *Driver) CreateShare(ctx context.Context, c drive.Context, params drive.ShareParams) (*model.ShareItem, error) {
	displayIDs := normalizePan139IDs(params.FileIDs)
	if len(displayIDs) == 0 {
		return nil, errors.New("移动云盘创建分享至少选择一个文件或文件夹")
	}
	if strings.TrimSpace(params.Password) != "" {
		return nil, errors.New("移动云盘分享提取码由服务端生成，暂不支持自定义")
	}
	ids := make([]string, 0, len(displayIDs))
	for _, fileID := range displayIDs {
		if space, _ := pan139SpaceID(fileID); space == pan139FamilySpace {
			return nil, errors.New("移动云盘家庭云暂不支持创建公开分享链接")
		}
		_, rawID := pan139SpaceID(fileID)
		ids = append(ids, rawID)
	}
	period, err := pan139SharePeriod(params.Expiration)
	if err != nil {
		return nil, err
	}
	account := accountOf(c)
	if account == "" {
		return nil, errors.New("移动云盘账号信息缺失，请重新登录")
	}
	folders, files := pan139ShareTargets(ids, pan139RawShareRefs(params.FileRefs))
	name := strings.TrimSpace(params.ShareName)
	if name == "" {
		name = "移动云盘分享"
	}
	body := map[string]any{
		"getOutLinkReq": map[string]any{
			"period":  period,
			"caIDLst": folders,
			"coIDLst": files,
			"commonAccountInfo": map[string]any{
				"account":     account,
				"accountType": 1,
			},
			"dedicatedName": name,
			"encrypt":       1,
			"extInfo": map[string]any{
				"isWatermark":  0,
				"shareChannel": "3001",
			},
			"periodUnit":  1,
			"pubType":     1,
			"subLinkType": 0,
			"viewerLst":   []any{},
		},
	}
	raw, err := d.personalPost(ctx, c, pan139CreateSharePath, body)
	if err != nil {
		return nil, err
	}
	link, err := parsePan139ShareLink(raw)
	if err != nil {
		return nil, err
	}
	shareURL := firstPan139ShareString(link.LinkURL, link.LinkURLMin)
	if shareURL == "" {
		return nil, errors.New("移动云盘创建分享未返回链接")
	}
	shareID := firstPan139ShareString(link.LinkID.String(), link.ObjID.String(), shareURL)
	return &model.ShareItem{
		AccountID:   c.UserID,
		DriveID:     c.DriveID,
		ShareID:     shareID,
		ShareURL:    shareURL,
		SharePwd:    link.Passwd,
		ShareName:   name,
		SharePolicy: "public",
		Expiration:  params.Expiration,
		FileID:      displayIDs[0],
		FileIDList:  displayIDs,
		ShareMsg:    "创建成功",
	}, nil
}

func pan139RawShareRefs(refs []drive.FileRef) []drive.FileRef {
	out := make([]drive.FileRef, 0, len(refs))
	for _, ref := range refs {
		_, rawID := pan139SpaceID(ref.ID)
		ref.ID = rawID
		out = append(out, ref)
	}
	return out
}

func pan139ShareTargets(ids []string, refs []drive.FileRef) (folders, files []string) {
	isDir := make(map[string]bool, len(refs))
	for _, ref := range refs {
		id := strings.TrimSpace(ref.ID)
		if id != "" && ref.IsDir != nil {
			isDir[id] = *ref.IsDir
		}
	}
	for _, id := range ids {
		if isDir[id] {
			folders = append(folders, id)
		} else {
			files = append(files, id)
		}
	}
	return folders, files
}

func pan139SharePeriod(value string) (*int, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" || value == "-1" {
		return nil, nil
	}
	if days, err := strconv.Atoi(value); err == nil {
		if days == 1 || days == 7 {
			return &days, nil
		}
		return nil, errors.New("移动云盘分享只支持 1 天、7 天或永久有效")
	}
	target, err := parsePan139ShareTime(value)
	if err != nil {
		return nil, errors.New("移动云盘分享有效期格式无效")
	}
	remaining := time.Until(target)
	switch {
	case remaining <= 0:
		return nil, errors.New("移动云盘分享有效期已过期")
	case remaining <= 24*time.Hour:
		days := 1
		return &days, nil
	case remaining <= 7*24*time.Hour:
		days := 7
		return &days, nil
	default:
		return nil, errors.New("移动云盘分享只支持 1 天、7 天或永久有效")
	}
}

func parsePan139ShareTime(value string) (time.Time, error) {
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

func parsePan139ShareLink(raw json.RawMessage) (pan139ShareLink, error) {
	var response struct {
		GetOutLinkRes struct {
			Items []pan139ShareLink `json:"getOutLinkResSet"`
		} `json:"getOutLinkRes"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return pan139ShareLink{}, errors.New("移动云盘创建分享响应无效")
	}
	if len(response.GetOutLinkRes.Items) == 0 {
		return pan139ShareLink{}, errors.New("移动云盘创建分享未返回结果")
	}
	return response.GetOutLinkRes.Items[0], nil
}

func firstPan139ShareString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
