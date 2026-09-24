package model

import (
	"encoding/json"
	"sort"
	"strings"
)

// Known drive providers. gofile and gdrive are intentionally removed.
const (
	ProviderPikpak   = "pikpak"
	ProviderOnedrive = "onedrive"
	ProviderDropbox  = "dropbox"
	ProviderPan123   = "pan123"
	ProviderLanzou   = "lanzou"
	ProviderIlanzou  = "ilanzou"
	ProviderPan139   = "pan139"
	ProviderPan189   = "pan189"
	ProviderYike     = "yike"
	ProviderAliopen  = "aliopen"
	ProviderGuangya  = "guangya"
	ProviderWebdav   = "webdav"
	ProviderS3       = "s3"
	ProviderUnknown  = "unknown"
)

// TokenInfo mirrors the legacy ITokenInfo shape. All provider-specific
// credentials are persisted verbatim. Extra unknown fields are preserved in
// Raw so refresh flows always round-trip the original payload.
type TokenInfo struct {
	TokenFrom    string `json:"tokenfrom"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`

	// Open API (alist-style) segment.
	OpenAPITokenType    string `json:"open_api_token_type"`
	OpenAPIAccessToken  string `json:"open_api_access_token"`
	OpenAPIRefreshToken string `json:"open_api_refresh_token"`
	OpenAPIExpiresIn    int64  `json:"open_api_expires_in"`

	Signature  string `json:"signature,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	UserName   string `json:"user_name,omitempty"`
	Avatar     string `json:"avatar,omitempty"`
	NickName   string `json:"nick_name,omitempty"`
	Name       string `json:"name,omitempty"`
	Role       string `json:"role,omitempty"`
	Status     string `json:"status,omitempty"`
	State      string `json:"state,omitempty"`
	ExpireTime string `json:"expire_time,omitempty"`
	SpuID      string `json:"spu_id,omitempty"`

	DefaultDriveID     string `json:"default_drive_id,omitempty"`
	ResourceDriveID    string `json:"resource_drive_id,omitempty"`
	BackupDriveID      string `json:"backup_drive_id,omitempty"`
	SboxDriveID        string `json:"sbox_drive_id,omitempty"`
	PicDriveID         string `json:"pic_drive_id,omitempty"`
	DefaultSboxDriveID string `json:"default_sbox_drive_id,omitempty"`

	ProviderAccountID string `json:"provider_account_id,omitempty"`
	ProviderRootID    string `json:"provider_root_id,omitempty"`
	ProviderDriveType string `json:"provider_drive_type,omitempty"`

	UsedSize             int64 `json:"used_size,omitempty"`
	TotalSize            int64 `json:"total_size,omitempty"`
	FreeSize             int64 `json:"free_size,omitempty"`
	FamilyUsedSize       int64 `json:"family_used_size,omitempty"`
	FamilyTotalSize      int64 `json:"family_total_size,omitempty"`
	FamilyQuotaSeparated bool  `json:"family_quota_separated,omitempty"`

	VIPName   string `json:"vipname,omitempty"`
	VIPIcon   string `json:"vipIcon,omitempty"`
	VIPExpire string `json:"vipexpire,omitempty"`

	// Raw preserves the original JSON document for opaque providers.
	Raw json.RawMessage `json:"-"`

	// Conn carries mounted-storage connection config (webdav/s3).
	Conn *ConnConfig `json:"conn,omitempty"`
}

// ConnConfig describes a mounted storage connection.
type ConnConfig struct {
	Name     string `json:"name,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// AuthType applies to mounted WebDAV storage: auto (default), basic,
	// digest, or bearer. S3 ignores this field.
	AuthType       string `json:"authType,omitempty"`
	RootPath       string `json:"rootPath,omitempty"`
	Region         string `json:"region,omitempty"`
	Bucket         string `json:"bucket,omitempty"`
	BasePath       string `json:"basePath,omitempty"`
	ForcePathStyle *bool  `json:"forcePathStyle,omitempty"`
	SessionToken   string `json:"sessionToken,omitempty"`
	// AllowPrivateNetwork permits media preview through explicitly configured
	// WebDAV/S3 endpoints on a LAN. It does not disable proxy SSRF checks or
	// allow redirects to arbitrary private hosts.
	AllowPrivateNetwork bool `json:"allowPrivateNetwork,omitempty"`
}

// Account is the persisted session record. UserID is the primary key and is
// namespaced by provider prefix (e.g. pikpak_xxx).
type Account struct {
	UserID     string     `json:"user_id"`
	DriveID    string     `json:"drive_id"`
	Token      *TokenInfo `json:"token,omitempty"`
	Order      int64      `json:"order,omitempty"`
	Disabled   bool       `json:"disabled,omitempty"`
	CustomName string     `json:"custom_name,omitempty"`
	CustomIcon string     `json:"custom_icon,omitempty"`
	// Usage is a cached quota snapshot for display.
	Usage       *Quota `json:"usage,omitempty"`
	FamilyUsage *Quota `json:"family_usage,omitempty"`
}

// Provider returns the provider key for this account, resolved from the token
// or the user id prefix.
func (a *Account) Provider() string {
	if a.Token != nil && a.Token.TokenFrom != "" {
		return a.Token.TokenFrom
	}
	return ResolveProviderFromUserID(a.UserID)
}

// placeholder injection for prefix table defined in drive/meta.
var prefixFor func(string) string = func(string) string { return "" }

// SetProviderPrefixResolver wires the drive meta prefix resolver.
func SetProviderPrefixResolver(fn func(string) string) {
	if fn != nil {
		prefixFor = fn
	}
}

// ResolveProviderFromUserID guesses the provider from a user id prefix.
func ResolveProviderFromUserID(userID string) string {
	if prefixFor == nil {
		return ProviderUnknown
	}
	return prefixFor(userID)
}

// AllProviders lists every registered provider key in stable order.
func AllProviders() []string {
	v := []string{ProviderPikpak, ProviderOnedrive, ProviderDropbox, ProviderPan123,
		ProviderLanzou, ProviderIlanzou, ProviderPan139, ProviderPan189, ProviderYike,
		ProviderAliopen, ProviderGuangya, ProviderWebdav, ProviderS3}
	sort.Strings(v)
	return v
}

// BuildUserID prefixes an account id with the provider namespace.
func BuildUserID(provider, accountID string) string {
	value := strings.TrimSpace(accountID)
	if value == "" {
		return ""
	}
	switch provider {
	case ProviderWebdav, ProviderS3:
		return provider + ":" + value
	default:
		if strings.HasPrefix(value, provider+"_") {
			return value
		}
		return provider + "_" + value
	}
}

// StripUserID removes the provider namespace from a user id.
func StripUserID(provider, userID string) string {
	value := strings.TrimSpace(userID)
	switch provider {
	case ProviderWebdav, ProviderS3:
		prefix := provider + ":"
		if strings.HasPrefix(value, prefix) {
			return value[len(prefix):]
		}
	default:
		prefix := provider + "_"
		if strings.HasPrefix(value, prefix) {
			return value[len(prefix):]
		}
	}
	return value
}

// BuildDriveID namespaces an account id under a provider drive namespace.
func BuildDriveID(provider, accountID string) string {
	value := strings.TrimSpace(accountID)
	if value == "" {
		return ""
	}
	prefix := provider + ":"
	if strings.HasPrefix(value, prefix) {
		return value
	}
	return prefix + value
}

// StripDriveID removes the provider drive namespace.
func StripDriveID(provider, driveID string) string {
	prefix := provider + ":"
	if strings.HasPrefix(driveID, prefix) {
		return driveID[len(prefix):]
	}
	return driveID
}
