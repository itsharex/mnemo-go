package app

import (
	"os"
	"path/filepath"
	"testing"

	"mnemo-go/internal/model"
	"mnemo-go/internal/store"
)

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
