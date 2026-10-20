package swarm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// escalated reports the manager's stall diagnoses, in order.
func diagnoses(r *rvRig) []string {
	var out []string
	for _, e := range r.log.OfType(events.TypeMailSend) {
		var m struct{ From, To, Text string }
		_ = json.Unmarshal(e.Data, &m)
		if m.From == harnessSender && m.To == "mgr" && strings.HasPrefix(m.Text, "Stall diagnosis") {
			out = append(out, m.Text)
		}
	}
	return out
}

// A stall that still holds stallEscalateAfter after it was raised is escalated once to the manager, with the task's state and
// what would end it; one that clears first is not, and a later raise of the same finding starts its clock again.
func TestAStallThatOutlivesItsNudgeIsEscalatedOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := stallClock
	stallClock = func() time.Time { return now }
	t.Cleanup(func() { stallClock = old })

	r := newRVRig(t, stallCfg(), gatedWorkers(nil))
	r.sw.StartManager()
	_, a := spawnWorker(t, r, "api", 1)
	_, c := spawnWorker(t, r, "client", 1)
	b := r.sw.Board
	if err := b.BlockOn(a.Owner, a.ID, c.ID, "needs the client"); err != nil {
		t.Fatal(err)
	}
	if err := b.BlockOn(c.Owner, c.ID, a.ID, "needs the api"); err != nil {
		t.Fatal(err)
	}
	r.sw.sweepStalls()
	now = now.Add(stallEscalateAfter - time.Second)
	r.sw.sweepStalls()
	if d := diagnoses(r); len(d) != 0 {
		t.Fatalf("escalated early: %q", d)
	}
	now = now.Add(2 * time.Second)
	r.sw.sweepStalls()
	now = now.Add(stallEscalateAfter)
	r.sw.sweepStalls()
	d := diagnoses(r)
	if len(d) != 1 {
		t.Fatalf("diagnoses = %q", d)
	}
	for _, want := range []string{"(blocked_cycle)", "T1 -> T2 -> T1", "held for 10m", "mgr was told", "Task T1: blocked", "Blocked on T2", "Decide:"} {
		if !strings.Contains(d[0], want) {
			t.Fatalf("diagnosis misses %q:\n%s", want, d[0])
		}
	}
	n := 0
	for _, e := range stallEvents(r) {
		if e.Action == "escalate" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d escalate events", n)
	}

	if err := b.Unblock("mgr", c.ID); err != nil {
		t.Fatal(err)
	}
	r.sw.sweepStalls()
	if err := b.BlockOn(c.Owner, c.ID, a.ID, "needs the api again"); err != nil {
		t.Fatal(err)
	}
	r.sw.sweepStalls()
	now = now.Add(time.Minute)
	r.sw.sweepStalls()
	if d := diagnoses(r); len(d) != 1 {
		t.Fatalf("a fresh raise was escalated at once: %q", d)
	}
}
