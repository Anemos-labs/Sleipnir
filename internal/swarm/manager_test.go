package swarm

import (
	"context"
	"path/filepath"
	"testing"
)

func TestManagerPublicationIncludesWorkAcceptedAfterItsOwnChanges(t *testing.T) {
	sc := script{"be-1": {act(writeCall("b.txt", "peer change\n")), act(doneCall("T1", "updated b"))}}
	r := newIsoRig(t, isoOpts{verify: true}, sc.fn())
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	ctx := context.Background()
	if err := writeFileString(filepath.Join(m.dir, "a.txt"), "manager change\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.flushManager(ctx, m); err != nil {
		t.Fatal(err)
	}
	r.mustSpawn("backend", "update b", "b.txt")
	r.waitStatus("T1", StatusReview)
	if result := r.accept("T1"); result.IsError {
		t.Fatal(result.Text)
	}
	if err := r.sw.flushManager(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, filepath.Join(m.dir, "b.txt")); got != "peer change\n" {
		t.Fatalf("publication misses accepted work: %q", got)
	}
	if got := readText(t, filepath.Join(m.dir, "a.txt")); got != "manager change\n" {
		t.Fatalf("lost manager work: %q", got)
	}
	if n := len(r.sw.Board.Snapshot().Tasks); n != 1 {
		t.Fatalf("direct manager work invented a task: %d", n)
	}
}
