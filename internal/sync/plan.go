package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/driveutil"
	"mnemo-go/internal/model"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Local  *Entry `json:"local,omitempty"`
	Remote *Entry `json:"remote,omitempty"`
}
type Plan struct {
	SnapshotTotal int      `json:"snapshotTotal"`
	Token         string   `json:"token"`
	Changes       []Change `json:"changes"`
	Blocked       string   `json:"blocked,omitempty"`
}

func configScope(cfg Config) string {
	local, _ := filepath.Abs(cfg.LocalDir)
	data, _ := json.Marshal([]string{cfg.UserID, cfg.DriveID, filepath.Clean(local), cfg.RemoteDir, cfg.Direction})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func localChanged(entry, baseline Entry) bool {
	return entry.Size != baseline.LocalSize || entry.ModTime != baseline.LocalTime || baseline.LocalTimeNano != 0 && entry.ModTimeNano != baseline.LocalTimeNano
}

func planChanges(cfg Config, local, remote, snapshot []Entry) []Change {
	lm, rm, sm := map[string]Entry{}, map[string]Entry{}, map[string]Entry{}
	names := map[string]bool{}
	for _, e := range local {
		lm[e.RemoteName] = e
		names[e.RemoteName] = true
	}
	for _, e := range remote {
		rm[e.RemoteName] = e
		names[e.RemoteName] = true
	}
	for _, e := range snapshot {
		sm[e.RemoteName] = e
	}
	keys := []string{}
	for name := range names {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	out := []Change{}
	for _, name := range keys {
		l, lok := lm[name]
		r, rok := rm[name]
		s, tracked := sm[name]
		c := Change{Path: name}
		if lok {
			c.Local = &l
		}
		if rok {
			c.Remote = &r
		}
		switch cfg.Direction {
		case "push":
			if lok && (!rok || l.Size != r.Size || l.ModTime > r.ModTime) {
				c.Action = "upload"
			}
			if !lok && rok && tracked && cfg.DeletePropagation {
				c.Action = "delete-remote"
			}
		case "pull":
			if rok && (!lok || l.Size != r.Size || r.ModTime > l.ModTime) {
				c.Action = "download"
			}
			if lok && !rok && tracked && cfg.DeletePropagation {
				c.Action = "delete-local"
			}
		default:
			if lok && !rok {
				c.Action = "upload"
				if tracked && s.Paired && cfg.DeletePropagation {
					if !localChanged(l, s) {
						c.Action = "delete-local"
					} else {
						c.Action = "conflict"
					}
				}
			} else if !lok && rok {
				c.Action = "download"
				if tracked && s.Paired && cfg.DeletePropagation {
					if r.Size == s.RemoteSize && r.ModTime == s.RemoteTime && r.Hash == s.Hash {
						c.Action = "delete-remote"
					} else {
						c.Action = "conflict"
					}
				}
			} else if lok && rok {
				if tracked && s.Paired {
					lc := localChanged(l, s)
					rc := r.Size != s.RemoteSize || r.ModTime != s.RemoteTime || r.Hash != s.Hash
					if lc && rc {
						c.Action = "conflict"
					} else if lc {
						c.Action = "upload"
					} else if rc {
						c.Action = "download"
					}
				} else {
					// Without a paired baseline, matching size/timestamps do not
					// prove matching contents. Require an explicit conflict policy.
					c.Action = "conflict"
				}
			}
		}
		if c.Action != "" {
			out = append(out, c)
		}
	}
	return out
}

func (e *Engine) Preview(ctx context.Context, cfg Config) (*Plan, error) {
	if cfg.Direction != "" && cfg.Direction != "two-way" && cfg.Direction != "push" && cfg.Direction != "pull" {
		return nil, fmt.Errorf("sync: invalid direction %q", cfg.Direction)
	}
	local, err := scanLocalFiles(cfg.LocalDir)
	if err != nil {
		return nil, err
	}
	remote, err := remoteTree(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var snapshot []Entry
	if e.snapshots != nil {
		snapshot, err = e.snapshots.LoadSyncSnapshot(cfg.ID)
		if err != nil {
			return nil, err
		}
	}
	scope := configScope(cfg)
	validSnapshot := []Entry{}
	for _, entry := range snapshot {
		if entry.Scope == scope {
			validSnapshot = append(validSnapshot, entry)
		}
	}
	snapshot = validSnapshot
	sort.Slice(local, func(i, j int) bool { return local[i].RemoteName < local[j].RemoteName })
	sort.Slice(remote, func(i, j int) bool { return remote[i].RemoteName < remote[j].RemoteName })
	data, _ := json.Marshal(struct {
		Config                  Config
		Local, Remote, Snapshot []Entry
	}{cfg, local, remote, snapshot})
	sum := sha256.Sum256(data)
	plan := &Plan{Token: hex.EncodeToString(sum[:]), Changes: planChanges(cfg, local, remote, snapshot), SnapshotTotal: len(snapshot)}
	caps := drive.RegistryCaps(drive.ProviderOf(cfg.UserID, cfg.DriveID, ""))
	deletes := 0
	for _, c := range plan.Changes {
		if _, err := safeLocalPath(cfg.LocalDir, c.Path); err != nil {
			plan.Blocked = err.Error()
		}
		if c.Action == "delete-local" || c.Action == "delete-remote" {
			deletes++
		}
		if (c.Action == "upload" || c.Action == "conflict") && !caps.Upload {
			plan.Blocked = "该网盘不支持所需的上传操作"
		}
		if (c.Action == "download" || c.Action == "conflict") && !caps.Download {
			plan.Blocked = "该网盘不支持所需的下载操作"
		}
		if c.Action == "delete-remote" && !caps.RecycleBin && !caps.PermanentDelete {
			plan.Blocked = "该网盘不支持同步删除"
		}
		if (c.Action == "upload" || c.Action == "conflict") && c.Local != nil && c.Remote != nil {
			overwrite := false
			for _, policy := range caps.UploadConflictPolicies {
				if policy == driveutil.ConflictPolicyOverwrite {
					overwrite = true
				}
			}
			if !overwrite {
				plan.Blocked = "该网盘不支持覆盖同名文件，请使用独立目标目录"
			}
		}
	}
	if deletes > 0 && !e.guardDelete(cfg.ID, deletes, len(snapshot)) {
		plan.Blocked = "删除数量超过快照的 50%，请检查两端目录"
	}
	return plan, nil
}

// ExecutePlan re-reads both sides and refuses an obsolete preview before any writes.
func (e *Engine) ExecutePlan(ctx context.Context, cfg Config, token string, choices map[string]string) error {
	plan, err := e.Preview(ctx, cfg)
	if err != nil {
		return err
	}
	if token != "" && token != plan.Token {
		return fmt.Errorf("文件或配置已变化，请重新预览")
	}
	if plan.Blocked != "" {
		return fmt.Errorf("%s", plan.Blocked)
	}
	if len(plan.Changes) == 0 {
		return nil
	}
	expectedLocal := map[string]Entry{}
	previous := map[string]Entry{}
	if e.snapshots != nil {
		entries, err := e.snapshots.LoadSyncSnapshot(cfg.ID)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Scope == configScope(cfg) {
				previous[entry.RemoteName] = entry
			}
		}
	}
	deleteCount := 0
	for _, c := range plan.Changes {
		if c.Action == "delete-local" || c.Action == "delete-remote" {
			deleteCount++
		}
		if c.Action == "conflict" {
			policy := choices[c.Path]
			if policy == "" {
				policy = cfg.ConflictPolicy
			}
			if policy == "" {
				policy = "keep-both"
			}
			if policy != "local" && policy != "remote" && policy != "keep-both" {
				return fmt.Errorf("请选择冲突处理方式: %s", c.Path)
			}
			if policy == "local" && c.Local == nil || policy == "remote" && c.Remote == nil {
				deleteCount++
			}
		}
	}
	if deleteCount > 0 && !e.guardDelete(cfg.ID, deleteCount, plan.SnapshotTotal) {
		return fmt.Errorf("删除数量超过快照的一半，已停止执行")
	}
	for i, c := range plan.Changes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkPlannedLocal(cfg, c.Path, c.Local); err != nil {
			return fmt.Errorf("%s: %w", c.Path, err)
		}
		if err := checkPlannedRemote(ctx, cfg, c.Path, c.Remote); err != nil {
			return err
		}
		action := c.Action
		if action == "conflict" {
			policy := choices[c.Path]
			if policy == "" {
				policy = cfg.ConflictPolicy
			}
			if policy == "" {
				policy = "keep-both"
			}
			switch policy {
			case "local":
				if c.Local != nil {
					action = "upload"
				} else {
					action = "delete-remote"
				}
			case "remote":
				if c.Remote != nil {
					action = "download"
				} else {
					action = "delete-local"
				}
			case "keep-both":
				if c.Local == nil {
					action = "download"
				} else if c.Remote == nil {
					action = "upload"
				} else {
					name := c.Path + ".remote-" + fmt.Sprint(time.Now().UnixNano())
					if err := e.downloadEntry(ctx, cfg, *c.Remote, name, nil); err != nil {
						return err
					}
					localPath, err := safeLocalPath(cfg.LocalDir, name)
					if err != nil {
						return err
					}
					entry := *c.Remote
					entry.RemoteName = name
					entry.LocalPath = localPath
					if info, err := os.Stat(localPath); err == nil {
						entry.ModTime = info.ModTime().Unix()
						entry.ModTimeNano = info.ModTime().UnixNano()
					} else {
						return err
					}
					if err := e.uploadPlanned(ctx, cfg, entry, nil); err != nil {
						return err
					}
					expectedLocal[name] = entry
					action = "upload"
				}
			}
		}
		switch action {
		case "upload":
			err = e.uploadPlanned(ctx, cfg, *c.Local, c.Remote)
		case "download":
			err = e.downloadEntry(ctx, cfg, *c.Remote, c.Path, c.Local)
		case "delete-local":
			err = e.propagateLocalDeletes(cfg, []Entry{*c.Local})
		case "delete-remote":
			err = e.propagateRemoteDeletes(ctx, cfg, []Entry{*c.Remote})
		}
		if err != nil {
			return fmt.Errorf("%s: %w", c.Path, err)
		}
		if action == "upload" {
			expectedLocal[c.Path] = *c.Local
		}
		if action == "download" {
			path, err := safeLocalPath(cfg.LocalDir, c.Path)
			if err != nil {
				return err
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			entry := *c.Remote
			entry.ModTime = info.ModTime().Unix()
			entry.ModTimeNano = info.ModTime().UnixNano()
			expectedLocal[c.Path] = entry
		}
		if e.onProgress != nil {
			e.onProgress(cfg.ID, i+1, len(plan.Changes))
		}
	}
	if e.snapshots != nil {
		remote, err := remoteTree(ctx, cfg)
		if err != nil {
			return err
		}
		for i := range remote {
			remote[i].Scope = configScope(cfg)
			if l, ok := expectedLocal[remote[i].RemoteName]; ok && l.Size == remote[i].Size {
				remote[i].Paired = true
				remote[i].LocalSize = l.Size
				remote[i].LocalTime = l.ModTime
				remote[i].LocalTimeNano = l.ModTimeNano
				remote[i].RemoteSize = remote[i].Size
				remote[i].RemoteTime = remote[i].ModTime
			} else if old, ok := previous[remote[i].RemoteName]; ok && old.Paired && old.RemoteSize == remote[i].Size && old.RemoteTime == remote[i].ModTime && old.Hash == remote[i].Hash {
				remote[i] = old
			}
		}
		return e.snapshots.SaveSyncSnapshot(cfg.ID, remote)
	}
	return nil
}

func (e *Engine) uploadPlanned(ctx context.Context, cfg Config, entry Entry, expectedRemote *Entry) error {
	if err := drive.ValidateUploadItems(cfg.UserID, cfg.DriveID, []drive.UploadValidationItem{{Name: filepath.Base(entry.RemoteName), Size: entry.Size}}); err != nil {
		return err
	}
	info, err := os.Stat(entry.LocalPath)
	if err != nil {
		return err
	}
	if info.Size() != entry.Size || entry.ModTimeNano != 0 && info.ModTime().UnixNano() != entry.ModTimeNano {
		return fmt.Errorf("本地文件已变化，请重新预览")
	}
	parent, err := ensureRemoteDir(ctx, cfg, filepath.Dir(entry.RemoteName))
	if err != nil {
		return err
	}
	handler, err := drive.QueueUploadHandlerContext(ctx, cfg.UserID, cfg.DriveID)
	if err != nil {
		return err
	}
	if err := checkPlannedRemote(ctx, cfg, entry.RemoteName, expectedRemote); err != nil {
		return err
	}
	if err := handler(ctx, &model.UploadingUI{UploadID: entry.LocalPath, Info: model.UploadInfo{LocalFilePath: entry.LocalPath, ParentFileID: parent, DriveID: cfg.DriveID, Name: filepath.Base(entry.RemoteName), Size: entry.Size, ConflictPolicy: driveutil.ConflictPolicyOverwrite}}); err != nil {
		return err
	}
	after, err := os.Stat(entry.LocalPath)
	if err != nil {
		return err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("本地文件在上传期间变化，请重试")
	}
	return nil
}

// checkPlannedRemote reads fresh parent listings, including planned absence.
// Never use the UI metadata cache when deciding whether a write is still safe.
// Providers without conditional writes still have a request-sized race window;
// this check prevents earlier transfers from making the entire plan stale.
func checkPlannedRemote(ctx context.Context, cfg Config, name string, expected *Entry) error {
	parent := cfg.RemoteDir
	parts := strings.Split(name, "/")
	changed := func() error { return fmt.Errorf("远端文件已变化，请重新预览: %s", name) }
	for i, part := range parts {
		files, err := drive.ListDirAllContext(ctx, cfg.UserID, cfg.DriveID, parent, nil)
		if err != nil {
			return fmt.Errorf("复核远端文件 %s: %w", name, err)
		}
		var found *model.File
		for j := range files {
			if files[j].Name == part {
				if found != nil {
					return changed()
				}
				found = &files[j]
			}
		}
		if found == nil {
			if expected == nil {
				return nil
			}
			return changed()
		}
		if i < len(parts)-1 {
			if !found.IsDir || found.FileID == "" {
				return changed()
			}
			parent = found.FileID
			continue
		}
		if expected == nil || found.IsDir || found.FileID != expected.RemoteID || found.Size != expected.Size || found.Time != expected.ModTime || found.ContentHashName+":"+found.ContentHash != expected.Hash {
			return changed()
		}
	}
	return nil
}

// checkPlannedLocal also checks planned absence: a file created during a
// transfer must not be overwritten by a previously approved download.
func checkPlannedLocal(cfg Config, name string, expected *Entry) error {
	path, err := safeLocalPath(cfg.LocalDir, name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && expected == nil {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if expected == nil || err != nil || !info.Mode().IsRegular() || info.Size() != expected.Size || info.ModTime().Unix() != expected.ModTime || expected.ModTimeNano != 0 && info.ModTime().UnixNano() != expected.ModTimeNano {
		return fmt.Errorf("本地文件已变化，请重新预览")
	}
	return nil
}

func (e *Engine) downloadEntry(ctx context.Context, cfg Config, entry Entry, name string, expected *Entry) error {
	if err := checkPlannedLocal(cfg, name, expected); err != nil {
		return err
	}
	target, err := safeLocalPath(cfg.LocalDir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".mnemo-sync-*")
	if err != nil {
		return err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	url, err := drive.GetDownloadURLContext(ctx, cfg.UserID, cfg.DriveID, entry.RemoteID, 14400)
	if err != nil {
		return err
	}
	if err := downloadTo(ctx, url, path); err != nil {
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if entry.Size >= 0 && stat.Size() != entry.Size {
		return fmt.Errorf("download size mismatch")
	}
	if entry.ModTime > 0 {
		t := time.Unix(entry.ModTime, 0)
		if err := os.Chtimes(path, t, t); err != nil {
			return err
		}
	}
	if strings.HasPrefix(filepath.Base(target), ".mnemo-sync-") {
		return fmt.Errorf("sync reserved temporary name")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkPlannedLocal(cfg, name, expected); err != nil {
		return err
	}
	if err := checkPlannedRemote(ctx, cfg, entry.RemoteName, &entry); err != nil {
		return err
	}
	return os.Rename(path, target)
}
