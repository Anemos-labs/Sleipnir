package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoBranches builds main and side from a common base. mainEdit/sideEdit are the
// new contents of f.txt on each side (empty string leaves it alone).
func twoBranches(t *testing.T, mainEdit, sideEdit string) (dir string, r *Repo) {
	t.Helper()
	dir = newRepo(t)
	writeFile(t, filepath.Join(dir, "f.txt"), "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "base with f.txt")
	rawGit(t, dir, "checkout", "-q", "-b", "side")
	if sideEdit != "" {
		writeFile(t, filepath.Join(dir, "f.txt"), sideEdit)
	}
	writeFile(t, filepath.Join(dir, "side.txt"), "side\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "side")
	rawGit(t, dir, "checkout", "-q", "main")
	if mainEdit != "" {
		writeFile(t, filepath.Join(dir, "f.txt"), mainEdit)
	}
	writeFile(t, filepath.Join(dir, "main.txt"), "main\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "main")
	return dir, openRepo(t, dir)
}

func TestMergeCleanAndFastForward(t *testing.T) {
	dir, r := twoBranches(t, "l1\nMAIN2\nl3\nl4\nl5\nl6\nl7\nl8\n", "l1\nl2\nl3\nl4\nl5\nl6\nSIDE7\nl8\n")
	ctx := ctxT(t)
	before, _ := r.Head(ctx)
	res, err := r.Merge(ctx, MergeOptions{Ref: "side", Message: "integrate side", Author: Author{Name: "queue", Email: "q@example.com"}})
	if err != nil || res.Conflicted || res.UpToDate {
		t.Fatalf("clean merge: %+v, %v", res, err)
	}
	c, _ := r.CommitInfo(ctx, res.Head)
	if len(c.Parents) != 2 || c.Parents[0] != before || c.Subject != "integrate side" || c.Author.Name != "queue" {
		t.Fatalf("merge commit: %+v", c)
	}
	if got := readFile(t, filepath.Join(dir, "f.txt")); got != "l1\nMAIN2\nl3\nl4\nl5\nl6\nSIDE7\nl8\n" {
		t.Fatalf("merged content: %q", got)
	}
	// merging again is a no-op
	again, err := r.Merge(ctx, MergeOptions{Ref: "side"})
	if err != nil || !again.UpToDate || again.Head != res.Head {
		t.Fatalf("second merge: %+v, %v", again, err)
	}

	// Fast-forward when possible; --no-ff forces a merge commit anyway.
	rawGit(t, dir, "checkout", "-q", "-b", "ff-base", "side")
	rawGit(t, dir, "checkout", "-q", "-b", "ahead", "side")
	writeFile(t, filepath.Join(dir, "ahead.txt"), "ahead\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "ahead")
	rawGit(t, dir, "checkout", "-q", "ff-base")
	aheadSHA, _ := r.ResolveRef(ctx, "ahead")
	ff, err := r.Merge(ctx, MergeOptions{Ref: "ahead"})
	if err != nil || ff.Head != aheadSHA {
		t.Fatalf("fast-forward: %+v, %v (want head %s)", ff, err, aheadSHA)
	}
	rawGit(t, dir, "reset", "-q", "--hard", "side")
	nff, err := r.Merge(ctx, MergeOptions{Ref: "ahead", NoFF: true})
	if err != nil || nff.Head == aheadSHA {
		t.Fatalf("--no-ff must create a merge commit: %+v, %v", nff, err)
	}
	// --ff-only refuses divergent history with a typed error.
	rawGit(t, dir, "reset", "-q", "--hard", "main")
	if _, err := r.Merge(ctx, MergeOptions{Ref: "side", FFOnly: true}); err == nil {
		t.Fatal("--ff-only merged divergent history")
	}
	if _, err := r.Merge(ctx, MergeOptions{Ref: "--strategy=ours"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("option-looking ref: %v", err)
	}
}

func TestMergeConflictThenAbortLeavesNothingBehind(t *testing.T) {
	dir, r := twoBranches(t, "l1\nMAIN\nl3\nl4\nl5\nl6\nl7\nl8\n", "l1\nSIDE\nl3\nl4\nl5\nl6\nl7\nl8\n")
	ctx := ctxT(t)
	before, _ := r.Head(ctx)
	res, err := r.Merge(ctx, MergeOptions{Ref: "side", NoFF: true})
	if err != nil || !res.Conflicted {
		t.Fatalf("expected a conflict result, got %+v, %v", res, err)
	}
	if !strings.Contains(res.Output, "CONFLICT") {
		t.Fatalf("output lacks the CONFLICT line: %q", res.Output)
	}
	// diff3 markers: the base section is present.
	body := readFile(t, filepath.Join(dir, "f.txt"))
	for _, want := range []string{"<<<<<<<", "|||||||", "=======", ">>>>>>>", "MAIN", "SIDE", "l2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("conflict file lacks %q:\n%s", want, body)
		}
	}
	un, _ := r.Unmerged(ctx)
	if len(un) != 1 || un[0].Path != "f.txt" || un[0].Kind() != "content" {
		t.Fatalf("unmerged: %+v", un)
	}
	if err := r.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := r.Head(ctx)
	if after != before || r.InProgress() != "" {
		t.Fatalf("abort left HEAD=%s (want %s), in progress %q", after, before, r.InProgress())
	}
	if ok, _ := r.IsClean(ctx); !ok {
		st, _ := r.Status(ctx)
		t.Fatalf("tree dirty after abort: %+v", st)
	}
	if got := readFile(t, filepath.Join(dir, "f.txt")); strings.Contains(got, "<<<<<<<") {
		t.Fatal("markers survived the abort")
	}
}

func TestMergeDriverBecomesAConflictNeverASilentPick(t *testing.T) {
	skipWithoutUnix(t)
	dir, _ := twoBranches(t, "l1\nl2\nl3\nl4\nMAIN5\nl6\nl7\nl8\n", "l1\nl2\nl3\nl4\nl5\nl6\nSIDE7\nl8\n")
	// A `merge=ours`-style custom driver: with plain git the config command "true"
	// wins silently and the side's change is lost without any conflict.
	writeFile(t, filepath.Join(dir, ".gitattributes"), "f.txt merge=lockdrv\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "attrs")
	rawGit(t, dir, "config", "merge.lockdrv.driver", "true")
	r := openRepo(t, dir)
	ctx := ctxT(t)
	res, err := r.Merge(ctx, MergeOptions{Ref: "side", NoFF: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Conflicted {
		body := readFile(t, filepath.Join(dir, "f.txt"))
		t.Fatalf("custom merge driver was honored (or silently ignored); result:\n%s", body)
	}
	un, _ := r.Unmerged(ctx)
	if len(un) != 1 || un[0].Path != "f.txt" {
		t.Fatalf("unmerged = %+v", un)
	}
	if err := r.Abort(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRebaseOnto(t *testing.T) {
	dir, r := twoBranches(t, "", "")
	ctx := ctxT(t)
	// side has one commit adding side.txt; replay it onto main in a detached worktree
	wt := filepath.Join(t.TempDir(), "int")
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt, Detach: true, Commit: "main"}); err != nil {
		t.Fatal(err)
	}
	w, err := r.Reopen(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	side, _ := r.ResolveRef(ctx, "side")
	mb, _ := r.MergeBase(ctx, "main", "side")
	mainSHA, _ := r.ResolveRef(ctx, "main")
	res, err := w.Rebase(ctx, RebaseOptions{Onto: mainSHA, Upstream: mb, Branch: side, Author: Author{Name: "queue"}})
	if err != nil || res.Conflicted {
		t.Fatalf("rebase: %+v, %v", res, err)
	}
	c, _ := w.CommitInfo(ctx, res.Head)
	if len(c.Parents) != 1 || c.Parents[0] != mainSHA || c.Subject != "side" {
		t.Fatalf("rebased commit: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(wt, "side.txt")); err != nil {
		t.Fatal("rebased content missing")
	}
	_ = dir
}

func TestRebaseConflictAbortNeedsExplicitReset(t *testing.T) {
	dir, r := twoBranches(t, "l1\nMAIN\nl3\nl4\nl5\nl6\nl7\nl8\n", "l1\nSIDE\nl3\nl4\nl5\nl6\nl7\nl8\n")
	ctx := ctxT(t)
	wt := filepath.Join(t.TempDir(), "int")
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt, Detach: true, Commit: "main"}); err != nil {
		t.Fatal(err)
	}
	w, _ := r.Reopen(ctx, wt)
	side, _ := r.ResolveRef(ctx, "side")
	mb, _ := r.MergeBase(ctx, "main", "side")
	mainSHA, _ := r.ResolveRef(ctx, "main")
	res, err := w.Rebase(ctx, RebaseOptions{Onto: mainSHA, Upstream: mb, Branch: side})
	if err != nil || !res.Conflicted {
		t.Fatalf("expected conflict, got %+v, %v", res, err)
	}
	if w.InProgress() != "rebase" {
		t.Fatalf("InProgress = %q", w.InProgress())
	}
	if err := w.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	// git returned to the *branch tip*, not to where we started: documented, and
	// the reason callers reset explicitly.
	head, _ := w.Head(ctx)
	if head != side {
		t.Logf("after abort HEAD=%s (side tip %s, start %s)", head, side, mainSHA)
	}
	if err := w.ResetHard(ctx, mainSHA); err != nil {
		t.Fatal(err)
	}
	if err := w.CleanUntracked(ctx); err != nil {
		t.Fatal(err)
	}
	head, _ = w.Head(ctx)
	if head != mainSHA {
		t.Fatalf("HEAD = %s, want %s", head, mainSHA)
	}
	if ok, _ := w.IsClean(ctx); !ok {
		t.Fatal("integration tree dirty after abort+reset")
	}
	_ = dir
}

func TestWorktreeLifecycle(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	base, _ := r.Head(ctx)
	root := t.TempDir()
	wt := filepath.Join(root, "agent one") // a space in the path
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt, Branch: "sleipnir/s1/agent-one", Commit: base}); err != nil {
		t.Fatal(err)
	}
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt, Branch: "sleipnir/s1/other", Commit: base}); err == nil {
		t.Fatal("adding a worktree over an existing one succeeded")
	}
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: filepath.Join(root, "x"), Branch: "sleipnir/s1/agent-one", Commit: base}); err == nil {
		t.Fatal("reusing a branch name succeeded")
	}
	for _, bad := range []WorktreeAddOptions{
		{Path: "relative", Branch: "b", Commit: base},
		{Path: filepath.Join(root, "y"), Branch: "-b", Commit: base},
		{Path: filepath.Join(root, "y"), Branch: "b", Commit: "--force"},
		{Path: filepath.Join(root, "y"), Commit: base},
		{Path: filepath.Join(root, "y\nz"), Branch: "b", Commit: base},
	} {
		if err := r.WorktreeAdd(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("WorktreeAdd(%+v) = %v, want ErrInvalid", bad, err)
		}
	}
	list, err := r.Worktrees(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("Worktrees = %+v, %v", list, err)
	}
	realWT, _ := filepath.EvalSymlinks(wt)
	if list[0].Path != dir || list[0].Branch != "main" {
		t.Fatalf("main entry: %+v", list[0])
	}
	if list[1].Path != realWT || list[1].Branch != "sleipnir/s1/agent-one" || list[1].Head != base || list[1].Detached || list[1].Prunable {
		t.Fatalf("linked entry: %+v (want path %s)", list[1], realWT)
	}

	// dirty trees are not removed without force
	writeFile(t, filepath.Join(wt, "wip.txt"), "wip\n")
	if err := r.WorktreeRemove(ctx, wt, false); err == nil {
		t.Fatal("removed a dirty worktree without force")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("dirty worktree vanished")
	}
	if err := r.WorktreeRemove(ctx, wt, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree directory still there")
	}
	// A directory that disappeared behind git's back is reported prunable, and can be removed.
	gone := filepath.Join(root, "gone")
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: gone, Detach: true, Commit: base}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	list, _ = r.Worktrees(ctx)
	prunable := 0
	for _, w := range list {
		if w.Prunable {
			prunable++
			if !strings.HasSuffix(w.Path, "gone") || w.PrunableReason == "" || !w.Detached {
				t.Fatalf("prunable entry: %+v", w)
			}
		}
	}
	if prunable != 1 {
		t.Fatalf("expected one prunable entry, got %+v", list)
	}
	realGone, _ := filepath.EvalSymlinks(filepath.Dir(gone))
	if err := r.WorktreeRemove(ctx, filepath.Join(realGone, "gone"), true); err != nil {
		t.Fatalf("removing a prunable entry: %v", err)
	}
	list, _ = r.Worktrees(ctx)
	if len(list) != 1 {
		t.Fatalf("entries left: %+v", list)
	}
}

func TestBranchHelpers(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	head, _ := r.Head(ctx)
	if err := r.CreateBranch(ctx, "sleipnir/s1/a", head); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBranch(ctx, "sleipnir/s1/b", head); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBranch(ctx, "other/c", head); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBranch(ctx, "sleipnir/s1/a", head); err == nil {
		t.Fatal("re-creating an existing branch succeeded")
	}
	refs, err := r.Branches(ctx, "sleipnir/s1/")
	if err != nil || len(refs) != 2 || refs[0].Name != "sleipnir/s1/a" || refs[1].Name != "sleipnir/s1/b" || refs[0].SHA != head {
		t.Fatalf("Branches = %+v, %v", refs, err)
	}
	if refs2, _ := r.Branches(ctx, "sleipnir/s1"); len(refs2) != 2 {
		t.Fatalf("prefix without a trailing slash: %+v", refs2)
	}
	if _, err := r.BranchSHA(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BranchSHA(nope): %v", err)
	}

	// compare-and-swap: moving from the wrong old value fails and changes nothing
	writeFile(t, filepath.Join(dir, "a.txt"), "next\n")
	rawGit(t, dir, "commit", "-aqm", "next")
	next, _ := r.Head(ctx)
	if err := r.UpdateBranch(ctx, "sleipnir/s1/a", next, next, "wrong old"); err == nil {
		t.Fatal("CAS with the wrong old value succeeded")
	}
	if got, _ := r.BranchSHA(ctx, "sleipnir/s1/a"); got != head {
		t.Fatalf("failed CAS moved the branch to %s", got)
	}
	if err := r.UpdateBranch(ctx, "sleipnir/s1/a", next, head, "right old"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.BranchSHA(ctx, "sleipnir/s1/a"); got != next {
		t.Fatalf("CAS did not move the branch: %s", got)
	}
	// creation via CAS: old="" means the branch must not exist
	if err := r.UpdateBranch(ctx, "sleipnir/s1/new", next, "", "create"); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateBranch(ctx, "sleipnir/s1/new", head, "", "create again"); err == nil {
		t.Fatal("create-only CAS overwrote an existing branch")
	}
	// git refuses to delete a branch that is checked out somewhere
	if err := r.DeleteBranch(ctx, "main"); err == nil {
		t.Fatal("deleted the checked-out branch")
	}
	if err := r.DeleteBranch(ctx, "sleipnir/s1/b"); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteBranch(ctx, "-D"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("option-looking name: %v", err)
	}
}

func TestMergeFile(t *testing.T) {
	ctx := ctxT(t)
	isolateHome(t)
	merged, n, err := MergeFile(ctx, []byte("a\nb\nc\n"), []byte("a\nb\n"), []byte("a\nb\nd\n"), "ours", "base", "theirs", WithHermeticConfig())
	if err != nil || n != 1 {
		t.Fatalf("conflicting merge: n=%d err=%v", n, err)
	}
	for _, want := range []string{"<<<<<<< ours", "||||||| base", ">>>>>>> theirs", "c\n", "d\n"} {
		if !strings.Contains(string(merged), want) {
			t.Fatalf("merged text lacks %q:\n%s", want, merged)
		}
	}
	clean, n, err := MergeFile(ctx, []byte("x\nb\n"), []byte("a\nb\n"), []byte("a\nb\nz\n"), "o", "b", "t", WithHermeticConfig())
	if err != nil || n != 0 || string(clean) != "x\nb\nz\n" {
		t.Fatalf("clean merge: %q n=%d err=%v", clean, n, err)
	}
	if _, _, err := MergeFile(ctx, []byte("a\x00b"), []byte("a"), []byte("a"), "o", "b", "t", WithHermeticConfig()); !errors.Is(err, ErrBinary) {
		t.Fatalf("binary: %v", err)
	}
	// labels cannot smuggle extra lines into the marker text
	m2, _, _ := MergeFile(ctx, []byte("1\n"), []byte("0\n"), []byte("2\n"), "o\nrm -rf", "b", "t", WithHermeticConfig())
	if strings.Contains(string(m2), "\nrm -rf") {
		t.Fatalf("label with newline leaked: %q", m2)
	}
}

func TestInitCreatesQuietRepositories(t *testing.T) {
	skipWithoutUnix(t)
	home := isolateHome(t)
	// A hostile template directory in the user's configuration must not seed hooks.
	tmpl := filepath.Join(home, "template", "hooks")
	writeFile(t, filepath.Join(tmpl, "pre-commit"), "#!/bin/sh\nexit 1\n")
	writeFile(t, filepath.Join(home, ".gitconfig"), "[init]\n\ttemplateDir = "+filepath.Join(home, "template")+"\n\tdefaultBranch = trunk\n")
	ctx := ctxT(t)
	r, err := Init(ctx, filepath.Join(t.TempDir(), "shadow"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.CommonDir(), "hooks", "pre-commit")); err == nil {
		t.Fatal("template hook copied into a new repository")
	}
	if br, _ := r.Branch(ctx); br != "main" {
		t.Fatalf("initial branch = %q, want main regardless of init.defaultBranch", br)
	}
	bare, err := Init(ctx, filepath.Join(t.TempDir(), "bare.git"), true)
	if err != nil || !bare.IsBare() {
		t.Fatalf("bare init: %v", err)
	}
}

func TestOpenPinsTheWorkTreeAgainstConfigRedirects(t *testing.T) {
	dir := newRepo(t)
	victim := t.TempDir()
	writeFile(t, filepath.Join(victim, "keep.txt"), "keep\n")
	// core.worktree redirect
	rawGit(t, dir, "config", "core.worktree", victim)
	r := openRepo(t, dir)
	if r.Root() != dir {
		t.Fatalf("Root = %q, want %q", r.Root(), dir)
	}
	ctx := ctxT(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "changed\n")
	if err := r.ResetHard(ctx, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := r.CleanUntracked(ctx); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, "a.txt")) != "alpha\n" {
		t.Fatal("ResetHard did not act on the repository's own work tree")
	}
	if readFile(t, filepath.Join(victim, "keep.txt")) != "keep\n" {
		t.Fatal("victim touched")
	}
	// core.bare=true in a work tree: git's view wins, operations needing a work tree fail closed.
	rawGit(t, dir, "config", "--unset", "core.worktree")
	rawGit(t, dir, "config", "core.bare", "true")
	rb := openRepo(t, dir)
	if !rb.IsBare() {
		t.Fatal("core.bare=true not honored")
	}
	if err := rb.ResetHard(ctx, "HEAD"); err == nil {
		t.Fatal("ResetHard ran on a repository configured as bare")
	}
}
