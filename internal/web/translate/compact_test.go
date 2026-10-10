package translate

import (
	"strings"
	"testing"
	"time"
)

// doings are the doing lines of an agent's state events, in order, a line that the next event says again counted once.
func doings(evs []map[string]any, id string) []string {
	var out []string
	for _, e := range ofKind(evs, "state") {
		if d := e["doing"].(string); e["id"] == id && (len(out) == 0 || out[len(out)-1] != d) {
			out = append(out, d)
		}
	}
	return out
}

// compactionLog starts a team whose worker be-1 has a model request in flight, and returns the harness and its log.
func compactionLog(t *testing.T) (*harness, *logBuilder) {
	t.Helper()
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(b.add(time.Second, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work"}))
	h.feed(b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"}))
	h.feed(b.add(time.Second, "mgr", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1", "model": "m1"}))
	h.feed(b.add(time.Second, "be-1", "model.request", request("r1")))
	h.advance(time.Second)
	return h, b
}

// From the planner's decision to the commit (and while the compactor's patch is made) the agent's state says it folds its thread, in
// the terminal's words; the state goes back when the compaction is committed or rejected, and a patch that was unusable but is folded
// mechanically instead is still a compaction at work.
func TestAnAgentThatCompactsSaysSo(t *testing.T) {
	h, b := compactionLog(t)
	step := func(typ string, data map[string]any) {
		h.feed(b.add(time.Second, "be-1", typ, data))
		h.advance(time.Second)
	}
	want := []string{"thinking"}
	check := func(what string) {
		t.Helper()
		if got := doings(h.decoded(), "be-1"); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s: the doing lines are %q, want %q", what, got, want)
		}
	}
	check("before the plan")
	step("compact.plan", map[string]any{"decision": "commit?", "yes": false, "warm": true})
	check("a plan that only asks whether to commit")
	step("compact.plan", map[string]any{"decision": "start", "mode": "fork", "warm": true, "reason": "thread over its limit"})
	want = append(want, "folds its thread")
	check("after the decision to start")
	step("compact.patch", map[string]any{"stage": "request", "reason": "thread over its limit", "thread_from": 1, "thread_to": 7})
	check("while the patch is made")
	step("compact.reject", map[string]any{"stage": "model_patch", "reason": "the patch was unusable", "fallback": "mechanical"})
	check("after a patch that was unusable and is folded mechanically")
	step("compact.commit", map[string]any{"reason": "thread over its limit", "snap_tokens": 9000, "spine_added": 300, "retained_tokens": 700})
	want = append(want, "thinking")
	check("after the commit")
	step("compact.plan", map[string]any{"decision": "start", "mode": "fork", "warm": false})
	want = append(want, "folds its thread")
	check("a second decision")
	step("compact.reject", map[string]any{"stage": "stale", "reason": "the thread moved"})
	want = append(want, "thinking")
	check("after a rejection")
	// an agent that runs a tool shows the tool, as the terminal does
	step("compact.plan", map[string]any{"decision": "start", "mode": "fork", "warm": false})
	want = append(want, "folds its thread")
	step("tool.call", map[string]any{"id": "c1", "name": "bash", "input": map[string]any{"command": "go test ./..."}})
	want = append(want, "Bash go test ./...")
	check("a tool call during the compaction")
}

// The page's row of a compaction says whether the cache was warm or cold when the planner decided on it, as the terminal's does; a
// compaction with no plan before it (an emergency or a mask compaction) says neither.
func TestACompactionSaysWhetherTheCacheWasWarm(t *testing.T) {
	h, b := compactionLog(t)
	step := func(typ string, data map[string]any) {
		h.feed(b.add(time.Second, "be-1", typ, data))
	}
	commit := func(reason string) {
		step("compact.commit", map[string]any{"reason": reason, "snap_tokens": 9000, "spine_added": 300, "retained_tokens": 700})
	}
	step("compact.plan", map[string]any{"decision": "start", "mode": "fork", "warm": false})
	commit("thread over its limit")
	step("compact.plan", map[string]any{"decision": "start", "mode": "fork", "warm": true})
	commit("thread over its limit")
	commit("emergency: the prompt would not fit")
	step("compact.plan", map[string]any{"decision": "start", "mode": "mask", "warm": false})
	commit("mask: bulky results")
	var moments []any
	for _, e := range ofKind(h.decoded(), "compact") {
		if e["from"] != 9000.0 || e["to"] != 1000.0 || e["pct"] != -89.0 {
			t.Errorf("the compaction: %v", e)
		}
		moments = append(moments, e["moment"])
	}
	if len(moments) != 4 || moments[0] != "cold" || moments[1] != "warm" || moments[2] != nil || moments[3] != "cold" {
		t.Fatalf("the moments of the four compactions: %v", moments)
	}
}

// The merge that was rolled back after its verification failed is a row of its own after the row of the failure; it counts as no second
// bounce of the submission.
func TestARolledBackMergeIsSaid(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(b.add(time.Second, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work", "isolation": "worktree"}))
	h.feed(b.add(time.Second, "be-1", "merge.queued", map[string]any{"position": 1, "task": "T1: catalogue"}))
	h.feed(b.add(time.Second, "be-1", "merge.verify_failed", map[string]any{"task": "T1: catalogue", "cmd": "go test ./...", "exit_code": 1}))
	h.feed(b.add(time.Second, "be-1", "merge.rolled_back", map[string]any{"task": "T1: catalogue", "tip": "abcdef0123456789"}))
	h.advance(time.Second)
	rows := ofKind(h.decoded(), "sys")
	if len(rows) != 2 || rows[0]["text"] != "T1: go test ./... failed (exit 1)" ||
		rows[1]["text"] != "T1: the merge was rolled back" || rows[1]["glyph"] != "↺" || rows[1]["ag"] != "be-1" || rows[1]["task"] != "T1" || rows[1]["ch"] != "mgr" {
		t.Fatalf("rows: %v", rows)
	}
	if q := ofKind(h.decoded(), "queue"); q[len(q)-1]["bounced"] != 1.0 {
		t.Fatalf("the queue's counters after the rollback: %v", q[len(q)-1])
	}
	// the name of the task is data
	hostile := "T2: <script>x</script>\x1b[2J " + secret
	h.feed(b.add(time.Second, "be-1", "merge.rolled_back", map[string]any{"task": hostile}))
	raw := string(lines(h.raws()))
	for _, bad := range []string{"\x1b", "<script>", secret} {
		if strings.Contains(raw, bad) {
			t.Errorf("the output carries %q", bad)
		}
	}
}
