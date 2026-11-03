package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
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

// A change the manager makes itself is held to how long it took to build: from the last flush that found its checkout with
// nothing pending to the step that integrated it. Recorded as taking no time, a review of the M4 backend a manager wrote over
// most of an hour was held to the ten-minute floor and found overrunning "work that took 3 minutes to build".
func TestAManagerChangeIsHeldToHowLongItTookToBuild(t *testing.T) {
	start := func(t *testing.T) (*isoRig, *member, func(time.Duration)) {
		var mu sync.Mutex
		now := time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC)
		clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
		advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
		r := newIsoRig(t, isoOpts{verify: true, tweak: func(d *Deps) { d.Now = clock }}, script{}.fn())
		if _, err := r.sw.StartManager(); err != nil {
			t.Fatal(err)
		}
		m := r.sw.get(r.sw.ManagerID())
		// a step that left nothing pending
		if err := r.sw.flushManager(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		return r, m, advance
	}
	write := func(t *testing.T, m *member, name, text string) {
		if err := writeFileString(filepath.Join(m.dir, name), text); err != nil {
			t.Fatal(err)
		}
	}
	want := func(t *testing.T, r *isoRig, d time.Duration) {
		t.Helper()
		if work, ids := r.sw.review.unreviewedWork(); work != d || len(ids) != 1 || ids[0] != "manager changes" {
			t.Fatalf("unreviewed work %v %v, want %v for the manager's changes", work, ids, d)
		}
	}
	t.Run("across the steps that failed verification", func(t *testing.T) {
		r, m, advance := start(t)
		advance(5 * time.Minute)
		write(t, m, "a.txt", "BROKEN first draft\n")
		if err := r.sw.flushManager(context.Background(), m); err == nil {
			t.Fatal("a manager change that fails verification was integrated")
		}
		advance(40 * time.Minute)
		write(t, m, "a.txt", "working\n")
		if err := r.sw.flushManager(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		want(t, r, 45*time.Minute)
	})
	t.Run("in the one step that made it", func(t *testing.T) {
		r, m, advance := start(t)
		advance(25 * time.Minute)
		write(t, m, "a.txt", "working\n")
		if err := r.sw.flushManager(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		want(t, r, 25*time.Minute)
	})
}

// A change the manager committed in its own checkout reaches the shared tree before it starts a member, which works on that
// tree. Integrated only when the manager's turn ended, a reviewer the manager started over its own fix read a tree without the
// fix and found nothing to review.
func TestAManagersOwnChangeIsIntegratedBeforeItStartsAMember(t *testing.T) {
	r := newIsoRig(t, isoOpts{verify: true}, script{}.fn())
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	h := &holdHooks{s: r.sw, m: m}
	call := func(tool string) agent.ToolHookOutcome {
		return h.BeforeTool(context.Background(), agent.ToolHookCall{Agent: m.id, Role: "manager", Tool: tool, Input: json.RawMessage(`{}`)})
	}
	if err := writeFileString(filepath.Join(m.dir, "fix.txt"), "manager fix\n"); err != nil {
		t.Fatal(err)
	}
	// wait is read-only and may run beside reads of the checkout: it does not flush.
	for _, tool := range []string{"read", "wait"} {
		if o := call(tool); o.Veto || len(r.log.OfType(events.TypeMergeQueued)) != 0 {
			t.Fatalf("%s submitted the manager's checkout (veto %v)", tool, o.Veto)
		}
	}
	if o := call("spawn"); o.Veto || o.Context != "" {
		t.Fatalf("spawn: veto %v, context %q", o.Veto, o.Context)
	}
	if len(r.log.OfType(events.TypeMergeMerged)) == 0 {
		t.Fatal("the manager's change was not integrated before spawn")
	}
	if got := r.sw.review.merges; got != 1 {
		t.Fatalf("the review gate counts %d merges, want the manager's change", got)
	}
}
