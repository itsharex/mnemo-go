package sync

import "testing"

func TestRemoteHashChangeConflictsWithLocalDeletion(t *testing.T) {
	base := Entry{RemoteName: "a", Paired: true, RemoteSize: 10, RemoteTime: 1, Hash: "sha1:old"}
	remote := Entry{RemoteName: "a", Size: 10, ModTime: 1, Hash: "sha1:new"}
	changes := planChanges(Config{DeletePropagation: true}, nil, []Entry{remote}, []Entry{base})
	if len(changes) != 1 || changes[0].Action != "conflict" {
		t.Fatalf("changed remote must not be deleted: %+v", changes)
	}
}

func TestPlanChangesTracksBothSidesWithoutOverwritingRemoteChanges(t *testing.T) {
	base := Entry{RemoteName: "a", Paired: true, LocalSize: 10, RemoteSize: 10, LocalTime: 1, RemoteTime: 2}
	local := Entry{RemoteName: "a", Size: 10, ModTime: 1}
	remote := Entry{RemoteName: "a", Size: 20, ModTime: 3}
	changes := planChanges(Config{Direction: "two-way"}, []Entry{local}, []Entry{remote}, []Entry{base})
	if len(changes) != 1 || changes[0].Action != "download" {
		t.Fatalf("remote change must download: %+v", changes)
	}
	local.Size = 30
	changes = planChanges(Config{Direction: "two-way"}, []Entry{local}, []Entry{remote}, []Entry{base})
	if changes[0].Action != "conflict" {
		t.Fatalf("both changed must conflict: %+v", changes)
	}
}

func TestPlanDeletionVsModificationIsConflict(t *testing.T) {
	cfg := Config{Direction: "two-way", DeletePropagation: true}
	base := Entry{RemoteName: "a", Paired: true, LocalSize: 10, LocalTime: 1}
	local := Entry{RemoteName: "a", Size: 10, ModTime: 1}
	changes := planChanges(cfg, []Entry{local}, nil, []Entry{base})
	if changes[0].Action != "delete-local" {
		t.Fatalf("expected propagated deletion: %+v", changes)
	}
	local.Size = 11
	changes = planChanges(cfg, []Entry{local}, nil, []Entry{base})
	if changes[0].Action != "conflict" {
		t.Fatalf("modified surviving file must not be deleted: %+v", changes)
	}
	cfg.DeletePropagation = false
	changes = planChanges(cfg, []Entry{local}, nil, []Entry{base})
	if changes[0].Action != "upload" {
		t.Fatalf("disabled deletion must restore missing side: %+v", changes)
	}
}

func TestInitialTwoWayDifferencesRequireConflictPolicy(t *testing.T) {
	changes := planChanges(Config{}, []Entry{{RemoteName: "a", Size: 1}}, []Entry{{RemoteName: "a", Size: 2}}, nil)
	if len(changes) != 1 || changes[0].Action != "conflict" {
		t.Fatalf("untracked difference must conflict: %+v", changes)
	}
}

func TestConfigScopeSeparatesAccountsAndDirectories(t *testing.T) {
	cfg := Config{UserID: "one", DriveID: "drive", LocalDir: "local", RemoteDir: "root", Direction: "two-way"}
	first := configScope(cfg)
	cfg.UserID = "two"
	if first == configScope(cfg) {
		t.Fatal("account change reused snapshot scope")
	}
	cfg.UserID = "one"
	cfg.RemoteDir = "folder"
	if first == configScope(cfg) {
		t.Fatal("directory change reused snapshot scope")
	}
}

func TestLocalSubsecondChangeIsDetected(t *testing.T) {
	entry := Entry{Size: 10, ModTime: 1, ModTimeNano: 1000000002}
	baseline := Entry{LocalSize: 10, LocalTime: 1, LocalTimeNano: 1000000001}
	if !localChanged(entry, baseline) {
		t.Fatal("same-second edit was ignored")
	}
}
