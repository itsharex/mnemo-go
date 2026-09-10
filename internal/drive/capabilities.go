package drive

// Capabilities is the capability bit set of a drive provider.
// The UI trims menus/actions against these bits; providers declare them via
// NewCapabilities with overrides.
type Capabilities struct {
	Provider string `json:"provider"`

	MountedStorage bool `json:"mountedStorage"`

	Download          bool   `json:"download"`
	CloudVideoPreview bool   `json:"cloudVideoPreview"`
	OfflineDownload   bool   `json:"offlineDownload"`
	Search            bool   `json:"search"`
	Upload            bool   `json:"upload"`
	UploadMode        string `json:"uploadMode"` // queue | direct | none

	CreateFolder     bool `json:"createFolder"`
	CreateDateFolder bool `json:"createDateFolder"`
	CreateTextFile   bool `json:"createTextFile"`

	Rename          bool `json:"rename"`
	Move            bool `json:"move"`
	Copy            bool `json:"copy"`
	RecycleBin      bool `json:"recycleBin"`
	PermanentDelete bool `json:"permanentDelete"`
	TrashView       bool `json:"trashView"`
	TrashRestore    bool `json:"trashRestore"`
	TrashPurge      bool `json:"trashPurge"`
	TrashClear      bool `json:"trashClear"`

	CreateShare     bool `json:"createShare"`
	ShareExpiration bool `json:"shareExpiration"`
	// ShareExpirationOptions lists the provider-supported durations in days.
	// Zero means no expiration. An empty list keeps the legacy common options.
	ShareExpirationOptions []int `json:"shareExpirationOptions,omitempty"`
	SharePassword          bool  `json:"sharePassword"`
	CombinedShare          bool  `json:"combinedShare"`
	ImportShare            bool  `json:"importShare"`
	ManageCreatedShares    bool  `json:"manageCreatedShares"`
	EditCreatedShares      bool  `json:"editCreatedShares"`
	CancelCreatedShares    bool  `json:"cancelCreatedShares"`
	ManageImportedShares   bool  `json:"manageImportedShares"`
	ShareHistory           bool  `json:"shareHistory"`

	QuickTransfer   bool `json:"quickTransfer"`
	Favorite        bool `json:"favorite"`
	Encryption      bool `json:"encryption"`
	PlaybackHistory bool `json:"playbackHistory"`
	CopyTree        bool `json:"copyTree"`
	PhotoAlbum      bool `json:"photoAlbum"`

	// ProvideHashes lists content fingerprints the source can provide.
	ProvideHashes []string `json:"provideHashes"`
	// RapidUploadHashes lists fingerprint秒传 types the target supports.
	RapidUploadHashes []string `json:"rapidUploadHashes"`
	// UploadConflictPolicies lists frontend-choosable conflict policies.
	UploadConflictPolicies []string `json:"uploadConflictPolicies"`
}

const (
	UploadModeQueue  = "queue"
	UploadModeDirect = "direct"
	UploadModeNone   = "none"
)

// noCaps is the all-disabled baseline.
func noCaps() Capabilities {
	return Capabilities{
		Provider:               "",
		UploadMode:             UploadModeNone,
		UploadConflictPolicies: []string{"rename", "skip", "overwrite"},
	}
}

// standardFileCaps is the common file-management baseline most drives share.
func standardFileCaps() Capabilities {
	return Capabilities{
		Download:         true,
		Upload:           true,
		UploadMode:       UploadModeQueue,
		CreateFolder:     true,
		CreateTextFile:   true,
		CreateDateFolder: true,
		Rename:           true,
		Move:             true,
		Copy:             true,
		// Recycle-bin semantics vary by provider. Each provider must opt in
		// explicitly so a permanent-delete API is never shown as "trash".
		RecycleBin: false,
	}
}

// NewCapabilities builds capabilities for a provider, layering overrides over
// the standard baseline.
func NewCapabilities(provider string, overrides map[string]bool, extra func(*Capabilities)) Capabilities {
	caps := noCaps()
	std := standardFileCaps()
	merge := func(src Capabilities) {
		if src.Download {
			caps.Download = true
		}
		if src.Upload {
			caps.Upload = true
		}
		if src.UploadMode != UploadModeNone {
			caps.UploadMode = src.UploadMode
		}
		if src.CreateFolder {
			caps.CreateFolder = true
		}
		if src.CreateTextFile {
			caps.CreateTextFile = true
		}
		if src.CreateDateFolder {
			caps.CreateDateFolder = true
		}
		if src.Rename {
			caps.Rename = true
		}
		if src.Move {
			caps.Move = true
		}
		if src.Copy {
			caps.Copy = true
		}
		if src.RecycleBin {
			caps.RecycleBin = true
		}
	}
	merge(std)
	for k, v := range overrides {
		switch k {
		case "mountedStorage":
			caps.MountedStorage = v
		case "download":
			caps.Download = v
		case "cloudVideoPreview":
			caps.CloudVideoPreview = v
		case "offlineDownload":
			caps.OfflineDownload = v
		case "search":
			caps.Search = v
		case "upload":
			caps.Upload = v
		case "createFolder":
			caps.CreateFolder = v
		case "createDateFolder":
			caps.CreateDateFolder = v
		case "createTextFile":
			caps.CreateTextFile = v
		case "rename":
			caps.Rename = v
		case "move":
			caps.Move = v
		case "copy":
			caps.Copy = v
		case "recycleBin":
			caps.RecycleBin = v
		case "permanentDelete":
			caps.PermanentDelete = v
		case "trashView":
			caps.TrashView = v
		case "trashRestore":
			caps.TrashRestore = v
		case "trashPurge":
			caps.TrashPurge = v
		case "trashClear":
			caps.TrashClear = v
		case "createShare":
			caps.CreateShare = v
		case "shareExpiration":
			caps.ShareExpiration = v
		case "sharePassword":
			caps.SharePassword = v
		case "combinedShare":
			caps.CombinedShare = v
		case "importShare":
			caps.ImportShare = v
		case "manageCreatedShares":
			caps.ManageCreatedShares = v
		case "editCreatedShares":
			caps.EditCreatedShares = v
		case "cancelCreatedShares":
			caps.CancelCreatedShares = v
		case "manageImportedShares":
			caps.ManageImportedShares = v
		case "shareHistory":
			caps.ShareHistory = v
		case "quickTransfer":
			caps.QuickTransfer = v
		case "favorite":
			caps.Favorite = v
		case "encryption":
			caps.Encryption = v
		case "playbackHistory":
			caps.PlaybackHistory = v
		case "copyTree":
			caps.CopyTree = v
		case "photoAlbum":
			caps.PhotoAlbum = v
		}
	}
	if extra != nil {
		extra(&caps)
	}
	caps.Provider = provider
	return caps
}

// SetUploadMode switches upload mode (direct for webdav/s3 etc.).
func (c *Capabilities) SetUploadMode(mode string) *Capabilities { c.UploadMode = mode; return c }

// SetHashes declares provide/rapid fingerprint types.
func (c *Capabilities) SetHashes(provide, rapid []string) *Capabilities {
	c.ProvideHashes = provide
	c.RapidUploadHashes = rapid
	return c
}

// SetShareExpirationOptions declares the exact expiration choices supported
// by the provider. Zero represents a permanent link. Keeping this on the
// capability contract prevents the UI from offering options the remote API
// cannot honor (for example 189/139 only expose 1 day, 7 days and forever).
func (c *Capabilities) SetShareExpirationOptions(days ...int) *Capabilities {
	c.ShareExpirationOptions = append([]int(nil), days...)
	return c
}

// SetConflictPolicies replaces the default conflict policy list.
func (c *Capabilities) SetConflictPolicies(policies ...string) *Capabilities {
	c.UploadConflictPolicies = policies
	return c
}
