package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

// A project with a submodule: the agents' trees hold the gitlink as an empty
// directory (their content is not fetched: that would need the network), nothing
// reports the empty directory as a change, and the whole workflow - create,
// commit, merge, verify, hand back - works around it.
func TestSubmodulesAreCarriedAsGitlinksNotFetched(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	lib := filepath.Join(t.TempDir(), "lib")
	rawGit(t, filepath.Dir(lib), "init", "-q", "-b", "main", lib)
	writeFile(t, filepath.Join(lib, "lib.go"), "package lib\n")
	rawGit(t, lib, "add", "-A")
	rawGit(t, lib, "commit", "-qm", "lib")
	rawGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "vendor/lib")
	rawGit(t, dir, "commit", "-qm", "add a submodule")

	m := newManager(t, openRepo(t, dir))
	q := mustQueue(t, m, QueueOptions{VerifyCmd: "test -f README.md"})
	a := mustCreate(t, m, "a", CreateOptions{Base: q.Tip()})
	b := mustCreate(t, m, "b", CreateOptions{Base: q.Tip()})
	for _, tr := range []*Tree{a, b} {
		if !exists(filepath.Join(tr.Path, "vendor", "lib")) {
			t.Fatalf("%s: the submodule's directory is missing", tr.Agent)
		}
		if ch, err := tr.Changed(tctx(t)); err != nil || len(ch) != 0 {
			t.Fatalf("%s: a fresh tree reports changes: %v, %v", tr.Agent, ch, err)
		}
		if dirty, err := tr.Dirty(tctx(t)); err != nil || dirty {
			t.Fatalf("%s: a fresh tree is dirty: %v, %v", tr.Agent, dirty, err)
		}
	}
	edit(t, a, "a.txt", "a\n")
	edit(t, b, "b.txt", "b\n")
	if r := mustSubmit(t, q, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	if r := mustSubmit(t, q, Submission{Tree: b}); !r.Merged() {
		t.Fatalf("b: %+v", r)
	}
	q2 := q.tree.repo
	ls, err := q2.Git(tctx(t), "ls-files", "-s", "vendor")
	if err != nil || !strings.Contains(ls.Stdout, "160000") {
		t.Fatalf("the gitlink must survive integration: %q, %v", ls.Stdout, err)
	}
	patch, err := q.Finish(tctx(t))
	if err != nil || strings.Contains(patch, "Subproject") || !strings.Contains(patch, "a.txt") || !strings.Contains(patch, "b.txt") {
		t.Fatalf("Finish: %v\n%s", err, patch)
	}
	for _, tr := range []*Tree{a, b} {
		if err := tr.Remove(tctx(t), false); err != nil {
			t.Fatalf("Remove %s: %v", tr.Agent, err)
		}
	}
}
