package state

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A stall finding is open from its raise to its clear, one per kind, agent and task; a second raise of the same finding replaces
// the first, and each raise and clear is a line of the feed.
func TestStallFindingsRaiseAndClear(t *testing.T) {
	b := newB()
	st := fold(t,
		b.Emit("", events.TypeSwarmStall, map[string]any{"action": "raise", "kind": "claimed_no_progress", "task": "T4", "agent": "be-1", "detail": "be-1 claimed T4 9 minutes ago"}),
		b.Emit("", events.TypeSwarmStall, map[string]any{"action": "raise", "kind": "orphaned_task", "task": "T5"}),
		b.Emit("", events.TypeSwarmStall, map[string]any{"action": "raise", "kind": "claimed_no_progress", "task": "T4", "agent": "be-1", "detail": "be-1 claimed T4 10 minutes ago"}),
	)
	sn := st.Snapshot()
	if len(sn.Stalls) != 2 || sn.Stalls[0].Detail != "be-1 claimed T4 10 minutes ago" || sn.Stalls[1].Kind != "orphaned_task" {
		t.Fatalf("open findings: %s", js(sn.Stalls))
	}
	apply(t, st, b.Emit("", events.TypeSwarmStall, map[string]any{"action": "clear", "kind": "claimed_no_progress", "task": "T4", "agent": "be-1"}))
	if got := st.Stalls(); len(got) != 1 || got[0].Kind != "orphaned_task" {
		t.Fatalf("after the clear: %s", js(got))
	}
	if s := st.Stats(); s.Unknown != 0 || s.Bad != 0 {
		t.Fatalf("stats: %+v", s)
	}
	var lines int
	for _, l := range st.Snapshot().Feed {
		if l.Kind == FeedNote {
			lines++
		}
	}
	if lines != 4 {
		t.Fatalf("%d feed lines for 4 findings", lines)
	}
}

// The phases of a handover are kept (the newest HandoverLog), with the closure written as kind(target); a handover without a task
// is a bad payload.
func TestHandoversAreKept(t *testing.T) {
	b := newB()
	st := fold(t,
		b.Emit("mgr", events.TypeSwarmHandover, map[string]any{"phase": "begin", "task": "T5", "from": "be-2", "to": "be-3", "by": "mgr"}),
		b.Emit("mgr", events.TypeSwarmHandover, map[string]any{"phase": "done", "task": "T5", "from": "be-2", "to": "be-3", "by": "mgr", "closure": map[string]any{"kind": "handed_off", "target": "be-3"}}),
		b.Emit("mgr", events.TypeSwarmHandover, map[string]any{"phase": "abort", "from": "be-2"}),
	)
	sn := st.Snapshot()
	if len(sn.Handovers) != 2 || sn.Handovers[1].Closure != "handed_off(be-3)" || sn.Handovers[0].Phase != "begin" {
		t.Fatalf("handovers: %s", js(sn.Handovers))
	}
	if sn.Stats.Bad != 1 {
		t.Fatalf("a handover without a task was not counted bad: %+v", sn.Stats)
	}
	for i := 0; i < HandoverLog+5; i++ {
		apply(t, st, b.Emit("mgr", events.TypeSwarmHandover, map[string]any{"phase": "begin", "task": "T9"}))
	}
	if n := len(st.Snapshot().Handovers); n != HandoverLog {
		t.Fatalf("%d handovers kept, want %d", n, HandoverLog)
	}
}

// goal.state (keys without JSON tags) and goal.judge make the Goal: active, paused, met, cleared, with the judge's latest verdict
// kept while the objective stays the same.
func TestGoalStateAndJudge(t *testing.T) {
	b := newB()
	st := fold(t, b.Emit("", TypeGoalState, map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Max": 20}}))
	if g := st.Goal(); g == nil || g.State() != "active" || g.Max != 20 {
		t.Fatalf("goal: %s", js(g))
	}
	apply(t, st, b.Emit("", TypeGoalJudge, map[string]any{"verdict": "continue", "reason": "T4 unfinished", "left": []string{"T4 merged", "go test passes"}}))
	apply(t, st, b.Emit("", TypeGoalState, map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Max": 20, "Turns": 1, "Paused": "you interrupted it"}}))
	g := st.Goal()
	if g.State() != "paused" || g.Turns != 1 || g.Verdict != "continue" || !reflect.DeepEqual(g.Left, []string{"T4 merged", "go test passes"}) {
		t.Fatalf("goal after a judge and a pause: %s", js(g))
	}
	apply(t, st, b.Emit("", TypeGoalState, map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Done": true}}))
	if st.Goal().State() != "met" {
		t.Fatalf("goal: %s", js(st.Goal()))
	}
	apply(t, st, b.Emit("", TypeGoalState, map[string]any{"goal": nil}))
	if g := st.Goal(); g.State() != "cleared" || g.Objective != "Build the shop" {
		t.Fatalf("goal after null: %s", js(g))
	}
	left := make([]string, 12)
	for i := range left {
		left[i] = "x"
	}
	apply(t, st, b.Emit("", TypeGoalJudge, map[string]any{"verdict": "blocked", "reason": "needs a key", "left": left}))
	if g := st.Goal(); len(g.Left) != 8 || g.Verdict != "blocked" {
		t.Fatalf("left is not bounded: %s", js(g))
	}
	if s := st.Stats(); s.Unknown != 0 {
		t.Fatalf("unknown: %v", s.UnknownTypes)
	}
}

// A checkpoint event creates the checkpoint and updates it in place as its files change; a restore is counted on it.
func TestCheckpointEvents(t *testing.T) {
	b := newB()
	st := fold(t,
		b.Emit("", TypeCheckpoint, map[string]any{"id": "cp_0007", "label": "turn 3", "files": []string{}, "agents": []string{}}),
		b.Emit("", TypeCheckpoint, map[string]any{"id": "cp_0007", "label": "turn 3", "files": []string{"a.go", "b.go"}, "agents": []string{"be-1"}}),
		b.Emit("", TypeCheckpoint, map[string]any{"id": "cp_0008", "label": "turn 4", "files": []string{"c.go"}, "safety": true}),
		b.Emit("", TypeCheckpointRestore, map[string]any{"id": "cp_0007", "files": []string{"a.go"}}),
		b.Emit("", TypeCheckpoint, map[string]any{"label": "no id"}),
	)
	cs := st.Snapshot().Checkpoints
	if len(cs) != 2 || cs[0].Files != 2 || cs[0].Restored != 1 || !reflect.DeepEqual(cs[0].Agents, []string{"be-1"}) || !cs[1].Safety {
		t.Fatalf("checkpoints: %s", js(cs))
	}
	if s := st.Stats(); s.Bad != 1 || s.Unknown != 0 {
		t.Fatalf("stats: %+v", s)
	}
}

// board.op's closure, blocked_on, kind and verification_failures reach the task, in the full form and in the operand form; a
// closure that is not the board's shape is left out.
func TestBoardClosuresAndBlocks(t *testing.T) {
	b := newB()
	rev := func(n uint64) *uint64 { return &n }
	st := fold(t,
		b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "create", "task": "T1", "title": "plan it", "kind": "plan", "status": "todo", "rev": rev(0)}),
		b.Emit("be-1", events.TypeBoardOp, map[string]any{"op": "block", "task": "T1", "status": "blocked", "owner": "be-1", "blocked_on": "T2", "rev": rev(1), "closure": nil}),
		b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "finish", "task": "T1", "status": "failed", "owner": "be-1", "rev": rev(2), "closure": map[string]any{"kind": "superseded", "target": "T7"}, "verification_failures": 2}),
		b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "create", "task": "T2", "title": "x", "status": "todo", "rev": rev(0), "closure": 7}),
		b.Emit("mgr", events.TypeBoardOp, map[string]any{"op": "finish", "task": "T3", "status": "done", "closure": map[string]any{"kind": "verified"}}),
	)
	t1, _ := st.Task("T1")
	if t1.Kind != "plan" || t1.Closure != "superseded(T7)" || t1.BlockedOn != "" || t1.VerificationFailures != 2 || t1.State != TaskFailed {
		t.Fatalf("T1: %s", js(t1))
	}
	if t2, _ := st.Task("T2"); t2.Closure != "" {
		t.Fatalf("T2: %s", js(t2))
	}
	if t3, _ := st.Task("T3"); t3.Closure != "verified" {
		t.Fatalf("T3 (operand form): %s", js(t3))
	}
	st2 := fold(t, b.Emit("be-1", events.TypeBoardOp, map[string]any{"op": "block", "task": "T4", "status": "blocked", "owner": "be-1", "blocked_on": "T2", "rev": rev(1)}))
	if t4, _ := st2.Task("T4"); t4.BlockedOn != "T2" || t4.State != TaskRunning {
		t.Fatalf("T4: %s", js(t4))
	}
}

// OnStatus is told once per change of an agent's status, in order, after the lock is released (a callback may read the State);
// OnChange names what each event touched.
func TestOnStatusOncePerChange(t *testing.T) {
	b := newB()
	st := New()
	type change struct {
		agent    string
		from, to Status
	}
	var got []change
	var touched [][]string
	st.OnStatus(func(agent string, from, to Status, at time.Time) {
		_ = st.Stats() // the lock is not held
		got = append(got, change{agent, from, to})
	})
	st.OnChange(func(agents, tasks []string) {
		touched = append(touched, append(append([]string(nil), agents...), tasks...))
	})
	apply(t, st,
		b.Spawn("be-1", "backend", "T1", "mgr"),
		b.Request("be-1", "r1", "m", "pk"),
		b.Call("be-1", "c1", "bash", map[string]any{"command": "go test ./..."}),
		b.Call("be-1", "c2", "bash", map[string]any{"command": "go vet ./..."}),
		b.Result("be-1", "c2", "bash", false, 5),
		b.Result("be-1", "c1", "bash", false, 5),
		b.Emit("", "notice", map[string]any{"msg": "nothing about an agent"}),
	)
	want := []change{{"be-1", "", StatusStarting}, {"be-1", StatusStarting, StatusThinking}, {"be-1", StatusThinking, StatusTool}, {"be-1", StatusTool, StatusThinking}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status changes:\n got %v\nwant %v", got, want)
	}
	if len(touched) != 6 || touched[0][0] != "be-1" {
		t.Fatalf("touched: %v", touched)
	}
	st.ApplyAll([]events.Event{b.Emit("be-1", events.TypeAgentEnd, map[string]any{"id": "be-1", "state": "idle"})})
	if last := got[len(got)-1]; last.to != StatusIdle {
		t.Fatalf("ApplyAll did not report: %v", got)
	}
}

// The callbacks are safe with concurrent appliers and readers.
func TestCallbacksUnderConcurrency(t *testing.T) {
	st := New()
	var mu sync.Mutex
	n := 0
	st.OnChange(func(agents, tasks []string) {
		mu.Lock()
		n++
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			b := newB()
			for i := 0; i < 200; i++ {
				e := b.Spawn("be-"+string(rune('1'+g)), "backend", "", "mgr")
				e.Seq = 0
				st.Apply(e)
				_ = st.Snapshot()
			}
		}(g)
	}
	wg.Wait()
	if n != 800 {
		t.Fatalf("%d notes for 800 events", n)
	}
}

// LayerSplit splits a prompt as the stack bar does: G0 is the estimate bounded by what the request did not size, G5 the rest of it
// plus any section of another name, so that the six add up to the prompt.
func TestLayerSplit(t *testing.T) {
	sec := func(name string, tok int) Section { return Section{Name: name, Tokens: tok} }
	for _, tc := range []struct {
		name string
		stk  Stack
		g0   int
		want [6]int
	}{
		{"nothing sent", Stack{}, 4000, [6]int{}},
		{"every layer", Stack{Sections: []Section{sec("spine", 30), sec("shared", 1000), sec("notes", 50), sec("role", 200)}, Unsectioned: 5000}, 4112, [6]int{4112, 1000, 200, 50, 30, 888}},
		{"estimate over the rest", Stack{Sections: []Section{sec("shared", 10)}, Unsectioned: 300}, 4112, [6]int{300, 10, 0, 0, 0, 0}},
		{"no estimate", Stack{Sections: []Section{sec("role", 10)}, Unsectioned: 300}, 0, [6]int{0, 0, 10, 0, 0, 300}},
		{"negative estimate", Stack{Unsectioned: 300}, -5, [6]int{0, 0, 0, 0, 0, 300}},
		{"another section", Stack{Sections: []Section{sec("extra", 7)}, Unsectioned: 10}, 4, [6]int{4, 0, 0, 0, 0, 13}},
	} {
		if got := LayerSplit(Agent{Stack: tc.stk}, tc.g0); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ToolSummary is the summary the State keeps for a call, so that a follower that shows a tool's start before the log has it shows
// the same line.
func TestToolSummaryMatchesTheFold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"bash", map[string]any{"command": "go test ./..."}},
		{"read", map[string]any{"path": "api/x.go"}},
		{"grep", map[string]any{"pattern": "TODO", "path": "api"}},
		{"task", map[string]any{"action": "claim", "id": "T4"}},
		{"mail", map[string]any{"to": "fe-1", "text": "hello\nthere"}},
		{"custom_tool", map[string]any{"query": "q"}},
	} {
		b := newB()
		st := fold(t, b.Spawn("be-1", "backend", "", "mgr"), b.Call("be-1", "c1", tc.name, tc.input))
		raw, _ := json.Marshal(tc.input)
		a, _ := st.AgentLite("be-1")
		if got := ToolSummary(tc.name, raw); got != a.ToolSummary || got == "" {
			t.Errorf("%s: ToolSummary %q, the fold %q", tc.name, got, a.ToolSummary)
		}
	}
	if ToolStatus("write") != StatusEditing || ToolStatus("wait") != StatusWaiting || ToolStatus("bash") != StatusTool {
		t.Fatal("ToolStatus")
	}
}

// A response at a price nobody knows is counted per agent as unpriced cache reads.
func TestUnpricedReadsPerAgent(t *testing.T) {
	b := newB()
	st := fold(t, b.Spawn("be-1", "backend", "", "mgr"), b.Request("be-1", "r1", "no-such-model-xyz", "pk"),
		b.Response("be-1", "r1", "no-such-model-xyz", 10, 900, 0, 5, 0))
	a, _ := st.AgentLite("be-1")
	if a.UnpricedReadTokens != 900 || a.SavedUSD != 0 {
		t.Fatalf("unpriced %d saved %v", a.UnpricedReadTokens, a.SavedUSD)
	}
}

// swarm.unfinished carries what was left in the key the producer writes (internal/swarm/swarm.go afterManagerRun: "unfinished"), and
// the line of the feed shows it; a log that names the key reason is read as well.
func TestUnfinishedLineCarriesWhatWasLeft(t *testing.T) {
	b := newB()
	st := fold(t,
		b.Emit("mgr", events.TypeSwarmUnfinished, map[string]any{"unfinished": "running: be-1 (T3); waiting: T5"}),
		b.Emit("mgr", events.TypeSwarmUnfinished, map[string]any{"reason": "T6 not started"}),
		b.Emit("mgr", events.TypeSwarmUnfinished, map[string]any{}),
	)
	var details []string
	for _, l := range st.Snapshot().Feed {
		if l.Text == "the run ended with work unfinished" {
			details = append(details, l.Detail)
		}
	}
	if !reflect.DeepEqual(details, []string{"running: be-1 (T3); waiting: T5", "T6 not started", ""}) {
		t.Fatalf("details of the unfinished lines: %q", details)
	}
	if st.Snapshot().Supervision.Unfinished != 3 {
		t.Fatalf("supervision: %s", js(st.Snapshot().Supervision))
	}
}

// A recovered panic of an agent's output sink is a line of the feed that says so, names the agent and shows the panic.
func TestSinkPanicLineSaysWhatHappened(t *testing.T) {
	b := newB()
	st := fold(t, b.Emit("swarm", "sink.panic", map[string]any{"agent": "be-1", "panic": "index out of range [3] with length 2"}))
	feed := st.Snapshot().Feed
	if len(feed) != 1 || feed[0].Kind != FeedError || feed[0].Agent != "be-1" ||
		feed[0].Text != "a panic in be-1's output sink was recovered" || feed[0].Detail != "index out of range [3] with length 2" {
		t.Fatalf("feed: %s", js(feed))
	}
}

// FeedSince gives the lines an event wrote and the total to ask from next; the lines the ring overwrote are not given, and a total
// above the feed's (the State was reset) counts from the start.
func TestFeedSinceGivesWhatEachEventWrote(t *testing.T) {
	b := newB()
	st := New()
	lines, total := st.FeedSince(0)
	if len(lines) != 0 || total != 0 {
		t.Fatalf("an empty feed gave %d lines, total %d", len(lines), total)
	}
	apply(t, st, b.Emit("mgr", events.TypeSwarmWake, map[string]any{"n": 1, "note": "be-1 finished"}))
	lines, total = st.FeedSince(total)
	if len(lines) != 1 || total != 1 || lines[0].Text != "the idle manager was woken" || lines[0].Detail != "be-1 finished" {
		t.Fatalf("after a wake: %s, total %d", js(lines), total)
	}
	apply(t, st, b.Emit("mgr", events.TypeSwarmWakePaused, map[string]any{"n": 3}), b.Emit("swarm", "swarm.shutdown", map[string]any{}))
	lines, total = st.FeedSince(total)
	if len(lines) != 2 || total != 3 || lines[0].Text != "the manager will not be woken again until you write to it" || lines[1].Text != "the swarm shut down with agents still running" {
		t.Fatalf("after two more: %s, total %d", js(lines), total)
	}
	if lines, _ = st.FeedSince(total); len(lines) != 0 {
		t.Fatalf("nothing happened and %d lines came", len(lines))
	}
	for i := 0; i < FeedCap+10; i++ {
		apply(t, st, b.Emit("mgr", events.TypeSwarmWake, map[string]any{"n": i}))
	}
	lines, total2 := st.FeedSince(total)
	if len(lines) != FeedCap || total2 != total+FeedCap+10 {
		t.Fatalf("after %d lines the ring holds %d of them; total %d", FeedCap+10, len(lines), total2)
	}
	st.Reset()
	apply(t, st, b.Emit("mgr", events.TypeSwarmWake, map[string]any{"n": 1}))
	if lines, total = st.FeedSince(total2); len(lines) != 1 || total != 1 {
		t.Fatalf("after a reset: %d lines, total %d", len(lines), total)
	}
}

// LastCompaction is the agent's newest commit, with the moment the planner decided at.
func TestLastCompactionKeepsTheMoment(t *testing.T) {
	b := newB()
	st := New()
	if _, ok := st.LastCompaction("be-1"); ok {
		t.Fatal("a compaction of an agent nobody has heard of")
	}
	apply(t, st, b.Spawn("be-1", "backend", "", "mgr"))
	if _, ok := st.LastCompaction("be-1"); ok {
		t.Fatal("a compaction before any commit")
	}
	apply(t, st,
		b.Emit("be-1", events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "fork", "warm": false}),
		b.Emit("be-1", events.TypeCompactCommit, map[string]any{"reason": "thread over its limit", "snap_tokens": 9000, "spine_added": 300, "retained_tokens": 700}),
		b.Emit("be-1", events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "fork", "warm": true}),
		b.Emit("be-1", events.TypeCompactCommit, map[string]any{"reason": "thread over its limit", "snap_tokens": 8000, "spine_added": 200, "retained_tokens": 600}),
	)
	c, ok := st.LastCompaction("be-1")
	if !ok || c.Moment != "warm" || c.Before != 8000 || c.After != 800 {
		t.Fatalf("the newest compaction: %s", js(c))
	}
}
