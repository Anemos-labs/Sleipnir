package env

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// caseInsensitiveFS reports whether dir is on a file system that treats names that differ
// only in case as one file (the default on macOS and Windows).
func caseInsensitiveFS(t testing.TB, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "CaseProbe.tmp")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	_, err := os.Stat(filepath.Join(dir, "caseprobe.TMP"))
	return err == nil
}

func mustWrite(t testing.TB, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestWriteFileInDoesNotFollowSymlinks(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	mustWrite(t, filepath.Join(outside, "victim.txt"), "precious")
	root := filepath.Join(base, "checkout")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// The agent's diff planted symlinks where hidden files must land: a
	// directory link, a link at the final component, and a dangling link.
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim.txt"), filepath.Join(root, "final_test.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "new.txt"), filepath.Join(root, "dangling_test.go")); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	for _, rel := range []string{"linkdir/victim.txt", "linkdir/deep/er.txt", "final_test.go", "dangling_test.go"} {
		if err := writeFileIn(r, rel, []byte("HIDDEN"), 0o644); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	if got := mustRead(t, filepath.Join(outside, "victim.txt")); got != "precious" {
		t.Fatalf("a symlink was followed out of the checkout: victim.txt = %q", got)
	}
	if exists(filepath.Join(outside, "new.txt")) || exists(filepath.Join(outside, "deep")) {
		t.Fatal("files were created outside the checkout")
	}
	for _, rel := range []string{"linkdir/victim.txt", "linkdir/deep/er.txt", "final_test.go", "dangling_test.go"} {
		fi, err := os.Lstat(filepath.Join(root, rel))
		if err != nil || !fi.Mode().IsRegular() {
			t.Errorf("%s is not a regular file inside the checkout: %v %v", rel, fi, err)
		}
		if got := mustRead(t, filepath.Join(root, rel)); got != "HIDDEN" {
			t.Errorf("%s = %q", rel, got)
		}
	}
}

func TestWriteFileInReplacesDirectoryAndSetsExecBit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "x_test.go", "inner", "f"), "in the way")
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if err := writeFileIn(r, "x_test.go", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(root, "x_test.go"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("not a regular file: %v %v", fi, err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("exec bit lost: %v", fi.Mode())
	}
}

func TestWriteFileInRejectsBadPaths(t *testing.T) {
	root := t.TempDir()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	for _, rel := range []string{"../x", "/abs", ".git/config", "a/../../x", ""} {
		if err := writeFileIn(r, rel, []byte("x"), 0o644); err == nil {
			t.Errorf("%q accepted", rel)
		}
	}
}

func TestRemoveAllNoFollow(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	mustWrite(t, filepath.Join(outside, "keep.txt"), "keep")
	mustWrite(t, filepath.Join(outside, "sub", "keep2.txt"), "keep")
	target := filepath.Join(base, "ws")
	mustWrite(t, filepath.Join(target, "a", "b", "c.txt"), "x")
	// Links out of the workspace: to a directory, to a file, dangling, and a loop.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(outside, filepath.Join(target, "dirlink")))
	must(os.Symlink(filepath.Join(outside, "keep.txt"), filepath.Join(target, "a", "filelink")))
	must(os.Symlink("/nonexistent/place", filepath.Join(target, "dangling")))
	must(os.Symlink("loop", filepath.Join(target, "loop")))
	// Read-only directory trees, like the Go module cache.
	mustWrite(t, filepath.Join(target, "mod", "pkg", "f.go"), "package x")
	must(os.Chmod(filepath.Join(target, "mod", "pkg", "f.go"), 0o444))
	must(os.Chmod(filepath.Join(target, "mod", "pkg"), 0o555))
	must(os.Chmod(filepath.Join(target, "mod"), 0o555))
	// An unreadable directory.
	mustWrite(t, filepath.Join(target, "locked", "f"), "x")
	must(os.Chmod(filepath.Join(target, "locked"), 0o000))

	if err := removeAllNoFollow(target); err != nil {
		t.Fatal(err)
	}
	if exists(target) {
		t.Fatal("workspace still exists")
	}
	if got := mustRead(t, filepath.Join(outside, "keep.txt")); got != "keep" {
		t.Fatal("cleanup followed a symlink and damaged the outside file")
	}
	if got := mustRead(t, filepath.Join(outside, "sub", "keep2.txt")); got != "keep" {
		t.Fatal("cleanup followed a directory symlink")
	}
	// Removing something that is gone, or a lone symlink, is fine.
	if err := removeAllNoFollow(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "l")
	must(os.Symlink(outside, link))
	if err := removeAllNoFollow(link); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(outside, "keep.txt")) || exists(link) {
		t.Fatal("removing a top-level symlink must remove only the link")
	}
}

func snapshotTree(t testing.TB, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		line := rel + " " + fi.Mode().String()
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			line += " -> " + l
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			line += " " + string(b)
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func buildSampleTree(t testing.TB, root string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "a.txt"), "alpha")
	mustWrite(t, filepath.Join(root, "sub", "deep", "b.txt"), "beta")
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../a.txt", filepath.Join(root, "sub", "rel")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(root, "abs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, ".git", "config"), "[core]")
}

func TestCloneTreeStrategies(t *testing.T) {
	src := t.TempDir()
	buildSampleTree(t, src)
	want := snapshotTree(t, src)

	t.Run("cp", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dst")
		var c cloner
		if err := c.clone(context.Background(), src, dst); err != nil {
			t.Fatal(err)
		}
		got := snapshotTree(t, dst)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("clone differs:\n got %q\nwant %q", got, want)
		}
	})
	t.Run("go fallback", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dst")
		if err := copyTreeGo(context.Background(), src, dst); err != nil {
			t.Fatal(err)
		}
		if got := snapshotTree(t, dst); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("copyTreeGo differs:\n got %q\nwant %q", got, want)
		}
	})
	t.Run("independent inodes", func(t *testing.T) {
		// Editing the clone must never change the source: that would poison the
		// cached snapshot for every later rollout.
		dst := filepath.Join(t.TempDir(), "dst")
		var c cloner
		if err := c.clone(context.Background(), src, dst); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(dst, "a.txt"), os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("changed"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if got := mustRead(t, filepath.Join(src, "a.txt")); got != "alpha" {
			t.Fatalf("source was modified through the clone: %q", got)
		}
	})
	t.Run("destination exists", func(t *testing.T) {
		var c cloner
		if err := c.clone(context.Background(), src, t.TempDir()); err == nil {
			t.Fatal("clone into an existing directory must fail")
		}
	})
	t.Run("source missing", func(t *testing.T) {
		var c cloner
		if err := c.clone(context.Background(), filepath.Join(src, "nope"), filepath.Join(t.TempDir(), "d")); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := copyTreeGo(ctx, src, filepath.Join(t.TempDir(), "d")); err == nil {
			t.Fatal("expected cancellation")
		}
	})
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f.json")
	if err := atomicWriteFile(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, p); got != "two" {
		t.Fatal(got)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
}
