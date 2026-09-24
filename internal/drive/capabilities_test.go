package drive_test

import (
	"testing"

	"mnemo-go/internal/drive"
	_ "mnemo-go/internal/drive/providers"
	"mnemo-go/internal/model"
)

func TestProviderDeleteCapabilitiesAreExplicit(t *testing.T) {
	expected := map[string]struct {
		recycle   bool
		permanent bool
	}{
		model.ProviderPikpak:   {recycle: true, permanent: true},
		model.ProviderOnedrive: {recycle: true, permanent: false},
		model.ProviderDropbox:  {recycle: true, permanent: false},
		model.ProviderPan123:   {recycle: true, permanent: true},
		model.ProviderLanzou:   {recycle: true, permanent: false},
		model.ProviderIlanzou:  {recycle: true, permanent: true},
		model.ProviderPan139:   {recycle: true, permanent: true},
		model.ProviderPan189:   {recycle: true, permanent: true},
		model.ProviderYike:     {recycle: true, permanent: false},
		model.ProviderAliopen:  {recycle: true, permanent: true},
		model.ProviderGuangya:  {recycle: true, permanent: false},
		model.ProviderWebdav:   {recycle: false, permanent: true},
		model.ProviderS3:       {recycle: false, permanent: true},
	}
	if got := drive.Count(); got != len(expected) {
		t.Fatalf("registered provider count = %d, want %d", got, len(expected))
	}
	for provider, want := range expected {
		caps := drive.RegistryCaps(provider)
		if caps.RecycleBin != want.recycle || caps.PermanentDelete != want.permanent {
			t.Errorf("%s delete caps = recycle:%v permanent:%v, want recycle:%v permanent:%v", provider, caps.RecycleBin, caps.PermanentDelete, want.recycle, want.permanent)
		}
	}
}

func TestDropboxOnlyExposesRecoverableFileTrash(t *testing.T) {
	caps := drive.RegistryCaps(model.ProviderDropbox)
	if !caps.TrashView || !caps.TrashRestore || caps.TrashPurge || caps.TrashClear {
		t.Fatalf("Dropbox trash capabilities = %+v", caps)
	}
}

func TestGuangyaExposesRestoreButNotPermanentDelete(t *testing.T) {
	caps := drive.RegistryCaps(model.ProviderGuangya)
	if !caps.RecycleBin || !caps.TrashView || !caps.TrashRestore || caps.PermanentDelete {
		t.Fatalf("光鸭回收站能力声明不正确: %+v", caps)
	}
}

func TestRenameBatchRejectsMismatchedInput(t *testing.T) {
	_, err := drive.RenameBatch("", "", []drive.FileRef{{ID: "file-1"}}, nil)
	if err == nil {
		t.Fatal("RenameBatch should reject mismatched refs and names")
	}
}

func TestShareExpirationOptionsAreDeclaredExplicitly(t *testing.T) {
	caps := drive.NewCapabilities("test", nil, func(c *drive.Capabilities) {
		c.SetShareExpirationOptions(0, 1, 7)
	})
	if got := caps.ShareExpirationOptions; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 7 {
		t.Fatalf("share expiration options = %v, want [0 1 7]", got)
	}
}

func TestShareCreationDoesNotRequireRemoteCancellation(t *testing.T) {
	caps := drive.NewCapabilities("test", map[string]bool{
		"createShare":     true,
		"shareExpiration": true,
		"sharePassword":   true,
		"combinedShare":   true,
		"shareHistory":    true,
	}, func(c *drive.Capabilities) {
		c.SetShareExpirationOptions(0, 1, 7)
	})
	if !caps.CreateShare || !caps.ShareExpiration || !caps.SharePassword || !caps.CombinedShare || !caps.ShareHistory {
		t.Fatalf("share creation capabilities must be retained: %+v", caps)
	}
	if got := caps.ShareExpirationOptions; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 7 {
		t.Fatalf("share expiration options = %v, want [0 1 7]", got)
	}

	for _, registration := range drive.All() {
		caps := registration.Caps
		if !caps.CancelCreatedShares {
			continue
		}
		if !caps.ManageCreatedShares {
			t.Errorf("%s advertises cancellation without created-share management: %+v", registration.ID, caps)
		}
		if _, ok := drive.New(registration.ID).(drive.ShareCancellationDriver); !ok {
			t.Errorf("%s advertises cancellation without a cancellation driver", registration.ID)
		}
	}
}
