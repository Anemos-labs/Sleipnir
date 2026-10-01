package env

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Workspace modes.
const (
	// ModeExport (the default) writes the commit's files into a fresh directory
	// with a new single-commit repository. The agent can use git normally but
	// finds no history: no future commits (the fix of a task mined from history),
	// no other branches, no remote.
	ModeExport = "export"
	// ModeClone makes a local clone with full history, for tasks where history
	// is deliberately part of the problem. For tasks mined from history it hands
	// the agent the solution.
	ModeClone = "clone"
)

// WorkspaceOptions configures a Workspaces manager. Only Root is required.
type WorkspaceOptions struct {
	// Root is the directory everything lives under: cached snapshots, rollout
	// workspaces, verification checkouts, mirrors and scratch space. Nothing is
	// ever created outside it (except by the commands a task runs).
	Root string
	// Mode is ModeExport (default) or ModeClone.
	Mode string
	// Sandbox runs setup and verifier commands. Default: a LocalSandbox.
	Sandbox Sandbox
	// RepoBase resolves relative repo paths; default the current directory.
	RepoBase string
	// SetupTimeout bounds each setup command (default 15 min).
	SetupTimeout time.Duration
	// KillGrace is the SIGTERM to SIGKILL delay (default 2 s).
	KillGrace time.Duration
	// MaxOutput bounds the output kept per command stream (default 1 MiB).
	MaxOutput int64
	// Limits are the resource limits of setup and verifier commands.
	Limits Limits
	// BaseEnv is the environment pass-through variables are drawn from; nil
	// means os.Environ().
	BaseEnv []string
	// PassEnv and SetEnv extend and override the scrubbed command environment
	// (see EnvSpec). SetEnv is how tests and operators point tools at a shared
	// cache: doing so trades isolation for speed.
	PassEnv []string
	SetEnv  map[string]string
	// GitBin names the git executable; "" looks it up on PATH.
	GitBin string
	// RequireNetIsolation makes the manager refuse to start where network
	// isolation is unavailable, instead of running network-off tasks without it.
	RequireNetIsolation bool
	// DisableNetIsolation skips network isolation entirely.
	DisableNetIsolation bool
	// SkipSnapshotCheck turns off the integrity check run each time a cached
	// snapshot is used (see checkSnapshot).
	SkipSnapshotCheck bool
	// MaxFiles and MaxFileBytes bound what is hashed into a baseline or diff.
	MaxFiles     int
	MaxFileBytes int64
	// FailureTTL is how long a failed setup is remembered, so the remaining
	// samples of a broken task fail at once instead of each repeating the setup.
	// Default 30 s; negative disables.
	FailureTTL time.Duration
	// LookPath and Probe replace how the default sandbox finds and tries host
	// tools (unshare, ip, prlimit); they exist for tests and embedders with
	// unusual hosts. Nil means exec.LookPath and running the command.
	LookPath func(string) (string, error)
	Probe    func(ctx context.Context, name string, args ...string) error
	// Logf receives diagnostics; nil discards them.
	Logf func(format string, args ...any)
}

// Workspaces creates isolated, disposable working trees for rollouts and the
// clean checkouts the verifier runs in. See the package documentation for the
// security model.
type Workspaces struct {
	opts    WorkspaceOptions
	root    string
	tmp     string
	git     *Git
	sandbox Sandbox
	local   *LocalSandbox
	baseEnv []string
	cl      cloner

	baseCtx context.Context
	cancel  context.CancelFunc
	bg      sync.WaitGroup

	emptyOnce sync.Once
	emptyDir  string
	emptyErr  error

	mu       sync.Mutex
	loaded   map[string]*snapshot
	flights  map[string]*flight
	failures map[string]failure
	gen      map[string]int
	mirrors  map[string]*sync.Mutex
	warnings []string
}

type flight struct {
	done chan struct{}
	snap *snapshot
	err  error
}

type failure struct {
	err   error
	until time.Time
}

// NewWorkspaces creates the directory layout under o.Root and probes the host.
func NewWorkspaces(o WorkspaceOptions) (*Workspaces, error) {
	if o.Root == "" {
		return nil, errors.New("WorkspaceOptions.Root is required")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	if o.Mode == "" {
		o.Mode = ModeExport
	}
	if o.Mode != ModeExport && o.Mode != ModeClone {
		return nil, fmt.Errorf("unknown workspace mode %q", o.Mode)
	}
	if o.SetupTimeout <= 0 {
		o.SetupTimeout = 15 * time.Minute
	}
	if o.FailureTTL == 0 {
		o.FailureTTL = 30 * time.Second
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	for _, d := range []string{"", "snaps", "ws", "verify", "repos", "tmp", "git-home"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, err
		}
	}
	// Resolve symlinks once so that "is this path under the root" checks compare
	// like with like (macOS's /var is a link to /private/var, for example).
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	g, err := NewGit(GitOptions{Bin: o.GitBin, Scratch: filepath.Join(root, "git-home"), PassEnv: o.PassEnv, Base: o.BaseEnv})
	if err != nil {
		return nil, err
	}
	m := &Workspaces{
		opts: o, root: root, tmp: filepath.Join(root, "tmp"), git: g,
		baseEnv: o.BaseEnv, loaded: map[string]*snapshot{}, flights: map[string]*flight{},
		failures: map[string]failure{}, gen: map[string]int{}, mirrors: map[string]*sync.Mutex{},
	}
	m.baseCtx, m.cancel = context.WithCancel(context.Background())
	m.opts.MaxOutput = max(o.MaxOutput, 0)
	if o.Sandbox != nil {
		m.sandbox = o.Sandbox
	} else {
		ls, err := NewLocalSandbox(LocalSandboxOptions{
			DisableNetIsolation: o.DisableNetIsolation,
			Defaults:            ExecPolicy{Limits: o.Limits, MaxOutput: o.MaxOutput, Grace: o.KillGrace},
			LookPath:            o.LookPath, Probe: o.Probe,
		})
		if err != nil {
			return nil, err
		}
		m.local, m.sandbox = ls, ls
		m.warnings = append(m.warnings, ls.Warnings()...)
		if o.RequireNetIsolation && !ls.NetIsolation().Available {
			return nil, fmt.Errorf("network isolation is required but unavailable: %s", ls.NetIsolation().Reason)
		}
	}
	return m, nil
}

// Root returns the absolute root directory.
func (m *Workspaces) Root() string { return m.root }

// Git returns the hardened git runner, for task generators.
func (m *Workspaces) Git() *Git { return m.git }

// Warnings lists host degradations (for example unavailable network
// isolation) that a run manifest should record.
func (m *Workspaces) Warnings() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.warnings...)
}

func (m *Workspaces) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	m.mu.Lock()
	for _, w := range m.warnings {
		if w == msg {
			m.mu.Unlock()
			return
		}
	}
	m.warnings = append(m.warnings, msg)
	m.mu.Unlock()
	m.opts.Logf("env: %s", msg)
}

// IsolationPrefix returns the argv prefix a harness can put in front of the
// shell commands it runs for the agent to deny them network access (a fresh
// network namespace with loopback up), or nil when the task may use the
// network or the host cannot isolate. The harness owns the agent's shell, so
// applying the prefix is its job; see RunSpec.NetPrefix.
func (m *Workspaces) IsolationPrefix(task rl.Task) []string {
	if task.Network || m.local == nil || !m.local.netIso.Available {
		return nil
	}
	return m.local.netIso.wrap(m.local.shell, nil)
}

// Close stops background snapshot builds and waits for them.
func (m *Workspaces) Close() {
	m.cancel()
	m.bg.Wait()
}

// ---- repository sources ----

type repoSource struct {
	kind        string // "git" or "dir"
	gitDir      string
	dir         string
	commit      string // resolved full id (git)
	ident       string // stable identity of the repository
	fingerprint string // content fingerprint (dir)
}

func (m *Workspaces) resolveSource(ctx context.Context, task rl.Task) (*repoSource, error) {
	var pathErr error
	if p := task.Repo.Path; p != "" {
		abs := p
		if !filepath.IsAbs(abs) {
			base := m.opts.RepoBase
			if base == "" {
				base, _ = os.Getwd()
			}
			abs = filepath.Join(base, p)
		}
		abs = filepath.Clean(abs)
		fi, err := os.Stat(abs)
		switch {
		case err == nil && fi.IsDir():
			real, rerr := filepath.EvalSymlinks(abs)
			if rerr != nil {
				real = abs
			}
			if gd, ok := m.git.GitDirOf(ctx, real); ok {
				sha, err := m.git.ResolveCommit(ctx, gd, task.Repo.Commit)
				if err != nil {
					return nil, Infra("resolve commit", fmt.Errorf("repo %s: commit %q: %w", p, task.Repo.Commit, err))
				}
				return &repoSource{kind: "git", gitDir: gd, commit: sha, ident: real}, nil
			}
			fp, err := dirFingerprint(ctx, real)
			if err != nil {
				return nil, Infra("fingerprint "+p, err)
			}
			return &repoSource{kind: "dir", dir: real, ident: real, fingerprint: fp}, nil
		case err == nil:
			pathErr = fmt.Errorf("repo path %q is not a directory", p)
		default:
			pathErr = fmt.Errorf("repo path %q: %w", p, err)
		}
		if task.Repo.URL == "" {
			return nil, Infra("resolve repo", pathErr)
		}
	}
	gd, err := m.ensureMirror(ctx, task.Repo.URL, task.Repo.Commit)
	if err != nil {
		return nil, Infra("mirror "+task.Repo.URL, err)
	}
	sha, err := m.git.ResolveCommit(ctx, gd, task.Repo.Commit)
	if err != nil {
		return nil, Infra("resolve commit", fmt.Errorf("repo %s: commit %q: %w", task.Repo.URL, task.Repo.Commit, err))
	}
	return &repoSource{kind: "git", gitDir: gd, commit: sha, ident: normalizeRepoURL(task.Repo.URL)}, nil
}

// ensureMirror maintains one bare mirror per URL under Root, so a thousand tasks
// on one repository clone it once.
func (m *Workspaces) ensureMirror(ctx context.Context, url, commit string) (string, error) {
	dir := filepath.Join(m.root, "repos", shortHash("mirror", url)+".git")
	m.mu.Lock()
	lock := m.mirrors[dir]
	if lock == nil {
		lock = &sync.Mutex{}
		m.mirrors[dir] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if _, err := os.Stat(dir); err == nil {
		if _, err := m.git.ResolveCommit(ctx, dir, commit); err == nil {
			return dir, nil
		}
		if _, err := m.git.Run(ctx, "", nil, "--git-dir="+dir, "remote", "update", "--prune"); err != nil {
			return "", err
		}
		return dir, nil
	}
	tmp := dir + ".tmp-" + randHex(4)
	if _, err := m.git.Run(ctx, "", nil, "clone", "--mirror", "--quiet", "--", url, tmp); err != nil {
		_ = removeAllNoFollow(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = removeAllNoFollow(tmp)
		return "", err
	}
	return dir, nil
}

// dirFingerprint identifies the content of a plain directory by names, modes,
// sizes and modification times, so editing the directory makes a new snapshot.
func dirFingerprint(ctx context.Context, dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashHex(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---- snapshot cache ----

func (m *Workspaces) snapshotKey(task rl.Task, src *repoSource) string {
	id := src.commit
	if src.kind == "dir" {
		id = src.fingerprint
	}
	base := hashHex("snapshot/v1", m.opts.Mode, src.kind, src.ident, id, task.Repo.Subdir, strings.Join(task.Setup, "\x00"))[:32]
	m.mu.Lock()
	g := m.gen[base]
	m.mu.Unlock()
	if g > 0 {
		return fmt.Sprintf("%s-g%d", base, g)
	}
	return base
}

// snapshotFor returns the (possibly cached) starting state for a task, building
// it if needed. Concurrent callers for the same snapshot share one build.
func (m *Workspaces) snapshotFor(ctx context.Context, task rl.Task) (*snapshot, error) {
	src, err := m.resolveSource(ctx, task)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		key := m.snapshotKey(task, src)
		snap, err := m.getOrBuild(ctx, key, task, src)
		if err != nil {
			return nil, err
		}
		if err := m.checkSnapshot(ctx, snap); err != nil {
			if errors.Is(err, errSnapshotTampered) && attempt == 0 {
				// Leave the damaged copy in place (running rollouts still read it) and
				// build a fresh generation next to it.
				base := strings.SplitN(key, "-g", 2)[0]
				m.warn("cached snapshot %s was modified after it was built; rebuilding", base[:12])
				m.mu.Lock()
				m.gen[base]++
				delete(m.loaded, key)
				m.mu.Unlock()
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, Infra("snapshot check", err)
		}
		return snap, nil
	}
}

func (m *Workspaces) getOrBuild(ctx context.Context, key string, task rl.Task, src *repoSource) (*snapshot, error) {
	m.mu.Lock()
	if s, ok := m.loaded[key]; ok {
		m.mu.Unlock()
		return s, nil
	}
	if f, ok := m.failures[key]; ok {
		if time.Now().Before(f.until) {
			m.mu.Unlock()
			return nil, f.err
		}
		delete(m.failures, key)
	}
	fl, running := m.flights[key]
	if !running {
		fl = &flight{done: make(chan struct{})}
		m.flights[key] = fl
		m.bg.Add(1)
		go func() {
			defer m.bg.Done()
			s, err := m.loadOrBuild(m.baseCtx, key, task, src)
			m.mu.Lock()
			delete(m.flights, key)
			if err == nil {
				m.loaded[key] = s
			} else if m.opts.FailureTTL > 0 && m.baseCtx.Err() == nil {
				m.failures[key] = failure{err: err, until: time.Now().Add(m.opts.FailureTTL)}
			}
			m.mu.Unlock()
			fl.snap, fl.err = s, err
			close(fl.done)
		}()
	}
	m.mu.Unlock()
	select {
	case <-fl.done:
		return fl.snap, fl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *Workspaces) loadOrBuild(ctx context.Context, key string, task rl.Task, src *repoSource) (*snapshot, error) {
	if s, err := m.loadSnapshot(key); err == nil {
		if fi, serr := os.Stat(s.tree); serr == nil && fi.IsDir() {
			return s, nil
		}
	}
	return m.buildSnapshot(ctx, task, src, key)
}

// ---- workspaces ----

// Workspace is one rollout's private working tree.
type Workspace struct {
	ID string
	// Dir is the directory the agent works in: the tree root, or its Repo.Subdir.
	Dir string
	// Root is the tree root (the repository top level).
	Root string
	// Home and Tmp are the HOME and TMPDIR of the agent's commands.
	Home, Tmp string
	// BaseTree is the git tree id of the starting state; the agent's diff is
	// computed against it.
	BaseTree string
	// Commit is the resolved source commit ("" for a plain directory).
	Commit string
	// Warnings are host degradations that applied to this workspace.
	Warnings []string

	mgr    *Workspaces
	task   rl.Task
	snap   *snapshot
	dir    string // the directory removed by Cleanup ("" for attached trees)
	marker string
	once   sync.Once
}

var labelRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (m *Workspaces) newID(task, label string) string {
	name := labelRe.ReplaceAllString(task, "_")
	if label != "" {
		name += "-" + labelRe.ReplaceAllString(label, "_")
	}
	if len(name) > 80 {
		name = name[:80]
	}
	return name + "-" + randHex(6)
}

// Prepare creates a fresh workspace for task: a copy-on-write clone of the cached
// snapshot (building it first if this is the first use, which runs the task's
// setup once), a private home and temp directory. label distinguishes rollouts
// in directory names ("s3").
func (m *Workspaces) Prepare(ctx context.Context, task rl.Task, label string) (*Workspace, error) {
	if err := ValidateTask(task); err != nil {
		return nil, Infra("prepare", err)
	}
	snap, err := m.snapshotFor(ctx, task)
	if err != nil {
		return nil, err
	}
	id := m.newID(task.ID, label)
	dir := filepath.Join(m.root, "ws", id)
	co, err := m.cloneSnapshot(ctx, snap, dir, task)
	if err != nil {
		return nil, err
	}
	w := &Workspace{
		ID: id, Dir: co.dirIn, Root: co.tree, Home: co.home, Tmp: co.tmp,
		BaseTree: snap.meta.BaseTree, Commit: snap.meta.Commit, Warnings: m.Warnings(),
		mgr: m, task: task, snap: snap, dir: dir, marker: id,
	}
	return w, nil
}

// Attach wraps an existing directory (a workspace some other process made, or a
// tree restored from a diff) so it can be diffed and verified against the
// snapshot of task. Cleanup does nothing for an attached workspace.
func (m *Workspaces) Attach(ctx context.Context, task rl.Task, dir string) (*Workspace, error) {
	snap, err := m.snapshotFor(ctx, task)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	in := root
	if task.Repo.Subdir != "" {
		in = filepath.Join(root, filepath.FromSlash(task.Repo.Subdir))
	}
	return &Workspace{
		ID: "attached", Dir: in, Root: root, BaseTree: snap.meta.BaseTree, Commit: snap.meta.Commit,
		mgr: m, task: task, snap: snap, Warnings: m.Warnings(),
	}, nil
}

type checkout struct {
	dir, tree, dirIn, home, tmp string
	id                          string
}

// cloneSnapshot copies the snapshot's tree and home into dir.
func (m *Workspaces) cloneSnapshot(ctx context.Context, snap *snapshot, dir string, task rl.Task) (*checkout, error) {
	co := &checkout{dir: dir, tree: filepath.Join(dir, "tree"), home: filepath.Join(dir, "home"), tmp: filepath.Join(dir, "tmp"), id: filepath.Base(dir)}
	ok := false
	defer func() {
		if !ok {
			_ = removeAllNoFollow(dir)
		}
	}()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, Infra("workspace", err)
	}
	if err := m.cl.clone(ctx, snap.tree, co.tree); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, Infra("clone snapshot", err)
	}
	if err := m.cl.clone(ctx, snap.home, co.home); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, Infra("clone tool home", err)
	}
	if err := os.MkdirAll(co.tmp, 0o700); err != nil {
		return nil, Infra("workspace", err)
	}
	co.dirIn = co.tree
	if task.Repo.Subdir != "" {
		co.dirIn = filepath.Join(co.tree, filepath.FromSlash(task.Repo.Subdir))
	}
	ok = true
	return co, nil
}

// newCheckout makes a clean checkout for verification: a clone of the pristine
// snapshot, which no agent ever touched.
func (m *Workspaces) newCheckout(ctx context.Context, snap *snapshot, task rl.Task, label string) (*checkout, error) {
	id := m.newID(task.ID, "v"+label)
	return m.cloneSnapshot(ctx, snap, filepath.Join(m.root, "verify", id), task)
}

// Env returns the scrubbed environment the agent's commands should run with.
func (w *Workspace) Env() []string {
	return w.mgr.commandEnv(w.Home, w.Tmp, w.task.Network, w.marker)
}

func (m *Workspaces) commandEnv(home, tmp string, network bool, marker string) []string {
	return BuildEnv(EnvSpec{
		Home: home, Tmp: tmp, Network: network, Marker: marker, Base: m.baseEnv,
		Deny: []string{m.root}, PassEnv: m.opts.PassEnv, Set: m.opts.SetEnv,
	})
}

// Task returns the task the workspace was made for.
func (w *Workspace) Task() rl.Task { return w.task }

// Manager returns the owning manager.
func (w *Workspace) Manager() *Workspaces { return w.mgr }

// SnapshotKey identifies the cached snapshot the workspace started from.
func (w *Workspace) SnapshotKey() string { return w.snap.key }

// Cleanup kills whatever the workspace left running and removes it. It never
// follows a symlink out of the workspace, is idempotent and safe to call from
// several goroutines. The directory is only removed if it lies under the
// manager's workspace area.
func (w *Workspace) Cleanup() error {
	var err error
	w.once.Do(func() {
		if w.dir == "" {
			return
		}
		if w.marker != "" {
			sweepMarker(w.marker)
		}
		wsRoot := filepath.Join(w.mgr.root, "ws") + string(filepath.Separator)
		if !strings.HasPrefix(w.dir, wsRoot) {
			err = fmt.Errorf("refusing to remove %s: not inside %s", w.dir, wsRoot)
			return
		}
		err = removeAllNoFollow(w.dir)
	})
	return err
}

// removeCheckout deletes a checkout directory under Root/verify or Root/ws.
func (m *Workspaces) removeCheckout(co *checkout) {
	if co == nil || co.dir == "" {
		return
	}
	sweepMarker(co.id)
	for _, area := range []string{"verify", "ws"} {
		if strings.HasPrefix(co.dir, filepath.Join(m.root, area)+string(filepath.Separator)) {
			_ = removeAllNoFollow(co.dir)
			return
		}
	}
}

// PruneStale removes rollout workspaces, verification checkouts and abandoned
// snapshot builds older than maxAge: leftovers of crashed runs. Cached
// snapshots and mirrors are kept. It must not be used while rollouts are
// running with a maxAge shorter than the longest of them.
func (m *Workspaces) PruneStale(maxAge time.Duration) (removed int, err error) {
	cutoff := time.Now().Add(-maxAge)
	for _, area := range []struct{ dir, prefix string }{{"ws", ""}, {"verify", ""}, {"snaps", ".tmp-"}, {"tmp", ""}} {
		ents, rerr := os.ReadDir(filepath.Join(m.root, area.dir))
		if rerr != nil {
			continue
		}
		for _, e := range ents {
			if area.prefix != "" && !strings.HasPrefix(e.Name(), area.prefix) {
				continue
			}
			fi, ierr := e.Info()
			if ierr != nil || fi.ModTime().After(cutoff) {
				continue
			}
			if rerr := removeAllNoFollow(filepath.Join(m.root, area.dir, e.Name())); rerr != nil {
				err = errors.Join(err, rerr)
				continue
			}
			removed++
		}
	}
	return removed, err
}

// ---- diffs ----

// Diff is an agent's change relative to the starting state.
type Diff struct {
	// Patch is the raw diff of the whole tree (binary-safe, no renames),
	// including changes to protected paths; it is what diff.patch stores.
	Patch []byte
	// Files describes each file section of Patch.
	Files []PatchFile
	// BaseTree and Tree are the git tree ids compared.
	BaseTree, Tree string
	// Skipped lists paths that could not be represented (special files, nested
	// repositories, unreadable or oversized files).
	Skipped []SkippedPath
}

// DefaultMaxDiffBytes bounds the patch an agent may produce.
const DefaultMaxDiffBytes = 64 << 20

// Diff computes the agent's change: the tree's files are hashed into a git tree
// and compared with the baseline. Problems with the agent's content (too many
// or too large files, a patch over maxBytes) are returned as an error for which
// IsAgentLimit is true; everything else is infrastructure.
func (w *Workspace) Diff(ctx context.Context, maxBytes int64) (*Diff, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDiffBytes
	}
	m := w.mgr
	scratch, err := os.MkdirTemp(m.tmp, "diff-")
	if err != nil {
		return nil, Infra("diff", err)
	}
	defer func() { _ = removeAllNoFollow(scratch) }()
	gd := filepath.Join(scratch, "git")
	if err := m.git.initBare(ctx, gd); err != nil {
		return nil, Infra("diff", err)
	}
	if err := os.WriteFile(filepath.Join(gd, "objects", "info", "alternates"), []byte(w.snap.altObjects()+"\n"), 0o644); err != nil {
		return nil, Infra("diff", err)
	}
	hr, err := m.hashTree(ctx, hashOpts{
		GitDir: gd, AltObjects: w.snap.altObjects(), Work: w.Root, Base: w.BaseTree, Write: true,
		MaxFiles: m.opts.MaxFiles, MaxFileBytes: m.opts.MaxFileBytes,
	})
	if err != nil {
		if IsAgentLimit(err) || ctx.Err() != nil {
			return nil, err
		}
		return nil, Infra("hash workspace", err)
	}
	patch, err := m.diffTrees(ctx, gd, w.BaseTree, hr.Tree, maxBytes)
	if err != nil {
		if IsAgentLimit(err) || ctx.Err() != nil {
			return nil, err
		}
		return nil, Infra("diff trees", err)
	}
	files, err := m.parsePatch(ctx, patch)
	if err != nil {
		return nil, Infra("parse diff", err)
	}
	return &Diff{Patch: patch, Files: files, BaseTree: w.BaseTree, Tree: hr.Tree, Skipped: hr.Skipped}, nil
}

// IsAgentLimit reports whether err says the agent's work product exceeded a
// resource limit (it is not an infrastructure failure).
func IsAgentLimit(err error) bool {
	var le *limitError
	return errors.As(err, &le)
}
