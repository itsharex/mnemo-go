package registrycheck

import (
	"testing"

	"mnemo-go/internal/drive"
	_ "mnemo-go/internal/drive/providers"
	"mnemo-go/internal/model"
)

func TestAllProvidersRegistered(t *testing.T) {
	want := []string{"aliopen", "dropbox", "guangya", "ilanzou", "lanzou", "onedrive", "pan123", "pan139", "pan189", "pikpak", "s3", "webdav", "yike"}
	if drive.Count() != len(want) {
		t.Fatalf("registered %d providers, want %d", drive.Count(), len(want))
	}
	seen := map[string]bool{}
	for _, p := range drive.All() {
		seen[p.ID] = true
		if p.Caps.Provider != p.ID {
			t.Errorf("caps provider mismatch for %s", p.ID)
		}
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("provider %s not registered", w)
		}
	}
	// gofile/gdrive must NOT be present
	if seen["gofile"] || seen["gdrive"] {
		t.Error("gofile/gdrive must be removed")
	}
	_ = model.ProviderUnknown
}

func TestFavoriteCapabilitiesMatchCompleteNativeAdapters(t *testing.T) {
	native := map[string]bool{"pikpak": true, "onedrive": true, "pan123": true, "aliopen": true}
	for _, p := range drive.All() {
		_, implemented := p.Factory().(drive.RemoteFavorites)
		if implemented != native[p.ID] || p.Caps.Favorite != native[p.ID] {
			t.Errorf("%s favorite capability=%v adapter=%v", p.ID, p.Caps.Favorite, implemented)
		}
	}
}
