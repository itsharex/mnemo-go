package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mnemo-go/internal/model"
	syncmodel "mnemo-go/internal/sync"
)

func TestCachedSearchKeepsAccountAndParentIdentity(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := model.File{FileID: "same", Name: "Report.txt"}
	for _, key := range []string{"webdav|account-a|drive-a|list|folder%2Fa|", "webdav|account-b|drive-b|list|root|"} {
		if err := st.SaveDirectoryCache(key, []model.File{file}); err != nil {
			t.Fatal(err)
		}
	}
	results, err := st.SearchDirectoryCache("report")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("cross-account results merged: %+v", results)
	}
	for _, r := range results {
		if r.UserID == "account-a" && r.ParentID != "folder/a" {
			t.Fatalf("parent identity lost: %+v", r)
		}
	}
	if err := st.ClearCache(); err != nil {
		t.Fatal(err)
	}
	results, err = st.SearchDirectoryCache("report")
	if err != nil || len(results) != 0 {
		t.Fatalf("cleared cache is still searchable: %+v %v", results, err)
	}
}

func TestOpenAndSettings(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// default settings
	s, err := st.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.MaxConcurrentDownloads <= 0 {
		t.Error("default MaxConcurrentDownloads should be positive")
	}
	// update settings
	s.Proxy = "http://127.0.0.1:7890"
	s.MaxDownloadSpeed = 1024000
	if err := st.SetSettings(s); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	// verify persistence
	s2, _ := st.GetSettings()
	if s2.Proxy != "http://127.0.0.1:7890" {
		t.Errorf("Proxy not persisted: %s", s2.Proxy)
	}
	if s2.MaxDownloadSpeed != 1024000 {
		t.Errorf("MaxDownloadSpeed not persisted: %d", s2.MaxDownloadSpeed)
	}
}

func TestFavoriteCloudSnapshotsPreserveLocalRecordsAndAddedTime(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Favorite{
		{UserID: "u", DriveID: "d", FileID: "local", Added: 10},
		{UserID: "u", DriveID: "d", FileID: "both", Added: 20},
		{UserID: "other", DriveID: "d", FileID: "cloud", Source: "cloud", Added: 30},
		{UserID: "u", DriveID: "other", FileID: "cloud", Source: "cloud", Added: 40},
	} {
		if err := st.AddFavorite(f); err != nil {
			t.Fatal(err)
		}
	}
	remote := []Favorite{{FileID: "both", Name: "renamed"}, {FileID: "cloud", Name: "fresh"}}
	if err := st.ReplaceRemoteFavorites("u", "d", remote); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ListFavorites("u", "d")
	if len(got) != 3 {
		t.Fatalf("merged favorites = %+v", got)
	}
	for _, f := range got {
		if f.Added == 0 || f.FileID == "both" && f.Added != 20 {
			t.Fatalf("lost favorite timestamp: %+v", f)
		}
	}
	if err := st.ReplaceRemoteFavorites("u", "d", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListFavorites("", "")
	if len(got) != 3 {
		t.Fatalf("snapshot erased local/other account records: %+v", got)
	}
}

func TestAtomicJSONWriteCleansTempAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const name = "blocked.json"
	if err := os.Mkdir(st.path(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.writeJSON(name, map[string]string{"state": "new"}); err == nil {
		t.Fatal("writeJSON unexpectedly replaced a directory")
	}
	if _, err := os.Stat(st.path(name + ".tmp")); !os.IsNotExist(err) {
		t.Fatalf("failed atomic write left a temporary file: %v", err)
	}
	info, err := os.Stat(st.path(name))
	if err != nil || !info.IsDir() {
		t.Fatalf("existing target was changed after failed rename: info=%#v err=%v", info, err)
	}
}

func TestUploadSessionStore(t *testing.T) {
	dir := t.TempDir()
	InitUploadSessions(dir)
	key := "testkey123"
	parts := []int{1, 3, 5}
	if err := SaveUploadSession(key, parts); err != nil {
		t.Fatalf("SaveUploadSession: %v", err)
	}
	got := LoadUploadSession(key)
	if len(got) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(got))
	}
	if got[0] != 1 || got[1] != 3 || got[2] != 5 {
		t.Errorf("parts mismatch: %v", got)
	}
	ClearUploadSession(key)
	got2 := LoadUploadSession(key)
	if len(got2) > 0 {
		t.Error("session should be cleared")
	}
}

func TestAccountSaveLoad(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	acc := &model.Account{
		UserID: "pikpak_test123",
		Token: &model.TokenInfo{
			UserName:          "testuser",
			ProviderAccountID: "123",
		},
	}
	if err := st.SaveAccount(acc); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	list, err := st.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 account, got %d", len(list))
	}
	if list[0].UserID != "pikpak_test123" {
		t.Errorf("UserID mismatch: %s", list[0].UserID)
	}
	got, err := st.GetAccount("pikpak_test123")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.Token.UserName != "testuser" {
		t.Errorf("UserName mismatch: %s", got.Token.UserName)
	}
	_ = filepath.Join(dir, "unused")
}

func TestRenameMountedAccountKeepsIdentityAndCredentials(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	forcePath := true
	acc := &model.Account{
		UserID:  "webdav:mount-1",
		DriveID: "webdav:mount-1",
		Token: &model.TokenInfo{
			TokenFrom:   model.ProviderWebdav,
			UserName:    "旧名称",
			AccessToken: "unused",
			Conn: &model.ConnConfig{
				Name: "旧名称", Endpoint: "https://dav.example.test/", Username: "alice", Password: "secret", ForcePathStyle: &forcePath,
			},
		},
	}
	if err := st.SaveAccount(acc); err != nil {
		t.Fatal(err)
	}
	updated, err := st.RenameMountedAccount(acc.UserID, "家庭 WebDAV")
	if err != nil {
		t.Fatal(err)
	}
	if updated.UserID != acc.UserID || updated.DriveID != acc.DriveID {
		t.Fatalf("identity changed after rename: %#v", updated)
	}
	if updated.Token.UserName != "家庭 WebDAV" || updated.Token.Conn.Name != "家庭 WebDAV" || updated.Token.Conn.Password != "secret" {
		t.Fatalf("rename did not preserve mounted credentials/name: %#v", updated.Token)
	}
}

func TestListAccountsIgnoresNullEntries(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Plaintext is a supported legacy format and is intentionally used here
	// to exercise recovery of a partially corrupted account list.
	content := `[
  null,
  {"user_id":"valid-account","order":1}
]`
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("write legacy accounts: %v", err)
	}
	list, err := st.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(list) != 1 || list[0] == nil || list[0].UserID != "valid-account" {
		t.Fatalf("unexpected accounts: %#v", list)
	}
	account, err := st.GetAccount("valid-account")
	if err != nil || account == nil {
		t.Fatalf("GetAccount: account=%#v err=%v", account, err)
	}
}

func TestDirectoryCacheIsolationAndClear(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	files := []model.File{{FileID: "file-1", Name: "one.txt"}}
	if err := st.SaveDirectoryCache("provider|account-a|drive-a|list|root|", files); err != nil {
		t.Fatalf("SaveDirectoryCache: %v", err)
	}
	got, err := st.LoadDirectoryCache("provider|account-a|drive-a|list|root|")
	if err != nil {
		t.Fatalf("LoadDirectoryCache: %v", err)
	}
	if len(got) != 1 || got[0].FileID != "file-1" {
		t.Fatalf("cached files mismatch: %#v", got)
	}
	other, err := st.LoadDirectoryCache("provider|account-b|drive-a|list|root|")
	if err != nil {
		t.Fatalf("LoadDirectoryCache(other): %v", err)
	}
	if other != nil {
		t.Fatalf("cache leaked between accounts: %#v", other)
	}
	if _, err := os.Stat(filepath.Join(dir, "cache", "directories")); err != nil {
		t.Fatalf("cache directory missing: %v", err)
	}
	if err := st.DeleteDirectoryCache("provider|account-a|drive-a|list|root|"); err != nil {
		t.Fatalf("DeleteDirectoryCache: %v", err)
	}
	deleted, err := st.LoadDirectoryCache("provider|account-a|drive-a|list|root|")
	if err != nil {
		t.Fatalf("LoadDirectoryCache after delete: %v", err)
	}
	if deleted != nil {
		t.Fatalf("cache still present after delete: %#v", deleted)
	}
	if err := st.SaveDirectoryCache("provider|account-a|drive-a|list|root|", files); err != nil {
		t.Fatalf("SaveDirectoryCache after delete: %v", err)
	}
	if err := st.SetSettings(Settings{}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	if err := st.ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	cleared, err := st.LoadDirectoryCache("provider|account-a|drive-a|list|root|")
	if err != nil {
		t.Fatalf("LoadDirectoryCache after clear: %v", err)
	}
	if cleared != nil {
		t.Fatalf("cache still present after clear: %#v", cleared)
	}
	if _, err := st.GetSettings(); err != nil {
		t.Fatalf("ClearCache removed settings: %v", err)
	}
}

func TestDirectoryCacheDoesNotPersistExpiringProviderURLs(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := "provider|account-a|drive-a|list|root|"
	files := []model.File{{FileID: "file-1", Name: "one.txt", DownloadURL: "https://download.test/?token=secret", Thumbnail: "https://thumb.test/?token=secret"}}
	if err := st.SaveDirectoryCache(key, files); err != nil {
		t.Fatal(err)
	}
	if files[0].DownloadURL == "" || files[0].Thumbnail == "" {
		t.Fatal("SaveDirectoryCache mutated the caller's file slice")
	}
	cached, err := st.LoadDirectoryCache(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 1 || cached[0].DownloadURL != "" || cached[0].Thumbnail != "" {
		t.Fatalf("long-lived cache retained an expiring provider URL: %#v", cached)
	}
	raw, err := os.ReadFile(st.path(directoryCacheName(key)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("cache file contains signed URL material: %s", raw)
	}
}

func TestDirectoryCacheRemainsAvailableAcrossRestartsWithoutExpiry(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := "provider|account-a|drive-a|list|root|"
	if err := st.SaveDirectoryCache(key, []model.File{{FileID: "persisted"}}); err != nil {
		t.Fatal(err)
	}
	name := directoryCacheName(key)
	if err := st.writeJSON(name, directoryCacheDoc{
		Key:       key,
		UpdatedAt: time.Now().Add(-365 * 24 * time.Hour).Unix(),
		Files:     []model.File{{FileID: "persisted"}},
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := reopened.LoadDirectoryCache(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].FileID != "persisted" {
		t.Fatalf("long-lived directory cache was lost: %#v", files)
	}
	if _, err := os.Stat(reopened.path(name)); err != nil {
		t.Fatalf("long-lived directory cache file was removed: %v", err)
	}
}

func TestDeleteDirectoryCachesForAccountKeepsOtherAccounts(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{
		"webdav|webdav%3Aaccount-a|drive-a|list|%2F|",
		"webdav|webdav%3Aaccount-a|drive-a|list|folder|",
		"webdav|webdav%3Aaccount-b|drive-b|list|%2F|",
	}
	for _, key := range keys {
		if err := st.SaveDirectoryCache(key, []model.File{{FileID: key}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteDirectoryCachesForAccount("webdav:account-a"); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys[:2] {
		if files, err := st.LoadDirectoryCache(key); err != nil || files != nil {
			t.Fatalf("removed account cache remains: key=%q files=%#v err=%v", key, files, err)
		}
	}
	files, err := st.LoadDirectoryCache(keys[2])
	if err != nil || len(files) != 1 {
		t.Fatalf("other account cache was removed: files=%#v err=%v", files, err)
	}
}

func TestTaskSavesKeepConcurrentRecords(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := st.SaveDownloadTask(&model.DownloadTask{ID: fmt.Sprintf("dl-%d", i), Name: "file"}); err != nil {
				t.Errorf("SaveDownloadTask(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	list, err := st.ListDownloadTasks()
	if err != nil {
		t.Fatalf("ListDownloadTasks: %v", err)
	}
	if len(list) != 20 {
		t.Fatalf("concurrent task saves kept %d records, want 20", len(list))
	}
}

func TestDownloadTaskPersistenceOmitsSecrets(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task := &model.DownloadTask{
		ID:      "dl-sensitive",
		URL:     "https://download.example/file?access_token=secret123",
		Headers: map[string]string{"Authorization": "Bearer secret123", "Cookie": "sid=secret123"},
		Error:   "password=secret123 url=https://download.example/file?access_token=secret123",
	}
	if err := st.SaveDownloadTask(task); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, tasksFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret123") || strings.Contains(string(raw), "authorization") || strings.Contains(string(raw), "headers") {
		t.Fatalf("tasks file contains sensitive download state: %s", raw)
	}
	list, err := st.ListDownloadTasks()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListDownloadTasks: tasks=%#v err=%v", list, err)
	}
	if list[0].URL != "" || list[0].Headers != nil || strings.Contains(list[0].Error, "secret123") {
		t.Fatalf("persisted task retained secrets: %#v", list[0])
	}
}

func TestSyncConfigSavesKeepConcurrentRecords(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := syncmodel.Config{ID: fmt.Sprintf("sync-%d", i), Name: "job"}
			if err := st.SaveSyncConfig(cfg); err != nil {
				t.Errorf("SaveSyncConfig(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	list, err := st.ListSyncConfigs()
	if err != nil {
		t.Fatalf("ListSyncConfigs: %v", err)
	}
	if len(list) != 20 {
		t.Fatalf("concurrent sync config saves kept %d records, want 20", len(list))
	}
}

func TestSyncSnapshotFileCannotEscapeStore(t *testing.T) {
	unsafe := syncSnapshotFile("../../outside")
	if filepath.Base(unsafe) != unsafe || unsafe == "SyncSnapshot_../../outside.json" {
		t.Fatalf("unsafe snapshot file name: %q", unsafe)
	}
	if got := syncSnapshotFile("sync-123_OK"); got != "SyncSnapshot_sync-123_OK.json" {
		t.Fatalf("safe snapshot ID should remain compatible, got %q", got)
	}

	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSyncSnapshot("../../outside", []syncmodel.Entry{{RemoteName: "safe.txt"}}); err != nil {
		t.Fatalf("SaveSyncSnapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.dir, unsafe)); err != nil {
		t.Fatalf("hashed snapshot was not stored inside data directory: %v", err)
	}
}

func TestShareHistoryReadsDuringConcurrentSaves(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entry := model.ShareHistoryEntry{
				ShareID: fmt.Sprintf("share-%d", i), AccountID: "pikpak-account",
				DriveID: "pikpak-drive", ShareURL: fmt.Sprintf("https://example/%d", i),
				ShareName: "share",
			}
			if err := st.SaveShareHistory(entry); err != nil {
				t.Errorf("SaveShareHistory(%d): %v", i, err)
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.ListShareHistory("pikpak-account"); err != nil {
				t.Errorf("ListShareHistory: %v", err)
			}
		}()
	}
	wg.Wait()
	list, err := st.ListShareHistory("pikpak-account")
	if err != nil {
		t.Fatalf("ListShareHistory(final): %v", err)
	}
	if len(list) != 20 {
		t.Fatalf("concurrent share history saves kept %d records, want 20", len(list))
	}
}

func TestDeleteShareHistoryKeepsOtherAccountAndEntries(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entries := []model.ShareHistoryEntry{
		{ShareID: "share-1", AccountID: "account-a", ShareURL: "https://example/a"},
		{ShareID: "share-2", AccountID: "account-a", ShareURL: "https://example/b"},
		{ShareID: "share-1", AccountID: "account-b", ShareURL: "https://example/c"},
		{ShareID: "", AccountID: "account-a", ShareURL: "https://example/legacy"},
	}
	for _, entry := range entries {
		if err := st.SaveShareHistory(entry); err != nil {
			t.Fatalf("SaveShareHistory: %v", err)
		}
	}
	if err := st.DeleteShareHistory("account-a", "share-1", ""); err != nil {
		t.Fatalf("DeleteShareHistory by id: %v", err)
	}
	if err := st.DeleteShareHistory("account-a", "", "https://example/legacy"); err != nil {
		t.Fatalf("DeleteShareHistory by url: %v", err)
	}
	list, err := st.ListShareHistory("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("remaining history = %+v, want two entries", list)
	}
	for _, entry := range list {
		if entry.AccountID == "account-a" && (entry.ShareID == "share-1" || entry.ShareURL == "https://example/legacy") {
			t.Fatalf("deleted entry still present: %+v", entry)
		}
	}
}

func TestMigrateJobPersistenceKeepsConcurrentUpdates(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job := &model.MigrateJob{ID: fmt.Sprintf("mig-%d", i), Status: "completed"}
			if err := st.SaveMigrateJob(job); err != nil {
				t.Errorf("SaveMigrateJob(%d): %v", i, err)
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.ListMigrateJobs(); err != nil {
				t.Errorf("ListMigrateJobs: %v", err)
			}
		}()
	}
	wg.Wait()
	list, err := st.ListMigrateJobs()
	if err != nil {
		t.Fatalf("ListMigrateJobs(final): %v", err)
	}
	if len(list) != 20 {
		t.Fatalf("concurrent migrate job saves kept %d records, want 20", len(list))
	}
}

func TestMigrateJobPersistenceKeepsRecoveryCheckpoints(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := &model.MigrateJob{
		ID:                 "resume-job",
		Status:             "partial",
		FileIDs:            []string{"root"},
		CompletedFileIDs:   []string{"child-complete"},
		CopiedFileIDs:      []string{"child-cleanup"},
		TargetDirectoryIDs: map[string]string{"root": "destination-root"},
	}
	if err := st.SaveMigrateJob(want); err != nil {
		t.Fatalf("SaveMigrateJob: %v", err)
	}
	jobs, err := st.ListMigrateJobs()
	if err != nil {
		t.Fatalf("ListMigrateJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("job count = %d, want 1", len(jobs))
	}
	got := jobs[0]
	if len(got.CompletedFileIDs) != 1 || got.CompletedFileIDs[0] != "child-complete" {
		t.Fatalf("completed checkpoints = %#v", got.CompletedFileIDs)
	}
	if len(got.CopiedFileIDs) != 1 || got.CopiedFileIDs[0] != "child-cleanup" {
		t.Fatalf("copied checkpoints = %#v", got.CopiedFileIDs)
	}
	if got.TargetDirectoryIDs["root"] != "destination-root" {
		t.Fatalf("target directory checkpoints = %#v", got.TargetDirectoryIDs)
	}
}
