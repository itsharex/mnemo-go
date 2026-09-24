package pan189

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"mnemo-go/internal/drive"
)

func (d *Driver) ensureFamilyID(ctx context.Context, c drive.Context) error {
	session, err := sessionOf(c.Token)
	if err != nil {
		return err
	}
	if session.FamilyID != "" {
		return nil
	}
	if session.FamilySessionKey == "" || session.FamilySessionSecret == "" {
		return errors.New("天翼云盘家庭云会话缺失，请重新登录")
	}
	raw, err := d.request(ctx, c, apiURL+"/family/manage/getFamilyList.action", reqOptions{method: "GET", family: boolPtr(true)})
	if err != nil {
		return err
	}
	var response struct {
		Families []struct {
			ID      json.RawMessage `json:"familyId"`
			Name    string          `json:"remarkName"`
			UseFlag int             `json:"useFlag"`
		} `json:"familyInfoResp"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	selected := -1
	for index, family := range response.Families {
		if rawIDString(family.ID) == "" {
			continue
		}
		if selected < 0 || (family.UseFlag != 0 && response.Families[selected].UseFlag == 0) {
			selected = index
		}
		if name := strings.TrimSpace(family.Name); name != "" && strings.Contains(session.LoginName, name) {
			selected = index
			break
		}
	}
	if selected < 0 {
		return errors.New("天翼云盘账号未加入任何家庭云")
	}
	session, err = sessionOf(c.Token)
	if err != nil {
		return err
	}
	session.FamilyID = rawIDString(response.Families[selected].ID)
	session.FamilyName = response.Families[selected].Name
	saveSession(c.Token, session)
	return drive.PersistToken(c)
}
