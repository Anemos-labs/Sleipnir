package env

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// savepointTask is a small repository with the kinds of files a capture must
// carry: text, binary, an executable script, a nested directory and an ignored
// build directory.
func savepointTask(t *testing.T) rl.Task {
	t.Helper()
	r := newFixtureRepo(t)
	r.write("a.txt", "one\n")
	r.write("dir/b.txt", "bee\n")
	r.write("script.sh", "#!/bin/sh\necho hi\n")
	r.write(".gitignore", "out/\n")
	if err := os.WriteFile(filepath.Join(r.Dir, "bin.dat"), []byte{0, 1, 2, 0xff, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	base := r.commit("start")
	return rl.Task{ID: "savepoint", Kind: rl.TaskFix, Repo: rl.RepoSpec{Path: r.Dir, Commit: base}, Prompt: "work",
		Verifier: rl.Verifier{Cmd: "true", TimeoutS: 60}, Budget: rl.Budget{WallS: 300}}
}

// A captured work tree comes back at the same path, byte for byte, with the
// executable bit and symlinks, without what changed after the capture and
// without ignored files.
func TestCaptureAndRestoreWorkspace(t *testing.T) {
	m := newManager(t)
	task := savepointTask(t)
	id := StableID(task.ID, 3, "run1")
	w, err := m.PrepareID(ctxT(t), task, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Cleanup() })
	if filepath.Base(w.Info().Path) != id {
		t.Fatalf("workspace path %s does not end in its id %s", w.Info().Path, id)
	}
	mustWrite(t, filepath.Join(w.Root, "a.txt"), "two\n")
	mustWrite(t, filepath.Join(w.Root, "c.txt"), "new file\n")
	mustWrite(t, filepath.Join(w.Root, "out", "build.o"), "ignored output\n")
	if err := os.Remove(filepath.Join(w.Root, "dir", "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, "new.bin"), []byte{0xff, 0, 0xfe, 0, 7}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(w.Root, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(w.Root, "link")); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "savepoints", "git")
	tree, skipped, err := w.Capture(ctxT(t), store)
	if err != nil || tree == "" || len(skipped) != 0 {
		t.Fatalf("capture: %q %v %v", tree, skipped, err)
	}
	// Work after the capture is not part of it.
	mustWrite(t, filepath.Join(w.Root, "a.txt"), "three\n")
	mustWrite(t, filepath.Join(w.Root, "later.txt"), "after the savepoint\n")

	info := w.Info()
	r, err := m.RestoreWorkspace(ctxT(t), task, info, store, tree)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Cleanup() })
	if r.Info().Path != info.Path || r.Root != w.Root {
		t.Fatalf("restored at %s, want %s", r.Info().Path, info.Path)
	}
	for name, want := range map[string]string{"a.txt": "two\n", "c.txt": "new file\n", "script.sh": "#!/bin/sh\necho hi\n"} {
		if got := mustRead(t, filepath.Join(r.Root, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(r.Root, "new.bin")); err != nil || !bytes.Equal(b, []byte{0xff, 0, 0xfe, 0, 7}) {
		t.Errorf("binary file: %v %v", b, err)
	}
	for _, gone := range []string{"dir/b.txt", "later.txt", "out/build.o"} {
		if exists(filepath.Join(r.Root, gone)) {
			t.Errorf("%s exists after the restore", gone)
		}
	}
	if fi, err := os.Stat(filepath.Join(r.Root, "script.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Errorf("the executable bit was not restored: %v %v", fi, err)
	}
	if target, err := os.Readlink(filepath.Join(r.Root, "link")); err != nil || target != "a.txt" {
		t.Errorf("symlink: %q %v", target, err)
	}

	// A capture of a later state restores that state.
	tree2, _, err := r.Capture(ctxT(t), store)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(r.Root, "a.txt"), "four\n")
	r2, err := m.RestoreWorkspace(ctxT(t), task, info, store, tree2)
	if err != nil || mustRead(t, filepath.Join(r2.Root, "a.txt")) != "two\n" {
		t.Fatalf("second restore: %v", err)
	}

	// A restore onto a different start is refused, and one that fails leaves the workspace at the path as it was.
	mustWrite(t, filepath.Join(r2.Root, "kept.txt"), "work in progress\n")
	other := info
	other.BaseTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if _, err := m.RestoreWorkspace(ctxT(t), task, other, store, tree2); err == nil {
		t.Error("a restore onto another base tree was accepted")
	}
	if _, err := m.RestoreWorkspace(ctxT(t), task, info, store, "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Error("a restore of a tree the store does not hold was accepted")
	}
	if got := mustRead(t, filepath.Join(r2.Root, "kept.txt")); got != "work in progress\n" {
		t.Errorf("a failed restore changed the workspace at the path: %q", got)
	}
	if exists(info.Path+".restore") || exists(info.Path+".old") {
		t.Error("a failed restore left its staging directories behind")
	}
}

// A stable id is a plain name; anything that could leave ws/ is refused.
func TestPrepareIDRejectsUnsafeIDs(t *testing.T) {
	m := newManager(t)
	task := savepointTask(t)
	for _, id := range []string{"", "..", "a/b", "../x", "with space", string(make([]byte, maxWorkspaceID+1))} {
		if _, err := m.PrepareID(ctxT(t), task, id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
	if got := StableID("fix/odd id", 2, "abc123"); !strings.HasPrefix(got, "fix_odd_id-") || !strings.HasSuffix(got, "-s2-abc123") || labelRe.MatchString(got) {
		t.Errorf("StableID = %q", got)
	}
	if got := StableID("plain", 2, "abc123"); got != "plain-s2-abc123" {
		t.Errorf("StableID of a plain name = %q", got)
	}
	// Names that the replacement or the cut would make equal stay apart.
	if a, b := StableID("fix/odd", 0, "r"), StableID("fix_odd", 0, "r"); a == b {
		t.Errorf("fix/odd and fix_odd share the workspace %q", a)
	}
	if a, b := StableID(strings.Repeat("t", 200)+"1", 0, "r"), StableID(strings.Repeat("t", 200)+"2", 0, "r"); a == b || len(a) > maxWorkspaceID {
		t.Errorf("long task names: %q %q", a, b)
	}
	// A run tag too long for the id is replaced by a digest: no panic, a valid id, distinct tags kept apart.
	long1, long2 := StableID("task", 1, strings.Repeat("a", 300)), StableID("task", 1, strings.Repeat("a", 299)+"b")
	if long1 == long2 || len(long1) > maxWorkspaceID || labelRe.MatchString(long1) {
		t.Errorf("long run tags: %q %q", long1, long2)
	}
}

// PruneStale keeps a workspace whose rollout can still be resumed and removes
// it once the rollout has its episode.
func TestPruneKeepsResumableWorkspaces(t *testing.T) {
	m := newManager(t)
	task := savepointTask(t)
	w, err := m.PrepareID(ctxT(t), task, StableID(task.ID, 0, "run"))
	if err != nil {
		t.Fatal(err)
	}
	sample := t.TempDir()
	if err := w.MarkResumable(sample); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(w.Info().Path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PruneStale(time.Hour); err != nil {
		t.Fatal(err)
	}
	if !exists(w.Root) {
		t.Fatal("a resumable workspace was pruned")
	}
	mustWrite(t, filepath.Join(sample, "episode.json"), "{}")
	if _, err := m.PruneStale(time.Hour); err != nil {
		t.Fatal(err)
	}
	if exists(w.Root) {
		t.Fatal("the workspace of a finished rollout was kept")
	}
}
