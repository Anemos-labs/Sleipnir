package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A stale tree whose private git directory has been made to name another repository's objects and refs is kept: salvage would
// commit onto that repository's branch.
func TestPruneKeepsATreeWhoseGitDirSharesAnotherRepository(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	w := mustCreate(t, m, "a")
	edit(t, w, "wip.txt", "wip\n")
	orphan(t, w)

	other := newRepo(t)
	b, err := os.ReadFile(filepath.Join(w.Path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	admin := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(w.Path, admin)
	}
	if err := os.WriteFile(filepath.Join(admin, "commondir"), []byte(filepath.Join(other, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	rep, err := fresh.Prune(tctx(t), PruneOptions{Salvage: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Kept) != 1 || !strings.Contains(rep.Kept[0].Reason, "does not share") {
		t.Fatalf("Kept: %+v Removed: %+v", rep.Kept, rep.Removed)
	}
	if !exists(filepath.Join(w.Path, "wip.txt")) {
		t.Fatal("the tree was deleted")
	}
}
