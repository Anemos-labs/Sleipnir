package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommitTreeDoesNotMoveAnything(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	head, _ := r.Head(ctx)
	writeFile(t, filepath.Join(dir, "a.txt"), "dirty\n")
	tree, err := r.SnapshotTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := r.CommitTree(ctx, tree, []string{head}, "snapshot of the work tree", Author{Name: "Snap", Email: "snap@example.com"})
	if err != nil || len(sha) != 40 {
		t.Fatalf("CommitTree = %q, %v", sha, err)
	}
	c, err := r.CommitInfo(ctx, sha)
	if err != nil || c.Tree != tree || len(c.Parents) != 1 || c.Parents[0] != head || c.Subject != "snapshot of the work tree" || c.Author.Name != "Snap" {
		t.Fatalf("commit: %+v, %v", c, err)
	}
	// HEAD, branches, the index and the work tree are exactly as they were
	if h2, _ := r.Head(ctx); h2 != head {
		t.Fatal("HEAD moved")
	}
	st, _ := r.Status(ctx)
	if len(st.Staged) != 0 || len(st.Unstaged) != 1 {
		t.Fatalf("status changed: %+v", st)
	}
	if got, err := r.Show(ctx, sha, "a.txt"); err != nil || string(got) != "dirty\n" {
		t.Fatalf("Show: %q, %v", got, err)
	}
	for _, bad := range []struct {
		tree string
		par  []string
		msg  string
	}{{"-x", nil, "m"}, {tree, []string{"--all"}, "m"}, {tree, nil, " "}, {tree, nil, "a\x00b"}} {
		if _, err := r.CommitTree(ctx, bad.tree, bad.par, bad.msg, Author{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("CommitTree(%+v) = %v", bad, err)
		}
	}
}

// git decides that a file has not changed from its stat data alone (size, inode, times), which cannot tell a rewrite that keeps the size
// and falls in the second the entry was made in. It reads the content of such an entry instead when the entry is "racily clean": its
// time is not before the time of the index file. A private copy of the index that is given the time of the copying says the entry is
// clean, and a snapshot made through it records the old content, a checkpoint that silently misses an edit. (It took a second boundary
// between the rewrite and the copy, so on CI it showed as one run in a hundred.) The copy keeps the time of the index it came from.
func TestSnapshotSeesARewriteThatKeepsTheSizeInTheSecondOfTheIndex(t *testing.T) {
	dir := newRepo(t)
	rawGit(t, dir, "config", "core.trustctime", "false") // the time of the last change of an inode cannot be set back, and is not under test
	a := filepath.Join(dir, "a.txt")
	when := time.Now().Add(-time.Minute).Truncate(time.Second)

	// The index has the stat data of the old content ...
	if err := os.Chtimes(a, when, when); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "add", "a.txt")
	// ... the file is rewritten with as many bytes and the time it had, in the second the index file was written ...
	writeFile(t, a, "dirty\n")
	for _, p := range []string{a, filepath.Join(dir, ".git", "index")} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	// ... which is what makes git itself read the content.
	if st := rawGit(t, dir, "--no-optional-locks", "status", "--porcelain"); !strings.Contains(st, "a.txt") {
		t.Fatalf("the premise does not hold, git does not see the rewrite: status = %q", st)
	}

	tree, err := openRepo(t, dir).SnapshotTree(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := rawGit(t, dir, "cat-file", "-p", tree+":a.txt"); got != "dirty" {
		t.Fatalf("the snapshot has %q for a.txt, want the content the file has", got)
	}
}

func TestCommitAllowEmpty(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	r, err := Init(ctxT(t), dir, false, WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	sha, err := r.CommitAllowEmpty(ctx, "first, empty", Author{})
	if err != nil || len(sha) != 40 {
		t.Fatalf("empty repository, empty commit: %q, %v", sha, err)
	}
	c, _ := r.CommitInfo(ctx, sha)
	if len(c.Parents) != 0 || c.Subject != "first, empty" {
		t.Fatalf("commit: %+v", c)
	}
	// with content it behaves like CommitAll
	writeFile(t, filepath.Join(dir, "f.txt"), "x\n")
	sha2, err := r.CommitAllowEmpty(ctx, "with content", Author{})
	if err != nil || sha2 == sha {
		t.Fatalf("%q %v", sha2, err)
	}
	if got, _ := r.Show(ctx, sha2, "f.txt"); string(got) != "x\n" {
		t.Fatal("content missing")
	}
	// and an empty commit on top of a non-empty history is still recorded
	sha3, err := r.CommitAllowEmpty(ctx, "empty again", Author{})
	if err != nil || sha3 == sha2 {
		t.Fatalf("%q %v", sha3, err)
	}
}

func TestCommitsOnlyOn(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	base, _ := r.Head(ctx)
	rawGit(t, dir, "checkout", "-q", "-b", "sleipnir/s1/agent")
	writeFile(t, filepath.Join(dir, "w1.txt"), "1\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "w1")
	writeFile(t, filepath.Join(dir, "w2.txt"), "2\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "w2")
	tip, _ := r.ResolveRef(ctx, "sleipnir/s1/agent")
	rawGit(t, dir, "checkout", "-q", "main")
	// two commits exist nowhere else (the agent branch itself does not count, nor does HEAD=main)
	if n, err := r.CommitsOnlyOn(ctx, tip, "sleipnir/s1/*"); err != nil || n != 2 {
		t.Fatalf("unique = %d, %v", n, err)
	}
	// once a user branch has them, nothing is unique any more
	rawGit(t, dir, "branch", "saved", tip)
	if n, _ := r.CommitsOnlyOn(ctx, tip, "sleipnir/s1/*"); n != 0 {
		t.Fatalf("after saving: %d", n)
	}
	rawGit(t, dir, "branch", "-D", "saved")
	// a tag counts as elsewhere
	rawGit(t, dir, "tag", "keep", tip)
	if n, _ := r.CommitsOnlyOn(ctx, tip, "sleipnir/s1/*"); n != 0 {
		t.Fatalf("tag: %d", n)
	}
	rawGit(t, dir, "tag", "-d", "keep")
	// extra refs (the merge queue's integration branch) count as well
	rawGit(t, dir, "branch", "sleipnir/s1/_integration", tip)
	if n, _ := r.CommitsOnlyOn(ctx, tip, "sleipnir/s1/*"); n != 2 {
		t.Fatalf("the exclude glob must also hide the integration branch: %d", n)
	}
	if n, err := r.CommitsOnlyOn(ctx, tip, "sleipnir/s1/*", "refs/heads/sleipnir/s1/_integration"); err != nil || n != 0 {
		t.Fatalf("extra ref: %d, %v", n, err)
	}
	// HEAD counts: a detached HEAD on the tip saves it
	rawGit(t, dir, "checkout", "-q", "--detach", tip)
	if n, _ := r.CommitsOnlyOn(ctx, tip, "sleipnir/s1/*"); n != 0 {
		t.Fatalf("HEAD: %d", n)
	}
	// the base itself is reachable from main: not unique
	if n, _ := r.CommitsOnlyOn(ctx, base, "sleipnir/s1/*"); n != 0 {
		t.Fatalf("base: %d", n)
	}
	for _, bad := range []string{"-x", "--all", ""} {
		if _, err := r.CommitsOnlyOn(ctx, bad, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("CommitsOnlyOn(%q) = %v", bad, err)
		}
	}
	if _, err := r.CommitsOnlyOn(ctx, tip, "--evil"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("option-looking glob: %v", err)
	}
}

func TestSparseCheckoutAndRefreshIndex(t *testing.T) {
	dir := newRepo(t)
	r := openRepo(t, dir)
	ctx := ctxT(t)
	wt := filepath.Join(t.TempDir(), "sparse")
	if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt, Detach: true, Commit: "HEAD", NoCheckout: true}); err != nil {
		t.Fatal(err)
	}
	w, err := r.Reopen(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SparseCheckoutSet(ctx, []string{"sub"}); err != nil {
		t.Fatal(err)
	}
	if err := w.ResetHard(ctx, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "sub", "c.txt")); err != nil {
		t.Fatal("sparse directory not populated")
	}
	if _, err := os.Stat(filepath.Join(wt, "a.txt")); err != nil {
		t.Fatal("root files are always part of the cone")
	}
	for _, bad := range []string{"../x", "/abs", "a\nb"} {
		if err := w.SparseCheckoutSet(ctx, []string{bad}); !errors.Is(err, ErrInvalid) {
			t.Errorf("SparseCheckoutSet(%q) = %v", bad, err)
		}
	}
	// A sparse tree diffs cleanly: files outside the cone are not "deleted".
	writeFile(t, filepath.Join(wt, "sub", "c.txt"), "edited\n")
	d, err := w.Diff(ctx, "HEAD", DiffOptions{})
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "sub/c.txt" {
		t.Fatalf("sparse diff: %+v, %v", d, err)
	}
	if err := w.RefreshIndex(ctx); err != nil {
		t.Fatalf("RefreshIndex: %v", err)
	}
	// bare repositories have no work tree for either
	bare := filepath.Join(t.TempDir(), "b.git")
	rawGit(t, dir, "clone", "-q", "--bare", ".", bare)
	if err := openRepo(t, bare).SparseCheckoutSet(ctx, []string{"sub"}); !errors.Is(err, ErrNotARepo) {
		t.Fatalf("bare: %v", err)
	}
}

func TestRepoInitInheritsSettings(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	gitShim, out := shim(t, `printf '%s\n' "$@" > "$D/args.$$"; exec "$REAL" "$@"`)
	r, err := Open(dir, WithGitPath(gitShim), WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	child, err := r.Init(ctxT(t), filepath.Join(t.TempDir(), "child"), false)
	if err != nil {
		t.Fatal(err)
	}
	if child.IsBare() || child.Root() == r.Root() {
		t.Fatalf("child: %+v", child)
	}
	var sawInit bool
	for _, f := range globFiles(t, filepath.Join(out, "args.*")) {
		if strings.Contains(readFile(t, f), "\ninit\n") {
			sawInit = true
			if !strings.Contains(readFile(t, f), "--template=") {
				t.Fatal("init without disabling templates")
			}
		}
	}
	if !sawInit {
		t.Fatal("the child repository was not created through the parent's git binary")
	}
}
