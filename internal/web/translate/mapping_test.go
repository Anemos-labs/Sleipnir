package translate

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// lastState is the last state event of an agent.
func lastState(evs []map[string]any, id string) map[string]any {
	var out map[string]any
	for _, e := range ofKind(evs, "state") {
		if e["id"] == id {
			out = e
		}
	}
	return out
}

// Each status of the State maps to the page's state and doing line; the single agent is mgr.
func TestStateMapping(t *testing.T) {
	type step struct {
		agent, typ string
		data       map[string]any
	}
	for _, tc := range []struct {
		name     string
		steps    []step
		id       string
		s, doing string
	}{
		{"starting", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1"}}}, "be-1", "think", "thinking"},
		{"thinking with a line", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1", "role": "backend"}},
			{"be-1", "agent.state", map[string]any{"id": "be-1", "state": "running", "line": "reading files"}}, {"be-1", "model.request", request("r1")}}, "be-1", "think", "reading files"},
		{"tool", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "tool.call", map[string]any{"id": "c", "name": "bash", "input": map[string]any{"command": "go test ./..."}}}}, "be-1", "tool", "Bash go test ./..."},
		{"editing", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "tool.call", map[string]any{"id": "c", "name": "edit", "input": map[string]any{"path": "/work/api/x.go"}}}}, "be-1", "edit", "Edit api/x.go"},
		{"waiting", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "tool.call", map[string]any{"id": "c", "name": "wait", "input": map[string]any{}}}}, "be-1", "wait", "waits"},
		{"asking", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "tool.call", map[string]any{"id": "c", "name": "bash", "input": map[string]any{"command": "npm i"}}},
			{"be-1", "perm.ask", map[string]any{"tool": "bash", "command": "npm i", "reason": "network"}}}, "be-1", "ask", "wants to run `npm i`"},
		{"idle", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "agent.end", map[string]any{"id": "be-1", "state": "idle"}}}, "be-1", "idle", "waits for work"},
		{"stuck", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "model.request", request("r1")}, {"be-1", "agent.stuck", map[string]any{"phase": "nudge", "note": "the same call failed 4 times"}}}, "be-1", "stuck", "the same call failed 4 times"},
		{"done", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "agent.end", map[string]any{"id": "be-1", "state": "done", "evidence": "edited 2 files; go test passed"}}}, "be-1", "done", "edited 2 files; go test passed"},
		{"error", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1"}}, {"be-1", "model.request", request("r1")}, {"be-1", "model.error", map[string]any{"req": "r1", "error": "provider: 500 internal"}}}, "be-1", "stuck", "failed: provider: 500 internal"},
		{"the single agent is mgr", []step{{"main", "user.input", map[string]any{"text": "hi"}}, {"main", "model.request", request("r1")}, {"main", "model.response", response("r1", 0)}}, "mgr", "idle", "waits at the prompt"},
		{"the manager waits for its team", []step{{"", "session.start", map[string]any{"swarm": true}}, {"mgr", "board.op", boardOp("create", "T4", "todo", "", 0, map[string]any{"title": "a"})},
			{"mgr", "board.op", boardOp("create", "T5", "todo", "", 0, map[string]any{"title": "b"})}, {"mgr", "model.request", request("r1")}, {"mgr", "model.response", response("r1", 0)}}, "mgr", "wait", "waits for the team (T4 T5)"},
		{"blocked on a task", []step{{"be-1", "agent.spawn", map[string]any{"id": "be-1", "task": "T4"}}, {"be-1", "board.op", boardOp("block", "T4", "blocked", "be-1", 1, map[string]any{"blocked_on": "T2"})},
			{"be-1", "agent.end", map[string]any{"id": "be-1", "state": "idle"}}}, "be-1", "wait", "blocked on T2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{Root: "/work", StartedAt: t0})
			b := newLog(t0)
			for _, s := range tc.steps {
				h.feed(b.add(time.Second, s.agent, s.typ, s.data))
			}
			h.advance(2 * time.Second)
			got := lastState(h.decoded(), tc.id)
			if got == nil || got["s"] != tc.s || got["doing"] != tc.doing {
				t.Fatalf("state %v, want %s %q", got, tc.s, tc.doing)
			}
		})
	}
}

// An open question of the bridge shows its agent asking with the command, and its answer gives the agent back to the log's view.
func TestQuestionShowsTheAgentAsking(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(b.add(time.Second, "fe-1", "agent.spawn", map[string]any{"id": "fe-1", "role": "frontend", "task": "T6"}))
	h.set(t0.Add(2 * time.Second))
	h.tr.Question(wire.Question{ID: "q_x", Agent: "fe-1", Cmd: "npm install --save-dev vitest", Kind: "command"})
	if s := lastState(h.decoded(), "fe-1"); s["s"] != "ask" || s["doing"] != "wants to run `npm install --save-dev vitest`" || s["task"] != "T6" {
		t.Fatalf("while the question is open: %v", s)
	}
	h.set(t0.Add(3 * time.Second))
	h.tr.Answered(wire.Answer{QID: "q_x", Choice: 1})
	if s := lastState(h.decoded(), "fe-1"); s["s"] != "think" {
		t.Fatalf("after the answer: %v", s)
	}
	if a := ofKind(h.decoded(), "answer"); len(a) != 1 || a[0]["by"] != "you" {
		t.Fatalf("answer: %v", a)
	}
}

// The board's columns map to the page's four: a failed task is in todo with failed and its closure; one
// superseded or canceled keeps its column; a blocked one is running with blocked_on(T) as closure; a handover moves its owner.
func TestTaskMapping(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(
		b.add(time.Second, "mgr", "board.op", boardOp("create", "T1", "todo", "", 0, map[string]any{"title": "one", "deps": []string{"T0"}})),
		b.add(time.Second, "be-1", "board.op", boardOp("claim", "T1", "doing", "be-1", 1, nil)),
		b.add(time.Second, "be-1", "board.op", boardOp("finish", "T1", "review", "be-1", 2, nil)),
		b.add(time.Second, "mgr", "board.op", boardOp("finish", "T1", "failed", "be-1", 3, map[string]any{"closure": map[string]any{"kind": "denied"}})),
		b.add(time.Second, "mgr", "board.op", boardOp("create", "T2", "todo", "", 0, map[string]any{"title": "two"})),
		b.add(time.Second, "be-2", "board.op", boardOp("claim", "T2", "doing", "be-2", 1, nil)),
		b.add(time.Second, "mgr", "board.op", boardOp("finish", "T2", "failed", "be-2", 2, map[string]any{"closure": map[string]any{"kind": "superseded", "target": "T3"}})),
		b.add(time.Second, "mgr", "board.op", boardOp("create", "T3", "todo", "", 0, map[string]any{"title": "three"})),
		b.add(time.Second, "be-3", "board.op", boardOp("block", "T3", "blocked", "be-3", 1, map[string]any{"blocked_on": "T1", "attempts": 2})),
		b.add(time.Second, "mgr", "swarm.handover", map[string]any{"phase": "done", "task": "T3", "from": "be-3", "to": "be-4", "closure": map[string]any{"kind": "handed_off", "target": "be-4"}}),
		b.add(time.Second, "mgr", "board.op", boardOp("assign", "T3", "doing", "be-4", 2, map[string]any{"attempts": 2})),
	)
	tasks := ofKind(h.decoded(), "task")
	last := map[string]map[string]any{}
	var cols []string
	for _, e := range tasks {
		last[e["id"].(string)] = e
		if e["id"] == "T1" {
			cols = append(cols, e["s"].(string))
		}
	}
	if strings.Join(cols, " ") != "todo running verify todo" {
		t.Fatalf("T1's columns: %v", cols)
	}
	if t1 := last["T1"]; t1["failed"] != true || t1["closure"] != "denied" || t1["owner"] != "be-1" {
		t.Fatalf("T1: %v", t1)
	}
	if t2 := last["T2"]; t2["s"] != "running" || t2["failed"] != true || t2["closure"] != "superseded(T3)" {
		t.Fatalf("T2 keeps its column: %v", t2)
	}
	if t3 := last["T3"]; t3["owner"] != "be-4" || t3["s"] != "running" || t3["attempts"] != 2.0 {
		t.Fatalf("T3 after the handover: %v", t3)
	}
	var blocked bool
	for _, e := range tasks {
		if e["id"] == "T3" && e["closure"] == "blocked_on(T1)" && e["blocked"] == true && e["s"] == "running" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("T3 was never shown blocked on T1: %v", tasks)
	}
	if sys := ofKind(h.decoded(), "sys"); len(sys) != 1 || sys[0]["text"] != "T3 handed from be-3 to be-4" {
		t.Fatalf("handover row: %v", sys)
	}
	if n := ofKind(h.decoded(), "note"); len(n) != 1 || n[0]["text"] != "submitted T1" || n[0]["id"] != "be-1" {
		t.Fatalf("submission note: %v", n)
	}
}

// The queue's counters, as the terminal's merge line counts them (the State's merge counts): a conflict counts in conflicts and only
// a conflict does; a submission sent back to its worker, whether for a conflict, a failed verification or a refusal of the queue,
// counts in bounced (an empty submission in neither); a shared-tree team's failed verification gates count in bounced too.
func TestQueueCounters(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0, Verify: func() (string, bool) { return "go test {dirs}", true }})
	b := newLog(t0)
	h.feed(
		b.add(time.Second, "be-1", "merge.queued", map[string]any{"task": "T1: a", "position": 1}),
		b.add(time.Second, "be-1", "merge.conflict", map[string]any{"task": "T1: a", "files": []string{"x.go"}, "hunks": 2}),
		b.add(time.Second, "be-1", "merge.queued", map[string]any{"task": "T1: a", "position": 1}),
		b.add(time.Second, "be-1", "merge.verify_failed", map[string]any{"task": "T1: a", "cmd": "go test ./a", "exit_code": 2}),
		b.add(time.Second, "be-1", "merge.queued", map[string]any{"task": "T1: a", "position": 1}),
		b.add(time.Second, "be-1", "merge.rejected", map[string]any{"task": "T1: a", "reason": "out of scope"}),
		b.add(time.Second, "be-2", "merge.queued", map[string]any{"task": "T2: b", "position": 1}),
		b.add(time.Second, "be-2", "merge.rejected", map[string]any{"task": "T2: b", "reason": "nothing to merge", "empty": true}),
		b.add(time.Second, "be-2", "task.merge", map[string]any{"task": "T2", "outcome": "empty"}),
	)
	qs := ofKind(h.decoded(), "queue")
	last := qs[len(qs)-1]
	if last["conflicts"] != 1.0 || last["bounced"] != 3.0 || last["head"] != nil {
		t.Fatalf("counters: %v, want 1 conflict among 3 submissions sent back (a conflict, a failed verification, a refusal)", last)
	}
	// after each outcome: the conflict, the failed verification, the refusal
	var seen [][2]float64
	for _, q := range qs {
		if q["head"] == nil {
			seen = append(seen, [2]float64{q["conflicts"].(float64), q["bounced"].(float64)})
		}
	}
	if want := [][2]float64{{1, 1}, {1, 2}, {1, 3}, {1, 3}}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("conflicts and bounced after each outcome: %v, want %v", seen, want)
	}
	if qs[0]["cmd"] != "go test ./..." || qs[0]["step"] != "verifying" || qs[0]["head"] != "T1" {
		t.Fatalf("the first queue: %v", qs[0])
	}
	if m := ofKind(h.decoded(), "merge"); len(m) != 1 || m[0]["id"] != "T2" {
		t.Fatalf("merges: %v", m)
	}
	rows := ofKind(h.decoded(), "sys")
	if len(rows) != 3 || rows[0]["text"] != "T1: merge conflict in x.go" || rows[1]["text"] != "T1: go test ./a failed (exit 2)" {
		t.Fatalf("rows: %v", rows)
	}

	shared := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b = newLog(t0)
	shared.feed(
		b.add(time.Second, "mgr", "board.op", boardOp("create", "T1", "todo", "", 0, map[string]any{"title": "a"})),
		b.add(time.Second, "be-1", "board.op", boardOp("update", "T1", "doing", "be-1", 1, map[string]any{"verification_failures": 1})),
		b.add(time.Second, "be-1", "board.op", boardOp("update", "T1", "doing", "be-1", 2, map[string]any{"verification_failures": 2})),
	)
	if qs := ofKind(shared.decoded(), "queue"); len(qs) != 2 || qs[1]["bounced"] != 2.0 {
		t.Fatalf("shared tree: %v", qs)
	}
}

// A notice the session gives both to the sink and to the log is one row; past 20 rows in 10 seconds the rest are counted into one.
func TestNoticesAreShownOnceAndLimited(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	s := h.tr.Sink()
	s.Notice("", "warn", "instruction files: AGENTS.md is too long")
	h.drain()
	h.feed(b.add(time.Second, "", "notice", map[string]any{"level": "warn", "msg": "instruction files: AGENTS.md is too long"}))
	h.feed(b.add(time.Second, "", "notice", map[string]any{"level": "info", "msg": "mcp: started"}))
	rows := ofKind(h.decoded(), "sys")
	if len(rows) != 2 || rows[0]["glyph"] != "⚠" || rows[1]["glyph"] != "◇" || rows[0]["ch"] != "mgr" {
		t.Fatalf("rows: %v", rows)
	}
	for i := 0; i < 30; i++ {
		h.feed(b.add(10*time.Millisecond, "be-1", "notice", map[string]any{"level": "info", "msg": "n" + jsNum(float64(i))}))
	}
	h.advance(11 * time.Second)
	rows = ofKind(h.decoded(), "sys")
	if len(rows) != 21 || rows[20]["text"] != "12 more notices" {
		t.Fatalf("%d rows, the last %v", len(rows), rows[len(rows)-1])
	}
}

// The goal and the verdict are sent once whichever of the host and the log says them first.
func TestGoalFromHostAndLogIsSentOnce(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.set(t0.Add(time.Second))
	h.tr.Emit(&wire.Goal{S: "active", Objective: "Build the shop", Max: 20}, &wire.Verdict{Text: "checking: the judge reads the evidence of this turn", Kind: "checking"})
	h.feed(b.add(2*time.Second, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Max": 20}}))
	h.feed(b.add(time.Second, "", "goal.judge", map[string]any{"verdict": "done", "reason": "all merged", "left": []string{}}))
	h.tr.Emit(&wire.Verdict{Text: "met: all merged", Kind: "done"})
	h.feed(b.add(time.Second, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Max": 20, "Done": true}}))
	h.feed(b.add(time.Second, "", "goal.state", map[string]any{"goal": nil}))
	goals, verdicts := ofKind(h.decoded(), "goal"), ofKind(h.decoded(), "verdict")
	var states []string
	for _, g := range goals {
		states = append(states, g["s"].(string))
	}
	if strings.Join(states, " ") != "active met cleared" {
		t.Fatalf("goals: %v", goals)
	}
	if len(verdicts) != 2 || verdicts[1]["text"] != "met: all merged" {
		t.Fatalf("verdicts: %v", verdicts)
	}
}

// The roster has the manager first and the workers in the order they started, legs by start order, read-only roles and the scope
// of the task they hold; the harness's mailman is not in it; a roster frame precedes the first event of a new agent.
func TestRoster(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(
		b.add(time.Second, "", "session.start", map[string]any{"swarm": true, "model": "m1"}),
		b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "sc-1", "role": "scout", "model": "m2"}),
		b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "mm-1", "role": "mailman", "service": true}),
		b.add(time.Second, "mgr", "board.op", boardOp("create", "T4", "todo", "", 0, map[string]any{"title": "a", "files": []string{"api/catalog/**", "api/server.go"}})),
		b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T4", "model": "m2"}),
		b.add(time.Second, "be-1", "board.op", boardOp("claim", "T4", "doing", "be-1", 1, map[string]any{"files": []string{"api/catalog/**", "api/server.go"}})),
	)
	for i := 0; i < 9; i++ {
		h.feed(b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "ts-" + jsNum(float64(i+1)), "role": "tester"}))
	}
	r := h.tr.Roster()
	if len(r) != 12 || r[0].ID != "mgr" || r[0].Scope != "- (edits no file)" || r[0].Model != "m1" || r[0].Leg != -1 {
		t.Fatalf("roster: %+v", r)
	}
	if sc := r[1]; sc.ID != "sc-1" || !sc.RO || sc.Scope != "- (read-only)" || sc.K != 1 || sc.Leg != 0 || sc.Code != "sc" || sc.Nth != 1 || sc.Model != "m2" || sc.Spawn != 2 {
		t.Fatalf("scout: %+v", sc)
	}
	if be := r[2]; be.ID != "be-1" || be.Scope != "api/catalog/**, api/server.go" || be.K != 2 || be.Leg != 1 {
		t.Fatalf("worker: %+v", be)
	}
	if ninth := r[11]; ninth.K != 11 || ninth.Leg != 2 {
		t.Fatalf("the eleventh worker: %+v", ninth)
	}
	// the roster frame that names be-1 comes before be-1's first event
	sawRoster := false
	for _, f := range h.frames {
		if f.Type == "roster" {
			for _, e := range f.Data.(wire.RosterFrame).Roster {
				if e.ID == "be-1" {
					sawRoster = true
				}
			}
		}
		if f.Type == "ev" && strings.Contains(string(f.Data.(wire.EvFrame).Ev), `"id":"be-1"`) && !sawRoster {
			t.Fatal("an event of be-1 was published before a roster that names it")
		}
		if f.Type == "roster" && !f.Critical {
			t.Fatal("a roster frame is not critical")
		}
	}
	for _, e := range h.decoded() {
		if e["id"] == "mm-1" {
			t.Fatalf("an event of the mailman: %v", e)
		}
	}
}

// The frames carry a class: critical, coalescable with its key, or ordinary.
func TestFrameClasses(t *testing.T) {
	h := fullScenario(t)
	byKind := map[string]wire.Frame{}
	for _, f := range h.frames {
		if f.Type != "ev" {
			continue
		}
		var k struct{ K string }
		_ = jsonUnmarshal(f.Data.(wire.EvFrame).Ev, &k)
		if _, seen := byKind[k.K]; !seen {
			byKind[k.K] = f
		}
	}
	for k, f := range byKind {
		switch k {
		case "use", "layers":
			if !f.Coalescable || !strings.HasPrefix(f.Key, k+"/t1/") {
				t.Errorf("%s: %+v", k, f)
			}
		case "gov", "warm", "plan", "verdict", "queue", "mailstat":
			if !f.Coalescable || f.Key != k+"/t1" {
				t.Errorf("%s: %+v", k, f)
			}
		case "diff":
			if !f.Coalescable || !strings.HasPrefix(f.Key, "diff/t1/") {
				t.Errorf("%s: %+v", k, f)
			}
		case "more":
			if f.Critical || f.Coalescable {
				t.Errorf("more is ordinary: %+v", f)
			}
		case "ckpt":
			if !f.Critical {
				t.Errorf("the first ckpt of an id is critical: %+v", f)
			}
		default:
			if !f.Critical {
				t.Errorf("%s is critical: %+v", k, f)
			}
		}
	}
	var ckpts []wire.Frame
	for _, f := range h.frames {
		if f.Type == "ev" && strings.Contains(string(f.Data.(wire.EvFrame).Ev), `"k":"ckpt"`) {
			ckpts = append(ckpts, f)
		}
	}
	if len(ckpts) != 2 || !ckpts[0].Critical || !ckpts[1].Coalescable || ckpts[1].Key != "ckpt/t1/c07" {
		t.Fatalf("ckpt frames: %+v", ckpts)
	}
}
