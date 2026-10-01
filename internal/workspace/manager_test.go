package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// deadPID returns the pid of a process that has exited.
func deadPID(t testing.TB) int {
	t.Helper()
	c := exec.Command("sh", "-c", "exit 0")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// orphan rewrites a tree's marker so that its owner looks like a crashed process.
func orphan(t testing.TB, tree *Tree) {
	t.Helper()
	mk, err := readMarker(tree.repo.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	mk.PID, mk.Start = deadPID(t), 0
	if err := writeMarker(tree.repo.GitDir(), *mk); err != nil {
		t.Fatal(err)
	}
}

func projectFiles(t testing.TB, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			tgt, _ := os.Readlink(p)
			out[filepath.ToSlash(rel)] = "symlink:" + tgt
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = fmt.Sprintf("%v:%s", fi.Mode().Perm()&0o111 != 0, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCreateGivesAnIsolatedTree(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	log := &eventLog{}
	m.OnEvent = log.fn()

	a := mustCreate(t, m, "be-1")
	b := mustCreate(t, m, "fe-1")

	if a.Path == b.Path || !strictlyUnder(m.TreesDir(), a.Path) || !strictlyUnder(m.TreesDir(), b.Path) {
		t.Fatalf("paths: %s %s (dir %s)", a.Path, b.Path, m.TreesDir())
	}
	if real, _ := filepath.EvalSymlinks(a.Path); real != a.Path {
		t.Fatalf("Path is not symlink-resolved: %s vs %s", a.Path, real)
	}
	if a.Branch != "sleipnir/s1/be-1" || a.Agent != "be-1" || a.Mode != ModeWorktree || len(a.Base) != 40 || a.Base != m.BaseSHA() {
		t.Fatalf("tree: %+v", a)
	}
	head, _ := repo.Head(tctx(t))
	if a.Base != head {
		t.Fatalf("base %s != HEAD %s", a.Base, head)
	}
	// identical content, modes and names (spaces included) as the base
	want := projectFiles(t, dir)
	for _, tr := range []*Tree{a, b} {
		got := projectFiles(t, tr.Path)
		if len(got) != len(want) {
			t.Fatalf("%s has %d files, want %d", tr.Agent, len(got), len(want))
		}
		for name, c := range want {
			if got[name] != c {
				t.Fatalf("%s: %s differs", tr.Agent, name)
			}
		}
	}
	if fi, err := os.Lstat(filepath.Join(a.Path, ".git")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf(".git in a worktree must be a file: %v %v", fi, err)
	}
	// ownership marker
	mk, err := readMarker(a.repo.GitDir())
	if err != nil || mk.Agent != "be-1" || mk.Prefix != "sleipnir/s1" || mk.Path != a.Path || mk.Base != a.Base || mk.PID != os.Getpid() || mk.Branch != a.Branch {
		t.Fatalf("marker: %+v, %v", mk, err)
	}
	// isolation: edits in one tree are invisible to the other and to the user's checkout
	edit(t, a, "README.md", "# changed by be-1\n")
	edit(t, a, "new/file.txt", "new\n")
	if readFile(t, filepath.Join(b.Path, "README.md")) != "# project\n" || readFile(t, filepath.Join(dir, "README.md")) != "# project\n" {
		t.Fatal("an edit leaked out of its tree")
	}
	if exists(filepath.Join(dir, "new")) || exists(filepath.Join(b.Path, "new")) {
		t.Fatal("a new file leaked out of its tree")
	}
	if ok, _ := repo.IsClean(tctx(t)); !ok {
		t.Fatal("the user's checkout was touched")
	}
	// the user's repository sees the branches and worktrees, nothing else
	br, _ := repo.Branches(tctx(t), "sleipnir/s1/")
	if len(br) != 2 {
		t.Fatalf("branches: %+v", br)
	}
	if _, err := m.Create(tctx(t), "be-1", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("creating twice: %v", err)
	}
	if got, ok := m.Get("be-1"); !ok || got != a {
		t.Fatal("Get")
	}
	if ts := m.Trees(); len(ts) != 2 || ts[0] != a || ts[1] != b {
		t.Fatalf("Trees: %v", ts)
	}
	if log.count(EventCreate) != 2 {
		t.Fatalf("events: %v", log.types())
	}
	ev, _ := log.first(EventCreate)
	if ev.Agent != "be-1" || ev.Data["branch"] != a.Branch || ev.Data["path"] != a.Path || ev.Data["mode"] != "worktree" {
		t.Fatalf("create event: %+v", ev)
	}
}

func TestCreateManyConcurrently(t *testing.T) {
	dir := newRepo(t)
	m := newManager(t, openRepo(t, dir))
	const n = 24
	trees := make([]*Tree, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			trees[i], errs[i] = m.Create(tctx(t), fmt.Sprintf("w-%02d", i), CreateOptions{})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	// every tree is complete and independent; edit and commit them all at once
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr := trees[i]
			edit(t, tr, fmt.Sprintf("work/w-%02d.txt", i), fmt.Sprintf("worker %d\n", i))
			if _, err := tr.Commit(tctx(t), "work"); err != nil {
				errs[i] = err
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Commit %d: %v", i, err)
		}
	}
	list, err := m.List(tctx(t))
	if err != nil || len(list) != n {
		t.Fatalf("List = %d entries, %v", len(list), err)
	}
	branches := map[string]bool{}
	for _, tr := range trees {
		if branches[tr.Branch] {
			t.Fatalf("duplicate branch %s", tr.Branch)
		}
		branches[tr.Branch] = true
		ch, _ := tr.Changed(tctx(t))
		if len(ch) != 1 {
			t.Fatalf("%s changed %v", tr.Agent, ch)
		}
	}
}

func TestNameValidation(t *testing.T) {
	valid := []string{"be-1", "mgr", "A", "agent.v2", "a_b", "x1", "Z-9.z", strings.Repeat("a", 63)}
	invalid := []string{"", "-x", "--force", "-", ".hidden", "a/b", `a\b`, "..", "a..b", "x.lock", "X.LOCK", "x.", "_integration", "_shadow", "con", "NUL",
		"nul.txt", "com1", "LPT9", "a b", "é", "a\x00b", "a\nb", "a\tb", "a:b", "a*b", "a?b", "a~b", "a^b", "a[b", "a@{b", strings.Repeat("a", 64), "/abs", "a/../b"}
	for _, id := range valid {
		if err := ValidateAgentID(id); err != nil {
			t.Errorf("ValidateAgentID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range invalid {
		if err := ValidateAgentID(id); !errors.Is(err, ErrBadName) {
			t.Errorf("ValidateAgentID(%q) = %v, want ErrBadName", id, err)
		}
	}
	// every valid id must be a branch name git accepts
	dir := newRepo(t)
	for _, id := range valid {
		rawGit(t, dir, "check-ref-format", "refs/heads/sleipnir/s1/"+id)
	}
	for _, p := range []string{"sleipnir", "sleipnir/s-1a2b", "a/b/c", "S1"} {
		if err := ValidatePrefix(p); err != nil {
			t.Errorf("ValidatePrefix(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"", "/x", "x/", "a//b", "-x", "a/-x", "a..b", "a b", "x.lock", ".x", "a/.b", "é", "a\x00b", strings.Repeat("a/", 100), "a@{b", "a:b"} {
		if err := ValidatePrefix(p); !errors.Is(err, ErrBadName) {
			t.Errorf("ValidatePrefix(%q) = %v, want ErrBadName", p, err)
		}
	}

	// Rejected ids create nothing: no directory, no branch, no worktree entry.
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	mustCreate(t, m, "ok-1")
	before, _ := repo.Worktrees(tctx(t))
	brBefore, _ := repo.Branches(tctx(t), "")
	entriesBefore, _ := os.ReadDir(m.TreesDir())
	for _, id := range invalid {
		if _, err := m.Create(tctx(t), id, CreateOptions{}); !errors.Is(err, ErrBadName) {
			t.Errorf("Create(%q) = %v, want ErrBadName", id, err)
		}
	}
	after, _ := repo.Worktrees(tctx(t))
	brAfter, _ := repo.Branches(tctx(t), "")
	entriesAfter, _ := os.ReadDir(m.TreesDir())
	if len(before) != len(after) || len(brBefore) != len(brAfter) || len(entriesBefore) != len(entriesAfter) {
		t.Fatalf("rejected names left traces: worktrees %d->%d branches %d->%d dirs %d->%d",
			len(before), len(after), len(brBefore), len(brAfter), len(entriesBefore), len(entriesAfter))
	}
	// invalid prefix on the manager
	bad := &Manager{Repo: repo, Dir: filepath.Join(t.TempDir(), "x"), Prefix: "../escape"}
	if _, err := bad.Create(tctx(t), "a", CreateOptions{}); !errors.Is(err, ErrBadName) {
		t.Fatalf("bad prefix: %v", err)
	}
	if _, err := (&Manager{Repo: repo}).Create(tctx(t), "a", CreateOptions{}); !errors.Is(err, ErrBadName) {
		t.Fatalf("no Dir: %v", err)
	}
	// a Create option naming an invalid base fails without a trace
	if _, err := m.Create(tctx(t), "base-bad", CreateOptions{Base: "--all"}); !errors.Is(err, gitx.ErrInvalid) {
		t.Fatalf("option-looking base: %v", err)
	}
	if _, err := m.Create(tctx(t), "base-missing", CreateOptions{Base: "no-such-ref"}); !errors.Is(err, gitx.ErrNotFound) {
		t.Fatalf("missing base: %v", err)
	}
	if exists(filepath.Join(m.TreesDir(), "base-bad")) || exists(filepath.Join(m.TreesDir(), "base-missing")) {
		t.Fatal("failed creation left a directory")
	}
}

func TestDirThroughSymlinkAndPreexistingPaths(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "the-symlink")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skip("no symlinks")
	}
	m := &Manager{Repo: repo, Dir: filepath.Join(link, "trees"), Prefix: "sleipnir/s1", Clock: testClock()}
	a := mustCreate(t, m, "a")
	realResolved, _ := filepath.EvalSymlinks(realDir)
	if !strings.HasPrefix(a.Path, realResolved) || strings.Contains(a.Path, "the-symlink") {
		t.Fatalf("tree path %s does not use the resolved directory %s", a.Path, realResolved)
	}
	if m.TreesDir() != filepath.Join(realResolved, "trees") {
		t.Fatalf("TreesDir = %s", m.TreesDir())
	}

	// A symlink planted where an agent's directory would go.
	victim := t.TempDir()
	writeFile(t, filepath.Join(victim, "sentinel.txt"), "keep\n")
	if err := os.Symlink(victim, filepath.Join(m.TreesDir(), "evil")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(tctx(t), "evil", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("Create over a symlink: %v", err)
	}
	if ents, _ := os.ReadDir(victim); len(ents) != 1 {
		t.Fatalf("Create wrote through the symlink into %s: %v", victim, ents)
	}
	// A dangling symlink, an empty directory and a plain file are equally refused.
	if err := os.Symlink(filepath.Join(victim, "nowhere"), filepath.Join(m.TreesDir(), "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(m.TreesDir(), "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(m.TreesDir(), "afile"), "x")
	for _, id := range []string{"dangling", "emptydir", "afile"} {
		if _, err := m.Create(tctx(t), id, CreateOptions{}); !errors.Is(err, ErrExists) {
			t.Errorf("Create(%s) over an existing path: %v", id, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(victim, "nowhere")); err == nil {
		t.Fatal("dangling target created")
	}
}

func TestBaseIsPinnedAndOverridable(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	// the user commits while the session runs
	writeFile(t, filepath.Join(dir, "README.md"), "# moved on\n")
	rawGit(t, dir, "commit", "-aqm", "user commit")
	newHead, _ := repo.Head(tctx(t))
	b := mustCreate(t, m, "b")
	if a.Base != b.Base || b.Base == newHead {
		t.Fatalf("base drifted: a=%s b=%s HEAD=%s", a.Base, b.Base, newHead)
	}
	if readFile(t, filepath.Join(b.Path, "README.md")) != "# project\n" {
		t.Fatal("tree b saw the user's later commit")
	}
	c := mustCreate(t, m, "c", CreateOptions{Base: "main"})
	if c.Base != newHead || readFile(t, filepath.Join(c.Path, "README.md")) != "# moved on\n" {
		t.Fatalf("explicit Base ignored: %s", c.Base)
	}
	d := mustCreate(t, m, "d", CreateOptions{Detach: true})
	if d.Branch != "" || d.Base != a.Base {
		t.Fatalf("detached tree: %+v", d)
	}
	if br, _ := d.repo.Branch(tctx(t)); br != "" {
		t.Fatalf("detached tree is on branch %q", br)
	}
	// Base configured on the manager
	m2 := &Manager{Repo: repo, Dir: filepath.Join(t.TempDir(), "t2"), Prefix: "sleipnir/s2", Base: "HEAD~1"}
	e := mustCreate(t, m2, "e")
	if e.Base != a.Base {
		t.Fatalf("Manager.Base HEAD~1 = %s, want %s", e.Base, a.Base)
	}
}

func TestDirtyBase(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	// the user has uncommitted work: a modified tracked file and a new file
	writeFile(t, filepath.Join(dir, "README.md"), "# user edit in progress\n")
	writeFile(t, filepath.Join(dir, "wip.txt"), "wip\n")

	plain := newManager(t, repo)
	a := mustCreate(t, plain, "a")
	if !plain.SourceDirty() {
		t.Fatal("SourceDirty should report the uncommitted changes")
	}
	if readFile(t, filepath.Join(a.Path, "README.md")) != "# project\n" || exists(filepath.Join(a.Path, "wip.txt")) {
		t.Fatal("default base must be the committed HEAD, not the user's work in progress")
	}

	snap := newManager(t, repo)
	snap.Snapshot = true
	snap.Prefix = "sleipnir/s9"
	b := mustCreate(t, snap, "b")
	if readFile(t, filepath.Join(b.Path, "README.md")) != "# user edit in progress\n" || readFile(t, filepath.Join(b.Path, "wip.txt")) != "wip\n" {
		t.Fatal("Snapshot base lacks the user's uncommitted work")
	}
	head, _ := repo.Head(tctx(t))
	if b.Base == head {
		t.Fatal("Snapshot base should be a new commit on top of HEAD")
	}
	c, err := repo.CommitInfo(tctx(t), b.Base)
	if err != nil || len(c.Parents) != 1 || c.Parents[0] != head {
		t.Fatalf("snapshot commit: %+v, %v", c, err)
	}
	if ch, _ := b.Changed(tctx(t)); len(ch) != 0 {
		t.Fatalf("a fresh snapshot tree reports changes: %v", ch)
	}
	// Nothing in the user's checkout moved: still dirty in exactly the same way,
	// nothing staged, branch unchanged.
	st, _ := repo.Status(tctx(t))
	if len(st.Staged) != 0 || len(st.Unstaged) != 1 || len(st.Untracked) != 1 || st.Head != head {
		t.Fatalf("user's checkout was disturbed: %+v", st)
	}
	// the base commit is anchored by a branch so gc cannot take it
	if sha, err := repo.BranchSHA(tctx(t), "sleipnir/s9/_base"); err != nil || sha != b.Base {
		t.Fatalf("base anchor: %s, %v", sha, err)
	}
	// A fresh tree can be removed without force: the snapshot commit is not agent work.
	if err := b.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove of an untouched snapshot tree: %v", err)
	}
	// A clean checkout is not "dirty".
	rawGit(t, dir, "checkout", "-q", "--", "README.md")
	if err := os.Remove(filepath.Join(dir, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	clean := newManager(t, repo)
	clean.Prefix = "sleipnir/s3"
	mustCreate(t, clean, "z")
	if clean.SourceDirty() {
		t.Fatal("clean source reported dirty")
	}
}

func TestDetachedHeadRepository(t *testing.T) {
	dir := newRepo(t)
	rawGit(t, dir, "checkout", "-q", "--detach", "main")
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	edit(t, a, "README.md", "# detached\n")
	q := mustQueue(t, m, QueueOptions{})
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("submit: %+v", r)
	}
	// there is no branch to fast-forward
	if _, err := q.FastForward(tctx(t)); !errors.Is(err, ErrNotFastForward) {
		t.Fatalf("FastForward on a detached HEAD: %v", err)
	}
	patch, err := q.Finish(tctx(t))
	if err != nil || !strings.Contains(patch, "# detached") {
		t.Fatalf("Finish: %q, %v", patch, err)
	}
	if head, _ := repo.Head(tctx(t)); head != m.BaseSHA() {
		t.Fatal("detached HEAD moved")
	}
}

func TestBareRepository(t *testing.T) {
	dir := newRepo(t)
	bare := filepath.Join(t.TempDir(), "hub.git")
	rawGit(t, dir, "clone", "-q", "--bare", ".", bare)
	repo := openRepo(t, bare)
	if !repo.IsBare() {
		t.Fatal("not bare")
	}
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	if readFile(t, filepath.Join(a.Path, "README.md")) != "# project\n" {
		t.Fatal("tree from a bare repository is empty")
	}
	edit(t, a, "docs/guide.md", "# guide\n\nedited\n")
	q := mustQueue(t, m, QueueOptions{VerifyCmd: "test -f README.md"})
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("submit: %+v", r)
	}
	// FastForward in a bare repository moves the branch HEAD points at
	ff, err := q.FastForward(tctx(t))
	if err != nil || ff.Branch != "main" || ff.To != q.Tip() {
		t.Fatalf("FastForward: %+v, %v", ff, err)
	}
	if sha, _ := repo.BranchSHA(tctx(t), "main"); sha != q.Tip() {
		t.Fatal("bare branch did not move")
	}
	// Copy extras need a work tree to copy from.
	if _, err := m.Create(tctx(t), "b", CreateOptions{Copy: []string{".env"}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Copy from a bare repository: %v", err)
	}
	if exists(filepath.Join(m.TreesDir(), "b")) {
		t.Fatal("failed Create left a directory")
	}
}

func TestRepositoryWithoutCommits(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "-q", "-b", "main", ".")
	writeFile(t, filepath.Join(dir, "f.txt"), "x\n")
	m := newManager(t, openRepo(t, dir))
	if _, err := m.Create(tctx(t), "a", CreateOptions{}); !errors.Is(err, ErrNoCommits) {
		t.Fatalf("want ErrNoCommits, got %v", err)
	}
}

func TestUnicodeAndSpacesEverywhere(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	dir := filepath.Join(root, "my project é 日本語")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "init", "-q", "-b", "main", ".")
	writeFile(t, filepath.Join(dir, "ü file.txt"), "one\n")
	writeFile(t, filepath.Join(dir, "sub dir", "ñ.txt"), "two\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "init")
	repo := openRepo(t, dir)
	m := &Manager{Repo: repo, Dir: filepath.Join(root, "trees dir ü"), Prefix: "sleipnir/s1", Clock: testClock()}
	a := mustCreate(t, m, "be-1")
	if !strings.Contains(a.Path, "trees dir ü") {
		t.Fatalf("path %s", a.Path)
	}
	edit(t, a, "ü file.txt", "one edited\n")
	edit(t, a, "sub dir/new ✓.txt", "three\n")
	ch, err := a.Changed(tctx(t))
	if err != nil || strings.Join(ch, "|") != "sub dir/new ✓.txt|ü file.txt" {
		t.Fatalf("Changed = %q, %v", ch, err)
	}
	patch, err := a.Diff(tctx(t))
	if err != nil || !strings.Contains(patch, "new ✓.txt") {
		t.Fatalf("Diff: %v", err)
	}
	q := mustQueue(t, m, QueueOptions{VerifyCmd: "test -f 'ü file.txt'"})
	if r := mustSubmit(t, q, Submission{Tree: a, Task: "unicode ✓ task"}); !r.Merged() || len(r.Files) != 2 {
		t.Fatalf("submit: %+v", r)
	}
	if err := a.Remove(tctx(t), false); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinksInsideTheProject(t *testing.T) {
	dir := newRepo(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "outside\n")
	for name, target := range map[string]string{"link-rel": "README.md", "link-abs": outside, "link-dir": "docs", "link-up": "../elsewhere"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skip("no symlinks")
		}
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "links")
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	for name, target := range map[string]string{"link-rel": "README.md", "link-abs": outside, "link-dir": "docs", "link-up": "../elsewhere"} {
		got, err := os.Readlink(filepath.Join(a.Path, name))
		if err != nil || got != target {
			t.Fatalf("%s -> %q, %v (want %q)", name, got, err, target)
		}
	}
	// Changes are found through the tree's own paths, and git does not follow the
	// link when recording: replacing a file with a link to outside is one changed path.
	if err := os.Remove(filepath.Join(a.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(a.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	ch, err := a.Changed(tctx(t))
	if err != nil || len(ch) != 1 || ch[0] != "README.md" {
		t.Fatalf("Changed = %v, %v", ch, err)
	}
	if _, err := a.Commit(tctx(t), "replace with symlink"); err != nil {
		t.Fatal(err)
	}
	// content of the outside file is not in the repository, only the link text
	if blob, err := a.repo.Show(tctx(t), "HEAD", "README.md"); err != nil || string(blob) != filepath.Join(outside, "secret.txt") {
		t.Fatalf("symlink stored as %q, %v", blob, err)
	}
	// Copy extras never write through a symlink that is in the tree
	writeFile(t, filepath.Join(dir, "docs", "extra.env"), "SECRET=1\n")
	_, err = m.Create(tctx(t), "b", CreateOptions{Copy: []string{"link-dir/extra.env"}})
	// link-dir/extra.env exists in the source through the link; the tree has link-dir as a link too
	if err == nil {
		t.Fatal("a copy into a path that traverses a symlink was accepted")
	}
	if exists(filepath.Join(m.TreesDir(), "b")) {
		t.Fatal("failed Create left its directory")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "docs")); len(ents) != 2 {
		t.Fatalf("docs changed: %v", ents)
	}
}

func TestBinaryRenameDeleteAndModeChanges(t *testing.T) {
	dir := newRepo(t)
	bin := make([]byte, 6000)
	for i := range bin {
		bin[i] = byte(i * 31)
	}
	if err := os.WriteFile(filepath.Join(dir, "asset.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "asset")
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")

	bin2 := append(append([]byte(nil), bin...), 0, 0, 1, 2, 3)
	bin2[10] ^= 0xff
	if err := os.WriteFile(filepath.Join(a.Path, "asset.bin"), bin2, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := osRename(filepath.Join(a.Path, "internal", "util", "util.go"), filepath.Join(a.Path, "internal", "util", "math.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.Path, "docs", "guide.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(a.Path, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(a.Path, "scripts", "ok.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := a.Changed(tctx(t))
	want := []string{"README.md", "asset.bin", "docs/guide.md", "internal/util/math.go", "internal/util/util.go", "scripts/ok.sh"}
	if err != nil || strings.Join(ch, ",") != strings.Join(want, ",") {
		t.Fatalf("Changed = %v, %v", ch, err)
	}
	patch, err := a.Diff(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, need := range []string{"GIT binary patch", "rename from internal/util/util.go", "deleted file mode", "old mode 100644", "new mode 100755", "old mode 100755", "new mode 100644"} {
		if !strings.Contains(patch, need) {
			t.Errorf("patch lacks %q", need)
		}
	}
	q := mustQueue(t, m, QueueOptions{})
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("submit: %+v", r)
	}
	// integration tree content is exactly the agent's tree content
	want2 := projectFiles(t, a.Path)
	got2 := projectFiles(t, q.tree.Path)
	if len(want2) != len(got2) {
		t.Fatalf("file sets differ: %d vs %d", len(want2), len(got2))
	}
	for name, c := range want2 {
		if got2[name] != c {
			t.Fatalf("%s differs in the integration tree", name)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(q.tree.Path, "asset.bin")); !bytes.Equal(b, bin2) {
		t.Fatal("binary content corrupted")
	}
	// the patch reproduces the result on a pristine checkout of the base
	fresh := filepath.Join(t.TempDir(), "fresh")
	rawGit(t, dir, "worktree", "add", "-q", "--detach", fresh, q.Base())
	final, err := q.Finish(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := openRepo(t, fresh).Apply(tctx(t), final, false); err != nil {
		t.Fatalf("Finish patch does not apply: %v", err)
	}
	applied := projectFiles(t, fresh)
	for name, c := range want2 {
		if applied[name] != c {
			t.Fatalf("%s differs after applying the final patch", name)
		}
	}
}

func TestSparseTrees(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	full := mustCreate(t, m, "full")
	sp := mustCreate(t, m, "sparse", CreateOptions{Sparse: []string{"internal/core"}})
	if len(projectFiles(t, sp.Path)) >= len(projectFiles(t, full.Path)) {
		t.Fatal("sparse tree is not smaller")
	}
	for _, must := range []string{"README.md", "go.mod", "internal/core/core.go"} {
		if !exists(filepath.Join(sp.Path, must)) {
			t.Errorf("sparse tree lacks %s", must)
		}
	}
	for _, mustNot := range []string{"cmd/app/main.go", "docs/guide.md", "internal/util/util.go", "data"} {
		if exists(filepath.Join(sp.Path, mustNot)) {
			t.Errorf("sparse tree has %s", mustNot)
		}
	}
	// the user's own checkout is still complete
	if !exists(filepath.Join(dir, "docs", "guide.md")) || !exists(filepath.Join(dir, "cmd", "app", "main.go")) {
		t.Fatal("the user's checkout lost files")
	}
	if ok, _ := repo.IsClean(tctx(t)); !ok {
		t.Fatal("the user's checkout is dirty")
	}
	edit(t, sp, "internal/core/core.go", "package core\n\nfunc Name() string { return \"sparse\" }\n\nfunc Version() int { return 2 }\n\nfunc Extra() int { return 0 }\n")
	edit(t, sp, "internal/core/added.go", "package core\n")
	ch, err := sp.Changed(tctx(t))
	if err != nil || strings.Join(ch, ",") != "internal/core/added.go,internal/core/core.go" {
		t.Fatalf("Changed in a sparse tree = %v, %v (files outside the cone must not look deleted)", ch, err)
	}
	patch, err := sp.Diff(tctx(t))
	if err != nil || strings.Contains(patch, "deleted file") {
		t.Fatalf("Diff in a sparse tree: %v\n%s", err, patch)
	}
	q := mustQueue(t, m, QueueOptions{VerifyCmd: "test -f docs/guide.md"}) // the integration tree is complete
	r := mustSubmit(t, q, Submission{Tree: sp, Task: "sparse work"})
	if !r.Merged() || len(r.Files) != 2 {
		t.Fatalf("submit: %+v", r)
	}
	// nothing outside the cone was lost by the merge
	if !exists(filepath.Join(q.tree.Path, "docs", "guide.md")) || !exists(filepath.Join(q.tree.Path, "internal", "util", "util.go")) {
		t.Fatal("integration tree lost files")
	}
	// a sparse tree can be reset
	edit(t, sp, "internal/core/junk.go", "x")
	if err := sp.Reset(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if ch, _ := sp.Changed(tctx(t)); len(ch) != 0 || exists(filepath.Join(sp.Path, "internal", "core", "junk.go")) {
		t.Fatalf("Reset in a sparse tree: %v", ch)
	}
	if exists(filepath.Join(sp.Path, "docs")) {
		t.Fatal("Reset populated files outside the cone")
	}
	if _, err := m.Create(tctx(t), "bad-sparse", CreateOptions{Sparse: []string{"../escape"}}); err == nil {
		t.Fatal("escaping sparse path accepted")
	}
	if exists(filepath.Join(m.TreesDir(), "bad-sparse")) {
		t.Fatal("failed Create left a directory")
	}
}

func TestCopyExtras(t *testing.T) {
	dir := newRepo(t)
	rawGit(t, dir, "config", "core.excludesFile", "/dev/null")
	writeFile(t, filepath.Join(dir, ".gitignore"), ".env\nvendor/\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "ignore")
	writeFile(t, filepath.Join(dir, ".env"), "KEY=value\n")
	writeFile(t, filepath.Join(dir, "vendor", "lib", "x.go"), "package lib\n")
	if err := os.Chmod(filepath.Join(dir, ".env"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a", CreateOptions{Copy: []string{".env", "vendor", "missing.txt", "docs/guide.md"}})
	if readFile(t, filepath.Join(a.Path, ".env")) != "KEY=value\n" || readFile(t, filepath.Join(a.Path, "vendor", "lib", "x.go")) != "package lib\n" {
		t.Fatal("extras not copied")
	}
	if fi, _ := os.Stat(filepath.Join(a.Path, ".env")); fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %v", fi.Mode().Perm())
	}
	// ignored files are not changes
	if ch, _ := a.Changed(tctx(t)); len(ch) != 0 {
		t.Fatalf("extras show up as changes: %v", ch)
	}
	for _, bad := range []string{"../outside", "/etc/passwd", ".git/config", ".git", "a/../../b"} {
		if _, err := m.Create(tctx(t), "x", CreateOptions{Copy: []string{bad}}); !errors.Is(err, ErrBadName) {
			t.Errorf("Copy %q: %v, want ErrBadName", bad, err)
		}
		if exists(filepath.Join(m.TreesDir(), "x")) {
			t.Fatalf("Copy %q left a tree behind", bad)
		}
	}
}

func TestMaxFileBytes(t *testing.T) {
	dir := newRepo(t)
	m := newManager(t, openRepo(t, dir))
	m.MaxFileBytes = 1000
	a := mustCreate(t, m, "a")
	before, _ := a.Head(tctx(t))
	edit(t, a, "notes.txt", "small\n")
	edit(t, a, "core.dump", strings.Repeat("x", 5000))
	edit(t, a, "deep/er/huge.bin", strings.Repeat("y", 3000))
	_, err := a.Commit(tctx(t), "oops")
	var tl *TooLargeError
	if !errors.Is(err, ErrTooLarge) || !errors.As(err, &tl) || strings.Join(tl.Files, ",") != "core.dump,deep/er/huge.bin" {
		t.Fatalf("Commit = %v", err)
	}
	if head, _ := a.Head(tctx(t)); head != before {
		t.Fatal("a refused commit moved the branch")
	}
	if st, _ := a.repo.Status(tctx(t)); len(st.Staged) != 0 {
		t.Fatalf("a refused commit staged files: %+v", st.Staged)
	}
	for _, f := range []string{"core.dump", "deep"} {
		if err := os.RemoveAll(filepath.Join(a.Path, f)); err != nil {
			t.Fatal(err)
		}
	}
	if sha, err := a.Commit(tctx(t), "fine now"); err != nil || sha == "" {
		t.Fatalf("Commit after cleanup: %q, %v", sha, err)
	}
	m2 := newManager(t, openRepo(t, dir))
	m2.Prefix = "sleipnir/s2"
	m2.MaxFileBytes = -1
	b := mustCreate(t, m2, "b")
	edit(t, b, "big.bin", strings.Repeat("z", 200_000))
	if _, err := b.Commit(tctx(t), "big allowed"); err != nil {
		t.Fatalf("check disabled: %v", err)
	}
}

func TestResetReturnsToBase(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, filepath.Join(dir, ".gitignore"), "cache/\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "ignore cache")
	m := newManager(t, openRepo(t, dir))
	a := mustCreate(t, m, "a")
	edit(t, a, "README.md", "# one\n")
	if _, err := a.Commit(tctx(t), "first"); err != nil {
		t.Fatal(err)
	}
	edit(t, a, "README.md", "# two\n")
	edit(t, a, "untracked/file.txt", "u\n")
	edit(t, a, "cache/build.o", "object\n")
	if err := os.Remove(filepath.Join(a.Path, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if dirty, _ := a.Dirty(tctx(t)); !dirty {
		t.Fatal("Dirty")
	}
	if err := a.Reset(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(a.Path, "README.md")) != "# project\n" || !exists(filepath.Join(a.Path, "go.mod")) {
		t.Fatal("tracked files not restored")
	}
	if exists(filepath.Join(a.Path, "untracked")) {
		t.Fatal("untracked files survived")
	}
	if !exists(filepath.Join(a.Path, "cache", "build.o")) {
		t.Fatal("ignored files must be kept (build caches)")
	}
	if head, _ := a.Head(tctx(t)); head != a.Base {
		t.Fatalf("branch did not return to the base: %s", head)
	}
	if ch, _ := a.Changed(tctx(t)); len(ch) != 0 {
		t.Fatalf("Changed after Reset: %v", ch)
	}
	// Reset also abandons a merge in progress
	b := mustCreate(t, m, "b")
	if _, err := os.Stat(b.Path); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(tctx(t), false); err != nil {
		t.Fatal(err)
	}
	if err := b.Reset(tctx(t)); !errors.Is(err, ErrRemoved) {
		t.Fatalf("Reset after Remove: %v", err)
	}
}

func TestReuseAndAdopt(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	edit(t, a, "README.md", "# work in progress\n")
	if _, err := a.Commit(tctx(t), "wip"); err != nil {
		t.Fatal(err)
	}
	// same manager: Reuse hands back the live tree
	again, err := m.Create(tctx(t), "a", CreateOptions{Reuse: true})
	if err != nil || again != a {
		t.Fatalf("Reuse in the same manager: %v, %v", again, err)
	}
	// a second manager (the same session after a restart) cannot take a live tree ...
	m2 := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := m2.Create(tctx(t), "a", CreateOptions{Reuse: true}); !errors.Is(err, ErrExists) {
		t.Fatalf("a running manager's tree was adopted by another manager: %v", err)
	}
	// ... but once its owner is dead it adopts it, keeping the base and the work
	orphan(t, a)
	m3 := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	adopted, err := m3.Create(tctx(t), "a", CreateOptions{Reuse: true})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if adopted.Base != a.Base || adopted.Branch != a.Branch || adopted.Path != a.Path {
		t.Fatalf("adopted tree: %+v vs %+v", adopted, a)
	}
	if readFile(t, filepath.Join(adopted.Path, "README.md")) != "# work in progress\n" {
		t.Fatal("adopted tree lost its work")
	}
	mk, _ := readMarker(adopted.repo.GitDir())
	if mk.PID != os.Getpid() {
		t.Fatalf("marker not updated to the new owner: %+v", mk)
	}
	// without Reuse an existing directory is never taken over
	orphan(t, adopted)
	m4 := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := m4.Create(tctx(t), "a", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("Create without Reuse: %v", err)
	}
	// a different agent's tree is not adoptable under another id
	if _, err := m4.Create(tctx(t), "b", CreateOptions{Reuse: true}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
}

func TestListShowsOnlyTreesWithOurMarker(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	// a look-alike made by hand: same directory, same branch namespace, no marker
	hand := filepath.Join(m.TreesDir(), "handmade")
	rawGit(t, dir, "worktree", "add", "-q", "-b", "sleipnir/s1/handmade", hand)
	// another prefix
	other := &Manager{Repo: repo, Dir: filepath.Join(t.TempDir(), "other"), Prefix: "sleipnir/s2", Clock: testClock()}
	mustCreate(t, other, "zed")

	list, err := m.List(tctx(t))
	if err != nil || len(list) != 1 || list[0].Agent != "a" || list[0].Path != a.Path || list[0].Owner != "alive" || list[0].Head != a.Base || list[0].Branch != a.Branch || list[0].Created.IsZero() {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if _, err := m.Create(tctx(t), "handmade", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("Create over a hand-made worktree: %v", err)
	}
	// a sweeper with the parent prefix and Dir sees the trees of both managers' prefixes
	// that live under its Dir, and still not the hand-made one
	sweep := &Manager{Repo: repo, Dir: filepath.Dir(m.TreesDir()), Prefix: "sleipnir", Clock: testClock()}
	sl, err := sweep.List(tctx(t))
	if err != nil || len(sl) != 1 || sl[0].Agent != "a" {
		t.Fatalf("sweeper List = %+v, %v", sl, err)
	}
	names := []string{}
	for _, i := range sl {
		names = append(names, i.Agent)
	}
	sort.Strings(names)
}

func TestManagerCloseRemovesItsTrees(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	b := mustCreate(t, m, "b")
	edit(t, b, "README.md", "# work\n")
	if _, err := b.Commit(tctx(t), "work"); err != nil {
		t.Fatal(err)
	}
	// b has unmerged work: Close without force refuses and keeps it, but still removes a
	err := m.Close(context.Background(), false)
	if !errors.Is(err, ErrUnmerged) {
		t.Fatalf("Close: %v", err)
	}
	if exists(a.Path) == false && exists(b.Path) == false {
		t.Fatal("Close removed a tree with unmerged work")
	}
	if !exists(b.Path) {
		t.Fatal("tree with unmerged work is gone")
	}
	if err := m.Close(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if exists(a.Path) || exists(b.Path) {
		t.Fatal("trees survived Close(force)")
	}
	if br, _ := repo.Branches(tctx(t), "sleipnir/s1/"); len(br) != 0 {
		t.Fatalf("branches survived: %+v", br)
	}
	if wts, _ := repo.Worktrees(tctx(t)); len(wts) != 1 {
		t.Fatalf("worktrees survived: %+v", wts)
	}
}
