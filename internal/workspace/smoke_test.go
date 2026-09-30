package workspace

import (
	"path/filepath"
	"testing"
)

func TestSmokeHappyPath(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	log := &eventLog{}
	m.OnEvent = log.fn()

	a := mustCreate(t, m, "be-1")
	b := mustCreate(t, m, "fe-1")
	if a.Base != b.Base || a.Branch != "sleipnir/s1/be-1" {
		t.Fatalf("trees: %+v %+v", a, b)
	}
	edit(t, a, "internal/util/util.go", "package util\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n")
	edit(t, b, "docs/guide.md", "# guide\n\nline one\nline two\nline three\nline four\nline five\nline six\n")

	changed, err := a.Changed(tctx(t))
	if err != nil || len(changed) != 1 || changed[0] != "internal/util/util.go" {
		t.Fatalf("Changed = %v, %v", changed, err)
	}

	q := mustQueue(t, m, QueueOptions{VerifyCmd: "test -f go.mod"})
	r1 := mustSubmit(t, q, Submission{Tree: a, Task: "T1 add Sub"})
	r2 := mustSubmit(t, q, Submission{Tree: b, Task: "T2 docs"})
	if !r1.Merged() || !r2.Merged() {
		t.Fatalf("results: %+v / %+v", r1, r2)
	}
	patch, err := q.Finish(tctx(t))
	if err != nil || patch == "" {
		t.Fatalf("Finish: %q, %v", patch, err)
	}
	t.Logf("patch:\n%s", patch)
	ff, err := q.FastForward(tctx(t))
	if err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	t.Logf("ff: %+v", ff)
	if got := readFile(t, filepath.Join(dir, "docs", "guide.md")); got == "" {
		t.Fatal("no file")
	}
	t.Logf("events: %v", log.types())
	if err := q.Close(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(tctx(t), false); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(tctx(t), false); err != nil {
		t.Fatal(err)
	}
}
