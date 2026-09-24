package pan139

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mnemo-go/internal/config"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
)

func TestLiveFamilyDiscoveryShapesReadOnly(t *testing.T) {
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
		if account.Provider() != model.ProviderPan139 || account.Token == nil || account.Disabled {
			continue
		}
		identity := accountOf(drive.Context{Token: account.Token})
		variants := []struct {
			name string
			body map[string]any
		}{
			{"pageInfo", map[string]any{"pageInfo": map[string]int{"pageNum": 1, "pageSize": 100}}},
			{"commonAccountInfo", map[string]any{"pageInfo": map[string]int{"pageNum": 1, "pageSize": 100}, "commonAccountInfo": map[string]any{"account": identity, "accountType": 1}}},
			{"commonAccountInfoList", map[string]any{"pageInfo": map[string]int{"pageNum": 1, "pageSize": 100}, "commonAccountInfoList": []map[string]any{{"account": identity}}}},
		}
		for _, variant := range variants {
			token := *account.Token
			raw, err := (&Driver{}).familyPost(t.Context(), drive.Context{Token: &token}, "/orchestration/familyCloud-rebuild/cloudManage/v1.0/queryFamilyCloud", variant.body)
			if err == nil {
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				var keys []string
				for key := range fields {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				t.Logf("%s: 成功，数据字段=%v", variant.name, keys)
				var families []map[string]json.RawMessage
				_ = json.Unmarshal(fields["familyCloudList"], &families)
				if len(families) > 0 {
					keys = keys[:0]
					for key := range families[0] {
						keys = append(keys, key)
					}
					sort.Strings(keys)
				}
				t.Logf("家庭云数量=%d，首条字段=%v", len(families), keys)
				var count json.Number
				_ = json.Unmarshal(fields["totalCount"], &count)
				var result map[string]json.RawMessage
				_ = json.Unmarshal(fields["result"], &result)
				keys = keys[:0]
				for key := range result {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				t.Logf("报告总数=%s，结果字段=%v", count, keys)
				return
			}
			codes := regexp.MustCompile(`(?:code|resultCode)=[A-Za-z0-9_-]+`).FindAllString(err.Error(), -1)
			t.Logf("%s: %s", variant.name, strings.Join(codes, ", "))
		}
		return
	}
	t.Skip("未找到可用的移动云盘账号")
}
