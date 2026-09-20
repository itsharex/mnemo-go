// Package store provides local persistence for accounts, settings, tags,
// favorites, transfer history and share history. Backing store is a set of
// atomic JSON files under one directory (no cgo/sqlite dependency).
package store

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mnemo-go/internal/model"
)

// Store is the local persistence root.
// Most files live in dir; accountsDir overrides the location of
// accounts.json only (login credentials persist across installs).
type Store struct {
	dir         string
	accountsDir string
	mu          sync.Mutex
}

// Open creates (if needed) and opens the store directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// SetAccountsDir overrides the directory for accounts.json (login data).
func (s *Store) SetAccountsDir(d string) {
	if d != "" {
		_ = os.MkdirAll(d, 0o755)
		s.accountsDir = d
	}
}

// Dir returns the backing directory.
func (s *Store) Dir() string { return s.dir }

type directoryCacheDoc struct {
	Key       string       `json:"key,omitempty"`
	UpdatedAt int64        `json:"updatedAt"`
	Files     []model.File `json:"files"`
}

// LoadDirectoryCache reads one account-isolated directory snapshot. Cache
// files live under data/cache, which is co-located with the installation by
// config.DataDir. Directory snapshots intentionally have no time-based expiry:
// callers show them immediately and refresh from the provider in the
// background. Missing cache is represented by a nil slice and no error.
func (s *Store) LoadDirectoryCache(key string) ([]model.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := directoryCacheName(key)
	var doc directoryCacheDoc
	if err := s.readJSON(name, &doc); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return doc.Files, nil
}

// SaveDirectoryCache persists one directory snapshot without mixing it with
// accounts, transfer history, settings, or playback state.
func (s *Store) SaveDirectoryCache(key string, files []model.File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := directoryCacheName(key)
	if err := os.MkdirAll(filepath.Dir(s.path(name)), 0o755); err != nil {
		return err
	}
	snapshot := append([]model.File(nil), files...)
	for i := range snapshot {
		// Provider download/thumbnail URLs are frequently signed bearer URLs.
		// They expire independently and must not enter the long-lived cache.
		snapshot[i].DownloadURL = ""
		snapshot[i].Thumbnail = ""
	}
	if snapshot == nil && files != nil {
		snapshot = []model.File{}
	}
	return s.writeJSONUnlocked(name, directoryCacheDoc{Key: key, UpdatedAt: time.Now().Unix(), Files: snapshot})
}

type CachedSearchResult struct {
	UserID    string     `json:"userId"`
	DriveID   string     `json:"driveId"`
	ParentID  string     `json:"parentId"`
	UpdatedAt int64      `json:"updatedAt"`
	File      model.File `json:"file"`
}

func (s *Store) SearchDirectoryCache(keyword string) ([]CachedSearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CachedSearchResult{}
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return out, nil
	}
	entries, err := os.ReadDir(s.path(filepath.Join("cache", "directories")))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var doc directoryCacheDoc
		if s.readJSON(filepath.Join("cache", "directories", entry.Name()), &doc) != nil {
			continue
		}
		parts := strings.Split(doc.Key, "|")
		if len(parts) != 6 || parts[3] != "list" {
			continue
		}
		for i := range parts {
			parts[i], _ = url.PathUnescape(parts[i])
		}
		for _, file := range doc.Files {
			key := parts[1] + "|" + parts[2] + "|" + file.FileID
			if seen[key] || !strings.Contains(strings.ToLower(file.Name), keyword) {
				continue
			}
			seen[key] = true
			out = append(out, CachedSearchResult{parts[1], parts[2], parts[4], doc.UpdatedAt, file})
			if len(out) >= 1000 {
				return out, nil
			}
		}
	}
	return out, nil
}

// DeleteDirectoryCache removes one directory snapshot after a file mutation.
func (s *Store) DeleteDirectoryCache(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(directoryCacheName(key)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// DeleteDirectoryCachesForAccount removes every directory snapshot owned by
// one account. This prevents a removed or expired login from leaving cloud
// filenames behind in the long-lived cache.
func (s *Store) DeleteDirectoryCachesForAccount(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	dir := filepath.Join("cache", "directories")
	entries, err := os.ReadDir(s.path(dir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := filepath.Join(dir, entry.Name())
		var doc directoryCacheDoc
		if err := s.readJSON(name, &doc); err != nil {
			continue
		}
		parts := strings.Split(doc.Key, "|")
		if len(parts) != 6 {
			continue
		}
		owner, err := url.PathUnescape(parts[1])
		if err != nil || owner != userID {
			continue
		}
		if err := os.Remove(s.path(name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ClearCache removes only the application cache directory. It deliberately
// leaves credentials and all user-visible persistent state untouched.
func (s *Store) ClearCache() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := filepath.Join(s.dir, "cache")
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return os.MkdirAll(target, 0o755)
}

func directoryCacheName(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join("cache", "directories", fmt.Sprintf("%x.json", digest[:]))
}

// path returns the full path for a collection file, honoring accountsDir
// override for accounts.json.
func (s *Store) path(name string) string {
	if name == "accounts.json" && s.accountsDir != "" {
		return filepath.Join(s.accountsDir, name)
	}
	return filepath.Join(s.dir, name)
}

// readJSON loads a collection file; returns os.ErrNotExist when missing.
func (s *Store) readJSON(name string, v any) error {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// writeJSON persists a collection atomically (tmp + rename).
func (s *Store) writeJSON(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSONUnlocked(name, v)
}

func (s *Store) writeJSONUnlocked(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(name + ".tmp")
	// Store documents may contain share passwords, proxy credentials, or
	// transfer metadata. Keep every document private to the current user.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	target := s.path(name)
	if err := renameWithRetry(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(target, 0o600)
}

// renameWithRetry wraps os.Rename with a short retry loop. On Windows the
// target may be briefly locked by antivirus/indexing; a few retries with
// backoff avoid spurious persistence failures.
func renameWithRetry(src, dst string) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

// listFiles returns collection file names matching the prefix/suffix.
func (s *Store) listFiles(prefix, suffix string) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if prefix != "" && len(name) < len(prefix)+len(suffix) {
			continue
		}
		if prefix != "" && name[:len(prefix)] != prefix {
			continue
		}
		if suffix != "" && !hasSuffix(name, suffix) {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// errInvalid is a generic store error helper.
func errInvalid(msg string) error { return fmt.Errorf("store: %s", msg) }
