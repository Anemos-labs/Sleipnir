package translate

import (
	"context"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// endingTeamLog is a team whose last moments come within a few milliseconds of each other, as a recorded or fast run's do: the
// reviewer rv-1 reports and ends, the task is accepted, the manager answers and ends. The state of rv-1 that follows its report is
// held back by the rate limit of state events, because rv-1 was sent a state a moment before.
func endingTeamLog() []events.Event {
	b := newLog(t0)
	const gap = 2 * time.Millisecond
	b.add(0, "", "session.start", map[string]any{"swarm": true, "model": "m1", "root": "/work"})
	b.add(gap, "swarm", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(gap, "mgr", "board.op", boardOp("create", "T1", "todo", "", 0, map[string]any{"title": "review the summaries", "files": []string{}}))
	b.add(gap, "swarm", "agent.spawn", map[string]any{"id": "rv-1", "role": "reviewer", "model": "m1", "by": "mgr", "parent": "mgr"})
	b.add(gap, "rv-1", "board.op", boardOp("claim", "T1", "doing", "rv-1", 1, map[string]any{"files": []string{}}))
	b.add(gap, "rv-1", "model.request", request("r1"))
	b.add(gap, "rv-1", "model.response", response("r1", 0))
	b.add(gap, "rv-1", "board.op", boardOp("finish", "T1", "review", "rv-1", 2, map[string]any{"files": []string{}}))
	b.add(gap, "rv-1", "agent.state", map[string]any{"id": "rv-1", "state": "idle", "line": "", "task": "T1"})
	b.add(gap, "rv-1", "agent.end", map[string]any{"id": "rv-1", "state": "idle", "evidence": "no files edited; no tests run"})
	b.add(gap, "mgr", "board.op", boardOp("finish", "T1", "done", "rv-1", 3, map[string]any{"files": []string{}, "closure": map[string]any{"kind": "verified"}}))
	b.add(gap, "mgr", "model.request", request("r2"))
	b.add(gap, "mgr", "model.response", response("r2", 500))
	b.add(gap, "mgr", "agent.state", map[string]any{"id": "mgr", "state": "done", "line": "", "task": ""})
	b.add(gap, "", "session.end", map[string]any{"reason": "other"})
	return b.evs
}

// lastStates is the last state event of each agent in a stream, by agent.
func lastStates(evs []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, e := range ofKind(evs, "state") {
		out[e["id"].(string)] = e
	}
	return out
}

// A replayed team that ends in a burst ends with its manager done: the manager's state is made from the newest state of every
// worker, the one the rate limit still holds back included, and from nothing older. A manager is "waiting for the team" only while a
// task is open or a worker is working.
func TestReplayEndsTheManagerDoneWhenTheTeamIs(t *testing.T) {
	log := endingTeamLog()
	raws, err := Replay(context.Background(), recordedDir(t, encodeLog(log)), Config{Tab: "rec"})
	if err != nil {
		t.Fatal(err)
	}
	last := lastStates(decodeAll(t, toRaws(raws)))
	if last["rv-1"]["s"] != "done" {
		t.Fatalf("rv-1 ends %v, want done", last["rv-1"])
	}
	if last["mgr"]["s"] != "done" || last["mgr"]["doing"] != "done" {
		t.Errorf("the manager ends %v, want done: every task is merged and every worker done", last["mgr"])
	}

	// The same log followed as it is written, and a hosted session's log, end the same way.
	for _, logOnly := range []bool{false, true} {
		h := translateLog(t, log, "/work", logOnly)
		if got := lastStates(h.decoded())["mgr"]; got["s"] != "done" {
			t.Errorf("logOnly=%v: the manager ends %v, want done", logOnly, got)
		}
	}
}
