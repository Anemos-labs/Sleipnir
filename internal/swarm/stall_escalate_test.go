package swarm

import (
	"context"
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
	clock := newFakeClock()
	r := newRVRigWith(t, stallCfg(), gatedWorkers(nil), func(d *Deps) { d.Now = clock.Now })
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
	clock.Advance(stallEscalateAfter - time.Second)
	r.sw.sweepStalls()
	if d := diagnoses(r); len(d) != 0 {
		t.Fatalf("escalated early: %q", d)
	}
	clock.Advance(2 * time.Second)
	r.sw.sweepStalls()
	clock.Advance(stallEscalateAfter)
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
	clock.Advance(time.Minute)
	r.sw.sweepStalls()
	if d := diagnoses(r); len(d) != 1 {
		t.Fatalf("a fresh raise was escalated at once: %q", d)
	}
}

// Without a manager a finding is not escalated (nobody would receive it): it stays due, and the first sweep after the manager
// starts escalates it.
func TestAStallIsEscalatedOnlyOnceAManagerCanReceiveIt(t *testing.T) {
	clock := newFakeClock()
	r := newRVRigWith(t, stallCfg(), gatedWorkers(nil), func(d *Deps) { d.Now = clock.Now })
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
	clock.Advance(stallEscalateAfter + time.Minute)
	r.sw.sweepStalls()
	for _, e := range stallEvents(r) {
		if e.Action == "escalate" {
			t.Fatal("escalated with no manager")
		}
	}
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	r.sw.sweepStalls()
	if d := diagnoses(r); len(d) != 1 {
		t.Fatalf("diagnoses after the manager started = %q", d)
	}
}

// The remedy of a worker without progress matches its nudge: a persistent pool refuses a handover, so it is not offered there.
func TestTheNoProgressRemedyOfAPoolOffersNoHandover(t *testing.T) {
	if r := stallRemedy(StallClaimedNoProgress, false); !strings.Contains(r, "task handover") {
		t.Errorf("without pools: %q", r)
	}
	if r := stallRemedy(StallClaimedNoProgress, true); strings.Contains(r, "task handover") || !strings.Contains(r, "refuses a handover") {
		t.Errorf("with pools: %q", r)
	}
}

// A finding that concerns the manager itself carries the manager's state too.
func TestAManagersOwnStallDiagnosisNamesItsState(t *testing.T) {
	r := newRVRig(t, stallCfg(), gatedWorkers(nil))
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	p := r.sw.stallPacket(Stall{Kind: StallManagerWaitingOnIdle, Agent: "mgr", Notify: "mgr", Detail: "the manager waits"}, 10*time.Minute)
	if !strings.Contains(p, "\nAgent mgr (manager): ") {
		t.Fatalf("no manager state in:\n%s", p)
	}
}

// A diagnosis is of no use as an excerpt: while the manager's inbox would coalesce it, it is not sent and stays due.
func TestADiagnosisWaitsForRoomInTheManagersInbox(t *testing.T) {
	clock := newFakeClock()
	cfg := stallCfg()
	cfg.InboxSoftCap = 1
	gated := gatedWorkers(nil)
	r := newRVRigWith(t, cfg, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role == "manager" {
			if c.Assistants == 0 {
				return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "list"}}}} // a tool round drains the inbox
			}
			return rvReply{Text: "ok"}
		}
		return gated(ctx, c)
	}, func(d *Deps) { d.Now = clock.Now })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
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
	m := r.sw.get("mgr")
	rvWait(t, "the manager's inbox to fill", func() bool {
		r.sw.notify("mgr", "info", "filler")
		return !m.hasRoom(r.sw)
	})
	clock.Advance(stallEscalateAfter + time.Minute)
	r.sw.sweepStalls()
	if d := diagnoses(r); len(d) != 0 {
		t.Fatalf("a diagnosis was sent into a full inbox: %q", d)
	}
	for _, e := range stallEvents(r) {
		if e.Action == "escalate" {
			t.Fatal("counted as escalated while the inbox was full")
		}
	}
	r.sw.stalls.mu.Lock()
	n := len(r.sw.stalls.escalated)
	r.sw.stalls.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d findings marked escalated", n)
	}
	// Once the manager has read its mail (and the digest of the filler), a later sweep delivers the diagnosis whole.
	for i := 0; i < 5 && !m.hasRoom(r.sw); i++ {
		if _, err := r.sw.RunManager(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		m.pump(r.sw)
	}
	r.sw.sweepStalls()
	d := diagnoses(r)
	if len(d) != 1 || !strings.Contains(d[0], a.ID) && !strings.Contains(d[0], c.ID) {
		t.Fatalf("the diagnosis was not delivered whole once the inbox had room: %q", d)
	}
	var esc int
	for _, e := range stallEvents(r) {
		if e.Action == "escalate" {
			esc++
		}
	}
	if esc != 1 {
		t.Fatalf("%d escalate events after delivery, want 1", esc)
	}
}
