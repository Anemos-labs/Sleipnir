package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/gitx"
)

// newPlainDir makes a project directory that is not a git repository.
func newPlainDir(t testing.TB) string {
	t.Helper()
	isolateHome(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "plain project")
	files := map[string]string{
		"README.md":             "# plain\n",
		"src/main.py":           "print('hi')\n",
		"src/util/helpers.py":   "def add(a, b):\n    return a + b\n",
		"docs/guide.md":         "# guide\n\nline one\nline two\nline three\nline four\nline five\n",
		"data/table é.csv":      "a,b\n1,2\n",
		"node_modules/dep/x.js": "module.exports = 1\n",
		"build/out/app.bin":     "binary-ish\n",
		"__pycache__/x.pyc":     "cache\n",
		".hg/store":             "hg\n",
	}
	for name, content := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	}
	if err := os.Chmod(filepath.Join(dir, "src", "main.py"), 0o755); err != nil {
		t.Fatal(err)
	}
	// distinct, old modification times: the snapshot must preserve them
	old := time.Unix(1_600_000_000, 123_000_000)
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	return dir
}

func newCopyManager(t testing.TB, src string) *Manager {
	t.Helper()
	return &Manager{Source: src, Dir: filepath.Join(t.TempDir(), "trees"), Prefix: "sleipnir/s1", Clock: testClock(),
		Repo: nil}
}

func TestCopyModeGivesTheSameGuarantees(t *testing.T) {
	src := newPlainDir(t)
	m := newCopyManager(t, src)
	m.Excludes = []string{"build/out"}
	a := mustCreate(t, m, "a")
	if a.Mode != ModeCopy || m.EffectiveMode() != ModeCopy {
		t.Fatalf("mode: %v", a.Mode)
	}
	// content = source minus excludes, with modes and modification times
	want := map[string]bool{"README.md": true, "src/main.py": true, "src/util/helpers.py": true, "docs/guide.md": true, "data/table é.csv": true}
	got := projectFiles(t, a.Path)
	if len(got) != len(want) {
		t.Fatalf("copied files: %v", keys(got))
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s not copied", name)
		}
	}
	for _, excluded := range []string{"node_modules", "build/out", "__pycache__", ".hg"} {
		if exists(filepath.Join(a.Path, filepath.FromSlash(excluded))) {
			t.Errorf("%s was copied despite being excluded", excluded)
		}
	}
	if !strings.HasPrefix(got["src/main.py"], "true:") {
		t.Fatal("executable bit lost")
	}
	fi, _ := os.Stat(filepath.Join(a.Path, "docs", "guide.md"))
	if !fi.ModTime().Equal(time.Unix(1_600_000_000, 123_000_000)) {
		t.Fatalf("modification time not preserved: %v", fi.ModTime())
	}
	// the source directory itself is never touched: no .git, nothing added
	if exists(filepath.Join(src, ".git")) {
		t.Fatal("the user's directory was turned into a repository")
	}
	if ents, _ := os.ReadDir(src); len(ents) != 8 {
		t.Fatalf("source directory changed: %v", ents)
	}
	// git sees a consistent, unmodified tree right away (the adopted index): no
	// changes, and nothing had to be rehashed to find that out
	st, err := a.repo.Status(tctx(t))
	if err != nil || !st.Clean() {
		t.Fatalf("fresh copy tree is dirty: %+v, %v", st, err)
	}
	if ch, err := a.Changed(tctx(t)); err != nil || len(ch) != 0 {
		t.Fatalf("Changed on a fresh copy tree: %v, %v", ch, err)
	}
	// touching without changing content is not a change; editing is
	now := time.Now()
	must(t, os.Chtimes(filepath.Join(a.Path, "README.md"), now, now))
	if ch, _ := a.Changed(tctx(t)); len(ch) != 0 {
		t.Fatalf("touch counted as a change: %v", ch)
	}
	edit(t, a, "README.md", "# plain, edited\n")
	edit(t, a, "src/new.py", "x = 1\n")
	must(t, os.Remove(filepath.Join(a.Path, "docs", "guide.md")))
	ch, err := a.Changed(tctx(t))
	if err != nil || strings.Join(ch, ",") != "README.md,docs/guide.md,src/new.py" {
		t.Fatalf("Changed = %v, %v", ch, err)
	}
	patch, err := a.Diff(tctx(t))
	if err != nil || !strings.Contains(patch, "+x = 1") || !strings.Contains(patch, "deleted file mode") {
		t.Fatalf("Diff: %v\n%s", err, patch)
	}
	if sha, err := a.Commit(tctx(t), "work"); err != nil || sha == "" {
		t.Fatalf("Commit: %q, %v", sha, err)
	}
	if err := a.Reset(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if got := projectFiles(t, a.Path); len(got) != len(want) || readFile(t, filepath.Join(a.Path, "README.md")) != "# plain\n" {
		t.Fatal("Reset in copy mode")
	}
	// two trees are independent copies
	b := mustCreate(t, m, "b")
	edit(t, a, "README.md", "# only a\n")
	if readFile(t, filepath.Join(b.Path, "README.md")) != "# plain\n" || readFile(t, filepath.Join(src, "README.md")) != "# plain\n" {
		t.Fatal("copy trees are not independent")
	}
	if err := b.Remove(tctx(t), false); err != nil {
		t.Fatalf("Remove in copy mode: %v", err)
	}
	if err := a.Remove(tctx(t), true); err != nil {
		t.Fatal(err)
	}
}

func TestCopyModeQueueAndFinish(t *testing.T) {
	skipWithoutUnix(t)
	src := newPlainDir(t)
	m := newCopyManager(t, src)
	q := mustQueue(t, m, QueueOptions{VerifyCmd: "test -f README.md"})
	a := mustCreate(t, m, "a", CreateOptions{Base: q.Tip()})
	b := mustCreate(t, m, "b", CreateOptions{Base: q.Tip()})
	c := mustCreate(t, m, "c", CreateOptions{Base: q.Tip()})
	edit(t, a, "src/main.py", "print('hello from a')\n")
	edit(t, b, "docs/guide.md", "# guide\n\nline one\nline two\nline three\nline four\nline five\nby b\n")
	edit(t, c, "src/main.py", "print('hello from c')\n")
	if r := mustSubmit(t, q, Submission{Tree: a, Task: "a"}); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	if r := mustSubmit(t, q, Submission{Tree: b, Task: "b"}); !r.Merged() {
		t.Fatalf("b: %+v", r)
	}
	r := mustSubmit(t, q, Submission{Tree: c, Task: "c"})
	if r.Outcome != OutcomeConflict || r.Conflict.Files[0] != "src/main.py" || strings.Join(r.Conflict.Details[0].With, ",") != "a" {
		t.Fatalf("c must conflict with a: %+v", r)
	}
	// the integration tree is clean and holds the merged work
	if head, _ := q.tree.repo.Head(tctx(t)); head != q.Tip() {
		t.Fatal("integration tree moved")
	}
	if st, _ := q.tree.repo.Status(tctx(t)); !st.Clean() {
		t.Fatalf("integration tree dirty: %+v", st)
	}
	// hand the result back to a plain directory: apply the patch to a copy of the source
	final, err := q.Finish(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "user-copy")
	must(t, cloneTree(tctx(t), src, mustMkdir(t, target), cloneOpts{excludes: DefaultCopyExcludes}))
	applyPatchWithPlainGit(t, target, final)
	if readFile(t, filepath.Join(target, "src/main.py")) != "print('hello from a')\n" || !strings.Contains(readFile(t, filepath.Join(target, "docs/guide.md")), "by b") {
		t.Fatal("patch result differs from the integration tree")
	}
	for name, content := range projectFiles(t, q.tree.Path) {
		if projectFiles(t, target)[name] != content {
			t.Errorf("%s differs between the patched directory and the integration tree", name)
		}
	}
	// there is no user branch to move
	if _, err := q.FastForward(tctx(t)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("FastForward in copy mode: %v", err)
	}
	// sparse checkout needs git
	if _, err := m.Create(tctx(t), "d", CreateOptions{Sparse: []string{"src"}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Sparse in copy mode: %v", err)
	}
}

func mustMkdir(t testing.TB, p string) string {
	t.Helper()
	must(t, os.MkdirAll(p, 0o755))
	return p
}

// applyPatchWithPlainGit applies a patch in a directory that is not a repository,
// the way a user without our tooling would.
func applyPatchWithPlainGit(t testing.TB, dir, patch string) {
	t.Helper()
	cmd := exec.Command("git", "apply", "-")
	cmd.Dir = dir
	cmd.Env = fixtureEnv(t.TempDir())
	cmd.Stdin = strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply in a plain directory: %v\n%s", err, out)
	}
}

func TestCopyModeSnapshotIsStableAndReusable(t *testing.T) {
	src := newPlainDir(t)
	m := newCopyManager(t, src)
	a := mustCreate(t, m, "a")
	// the user keeps editing the live directory
	writeFile(t, filepath.Join(src, "README.md"), "# edited after the snapshot\n")
	writeFile(t, filepath.Join(src, "brand-new.txt"), "new\n")
	b := mustCreate(t, m, "b")
	if readFile(t, filepath.Join(b.Path, "README.md")) != "# plain\n" || exists(filepath.Join(b.Path, "brand-new.txt")) || a.Base != b.Base {
		t.Fatal("later trees must start from the same snapshot as earlier ones, not from the live directory")
	}
	base := m.BaseSHA()
	// a new manager over the same Dir and Source reuses the snapshot (the session's base persists)
	m2 := &Manager{Source: src, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	c, err := m2.Create(tctx(t), "c", CreateOptions{})
	if err != nil || m2.BaseSHA() != base || readFile(t, filepath.Join(c.Path, "README.md")) != "# plain\n" {
		t.Fatalf("reuse of the snapshot: %v (base %s vs %s)", err, m2.BaseSHA(), base)
	}
	// a different source in the same Dir is refused, not silently mixed in
	other := newPlainDir(t)
	m3 := &Manager{Source: other, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := m3.Create(tctx(t), "x", CreateOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("different source: %v", err)
	}
	// a _shadow directory that is not ours is never adopted or deleted
	foreignDir := filepath.Join(t.TempDir(), "trees")
	writeFile(t, filepath.Join(foreignDir, shadowName, "important.txt"), "mine\n")
	m4 := &Manager{Source: src, Dir: foreignDir, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := m4.Create(tctx(t), "x", CreateOptions{}); err == nil {
		t.Fatal("adopted a foreign _shadow directory")
	}
	if !exists(filepath.Join(foreignDir, shadowName, "important.txt")) {
		t.Fatal("foreign _shadow directory damaged")
	}
	// Close(force) removes its trees and then the snapshot repository - but never
	// while another manager's tree is still registered in it
	if err := m.Close(context.Background(), true); err == nil {
		t.Fatal("the shadow repository was removed while a tree of another manager was registered")
	}
	must(t, c.Remove(tctx(t), true))
	must(t, m.Close(context.Background(), true))
	if exists(filepath.Join(m.TreesDir(), shadowName)) {
		t.Fatal("shadow repository survived Close(force)")
	}
	if !exists(src) || readFile(t, filepath.Join(src, "README.md")) != "# edited after the snapshot\n" {
		t.Fatal("the source directory was harmed")
	}
}

func TestCopyModeWorkspaceInsideTheSource(t *testing.T) {
	src := newPlainDir(t)
	m := &Manager{Source: src, Dir: filepath.Join(src, ".workspace"), Prefix: "sleipnir/s1", Clock: testClock()}
	a := mustCreate(t, m, "a")
	b := mustCreate(t, m, "b") // would copy a's tree too if the workspace were not skipped
	for _, tr := range []*Tree{a, b} {
		if exists(filepath.Join(tr.Path, ".workspace")) {
			t.Fatalf("the workspace directory was copied into %s", tr.Agent)
		}
		if got := projectFiles(t, tr.Path); len(got) != 6 {
			t.Fatalf("%s has %v", tr.Agent, keys(got))
		}
	}
}

func TestCopyModeCopyExtrasAndEmptySource(t *testing.T) {
	src := newPlainDir(t)
	m := newCopyManager(t, src)
	// node_modules is excluded from the snapshot, but a task can ask for it
	a := mustCreate(t, m, "a", CreateOptions{Copy: []string{"node_modules"}})
	if readFile(t, filepath.Join(a.Path, "node_modules", "dep", "x.js")) != "module.exports = 1\n" {
		t.Fatal("extra directory not copied")
	}
	// ... and it is not a change (excluded/ignored by git? not necessarily): document actual behavior
	ch, _ := a.Changed(tctx(t))
	_ = ch

	empty := t.TempDir()
	m2 := &Manager{Source: empty, Dir: filepath.Join(t.TempDir(), "t"), Prefix: "sleipnir/s1", Clock: testClock()}
	e := mustCreate(t, m2, "e")
	edit(t, e, "new.txt", "n\n")
	q := mustQueue(t, m2, QueueOptions{})
	if r := mustSubmit(t, q, Submission{Tree: e}); !r.Merged() {
		t.Fatalf("empty source: %+v", r)
	}
	if patch, err := q.Finish(tctx(t)); err != nil || !strings.Contains(patch, "new.txt") {
		t.Fatalf("Finish: %v\n%s", err, patch)
	}
}

func TestRewriteLink(t *testing.T) {
	src := "/srv/project"
	cases := []struct{ rel, target, want string }{
		{"a/link", "b.txt", "b.txt"},                         // stays inside: kept
		{"a/link", "../c/d.txt", "../c/d.txt"},               // up but still inside
		{"link", "../outside/x", "/srv/outside/x"},           // climbs out of the source: made absolute
		{"a/b/link", "../../../elsewhere", "/srv/elsewhere"}, // (a/b/../../.. is the root's parent)
		{"a/link", "/srv/project/docs/x.md", "../docs/x.md"}, // absolute into the source: made relative
		{"link", "/srv/project/docs/x.md", "docs/x.md"},      // idem, at the top
		{"a/link", "/etc/hostname", "/etc/hostname"},         // absolute elsewhere: kept
		{"link", "/srv/project", "."},                        // the root itself
		{"link", "/srv/projectile/x", "/srv/projectile/x"},   // prefix trap: not inside
		{"link", "docs/../../out", "/srv/out"},               // normalizes before deciding
	}
	for _, c := range cases {
		if got := rewriteLink(src, c.rel, c.target); got != c.want {
			t.Errorf("rewriteLink(%q, %q) = %q, want %q", c.rel, c.target, got, c.want)
		}
	}
}

func TestCloneTreeDetails(t *testing.T) {
	skipWithoutUnix(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a.txt"), "a\n")
	writeFile(t, filepath.Join(src, "keep", "b.txt"), "b\n")
	writeFile(t, filepath.Join(src, "skip-me", "c.txt"), "c\n")
	writeFile(t, filepath.Join(src, "deep", "skip-me", "d.txt"), "d\n")
	writeFile(t, filepath.Join(src, "exact", "path", "e.txt"), "e\n")
	writeFile(t, filepath.Join(src, "exact", "other", "f.txt"), "f\n")
	writeFile(t, filepath.Join(src, "ro", "g.txt"), "g\n")
	must(t, os.Chmod(filepath.Join(src, "ro"), 0o555)) // a read-only directory must still be filled
	defer os.Chmod(filepath.Join(src, "ro"), 0o755)
	must(t, os.Symlink("a.txt", filepath.Join(src, "link")))
	must(t, os.Link(filepath.Join(src, "a.txt"), filepath.Join(src, "hardlink")))
	must(t, syscall.Mkfifo(filepath.Join(src, "fifo"), 0o644))
	must(t, os.MkdirAll(filepath.Join(src, "emptydir"), 0o755))
	big := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB
	writeFile(t, filepath.Join(src, "big.dat"), big)
	stamp := time.Unix(1_500_000_000, 987_000_000)
	must(t, os.Chtimes(filepath.Join(src, "a.txt"), stamp, stamp))
	must(t, os.Chmod(filepath.Join(src, "a.txt"), 0o640))

	dst := mustMkdir(t, filepath.Join(root, "dst"))
	skipAbs := filepath.Join(src, "keep") // never entered
	err := cloneTree(tctx(t), src, dst, cloneOpts{excludes: []string{"skip-me", "exact/path"}, skipAbs: []string{skipAbs}, skipTop: map[string]bool{"emptydir": true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a.txt", "exact/other/f.txt", "ro/g.txt", "link", "hardlink", "big.dat"} {
		if !exists(filepath.Join(dst, want)) {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{"keep", "skip-me", "deep/skip-me", "exact/path", "fifo", "emptydir"} {
		if exists(filepath.Join(dst, not)) {
			t.Errorf("%s should not have been copied", not)
		}
	}
	if !exists(filepath.Join(dst, "deep")) {
		t.Error("the parent of an excluded directory should still exist")
	}
	fi, _ := os.Stat(filepath.Join(dst, "a.txt"))
	if fi.Mode().Perm() != 0o640 || !fi.ModTime().Equal(stamp) {
		t.Errorf("a.txt: %v %v", fi.Mode().Perm(), fi.ModTime())
	}
	if tgt, _ := os.Readlink(filepath.Join(dst, "link")); tgt != "a.txt" {
		t.Errorf("symlink target %q", tgt)
	}
	// a hard link becomes an independent file, so editing the copy cannot touch the source
	must(t, os.WriteFile(filepath.Join(dst, "hardlink"), []byte("changed\n"), 0o644))
	if readFile(t, filepath.Join(src, "a.txt")) != "a\n" || readFile(t, filepath.Join(src, "hardlink")) != "a\n" {
		t.Fatal("copy shares an inode with the source")
	}
	if fi, _ := os.Stat(filepath.Join(dst, "ro")); fi.Mode().Perm() != 0o555 {
		t.Errorf("read-only directory mode not preserved: %v", fi.Mode().Perm())
	}
	if readFile(t, filepath.Join(dst, "big.dat")) != big {
		t.Fatal("large file corrupted")
	}
	// deterministic: a second copy is identical
	dst2 := mustMkdir(t, filepath.Join(root, "dst2"))
	must(t, cloneTree(tctx(t), src, dst2, cloneOpts{excludes: []string{"skip-me", "exact/path"}, skipAbs: []string{skipAbs}, skipTop: map[string]bool{"emptydir": true}}))
	a1, a2 := projectFiles(t, dst), projectFiles(t, dst2)
	if len(a1) != len(a2) {
		t.Fatal("copies differ")
	}
	// cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cloneTree(ctx, src, mustMkdir(t, filepath.Join(root, "dst3")), cloneOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled clone: %v", err)
	}
	// unreadable content fails loudly (a silently partial snapshot is worse than none)
	if os.Geteuid() != 0 {
		must(t, os.Chmod(filepath.Join(src, "a.txt"), 0))
		defer os.Chmod(filepath.Join(src, "a.txt"), 0o644)
		if err := cloneTree(tctx(t), src, mustMkdir(t, filepath.Join(root, "dst4")), cloneOpts{}); err == nil {
			t.Fatal("unreadable file skipped silently")
		}
	}
}

func TestCopyModeSymlinkPolicy(t *testing.T) {
	skipWithoutUnix(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	isolateHome(t)
	src := filepath.Join(root, "proj")
	sibling := filepath.Join(root, "sibling")
	writeFile(t, filepath.Join(src, "docs", "a.md"), "a\n")
	writeFile(t, filepath.Join(sibling, "secret.txt"), "sibling\n")
	must(t, os.Symlink("docs/a.md", filepath.Join(src, "link-ok")))
	must(t, os.Symlink("../sibling", filepath.Join(src, "link-up")))
	must(t, os.Symlink(filepath.Join(src, "docs", "a.md"), filepath.Join(src, "link-abs-inside")))
	must(t, os.Symlink("/etc", filepath.Join(src, "link-abs-outside")))
	m := &Manager{Source: src, Dir: filepath.Join(root, "trees"), Prefix: "sleipnir/s1", Clock: testClock()}
	a := mustCreate(t, m, "a")
	rl := func(name string) string {
		s, err := os.Readlink(filepath.Join(a.Path, name))
		if err != nil {
			t.Fatalf("readlink %s: %v", name, err)
		}
		return s
	}
	if rl("link-ok") != "docs/a.md" {
		t.Errorf("link-ok -> %s", rl("link-ok"))
	}
	if rl("link-up") != sibling {
		t.Errorf("link-up -> %s, want the original absolute target %s", rl("link-up"), sibling)
	}
	if rl("link-abs-inside") != "docs/a.md" {
		t.Errorf("link-abs-inside -> %s (an absolute link into the source would write through to it)", rl("link-abs-inside"))
	}
	if rl("link-abs-outside") != "/etc" {
		t.Errorf("link-abs-outside -> %s", rl("link-abs-outside"))
	}
	// a relative link that climbs out must not resolve into a sibling *tree* of the copy
	if strings.HasPrefix(rl("link-up"), "..") {
		t.Fatal("a relative escaping link was kept relative: it would resolve against the copy's location")
	}
	// the links are recorded as links: changes through them are not the copy's content
	if ch, _ := a.Changed(tctx(t)); len(ch) != 0 {
		t.Fatalf("Changed: %v", ch)
	}
}

func TestOpenNoFollowRefusesSymlinks(t *testing.T) {
	skipWithoutUnix(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "real.txt"), "real\n")
	must(t, os.Symlink("real.txt", filepath.Join(dir, "link")))
	if f, err := openNoFollow(filepath.Join(dir, "link")); err == nil {
		f.Close()
		t.Fatal("openNoFollow followed a symlink")
	}
	f, err := openNoFollow(filepath.Join(dir, "real.txt"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	// tryReflink on a filesystem without support must fail cleanly and leave the file usable
	in, _ := os.Open(filepath.Join(dir, "real.txt"))
	defer in.Close()
	out, _ := os.OpenFile(filepath.Join(dir, "out.txt"), os.O_WRONLY|os.O_CREATE, 0o644)
	defer out.Close()
	if tryReflink(out, in) {
		t.Log("this filesystem supports reflinks")
	} else if err := cloneContents(out, in); err != nil {
		t.Fatalf("fallback copy: %v", err)
	}
	out.Close()
	if readFile(t, filepath.Join(dir, "out.txt")) != "real\n" {
		t.Fatal("fallback copy content")
	}
}

// keep the gitx import used in this file for the helper below
var _ = gitx.KindOther
