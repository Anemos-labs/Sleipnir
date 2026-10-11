package taskgen

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// newGit is a hardened git runner for a test.
func newGit(t *testing.T) *env.Git {
	t.Helper()
	g, err := env.NewGit(env.GitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// objectSet lists the ids of every object in the repository whose git directory is dir.
func objectSet(f *fixture, dir string) map[string]bool {
	f.t.Helper()
	out := map[string]bool{}
	for _, l := range strings.Split(f.git("--git-dir="+dir, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)"), "\n") {
		if l != "" {
			out[l] = true
		}
	}
	return out
}

// treeObjects lists the objects rev's tree reaches in the fixture's repository.
func treeObjects(f *fixture, rev string) map[string]bool {
	f.t.Helper()
	out := map[string]bool{}
	for _, l := range strings.Split(f.git("rev-list", "--objects", rev+"^{tree}"), "\n") {
		if id, _, _ := strings.Cut(l, " "); id != "" {
			out[id] = true
		}
	}
	return out
}

func TestSealWritesOneCommitOfTheStartTree(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "alpha\n")
	f.write("dir/b.go", "package dir\n")
	f.write("run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(f.Dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(f.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	start := f.commit("start")
	f.write("future.txt", "FUTURE-SECRET\n")
	f.write("a.txt", "alpha v2\n")
	future := f.commit("the future")
	futureObjects := []string{future, f.git("rev-parse", future+"^{tree}"), f.git("rev-parse", future+":future.txt"), f.git("rev-parse", future+":a.txt")}

	ctx := context.Background()
	g := newGit(t)
	gitDir := filepath.Join(f.Dir, ".git")
	dest := filepath.Join(t.TempDir(), "repos", "task.git")
	commit, err := Seal(ctx, g, gitDir, start, dest, SealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	in := func(args ...string) string { return f.git(append([]string{"--git-dir=" + dest}, args...)...) }

	// One commit, the start's tree, on main, with a fixed identity and date.
	if all := in("rev-list", "--all"); all != commit {
		t.Errorf("commits: %q, want only %s", all, commit)
	}
	if in("rev-parse", "HEAD") != commit || in("symbolic-ref", "HEAD") != "refs/heads/main" {
		t.Errorf("HEAD is not main at the sealed commit")
	}
	if got, want := in("rev-parse", commit+"^{tree}"), f.git("rev-parse", start+"^{tree}"); got != want {
		t.Errorf("tree %s, want the start's %s", got, want)
	}
	// Unix times: how git renders a UTC date in strict ISO form (+00:00 or Z) differs between versions.
	if got := in("log", "-1", "--format=%P|%an <%ae> %at %ad|%cn <%ce> %ct|%s", "--date=format:%z", commit); got != "|sleipnir <start@sleipnir.invalid> 946684800 +0000|sleipnir <start@sleipnir.invalid> 946684800|start" {
		t.Errorf("commit: %s", got)
	}

	// Exactly the commit and what the start's tree reaches: nothing of the future.
	objs := objectSet(f, dest)
	want := treeObjects(f, start)
	want[commit] = true
	if !reflect.DeepEqual(objs, want) {
		t.Errorf("objects: %d, want %d\n%v\n%v", len(objs), len(want), keysOf(objs), keysOf(want))
	}
	for _, o := range futureObjects {
		if objs[o] {
			t.Errorf("the sealed repository holds %s of a later commit", o)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "objects", "info", "alternates")); err == nil {
		t.Error("the sealed repository borrows objects")
	}
	in("fsck", "--strict", "--no-dangling")

	// The same tree seals to the same commit, here or elsewhere, now or later; a later commit's tree to another.
	againDest := filepath.Join(t.TempDir(), "again.git")
	again, err := Seal(ctx, g, gitDir, start, againDest, SealOptions{})
	if err != nil || again != commit {
		t.Errorf("sealing again: %s %v, want %s", again, err, commit)
	}
	if replaced, err := Seal(ctx, g, gitDir, future, dest, SealOptions{}); err != nil || replaced == commit || in("rev-list", "--all") != replaced {
		t.Errorf("sealing over an existing repository: %s %v", replaced, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
		t.Errorf("left beside the sealed repository: %v", entries)
	}

	// A workspace made from the sealed repository is the start, executable bit and symbolic link included.
	m := newManager(t)
	w, err := m.Prepare(ctx, rl.Task{ID: "sealed", Kind: rl.TaskFix, Prompt: "x", Repo: rl.RepoSpec{Path: againDest, Commit: commit}, Verifier: rl.Verifier{Cmd: "true"}}, "sealed")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	if b, err := os.ReadFile(filepath.Join(w.Root, "a.txt")); err != nil || string(b) != "alpha\n" {
		t.Errorf("a.txt: %q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(w.Root, "run.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Errorf("run.sh: %v %v", fi, err)
	}
	if l, err := os.Readlink(filepath.Join(w.Root, "link")); err != nil || l != "a.txt" {
		t.Errorf("link: %q %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "future.txt")); err == nil {
		t.Error("the workspace has a later commit's file")
	}
}

func TestSealRemovesPaths(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "alpha\n")
	f.write("dir/b_test.go", "package dir\n\n// SECRET\n")
	f.write("dir/c.go", "package dir\n")
	f.write("old/only.txt", "gone\n")
	start := f.commit("start")
	removed := []string{f.git("rev-parse", start+":dir/b_test.go"), f.git("rev-parse", start+":old/only.txt"), f.git("rev-parse", start+":old")}

	dest := filepath.Join(t.TempDir(), "s.git")
	commit, err := Seal(context.Background(), newGit(t), filepath.Join(f.Dir, ".git"), start, dest, SealOptions{Remove: []string{"dir/b_test.go", "old/only.txt", "not/there"}})
	if err != nil {
		t.Fatal(err)
	}
	files := f.git("--git-dir="+dest, "ls-tree", "-r", "--name-only", commit)
	if files != "a.txt\ndir/c.go" {
		t.Errorf("sealed tree: %q", files)
	}
	objs := objectSet(f, dest)
	for _, o := range removed {
		if objs[o] {
			t.Errorf("a removed object survives: %s", o)
		}
	}
	if len(objs) != 5 { // the commit, two trees, two blobs
		t.Errorf("%d objects: %v", len(objs), keysOf(objs))
	}
	if out, err := exec.Command("find", filepath.Join(dest, "objects"), "-type", "f", "-path", "*/objects/??/*").Output(); err == nil && strings.Count(string(out), "\n") != 1 {
		t.Errorf("loose objects besides the commit:\n%s", out)
	}
	for _, bad := range []string{"", "a\nb"} {
		if _, err := Seal(context.Background(), newGit(t), filepath.Join(f.Dir, ".git"), start, filepath.Join(t.TempDir(), "x.git"), SealOptions{Remove: []string{bad}}); err == nil {
			t.Errorf("removing %q was accepted", bad)
		}
	}
}

func TestFromGitSeal(t *testing.T) {
	f := newFixture(t)
	f.write("go.mod", calcGoMod)
	f.write("calc.go", calcV1)
	f.write("calc_test.go", calcTestV1)
	f.write("LICENSE", mitLicense)
	f.commit("initial")
	fixed := strings.Replace(calcV1, "return lo // BUG: should return hi", "return hi", 1)
	f.write("calc.go", fixed)
	f.write("calc_test.go", calcTestV1+"\nfunc TestClampUpper(t *testing.T) {\n\tif Clamp(10, 0, 5) != 5 {\n\t\tt.Fatal()\n\t}\n}\n")
	fix := f.commit("Fix Clamp above the range")
	parent := f.git("rev-parse", fix+"^")

	ctx := context.Background()
	m := newManager(t)
	hidden, gold := events.NewMemBlobs(), events.NewMemBlobs()
	seal := filepath.Join(t.TempDir(), "repos")
	tasks, rep, err := FromGit(ctx, f.Dir, GitOptions{Workspaces: m, HiddenBlobs: hidden, GoldBlobs: gold, Timeout: time.Minute, IDPrefix: "calc", Seal: seal})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("%v %d %+v", err, len(tasks), rep)
	}
	task := tasks[0]
	if task.Repo.Path != filepath.Join(seal, task.ID+".git") || task.Repo.License != "MIT" {
		t.Fatalf("repo: %+v", task.Repo)
	}
	in := func(args ...string) string { return f.git(append([]string{"--git-dir=" + task.Repo.Path}, args...)...) }
	if in("rev-list", "--all") != task.Repo.Commit || in("rev-parse", task.Repo.Commit+"^{tree}") != f.git("rev-parse", parent+"^{tree}") {
		t.Errorf("the sealed repository is not the parent's tree alone")
	}
	if objectSet(f, task.Repo.Path)[f.git("rev-parse", fix+":calc.go")] {
		t.Error("the fixed calc.go is in the sealed repository")
	}
	var meta Meta
	if err := json.Unmarshal(task.Meta, &meta); err != nil || meta.Parent != parent || meta.Commit != fix {
		t.Errorf("meta keeps the source commits: %+v %v", meta, err)
	}
	// The task, as written, is sound: its start fails and the reference solution passes.
	patch, err := gold.Get(core.Hash(meta.GoldBlob))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.CheckTask(ctx, task, patch, env.VerifyOptions{Workspaces: m, HiddenBlobs: hidden}); err != nil {
		t.Fatalf("the sealed task is unsound: %v", err)
	}

	// SealPath names the directory as the tasks see it.
	tasks, _, err = FromGit(ctx, f.Dir, GitOptions{NoValidate: true, IDPrefix: "calc", Seal: seal, SealPath: "repos"})
	if err != nil || len(tasks) != 1 || tasks[0].Repo.Path != "repos/"+tasks[0].ID+".git" || tasks[0].Repo.Commit != task.Repo.Commit {
		t.Fatalf("%v %+v", err, tasks)
	}
}

// keysOf returns a set's members in order.
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// installDir replaces a directory, and a failed installation keeps the one that was there.
func TestInstallDirKeepsTheOldOneWhenItFails(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "start.git")
	mk := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "HEAD"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		b, _ := os.ReadFile(filepath.Join(dest, "HEAD"))
		return string(b)
	}
	mk(dest, "old")
	mk(filepath.Join(dir, "new"), "new")
	if err := installDir(filepath.Join(dir, "new"), dest); err != nil || read() != "new" {
		t.Fatalf("install: %v, dest holds %q", err, read())
	}
	if err := installDir(filepath.Join(dir, "missing"), dest); err == nil || read() != "new" {
		t.Fatalf("a failed install: %v, dest holds %q, want the earlier one", err, read())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
	fresh := filepath.Join(dir, "fresh.git")
	mk(filepath.Join(dir, "other"), "x")
	if err := installDir(filepath.Join(dir, "other"), fresh); err != nil {
		t.Fatalf("install where nothing was: %v", err)
	}
}
