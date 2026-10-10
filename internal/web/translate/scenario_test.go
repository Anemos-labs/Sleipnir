package translate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// boardOp is a board.op of one task in the board's full form (internal/swarm/board.go setTask), with the operands given.
func boardOp(op, task, status, owner string, rev uint64, extra map[string]any) map[string]any {
	m := map[string]any{"op": op, "version": rev + 1, "task": task, "status": status, "owner": owner, "line": "", "result": "", "evidence": "",
		"attempts": 1, "rev": rev, "files": []string{"api/" + task + "/**"}, "closure": nil, "blocked_on": "", "verification_failures": 0}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// request and response are a main model request of an agent and its answer.
func request(req string) map[string]any {
	return map[string]any{"req": req, "kind": "main", "model": "m1", "prefix_key": "pk-shared-1", "sections": []map[string]any{
		{"name": "shared", "tokens": 1000, "hash": "h1"}, {"name": "role", "tokens": 200}, {"name": "notes", "tokens": 50}, {"name": "spine", "tokens": 30}},
		"breakpoints": []map[string]any{{"label": "shared", "ttl": int64(300 * time.Second)}}}
}

func response(req string, read int) map[string]any {
	return map[string]any{"req": req, "usage": map[string]any{"input_tokens": 500, "cache_read_tokens": read, "output_tokens": 100},
		"cost_usd": 0.01, "total_ms": 400, "stop": "end_turn"}
}

// fullScenario runs a hand-built team through a translator with every source: the log (the board, tasks failed and handed over,
// the merge queue, mail, a stall, an anomaly, a compaction, a checkpoint, the goal and its judge, a retry), the sink (streamed text
// of the manager and a worker, a write shown as code, a refusal for want of anyone to answer, a notice) and the host (the person's
// message, a turn, a question and its answer, a steer, an interrupt).
func fullScenario(tb testing.TB) *harness {
	tb.Helper()
	verify := func() (string, bool) { return "go test {dirs}", true }
	h := newHarness(tb, Config{Root: "/work/shop", StartedAt: t0, Verify: verify})
	tr := h.tr
	snk := tr.Sink()
	b := newLog(t0)
	step := func(dt time.Duration, agent, typ string, data any) {
		b.at = h.now() // the log's clock is the harness's
		h.feed(b.add(dt, agent, typ, data))
	}
	host := func(dt time.Duration, evs ...wire.Event) {
		h.set(h.now().Add(dt))
		tr.Emit(evs...)
	}
	sinkDo := func(dt time.Duration, fn func()) {
		h.set(h.now().Add(dt))
		fn()
		h.drain()
	}

	host(100*time.Millisecond, &wire.Say{Who: "you", Text: "/goal Build the shop"}, &wire.Turn{S: "start"})
	step(10*time.Millisecond, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work/shop", "isolation": "worktree",
		"models": map[string]any{"m1": map[string]any{"source": "session", "input_per_m": 1.0, "output_per_m": 4.0, "cache_read_per_m": 0.1, "ttl_s": 300}}})
	step(10*time.Millisecond, "mgr", "user.input", map[string]any{"text": "Build the shop"})
	step(10*time.Millisecond, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Max": 20}})
	step(10*time.Millisecond, "mgr", "model.request", request("r1"))
	sinkDo(50*time.Millisecond, func() {
		snk.Text("mgr", "I'll have three scouts ")
		snk.Text("mgr", "map the API")
	})
	h.advance(150 * time.Millisecond)
	sinkDo(10*time.Millisecond, func() { snk.Text("mgr", " and then split the build.") })
	sinkDo(10*time.Millisecond, func() { snk.Response("mgr", nil, 0) })
	step(10*time.Millisecond, "mgr", "model.response", response("r1", 0))
	step(10*time.Millisecond, "mgr", "tool.call", map[string]any{"id": "p1", "name": "plan", "input": map[string]any{"items": []map[string]any{
		{"step": "Survey the API", "status": "done"}, {"step": "Build the catalogue", "status": "doing"}, {"step": "Review"}}}})
	step(5*time.Millisecond, "mgr", "tool.result", map[string]any{"id": "p1", "name": "plan", "error": false})
	step(10*time.Millisecond, "mgr", "board.op", boardOp("create", "T1", "todo", "", 0, map[string]any{"title": "catalogue", "role": "backend", "deps": []string{}}))
	step(10*time.Millisecond, "mgr", "board.op", boardOp("create", "T2", "todo", "", 0, map[string]any{"title": "cart", "role": "backend", "deps": []string{"T1"}}))
	step(10*time.Millisecond, "mgr", "board.op", boardOp("create", "T3", "todo", "", 0, map[string]any{"title": "old cart", "role": "backend"}))
	step(10*time.Millisecond, "mgr", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "task": "T1", "model": "m1"})
	step(10*time.Millisecond, "mgr", "agent.spawn", map[string]any{"id": "rv-1", "role": "reviewer", "task": "", "model": "m1"})
	step(10*time.Millisecond, "be-1", "board.op", boardOp("claim", "T1", "doing", "be-1", 1, nil))
	step(10*time.Millisecond, "be-1", "model.request", request("r2"))
	sinkDo(20*time.Millisecond, func() { snk.Text("be-1", "Total() keeps cents as int64. ") })
	step(10*time.Millisecond, "be-1", "model.response", map[string]any{"req": "r2", "usage": map[string]any{"input_tokens": 300, "cache_read_tokens": 1500,
		"output_tokens": 80}, "cost_usd": 0.002, "total_ms": 300, "stop": "tool_use"})
	write := core.Block{Kind: core.BlockToolUse, ToolID: "w1", ToolName: "write", Input: json.RawMessage(`{"path":"/work/shop/api/catalog/items.go","content":"package catalog\n\nconst Size = 12\n"}`)}
	sinkDo(10*time.Millisecond, func() { snk.ToolStart("be-1", write) })
	step(5*time.Millisecond, "be-1", "tool.call", map[string]any{"id": "w1", "name": "write", "input": map[string]any{"path": "api/catalog/items.go"}})
	sinkDo(30*time.Millisecond, func() {
		snk.ToolEnd("be-1", write, &tools.Result{Text: "Created api/catalog/items.go (3 lines, 34 bytes)", Meta: map[string]any{"path": "api/catalog/items.go", "created": true, "bytes": 34}}, 30*time.Millisecond)
	})
	step(5*time.Millisecond, "be-1", "tool.result", map[string]any{"id": "w1", "name": "write", "error": false})
	step(10*time.Millisecond, "be-1", "mail.send", map[string]any{"id": "m1", "from": "be-1", "to": "rv-1", "kind": "info", "text": "catalogue: GET /items?page&size"})
	step(10*time.Millisecond, "rv-1", "mail.deliver", map[string]any{"id": "m1", "from": "be-1"})
	step(10*time.Millisecond, "", "lease", map[string]any{"action": "conflict", "agent": "rv-1", "path": "/work/shop/api/server.go", "holder": "be-1"})
	step(10*time.Millisecond, "", "board.op", map[string]any{"op": "alert", "version": 9, "kind": "stalled", "key": "be-1", "text": "be-1 has been quiet for 9 minutes"})
	step(10*time.Millisecond, "", "swarm.stall", map[string]any{"action": "raise", "kind": "claimed_no_progress", "task": "T1", "agent": "be-1", "detail": "be-1 claimed T1 9 minutes ago and has not reported progress"})
	step(10*time.Millisecond, "be-1", "cache.anomaly", map[string]any{"kind": "low_hit", "expected_read": 4500, "actual_read": 0, "req": "r2"})
	step(10*time.Millisecond, "be-1", "compact.commit", map[string]any{"snap_tokens": 1400, "spine_added": 125, "retained_tokens": 500})
	step(10*time.Millisecond, "", "checkpoint", map[string]any{"id": "cp_0007", "label": "turn 1: build the shop", "files": []string{}, "agents": []string{}, "time": h.now().Format(time.RFC3339Nano)})
	step(2*time.Second, "", "checkpoint", map[string]any{"id": "cp_0007", "label": "turn 1: build the shop", "files": []string{"api/catalog/items.go"}, "agents": []string{"be-1"}, "time": h.now().Format(time.RFC3339Nano), "added": 3, "removed": 1})
	step(10*time.Millisecond, "rv-1", "model.request", map[string]any{"req": "r3", "kind": "main", "model": "x-unpriced"})
	step(10*time.Millisecond, "rv-1", "model.response", map[string]any{"req": "r3", "model": "x-unpriced", "usage": map[string]any{"input_tokens": 100, "cache_read_tokens": 900, "output_tokens": 10}, "total_ms": 100, "stop": "end_turn"})
	step(10*time.Millisecond, "", "governor", map[string]any{"action": "rate-limited", "rate_per_min": 30, "pause_ms": 1000, "inflight": 3, "queued": 2})
	step(10*time.Millisecond, "", "swarm.stall", map[string]any{"action": "clear", "kind": "claimed_no_progress", "task": "T1", "agent": "be-1"})
	step(10*time.Millisecond, "", "board.op", map[string]any{"op": "alert-clear", "version": 10, "kind": "stalled", "key": "be-1"})
	// the question of rv-1, its answer, and a refusal for want of anyone to answer
	h.set(h.now().Add(10 * time.Millisecond))
	tr.Question(wire.Question{ID: "q_aaaaaaaaaaaaaaaaaaaaaaaaaa", Agent: "rv-1", Cmd: "npm install --save-dev vitest", Cwd: "web/", Why: "installs a package", What: "this command", Kind: "command", Tool: "bash"})
	h.set(h.now().Add(500 * time.Millisecond))
	tr.Answered(wire.Answer{QID: "q_aaaaaaaaaaaaaaaaaaaaaaaaaa", Choice: 3, Note: "use what is there", By: "you"})
	host(10*time.Millisecond, &wire.Steer{To: "rv-1", Text: "use what is there", Quiet: true})
	bash := core.Block{Kind: core.BlockToolUse, ToolID: "b1", ToolName: "bash", Input: json.RawMessage(`{"command":"npm run lint"}`)}
	sinkDo(10*time.Millisecond, func() {
		snk.ToolStart("rv-1", bash)
		snk.ToolEnd("rv-1", bash, &tools.Result{Text: "approval required: npm run lint" + perm.NoOneToAsk, IsError: true, Meta: map[string]any{"error_kind": "permission"}}, 5*time.Millisecond)
		snk.Notice("rv-1", "warn", "the response reached the output limit")
	})
	// the merge queue
	step(10*time.Millisecond, "be-1", "board.op", boardOp("update", "T1", "doing", "be-1", 2, nil))
	step(10*time.Millisecond, "be-1", "merge.queued", map[string]any{"position": 1, "task": "T1: catalogue"})
	step(400*time.Millisecond, "be-1", "merge.merged", map[string]any{"task": "T1: catalogue", "after": "abcdef0123456789", "files": []string{"api/catalog/items.go"}, "verified": true})
	step(10*time.Millisecond, "be-1", "task.merge", map[string]any{"task": "T1", "outcome": "merged", "commit": "abcdef0123456789"})
	step(10*time.Millisecond, "be-1", "board.op", boardOp("finish", "T1", "review", "be-1", 3, nil))
	step(10*time.Millisecond, "mgr", "board.op", boardOp("finish", "T1", "done", "be-1", 4, map[string]any{"closure": map[string]any{"kind": "verified"}}))
	step(10*time.Millisecond, "", "merge.verify_failed", map[string]any{"task": "T2: cart", "cmd": "go test ./api/cart/...", "exit_code": 1})
	// a handover and a task failed as superseded
	step(10*time.Millisecond, "mgr", "swarm.handover", map[string]any{"phase": "begin", "task": "T2", "from": "be-1", "to": "be-2", "by": "mgr"})
	step(10*time.Millisecond, "mgr", "agent.spawn", map[string]any{"id": "be-2", "role": "backend", "task": "T2", "model": "m1", "handover_from": "be-1"})
	step(10*time.Millisecond, "mgr", "board.op", boardOp("assign", "T2", "doing", "be-2", 1, nil))
	step(10*time.Millisecond, "mgr", "swarm.handover", map[string]any{"phase": "done", "task": "T2", "from": "be-1", "to": "be-2", "by": "mgr", "closure": map[string]any{"kind": "handed_off", "target": "be-2"}})
	step(10*time.Millisecond, "mgr", "board.op", boardOp("finish", "T3", "failed", "", 1, map[string]any{"closure": map[string]any{"kind": "superseded", "target": "T2"}}))
	step(10*time.Millisecond, "mgr", "model.error", map[string]any{"req": "r9", "kind": "rate_limit", "status": 429, "attempt": 1, "delay_ms": 2000})
	// the judge, the end of the turn, an interrupt
	step(10*time.Millisecond, "", "goal.judge", map[string]any{"verdict": "continue", "reason": "T2 unfinished", "left": []string{"T2 merged"}})
	host(10*time.Millisecond, &wire.Final{}, &wire.Turn{S: "end"})
	host(10*time.Millisecond, &wire.Interrupt{ID: "turn"})
	h.advance(6 * time.Second)
	return h
}

// everyKindCorpus is the events of the full scenario and of the recorded demo sessions, decoded.
func everyKindCorpus(tb testing.TB) []map[string]any {
	tb.Helper()
	out := fullScenario(tb).decoded()
	out = append(out, translateLog(tb, shopEvents(tb), "/work/shop", false).decoded()...)
	out = append(out, translateLog(tb, shopEvents(tb), "/work/shop", true).decoded()...)
	out = append(out, translateLog(tb, statetest.DemoEvents(), "/work/handbook", true).decoded()...)
	return out
}

// The full scenario keeps the journal's invariants and produces each source's events.
func TestFullScenario(t *testing.T) {
	h := fullScenario(t)
	raws := h.raws()
	checkStream(t, raws)
	golden(t, "scenario.ui.jsonl", lines(raws))
}
