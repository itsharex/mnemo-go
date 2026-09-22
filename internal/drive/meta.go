package drive

import (
	"strings"

	"mnemo-go/internal/model"
)

// Meta describes a drive provider for display purposes.
type Meta struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Icon      string `json:"icon"`
	RootKey   string `json:"rootKey,omitempty"`
	RootTitle string `json:"rootTitle,omitempty"`
}

// builtinMetas lists the 13 in-scope providers. Each plugin registers its own
// meta; the builtin table serves as the authoritative display registry used by
// the UI (login list + account rails).
var builtinMetas = map[string]Meta{
	model.ProviderPikpak:   {Key: "pikpak", Label: "PikPak", Icon: "drive-icons/pikpak.png", RootKey: "pikpak_root", RootTitle: "网盘文件"},
	model.ProviderOnedrive: {Key: "onedrive", Label: "OneDrive", Icon: "drive-icons/onedrive.svg"},
	model.ProviderDropbox:  {Key: "dropbox", Label: "Dropbox", Icon: "drive-icons/dropbox.svg"},
	model.ProviderPan123:   {Key: "pan123", Label: "123 云盘", Icon: "drive-icons/pan123.svg"},
	model.ProviderLanzou:   {Key: "lanzou", Label: "蓝奏云", Icon: "drive-icons/lanzou.svg"},
	model.ProviderIlanzou:  {Key: "ilanzou", Label: "优享版蓝奏云", Icon: "drive-icons/ilanzou.svg"},
	model.ProviderPan139:   {Key: "pan139", Label: "移动云盘", Icon: "drive-icons/pan139.svg"},
	model.ProviderPan189:   {Key: "pan189", Label: "天翼云盘", Icon: "drive-icons/pan189.svg"},
	model.ProviderYike:     {Key: "yike", Label: "一刻相册", Icon: "drive-icons/yike.svg"},
	model.ProviderAliopen:  {Key: "aliopen", Label: "阿里云盘", Icon: "drive-icons/aliopen.svg"},
	model.ProviderGuangya:  {Key: "guangya", Label: "光鸭云盘", Icon: "drive-icons/guangya.svg"},
	model.ProviderWebdav:   {Key: "webdav", Label: "WebDAV", Icon: "drive-icons/webdav.svg", RootKey: "/"},
	model.ProviderS3:       {Key: "s3", Label: "S3", Icon: "drive-icons/s3.svg", RootKey: "/"},
}

// userIDPrefixes maps provider -> user id prefix (for resolution).
var userIDPrefixes = map[string]string{
	"pikpak": "pikpak_", "onedrive": "onedrive_", "dropbox": "dropbox_",
	"pan123": "pan123_", "lanzou": "lanzou_", "ilanzou": "ilanzou_",
	"pan139": "pan139_", "pan189": "pan189_", "yike": "yike_",
	"aliopen": "aliopen_", "guangya": "guangya_",
	"webdav": "webdav:", "s3": "s3:",
}

// driveIDPrefixes maps provider -> drive id namespace.
var driveIDPrefixes = map[string]string{
	"webdav": "webdav:", "s3": "s3:",
}

func init() {
	model.SetProviderPrefixResolver(resolveProviderByUserID)
}

// resolveProviderByUserID guesses the provider from a user id prefix.
func resolveProviderByUserID(userID string) string {
	for provider, prefix := range userIDPrefixes {
		if strings.HasPrefix(userID, prefix) {
			return provider
		}
	}
	return model.ProviderUnknown
}

// ResolveProvider determines the provider for a context: tokenfrom wins,
// then user id prefix, then drive id namespace.
func ResolveProvider(userID, driveID, tokenFrom string) string {
	if tokenFrom != "" && tokenFrom != model.ProviderUnknown {
		if _, ok := builtinMetas[tokenFrom]; ok {
			return tokenFrom
		}
	}
	if userID != "" {
		if p := resolveProviderByUserID(userID); p != model.ProviderUnknown {
			return p
		}
	}
	if driveID != "" {
		for provider, prefix := range driveIDPrefixes {
			if strings.HasPrefix(driveID, prefix) {
				return provider
			}
		}
	}
	return model.ProviderUnknown
}

// GetMeta returns the builtin display meta for a provider.
func GetMeta(provider string) Meta {
	if m, ok := builtinMetas[provider]; ok {
		return m
	}
	return Meta{Key: provider, Label: provider}
}

// ListProviders returns the 13 in-scope provider keys in a stable order.
func ListProviders() []string {
	return []string{
		model.ProviderPikpak, model.ProviderOnedrive, model.ProviderDropbox,
		model.ProviderPan123, model.ProviderLanzou, model.ProviderIlanzou,
		model.ProviderPan139, model.ProviderPan189, model.ProviderYike,
		model.ProviderAliopen, model.ProviderGuangya,
		model.ProviderWebdav, model.ProviderS3,
	}
}

// IsRootID reports whether a file id is a provider root sentinel.
func IsRootID(provider, fileID string) bool {
	v := fileID
	if v == "/" || v == "root" {
		return true
	}
	rootKey := GetMeta(provider).RootKey
	return rootKey != "" && v == rootKey
}

// IsSessionUsable reports whether a token can drive operations.
func IsSessionUsable(provider string, token *model.TokenInfo) bool {
	if token == nil {
		return false
	}
	caps := RegistryCaps(provider)
	return caps.MountedStorage || token.AccessToken != ""
}

// RegistryCaps returns capabilities of a registered provider (zero-caps when
// plugin missing, e.g. tests without imports).
func RegistryCaps(provider string) Capabilities {
	if reg, ok := registry[provider]; ok {
		return reg.Caps
	}
	return noCaps()
}
