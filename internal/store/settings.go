package store

import (
	"fmt"
	"mnemo-go/internal/model"
	"os"
	"strings"
	"time"
)

const settingsFile = "settings.json"

// Settings is the persisted application settings document.
type Settings struct {
	Theme       string `json:"theme"`
	DefaultTab  string `json:"defaultTab"`
	Proxy       string `json:"proxy,omitempty"`
	DownloadDir string `json:"downloadDir,omitempty"`

	MaxConcurrentDownloads int   `json:"maxConcurrentDownloads"`
	MaxDownloadSpeed       int64 `json:"maxDownloadSpeed"` // bytes/s, 0 = unlimited
	MaxUploadSpeed         int64 `json:"maxUploadSpeed"`

	AutoUpdate     bool   `json:"autoUpdate"`
	ConfirmUpdate  bool   `json:"confirmUpdate"`
	PlaybackResume bool   `json:"playbackResume"`
	KeepTasks      bool   `json:"keepTasks"`
	LogLevel       string `json:"logLevel,omitempty"`
	// CloseToTray 为 nil 时表示默认开启「关闭不退出」（最小化到系统托盘）
	CloseToTray *bool `json:"closeToTray,omitempty"`
}

// CloseToTrayEnabled 返回「关闭窗口时最小化到托盘」是否生效（默认开启）。
func (s Settings) CloseToTrayEnabled() bool { return s.CloseToTray == nil || *s.CloseToTray }

// DefaultSettings returns sane defaults.
func DefaultSettings() Settings {
	return Settings{
		// 首次启动交由系统决定明暗模式；用户显式选择后才持久化覆盖。
		Theme:                  "system",
		DefaultTab:             "pan",
		MaxConcurrentDownloads: 3,
		MaxDownloadSpeed:       0,
		MaxUploadSpeed:         0,
		AutoUpdate:             true,
		ConfirmUpdate:          true,
		PlaybackResume:         true,
		KeepTasks:              true,
		LogLevel:               "info",
	}
}

// GetSettings loads settings, falling back to defaults.
func (s *Store) GetSettings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := DefaultSettings()
	err := s.readJSON(settingsFile, &st)
	if err != nil && !os.IsNotExist(err) {
		return st, err
	}
	return st, nil
}

// SetSettings persists settings.
func (s *Store) SetSettings(st Settings) error {
	return s.writeJSON(settingsFile, st)
}

// LocalTag is a user-defined colored label attached to a file path locally.
type LocalTag struct {
	UserID  string `json:"user_id"`
	DriveID string `json:"drive_id"`
	FileID  string `json:"file_id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	TagID   string `json:"tag_id"`
	Created int64  `json:"created"`
}

// TagDef is the tag palette definition (name + color).
type TagDef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

const tagsFile = "tags.json"

type tagsDoc struct {
	Defs  []TagDef   `json:"defs"`
	Marks []LocalTag `json:"marks"`
}

// ListTags returns the tag palette.
func (s *Store) ListTags() ([]TagDef, error) {
	doc, err := s.loadTags()
	if err != nil {
		return nil, err
	}
	return doc.Defs, nil
}

// SaveTags replaces the tag palette.
func (s *Store) SaveTags(defs []TagDef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadTagsUnlocked()
	if err != nil {
		return err
	}
	doc.Defs = defs
	return s.writeJSONUnlocked(tagsFile, doc)
}

// ListLocalTags returns local marks for an account.
func (s *Store) ListLocalTags(userID, driveID string) ([]LocalTag, error) {
	doc, err := s.loadTags()
	if err != nil {
		return nil, err
	}
	var out []LocalTag
	for _, m := range doc.Marks {
		if (userID == "" || m.UserID == userID) && (driveID == "" || m.DriveID == driveID) {
			out = append(out, m)
		}
	}
	return out, nil
}

// UpsertLocalTag adds or updates a local mark.
func (s *Store) UpsertLocalTag(t LocalTag) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadTagsUnlocked()
	if err != nil {
		return err
	}
	replaced := false
	for i, m := range doc.Marks {
		if m.UserID == t.UserID && m.DriveID == t.DriveID && m.FileID == t.FileID {
			doc.Marks[i] = t
			replaced = true
			break
		}
	}
	if !replaced {
		if t.Created == 0 {
			t.Created = time.Now().Unix()
		}
		doc.Marks = append(doc.Marks, t)
	}
	return s.writeJSONUnlocked(tagsFile, doc)
}

// DeleteLocalTag removes a local mark.
func (s *Store) DeleteLocalTag(userID, driveID, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadTagsUnlocked()
	if err != nil {
		return err
	}
	out := doc.Marks[:0]
	for _, m := range doc.Marks {
		if m.UserID == userID && m.DriveID == driveID && m.FileID == fileID {
			continue
		}
		out = append(out, m)
	}
	doc.Marks = out
	return s.writeJSONUnlocked(tagsFile, doc)
}

// CleanupLocalTags removes marks referencing deleted files.
func (s *Store) CleanupLocalTags(userID, driveID string, alive map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadTagsUnlocked()
	if err != nil {
		return err
	}
	out := doc.Marks[:0]
	for _, m := range doc.Marks {
		if m.UserID == userID && m.DriveID == driveID {
			if !alive[m.FileID] {
				continue
			}
		}
		out = append(out, m)
	}
	doc.Marks = out
	return s.writeJSONUnlocked(tagsFile, doc)
}

func (s *Store) loadTags() (tagsDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadTagsUnlocked()
}

func (s *Store) loadTagsUnlocked() (tagsDoc, error) {
	var doc tagsDoc
	err := s.readJSON(tagsFile, &doc)
	if err != nil && !os.IsNotExist(err) {
		return doc, err
	}
	return doc, nil
}

// Favorite is either a local bookmark or a cached native favorite. An empty
// Source denotes a legacy local bookmark.
type Favorite struct {
	UserID  string      `json:"user_id"`
	DriveID string      `json:"drive_id"`
	FileID  string      `json:"file_id"`
	Name    string      `json:"name"`
	IsDir   bool        `json:"isDir"`
	Added   int64       `json:"added"`
	Source  string      `json:"source,omitempty"`
	File    *model.File `json:"file,omitempty"`
}

const favoritesFile = "favorites.json"

// ReplaceRemoteFavorites refreshes a cloud snapshot without erasing legacy
// local bookmarks or another account's entries. Cloud rows win identical IDs.
func (s *Store) ReplaceRemoteFavorites(userID, driveID string, remote []Favorite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Favorite
	if err := s.readJSON(favoritesFile, &list); err != nil && !os.IsNotExist(err) {
		return err
	}
	ids := map[string]bool{}
	for _, f := range remote {
		if strings.TrimSpace(f.FileID) == "" || ids[f.FileID] {
			return fmt.Errorf("收藏快照包含空白或重复文件 ID")
		}
		ids[f.FileID] = true
	}
	added := map[string]int64{}
	out := make([]Favorite, 0, len(list)+len(remote))
	for _, f := range list {
		if f.UserID == userID && f.DriveID == driveID {
			added[f.FileID] = f.Added
		}
		if f.UserID == userID && f.DriveID == driveID && (f.Source == "cloud" || ids[f.FileID]) {
			continue
		}
		out = append(out, f)
	}
	for _, f := range remote {
		f.UserID, f.DriveID, f.Source = userID, driveID, "cloud"
		f.Added = added[f.FileID]
		if f.Added == 0 {
			f.Added = time.Now().Unix()
		}
		out = append(out, f)
	}
	return s.writeJSONUnlocked(favoritesFile, out)
}

// ListFavorites returns favorites of an account.
func (s *Store) ListFavorites(userID, driveID string) ([]Favorite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Favorite
	err := s.readJSON(favoritesFile, &list)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []Favorite
	for _, f := range list {
		if (userID == "" || f.UserID == userID) && (driveID == "" || f.DriveID == driveID) {
			out = append(out, f)
		}
	}
	return out, nil
}

// AddFavorite records a favorite.
func (s *Store) AddFavorite(f Favorite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Favorite
	err := s.readJSON(favoritesFile, &list)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for i, x := range list {
		if x.UserID == f.UserID && x.DriveID == f.DriveID && x.FileID == f.FileID {
			if f.Added == 0 {
				f.Added = x.Added
			}
			if f.Added == 0 {
				f.Added = time.Now().Unix()
			}
			list[i] = f
			return s.writeJSONUnlocked(favoritesFile, list)
		}
	}
	if f.Added == 0 {
		f.Added = time.Now().Unix()
	}
	list = append(list, f)
	return s.writeJSONUnlocked(favoritesFile, list)
}

// RemoveFavorite removes a favorite.
func (s *Store) RemoveFavorite(userID, driveID, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Favorite
	err := s.readJSON(favoritesFile, &list)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out := list[:0]
	for _, x := range list {
		if x.UserID == userID && x.DriveID == driveID && x.FileID == fileID {
			continue
		}
		out = append(out, x)
	}
	return s.writeJSONUnlocked(favoritesFile, out)
}

// LocalPlayCursor stores per-file playback progress for resume.
type PlayCursor struct {
	UserID  string  `json:"user_id"`
	DriveID string  `json:"drive_id"`
	FileID  string  `json:"file_id"`
	Seconds float64 `json:"seconds"`
	Updated int64   `json:"updated"`
}

const cursorFile = "playcursor.json"

// GetPlayCursor returns the saved cursor.
func (s *Store) GetPlayCursor(userID, driveID, fileID string) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []PlayCursor
	err := s.readJSON(cursorFile, &list)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	for _, c := range list {
		if c.UserID == userID && c.DriveID == driveID && c.FileID == fileID {
			return c.Seconds, nil
		}
	}
	return 0, nil
}

// SavePlayCursor persists playback progress.
func (s *Store) SavePlayCursor(c PlayCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []PlayCursor
	err := s.readJSON(cursorFile, &list)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for i, x := range list {
		if x.UserID == c.UserID && x.DriveID == c.DriveID && x.FileID == c.FileID {
			list[i] = c
			return s.writeJSONUnlocked(cursorFile, list)
		}
	}
	if c.Updated == 0 {
		c.Updated = time.Now().Unix()
	}
	list = append(list, c)
	return s.writeJSONUnlocked(cursorFile, list)
}
