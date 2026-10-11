package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Durable workspaces: what a resumable rollout needs from the workspace
// manager. A rollout that can be resumed works in a workspace at a stable,
// recorded path (PrepareID), captures its work tree at savepoints into an
// object store of its own (Workspace.Capture), and, after the process that ran
// it died, gets the same path back with the tree of its last savepoint
// (RestoreWorkspace). What is not part of a capture: files git ignores (build
// outputs; setup outputs come back with the snapshot), the agent's own git
// history and stashes, the private HOME and TMPDIR, and running processes.

// maxWorkspaceID bounds a stable workspace id.
const maxWorkspaceID = 120

// resumableMarker names the file that marks a workspace as belonging to a
// rollout that may still be resumed; PruneStale leaves such a workspace alone
// until its rollout finished or the marker is older than ResumableTTL.
const resumableMarker = ".sleipnir-resumable.json"

// ResumableTTL is how long PruneStale keeps a workspace marked resumable whose
// rollout never finished.
const ResumableTTL = 7 * 24 * time.Hour

// WorkspaceInfo records where a workspace is and what it started from: enough
// to restore it at the same path and to refuse a restore onto a different start.
type WorkspaceInfo struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Snapshot string `json:"snapshot"`
	BaseTree string `json:"base_tree"`
	Commit   string `json:"commit,omitempty"`
}

// Info returns the workspace's record.
func (w *Workspace) Info() WorkspaceInfo {
	return WorkspaceInfo{ID: w.ID, Path: w.dir, Snapshot: w.SnapshotKey(), BaseTree: w.BaseTree, Commit: w.Commit}
}

// PrepareID is Prepare at a fixed id: the workspace is ws/<id> under the work
// root, replacing whatever a crashed process left there. id must be a plain
// name (letters, digits, ".", "_", "-").
func (m *Workspaces) PrepareID(ctx context.Context, task rl.Task, id string) (*Workspace, error) {
	if id == "" || len(id) > maxWorkspaceID || labelRe.MatchString(id) || id == "." || id == ".." {
		return nil, fmt.Errorf("workspace id %q is not a plain name of at most %d characters", id, maxWorkspaceID)
	}
	if err := ValidateTask(task); err != nil {
		return nil, Infra("prepare", err)
	}
	snap, err := m.snapshotFor(ctx, task)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.root, "ws", id)
	if _, err := os.Lstat(dir); err == nil {
		sweepMarker(id)
		if err := removeAllNoFollow(dir); err != nil {
			return nil, Infra("workspace", fmt.Errorf("replace %s: %w", dir, err))
		}
	}
	co, err := m.cloneSnapshot(ctx, snap, dir, task)
	if err != nil {
		return nil, err
	}
	return &Workspace{
		ID: id, Dir: co.dirIn, Root: co.tree, Home: co.home, Tmp: co.tmp,
		BaseTree: snap.meta.BaseTree, Commit: snap.meta.Commit, Warnings: m.Warnings(),
		mgr: m, task: task, snap: snap, dir: dir, marker: id,
	}, nil
}

// StableID is the stable workspace id of a rollout: the task, the sample and a
// tag of the run (for example a hash of its output directory), so two runs of
// the same task never share a workspace.
func StableID(task string, sample int, run string) string {
	id := labelRe.ReplaceAllString(task, "_")
	suffix := fmt.Sprintf("-s%d-%s", sample, labelRe.ReplaceAllString(run, "_"))
	if len(id)+len(suffix) > maxWorkspaceID {
		id = id[:maxWorkspaceID-len(suffix)]
	}
	return id + suffix
}

// Capture hashes the work tree into the object store at storeDir (a bare
// repository created on first use, whose alternates point at the snapshot's
// objects, so only content the rollout changed is stored) and returns the tree
// id. Files git ignores are not captured; Skipped lists files over the size
// limit.
func (w *Workspace) Capture(ctx context.Context, storeDir string) (tree string, skipped []SkippedPath, err error) {
	if w.snap == nil {
		return "", nil, errors.New("capture: the workspace has no snapshot (an attached tree cannot be restored)")
	}
	m := w.mgr
	if err := m.ensureStore(ctx, storeDir, w.snap); err != nil {
		return "", nil, Infra("capture", err)
	}
	hr, err := m.hashTree(ctx, hashOpts{
		GitDir: storeDir, AltObjects: w.snap.altObjects(), Work: w.Root, Base: w.BaseTree, Write: true,
		MaxFiles: m.opts.MaxFiles, MaxFileBytes: m.opts.MaxFileBytes,
	})
	if err != nil {
		if IsAgentLimit(err) || ctx.Err() != nil {
			return "", nil, err
		}
		return "", nil, Infra("capture", err)
	}
	return hr.Tree, hr.Skipped, nil
}

// ensureStore creates the capture store: a bare repository whose alternates
// point at the snapshot's objects.
func (m *Workspaces) ensureStore(ctx context.Context, storeDir string, snap *snapshot) error {
	if _, err := os.Stat(filepath.Join(storeDir, "objects")); err != nil {
		if err := os.MkdirAll(filepath.Dir(storeDir), 0o700); err != nil {
			return err
		}
		if err := m.git.initBare(ctx, storeDir); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(storeDir, "objects", "info", "alternates"), []byte(snap.altObjects()+"\n"), 0o644)
}

// maxRestoreBytes bounds the patch a restore applies.
const maxRestoreBytes int64 = 1 << 30

// RestoreWorkspace rebuilds the workspace of info at the same path with the
// work tree captured as tree in storeDir: a fresh clone of the task's snapshot
// with the difference between the start and tree applied. It refuses when the
// task's snapshot no longer starts from info's base tree.
func (m *Workspaces) RestoreWorkspace(ctx context.Context, task rl.Task, info WorkspaceInfo, storeDir, tree string) (*Workspace, error) {
	w, err := m.PrepareID(ctx, task, info.ID)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Workspace, error) {
		_ = w.Cleanup()
		return nil, err
	}
	if info.BaseTree != "" && w.BaseTree != info.BaseTree {
		return fail(fmt.Errorf("restore %s: the task now starts from tree %s, the capture from %s", info.ID, w.BaseTree, info.BaseTree))
	}
	if err := m.ensureStore(ctx, storeDir, w.snap); err != nil {
		return fail(Infra("restore", err))
	}
	patch, err := m.diffTrees(ctx, storeDir, w.BaseTree, tree, maxRestoreBytes)
	if err != nil {
		return fail(Infra("restore", fmt.Errorf("diff %s..%s: %w", w.BaseTree, tree, err)))
	}
	if err := m.applyPatch(ctx, w.Root, patch); err != nil {
		return fail(Infra("restore", fmt.Errorf("apply the captured tree: %w", err)))
	}
	return w, nil
}

// MarkResumable records next to the workspace that it belongs to the rollout in
// sampleDir, which may still be resumed: PruneStale keeps it until that rollout
// has its episode.json or the mark is older than ResumableTTL.
func (w *Workspace) MarkResumable(sampleDir string) error {
	if w.dir == "" {
		return nil
	}
	b, err := json.Marshal(map[string]string{"sample": sampleDir, "at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, resumableMarker), b, 0o600)
}

// keepResumable reports whether PruneStale must keep the workspace directory
// dir: it is marked resumable, its rollout has not finished, and the mark is
// recent.
func keepResumable(dir string, now time.Time) bool {
	b, err := os.ReadFile(filepath.Join(dir, resumableMarker))
	if err != nil {
		return false
	}
	var mk struct{ Sample, At string }
	if json.Unmarshal(b, &mk) != nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, mk.At)
	if err != nil || now.Sub(at) > ResumableTTL {
		return false
	}
	if mk.Sample != "" {
		if _, err := os.Stat(filepath.Join(mk.Sample, "episode.json")); err == nil {
			return false
		}
	}
	return true
}
