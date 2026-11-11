package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A binary a build wrote into the tree is not recorded with the agent's work: it stays in the tree, untracked, and the source
// beside it is committed. A text file that happens to open with "MZ" is source, and a binary fixture the agent staged is kept.
func TestCommitLeavesBuiltExecutablesOut(t *testing.T) {
	dir := newRepo(t)
	m := newManager(t, openRepo(t, dir))
	tree := mustCreate(t, m, "be-1")
	edit(t, tree, "cmd/app/main.go", "package main\n\nfunc main() { println(1) }\n")
	elf := "\x7fELF" + strings.Repeat("\x00", 200)
	if err := os.WriteFile(filepath.Join(tree.Path, "app"), []byte(elf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tree.Path, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree.Path, "testdata", "fixture.bin"), []byte(elf), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", tree.Path, "add", "testdata/fixture.bin").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	edit(t, tree, "notes/MZ.md", "MZ is a prefix in this note, long enough to be looked at by the check for executables.\n")
	sha, err := tree.Commit(context.Background(), "work")
	if err != nil || sha == "" {
		t.Fatalf("commit: %q %v", sha, err)
	}
	files, err := tree.Repo().ChangedPaths(context.Background(), tree.Base, sha)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(files, "app") || !slices.Contains(files, "cmd/app/main.go") || !slices.Contains(files, "notes/MZ.md") || !slices.Contains(files, "testdata/fixture.bin") {
		t.Fatalf("committed %v", files)
	}
	if _, err := os.Stat(filepath.Join(tree.Path, "app")); err != nil {
		t.Fatal("the binary left the tree")
	}
}
