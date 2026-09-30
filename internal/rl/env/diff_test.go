package env

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// treeState captures everything that matters about a tree: file kinds, modes,
// content and symlink targets. .git is excluded.
func treeState(t testing.TB, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			out[filepath.ToSlash(rel)] = "symlink:" + l
		case fi.IsDir():
			// directories are implied by their files; empty ones are not tracked by git
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			exec := "-"
			if fi.Mode().Perm()&0o100 != 0 {
				exec = "x"
			}
			out[filepath.ToSlash(rel)] = fmt.Sprintf("file:%s:%x", exec, b)
		default:
			out[filepath.ToSlash(rel)] = "special:" + fi.Mode().String()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffStates(a, b map[string]string) []string {
	var d []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			d = append(d, "only in first: "+k)
		} else if v != w {
			d = append(d, "differs: "+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			d = append(d, "only in second: "+k)
		}
	}
	sort.Strings(d)
	return d
}

// prepared returns a manager, a workspace on the mathx fixture and the task.
func prepared(t *testing.T) (*Workspaces, *Workspace) {
	t.Helper()
	r, base := mathxRepo(t)
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Cleanup() })
	return m, w
}

// applyToCheckout applies patch to a fresh checkout of w's snapshot.
func applyToCheckout(t *testing.T, m *Workspaces, w *Workspace, patch []byte) string {
	t.Helper()
	co, err := m.newCheckout(ctxT(t), w.snap, w.task, "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.removeCheckout(co) })
	if err := m.applyPatch(ctxT(t), co.tree, patch); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return co.tree
}

func TestDiffRoundTripsEveryKindOfChange(t *testing.T) {
	m, w := prepared(t)
	root := w.Root
	// modify, add, delete, exec bit, symlink, binary, odd names, empty file, CRLF, no EOL
	mustWrite(t, filepath.Join(root, "mathx.go"), fixedMath)
	mustWrite(t, filepath.Join(root, "new.go"), "package mathx\n")
	mustWrite(t, filepath.Join(root, "dir with space", "we ird 'name'.txt"), "odd\n")
	mustWrite(t, filepath.Join(root, "tab\tname.txt"), "tab\n")
	mustWrite(t, filepath.Join(root, "unicode-ünï-çödé.txt"), "u\n")
	mustWrite(t, filepath.Join(root, "-leading-dash"), "dash\n")
	mustWrite(t, filepath.Join(root, "empty.txt"), "")
	mustWrite(t, filepath.Join(root, "crlf.txt"), "a\r\nb\r\n")
	mustWrite(t, filepath.Join(root, "noeol.txt"), "no newline at end")
	bin := append([]byte{0, 1, 2, 3, 0xff, 0xfe, 0}, bytes.Repeat([]byte{0xAA, 0x00}, 5000)...)
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("mathx.go", filepath.Join(root, "alias.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "abs-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "go.mod"), 0o755); err != nil {
		t.Fatal(err)
	}

	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range d.Files {
		got[f.Path] = f.Status + "/" + f.Kind
		if f.Path == "blob.bin" && !f.Binary {
			t.Errorf("blob.bin not detected as binary")
		}
	}
	want := map[string]string{
		"mathx.go": "M/file", "new.go": "A/file", "dir with space/we ird 'name'.txt": "A/file",
		"tab\tname.txt": "A/file", "unicode-ünï-çödé.txt": "A/file", "-leading-dash": "A/file",
		"empty.txt": "A/file", "crlf.txt": "A/file", "noeol.txt": "A/file", "blob.bin": "A/file",
		"run.sh": "A/file", "alias.go": "A/symlink", "abs-link": "A/symlink", "README.md": "D/file", "go.mod": "M/file",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files:\n got %v\nwant %v", got, want)
	}
	if len(d.Skipped) != 0 {
		t.Fatalf("skipped: %v", d.Skipped)
	}
	// Applying the raw diff to a pristine checkout reproduces the workspace exactly.
	co := applyToCheckout(t, m, w, d.Patch)
	if diff := diffStates(treeState(t, root), treeState(t, co)); len(diff) != 0 {
		t.Fatalf("checkout differs from the workspace after applying the diff:\n%s", strings.Join(diff, "\n"))
	}
	// The diff is deterministic.
	d2, err := w.Diff(ctxT(t), 0)
	if err != nil || !bytes.Equal(d.Patch, d2.Patch) {
		t.Fatalf("diff is not deterministic (%v)", err)
	}
	if d.Tree != d2.Tree || d.BaseTree != w.BaseTree {
		t.Fatalf("tree ids: %s %s", d.Tree, d2.Tree)
	}
}

func TestDiffRespectsIgnoreRulesButKeepsTrackedIgnoredFiles(t *testing.T) {
	r := newFixtureRepo(t)
	r.write(".gitignore", "*.log\nbuild/\nvendor/\n")
	r.write("main.go", "package main\n")
	r.write("vendor/dep/dep.go", "package dep\n") // tracked although ignored (git add -f)
	r.git("add", "-A")
	r.git("add", "-f", "vendor/dep/dep.go")
	base := r.commit("start")
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Cleanup()
	if !exists(filepath.Join(w.Root, "vendor", "dep", "dep.go")) {
		t.Fatal("tracked-but-ignored file missing from the workspace")
	}
	mustWrite(t, filepath.Join(w.Root, "debug.log"), "noise")
	mustWrite(t, filepath.Join(w.Root, "build", "out.bin"), "artifact")
	mustWrite(t, filepath.Join(w.Root, "vendor", "dep", "dep.go"), "package dep // edited\n")
	mustWrite(t, filepath.Join(w.Root, "vendor", "untracked.go"), "package vendor\n")
	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range d.Files {
		paths = append(paths, f.Path)
	}
	if !reflect.DeepEqual(paths, []string{"vendor/dep/dep.go"}) {
		t.Fatalf("diff paths = %v, want only the tracked vendor file", paths)
	}
}

func TestDiffSurvivesHostileWorkspaceContent(t *testing.T) {
	m, w := prepared(t)
	root := w.Root
	// A nested repository, with and without commits: `git add -A` aborts on the
	// latter, which would let an agent turn "no diff" into an infra error.
	for _, name := range []string{"nested-empty", "nested-committed"} {
		dir := filepath.Join(root, name)
		mustWrite(t, filepath.Join(dir, "f.txt"), "x")
		cmd := exec.Command("git", "init", "-q", dir)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	// A FIFO and a unix socket: opening the FIFO for reading would block forever.
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Names git or a filesystem could misread as control paths.
	for _, name := range []string{".GIT/config", "git~1/x", ".git./y", "sub/.git ../z"} {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(name)), "boom")
	}
	// Non-UTF-8 name.
	if err := os.WriteFile(filepath.Join(root, "bad-\xff\xfe-name"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "mathx.go"), fixedMath) // the legitimate change
	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatalf("hostile content must not make the diff fail: %v", err)
	}
	var paths []string
	for _, f := range d.Files {
		paths = append(paths, f.Path)
	}
	if !reflect.DeepEqual(paths, []string{"mathx.go"}) {
		t.Fatalf("paths = %v", paths)
	}
	reasons := map[string]string{}
	for _, s := range d.Skipped {
		reasons[s.Path] = s.Reason
	}
	for _, p := range []string{".GIT/config", "git~1/x", ".git./y", "nested-empty/", "nested-committed/"} {
		if reasons[p] == "" {
			t.Errorf("%q should be reported as skipped; got %v", p, reasons)
		}
	}
	// git itself never lists special files, so the FIFO is simply absent (and
	// nothing blocked on opening it).
	// The legitimate change still applies.
	co := applyToCheckout(t, m, w, d.Patch)
	if mustRead(t, filepath.Join(co, "mathx.go")) != fixedMath {
		t.Fatal("legitimate change lost")
	}
	if exists(filepath.Join(co, ".GIT")) || exists(filepath.Join(co, "git~1")) {
		t.Fatal("control-looking paths reached the clean checkout")
	}
}

func TestDiffDoesNotReadThroughReplacedDirectories(t *testing.T) {
	m, w := prepared(t)
	secret := t.TempDir()
	mustWrite(t, filepath.Join(secret, "mathx.go"), "package mathx // SECRET FROM OUTSIDE\n")
	mustWrite(t, filepath.Join(w.Root, "sub", "keep.txt"), "x")
	// Re-snapshot with a tracked directory: build a task whose repo has one.
	r := newFixtureRepo(t)
	r.write("sub/a.txt", "a\n")
	r.write("top.txt", "t\n")
	base := r.commit("start")
	w2, err := m.Prepare(ctxT(t), mathxTask(r, base), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Cleanup()
	// The agent replaces the tracked directory by a symlink to somewhere else.
	if err := os.RemoveAll(filepath.Join(w2.Root, "sub")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(secret, "a.txt"), "SECRET CONTENT\n")
	if err := os.Symlink(secret, filepath.Join(w2.Root, "sub")); err != nil {
		t.Fatal(err)
	}
	d, err := w2.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(d.Patch, []byte("SECRET")) {
		t.Fatalf("content from outside the workspace leaked into the diff:\n%s", d.Patch)
	}
	// It is recorded as a symlink entry, and the old file as deleted.
	got := map[string]string{}
	for _, f := range d.Files {
		got[f.Path] = f.Status + "/" + f.Kind
	}
	if got["sub"] != "A/symlink" || got["sub/a.txt"] != "D/file" {
		t.Fatalf("files = %v", got)
	}
	// And it round-trips.
	co := applyToCheckout(t, m, w2, d.Patch)
	if l, err := os.Readlink(filepath.Join(co, "sub")); err != nil || l != secret {
		t.Fatalf("symlink not reproduced: %q %v", l, err)
	}
}

func TestDiffTypeChanges(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("was-file", "content\n")
	r.write("was-dir/inner.txt", "inner\n")
	r.write("target.txt", "t\n")
	if err := os.Symlink("target.txt", filepath.Join(r.Dir, "was-link")); err != nil {
		t.Fatal(err)
	}
	base := r.commit("start")
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Cleanup()
	root := w.Root
	// file -> symlink, symlink -> file, directory -> file
	os.Remove(filepath.Join(root, "was-file"))
	if err := os.Symlink("target.txt", filepath.Join(root, "was-file")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "was-link"))
	mustWrite(t, filepath.Join(root, "was-link"), "now a file\n")
	os.RemoveAll(filepath.Join(root, "was-dir"))
	mustWrite(t, filepath.Join(root, "was-dir"), "now a file\n")
	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	co := applyToCheckout(t, m, w, d.Patch)
	if diff := diffStates(treeState(t, root), treeState(t, co)); len(diff) != 0 {
		t.Fatalf("type changes do not round-trip:\n%s\npatch:\n%s", strings.Join(diff, "\n"), d.Patch)
	}
}

func TestDiffLimits(t *testing.T) {
	t.Run("patch too large is an agent failure", func(t *testing.T) {
		_, w := prepared(t)
		mustWrite(t, filepath.Join(w.Root, "huge.txt"), strings.Repeat("line of text\n", 200_000))
		_, err := w.Diff(ctxT(t), 64<<10)
		if err == nil || !IsAgentLimit(err) || IsInfra(err) {
			t.Fatalf("want an agent limit error, got %v", err)
		}
	})
	t.Run("oversized file is skipped and reported", func(t *testing.T) {
		r, base := mathxRepo(t)
		m := newManager(t, func(o *WorkspaceOptions) { o.MaxFileBytes = 1 << 10 })
		w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
		if err != nil {
			t.Fatal(err)
		}
		defer w.Cleanup()
		mustWrite(t, filepath.Join(w.Root, "big.dat"), strings.Repeat("x", 4096))
		mustWrite(t, filepath.Join(w.Root, "small.dat"), "ok")
		d, err := w.Diff(ctxT(t), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Files) != 1 || d.Files[0].Path != "small.dat" || len(d.Skipped) != 1 || d.Skipped[0].Path != "big.dat" {
			t.Fatalf("files %v skipped %v", d.Files, d.Skipped)
		}
	})
	t.Run("too many files is an agent failure", func(t *testing.T) {
		r, base := mathxRepo(t)
		m := newManager(t, func(o *WorkspaceOptions) { o.MaxFiles = 50 })
		w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
		if err != nil {
			t.Fatal(err)
		}
		defer w.Cleanup()
		for i := 0; i < 60; i++ {
			mustWrite(t, filepath.Join(w.Root, "many", fmt.Sprintf("f%03d", i)), "x")
		}
		if _, err := w.Diff(ctxT(t), 0); err == nil || !IsAgentLimit(err) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestDiffOfCancelledContext(t *testing.T) {
	_, w := prepared(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Diff(ctx, 0); err == nil || IsInfra(err) {
		t.Fatalf("cancelled diff: %v", err)
	}
}

func TestDiffConcurrentWorkspaces(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	task := mathxTask(r, base)
	const n = 6
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			w, err := m.Prepare(ctxT(t), task, fmt.Sprintf("s%d", i))
			if err != nil {
				errs <- err
				return
			}
			defer w.Cleanup()
			content := fmt.Sprintf("package mathx // variant %d\n", i)
			if err := os.WriteFile(filepath.Join(w.Root, "mathx.go"), []byte(content), 0o644); err != nil {
				errs <- err
				return
			}
			d, err := w.Diff(ctxT(t), 0)
			if err != nil {
				errs <- err
				return
			}
			if len(d.Files) != 1 || !bytes.Contains(d.Patch, []byte(fmt.Sprintf("variant %d", i))) {
				errs <- fmt.Errorf("sample %d got someone else's diff: %s", i, d.Patch)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestParsePatchRejectsRenames(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("a.txt", strings.Repeat("some content that is long enough to match\n", 20))
	r.commit("one")
	r.git("mv", "a.txt", "b.txt")
	patch := r.git("diff", "--cached", "-M", "--binary", "--full-index")
	if !strings.Contains(patch, "rename from") {
		t.Fatalf("fixture did not produce a rename:\n%s", patch)
	}
	m := newManager(t)
	if _, err := m.parsePatch(ctxT(t), []byte(patch+"\n")); err == nil {
		t.Fatal("rename sections must be rejected")
	}
	// The same change without rename detection parses fine (delete + add).
	plain := r.git("diff", "--cached", "--no-renames", "--binary", "--full-index")
	files, err := m.parsePatch(ctxT(t), []byte(plain+"\n"))
	if err != nil || len(files) != 2 {
		t.Fatalf("%v %v", files, err)
	}
}

func TestParsePatchMalformed(t *testing.T) {
	m := newManager(t)
	for _, in := range []string{"garbage", "not a diff\ndiff --git a/x b/x\n"} {
		if _, err := m.parsePatch(ctxT(t), []byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if files, err := m.parsePatch(ctxT(t), nil); err != nil || files != nil {
		t.Errorf("empty patch: %v %v", files, err)
	}
}

func TestSplitSectionsIgnoresLookalikeContent(t *testing.T) {
	// A file whose content contains a diff header must not split the section:
	// hunk lines are prefixed.
	m, w := prepared(t)
	mustWrite(t, filepath.Join(w.Root, "evil.txt"), "diff --git a/mathx_test.go b/mathx_test.go\nnew file mode 100644\n+++ b/x\n")
	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "evil.txt" {
		t.Fatalf("files = %+v", d.Files)
	}
	secs, err := splitSections(d.Patch)
	if err != nil || len(secs) != 1 {
		t.Fatalf("sections: %d %v", len(secs), err)
	}
	co := applyToCheckout(t, m, w, d.Patch)
	if mustRead(t, filepath.Join(co, "evil.txt")) != mustRead(t, filepath.Join(w.Root, "evil.txt")) {
		t.Fatal("content changed")
	}
}

func TestFilterPatch(t *testing.T) {
	m, w := prepared(t)
	root := w.Root
	mustWrite(t, filepath.Join(root, "mathx.go"), fixedMath)                                        // allowed
	mustWrite(t, filepath.Join(root, "mathx_test.go"), "package mathx // weakened\n")               // protected by *_test.go
	mustWrite(t, filepath.Join(root, "sub", "deep_test.go"), "package sub\n")                       // protected, new
	mustWrite(t, filepath.Join(root, "go.mod"), "module evil\n")                                    // protected
	mustWrite(t, filepath.Join(root, "MATHX_HIDDEN_TEST.GO"), "package mathx\n")                    // protected by case-insensitive glob
	mustWrite(t, filepath.Join(root, "helper.txt"), "hello\n")                                      // allowed
	mustWrite(t, filepath.Join(root, "conftest.data"), "x")                                         // allowed
	mustWrite(t, filepath.Join(root, "mathx_hidden_test.go"), "package mathx // pre-empt hidden\n") // implicitly protected (hidden path)
	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	prot, _ := CompileGlobs([]string{"*_test.go", "go.mod"})
	hidden := map[string]bool{foldPath("mathx_hidden_test.go"): true}
	fr := filterPatch(d.Files, prot, hidden)
	wantProt := []string{"MATHX_HIDDEN_TEST.GO", "go.mod", "mathx_hidden_test.go", "mathx_test.go", "sub/deep_test.go"}
	if !reflect.DeepEqual(fr.Protected, wantProt) {
		t.Fatalf("protected touched = %v, want %v", fr.Protected, wantProt)
	}
	var applied []string
	for _, f := range fr.Applied {
		applied = append(applied, f.Path)
	}
	sort.Strings(applied)
	if !reflect.DeepEqual(applied, []string{"conftest.data", "helper.txt", "mathx.go"}) {
		t.Fatalf("applied = %v", applied)
	}
	co := applyToCheckout(t, m, w, fr.Patch)
	if mustRead(t, filepath.Join(co, "mathx.go")) != fixedMath {
		t.Fatal("allowed change not applied")
	}
	if mustRead(t, filepath.Join(co, "mathx_test.go")) != visibleTest {
		t.Fatal("protected test file was modified in the clean checkout")
	}
	if mustRead(t, filepath.Join(co, "go.mod")) != goMod {
		t.Fatal("protected go.mod was modified")
	}
	if exists(filepath.Join(co, "sub", "deep_test.go")) || exists(filepath.Join(co, "MATHX_HIDDEN_TEST.GO")) {
		t.Fatal("protected new files reached the checkout")
	}
}

func TestFilterPatchDropsGitlinksAndUnsafePaths(t *testing.T) {
	files := []PatchFile{
		{Path: "ok.go", Kind: "file", body: []byte("diff --git a/ok.go b/ok.go\n")},
		{Path: "sub", Kind: "gitlink", body: []byte("diff --git a/sub b/sub\n")},
		{Path: ".git/hooks/pre-commit", Kind: "file", body: []byte("diff --git a/x b/x\n")},
		{Path: "../escape", Kind: "file", body: []byte("diff --git a/x b/x\n")},
		{Path: "/etc/passwd", Kind: "file", body: []byte("diff --git a/x b/x\n")},
	}
	fr := filterPatch(files, nil, nil)
	if len(fr.Applied) != 1 || fr.Applied[0].Path != "ok.go" {
		t.Fatalf("applied: %v", fr.Applied)
	}
	if len(fr.Skipped) != 4 {
		t.Fatalf("skipped: %v", fr.Skipped)
	}
	if len(fr.Protected) != 0 {
		t.Fatalf("protected: %v", fr.Protected)
	}
}

func TestApplyPatchFailureIsNotInfra(t *testing.T) {
	m, w := prepared(t)
	mustWrite(t, filepath.Join(w.Root, "mathx.go"), fixedMath)
	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	co, err := m.newCheckout(ctxT(t), w.snap, w.task, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer m.removeCheckout(co)
	// The checkout no longer matches the diff's base (something else edited it).
	mustWrite(t, filepath.Join(co.tree, "mathx.go"), "package mathx // other\n")
	err = m.applyPatch(ctxT(t), co.tree, d.Patch)
	var rej *errPatchRejected
	if err == nil || !asPatchRejected(err, &rej) || IsInfra(err) {
		t.Fatalf("got %v", err)
	}
}

func asPatchRejected(err error, target **errPatchRejected) bool {
	e, ok := err.(*errPatchRejected)
	if ok {
		*target = e
	}
	return ok
}
