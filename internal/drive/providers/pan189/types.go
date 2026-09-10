// Package pan189 implements the 天翼云盘 (189 cloud drive) provider.
// Ported from the legacy Electron TS implementation (AList drivers/189pc
// personal cloud): account+password login with RSA, HMAC-SHA1 signed API
// requests, chunked upload via initMultiUpload/getMultiUploadUrls/commit.
package pan189

import (
	"encoding/json"
	"fmt"
	"strings"
)

// listEntryID accepts both API encodings without rounding numeric IDs through float64.
type listEntryID string

func (id *listEntryID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		var number json.Number
		if err := json.Unmarshal(data, &number); err != nil {
			return fmt.Errorf("pan189: invalid entry ID: %w", err)
		}
		value = number.String()
		if strings.ContainsAny(value, ".eE") {
			return fmt.Errorf("pan189: entry ID must be an integer")
		}
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("pan189: empty entry ID")
	}
	*id = listEntryID(value)
	return nil
}

const (
	// PAN189Root is the canonical root folder id surfaced to the UI.
	PAN189Root = "pan189_root"
	// Pan189DefaultFolder is the server-side root folder id (-11).
	Pan189DefaultFolder = "-11"

	webURL    = "https://cloud.189.cn"
	authURL   = "https://open.e.189.cn"
	apiURL    = "https://api.cloud.189.cn"
	uploadURL = "https://upload.cloud.189.cn"
	returnURL = "https://m.cloud.189.cn/zhuanti/2020/loginErrorPc/index.html"

	accountType = "02"
	appID       = "8025431004"
	clientType  = "10020"
	version     = "6.2"
	pc          = "TELEPC"
	channelID   = "web_cloud.189.cn"
)

const ua189 = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Session is the persisted 189 session (mirrors legacy Pan189Session).
// JSON-marshalled into TokenInfo.RefreshToken, so the account survives app
// restarts and sessions can be refreshed (or re-logged-in) transparently.
type Session struct {
	SessionKey          string `json:"sessionKey"`
	SessionSecret       string `json:"sessionSecret"`
	FamilySessionKey    string `json:"familySessionKey,omitempty"`
	FamilySessionSecret string `json:"familySessionSecret,omitempty"`
	AccessToken         string `json:"accessToken,omitempty"`
	RefreshToken        string `json:"refreshToken,omitempty"`
	LoginName           string `json:"loginName,omitempty"`
	Username            string `json:"username,omitempty"`
	Password            string `json:"password,omitempty"`
	// CloudType is "personal" or "family" (mounted cloud, persisted).
	CloudType  string `json:"cloudType,omitempty"`
	FamilyID   string `json:"familyId,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

// pan189File is a raw list entry (personal + family shares the same shape).
type pan189File struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	MD5        string `json:"md5"`
	LastOpTime string `json:"lastOpTime"`
	CreateDate string `json:"createDate"`
	IsFolder   bool   `json:"isFolder"`
	ParentID   string `json:"parentId"`
	SmallURL   string `json:"smallUrl"`
	LargeURL   string `json:"largeUrl"`
}

// toFolderID normalises a UI file id into the server-side folder id:
// roots (-11 / pan189_root / root / /) become the default folder.
func toFolderID(id string) string {
	v := strings.TrimSpace(id)
	if v == "" || v == PAN189Root || v == "root" || v == "/" {
		return Pan189DefaultFolder
	}
	return v
}

// displayParent maps a server-side parent folder id back to the UI space:
// the root folder (-11) is surfaced as pan189_root.
func displayParent(parentID string) string {
	if parentID == Pan189DefaultFolder {
		return PAN189Root
	}
	return parentID
}
