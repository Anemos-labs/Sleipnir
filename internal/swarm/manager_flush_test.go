package swarm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A manager checkout whose work is all integrated is not submitted again: its diff against the
// commit it started from still lists the files it changed, but the merge queue would refuse the
// submission as the integration tip itself, once per manager turn.
func TestManagerFlushSkipsACheckoutWithNothingBeyondTheIntegrationTip(t *testing.T) {
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
	if len(r.log.OfType(events.TypeMergeQueued)) == 0 {
		t.Fatal("the manager's own change was not submitted")
	}
	r.mustSpawn("backend", "update b", "b.txt")
	r.waitStatus("T1", StatusReview)
	if result := r.accept("T1"); result.IsError {
		t.Fatal(result.Text)
	}
	queued := len(r.log.OfType(events.TypeMergeQueued))
	for range 3 {
		if err := r.sw.flushManager(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(r.log.OfType(events.TypeMergeQueued)); got != queued {
		t.Fatalf("an integrated manager checkout was submitted again: %d submissions, want %d", got, queued)
	}
	if rej := r.log.OfType(events.TypeMergeRejected); len(rej) > 0 {
		t.Fatalf("manager submissions were rejected: %+v", rej)
	}
	if got := readText(t, filepath.Join(m.dir, "b.txt")); got != "peer change\n" {
		t.Fatalf("the checkout misses accepted work: %q", got)
	}
	// New work after that is still submitted.
	if err := writeFileString(filepath.Join(m.dir, "c.txt"), "later\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.flushManager(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := len(r.log.OfType(events.TypeMergeQueued)); got != queued+1 {
		t.Fatalf("new manager work was not submitted: %d submissions, want %d", got, queued+1)
	}
}
