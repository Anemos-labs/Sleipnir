package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

func TestRemoveCleanAndMerged(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	log := &eventLog{}
	m.OnEvent = log.fn()
	a := mustCreate(t, m, "a")
	if err := a.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove of an untouched tree: %v", err)
	}
	if exists(a.Path) {
		t.Fatal("directory still there")
	}
	if br, _ := repo.Branches(tctx(t), "sleipnir/s1/"); len(br) != 0 {
		t.Fatalf("branch survived: %+v", br)
	}
	if wts, _ := repo.Worktrees(tctx(t)); len(wts) != 1 {
		t.Fatalf("worktree entry survived: %+v", wts)
	}
	if err := a.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove is idempotent: %v", err)
	}
	if _, err := a.Changed(tctx(t)); !errors.Is(err, ErrRemoved) {
		t.Fatalf("use after Remove: %v", err)
	}
	if _, err := a.Commit(tctx(t), "x"); !errors.Is(err, ErrRemoved) {
		t.Fatalf("Commit after Remove: %v", err)
	}
	if _, ok := m.Get("a"); ok {
		t.Fatal("removed tree still registered")
	}
	if log.count(EventRemove) != 1 {
		t.Fatalf("events: %v", log.types())
	}
	// The same agent id can be created again afterwards.
	if b := mustCreate(t, m, "a"); b.Path != a.Path {
		t.Fatal("re-create")
	}

	// A tree whose work was merged by the queue is removable without force.
	c := mustCreate(t, m, "c")
	edit(t, c, "README.md", "# merged work\n")
	q := mustQueue(t, m, QueueOptions{})
	if r := mustSubmit(t, q, Submission{Tree: c}); !r.Merged() {
		t.Fatalf("submit: %+v", r)
	}
	if err := c.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove of a merged tree: %v", err)
	}
	if br, _ := repo.Branches(tctx(t), "sleipnir/s1/c"); len(br) != 0 {
		t.Fatalf("merged tree's branch survived: %+v", br)
	}
	// ... and the integration branch still has the work
	if got, _ := repo.Show(tctx(t), q.Branch(), "README.md"); string(got) != "# merged work\n" {
		t.Fatalf("integration branch lost the work: %q", got)
	}
}

func TestRemoveRefusesToLoseWork(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)

	dirty := mustCreate(t, m, "dirty")
	edit(t, dirty, "README.md", "# uncommitted\n")
	if err := dirty.Remove(tctx(t), false); !errors.Is(err, ErrDirty) {
		t.Fatalf("dirty: %v", err)
	}
	if !exists(dirty.Path) || readFile(t, filepath.Join(dirty.Path, "README.md")) != "# uncommitted\n" {
		t.Fatal("a refused Remove changed the tree")
	}
	// untracked files count as work
	untracked := mustCreate(t, m, "untracked")
	edit(t, untracked, "new.txt", "n\n")
	if err := untracked.Remove(tctx(t), false); !errors.Is(err, ErrDirty) {
		t.Fatalf("untracked: %v", err)
	}
	// committed but not merged anywhere
	work := mustCreate(t, m, "work")
	edit(t, work, "README.md", "# committed\n")
	tip, err := work.Commit(tctx(t), "committed work")
	if err != nil || tip == "" {
		t.Fatal(err)
	}
	if err := work.Remove(tctx(t), false); !errors.Is(err, ErrUnmerged) {
		t.Fatalf("unmerged: %v", err)
	}
	if !exists(work.Path) {
		t.Fatal("directory removed despite the refusal")
	}
	if sha, _ := repo.BranchSHA(tctx(t), work.Branch); sha != tip {
		t.Fatal("branch changed")
	}
	// once the same commits are reachable from a user branch, no force is needed
	rawGit(t, dir, "branch", "keep-it", tip)
	if err := work.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove after the work was saved elsewhere: %v", err)
	}
	// force discards everything
	if err := dirty.Remove(tctx(t), true); err != nil {
		t.Fatal(err)
	}
	if exists(dirty.Path) {
		t.Fatal("force did not remove")
	}
	// remove of an unmerged tree by force deletes the branch too
	w2 := mustCreate(t, m, "w2")
	edit(t, w2, "README.md", "# w2\n")
	if _, err := w2.Commit(tctx(t), "w2"); err != nil {
		t.Fatal(err)
	}
	if err := w2.Remove(tctx(t), true); err != nil {
		t.Fatal(err)
	}
	if br, _ := repo.Branches(tctx(t), "sleipnir/s1/w2"); len(br) != 0 {
		t.Fatal("force left the branch")
	}
}

// TestRemoveOnlyDeletesWhatItCreated exercises every way a Tree value or a
// directory can lie about being ours.
func TestRemoveOnlyDeletesWhatItCreated(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	b := mustCreate(t, m, "b")
	forge := func(agent, path string) *Tree {
		return &Tree{Path: path, Agent: agent, Base: a.Base, m: m, repo: a.repo}
	}

	// 1. a path outside Dir
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "precious.txt"), "precious\n")
	for _, p := range []string{outside, filepath.Dir(m.TreesDir()), m.TreesDir(), dir, "/", "relative/path", filepath.Join(m.TreesDir(), "..", "..")} {
		err := forge("a", p).Remove(tctx(t), true)
		if !errors.Is(err, ErrOutsideDir) {
			t.Errorf("Remove(%q) = %v, want ErrOutsideDir", p, err)
		}
	}
	if !exists(filepath.Join(outside, "precious.txt")) || !exists(dir) || !exists(m.TreesDir()) {
		t.Fatal("something outside the trees was deleted")
	}

	// 2. another agent's tree under our name
	if err := forge("a", b.Path).Remove(tctx(t), true); !errors.Is(err, ErrForeign) {
		t.Fatalf("wrong agent: %v", err)
	}
	if !exists(b.Path) {
		t.Fatal("b was removed through a forged tree")
	}

	// 3. the agent replaced its directory by a symlink to somewhere valuable
	victim := t.TempDir()
	writeFile(t, filepath.Join(victim, "keep.txt"), "keep\n")
	if err := os.RemoveAll(b.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, b.Path); err != nil {
		t.Skip("no symlinks")
	}
	if err := b.Remove(tctx(t), true); !errors.Is(err, ErrOutsideDir) {
		t.Fatalf("symlinked tree: %v", err)
	}
	if !exists(filepath.Join(victim, "keep.txt")) {
		t.Fatal("Remove followed a symlink and deleted its target")
	}
	// ... including a symlinked intermediate component
	linkDir := filepath.Join(m.TreesDir(), "linkdir")
	if err := os.Symlink(victim, linkDir); err != nil {
		t.Fatal(err)
	}
	if err := forge("a", filepath.Join(linkDir, "sub")).Remove(tctx(t), true); err == nil {
		t.Fatal("a path through a symlink was accepted")
	}

	// 4. a directory inside Dir that is not a worktree at all
	junk := filepath.Join(m.TreesDir(), "junk")
	writeFile(t, filepath.Join(junk, "user-data.txt"), "data\n")
	if err := forge("junk", junk).Remove(tctx(t), true); !errors.Is(err, ErrForeign) {
		t.Fatalf("plain directory: %v", err)
	}
	if !exists(filepath.Join(junk, "user-data.txt")) {
		t.Fatal("a plain directory was deleted")
	}

	// 5. a worktree the user made by hand inside Dir: same location, same branch namespace, no marker
	hand := filepath.Join(m.TreesDir(), "hand")
	rawGit(t, dir, "worktree", "add", "-q", "-b", "sleipnir/s1/hand", hand)
	writeFile(t, filepath.Join(hand, "mine.txt"), "mine\n")
	if err := forge("hand", hand).Remove(tctx(t), true); !errors.Is(err, ErrForeign) {
		t.Fatalf("hand-made worktree: %v", err)
	}
	if !exists(filepath.Join(hand, "mine.txt")) {
		t.Fatal("hand-made worktree deleted")
	}
	if _, err := repo.BranchSHA(tctx(t), "sleipnir/s1/hand"); err != nil {
		t.Fatal("hand-made branch deleted")
	}

	// 6. a worktree of ours that a marker of another prefix claims
	other := &Manager{Repo: repo, Dir: filepath.Join(t.TempDir(), "o"), Prefix: "sleipnir/other", Clock: testClock()}
	o := mustCreate(t, other, "o1")
	moved := filepath.Join(m.TreesDir(), "o1")
	// pretend it lives in our directory (tree object lies about its path)
	if err := forge("o1", moved).Remove(tctx(t), true); err == nil {
		t.Fatal("missing directory accepted")
	}
	if !exists(o.Path) {
		t.Fatal("another manager's tree removed")
	}
	// and through the other manager's tree object, using our manager: wrong location
	if err := (&Tree{Path: o.Path, Agent: "o1", m: m, repo: o.repo}).Remove(tctx(t), true); !errors.Is(err, ErrOutsideDir) {
		t.Fatalf("other manager's tree via our manager: %v", err)
	}
	if !exists(o.Path) {
		t.Fatal("another manager's tree was removed")
	}

	// 7. a marker copied into a foreign worktree does not make it ours: the marker
	// must live in that worktree's own administrative directory and name its path
	hand2 := filepath.Join(m.TreesDir(), "hand2")
	rawGit(t, dir, "worktree", "add", "-q", "--detach", hand2)
	mkData, _ := os.ReadFile(filepath.Join(a.repo.GitDir(), markerFile))
	h2, _ := gitx.Open(hand2)
	if err := os.WriteFile(filepath.Join(h2.GitDir(), markerFile), mkData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := forge("a", hand2).Remove(tctx(t), true); !errors.Is(err, ErrForeign) {
		t.Fatalf("copied marker: %v", err)
	}
	if !exists(hand2) {
		t.Fatal("worktree with a copied marker was deleted")
	}

	// the legitimate trees are still removable
	if err := a.Remove(tctx(t), true); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveWhenTheDirectoryVanished(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	if err := os.RemoveAll(a.Path); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove of a tree whose directory is gone: %v", err)
	}
	if wts, _ := repo.Worktrees(tctx(t)); len(wts) != 1 {
		t.Fatalf("registration survived: %+v", wts)
	}
	if br, _ := repo.Branches(tctx(t), "sleipnir/s1/"); len(br) != 0 {
		t.Fatalf("branch survived: %+v", br)
	}
}

// crashedSession builds the aftermath of a dead session: a spread of trees in
// different conditions whose owner process is gone, plus things that are not ours.
type crashedSession struct {
	dir, dirTrees                          string
	repo                                   *gitx.Repo
	m                                      *Manager
	clean, dirty, work, merged, gone, live *Tree
	handmade                               string
	other                                  *Manager
	otherTree                              *Tree
	q                                      *Queue
}

func newCrashedSession(t *testing.T) *crashedSession {
	t.Helper()
	s := &crashedSession{dir: newRepo(t)}
	s.repo = openRepo(t, s.dir)
	s.m = newManager(t, s.repo)
	s.dirTrees = s.m.Dir
	s.clean = mustCreate(t, s.m, "clean-1")
	s.dirty = mustCreate(t, s.m, "dirty-1")
	edit(t, s.dirty, "README.md", "# unsaved work\n")
	edit(t, s.dirty, "notes.txt", "notes\n")
	s.work = mustCreate(t, s.m, "work-1")
	edit(t, s.work, "docs/guide.md", "# committed but never merged\n")
	if _, err := s.work.Commit(tctx(t), "unmerged work"); err != nil {
		t.Fatal(err)
	}
	s.merged = mustCreate(t, s.m, "merged-1")
	edit(t, s.merged, "internal/util/util.go", "package util\n\nfunc Add(a, b int) int { return a + b + 0 }\n")
	s.q = mustQueue(t, s.m, QueueOptions{})
	if r := mustSubmit(t, s.q, Submission{Tree: s.merged}); !r.Merged() {
		t.Fatalf("submit: %+v", r)
	}
	s.gone = mustCreate(t, s.m, "gone-1")
	s.live = mustCreate(t, s.m, "live-1")

	// things that are not ours
	s.handmade = filepath.Join(s.m.TreesDir(), "handmade")
	rawGit(t, s.dir, "worktree", "add", "-q", "-b", "sleipnir/s1/handmade", s.handmade)
	rawGit(t, s.dir, "branch", "feature/user-work", "main")
	s.other = &Manager{Repo: s.repo, Dir: filepath.Join(t.TempDir(), "other"), Prefix: "sleipnir/s2", Clock: testClock()}
	s.otherTree = mustCreate(t, s.other, "o-1")

	// the session "crashes": every owner except live-1 becomes a dead pid
	for _, tr := range []*Tree{s.clean, s.dirty, s.work, s.merged, s.gone, s.q.tree, s.otherTree} {
		orphan(t, tr)
	}
	if err := os.RemoveAll(s.gone.Path); err != nil { // a directory that vanished
		t.Fatal(err)
	}
	return s
}

func actionAgents(as []PruneAction) []string {
	var out []string
	for _, a := range as {
		if a.Agent != "" {
			out = append(out, a.Agent)
		}
	}
	return out
}

func branchNames(as []PruneAction) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Branch)
	}
	return out
}

func TestPruneAfterACrash(t *testing.T) {
	s := newCrashedSession(t)
	ctx := tctx(t)
	// A fresh manager, as a restarted harness would have.
	fresh := &Manager{Repo: s.repo, Dir: s.dirTrees, Prefix: "sleipnir/s1", Clock: testClock()}

	// Dry run first: reports, changes nothing.
	dry, err := fresh.Prune(ctx, PruneOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Removed) == 0 {
		t.Fatalf("dry run found nothing: %+v", dry)
	}
	for _, tr := range []*Tree{s.clean, s.dirty, s.work, s.merged, s.live} {
		if !exists(tr.Path) {
			t.Fatalf("dry run removed %s", tr.Path)
		}
	}

	rep, err := fresh.Prune(ctx, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("removed=%v kept=%v branches removed=%v kept=%v live=%d", actionAgents(rep.Removed), actionAgents(rep.Kept), branchNames(rep.BranchesRemoved), branchNames(rep.BranchesKept), rep.Live)

	removed := strings.Join(actionAgents(rep.Removed), ",")
	for _, want := range []string{"clean-1", "work-1", "merged-1", "gone-1", "_integration"} {
		if !strings.Contains(removed, want) {
			t.Errorf("%s was not pruned (removed: %s)", want, removed)
		}
	}
	// stale trees are gone from disk and from git
	for _, tr := range []*Tree{s.clean, s.work, s.merged, s.gone} {
		if exists(tr.Path) {
			t.Errorf("%s still exists", tr.Path)
		}
	}
	if exists(s.q.tree.Path) {
		t.Error("the dead queue's integration tree still exists")
	}
	// uncommitted work is never destroyed by default
	if !exists(s.dirty.Path) || readFile(t, filepath.Join(s.dirty.Path, "README.md")) != "# unsaved work\n" {
		t.Fatal("a stale tree with uncommitted work was deleted")
	}
	if got := actionAgents(rep.Kept); len(got) != 1 || got[0] != "dirty-1" || !strings.Contains(rep.Kept[0].Reason, "uncommitted") {
		t.Fatalf("Kept = %+v", rep.Kept)
	}
	// branches: those with no unique work go; unmerged work stays
	brs := branchNames(rep.BranchesRemoved)
	for _, want := range []string{"sleipnir/s1/clean-1", "sleipnir/s1/merged-1", "sleipnir/s1/gone-1"} {
		if !contains(brs, want) {
			t.Errorf("branch %s not removed (removed: %v)", want, brs)
		}
	}
	if _, err := s.repo.BranchSHA(ctx, "sleipnir/s1/work-1"); err != nil {
		t.Fatal("the branch holding unmerged commits was deleted")
	}
	if !contains(branchNames(rep.BranchesKept), "sleipnir/s1/work-1") {
		t.Fatalf("BranchesKept = %+v", rep.BranchesKept)
	}
	// the merged work is still on the integration branch (it holds commits nowhere else)
	if _, err := s.repo.BranchSHA(ctx, "sleipnir/s1/_integration"); err != nil {
		t.Fatal("the integration branch, which holds the session's result, was deleted")
	}
	// the live tree is untouched
	if !exists(s.live.Path) || rep.Live != 1 {
		t.Fatalf("live tree: exists=%v live=%d", exists(s.live.Path), rep.Live)
	}
	if _, err := s.repo.BranchSHA(ctx, "sleipnir/s1/live-1"); err != nil {
		t.Fatal("live tree's branch deleted")
	}
	// nothing that is not ours was touched
	if !exists(filepath.Join(s.handmade)) {
		t.Fatal("hand-made worktree removed")
	}
	if _, err := s.repo.BranchSHA(ctx, "sleipnir/s1/handmade"); err != nil {
		t.Fatal("hand-made branch under our prefix deleted (it is attached to a worktree without our marker)")
	}
	if _, err := s.repo.BranchSHA(ctx, "feature/user-work"); err != nil {
		t.Fatal("user's branch deleted")
	}
	if !exists(s.otherTree.Path) {
		t.Fatal("another prefix's tree was pruned")
	}
	if _, err := s.repo.BranchSHA(ctx, "sleipnir/s2/o-1"); err != nil {
		t.Fatal("another prefix's branch deleted")
	}
	// idempotent
	rep2, err := fresh.Prune(ctx, PruneOptions{})
	if err != nil || len(rep2.Removed) != 0 || len(rep2.BranchesRemoved) != 0 {
		t.Fatalf("second Prune: %+v, %v", rep2, err)
	}
	// git's own view is consistent: no stale entries of ours are left
	wts, _ := s.repo.Worktrees(ctx)
	for _, w := range wts {
		if w.Prunable {
			t.Errorf("prunable entry left: %+v", w)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestPruneSalvageForceAndAge(t *testing.T) {
	s := newCrashedSession(t)
	ctx := tctx(t)
	fresh := &Manager{Repo: s.repo, Dir: s.dirTrees, Prefix: "sleipnir/s1", Clock: testClock()}

	// MinAge protects recent trees (the test clock is far in the future relative to Created? use a huge age)
	rep, err := fresh.Prune(ctx, PruneOptions{MinAge: 100 * 365 * 24 * time.Hour})
	if err != nil || len(rep.Removed) != 0 {
		t.Fatalf("MinAge: %+v, %v", rep, err)
	}
	if len(rep.Kept) == 0 {
		t.Fatal("MinAge should report what it kept")
	}

	// Salvage: the uncommitted files become a commit on the branch, then the directory goes.
	rep, err = fresh.Prune(ctx, PruneOptions{Salvage: true})
	if err != nil {
		t.Fatal(err)
	}
	if exists(s.dirty.Path) {
		t.Fatalf("salvaged tree still exists; kept=%+v", rep.Kept)
	}
	got, err := s.repo.Show(ctx, "sleipnir/s1/dirty-1", "README.md")
	if err != nil || string(got) != "# unsaved work\n" {
		t.Fatalf("salvaged content: %q, %v", got, err)
	}
	if got, err := s.repo.Show(ctx, "sleipnir/s1/dirty-1", "notes.txt"); err != nil || string(got) != "notes\n" {
		t.Fatalf("salvaged untracked file: %q, %v", got, err)
	}
	c, _ := s.repo.CommitInfo(ctx, "sleipnir/s1/dirty-1")
	if !strings.Contains(c.Subject, "salvage") || c.Author.Name != "dirty-1" {
		t.Fatalf("salvage commit: %+v", c)
	}
	if !contains(branchNames(rep.BranchesKept), "sleipnir/s1/dirty-1") {
		t.Fatalf("the salvage branch must be kept: %+v", rep.BranchesKept)
	}

	// Force deletes the branches that hold unmerged work.
	rep, err = fresh.Prune(ctx, PruneOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"sleipnir/s1/work-1", "sleipnir/s1/dirty-1"} {
		if _, err := s.repo.BranchSHA(ctx, b); err == nil {
			t.Errorf("Force left %s", b)
		}
	}
	// live-1 is still running under the same prefix, so the session is alive and
	// its integration branch is not leftover, whatever Force says.
	if _, err := s.repo.BranchSHA(ctx, "sleipnir/s1/_integration"); err != nil {
		t.Error("Force deleted the integration branch of a session that still has a live tree")
	}
	if !exists(s.live.Path) {
		t.Fatal("Force removed a live tree")
	}
	if _, err := s.repo.BranchSHA(ctx, "sleipnir/s1/live-1"); err != nil {
		t.Fatal("Force deleted a live tree's branch")
	}
	if !exists(s.handmade) || !exists(s.otherTree.Path) {
		t.Fatal("Force went beyond our prefix and markers")
	}
}

func TestPruneSkipsRunningOwnersAndOnlyThose(t *testing.T) {
	skipWithoutSleep(t)
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	b := mustCreate(t, m, "b")
	// another process owns a
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	mk, _ := readMarker(a.repo.GitDir())
	mk.PID, mk.Start = cmd.Process.Pid, procStart(cmd.Process.Pid)
	if err := writeMarker(a.repo.GitDir(), *mk); err != nil {
		t.Fatal(err)
	}
	orphan(t, b)

	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	rep, err := fresh.Prune(tctx(t), PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Live != 1 || !exists(a.Path) || exists(b.Path) {
		t.Fatalf("live=%d a=%v b=%v", rep.Live, exists(a.Path), exists(b.Path))
	}
	// a pid that was reused by a different process (start time differs) is a dead owner
	mk.Start = mk.Start + 12345
	if err := writeMarker(a.repo.GitDir(), *mk); err != nil {
		t.Fatal(err)
	}
	rep, err = fresh.Prune(tctx(t), PruneOptions{})
	if err != nil || exists(a.Path) {
		t.Fatalf("pid reuse: exists=%v err=%v rep=%+v", exists(a.Path), err, rep)
	}
	// an owner recorded on another boot cannot be judged: it is left alone
	c := mustCreate(t, m, "c")
	mkc, _ := readMarker(c.repo.GitDir())
	mkc.PID, mkc.BootID = deadPID(t), "some-other-boot-id"
	if err := writeMarker(c.repo.GitDir(), *mkc); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Prune(tctx(t), PruneOptions{}); err != nil || !exists(c.Path) {
		t.Fatalf("tree of an unknown owner was pruned: %v", err)
	}
}

func skipWithoutSleep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep")
	}
}

func TestPruneRefusesUnsafeStaleTrees(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	orphan(t, a)
	victim := t.TempDir()
	writeFile(t, filepath.Join(victim, "keep.txt"), "keep\n")
	// the stale tree's directory was replaced by a symlink to a valuable directory
	if err := os.RemoveAll(a.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, a.Path); err != nil {
		t.Skip("no symlinks")
	}
	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	rep, err := fresh.Prune(tctx(t), PruneOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(victim, "keep.txt")) {
		t.Fatal("Prune followed a symlink and deleted its target")
	}
	if len(rep.Removed) != 0 {
		t.Fatalf("Removed = %+v", rep.Removed)
	}
}

func TestCrashRecoveryEndToEnd(t *testing.T) {
	// A session that died with work in flight; the next one prunes and starts again
	// with the same agent ids, without any manual cleanup.
	s := newCrashedSession(t)
	ctx := tctx(t)
	fresh := &Manager{Repo: s.repo, Dir: s.dirTrees, Prefix: "sleipnir/s1", Clock: testClock()}
	// Without Prune, reusing a leftover agent id is refused (never overwrites).
	if _, err := fresh.Create(ctx, "dirty-1", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("Create over a stale dirty tree: %v", err)
	}
	if _, err := fresh.Create(ctx, "work-1", CreateOptions{}); !errors.Is(err, ErrExists) {
		// the directory exists (a stale but clean tree)
		t.Fatalf("Create over a stale tree: %v", err)
	}
	// A registration whose directory vanished is cleared automatically on Create (ours, owner dead).
	g, err := fresh.Create(ctx, "gone-1", CreateOptions{})
	if err != nil || !exists(g.Path) {
		t.Fatalf("Create over a vanished stale registration: %v", err)
	}
	if _, err := fresh.Prune(ctx, PruneOptions{}); err != nil {
		t.Fatal(err)
	}
	// After Prune the ids whose trees were removed are free again; work-1's branch
	// holds unmerged commits, so a new work-1 refuses to reuse the branch name.
	if _, err := fresh.Create(ctx, "clean-1", CreateOptions{}); err != nil {
		t.Fatalf("clean-1 after Prune: %v", err)
	}
	if _, err := fresh.Create(ctx, "work-1", CreateOptions{}); !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "exist nowhere else") {
		t.Fatalf("work-1 must not clobber a branch with unmerged work: %v", err)
	}
	// A new queue for the same session refuses the dead one's integration branch
	// unless told to resume it, and reclaims the dead one's tree when it does.
	if _, err := NewQueue(ctx, fresh, QueueOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("NewQueue over an integration branch with work: %v", err)
	}
	q2, err := NewQueue(ctx, fresh, QueueOptions{Resume: true})
	if err != nil {
		t.Fatalf("NewQueue(Resume): %v", err)
	}
	if q2.Tip() == fresh.BaseSHA() {
		t.Fatal("resume did not pick up the integration tip")
	}
	patch, err := q2.Finish(ctx)
	if err != nil || !strings.Contains(patch, "util.go") {
		t.Fatalf("Finish after resume: %v\n%s", err, patch)
	}
}
