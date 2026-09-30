package inspect

import (
	"strings"
	"testing"
	"time"
)

// boardScenario replays the log a manager and two workers leave: the manager
// spawns work (creating a task by title, then assigning an existing one),
// workers update and finish, mail flows, and the harness closes a failed run.
func boardScenario(t *testing.T) *Session {
	b := newEvb(t)
	at := func(s int) *evb { return b.at(time.Duration(s) * time.Second) }
	at(0).emit("", "session.start", m{"model": "claude-sonnet-5-5", "swarm": true})
	at(1).emit("swarm", "agent.spawn", m{"id": "mgr", "role": "manager", "model": "claude-sonnet-5-5"})

	// The manager creates T1 with `task create`, then spawns be-1 on it.
	at(2).emit("mgr", "tool.call", m{"id": "k1", "name": "task", "input": m{"action": "create", "title": "Add retry helper", "description": "with jitter", "role": "backend", "files": []string{"internal/client/**"}}})
	at(2).emit("mgr", "board.op", m{"op": "create", "version": 1})
	at(2).emit("mgr", "tool.result", m{"id": "k1", "name": "task", "error": false, "chars": 30, "ms": 3})
	at(3).emit("mgr", "tool.call", m{"id": "k2", "name": "spawn", "input": m{"role": "backend", "task": "T1", "brief": "go"}})
	at(3).emit("mgr", "board.op", m{"op": "assign", "version": 2})
	at(3).emit("be-1", "board.op", m{"op": "agent", "version": 3})
	at(3).emit("swarm", "agent.spawn", m{"id": "be-1", "role": "backend", "task": "T1", "by": "mgr", "parent": "mgr", "model": "claude-sonnet-5-5"})
	at(3).emit("mgr", "tool.result", m{"id": "k2", "name": "spawn", "error": false, "chars": 20, "ms": 12})

	// A spawn by title creates T2 and assigns it in one call.
	at(4).emit("mgr", "tool.call", m{"id": "k3", "name": "spawn", "input": m{"role": "tester", "task": "Write table tests", "brief": "cover 429"}})
	at(4).emit("mgr", "board.op", m{"op": "create", "version": 4})
	at(4).emit("mgr", "board.op", m{"op": "assign", "version": 5})
	at(4).emit("swarm", "agent.spawn", m{"id": "te-1", "role": "tester", "task": "T2", "by": "mgr", "parent": "mgr", "model": "claude-sonnet-5-5"})
	at(4).emit("mgr", "tool.result", m{"id": "k3", "name": "spawn", "error": false, "chars": 20, "ms": 12})

	// be-1 reports progress and finishes; the manager accepts.
	at(10).emit("be-1", "tool.call", m{"id": "w1", "name": "task", "input": m{"action": "update", "id": "T1", "text": "helper written, wiring in"}})
	at(10).emit("be-1", "board.op", m{"op": "update", "version": 6})
	at(10).emit("be-1", "tool.result", m{"id": "w1", "name": "task", "error": false, "chars": 5, "ms": 1})
	at(20).emit("be-1", "tool.call", m{"id": "w2", "name": "task", "input": m{"action": "done", "id": "T1", "text": "retry with jitter; tests pass"}})
	at(20).emit("be-1", "board.op", m{"op": "finish", "version": 7})
	at(20).emit("be-1", "tool.result", m{"id": "w2", "name": "task", "error": false, "chars": 5, "ms": 900})
	at(21).emit("swarm", "agent.end", m{"id": "be-1", "state": "idle", "evidence": "edited 2 (retry.go, retry_test.go); last test passed"})
	at(22).emit("mgr", "tool.call", m{"id": "k4", "name": "task", "input": m{"action": "accept", "id": "T1", "text": "verified"}})
	at(22).emit("manager", "board.op", m{"op": "finish", "version": 8}) // the runtime names the actor "manager", not its id
	at(22).emit("mgr", "tool.result", m{"id": "k4", "name": "task", "error": false, "chars": 5, "ms": 1})

	// te-1 blocks, resumes, and is finally closed as failed by the harness.
	at(30).emit("te-1", "tool.call", m{"id": "t1", "name": "task", "input": m{"action": "block", "id": "T2", "text": "needs the retry helper"}})
	at(30).emit("te-1", "board.op", m{"op": "block", "version": 9})
	at(30).emit("te-1", "tool.result", m{"id": "t1", "name": "task", "error": false, "chars": 5, "ms": 1})
	at(31).emit("te-1", "tool.call", m{"id": "t2", "name": "task", "input": m{"action": "resume", "id": "T2"}})
	at(31).emit("te-1", "board.op", m{"op": "resume", "version": 10})
	at(31).emit("te-1", "tool.result", m{"id": "t2", "name": "task", "error": false, "chars": 5, "ms": 1})
	at(40).emit("te-1", "board.op", m{"op": "finish", "version": 11}) // the harness closes it: no task call is open
	at(40).emit("swarm", "agent.end", m{"id": "te-1", "state": "failed", "evidence": "no files edited; no tests run"})

	// An idle worker is given a new task by the manager (no agent.spawn is logged for a reuse).
	at(50).emit("mgr", "tool.call", m{"id": "k5", "name": "task", "input": m{"action": "create", "title": "Docs for retry"}})
	at(50).emit("mgr", "board.op", m{"op": "create", "version": 12})
	at(50).emit("mgr", "tool.result", m{"id": "k5", "name": "task", "error": false, "chars": 5, "ms": 1})
	at(51).emit("mgr", "tool.call", m{"id": "k6", "name": "spawn", "input": m{"agent": "be-1", "task": "T3"}})
	at(51).emit("mgr", "board.op", m{"op": "assign", "version": 13})
	at(51).emit("mgr", "tool.result", m{"id": "k6", "name": "spawn", "error": false, "chars": 5, "ms": 1})
	return mustLoad(t, b.dir)
}

func TestBoardIsReconstructedFromToolCallsAndBoardOps(t *testing.T) {
	s := boardScenario(t)
	rep := s.Swarm()
	got := map[string]TaskView{}
	for _, tk := range rep.Tasks {
		got[tk.ID] = tk
	}
	if len(got) != 3 {
		t.Fatalf("tasks = %+v, want T1..T3", rep.Tasks)
	}
	t1 := got["T1"]
	if t1.Title != "Add retry helper" || t1.Status != "done" || t1.Owner != "be-1" || t1.Role != "backend" || t1.Result != "retry with jitter; tests pass verified" {
		t.Errorf("T1 = %+v", t1)
	}
	if len(t1.Files) != 1 || t1.Files[0] != "internal/client/**" {
		t.Errorf("T1 files = %v", t1.Files)
	}
	t2 := got["T2"]
	if t2.Title != "Write table tests" || t2.Status != "failed" || t2.Owner != "te-1" {
		t.Errorf("T2 = %+v: created by title through spawn, blocked, resumed, then failed with its agent", t2)
	}
	t3 := got["T3"]
	if t3.Title != "Docs for retry" || t3.Status != "doing" || t3.Owner != "be-1" {
		t.Errorf("T3 = %+v: assigned to an idle worker by spawn agent=be-1", t3)
	}
	if rep.TaskCount["done"] != 1 || rep.TaskCount["failed"] != 1 || rep.TaskCount["doing"] != 1 {
		t.Errorf("task count = %v", rep.TaskCount)
	}
	if !strings.Contains(rep.Tasksrc, "reconstructed") {
		t.Errorf("the source of the task view must be stated: %q", rep.Tasksrc)
	}
	if rep.BoardOps["create"] != 3 || rep.BoardOps["assign"] != 3 || rep.BoardOps["finish"] != 3 || rep.Totals.BoardOps != 13 {
		t.Errorf("board ops = %v total %d", rep.BoardOps, rep.Totals.BoardOps)
	}
	if len(rep.Spawns) != 3 || rep.Spawns[0].ID != "te-1" || rep.Spawns[2].ID != "mgr" || rep.Spawns[1].Parent != "mgr" || rep.Spawns[1].Task != "T1" {
		t.Errorf("spawns (newest first) = %+v", rep.Spawns)
	}
}

func TestAgentStatesInASwarm(t *testing.T) {
	s := boardScenario(t)
	states := map[string]AgentView{}
	for _, a := range s.Agents() {
		states[a.ID] = a
	}
	if a := states["te-1"]; a.State != "failed" || a.Evidence == "" || a.Parent != "mgr" || a.Task != "T2" || a.Role != "tester" {
		t.Errorf("te-1 = %+v", a)
	}
	if a := states["be-1"]; a.State != "idle" || !a.Ended || a.EndState != "idle" || !strings.Contains(a.Evidence, "retry.go") {
		t.Errorf("be-1 = %+v: an agent whose run ended idle can take new work", a)
	}
	if a := states["mgr"]; a.Role != "manager" {
		t.Errorf("mgr = %+v", a)
	}
	// The swarm's own events are not an agent.
	if _, ok := states["swarm"]; ok {
		t.Error("the runtime's pseudo agent \"swarm\" was listed as an agent")
	}
	if _, ok := states["manager"]; ok {
		t.Error("the actor name \"manager\" was listed as an agent")
	}
	if sum := s.Summary(); !sum.Session.Swarm || sum.Swarm.Agents != 3 || sum.Swarm.Spawns != 3 {
		t.Errorf("swarm totals = %+v", sum.Swarm)
	}
}

func TestBoardOpsWithRicherPayloadsWin(t *testing.T) {
	// If a producer starts logging task details on board.op the inspector uses them.
	b := newEvb(t)
	b.emit("mgr", "board.op", m{"op": "create", "version": 1, "task": "T7", "title": "Logged title", "status": "todo"})
	b.emit("w", "board.op", m{"op": "claim", "version": 2, "task": "T7", "owner": "w", "status": "doing", "line": "reading"})
	tasks := mustLoad(t, b.dir).Swarm().Tasks
	if len(tasks) != 1 || tasks[0].ID != "T7" || tasks[0].Title != "Logged title" || tasks[0].Status != "doing" || tasks[0].Owner != "w" || tasks[0].Line != "reading" {
		t.Errorf("tasks = %+v", tasks)
	}
}

func TestMailLeasesAndGovernorAreLoggedGenerically(t *testing.T) {
	b := newEvb(t)
	at := func(ms int) *evb { return b.at(time.Duration(ms) * time.Millisecond) }
	at(0).emit("a", "mail.send", m{"id": "m1", "from": "a", "to": "b", "kind": "request", "text": "please   review\nthe\tpatch"})
	at(7).emit("b", "mail.deliver", m{"id": "m1", "from": "a"})
	at(100).emit("b", "mail.send", m{"id": "m2", "from": "b", "to": "a", "kind": "", "text": strings.Repeat("long ", 200)})
	at(120).emit("a", "mail.deliver", m{"id": "m2", "from": "b"})
	at(130).emit("b", "mail.send", m{"id": "m3", "from": "b", "to": "a", "kind": "info", "text": "x"}) // never delivered
	at(200).emit("a", "lease", m{"action": "conflict", "path": "internal/x.go", "holder": "b", "agent": "a", "nested": m{"ignored": true}})
	at(300).emit("swarm", "governor", m{"in_flight": 3, "queued": 2, "rate_per_min": 411.5, "paused": true, "note": "429 from upstream", "nested": m{"x": 1}, "list": []int{1}})
	rep := mustLoad(t, b.dir).Swarm()
	if rep.Totals.MailSent != 3 || rep.Totals.MailDelivered != 2 || rep.MailKinds["request"] != 1 || rep.MailKinds["info"] != 2 {
		t.Errorf("mail totals = %+v kinds %v: an empty kind is info", rep.Totals, rep.MailKinds)
	}
	if len(rep.Mail) != 3 || rep.Mail[2].ID != "m1" || rep.Mail[2].LatencyMs != 7 || rep.Mail[2].Text != "please review the patch" || rep.Mail[0].LatencyMs != -1 {
		t.Errorf("mail = %+v: newest first, whitespace collapsed, undelivered mail has no latency", rep.Mail)
	}
	if got := rep.Mail[1].Text; len([]rune(got)) > 240 || !strings.HasSuffix(got, "…") {
		t.Errorf("long mail text = %d runes", len([]rune(got)))
	}
	if len(rep.Pairs) != 2 || rep.Pairs[0].From != "b" || rep.Pairs[0].To != "a" || rep.Pairs[0].N != 2 {
		t.Errorf("pairs = %+v", rep.Pairs)
	}
	if len(rep.Leases) != 1 || rep.Leases[0].Action != "conflict" || rep.Leases[0].Path != "internal/x.go" || rep.Leases[0].Holder != "b" || rep.Totals.LeaseEvents != 1 {
		t.Errorf("leases = %+v", rep.Leases)
	}
	g := rep.Governor
	if g.Events != 1 || g.Last["in_flight"] != float64(3) || g.Last["paused"] != true || g.Last["note"] != "429 from upstream" {
		t.Errorf("governor = %+v", g)
	}
	if _, nested := g.Last["nested"]; nested {
		t.Error("nested governor values must be dropped: the payload is untrusted and shown as scalars only")
	}
	// Mail, leases and board ops land in per-minute buckets.
	if len(rep.Minutes) != 1 || rep.Minutes[0].MailSent != 3 || rep.Minutes[0].MailDeliv != 2 || rep.Minutes[0].Leases != 1 {
		t.Errorf("minutes = %+v", rep.Minutes)
	}
}

func TestActivityLinesAndCoordinationOnASyntheticSwarm(t *testing.T) {
	dir, res := synthDir(t, synthCfg{Workers: 4, Steps: 20, Anomalies: true})
	s := mustLoad(t, dir)
	rep := s.Swarm()
	if rep.Totals.Spawns != res.Spawns+1 || rep.Totals.MailSent != res.MailSent || rep.Totals.MailDelivered != res.MailSent {
		t.Errorf("spawns %d mail %d/%d, want %d spawns and %d mail", rep.Totals.Spawns, rep.Totals.MailSent, rep.Totals.MailDelivered, res.Spawns+1, res.MailSent)
	}
	if rep.TaskCount["done"]+rep.TaskCount["review"] != 4 {
		t.Errorf("tasks by status = %v, want the four spawned tasks done or in review", rep.TaskCount)
	}
	total := 0
	for _, m := range rep.Minutes {
		total += m.Requests
	}
	if total != res.Requests {
		t.Errorf("requests per minute sum to %d, want %d", total, res.Requests)
	}
	if rep.Governor.PeakRPM == 0 || rep.Governor.AvgRPM <= 0 || rep.Governor.PeakInFlight < 1 {
		t.Errorf("governor = %+v", rep.Governor)
	}
	if rep.Alerts == 0 || rep.Totals.Alerts != rep.Alerts {
		t.Errorf("alerts = %d/%d", rep.Alerts, rep.Totals.Alerts)
	}
}
