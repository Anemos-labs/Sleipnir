package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

// Mode selects how a tree is made.
type Mode int

const (
	// ModeAuto uses git worktrees when Manager.Repo is set and a copy-on-write
	// snapshot otherwise.
	ModeAuto Mode = iota
	// ModeWorktree gives each agent a git worktree on its own branch, sharing the
	// repository's object store.
	ModeWorktree
	// ModeCopy gives each agent a copy-on-write copy of a directory snapshot
	// (reflink where the filesystem supports it, a plain copy otherwise). It is the
	// fallback for directories that are not git repositories. A private repository
	// tracks the copies so that Diff, Commit, Reset and the merge queue behave
	// exactly as for worktrees.
	ModeCopy
)

func (m Mode) String() string {
	switch m {
	case ModeWorktree:
		return "worktree"
	case ModeCopy:
		return "copy"
	}
	return "auto"
}

// Manager creates and disposes isolated trees for agents. Set the exported fields
// before first use and do not change them afterwards; a Manager must not be
// copied. It is safe for concurrent use.
type Manager struct {
	// Repo is the repository to make worktrees of. Nil selects ModeCopy over
	// Source.
	Repo *gitx.Repo
	// Dir is the directory that holds the trees (<Dir>/<agent>). It is created
	// (mode 0700) if needed. Keep it outside the repository or ignored by it.
	Dir string
	// Prefix is the branch namespace, for example "sleipnir/<session>": agent
	// branches are <Prefix>/<agent>. Default "sleipnir". Prune and Remove only ever
	// touch branches under it. A manager with Prefix "sleipnir" and a Dir that
	// contains the per-session directories can sweep every session's leftovers.
	Prefix string
	// Base is the revision new trees start from (default "HEAD"). It is resolved
	// once, on first use, so every tree of a session shares one base commit even if
	// the user commits meanwhile; CreateOptions.Base overrides it per tree.
	Base string
	// Mode selects worktrees or copies (see the constants).
	Mode Mode
	// Source is the directory ModeCopy snapshots (default: Repo's work tree).
	Source string
	// Snapshot makes the base include the user's uncommitted changes (tracked
	// edits and untracked, non-ignored files) instead of the committed HEAD alone:
	// agents then start from what the user sees, and Queue.Finish returns only what
	// the agents added, which applies cleanly on top of the user's edits.
	Snapshot bool
	// Excludes are extra names or slash paths (relative to Source) ModeCopy leaves
	// out, on top of the defaults (VCS directories, node_modules, caches).
	Excludes []string
	// MaxFileBytes makes Tree.Commit refuse files larger than this, so a stray build
	// artifact or core dump does not become permanent repository history (default
	// 64 MiB; negative disables the check).
	MaxFileBytes int64
	// MaxDiffBytes bounds Tree.Diff (default 64 MiB).
	MaxDiffBytes int
	// OnEvent receives workspace events (optional).
	OnEvent EventFunc
	// Clock supplies commit times (default time.Now); tests pin it.
	Clock func() time.Time

	mu    sync.Mutex
	ready bool
	st    mstate
	trees map[string]*Tree
}

// mstate is what init derives.
type mstate struct {
	mode    Mode
	dirReal string
	prefix  string
	// base is the repository that owns the worktrees: the user's repository, or
	// the private one behind copy mode.
	base    *gitx.Repo
	baseSHA string
	// sourceDirty records that the user's work tree had uncommitted changes when
	// the base was chosen and Snapshot was off, so they are not in the base.
	sourceDirty bool
	source      string // ModeCopy: the directory that was snapshotted
	pristine    string // ModeCopy: the pristine snapshot work tree
	self        self
	lock        *sync.Mutex
}

// livePaths remembers which Manager instance owns each tree directory in this
// process. A second Manager over the same Dir (a restarted session component, a
// sweeper) must not adopt a tree that a running Manager is using, even though the
// marker's process id is this very process.
var livePaths sync.Map // tree path -> *Manager

// repoLocks serialize the metadata-changing steps (worktree add/remove, branch
// create/delete) per repository across every Manager in the process: git's own
// lock files make concurrent runs fail rather than corrupt, but "fail" would turn
// a swarm start-up into a retry storm.
var repoLocks sync.Map // common dir -> *sync.Mutex

func lockFor(commonDir string) *sync.Mutex {
	l, _ := repoLocks.LoadOrStore(commonDir, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// EffectiveMode reports whether the manager uses worktrees or copies. Valid after
// the first successful call.
func (m *Manager) EffectiveMode() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.mode
}

// BaseSHA is the commit new trees start from (available after first use).
func (m *Manager) BaseSHA() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.baseSHA
}

// SourceDirty reports whether the user's work tree had uncommitted changes that
// are not part of the base (Snapshot off).
func (m *Manager) SourceDirty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.sourceDirty
}

// Repository returns the repository that owns the trees: Manager.Repo, or the
// private one behind copy mode.
func (m *Manager) Repository() *gitx.Repo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.base
}

// TreesDir is Dir with symlinks resolved: the directory every tree path is under.
func (m *Manager) TreesDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.dirReal
}

func (m *Manager) now() time.Time {
	if m.Clock != nil {
		return m.Clock()
	}
	return time.Now()
}

func (m *Manager) emit(typ, agent, task string, data map[string]any) {
	m.OnEvent.emit(typ, agent, task, data)
}

func (m *Manager) maxFileBytes() int64 {
	switch {
	case m.MaxFileBytes < 0:
		return -1
	case m.MaxFileBytes == 0:
		return 64 << 20
	}
	return m.MaxFileBytes
}

func (m *Manager) maxDiffBytes() int {
	if m.MaxDiffBytes <= 0 {
		return 64 << 20
	}
	return m.MaxDiffBytes
}

// ensure initializes the manager. A failed attempt is not cached: it may have been
// a canceled context.
func (m *Manager) ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ready {
		return nil
	}
	st, err := m.derive(ctx)
	if err != nil {
		return err
	}
	m.st = st
	m.trees = map[string]*Tree{}
	m.ready = true
	return nil
}

func (m *Manager) derive(ctx context.Context) (mstate, error) {
	var st mstate
	if strings.TrimSpace(m.Dir) == "" {
		return st, fmt.Errorf("%w: Manager.Dir is required", ErrBadName)
	}
	st.prefix = m.Prefix
	if st.prefix == "" {
		st.prefix = "sleipnir"
	}
	if err := ValidatePrefix(st.prefix); err != nil {
		return st, err
	}
	abs, err := filepath.Abs(m.Dir)
	if err != nil {
		return st, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return st, fmt.Errorf("workspace: cannot create %s: %w", abs, err)
	}
	if st.dirReal, err = filepath.EvalSymlinks(abs); err != nil {
		return st, err
	}
	st.self = currentSelf()

	switch {
	case m.Mode == ModeCopy || m.Repo == nil:
		st.mode = ModeCopy
	default:
		st.mode = ModeWorktree
	}

	if st.mode == ModeWorktree {
		st.base = m.Repo
		st.lock = lockFor(m.Repo.CommonDir())
		rev := m.Base
		if rev == "" {
			rev = "HEAD"
		}
		sha, err := m.Repo.ResolveRef(ctx, rev)
		if err != nil {
			if gitx.KindOf(err) == gitx.KindNotFound && rev == "HEAD" {
				return st, fmt.Errorf("%w: commit the project first (or use ModeCopy)", ErrNoCommits)
			}
			return st, err
		}
		st.baseSHA = sha
		if !m.Repo.IsBare() {
			st.sourceDirty, sha, err = m.dirtyBase(ctx, st, sha)
			if err != nil {
				return st, err
			}
			st.baseSHA = sha
		}
		return st, nil
	}
	return m.deriveCopy(ctx, st)
}

// dirtyBase implements Snapshot: it turns the user's uncommitted state into a
// commit (unreachable from any user branch) and returns it as the base. When
// Snapshot is off it only reports whether such state exists.
func (m *Manager) dirtyBase(ctx context.Context, st mstate, base string) (dirty bool, sha string, err error) {
	sha = base
	tree, err := m.Repo.SnapshotTree(ctx)
	if err != nil {
		return false, sha, err
	}
	baseTree, err := m.Repo.ResolveTree(ctx, base)
	if err != nil {
		return false, sha, err
	}
	if tree == baseTree {
		return false, sha, nil
	}
	if !m.Snapshot {
		return true, sha, nil
	}
	c, err := m.Repo.CommitTree(ctx, tree, []string{base}, "sleipnir: snapshot of the working tree", gitx.Author{Name: "Sleipnir", Email: "sleipnir@localhost", When: m.now()})
	if err != nil {
		return false, sha, err
	}
	// Keep the commit reachable so gc cannot collect it while trees are based on it.
	if err := setBranch(ctx, m.Repo, st.prefix+"/"+baseBranchName, c); err != nil {
		return false, sha, err
	}
	return true, c, nil
}

// setBranch points name at sha whatever it pointed at before (or creates it).
func setBranch(ctx context.Context, r *gitx.Repo, name, sha string) error {
	old, err := r.BranchSHA(ctx, name)
	switch {
	case err == nil:
		return r.UpdateBranch(ctx, name, sha, old, "sleipnir: base")
	case gitx.KindOf(err) == gitx.KindNotFound:
		return r.UpdateBranch(ctx, name, sha, "", "sleipnir: base")
	}
	return err
}

// CreateOptions tunes Create.
type CreateOptions struct {
	// Base overrides the manager's base revision for this tree (typically the merge
	// queue's current tip, so late starters begin from fresh code).
	Base string
	// Detach checks out the base without creating a branch.
	Detach bool
	// Sparse limits the checkout to these directories plus the files at the
	// repository root (git's cone mode). It makes creating a tree for a scoped task
	// in a huge repository cheap. It needs worktree mode, and turns on git's
	// per-worktree configuration extension in the shared repository.
	Sparse []string
	// Copy lists paths (relative to the source work tree) that are not in the base
	// but agents need, such as .env or a prebuilt vendor directory; they are copied
	// (reflinked where possible) when present in the source.
	Copy []string
	// Reuse returns the agent's existing tree instead of failing when one is
	// already there and this manager (or a dead earlier run of it) created it.
	Reuse bool
}

// Create makes an isolated tree for agentID.
//
// The metadata step (worktree registration, branch creation) runs under a lock
// shared by every manager of the repository; populating the files does not, so a
// swarm start-up checks out its trees in parallel.
func (m *Manager) Create(ctx context.Context, agentID string, opts CreateOptions) (*Tree, error) {
	if err := ValidateAgentID(agentID); err != nil {
		return nil, err
	}
	return m.create(ctx, agentID, opts, treeSpec{})
}

// treeSpec carries what only the manager's own trees need.
type treeSpec struct {
	integration bool
	ref         string
}

func (m *Manager) create(ctx context.Context, agent string, opts CreateOptions, spec treeSpec) (*Tree, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	if len(opts.Sparse) > 0 && m.st.mode == ModeCopy {
		return nil, fmt.Errorf("%w: sparse checkout needs a git repository (worktree mode)", ErrUnsupported)
	}
	dest := filepath.Join(m.st.dirReal, agent)
	if !strictlyUnder(m.st.dirReal, dest) {
		return nil, fmt.Errorf("%w: %s", ErrOutsideDir, dest)
	}
	base := m.st.baseSHA
	if opts.Base != "" {
		sha, err := m.st.base.ResolveRef(ctx, opts.Base)
		if err != nil {
			return nil, err
		}
		base = sha
	}
	branch := ""
	if !opts.Detach && !spec.integration {
		branch = m.st.prefix + "/" + agent
	}

	t, fresh, err := m.register(ctx, agent, dest, branch, base, opts, spec)
	if err != nil {
		return nil, err
	}
	if !fresh {
		return t, nil // an existing tree taken over as it is: its files are the agent's work
	}
	if err := m.populate(ctx, t, opts); err != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if rerr := m.discard(cctx, t, true); rerr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup of %s failed: %w", dest, rerr))
		}
		return nil, err
	}
	m.mu.Lock()
	m.trees[agent] = t
	m.mu.Unlock()
	livePaths.Store(dest, m)
	data := map[string]any{"path": t.Path, "branch": t.Branch, "base": t.Base, "mode": m.st.mode.String()}
	if len(opts.Sparse) > 0 {
		data["sparse"] = opts.Sparse
	}
	m.emit(EventCreate, agent, "", data)
	return t, nil
}

// register runs the metadata step under the repository lock: it refuses to
// overwrite anything, clears our own stale leftovers, registers the worktree
// without checking files out, and writes the ownership marker.
func (m *Manager) register(ctx context.Context, agent, dest, branch, base string, opts CreateOptions, spec treeSpec) (t *Tree, fresh bool, err error) {
	m.st.lock.Lock()
	defer m.st.lock.Unlock()

	m.mu.Lock()
	if old, ok := m.trees[agent]; ok && !old.isRemoved() {
		m.mu.Unlock()
		if opts.Reuse && !spec.integration {
			return old, false, nil
		}
		return nil, false, fmt.Errorf("%w: agent %s already has a tree at %s", ErrExists, agent, old.Path)
	}
	m.mu.Unlock()

	if fi, err := os.Lstat(dest); err == nil {
		if opts.Reuse {
			if t, aerr := m.adopt(ctx, agent, dest, base); aerr == nil {
				return t, false, nil
			}
		}
		_ = fi
		return nil, false, fmt.Errorf("%w: %s", ErrExists, dest)
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	if err := m.reclaimStale(ctx, agent, dest, branch); err != nil {
		return nil, false, err
	}

	wa := gitx.WorktreeAddOptions{Path: dest, Branch: branch, Detach: branch == "", Commit: base, NoCheckout: true}
	if err := m.st.base.WorktreeAdd(ctx, wa); err != nil {
		return nil, false, err
	}
	repo, err := m.st.base.Reopen(ctx, dest)
	if err != nil {
		_ = m.st.base.WorktreeRemove(ctx, dest, true)
		if branch != "" {
			_ = m.st.base.DeleteBranch(ctx, branch)
		}
		return nil, false, err
	}
	mk := marker{
		Prefix: m.st.prefix, Agent: agent, Path: dest, Branch: branch, Base: base, Mode: m.st.mode.String(),
		Integration: spec.integration, Ref: spec.ref,
		PID: m.st.self.pid, Start: m.st.self.start, BootID: m.st.self.bootID, Created: m.now().UTC(),
	}
	if err := writeMarker(repo.GitDir(), mk); err != nil {
		_ = m.st.base.WorktreeRemove(ctx, dest, true)
		if branch != "" {
			_ = m.st.base.DeleteBranch(ctx, branch)
		}
		return nil, false, fmt.Errorf("workspace: cannot write marker: %w", err)
	}
	return &Tree{Path: dest, Branch: branch, Base: base, Agent: agent, Mode: m.st.mode, m: m, repo: repo, integration: spec.integration, ref: spec.ref}, true, nil
}

// reclaimStale clears what a crashed earlier run of ours left behind under the
// name we are about to use: a registered worktree whose directory is gone, or a
// branch nobody has checked out that holds no unique work. Anything else means
// the name is taken.
func (m *Manager) reclaimStale(ctx context.Context, agent, dest, branch string) error {
	list, err := m.st.base.Worktrees(ctx)
	if err != nil {
		return err
	}
	for _, w := range list {
		if !samePath(w.Path, dest) && (branch == "" || w.Branch != branch) {
			continue
		}
		if !w.Prunable {
			return fmt.Errorf("%w: %s is checked out at %s", ErrExists, nameOr(branch, dest), w.Path)
		}
		// A missing directory: ours to clear only if its marker says so and its owner
		// is gone.
		mk := m.markerForPath(w.Path)
		if mk == nil || !m.ownsMarker(mk) || mk.owner() == ownerAlive {
			return fmt.Errorf("%w: %s is registered at %s (missing) and is not ours to clear", ErrExists, nameOr(branch, dest), w.Path)
		}
		if err := m.st.base.WorktreeRemove(ctx, w.Path, true); err != nil {
			return err
		}
	}
	if branch == "" {
		return nil
	}
	if _, err := m.st.base.BranchSHA(ctx, branch); err != nil {
		if gitx.KindOf(err) == gitx.KindNotFound {
			return nil
		}
		return err
	}
	n, err := m.st.base.CommitsOnlyOn(ctx, branch, m.st.prefix+"/*", m.integrationRef())
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: branch %s holds %d commit(s) that exist nowhere else; run Prune or pick another agent id", ErrExists, branch, n)
	}
	return m.st.base.DeleteBranch(ctx, branch)
}

func nameOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (m *Manager) integrationRef() string {
	return "refs/heads/" + m.st.prefix + "/" + integrationName
}

// populate fills a registered tree with files (outside the repository lock).
func (m *Manager) populate(ctx context.Context, t *Tree, opts CreateOptions) error {
	switch m.st.mode {
	case ModeCopy:
		if err := m.populateCopy(ctx, t); err != nil {
			return err
		}
	default:
		if len(opts.Sparse) > 0 {
			if err := t.repo.SparseCheckoutSet(ctx, opts.Sparse); err != nil {
				return err
			}
		}
		// The tree was registered without a checkout; reset --hard populates it (and
		// honors the sparse patterns just set).
		if err := t.repo.ResetHard(ctx, t.Base); err != nil {
			return err
		}
	}
	return m.copyExtras(ctx, t, opts.Copy)
}

// adopt takes over a tree directory that exists from an earlier run of this
// manager: the marker must be ours and its owner gone (or this process).
func (m *Manager) adopt(ctx context.Context, agent, dest, base string) (*Tree, error) {
	admin, mk, err := m.verifyTreeDir(ctx, dest)
	if err != nil {
		return nil, err
	}
	if mk.Agent != agent || mk.Integration {
		return nil, ErrExists
	}
	if mk.owner() == ownerAlive {
		if mk.PID != m.st.self.pid {
			return nil, ErrExists
		}
		if other, ok := livePaths.Load(dest); ok && other.(*Manager) != m {
			return nil, ErrExists // a running manager in this process is using it
		}
	}
	repo, err := m.st.base.Reopen(ctx, dest)
	if err != nil {
		return nil, err
	}
	mk.PID, mk.Start, mk.BootID = m.st.self.pid, m.st.self.start, m.st.self.bootID
	if err := writeMarker(admin, *mk); err != nil {
		return nil, err
	}
	t := &Tree{Path: dest, Branch: mk.Branch, Base: mk.Base, Agent: agent, Mode: m.st.mode, m: m, repo: repo}
	m.mu.Lock()
	m.trees[agent] = t
	m.mu.Unlock()
	livePaths.Store(dest, m)
	return t, nil
}

// Get returns the live tree this manager created (or adopted) for agent.
func (m *Manager) Get(agent string) (*Tree, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.trees[agent]
	if !ok || t.isRemoved() {
		return nil, false
	}
	return t, true
}

// Trees returns the live trees created by this manager, sorted by agent.
func (m *Manager) Trees() []*Tree {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Tree
	for _, t := range m.trees {
		if !t.isRemoved() {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// Info describes one tree found in the repository.
type Info struct {
	Agent  string
	Path   string
	Branch string
	Base   string
	Head   string
	// Integration marks the merge queue's tree.
	Integration bool
	// Owner is "alive", "dead" or "unknown" (recorded on another boot or machine).
	Owner string
	// Prunable is set when the tree's directory no longer exists.
	Prunable bool
	Created  time.Time
}

// List returns the trees under this manager's prefix and directory, whoever
// created them, sorted by path. Worktrees that lack our marker are not listed:
// they are not ours.
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	if err := m.ensure(ctx); err != nil {
		return nil, err
	}
	entries, err := m.ownedEntries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(entries))
	for _, e := range entries {
		info := Info{Agent: e.mk.Agent, Path: e.wt.Path, Branch: e.mk.Branch, Base: e.mk.Base, Head: e.wt.Head,
			Integration: e.mk.Integration, Prunable: e.wt.Prunable, Created: e.mk.Created}
		switch e.mk.owner() {
		case ownerAlive:
			info.Owner = "alive"
		case ownerDead:
			info.Owner = "dead"
		default:
			info.Owner = "unknown"
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ownedEntry pairs a git worktree entry with its marker.
type ownedEntry struct {
	wt    gitx.Worktree
	mk    *marker
	admin string
}

// ownedEntries lists the worktrees this manager may act on: marker present and
// valid, marker prefix equal to or below ours, path under Dir. Everything else is
// invisible here by design.
func (m *Manager) ownedEntries(ctx context.Context) ([]ownedEntry, error) {
	list, err := m.st.base.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	admins := scanAdminDirs(m.st.base.CommonDir())
	var out []ownedEntry
	for i, w := range list {
		if i == 0 && !w.Bare && samePath(w.Path, m.st.base.Root()) {
			continue // the main work tree is never ours
		}
		a := admins.find(w.Path)
		if a == nil || a.mk == nil {
			continue
		}
		if !m.ownsMarker(a.mk) || !samePath(a.mk.Path, w.Path) || !strictlyUnder(m.st.dirReal, resolveLoose(w.Path)) {
			continue
		}
		out = append(out, ownedEntry{wt: w, mk: a.mk, admin: a.dir})
	}
	return out, nil
}

// ownsMarker: the marker's prefix is ours or nested under ours.
func (m *Manager) ownsMarker(mk *marker) bool {
	return mk.Prefix == m.st.prefix || strings.HasPrefix(mk.Prefix, m.st.prefix+"/")
}

// markerForPath finds the marker of the worktree registered at path.
func (m *Manager) markerForPath(path string) *marker {
	if a := scanAdminDirs(m.st.base.CommonDir()).find(path); a != nil {
		return a.mk
	}
	return nil
}

// adminEntry is one .git/worktrees/<id> directory.
type adminEntry struct {
	dir  string
	tree string
	mk   *marker
}

type adminSet []adminEntry

// scanAdminDirs lists the repository's linked-worktree administrative
// directories with their markers.
func scanAdminDirs(commonDir string) adminSet {
	root := filepath.Join(commonDir, "worktrees")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out adminSet
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		gd, err := os.ReadFile(filepath.Join(dir, "gitdir"))
		if err != nil {
			continue
		}
		p := strings.TrimSpace(string(gd)) // <tree>/.git
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		a := adminEntry{dir: dir, tree: filepath.Dir(filepath.Clean(p))}
		if mk, err := readMarker(dir); err == nil {
			a.mk = mk
		}
		out = append(out, a)
	}
	return out
}

func (s adminSet) find(treePath string) *adminEntry {
	for i := range s {
		if samePath(s[i].tree, treePath) {
			return &s[i]
		}
	}
	return nil
}

// samePath compares two paths, resolving symlinks where the paths exist.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	return resolveLoose(a) == resolveLoose(b)
}

// resolveLoose resolves symlinks in the longest existing prefix of p and appends
// the rest, so it works for paths that no longer exist.
func resolveLoose(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolveLoose(parent), filepath.Base(p))
}

// strictlyUnder reports whether p is inside dir (not dir itself), lexically.
func strictlyUnder(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
