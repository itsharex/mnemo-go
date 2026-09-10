// Package ilanzou implements the 优享版蓝奏云 (ilanzou.com) drive provider,
// ported from the legacy Electron TypeScript implementation (src/ilanzou +
// the ilanzou drive adapter). It uses the AES-128-ECB signed API with a
// qiniu multipart upload path.
package ilanzou

import (
	"bytes"
	"encoding/json"
)

// ILANZOU_ROOT is the provider root folder id surfaced to the frontend.
const ILANZOU_ROOT = "ilanzou_root"

// ILANZOU_CONF mirrors the legacy config.
var ILANZOU_CONF = struct {
	Base       string
	Secret     string
	Bucket     string
	Unproved   string
	Proved     string
	DevVersion string
	Site       string
}{
	Base:       "https://api.ilanzou.com",
	Secret:     "lanZouY-disk-app",
	Bucket:     "wpanstore-lanzou",
	Unproved:   "unproved",
	Proved:     "proved",
	DevVersion: "125",
	Site:       "https://www.ilanzou.com",
}

// listItem is one raw /record/file/list entry.
type listItem struct {
	FileType   int    `json:"fileType"`
	FileID     int64  `json:"fileId"`
	FolderID   int64  `json:"folderId"`
	FileName   string `json:"fileName"`
	FolderName string `json:"folderName"`
	FileSize   int64  `json:"fileSize"`
	UpdTime    string `json:"updTime"`
	ParentID   int64  `json:"parentId"`
}

// UnmarshalJSON accepts both numeric and quoted numeric fields. The legacy
// API has returned both shapes across different endpoints and deployments;
// decoding directly into int64 would silently drop the whole row on the
// quoted form.
func (i *listItem) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	i.FileType = int(numOf(raw["fileType"]))
	i.FileID = numOf(raw["fileId"])
	i.FolderID = numOf(raw["folderId"])
	i.FileName = strOf(raw["fileName"])
	i.FolderName = strOf(raw["folderName"])
	i.FileSize = numOf(raw["fileSize"])
	i.UpdTime = strOf(raw["updTime"])
	i.ParentID = numOf(raw["parentId"])
	return nil
}

// ToILanzouFolderId maps a user-facing folder id to the API folder id
// ('0' = root).
func ToILanzouFolderId(id string) string {
	if id == "" || id == ILANZOU_ROOT || id == "root" || id == "/" {
		return "0"
	}
	return id
}
