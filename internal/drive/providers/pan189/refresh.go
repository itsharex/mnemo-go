package pan189

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

// RefreshAccount reuses the current session, renewing only after the API
// explicitly rejects it, and refreshes the personal-cloud quota.
// Personal and family capacity are returned independently by the portal API.
func (d *Driver) RefreshAccount(ctx context.Context, c drive.Context, token *model.TokenInfo) (*model.TokenInfo, error) {
	if token == nil {
		return nil, drive.AuthExpired("天翼云盘未登录")
	}
	if _, err := sessionOf(token); err != nil {
		return nil, err
	}
	cc := c
	cc.Token = token
	raw, err := d.request(ctx, cc, apiURL+"/portal/getUserSizeInfo.action", reqOptions{method: "GET", family: boolPtr(false)})
	if err != nil {
		return nil, err
	}
	usedSize, totalSize, ok := parsePan189Capacity(raw, false)
	if !ok {
		return nil, errors.New("天翼容量接口未返回有效空间信息")
	}
	applyPan189Quota(token, usedSize, totalSize, ok)
	return token, nil
}

func parsePan189Capacity(raw []byte, family bool) (used, total int64, ok bool) {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return
	}
	key := "cloudCapacityInfo"
	if family {
		key = "familyCapacityInfo"
	}
	var capacity struct {
		Used  json.RawMessage `json:"usedSize"`
		Total json.RawMessage `json:"totalSize"`
	}
	if json.Unmarshal(values[key], &capacity) == nil {
		var usedOK, totalOK bool
		used, usedOK = pan189QuotaInt64(capacity.Used)
		total, totalOK = pan189QuotaInt64(capacity.Total)
		if usedOK && totalOK && total > 0 {
			return min(used, total), total, true
		}
	}
	if !family {
		return parsePan189Quota(raw)
	}
	return 0, 0, false
}

func parsePan189Quota(raw []byte) (usedSize, totalSize int64, ok bool) {
	var response struct {
		UserSizeInfo struct {
			UsedSize          json.RawMessage `json:"usedSize"`
			CloudCapacityInfo struct {
				TotalSize json.RawMessage `json:"totalSize"`
			} `json:"cloudCapacityInfo"`
		} `json:"userSizeInfo"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return 0, 0, false
	}
	usedSize, usedOK := pan189QuotaInt64(response.UserSizeInfo.UsedSize)
	totalSize, totalOK := pan189QuotaInt64(response.UserSizeInfo.CloudCapacityInfo.TotalSize)
	if !usedOK || !totalOK || totalSize <= 0 {
		return 0, 0, false
	}
	if usedSize > totalSize {
		usedSize = totalSize
	}
	return usedSize, totalSize, true
}

func pan189QuotaInt64(raw json.RawMessage) (int64, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, false
	}
	var stringValue string
	if json.Unmarshal(raw, &stringValue) == nil {
		text = strings.TrimSpace(stringValue)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	return value, err == nil && value >= 0
}

func applyPan189Quota(token *model.TokenInfo, usedSize, totalSize int64, ok bool) {
	if token == nil || !ok || totalSize <= 0 || usedSize < 0 {
		return
	}
	if usedSize > totalSize {
		usedSize = totalSize
	}
	token.TotalSize = totalSize
	token.UsedSize = usedSize
	token.FreeSize = totalSize - usedSize
}
