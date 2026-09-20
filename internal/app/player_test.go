package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
	"mnemo-go/internal/transfer/migrate"
)

func TestExpirationTimeUsesEarliestKnownValue(t *testing.T) {
	got := expirationTime(1_800_000_300, 1_800_000_100_000)
	want := time.UnixMilli(1_800_000_100_000)
	if !got.Equal(want) {
		t.Fatalf("expirationTime() = %v, want %v", got, want)
	}
}

func TestSourceExpirationPrefersEarlierQualityExpiry(t *testing.T) {
	preview := &model.VideoPreview{ExpireTime: 1_800_000_500}
	quality := model.VideoQuality{ExpireTime: 1_800_000_100}
	got := sourceExpiration(preview, quality)
	want := time.UnixMilli(1_800_000_100_000)
	if !got.Equal(want) {
		t.Fatalf("sourceExpiration() = %v, want %v", got, want)
	}
}

func TestResumeMigrateUsesPersistedCheckpoints(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a := NewApp()
	a.store = st
	a.migrate = migrate.NewEngine(st, nil)
	if err := st.SaveMigrateJob(&migrate.Job{
		ID:               "resume-checkpoint",
		Status:           "canceled",
		FileIDs:          []string{"already-done"},
		CompletedFileIDs: []string{"already-done"},
	}); err != nil {
		t.Fatalf("SaveMigrateJob: %v", err)
	}
	if _, err := a.ResumeMigrate("resume-checkpoint"); err != nil {
		t.Fatalf("ResumeMigrate: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jobs, err := st.ListMigrateJobs()
		if err != nil {
			t.Fatalf("ListMigrateJobs: %v", err)
		}
		if len(jobs) == 1 && jobs[0].Status == "completed" {
			if jobs[0].Processed != 1 || len(jobs[0].CompletedFileIDs) != 1 {
				t.Fatalf("recovered job = %#v", jobs[0])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resumed migration did not complete from its checkpoint")
}

func TestSanitizeVideoPreviewHidesProviderCredentials(t *testing.T) {
	preview := &model.VideoPreview{
		Headers: map[string]string{"Authorization": "Bearer secret"},
		Qualities: []model.VideoQuality{{
			HTML:    "https://provider.example/play?signature=secret",
			URL:     "https://provider.example/play?signature=secret",
			Headers: map[string]string{"Cookie": "secret"},
			Label:   "原画",
			Value:   "origin",
		}},
	}
	sanitizeVideoPreview(preview)
	if preview.Headers != nil {
		t.Fatalf("preview headers were not cleared: %#v", preview.Headers)
	}
	quality := preview.Qualities[0]
	if quality.HTML != "" || quality.URL != "" || quality.Headers != nil {
		t.Fatalf("provider details were not cleared: %#v", quality)
	}
	if quality.Label != "原画" || quality.Value != "origin" {
		t.Fatalf("player selection metadata changed: %#v", quality)
	}
}

func TestVideoStreamType(t *testing.T) {
	cases := map[string]struct {
		declared string
		filename string
		want     string
	}{
		"declared hls":          {declared: "m3u8", filename: "video.mp4", want: "hls"},
		"dash extension":        {filename: "video.mpd", want: "dash"},
		"unsupported container": {filename: "video.mkv", want: "mkv"},
		"native mp4":            {filename: "video.mov", want: "mp4"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := videoStreamType(tc.declared, tc.filename); got != tc.want {
				t.Fatalf("videoStreamType(%q, %q) = %q, want %q", tc.declared, tc.filename, got, tc.want)
			}
		})
	}
}

func TestSyncAccountUsageBuildsDisplayQuota(t *testing.T) {
	acc := &model.Account{Token: &model.TokenInfo{TotalSize: 4096, FreeSize: 3072}}
	syncAccountUsage(acc)
	if acc.Usage == nil {
		t.Fatal("syncAccountUsage() did not build quota")
	}
	if acc.Usage.Size != 4096 || acc.Usage.Used != 1024 {
		t.Fatalf("quota = %#v, want size=4096 used=1024", acc.Usage)
	}
	if acc.Usage.SizeStr == "" || acc.Usage.UsedStr == "" {
		t.Fatalf("formatted quota is incomplete: %#v", acc.Usage)
	}
	if acc.Usage.Status != "available" {
		t.Fatalf("quota status = %q, want available", acc.Usage.Status)
	}
}

func TestSyncAccountUsageMarksUnlimitedProviders(t *testing.T) {
	for _, provider := range []string{model.ProviderYike, model.ProviderLanzou} {
		t.Run(provider, func(t *testing.T) {
			acc := &model.Account{
				UserID: model.BuildUserID(provider, "account-1"),
				Token:  &model.TokenInfo{TokenFrom: provider},
			}
			markQuotaRefreshSuccess(acc)
			if acc.Usage == nil {
				t.Fatal("syncAccountUsage() did not build unlimited quota")
			}
			if acc.Usage.Type != "unlimited" || acc.Usage.Status != "available" || acc.Usage.Description != "无限空间" {
				t.Fatalf("quota = %#v, want unlimited available", acc.Usage)
			}
		})
	}
}

func TestQuotaRefreshStatusDistinguishesUnsupportedAndRateLimit(t *testing.T) {
	unsupported := &model.Account{Token: &model.TokenInfo{}}
	markQuotaRefreshSuccess(unsupported)
	if unsupported.Usage == nil || unsupported.Usage.Status != "unsupported" || unsupported.Usage.UpdatedAt <= 0 {
		t.Fatalf("unsupported quota status = %#v", unsupported.Usage)
	}

	limited := &model.Account{Token: &model.TokenInfo{TotalSize: 4096, UsedSize: 1024}}
	markQuotaRefreshFailure(limited, errors.New("HTTP 429 rate limited"))
	if limited.Usage == nil || limited.Usage.Status != "rate_limited" {
		t.Fatalf("rate limited quota status = %#v", limited.Usage)
	}
	if limited.Usage.Size != 4096 || limited.Usage.Used != 1024 {
		t.Fatalf("last known quota was lost: %#v", limited.Usage)
	}
}

func TestAccountRefreshRiskFailureUsesCooldown(t *testing.T) {
	a := NewApp()
	a.markAccountRefreshFailure("pikpak_test", errors.New("HTTP 429 rate limited"))
	if !a.accountRefreshCached("pikpak_test") {
		t.Fatal("risk-control refresh failure did not enter cooldown")
	}
}

type testRetryAfterError struct{ delay time.Duration }

func (e testRetryAfterError) Error() string             { return "rate limited" }
func (e testRetryAfterError) RetryAfter() time.Duration { return e.delay }

func TestAccountRefreshHonorsProviderRetryAfter(t *testing.T) {
	a := NewApp()
	const requested = 2 * time.Hour
	before := time.Now()
	a.markAccountRefreshFailure("provider_test", testRetryAfterError{delay: requested})
	a.accountRefreshMu.Lock()
	until := a.accountRefreshRetryAfter["provider_test"]
	a.accountRefreshMu.Unlock()
	if until.Before(before.Add(requested - time.Second)) {
		t.Fatalf("retry cooldown = %v, want at least %v", until.Sub(before), requested)
	}
}

func TestRefreshAccountNowKeepsShortManualGap(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a := NewApp()
	a.store = st
	userID := "manual-refresh-gap"

	a.accountRefreshMu.Lock()
	a.accountRefreshLast = map[string]time.Time{userID: time.Now()}
	a.accountRefreshMu.Unlock()
	_, _ = a.RefreshAccountNow(userID)
	a.accountRefreshMu.Lock()
	_, kept := a.accountRefreshLast[userID]
	a.accountRefreshMu.Unlock()
	if !kept {
		t.Fatal("short manual gap should keep the cached refresh timestamp")
	}

	a.accountRefreshMu.Lock()
	a.accountRefreshLast[userID] = time.Now().Add(-accountRefreshManualGap)
	a.accountRefreshMu.Unlock()
	_, _ = a.RefreshAccountNow(userID)
	a.accountRefreshMu.Lock()
	_, cleared := a.accountRefreshLast[userID]
	a.accountRefreshMu.Unlock()
	if cleared {
		t.Fatal("manual refresh after the short gap should invalidate the success cache")
	}
}

func TestDriveUnauthorizedRemovesOnlyExpiredLocalAccount(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	account := &model.Account{
		UserID:  "dropbox_expired",
		DriveID: "dropbox:expired",
		Token: &model.TokenInfo{
			TokenFrom: "dropbox",
			UserName:  "已过期账号",
		},
	}
	if err := st.SaveAccount(account); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	cacheKey := "dropbox|dropbox_expired|dropbox%3Aexpired|list|root|"
	if err := st.SaveDirectoryCache(cacheKey, []model.File{{FileID: "cached-file"}}); err != nil {
		t.Fatalf("SaveDirectoryCache: %v", err)
	}
	a := &App{store: st}

	a.handleDriveOperationError(account.UserID, account.DriveID, drive.ErrUnauthorized)

	if _, err := st.GetAccount(account.UserID); !os.IsNotExist(err) {
		t.Fatalf("expired account still exists: %v", err)
	}
	if files, err := st.LoadDirectoryCache(cacheKey); err != nil || files != nil {
		t.Fatalf("expired account directory cache remains: files=%#v err=%v", files, err)
	}
}

func TestDriveTransientFailureKeepsLocalAccount(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	account := &model.Account{
		UserID:  "dropbox_network",
		DriveID: "dropbox:network",
		Token:   &model.TokenInfo{TokenFrom: "dropbox"},
	}
	if err := st.SaveAccount(account); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	a := &App{store: st}

	a.handleDriveOperationError(account.UserID, account.DriveID, errors.New("network timeout"))

	if _, err := st.GetAccount(account.UserID); err != nil {
		t.Fatalf("transient failure removed account: %v", err)
	}
}

func TestSyncRunRegistryRejectsOverlapAndCancels(t *testing.T) {
	a := NewApp()
	ctx, finish, err := a.beginSyncRun(context.Background(), "sync-test", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.beginSyncRun(context.Background(), "sync-test", "scheduler"); err == nil {
		t.Fatal("overlapping sync run should be rejected")
	}
	ids := a.ListRunningSyncIDs()
	if len(ids) != 1 || ids[0] != "sync-test" {
		t.Fatalf("running sync IDs = %#v", ids)
	}
	if !a.CancelSync("sync-test") {
		t.Fatal("CancelSync should accept an active job")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("sync context was not canceled")
	}
	if !a.isSyncRunning("sync-test") {
		t.Fatal("registry should retain canceled job until worker exits")
	}
	finish(context.Canceled)
	if a.isSyncRunning("sync-test") || a.CancelSync("sync-test") {
		t.Fatal("finished sync job remained active")
	}
}

func TestCollectUploadValidationItemsIncludesNestedFiles(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "plain.txt")
	nestedDir := filepath.Join(root, "nested")
	nested := filepath.Join(nestedDir, "archive.zip")
	if err := os.WriteFile(plain, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nestedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := collectUploadValidationItems([]string{plain, nestedDir})
	if err != nil {
		t.Fatalf("collectUploadValidationItems: %v", err)
	}
	got := map[string]int64{}
	for _, item := range items {
		got[item.Name] = item.Size
	}
	if len(got) != 2 || got["plain.txt"] != 3 || got["archive.zip"] != 5 {
		t.Fatalf("validation items = %#v", items)
	}
	if _, err := collectUploadValidationItems(nil); err == nil {
		t.Fatal("empty upload selection should fail")
	}
}

func TestValidateExternalBrowserURL(t *testing.T) {
	for _, allowed := range []string{"https://example.com/help", "http://127.0.0.1:1234/callback", "http://localhost:1234/callback"} {
		if _, err := validateExternalBrowserURL(allowed); err != nil {
			t.Errorf("validateExternalBrowserURL(%q): %v", allowed, err)
		}
	}
	for _, rejected := range []string{"http://example.com/help", "file:///C:/Windows/win.ini", "custom:payload", "https://user:password@example.com/", "/relative"} {
		if _, err := validateExternalBrowserURL(rejected); err == nil {
			t.Errorf("validateExternalBrowserURL(%q) should reject", rejected)
		}
	}
}

func TestWriteCloudTextUploadTempIsolatesSameNamedEdits(t *testing.T) {
	first, err := writeCloudTextUploadTemp("notes.txt", "first")
	if err != nil {
		t.Fatal(err)
	}
	defer removeCloudTextUploadTemp(first)
	second, err := writeCloudTextUploadTemp("notes.txt", "second")
	if err != nil {
		t.Fatal(err)
	}
	defer removeCloudTextUploadTemp(second)

	if first == second || filepath.Dir(first) == filepath.Dir(second) {
		t.Fatalf("same-named edits share a temporary path: %q and %q", first, second)
	}
	for path, want := range map[string]string{first: "first", second: "second"} {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != want {
			t.Fatalf("temporary content at %q = %q, want %q", path, got, want)
		}
	}
}
