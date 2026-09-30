package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

// halfCreate leaves exactly what a process that died between "git worktree add"
// and the writing of our marker leaves: a registered, empty tree and its branch.
func halfCreate(t *testing.T, m *Manager, repo *gitx.Repo, agent string) string {
	t.Helper()
	dest := filepath.Join(m.TreesDir(), agent)
	err := repo.WorktreeAdd(tctx(t), gitx.WorktreeAddOptions{Path: dest, Branch: m.Prefix + "/" + agent, Commit: m.BaseSHA(), NoCheckout: true})
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func backdate(t *testing.T, path string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func listedWorktrees(t *testing.T, repo *gitx.Repo) string {
	t.Helper()
	wts, err := repo.Worktrees(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, w := range wts {
		paths = append(paths, w.Path)
	}
	return strings.Join(paths, "\n")
}

// A registration that never got its marker belongs to nobody the ordinary way
// (Remove and Prune need the marker), so without help its name would be taken for
// good. Once it has been abandoned long enough, Create clears it.
func TestCreateClearsARegistrationThatNeverGotItsMarker(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	mustCreate(t, m, "warmup") // initializes the manager and the trees directory
	dest := halfCreate(t, m, repo, "a")

	// A young one might be the creation of another process that is just about to
	// write its marker: it is left alone.
	if _, err := m.Create(tctx(t), "a", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("a young half-created tree must block the name, got %v", err)
	}
	if !exists(dest) {
		t.Fatal("a young half-created tree was removed")
	}

	backdate(t, filepath.Join(dest, ".git"), time.Hour)
	tree, err := m.Create(tctx(t), "a", CreateOptions{})
	if err != nil {
		t.Fatalf("Create over an abandoned half-created tree: %v", err)
	}
	if !exists(filepath.Join(tree.Path, "README.md")) || tree.Branch != "sleipnir/s1/a" {
		t.Fatalf("the new tree is not a proper checkout: %+v", tree)
	}
	if n := strings.Count(listedWorktrees(t, repo), dest); n != 1 {
		t.Fatalf("the tree is registered %d times:\n%s", n, listedWorktrees(t, repo))
	}
	if err := tree.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove of the recreated tree: %v", err)
	}
}

func TestPruneClearsAbandonedHalfCreatedTreesAndNothingElse(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	live := mustCreate(t, m, "live")
	old := halfCreate(t, m, repo, "old")
	young := halfCreate(t, m, repo, "young")
	backdate(t, filepath.Join(old, ".git"), time.Hour)

	// Lookalikes that are not registrations of this repository: a directory with a
	// .git file that points elsewhere, and one whose .git is a directory.
	stranger := filepath.Join(m.TreesDir(), "stranger")
	if err := os.MkdirAll(stranger, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stranger, ".git"), "gitdir: /nonexistent/elsewhere\n")
	backdate(t, filepath.Join(stranger, ".git"), time.Hour)
	other := filepath.Join(m.TreesDir(), "other")
	must(t, os.MkdirAll(filepath.Join(other, ".git"), 0o755))

	rep, err := m.Prune(tctx(t), PruneOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].Path != old || !strings.Contains(rep.Removed[0].Reason, "half-created") {
		t.Fatalf("dry run: %+v", rep)
	}
	if !exists(old) {
		t.Fatal("a dry run removed something")
	}

	rep, err = m.Prune(tctx(t), PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].Path != old {
		t.Fatalf("Prune: %+v", rep)
	}
	if exists(old) || strings.Contains(listedWorktrees(t, repo), old) {
		t.Fatal("the abandoned half-created tree is still there")
	}
	if _, err := repo.BranchSHA(tctx(t), "sleipnir/s1/old"); !errors.Is(err, gitx.ErrNotFound) {
		t.Fatalf("its branch should have been cleaned up too: %v", err)
	}
	for _, p := range []string{young, stranger, other, live.Path} {
		if !exists(p) {
			t.Fatalf("Prune removed %s", p)
		}
	}
	if _, err := repo.BranchSHA(tctx(t), "sleipnir/s1/young"); err != nil {
		t.Fatalf("the young half-created tree lost its branch: %v", err)
	}
	if got := listedWorktrees(t, repo); !strings.Contains(got, live.Path) || !strings.Contains(got, young) {
		t.Fatalf("worktrees after Prune:\n%s", got)
	}
}

// A "git worktree add" that is cancelled around the moment it finishes leaves a
// registration (and a branch) behind; the caller's Create must clean that up
// itself, at once, so the same agent can be created again.
func TestCancelingCreateDoesNotStrandTheAgentName(t *testing.T) {
	// the shim performs the add, then hangs: the cancellation lands after git is done
	shim, ctl := gitShim(t, `
case " $* " in
  *" worktree add "*)
    if [ -f "$D/armed" ]; then
      rm -f "$D/armed"
      "$REAL" "$@" || exit $?
      echo ready > "$D/ready"
      sleep 30
      exit 0
    fi;;
esac`)
	dir := newRepo(t)
	repo := openRepo(t, dir, gitx.WithGitPath(shim))
	m := newManager(t, repo)
	mustCreate(t, m, "warmup")
	if err := os.WriteFile(filepath.Join(ctl, "armed"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && !exists(filepath.Join(ctl, "ready")) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := m.Create(ctx, "c", CreateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create: want the cancellation, got %v", err)
	}
	dest := filepath.Join(m.TreesDir(), "c")
	if exists(dest) || strings.Contains(listedWorktrees(t, repo), dest) {
		t.Fatalf("the interrupted creation left its tree behind:\n%s", listedWorktrees(t, repo))
	}
	if _, err := repo.BranchSHA(tctx(t), "sleipnir/s1/c"); !errors.Is(err, gitx.ErrNotFound) {
		t.Fatalf("the interrupted creation left its branch behind: %v", err)
	}
	if _, err := m.Create(tctx(t), "c", CreateOptions{}); err != nil {
		t.Fatalf("Create after the interrupted one: %v", err)
	}
}
