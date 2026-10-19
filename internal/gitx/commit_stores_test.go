package gitx

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// committedFiles lists the files of HEAD's tree.
func committedFiles(t *testing.T, dir string) []string {
	t.Helper()
	out := rawGit(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	return strings.Split(out, "\n")
}

func TestCommitAllWithLeavesUntrackedStoresOut(t *testing.T) {
	dir := newRepo(t)
	// A dependency store that a tracked file already lives in, as a project that
	// vendors one patched package would have.
	writeFile(t, filepath.Join(dir, "tr", "node_modules", "patched.js"), "v1\n")
	writeFile(t, filepath.Join(dir, "tr", "node_modules", "gone.js"), "bye\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-q", "-m", "tracked store file")

	writeFile(t, filepath.Join(dir, "node_modules", "left-pad", "index.js"), "module.exports = 1\n")
	writeFile(t, filepath.Join(dir, "node_modules", ".package-lock.json"), "{}\n")
	writeFile(t, filepath.Join(dir, "pkg", "node_modules", "dep", "x.js"), "x\n")
	writeFile(t, filepath.Join(dir, "pkg", "main.js"), "main\n")
	writeFile(t, filepath.Join(dir, ".venv", "lib", "site.py"), "s\n")
	writeFile(t, filepath.Join(dir, "src", "__pycache__", "m.cpython-312.pyc"), "c\n")
	writeFile(t, filepath.Join(dir, "tr", "node_modules", "patched.js"), "v2\n")
	writeFile(t, filepath.Join(dir, "tr", "node_modules", "new-untracked.js"), "n\n")
	if err := os.Remove(filepath.Join(dir, "tr", "node_modules", "gone.js")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "new.txt"), "new\n")
	// Generic names are a project's own directories: they are committed.
	writeFile(t, filepath.Join(dir, "build", "out.txt"), "b\n")
	// A file named like a store is not inside one.
	writeFile(t, filepath.Join(dir, "venv"), "not a directory\n")

	r := openRepo(t, dir)
	sha, err := r.CommitAllWith(ctxT(t), "work", Author{Name: "agent"}, CommitOptions{LeaveUntrackedStores: true})
	if err != nil || sha == "" {
		t.Fatalf("CommitAllWith = %q, %v", sha, err)
	}
	got := committedFiles(t, dir)
	for _, want := range []string{"new.txt", "pkg/main.js", "build/out.txt", "venv", "tr/node_modules/patched.js"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s not committed; tree: %v", want, got)
		}
	}
	for _, p := range got {
		if InDependencyStore(p) && p != "tr/node_modules/patched.js" {
			t.Errorf("untracked store file %s was committed", p)
		}
	}
	if slices.Contains(got, "tr/node_modules/gone.js") {
		t.Error("deletion of a tracked file inside a store was not committed")
	}
	if c := rawGit(t, dir, "show", "HEAD:tr/node_modules/patched.js"); c != "v2" {
		t.Errorf("tracked store file committed as %q, want v2", c)
	}
	// What was left out is still there, untracked, and the next commit has nothing to record.
	if st := rawGit(t, dir, "status", "--porcelain", "--untracked-files=normal"); !strings.Contains(st, "?? node_modules/") {
		t.Errorf("node_modules is not left untracked:\n%s", st)
	}
	if again, err := r.CommitAllWith(ctxT(t), "again", Author{}, CommitOptions{LeaveUntrackedStores: true}); err != nil || again != "" {
		t.Errorf("second commit = %q, %v; want nothing to commit", again, err)
	}
	// CommitAll keeps staging everything.
	sha, err = r.CommitAll(ctxT(t), "all", Author{})
	if err != nil || sha == "" {
		t.Fatalf("CommitAll = %q, %v", sha, err)
	}
	if got := committedFiles(t, dir); !slices.Contains(got, "node_modules/left-pad/index.js") {
		t.Errorf("CommitAll left node_modules out: %v", got)
	}
}

func TestCommitAllWithOnlyStoresIsNothingToCommit(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, filepath.Join(dir, "node_modules", "a", "index.js"), "a\n")
	head := rawGit(t, dir, "rev-parse", "HEAD")
	r := openRepo(t, dir)
	sha, err := r.CommitAllWith(ctxT(t), "work", Author{}, CommitOptions{LeaveUntrackedStores: true})
	if err != nil || sha != "" {
		t.Fatalf("CommitAllWith = %q, %v; want nothing to commit", sha, err)
	}
	if now := rawGit(t, dir, "rev-parse", "HEAD"); now != head {
		t.Errorf("HEAD moved to %s", now)
	}
}

func TestInDependencyStore(t *testing.T) {
	for p, want := range map[string]bool{
		"node_modules/x.js":          true,
		"node_modules/":              true,
		"web/node_modules/a/b.js":    true,
		"pkg/.venv/":                 true,
		"src/__pycache__/m.pyc":      true,
		"node_modules":               false,
		"venv":                       false,
		"src/node_modules.go":        false,
		"build/out.txt":              false,
		"vendor/github.com/x/y.go":   false,
		"my_node_modules/index.js":   false,
		"docs/node_modules-notes.md": false,
	} {
		if got := InDependencyStore(p); got != want {
			t.Errorf("InDependencyStore(%q) = %v, want %v", p, got, want)
		}
	}
	if !slices.IsSorted(DependencyStores()) || !slices.Contains(DependencyStores(), "node_modules") {
		t.Errorf("DependencyStores() = %v", DependencyStores())
	}
}
