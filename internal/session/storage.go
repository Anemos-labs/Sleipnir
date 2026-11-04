package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// Storage: what the harness keeps in its data directory, and the cleanup of it (docs/STORAGE.md).
//
// Everything the harness writes for itself is under one directory, <state> ($SLEIPNIR_HOME, else ~/.sleipnir): the settings and
// credentials, the recorded sessions (each with its scratch directory, tmp/), the worktrees of isolated teams, the memories, the
// launchers of plugins and the RL work directories. The worktrees and the scratch directories grow without end when nothing removes
// them, and a session can do without them. The cleanup reclaims them, by itself at most once a day (AutoClean, run by `sleipnir web`)
// and when asked (`sleipnir storage clean`):
//
//   - the worktrees of a session that no process holds and that was not written for a while (storage.worktree_idle) are removed
//     when they hold nothing of their own: a clean tree whose branch has no commit that exists nowhere else
//     (workspace.PruneOptions.KeepUnmerged). A resumed session makes them again from the integration tip. A tree with work is kept;
//   - the scratch directory of a session that no process holds and that was not written for a day is emptied: it is $TMPDIR of the
//     session's commands, and nothing reads it once they are gone;
//   - a folder of worktrees whose session is gone (deleted by hand) and that was not written for orphanTreesAge is released as a
//     deleted session's are: salvaged onto their branches, then removed, unless a tree's owner may still be running.
//
// The automatic cleanup never deletes a session: a session and its history go only when a person deletes them (the page, `sessions
// prune`, or `storage clean --sessions`), through RemoveStoredSession. Nothing outside the data directory is touched but the worktrees
// that sessions of earlier versions kept in the user's cache directory (legacyWorktreesDir), and nothing of a project: a project's own
// .sleipnir/ folder is its own.

// StorageArea is one part of the data directory, with the room it takes.
type StorageArea struct {
	// Key names the area for a program (sessions, scratch, worktrees, ...); Name and What are for a person.
	Key, Name, What string
	// Path is where it is; Bytes the size of its files (links are not followed).
	Path  string
	Bytes int64
	// Cleaned says whether the storage cleanup reclaims room in it by itself (the sessions are deleted only when a person asks).
	Cleaned bool
}

// StorageUsage lists the areas of the data directory of home (StateRoot) and the room each takes, the largest first, and the total.
// An area that does not exist is left out. It reads, never writes.
func StorageUsage(home string) (areas []StorageArea, total int64) {
	state := StateRoot(home)
	sessions := filepath.Join(state, "sessions")
	var scratch int64
	if ents, err := os.ReadDir(sessions); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				scratch += DirSize(filepath.Join(sessions, e.Name(), "tmp"))
			}
		}
	}
	add := func(a StorageArea) {
		if _, err := os.Lstat(a.Path); err == nil {
			areas = append(areas, a)
		}
	}
	all := DirSize(state)
	add(StorageArea{Key: "sessions", Name: "sessions", What: "recorded sessions: event logs, blobs, checkpoints", Path: sessions,
		Bytes: DirSize(sessions) - scratch}) // deleted only when a person asks
	if scratch > 0 {
		areas = append(areas, StorageArea{Key: "scratch", Name: "scratch", What: "the sessions' $TMPDIR", Path: filepath.Join(sessions, "*", "tmp"),
			Bytes: scratch, Cleaned: true})
	}
	trees := WorktreesDir(home)
	add(StorageArea{Key: "worktrees", Name: "worktrees", What: "isolated teams' worktrees", Path: trees, Bytes: DirSize(trees), Cleaned: true})
	rl := filepath.Join(state, "rl-work")
	add(StorageArea{Key: "rl-work", Name: "rl work", What: "RL episode workspaces (sleipnir rl)", Path: rl, Bytes: DirSize(rl)})
	known := int64(0)
	for _, a := range areas {
		known += a.Bytes
	}
	if rest := all - known; rest > 0 {
		areas = append(areas, StorageArea{Key: "other", Name: "other", What: "settings, credentials, memories, plugins and launchers", Path: state, Bytes: rest})
	}
	total = all
	if legacy := legacyWorktreesDir(home); legacy != "" {
		if _, err := os.Lstat(legacy); err == nil {
			n := DirSize(legacy)
			areas = append(areas, StorageArea{Key: "legacy-worktrees", Name: "old worktrees", What: "worktrees of sessions of earlier versions, outside the data directory",
				Path: legacy, Bytes: n, Cleaned: true})
			total += n
		}
	}
	sort.SliceStable(areas, func(i, j int) bool { return areas[i].Bytes > areas[j].Bytes })
	return areas, total
}

// CleanOptions is what a storage cleanup removes.
type CleanOptions struct {
	// Sessions deletes the recorded sessions whose newest file is older than OlderThan, past the newest Keep. Only a person asks
	// for it (storage clean --sessions); the automatic cleanup never does.
	Sessions  bool
	OlderThan time.Duration
	Keep      int
	// TreesIdle is how long a stopped session must have gone unwritten for the trees of its team that hold nothing of their own to
	// be removed; 0 leaves them.
	TreesIdle time.Duration
	// Live are the sessions this process hosts: never touched, and counted among the newest.
	Live []string
	// DryRun plans and measures, and changes nothing.
	DryRun bool
	// Now is the time ages are measured from (time.Now when zero).
	Now time.Time
}

// orphanTreesAge is how long a folder of worktrees whose session is not recorded must have gone unwritten before the cleanup
// salvages and removes it: long enough that a session recorded elsewhere (run --session-dir) is done with it.
const orphanTreesAge = 30 * 24 * time.Hour

// ScratchAge is how long a session must have gone unwritten for the cleanup to empty its scratch directory.
const ScratchAge = 24 * time.Hour

// CleanReport is what a storage cleanup did, or in a dry run would do.
type CleanReport struct {
	// Sessions is the plan of the sessions to delete (PlanPrune); Removed what became of each (empty in a dry run).
	Sessions *PrunePlan
	Removed  []Removal
	// Scratch counts the scratch directories emptied and their size.
	Scratch      int
	ScratchBytes int64
	// Idle counts the stopped sessions whose trees were reclaimed (fully or in part); Trees are the folders of worktrees released
	// whose session was gone; TreesBytes the room both gave back.
	Idle       int
	Trees      []string
	TreesBytes int64
	// Kept says what was left and why: a branch that holds unmerged work, a tree whose owner may be running, a folder whose
	// repository is gone.
	Kept []string
	// Errors are the parts that failed; the rest of the cleanup went on.
	Errors []string
	// DryRun says the report is of a plan: nothing was removed.
	DryRun bool
}

// Freed is the room the cleanup gave back (or would give back): the sessions deleted, the scratch directories and the released trees.
func (r *CleanReport) Freed() int64 {
	if r == nil {
		return 0
	}
	n := r.ScratchBytes + r.TreesBytes
	if r.DryRun {
		if r.Sessions != nil {
			n += r.Sessions.Bytes
		}
		return n
	}
	for _, rm := range r.Removed {
		if rm.Err == nil {
			n += rm.Bytes
		}
	}
	return n
}

// CleanStorage reclaims room in the data directory of home (see Storage above): the scratch directories of sessions nobody holds that
// were not written for ScratchAge, the trees of stopped sessions idle for o.TreesIdle that hold nothing of their own, the folders of
// worktrees whose session is gone, and, with o.Sessions only, the sessions PlanPrune selects with o.OlderThan and o.Keep, deleted
// through RemoveStoredSession. It never touches a pinned or live session, never removes a tree whose owner may be running, and never
// destroys work: a tree with work is kept, or for a session deleted or gone its uncommitted edits are committed onto the tree's
// branch, and a branch with commits no other branch has is kept (CleanReport.Kept names it). A part that fails is reported and the
// rest goes on.
func CleanStorage(ctx context.Context, home string, o CleanOptions) (*CleanReport, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	rep := &CleanReport{DryRun: o.DryRun, Sessions: &PrunePlan{}}
	if o.Sessions {
		plan, err := PlanPrune(home, now, o.OlderThan, o.Keep, o.Live)
		if err != nil {
			return nil, err
		}
		rep.Sessions = plan
	}
	plan := rep.Sessions
	if !o.DryRun && len(plan.Delete) > 0 {
		ids := make([]string, 0, len(plan.Delete))
		for _, r := range plan.Delete {
			ids = append(ids, r.ID)
		}
		// One at a time, each to its end: a removal stopped in the middle of salvaging a tree is retried by the next cleanup,
		// but there is no reason to stop one. ctx stops the cleanup between two.
		for _, id := range ids {
			if ctx.Err() != nil {
				break
			}
			rep.Removed = append(rep.Removed, RemoveRecorded(context.WithoutCancel(ctx), home, []string{id})...)
		}
		for _, rm := range rep.Removed {
			seen := map[string]bool{}
			for _, k := range rm.Kept {
				if !seen[k.Branch] {
					seen[k.Branch] = true
					rep.Kept = append(rep.Kept, "branch "+k.Branch+" holds work no other branch has (session "+rm.ID+")")
				}
			}
			if rm.Err != nil {
				rep.Errors = append(rep.Errors, "session "+rm.ID+": "+firstLine(rm.Err.Error(), 300))
			}
		}
	}
	deleted := map[string]bool{}
	for _, r := range plan.Delete {
		deleted[r.ID] = true
	}
	cleanScratch(home, now, o, deleted, rep)
	if o.TreesIdle > 0 {
		reclaimIdleTrees(ctx, home, now, o, deleted, rep)
	}
	tops := []string{WorktreesDir(home)}
	if os.Getenv("SLEIPNIR_HOME") == "" {
		// The old cache location is shared by every data directory of the user: only the default one may take a folder there for
		// a session that is gone, since a session of another may be recorded there.
		tops = append(tops, legacyWorktreesDir(home))
	}
	for _, top := range tops {
		if top != "" && ctx.Err() == nil {
			cleanOrphanTrees(ctx, home, top, now, o, rep)
		}
	}
	return rep, ctx.Err()
}

// cleanScratch empties the scratch directory (tmp/) of every session that is not live, not being deleted, not held by a process and
// not written for ScratchAge. The session's lock is held while it is removed, so a session that starts again meanwhile waits for it.
func cleanScratch(home string, now time.Time, o CleanOptions, deleted map[string]bool, rep *CleanReport) {
	root := SessionsDir(home)
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, id := range o.Live {
		live[id] = true
	}
	for _, e := range ents {
		id := e.Name()
		if live[id] || deleted[id] { // a session being deleted takes its scratch directory with it
			continue
		}
		dir := filepath.Join(root, id)
		tmp := filepath.Join(dir, "tmp")
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || !hasLog(dir) {
			continue
		}
		fi, err := os.Lstat(tmp)
		if err != nil || !fi.IsDir() { // none, or a link: never followed
			continue
		}
		if now.Sub(lastWritten(dir)) < ScratchAge {
			continue
		}
		n := DirSize(tmp)
		if n == 0 {
			continue
		}
		if o.DryRun {
			rep.Scratch++
			rep.ScratchBytes += n
			continue
		}
		unlock, err := lockDir(dir)
		if err != nil {
			continue // a process holds it: its commands may be using their $TMPDIR
		}
		err = os.RemoveAll(tmp)
		unlock()
		if err != nil {
			rep.Errors = append(rep.Errors, "scratch of "+id+": "+firstLine(err.Error(), 300))
			continue
		}
		rep.Scratch++
		rep.ScratchBytes += n
	}
}

// reclaimIdleTrees removes the worktrees of every recorded isolated session that is not live, not being deleted, not held by a
// process and not written for o.TreesIdle, when they hold nothing of their own: a tree with uncommitted changes, commits on no branch
// or a branch with commits that exist nowhere else is kept, and so are the session's recovery references, so the session can still be
// resumed and makes the trees it needs again. The session's lock is held meanwhile.
func reclaimIdleTrees(ctx context.Context, home string, now time.Time, o CleanOptions, deleted map[string]bool, rep *CleanReport) {
	root := SessionsDir(home)
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	live := map[string]bool{}
	for _, id := range o.Live {
		live[id] = true
	}
	for _, e := range ents {
		if ctx.Err() != nil {
			return
		}
		id := e.Name()
		dir := filepath.Join(root, id)
		if live[id] || deleted[id] {
			continue
		}
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || !hasLog(dir) || now.Sub(lastWritten(dir)) < o.TreesIdle {
			continue
		}
		record, _, err := readIsolation(dir)
		if err != nil || record == nil || !exists(record.Dir) || DirSize(record.Dir) == 0 {
			continue
		}
		if record.Prefix != "sleipnir/"+id || filepath.Base(record.Dir) != id {
			continue // not the shape the harness records: not ours to judge
		}
		before := DirSize(record.Dir)
		unlock, err := lockDir(dir)
		if err != nil {
			continue // a process holds the session
		}
		kept, planned, err := releaseIdle(ctx, record, o.DryRun)
		unlock()
		rep.Kept = append(rep.Kept, kept...)
		if err != nil {
			rep.Errors = append(rep.Errors, "worktrees of session "+id+": "+firstLine(err.Error(), 300))
			continue
		}
		freed := before - DirSize(record.Dir)
		if o.DryRun {
			freed = planned
		}
		if freed > 0 {
			rep.Idle++
			rep.TreesBytes += freed
		}
	}
}

// releaseIdle removes the trees of an isolated session that hold nothing of their own (workspace.PruneOptions.KeepUnmerged), keeping
// the session's recovery references, and names what it kept. With dry it removes nothing and says how much it would free.
func releaseIdle(ctx context.Context, record *isolationRecord, dry bool) (kept []string, planned int64, err error) {
	repo, err := gitx.Open(record.Root)
	if err != nil {
		return nil, 0, err
	}
	if !samePathLoose(repo.Root(), record.Root) {
		return nil, 0, errors.New("the recorded repository root has changed")
	}
	m := &workspace.Manager{Repo: repo, Dir: record.Dir, Prefix: record.Prefix, Base: record.Base}
	refs := []string{record.Prefix + "/_resume", record.Prefix + "/_base", record.Prefix + "/" + "_integration", record.Prefix + "/_apply"}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout) // to its end: see CleanStorage
	defer cancel()
	r, err := m.Prune(pctx, workspace.PruneOptions{KeepUnmerged: true, RequireInactive: true, KeepPrefixes: refs, DryRun: dry})
	if err != nil {
		return nil, 0, err
	}
	for _, a := range r.Kept {
		kept = append(kept, a.Path+": "+a.Reason)
	}
	if dry {
		for _, a := range r.Removed {
			planned += DirSize(a.Path)
		}
	}
	return kept, planned, nil
}

// lastWritten is the newest of a session directory's and its event log's modification times (the age prune judges by).
func lastWritten(dir string) time.Time {
	var t time.Time
	if fi, err := os.Lstat(dir); err == nil {
		t = fi.ModTime()
	}
	if fi, err := os.Stat(filepath.Join(dir, "events.jsonl")); err == nil && fi.ModTime().After(t) {
		t = fi.ModTime()
	}
	return t
}

// cleanOrphanTrees releases the folders of worktrees under top whose session is gone: no session of that id is recorded in the
// sessions directory, and nothing in the folder was written for orphanTreesAge (a session recorded elsewhere, with --session-dir,
// is long done with them by then). A folder of a recorded session is left to the deletion of that session.
// The trees are released as RemoveStoredSession releases them; a folder left empty is removed, and so is top itself when it is the
// old cache location and nothing is left in it.
func cleanOrphanTrees(ctx context.Context, home, top string, now time.Time, o CleanOptions, rep *CleanReport) {
	ents, err := os.ReadDir(top)
	if err != nil {
		return
	}
	sessions := SessionsDir(home)
	for _, e := range ents {
		if ctx.Err() != nil {
			return
		}
		id := e.Name()
		dir := filepath.Join(top, id)
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || !ValidID(id) && !looseID(id) {
			continue // a file, a link, or a name the harness never gives a folder of trees
		}
		if hasLog(filepath.Join(sessions, id)) {
			continue // its session is recorded: the trees go with it
		}
		if now.Sub(newestShallow(dir)) < orphanTreesAge {
			continue
		}
		n := DirSize(dir)
		if o.DryRun {
			rep.Trees = append(rep.Trees, dir)
			rep.TreesBytes += n
			continue
		}
		freed, kept, err := releaseTrees(ctx, dir, id)
		rep.Kept = append(rep.Kept, kept...)
		if err != nil {
			rep.Errors = append(rep.Errors, "worktrees "+dir+": "+firstLine(err.Error(), 300))
			continue
		}
		if freed {
			rep.Trees = append(rep.Trees, dir)
			rep.TreesBytes += n - DirSize(dir)
		}
	}
	if top == legacyWorktreesDir(home) {
		if os.Remove(top) == nil { // only when empty
			_ = os.Remove(filepath.Dir(top))
		}
	}
}

// looseID accepts the folder names of sessions given an id of their own (Options.ID): a plain name, without separators or dots
// at its start.
func looseID(id string) bool {
	return id != "" && !strings.HasPrefix(id, ".") && !strings.ContainsAny(id, `/\`) && !strings.HasPrefix(id, "_")
}

// newestShallow is the newest modification time of dir and of the entries directly in it and in each of its subdirectories (a
// tree's own files are its agent's; a tree in use is written at its top or one level down soon enough).
func newestShallow(dir string) time.Time {
	var t time.Time
	see := func(p string) []fs.DirEntry {
		if fi, err := os.Lstat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
		ents, _ := os.ReadDir(p)
		return ents
	}
	for _, e := range see(dir) {
		if e.IsDir() {
			for _, c := range see(filepath.Join(dir, e.Name())) {
				if fi, err := c.Info(); err == nil && fi.ModTime().After(t) {
					t = fi.ModTime()
				}
			}
		}
	}
	return t
}

// releaseTrees salvages and removes the worktrees in dir, the folder of the trees of session id, as RemoveStoredSession does for a
// recorded session: each tree's uncommitted edits are committed onto its branch, the trees are removed, and the branches that hold
// no work of their own go. freed says whether anything was removed; kept names what was left and why.
func releaseTrees(ctx context.Context, dir, id string) (freed bool, kept []string, err error) {
	repo, err := treesRepo(dir)
	if err != nil {
		if empty(dir) {
			return os.Remove(dir) == nil, nil, nil
		}
		return false, []string{dir + ": " + err.Error() + "; left as it is"}, nil
	}
	m := &workspace.Manager{Repo: repo, Dir: dir, Prefix: "sleipnir/" + id}
	if owner, ok, err := repo.UserIdentity(ctx); err == nil && ok {
		m.Owner = owner
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout) // to its end: see CleanStorage
	defer cancel()
	r, err := m.Prune(pctx, workspace.PruneOptions{Salvage: true, RequireInactive: true})
	if err != nil {
		return false, nil, err
	}
	seen := map[string]bool{}
	for _, b := range r.BranchesKept {
		if !seen[b.Branch] {
			seen[b.Branch] = true
			kept = append(kept, "branch "+b.Branch+" holds work no other branch has (repository "+repo.Root()+")")
		}
	}
	for _, a := range r.Kept {
		kept = append(kept, a.Path+": "+a.Reason)
	}
	if r.Live > 0 {
		kept = append(kept, dir+": a tree's owner may still be running; left as it is")
	}
	_ = os.Remove(dir) // only when empty
	return len(r.Removed) > 0 || !exists(dir), kept, nil
}

// treesRepo finds the repository the worktrees in dir belong to: the main checkout of the repository the first tree's .git file
// points into.
func treesRepo(dir string) (*gitx.Repo, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), ".git"))
		if err != nil || len(data) > 4096 {
			continue
		}
		gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			continue
		}
		admin := strings.TrimSpace(gitdir)
		if !filepath.IsAbs(admin) {
			admin = filepath.Join(dir, e.Name(), admin) // relative to the tree (worktree.useRelativePaths)
		}
		admin = filepath.Clean(admin)
		if filepath.Base(filepath.Dir(admin)) != "worktrees" {
			continue
		}
		common := filepath.Dir(filepath.Dir(admin))
		if filepath.Base(common) != ".git" {
			continue // a bare repository, or a layout the harness never makes
		}
		repo, err := gitx.Open(filepath.Dir(common))
		if err != nil {
			continue
		}
		if !samePathLoose(repo.CommonDir(), common) {
			continue
		}
		return repo, nil
	}
	return nil, errors.New("the repository of these worktrees cannot be found")
}

// empty reports whether dir is a directory with nothing in it.
func empty(dir string) bool {
	ents, err := os.ReadDir(dir)
	return err == nil && len(ents) == 0
}

// exists reports whether something is at p.
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// autoCleanStamp is the file in the data directory that says when the automatic cleanup last ran.
const autoCleanStamp = "storage-clean.json"

// AutoCleanEvery is how often the automatic cleanup runs at most.
const AutoCleanEvery = 24 * time.Hour

// AutoClean runs the storage cleanup of the data directory of home when the settings allow it (config.Storage) and it has not run in
// the last AutoCleanEvery, and records that it ran. live are the sessions this process hosts. It returns nil when it did not run.
func AutoClean(ctx context.Context, home string, st config.Storage, live []string, now time.Time) (*CleanReport, error) {
	if !st.AutoClean() {
		return nil, nil
	}
	idle, ok := config.ParseAge(st.Idle())
	if !ok || idle < time.Hour {
		return nil, fmt.Errorf("storage.worktree_idle %q is not an age of an hour or more", st.Idle())
	}
	stamp := filepath.Join(StateRoot(home), autoCleanStamp)
	var last struct {
		At time.Time `json:"at"`
	}
	if b, err := os.ReadFile(stamp); err == nil && json.Unmarshal(b, &last) == nil && now.Sub(last.At) < AutoCleanEvery && !last.At.After(now) {
		return nil, nil
	}
	if _, err := os.Stat(StateRoot(home)); err != nil {
		return nil, nil // nothing recorded yet
	}
	b, _ := json.Marshal(map[string]any{"at": now.UTC()})
	if err := os.WriteFile(stamp, append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	return CleanStorage(ctx, home, CleanOptions{TreesIdle: idle, Live: live, Now: now}) // never Sessions: no history goes by itself
}
