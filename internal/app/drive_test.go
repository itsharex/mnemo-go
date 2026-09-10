package app

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mnemo-go/internal/config"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
	"mnemo-go/internal/store"
	"mnemo-go/internal/transfer"
)

func TestFavoritesBackupListsLocalRecordsWithoutNetwork(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddFavorite(store.Favorite{UserID: "pikpak:old", DriveID: "old", FileID: "file", Name: "photo.jpg"}); err != nil {
		t.Fatal(err)
	}
	drive.SetTokenResolver(func(_, _ string) (*model.TokenInfo, error) {
		t.Fatal("backup must not load account credentials")
		return nil, nil
	})
	t.Cleanup(func() { drive.SetTokenResolver(nil) })
	got, err := (&App{store: st}).ListFavorites("", "")
	if err != nil || len(got) != 1 {
		t.Fatalf("backup favorites = %+v, %v", got, err)
	}
}

type favoriteTestTransport func(*http.Request) (*http.Response, error)

func (f favoriteTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFavoritesCloudFailureDoesNotChangeLocalRecords(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const provider = "onedrive"
	status := http.StatusForbidden
	old := netx.TestTransportHook
	netx.TestTransportHook = favoriteTestTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"error":{"code":"accessDenied","message":"cloud unavailable"}}`
		if status == http.StatusOK {
			body = `{"id":"file"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	drive.SetTokenResolver(func(_, _ string) (*model.TokenInfo, error) {
		return &model.TokenInfo{AccessToken: "token", ProviderDriveType: "business"}, nil
	})
	t.Cleanup(func() { netx.TestTransportHook = old; drive.SetTokenResolver(nil) })
	user, space := provider+"_user", provider
	f := store.Favorite{UserID: user, DriveID: space, FileID: "file", Name: "old.jpg", Source: "cloud"}
	if err := st.AddFavorite(f); err != nil {
		t.Fatal(err)
	}
	a := &App{store: st}
	if _, err := a.ListFavorites(user, space); err == nil {
		t.Fatal("list error hidden")
	}
	if err := a.RemoveFavorite(user, space, "file"); err == nil {
		t.Fatal("remove error hidden")
	}
	f.Name = "new.jpg"
	if err := a.AddFavorite(user, space, f); err == nil {
		t.Fatal("add error hidden")
	}
	got, err := st.ListFavorites(user, space)
	if err != nil || len(got) != 1 || got[0].Name != "old.jpg" {
		t.Fatalf("cloud failure changed cache: %+v, %v", got, err)
	}
	status = http.StatusOK
	f.File = &model.File{FileID: "other", Name: "old.jpg", DownloadURL: "https://secret.invalid/signed", Size: 42, ParentFileID: "parent"}
	if err := a.AddFavorite(user, space, f); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListFavorites(user, space)
	if got[0].Source != "cloud" || got[0].File.FileID != "file" || got[0].File.DownloadURL != "" || got[0].File.Size != 42 {
		t.Fatalf("invalid stored snapshot: %+v", got)
	}
	if err := a.RemoveFavorite(user, space, "file"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListFavorites(user, space)
	if len(got) != 0 {
		t.Fatal("removed favorite remains")
	}
}

func TestFavoritesLocalFallbackForEveryUnsupportedProvider(t *testing.T) {
	old := netx.TestTransportHook
	netx.TestTransportHook = favoriteTestTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("local favorites made a network request: %s", r.URL)
		return nil, nil
	})
	drive.SetTokenResolver(func(_, _ string) (*model.TokenInfo, error) {
		return &model.TokenInfo{ProviderDriveType: "personal"}, nil
	})
	t.Cleanup(func() { netx.TestTransportHook = old; drive.SetTokenResolver(nil) })
	for _, provider := range []string{"onedrive", "dropbox", "lanzou", "ilanzou", "pan139", "pan189", "yike", "guangya", "webdav", "s3"} {
		t.Run(provider, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			user := provider + "_local"
			if provider == "webdav" || provider == "s3" {
				user = provider + ":local"
			}
			a := &App{store: st}
			f := store.Favorite{FileID: "file", Name: "movie.mp4", File: &model.File{Size: 42, ParentFileID: "folder"}}
			if err := a.AddFavorite(user, "space", f); err != nil {
				t.Fatal(err)
			}
			list, err := a.ListFavorites(user, "space")
			if err != nil || len(list) != 1 || list[0].Source != "local" || list[0].File.Size != 42 {
				t.Fatalf("local favorites=%+v, %v", list, err)
			}
			if err := a.RemoveFavorite(user, "space", "file"); err != nil {
				t.Fatal(err)
			}
			list, err = a.ListFavorites(user, "space")
			if err != nil || len(list) != 0 {
				t.Fatalf("removed favorite remains: %+v %v", list, err)
			}
		})
	}
}

func TestRestoreFavoriteNeverReplaysCloudChangesOrOverwritesCurrentRecord(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	drive.SetTokenResolver(func(_, _ string) (*model.TokenInfo, error) {
		t.Fatal("restore read account credentials")
		return nil, nil
	})
	t.Cleanup(func() { drive.SetTokenResolver(nil) })
	a := &App{store: st}
	f := store.Favorite{UserID: "pikpak_removed", DriveID: "d", FileID: "file", Name: "old.jpg", Source: "cloud", Added: 25, File: &model.File{DownloadURL: "https://secret.invalid/signed", Thumbnail: "https://secret.invalid/thumb", Size: 42}}
	if err := a.RestoreFavorite(f); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ListFavorites(f.UserID, f.DriveID)
	if len(got) != 1 || got[0].Source != "local" || got[0].Added != 25 || got[0].File.DownloadURL != "" || got[0].File.Thumbnail != "" {
		t.Fatalf("unsafe restored favorite: %+v", got)
	}
	if err := a.RemoveFavorite(f.UserID, f.DriveID, f.FileID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListFavorites(f.UserID, f.DriveID)
	if len(got) != 0 {
		t.Fatal("restored local favorite was not removed")
	}
	f.Name, f.Source = "current.jpg", "cloud"
	if err := st.AddFavorite(f); err != nil {
		t.Fatal(err)
	}
	f.Name, f.Source = "backup.jpg", "local"
	if err := a.RestoreFavorite(f); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListFavorites(f.UserID, f.DriveID)
	if got[0].Name != "current.jpg" || got[0].Source != "cloud" {
		t.Fatal("backup overwrote current favorite")
	}
}

func TestSettingsReadFailureReachesCaller(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{store: st}
	if _, err := a.GetSettings(); err == nil {
		t.Fatal("corrupt settings must return an error rather than defaults")
	}
}

func TestSettingsRejectInvalidDownloadDirectoryWithoutPersisting(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	previous := store.DefaultSettings()
	previous.DownloadDir = filepath.Join(dir, "downloads")
	if err := st.SetSettings(previous); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := previous
	changed.DownloadDir = blocked
	if err := (&App{store: st}).SaveSettings(changed); err == nil {
		t.Fatal("saving a regular file as the download directory must fail")
	}
	actual, err := st.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if actual.DownloadDir != previous.DownloadDir {
		t.Fatalf("failed validation replaced settings: %q", actual.DownloadDir)
	}
}

func TestSettingsNormalizeDownloadDirectoryAndRestoreDefault(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("MNEMO_DOWNLOAD_TEST_ROOT", dir)
	m, err := transfer.NewManager(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	a := &App{store: st, dl: m}
	s := store.DefaultSettings()
	s.DownloadDir = ` "%MNEMO_DOWNLOAD_TEST_ROOT%/中文 下载" `
	if err := a.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	saved, _ := st.GetSettings()
	want := filepath.Join(dir, "中文 下载")
	if saved.DownloadDir != want {
		t.Fatalf("saved directory = %q, want %q", saved.DownloadDir, want)
	}
	if got, err := a.GetDownloadDirectory(); err != nil || got != want {
		t.Fatalf("runtime directory = %q, %v", got, err)
	}
	saved.DownloadDir = ""
	if err := a.SaveSettings(saved); err != nil {
		t.Fatal(err)
	}
	wantDefault, err := config.DefaultDownloadDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.GetDownloadDirectory(); err != nil || got != wantDefault {
		t.Fatalf("restored runtime directory = %q, %v", got, err)
	}
	saved, _ = st.GetSettings()
	if saved.DownloadDir != "" {
		t.Fatal("system default was hardcoded into settings")
	}
}

func TestShouldPersistShareHistory(t *testing.T) {
	if shouldPersistShareHistory(&model.ShareItem{SharePolicy: "presigned"}) {
		t.Fatal("presigned URL must not be persisted as share history")
	}
	if !shouldPersistShareHistory(&model.ShareItem{SharePolicy: "public"}) {
		t.Fatal("provider-managed share should be persisted")
	}
}

func TestValidateShareRecordProvider(t *testing.T) {
	if err := validateShareRecordProvider(model.ShareHistoryEntry{}, model.ProviderDropbox); err != nil {
		t.Fatalf("legacy share record should remain usable: %v", err)
	}
	if err := validateShareRecordProvider(model.ShareHistoryEntry{Provider: model.ProviderDropbox}, model.ProviderDropbox); err != nil {
		t.Fatalf("matching provider should be accepted: %v", err)
	}
	if err := validateShareRecordProvider(model.ShareHistoryEntry{Provider: model.ProviderDropbox}, model.ProviderOnedrive); err == nil {
		t.Fatal("mismatched provider must be rejected before cancellation")
	}
}
