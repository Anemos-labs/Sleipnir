package swarm_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/swarm"
)

// Peer mail wakes an idle worker, and each wake is a whole run. Past the bound for the
// task it is working on, mail is still delivered (it waits in the inbox) but no longer
// starts runs; the manager is told once, and a new task starts the count again. The
// manager's own mail is never counted.
func TestPeerMailCannotKeepAWorkerRunningForEver(t *testing.T) {
	var mu sync.Mutex
	runs := map[string]int{}
	loose := swarm.RouterConfig{MaxPerMinute: 1 << 30, MaxPerPairPerMin: 1 << 30, MaxChars: 600, DedupeWindow: time.Nanosecond}
	r := newRig(t, swarm.Config{SessionID: "wl", MaxMailWakes: 3, Router: loose}, mock.Config{}, func(c *mock.Call) mock.Reply {
		id, role := who(c)
		if role != "manager" {
			mu.Lock()
			runs[id]++
			mu.Unlock()
		}
		return mock.Reply{Text: "noted by " + id}
	})
	r.sw.StartManager()
	be, err := r.sw.Spawn(swarm.SpawnReq{Role: "backend", Title: "Backend work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	fe, err := r.sw.Spawn(swarm.SpawnReq{Role: "frontend", Title: "Frontend work", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	idle := func(id string) bool { a, _ := r.sw.Board.Snapshot().Agent(id); return a.State == "idle" }
	waitFor(t, "both workers idle", func() bool { return idle(be) && idle(fe) })
	count := func(id string) int { mu.Lock(); defer mu.Unlock(); return runs[id] }
	base := count(be)

	// Six messages from the other worker, one at a time: the first three wake it.
	for i := 1; i <= 6; i++ {
		before := count(be)
		if _, err := r.sw.Router.Send(fe, be, "info", "ping "+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
		if i <= 3 {
			waitFor(t, "the worker to answer the ping", func() bool { return count(be) > before && idle(be) })
		} else {
			time.Sleep(150 * time.Millisecond) // long enough for a wake that should not happen
		}
	}
	if got := count(be) - base; got != 3 {
		t.Fatalf("peer mail started %d runs of the worker, want exactly the bound of 3", got)
	}
	if n := len(r.log.OfType(events.TypeSwarmWakeLimit)); n != 1 {
		t.Fatalf("the bound was reported %d times, want once", n)
	}

	// The manager's mail still wakes it: the bound is on peer chatter.
	before := count(be)
	if _, err := r.sw.Router.Send("mgr", be, "info", "the manager speaks"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the manager's mail to wake the worker", func() bool { return count(be) > before })
	waitFor(t, "the worker idle again", func() bool { return idle(be) })

	// A new task starts the count again.
	if _, err := r.sw.Spawn(swarm.SpawnReq{Role: "backend", Agent: be, Title: "More backend work", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker idle after its new task", func() bool { return idle(be) })
	before = count(be)
	if _, err := r.sw.Router.Send(fe, be, "info", "ping after the new task"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "peer mail to wake the worker for its new task", func() bool { return count(be) > before })
	for _, e := range r.log.OfType(events.TypeSwarmWakeLimit) {
		if !strings.Contains(string(e.Data), `"limit":3`) {
			t.Errorf("wake_limit payload: %s", e.Data)
		}
	}
}
