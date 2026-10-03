package workspace

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestReleaseRetainsDirtyWorkAndAllowsAdoption(t *testing.T) {
	repo := openRepo(t, newRepo(t))
	m := newManager(t, repo)
	w := mustCreate(t, m, "worker")
	edit(t, w, "README.md", "unfinished work\n")
	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: m.Prefix}
	if _, err := fresh.Create(tctx(t), "worker", CreateOptions{Reuse: true}); !errors.Is(err, ErrExists) {
		t.Fatalf("live adoption: %v", err)
	}
	if err := w.Release(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(tctx(t), "should fail"); !errors.Is(err, ErrRemoved) {
		t.Fatalf("released handle: %v", err)
	}
	adopted, err := fresh.Create(tctx(t), "worker", CreateOptions{Reuse: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(adopted.Path, "README.md")); got != "unfinished work\n" {
		t.Fatalf("lost work: %q", got)
	}
	if adopted.Base != w.Base || adopted.Branch != w.Branch {
		t.Fatal("adoption changed identity")
	}
}

func TestPrunePreservesRecoveryNamespaces(t *testing.T) {
	repo := openRepo(t, newRepo(t))
	m := newManager(t, repo)
	w := mustCreate(t, m, "worker")
	edit(t, w, "README.md", "unfinished work\n")
	if err := repo.CreateBranch(tctx(t), m.Prefix+"/_resume", w.Base); err != nil {
		t.Fatal(err)
	}
	if err := w.Release(tctx(t)); err != nil {
		t.Fatal(err)
	}
	sweep := &Manager{Repo: repo, Dir: m.Dir, Prefix: m.Prefix}
	report, err := sweep.Prune(tctx(t), PruneOptions{Salvage: true, KeepPrefixes: []string{m.Prefix}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Removed)+len(report.BranchesRemoved) != 0 {
		t.Fatalf("removed recoverable state: %+v", report)
	}
	if got := readFile(t, filepath.Join(w.Path, "README.md")); got != "unfinished work\n" {
		t.Fatalf("work changed: %q", got)
	}
	if _, err := repo.BranchSHA(tctx(t), m.Prefix+"/_resume"); err != nil {
		t.Fatal(err)
	}
	if keptPrefix(m.Prefix+"-other/worker", []string{m.Prefix}) {
		t.Fatal("prefix matched unrelated namespace")
	}
}

func TestPruneRequiresAllOwnersInactiveBeforeRemovingAnything(t *testing.T) {
	repo := openRepo(t, newRepo(t))
	m := newManager(t, repo)
	stopped := mustCreate(t, m, "a-stopped")
	live := mustCreate(t, m, "z-live")
	edit(t, stopped, "README.md", "unfinished work\n")
	if err := stopped.Release(tctx(t)); err != nil {
		t.Fatal(err)
	}
	pin := m.Prefix + "/_resume"
	if err := repo.CreateBranch(tctx(t), pin, live.Base); err != nil {
		t.Fatal(err)
	}
	report, err := m.Prune(tctx(t), PruneOptions{Salvage: true, RequireInactive: true})
	if err != nil || report.Live != 1 || len(report.Removed)+len(report.BranchesRemoved) != 0 {
		t.Fatalf("pruned a namespace with a live owner: %+v, %v", report, err)
	}
	if got := readFile(t, filepath.Join(stopped.Path, "README.md")); got != "unfinished work\n" {
		t.Fatalf("stopped worker changed: %q", got)
	}
	if _, err := repo.BranchSHA(tctx(t), pin); err != nil {
		t.Fatal(err)
	}
}
