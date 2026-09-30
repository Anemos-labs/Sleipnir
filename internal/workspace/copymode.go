package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

// Copy mode in one paragraph. A directory that is not a git repository still has
// to give every agent an isolated tree whose changes can be listed, diffed,
// rewound and merged. Reimplementing that on plain files would duplicate git; so
// the directory is snapshotted once into a private repository under Dir
// ("_shadow": its work tree is the pristine snapshot, never edited), and each
// agent's tree is a linked worktree of that repository whose files were
// populated by a copy-on-write clone of the pristine tree instead of a checkout.
// From there on nothing distinguishes it from worktree mode: Diff, Changed,
// Commit, Reset, the merge queue and Finish are the same code.

const shadowMarkerFile = "sleipnir-shadow.json"

// copyTimeout bounds one snapshot or one clone of the snapshot.
const copyTimeout = 30 * time.Minute

type shadowMarker struct {
	V       int       `json:"v"`
	Source  string    `json:"source"`
	Base    string    `json:"base"`
	Created time.Time `json:"created"`
}

// deriveCopy prepares the private repository and its base commit.
func (m *Manager) deriveCopy(ctx context.Context, st mstate) (mstate, error) {
	// A snapshot of a huge directory takes a while, but never forever.
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	src := m.Source
	if src == "" && m.Repo != nil {
		src = m.Repo.Root()
	}
	if src == "" {
		return st, fmt.Errorf("%w: copy mode needs Manager.Source (or Repo)", ErrBadName)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return st, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return st, fmt.Errorf("workspace: source %s: %w", src, err)
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return st, fmt.Errorf("workspace: source %s is not a directory", src)
	}
	st.source = real
	shadow := filepath.Join(st.dirReal, shadowName)
	st.pristine = shadow

	gitOpts := m.Repo // may be nil
	initRepo := func() (*gitx.Repo, error) {
		if gitOpts != nil {
			return gitOpts.Init(ctx, shadow, false)
		}
		return gitx.Init(ctx, shadow, false)
	}

	if fi, err := os.Lstat(shadow); err == nil {
		// A shadow from an earlier run: reuse only if it is ours, for this source.
		if !fi.IsDir() {
			return st, fmt.Errorf("%w: %s", ErrExists, shadow)
		}
		repo, err := m.reopenShadow(ctx, shadow)
		if err != nil {
			return st, err
		}
		data, err := os.ReadFile(filepath.Join(repo.GitDir(), shadowMarkerFile))
		if err != nil {
			return st, fmt.Errorf("%w: %s exists but was not created by a workspace manager", ErrForeign, shadow)
		}
		var sm shadowMarker
		if json.Unmarshal(data, &sm) != nil || sm.V != 1 || sm.Source != real || sm.Base == "" {
			return st, fmt.Errorf("%w: %s belongs to another source directory (%s)", ErrExists, shadow, sm.Source)
		}
		if _, err := repo.ResolveRef(ctx, sm.Base); err != nil {
			return st, fmt.Errorf("workspace: shadow repository lost its base commit: %w", err)
		}
		st.base, st.baseSHA, st.lock = repo, sm.Base, lockFor(repo.CommonDir())
		return st, nil
	} else if !os.IsNotExist(err) {
		return st, err
	}

	repo, err := initRepo()
	if err != nil {
		return st, err
	}
	excl := append(append([]string(nil), DefaultCopyExcludes...), m.Excludes...)
	var skip []string
	if strictlyUnder(real, st.dirReal) {
		skip = append(skip, st.dirReal) // the workspace lives inside the source
	}
	if err := cloneTree(ctx, real, shadow, cloneOpts{skipTop: map[string]bool{".git": true}, excludes: excl, skipAbs: skip}); err != nil {
		_ = os.RemoveAll(shadow) // ours: we just created it
		return st, fmt.Errorf("workspace: snapshot of %s failed: %w", real, err)
	}
	sha, err := repo.CommitAllowEmpty(ctx, "sleipnir: snapshot of "+filepath.Base(real), gitx.Author{Name: "Sleipnir", Email: "sleipnir@localhost", When: m.now()})
	if err != nil {
		_ = os.RemoveAll(shadow)
		return st, err
	}
	data, _ := json.Marshal(shadowMarker{V: 1, Source: real, Base: sha, Created: m.now().UTC()})
	if err := os.WriteFile(filepath.Join(repo.GitDir(), shadowMarkerFile), data, 0o600); err != nil {
		return st, err
	}
	st.base, st.baseSHA, st.lock = repo, sha, lockFor(repo.CommonDir())
	return st, nil
}

func (m *Manager) reopenShadow(ctx context.Context, shadow string) (*gitx.Repo, error) {
	if m.Repo != nil {
		return m.Repo.Reopen(ctx, shadow)
	}
	return gitx.Open(shadow)
}

// populateCopy fills a registered worktree by cloning the pristine snapshot and
// adopting its index (with file metadata refreshed), so git sees the tree as
// checked out and unmodified without hashing a single file.
func (m *Manager) populateCopy(ctx context.Context, t *Tree) error {
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	if err := cloneTree(ctx, m.st.pristine, t.Path, cloneOpts{skipTop: map[string]bool{".git": true}}); err != nil {
		return fmt.Errorf("workspace: cloning the snapshot for %s: %w", t.Agent, err)
	}
	if err := copyIndexFile(filepath.Join(m.st.base.GitDir(), "index"), filepath.Join(t.repo.GitDir(), "index")); err != nil {
		return fmt.Errorf("workspace: adopting the snapshot index: %w", err)
	}
	return t.repo.RefreshIndex(ctx)
}

// Close removes every tree this manager created (refusing, like Remove, trees
// with unmerged work unless force), and in copy mode the private snapshot
// repository. The merge queue's tree is closed by Queue.Close.
func (m *Manager) Close(ctx context.Context, force bool) error {
	if err := m.ensure(ctx); err != nil {
		return err
	}
	var errs []error
	for _, t := range m.Trees() {
		if err := t.Remove(ctx, force); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if m.st.mode == ModeCopy && force {
		if err := m.removeShadow(ctx); err != nil {
			return err
		}
	}
	return nil
}

// removeShadow deletes the private snapshot repository, after verifying it is
// ours.
func (m *Manager) removeShadow(ctx context.Context) error {
	shadow := m.st.pristine
	if !strictlyUnder(m.st.dirReal, shadow) || filepath.Base(shadow) != shadowName {
		return fmt.Errorf("%w: %s", ErrOutsideDir, shadow)
	}
	if fi, err := os.Lstat(shadow); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(shadow, ".git", shadowMarkerFile)); err != nil {
		return fmt.Errorf("%w: %s has no shadow marker", ErrForeign, shadow)
	}
	if entries := scanAdminDirs(m.st.base.CommonDir()); len(entries) > 0 {
		return fmt.Errorf("%w: %d tree(s) still registered in %s", ErrDirty, len(entries), shadow)
	}
	return os.RemoveAll(shadow)
}
