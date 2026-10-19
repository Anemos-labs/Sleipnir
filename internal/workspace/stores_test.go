package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// treeFiles lists the files of rev's tree in dir.
func treeFiles(t *testing.T, dir, rev string) []string {
	t.Helper()
	return strings.Split(rawGit(t, dir, "ls-tree", "-r", "--name-only", rev), "\n")
}

// An install in a project that does not ignore its dependency store is not
// recorded by the harness's commit of the agent's work, so it never reaches the
// integration branch; the store's contents are not held to the size and
// nested-repository guards either, since they are not recorded.
func TestWorkerCommitLeavesUntrackedDependencyStoresOut(t *testing.T) {
	e := newQueueEnv(t, QueueOptions{})
	e.m.MaxFileBytes = 2000
	a := e.agent("fe-1")
	edit(t, a, "web/app.js", "console.log(1)\n")
	edit(t, a, "node_modules/left-pad/index.js", "module.exports = 1\n")
	edit(t, a, "node_modules/esbuild/bin/esbuild", strings.Repeat("x", 5000))
	edit(t, a, "web/node_modules/react/index.js", "r\n")
	edit(t, a, ".venv/lib/site.py", "s\n")
	// A package installed from git keeps its repository.
	if err := os.MkdirAll(filepath.Join(a.Path, "node_modules", "from-git"), 0o755); err != nil {
		t.Fatal(err)
	}
	rawGit(t, filepath.Join(a.Path, "node_modules", "from-git"), "init", "-q")

	r := mustSubmit(t, e.q, Submission{Tree: a, Task: "T1: web app"})
	if !r.Merged() {
		t.Fatalf("%+v", r)
	}
	files := treeFiles(t, e.dir, e.q.Tip())
	if !slices.Contains(files, "web/app.js") {
		t.Fatalf("the agent's source file was not merged: %v", files)
	}
	for _, f := range files {
		if strings.Contains(f, "node_modules/") || strings.HasPrefix(f, ".venv/") {
			t.Errorf("dependency store file %s reached the integration branch", f)
		}
	}
	if !exists(filepath.Join(a.Path, "node_modules", "left-pad", "index.js")) {
		t.Error("the installed dependencies were removed from the agent's tree")
	}
}

// A file the project tracks inside a directory named like a store is the
// project's own: its changes are committed.
func TestWorkerCommitKeepsTrackedFilesInAStore(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, filepath.Join(dir, "patches", "node_modules", "fix.js"), "v1\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-q", "-m", "tracked store file")
	m := newManager(t, openRepo(t, dir))
	a := mustCreate(t, m, "fe-1")
	edit(t, a, "patches/node_modules/fix.js", "v2\n")
	edit(t, a, "patches/node_modules/extra.js", "untracked\n")
	sha, err := a.Commit(tctx(t), "update the patch")
	if err != nil || sha == "" {
		t.Fatalf("Commit = %q, %v", sha, err)
	}
	if got := rawGit(t, dir, "show", sha+":patches/node_modules/fix.js"); got != "v2" {
		t.Errorf("tracked file committed as %q, want v2", got)
	}
	if slices.Contains(treeFiles(t, dir, sha), "patches/node_modules/extra.js") {
		t.Error("an untracked file inside a store was committed")
	}
}

// Salvaging a dead session's work leaves the store out as well.
func TestSalvageLeavesUntrackedDependencyStoresOut(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "fe-1")
	edit(t, a, "wip.txt", "wip\n")
	edit(t, a, "node_modules/x/index.js", "x\n")
	branch := a.Branch
	orphan(t, a)
	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := fresh.Prune(tctx(t), PruneOptions{Salvage: true}); err != nil {
		t.Fatal(err)
	}
	files := treeFiles(t, dir, branch)
	if !slices.Contains(files, "wip.txt") {
		t.Fatalf("the work was not salvaged: %v", files)
	}
	if slices.Contains(files, "node_modules/x/index.js") {
		t.Error("salvage committed node_modules")
	}
}
