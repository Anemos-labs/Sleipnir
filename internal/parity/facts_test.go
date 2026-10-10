package parity

// The facts dimension. A fix to how an event is interpreted (what an agent's state is at the end of a run, what a token total
// counts, which tasks are merged) cannot be seen in a list of what either interface has: the number it shows changes. So the same
// recordings are run through both interfaces and the same facts must come out.
//
// This file is the terminal's half. It folds each recording with the State of internal/tui/state, which every terminal view reads,
// computes the facts below from it and compares them with testdata/facts-<recording>.json. The page's half is
// internal/web/uidev/test/parity-facts.test.mjs: it loads the user-interface stream the translator makes of the same recording
// through the page's reducer (internal/web/ui/js/30-model.js), computes the same facts and compares them with the same file. A
// fact on which the interfaces differ is listed in contract/facts.json as "<recording>/<fact>" with its reason, and both halves
// read that file.
//
// The recordings are the demo's handbook session (statetest.DemoEvents) and shop session (the log behind the translator's golden
// streams), and a hand-built team log that holds what those do not: a goal and its verdicts, permission questions, stalls, a
// handover, a conflict, a failed worker, the harness's mailman, a retry, checkpoints. The team log's page stream is written by
// this test (translate.Replay) and checked in, so that the JavaScript half reads what the translator really makes of it.
//
// The facts, with the definitions both halves keep:
//
//	agents                 agents of the run, the manager included and the harness's own service agents (the mailman) left out
//	manager_state          the manager's state at the end: done, idle, waiting, asking, stuck or working
//	workers_by_state       how many workers are in each of those states (a state nobody is in is left out)
//	tasks                  tasks on the board
//	tasks_by_status        tasks by todo, running, verify, merged and failed (a task the board failed counts as failed only)
//	merged_tasks           the ids of the merged tasks, in order
//	merge_conflicts        submissions to the merge queue that conflicted
//	merge_bounced          submissions sent back to their worker (a conflict, a failed verification, a refusal)
//	tokens_prompt          prompt tokens: uncached input, cache reads and cache writes
//	tokens_cache_read      prompt tokens read from the cache
//	tokens_cache_write     prompt tokens written to the cache
//	tokens_output          output tokens
//	cost_usd               cost as the responses report it
//	savings_usd            what the cache reads saved against uncached input, at list price
//	cache_hit_ratio        cache reads over prompt tokens, weighted by tokens
//	main_responses         answers to main requests, summed over the agents
//	tool_calls             tool calls (not in a stream of a hosted session, whose tool rows come from the agent's sink)
//	tool_errors            tool calls that failed (likewise)
//	permission_denials     permission requests refused (likewise: the refused row comes from the sink)
//	questions_asked        permission questions put to a person
//	checkpoints            checkpoints of the project's files
//	checkpoint_files       files held by them, summed
//	mail_sent              messages sent between agents
//	mail_delivered         messages delivered
//	mail_dropped           messages that were not delivered
//	goal_state             none, active, paused, met or cleared
//	goal_turns             turns the standing goal has used
//	goal_verdict           the kind of the judge's last verdict: continue, done or blocked
//	stalls_open            supervision findings raised and not cleared
//	handovers              phases of tasks changing hands
//	alerts_open            board alerts raised and not cleared
//	compactions            compactions committed
//	cache_breaks           cache anomalies
//	retries                request attempts repeated after a retryable failure
//	rate_limited           retries that were rate limits
//
// The token, cost and cache facts count every response of the log, those of the harness's own service agents (the mailman) included:
// the run spent them, though the agents list leaves those agents out. The page gets them from the translator's svc event.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/web/translate"
)

var updateFacts = flag.Bool("update", false, "rewrite the facts files and the recorded page streams under testdata")

// factTolerance is how far two money or ratio figures may be apart and still be the same fact: the facts are written to six decimals
// (a figure that lies between two of them rounds either way) and the two halves add their floating-point terms in different orders.
const factTolerance = 1.5e-6

// recording is a log the interfaces are compared on.
type recording struct {
	// name is the recording's name in the facts files and in the contract ("handbook" is "handbook/<fact>" there).
	name string
	// events returns the log's events, in order.
	events func(testing.TB) []events.Event
	// stream is the file under testdata that holds the page stream this test writes for the recording; empty for a recording the
	// translator's own golden files cover.
	stream string
}

// recordings are the logs of the dimension.
var recordings = []recording{
	{name: "handbook", events: func(testing.TB) []events.Event { return statetest.DemoEvents() }},
	{name: "shop", events: shopLog},
	{name: "team", events: teamLog, stream: "team.ui.jsonl"},
}

// testdataDir is internal/parity/testdata.
func testdataDir() string { return filepath.Join(filepath.Dir(Dir()), "testdata") }

// factsPath is the facts file of a recording.
func factsPath(name string) string { return filepath.Join(testdataDir(), "facts-"+name+".json") }

// shopLog is the recorded log of the demo's shop scenario that the translator's golden streams are made from.
func shopLog(t testing.TB) []events.Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), "internal/web/translate/testdata/shop.events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	evs, err := statetest.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// ---- the team recording -------------------------------------------------------------------------------------------------------------

// logBuilder makes a log: events with rising seqs and times, one session.
type logBuilder struct {
	seq uint64
	at  time.Time
	evs []events.Event
}

// add appends an event dt after the previous one.
func (b *logBuilder) add(dt time.Duration, agent, typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	b.seq++
	b.at = b.at.Add(dt)
	b.evs = append(b.evs, events.Event{Seq: b.seq, TS: b.at, Session: "team", Agent: agent, Type: typ, Data: raw})
}

// teamLog is a log of a small isolated team with a mailman, hand-built from the shapes the producers write (internal/session,
// internal/swarm, internal/agent, internal/workspace): a goal and its judge, a board of five tasks in every column, a handover, a
// conflicting, a refused and a failing merge, a refused and an asked permission, a retry under a rate limit, checkpoints, a stall and an
// alert, and a run that is not over: one worker failed, one is asking, one is thinking and the manager waits for the team.
func teamLog(testing.TB) []events.Event {
	b := &logBuilder{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	const step = 150 * time.Millisecond
	task := func(op, id, status, owner string, rev uint64, extra map[string]any) map[string]any {
		m := map[string]any{"op": op, "version": rev + 1, "task": id, "status": status, "owner": owner, "line": "", "result": "", "evidence": "",
			"attempts": 1, "rev": rev, "files": []string{"api/" + id + "/**"}, "closure": nil, "blocked_on": "", "verification_failures": 0,
			"title": "task " + id, "role": "backend", "deps": []string{}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	req := func(id string) map[string]any {
		return map[string]any{"req": id, "kind": "main", "model": "m1", "prefix_key": "pk-team",
			"sections":    []map[string]any{{"name": "shared", "tokens": 1000, "hash": "h1"}, {"name": "role", "tokens": 200}, {"name": "notes", "tokens": 50}, {"name": "spine", "tokens": 30}},
			"breakpoints": []map[string]any{{"label": "shared", "ttl": int64(300 * time.Second)}}}
	}
	resp := func(id string, in, read, write, out int, cost float64, stop string) map[string]any {
		return map[string]any{"req": id, "model": "m1", "usage": map[string]any{"input_tokens": in, "cache_read_tokens": read, "cache_write_5m_tokens": write, "output_tokens": out},
			"cost_usd": cost, "total_ms": 400, "stop": stop}
	}
	spawn := func(id, role, taskID string) map[string]any {
		return map[string]any{"id": id, "role": role, "task": taskID, "model": "m1", "by": "mgr", "parent": "mgr"}
	}

	b.add(0, "", "log.open", map[string]any{"schema": 1})
	b.add(step, "", "session.start", map[string]any{"model": "m1", "swarm": true, "root": "/work/team", "cwd": "/work/team", "isolation": "worktree", "mailman": true,
		"models": map[string]any{"m1": map[string]any{"source": "given", "input_per_m": 1.0, "output_per_m": 4.0, "cache_read_per_m": 0.1,
			"cache_write_5m_per_m": 1.25, "cache_write_1h_per_m": 2.0, "ttl_s": 300}}})
	b.add(step, "mgr", "user.input", map[string]any{"text": "Build the API: a catalogue, a cart, handlers, documentation"})
	b.add(step, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Build the API", "Turns": 1, "Max": 20}})
	b.add(step, "swarm", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(step, "mgr", "agent.state", map[string]any{"id": "mgr", "state": "running", "line": "", "task": ""})
	b.add(step, "mgr", "model.request", req("r1"))
	b.add(step, "mgr", "model.response", resp("r1", 800, 0, 1200, 120, 0.0035, "tool_use"))
	for i, id := range []string{"T1", "T2", "T3", "T4", "T5"} {
		extra := map[string]any{}
		if id == "T2" {
			extra["deps"] = []string{"T1"}
		}
		b.add(step, "mgr", "board.op", task("create", id, "todo", "", 0, extra))
		_ = i
	}
	b.add(step, "swarm", "agent.spawn", spawn("be-1", "backend", "T1"))
	b.add(step, "swarm", "agent.spawn", spawn("rv-1", "reviewer", ""))
	b.add(step, "swarm", "agent.spawn", spawn("sc-1", "scout", ""))
	b.add(step, "swarm", "agent.spawn", map[string]any{"id": "mm-1", "role": "mailman", "service": true, "model": "m1"})
	b.add(step, "be-1", "board.op", task("claim", "T1", "doing", "be-1", 1, nil))
	b.add(step, "be-1", "model.request", req("r2"))
	b.add(step, "be-1", "model.response", resp("r2", 300, 1200, 0, 60, 0.0012, "tool_use"))
	b.add(step, "be-1", "tool.call", map[string]any{"id": "w1", "name": "write", "input": map[string]any{"path": "/work/team/api/items.go", "content": "package api\n"}})
	b.add(step, "be-1", "tool.result", map[string]any{"id": "w1", "name": "write", "error": false, "ms": 12})
	b.add(step, "be-1", "tool.call", map[string]any{"id": "b1", "name": "bash", "input": map[string]any{"command": "rm -rf build"}})
	b.add(step, "be-1", "perm.decide", map[string]any{"tool": "bash", "command": "rm -rf build", "role": "backend", "allow": false, "by": "policy", "reason": "a deny rule matches"})
	b.add(step, "be-1", "tool.result", map[string]any{"id": "b1", "name": "bash", "error": true, "refused": true, "ms": 3})
	b.add(step, "rv-1", "model.request", req("r3"))
	b.add(step, "rv-1", "model.response", resp("r3", 200, 900, 0, 30, 0.0006, "tool_use"))
	b.add(step, "rv-1", "perm.ask", map[string]any{"tool": "bash", "command": "npm test", "role": "reviewer", "reason": "runs the tests"})
	b.add(step, "rv-1", "perm.decide", map[string]any{"tool": "bash", "command": "npm test", "role": "reviewer", "allow": true, "by": "user", "remember": "session"})
	b.add(step, "mm-1", "model.request", req("m-1"))
	b.add(step, "mm-1", "model.response", resp("m-1", 200, 0, 0, 40, 0.0004, "end_turn"))
	b.add(step, "be-1", "mail.send", map[string]any{"id": "m1", "from": "be-1", "to": "rv-1", "kind": "info", "text": "catalogue: GET /items?page&size"})
	b.add(step, "rv-1", "mail.deliver", map[string]any{"id": "m1", "from": "be-1"})
	b.add(step, "be-1", "mail.send", map[string]any{"id": "m2", "from": "be-1", "to": "sc-1", "kind": "info", "text": "please map the cart"})
	b.add(step, "sc-1", "mail.drop", map[string]any{"id": "m2", "from": "be-1", "reason": "its run had ended"})
	b.add(step, "", "checkpoint", map[string]any{"id": "cp_0001", "label": "turn 1: build the API", "files": []string{}, "agents": []string{}, "time": b.at.Format(time.RFC3339Nano)})
	b.add(2*time.Second, "", "checkpoint", map[string]any{"id": "cp_0001", "label": "turn 1: build the API", "files": []string{"api/items.go", "api/cart.go"}, "agents": []string{"be-1"}, "time": b.at.Format(time.RFC3339Nano), "added": 40, "removed": 2})
	b.add(step, "", "checkpoint", map[string]any{"id": "cp_0002", "label": "turn 2: handlers", "count": 3, "agents": []string{"be-1"}, "time": b.at.Format(time.RFC3339Nano), "safety": true})
	b.add(step, "", "swarm.stall", map[string]any{"action": "raise", "kind": "claimed_no_progress", "task": "T1", "agent": "be-1", "detail": "be-1 claimed T1 9 minutes ago and has not reported progress"})
	b.add(step, "", "board.op", map[string]any{"op": "alert", "version": 20, "kind": "stalled", "key": "be-1", "text": "be-1 has been quiet for 9 minutes"})
	b.add(step, "", "board.op", map[string]any{"op": "alert", "version": 21, "kind": "idle", "key": "sc-1", "text": "sc-1 is idle"})
	b.add(step, "", "board.op", map[string]any{"op": "alert-clear", "version": 22, "kind": "idle", "key": "sc-1"})
	b.add(step, "be-1", "cache.anomaly", map[string]any{"kind": "low_hit", "expected_read": 4500, "actual_read": 0, "req": "r2"})
	b.add(step, "be-1", "compact.commit", map[string]any{"snap_tokens": 1400, "spine_added": 125, "retained_tokens": 500})
	b.add(step, "be-1", "model.error", map[string]any{"req": "r4", "kind": "rate_limit", "status": 429, "attempt": 1, "delay_ms": 2000})
	b.add(step, "", "governor", map[string]any{"action": "rate-limited", "rate_per_min": 30, "pause_ms": 1000, "inflight": 3, "queued": 2})
	b.add(step, "be-1", "merge.queued", map[string]any{"position": 1, "task": "T1: task T1"})
	b.add(step, "be-1", "merge.merged", map[string]any{"task": "T1: task T1", "after": "abcdef0123456789", "files": []string{"api/items.go"}, "verified": true})
	b.add(step, "be-1", "task.merge", map[string]any{"task": "T1", "outcome": "merged", "commit": "abcdef0123456789"})
	b.add(step, "be-1", "board.op", task("finish", "T1", "review", "be-1", 2, nil))
	b.add(step, "mgr", "board.op", task("finish", "T1", "done", "be-1", 3, map[string]any{"closure": map[string]any{"kind": "verified"}}))
	b.add(step, "be-1", "board.op", task("claim", "T2", "doing", "be-1", 1, map[string]any{"deps": []string{"T1"}}))
	b.add(step, "be-1", "merge.queued", map[string]any{"position": 1, "task": "T2: task T2"})
	b.add(step, "be-1", "merge.verify_failed", map[string]any{"task": "T2: task T2", "cmd": "go test ./api/...", "exit_code": 1})
	b.add(step, "mgr", "swarm.handover", map[string]any{"phase": "begin", "task": "T2", "from": "be-1", "to": "be-2", "by": "mgr"})
	b.add(step, "swarm", "agent.spawn", map[string]any{"id": "be-2", "role": "backend", "task": "T2", "model": "m1", "by": "mgr", "parent": "mgr", "handover_from": "be-1"})
	b.add(step, "mgr", "board.op", task("assign", "T2", "doing", "be-2", 2, map[string]any{"deps": []string{"T1"}}))
	b.add(step, "mgr", "swarm.handover", map[string]any{"phase": "done", "task": "T2", "from": "be-1", "to": "be-2", "by": "mgr", "closure": map[string]any{"kind": "handed_off", "target": "be-2"}})
	b.add(step, "be-1", "agent.end", map[string]any{"id": "be-1", "state": "idle"})
	b.add(step, "be-2", "merge.queued", map[string]any{"position": 1, "task": "T2: task T2"})
	b.add(step, "be-2", "merge.merged", map[string]any{"task": "T2: task T2", "after": "bcdef01234567890", "files": []string{"api/cart.go"}, "verified": true})
	b.add(step, "be-2", "task.merge", map[string]any{"task": "T2", "outcome": "merged", "commit": "bcdef01234567890"})
	b.add(step, "mgr", "board.op", task("finish", "T2", "done", "be-2", 3, map[string]any{"closure": map[string]any{"kind": "verified"}}))
	b.add(step, "be-2", "board.op", task("claim", "T4", "doing", "be-2", 1, nil))
	b.add(step, "be-2", "merge.queued", map[string]any{"position": 1, "task": "T4: task T4"})
	b.add(step, "be-2", "merge.conflict", map[string]any{"task": "T4: task T4", "files": []string{"api/server.go"}, "hunks": 2})
	b.add(step, "be-2", "merge.queued", map[string]any{"position": 1, "task": "T4: task T4"})
	b.add(step, "be-2", "merge.rejected", map[string]any{"task": "T4: task T4", "reason": "it changes a protected file"})
	b.add(step, "mgr", "board.op", task("finish", "T3", "failed", "", 1, map[string]any{"closure": map[string]any{"kind": "superseded", "target": "T2"}}))
	b.add(step, "sc-1", "agent.end", map[string]any{"id": "sc-1", "state": "failed", "evidence": "the repository could not be read"})
	b.add(step, "rv-1", "agent.end", map[string]any{"id": "rv-1", "state": "done", "evidence": "no files edited; no tests run"})
	b.add(step, "swarm", "agent.spawn", spawn("fe-1", "frontend", ""))
	b.add(step, "fe-1", "model.request", req("r5"))
	b.add(step, "be-2", "perm.ask", map[string]any{"tool": "bash", "command": "npm install --save-dev vitest", "role": "backend", "reason": "installs a package"})
	b.add(step, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Build the API", "Turns": 3, "Max": 20}})
	b.add(step, "", "goal.judge", map[string]any{"verdict": "continue", "reason": "T4 and T5 unfinished", "left": []string{"T4 merged", "T5 done"}})
	b.add(step, "mgr", "tool.call", map[string]any{"id": "wt1", "name": "wait", "input": map[string]any{"until": []string{"T4"}}})
	b.add(1500*time.Millisecond, "mgr", "notice", map[string]any{"level": "info", "msg": "the team is working"})
	return b.evs
}

// ---- the terminal's facts ------------------------------------------------------------------------------------------------------------

// round6 writes a figure to six decimals.
func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }

// stateClass is the state word of an agent the facts use: the page's words for the states it draws, which the State's statuses map
// onto (an agent that is starting, thinking, running a tool or editing is working).
func stateClass(s state.Status) string {
	switch s {
	case state.StatusDone:
		return "done"
	case state.StatusIdle:
		return "idle"
	case state.StatusError, state.StatusStuck:
		return "stuck"
	case state.StatusWaiting:
		return "waiting"
	case state.StatusAsking:
		return "asking"
	}
	return "working"
}

// stateFacts folds a log with the State and computes the facts from what the terminal's views read of it.
func stateFacts(evs []events.Event) map[string]any {
	st := state.New()
	st.ApplyAll(evs)
	sn := st.Snapshot()
	f := map[string]any{}

	agents, workers := 0, map[string]int{}
	for _, a := range sn.Agents {
		if a.Service || a.Role == swarm.MailmanRoleName {
			continue
		}
		agents++
		if a.ID == "mgr" || a.ID == "main" {
			f["manager_state"] = stateClass(a.Status)
			continue
		}
		workers[stateClass(a.Status)]++
	}
	f["agents"], f["workers_by_state"] = agents, workers
	if _, ok := f["manager_state"]; !ok {
		f["manager_state"] = "none"
	}

	var merged []string
	for _, id := range st.TaskIDs() {
		if tk, ok := st.Task(id); ok && tk.State == state.TaskMerged {
			merged = append(merged, id)
		}
	}
	c := sn.Board.Counts
	f["tasks"] = len(sn.Board.Tasks)
	f["tasks_by_status"] = map[string]int{"todo": c.Todo, "running": c.Running, "verify": c.Verifying, "merged": c.Merged, "failed": c.Failed}
	f["merged_tasks"] = merged
	f["merge_conflicts"], f["merge_bounced"] = sn.Merge.Counts.Conflicts, sn.Merge.Counts.Bounced

	tot := sn.Totals
	f["tokens_prompt"], f["tokens_cache_read"] = tot.Tokens.Prompt(), tot.Tokens.CacheRead
	f["tokens_cache_write"], f["tokens_output"] = tot.Tokens.CacheWrite, tot.Tokens.Output
	f["cost_usd"], f["savings_usd"], f["cache_hit_ratio"] = round6(tot.CostUSD), round6(tot.Savings.SavedUSD), round6(tot.HitRatio())
	responses := 0
	for _, a := range sn.Agents {
		if !a.Service && a.Role != swarm.MailmanRoleName {
			responses += a.Requests
		}
	}
	f["main_responses"] = responses
	f["tool_calls"], f["tool_errors"] = tot.ToolCalls, tot.ToolErrors
	f["permission_denials"], f["questions_asked"] = sn.Perms.Denied, sn.Perms.Asked

	files := 0
	for _, ck := range sn.Checkpoints {
		files += ck.Files
	}
	f["checkpoints"], f["checkpoint_files"] = len(sn.Checkpoints), files
	f["mail_sent"], f["mail_delivered"], f["mail_dropped"] = sn.Mail.Counts.Sent, sn.Mail.Counts.Delivered, sn.Mail.Counts.Ignored

	goalState, turns, verdict := "none", 0, ""
	if g := sn.Goal; g != nil {
		goalState, turns, verdict = g.State(), g.Turns, g.Verdict
	}
	f["goal_state"], f["goal_turns"], f["goal_verdict"] = goalState, turns, verdict
	f["stalls_open"], f["handovers"], f["alerts_open"] = len(sn.Stalls), len(sn.Handovers), len(sn.Board.Alerts)
	f["compactions"], f["cache_breaks"] = tot.Compactions, tot.Anomalies
	f["retries"], f["rate_limited"] = tot.Retries, tot.RateLimited
	return f
}

// normalize makes a facts map what a JSON file holds: the types json.Unmarshal gives (float64, string, []any, map[string]any).
func normalize(t testing.TB, f map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// sameFact reports whether two values of one fact are the same: numbers within factTolerance, everything else exactly.
func sameFact(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && math.Abs(x-y) <= factTolerance
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, ok := y[k]; !ok || !sameFact(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameFact(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

// compareFacts names every fact on which two sets differ, in order.
func compareFacts(recording string, want, got map[string]any) []string {
	var names []string
	seen := map[string]bool{}
	for k := range want {
		seen[k] = true
	}
	for k := range got {
		seen[k] = true
	}
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []string
	for _, k := range names {
		w, wok := want[k]
		g, gok := got[k]
		switch {
		case !wok:
			out = append(out, fmt.Sprintf("recording %q: the terminal's State computes the fact %q (%s) and %s has no such fact: add it to the page's computation, then regenerate the file", recording, k, show(g), factsFile(recording)))
		case !gok:
			out = append(out, fmt.Sprintf("recording %q: %s holds the fact %q (%s) and the terminal's State no longer computes it", recording, factsFile(recording), k, show(w)))
		case !sameFact(w, g):
			out = append(out, fmt.Sprintf("recording %q: fact %q: %s holds %s, the terminal's State now computes %s", recording, k, factsFile(recording), show(w), show(g)))
		}
	}
	return out
}

// show spells a value of a fact in a message.
func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// factsFile is the name of a recording's facts file, for a message.
func factsFile(name string) string { return "testdata/facts-" + name + ".json" }

// encodeFacts is the file's bytes: the facts as indented JSON with sorted keys and a trailing newline.
func encodeFacts(t testing.TB, f map[string]any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// ---- the page stream of the team recording ------------------------------------------------------------------------------------------------

// teamStream is the page stream the translator makes of a log: a Replay of it, one encoded event per line.
func teamStream(t testing.TB, evs []events.Event) []byte {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	for _, e := range evs {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	raws, err := translate.Replay(context.Background(), dir, translate.Config{Tab: "facts", Root: "/work/team"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, r := range raws {
		out.Write(r)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// checkFile compares bytes with a file under testdata, or rewrites it with -update.
func checkFile(t *testing.T, path string, got []byte, how string) {
	t.Helper()
	if *updateFacts {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing %s (create it with: go test ./internal/parity -run %s -update): %v", path, t.Name(), err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s changed: %s\nIf the change is intended, read the diff, then run: go test ./internal/parity -run %s -update", filepath.Base(path), how, t.Name())
	}
}

// ---- the tests ------------------------------------------------------------------------------------------------------------------------------

// The terminal's State computes, from each recording, the facts that testdata/facts-<recording>.json holds. A change to how the State
// reads an event shows up here as the fact that moved; the page's reducer must then produce the same number from the same recording
// (node --test internal/web/uidev/test/parity-facts.test.mjs), or the difference is entered in contract/facts.json.
func TestTerminalFacts(t *testing.T) {
	for _, r := range recordings {
		t.Run(r.name, func(t *testing.T) {
			evs := r.events(t)
			got := normalize(t, stateFacts(evs))
			if *updateFacts {
				if err := os.WriteFile(factsPath(r.name), encodeFacts(t, got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			b, err := os.ReadFile(factsPath(r.name))
			if err != nil {
				t.Fatalf("missing %s (create it with: go test ./internal/parity -run TestTerminalFacts -update): %v", factsFile(r.name), err)
			}
			var want map[string]any
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatalf("%s: %v", factsFile(r.name), err)
			}
			if problems := compareFacts(r.name, want, got); len(problems) > 0 {
				t.Errorf("%s\nIf the change is intended, read it, regenerate with: go test ./internal/parity -run TestTerminalFacts -update\nthen run the page's half: node --test internal/web/uidev/test/parity-facts.test.mjs", strings.Join(problems, "\n"))
			}
		})
	}
}

// The page stream of the team recording, which the JavaScript half reads, is what the translator makes of the log now.
func TestFactsStreamsAreCurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stream holds POSIX paths, as the goldens of internal/web/translate do (scripts/windows-excluded.txt); the other systems compare it")
	}
	for _, r := range recordings {
		if r.stream == "" {
			continue
		}
		checkFile(t, filepath.Join(testdataDir(), r.stream), teamStream(t, r.events(t)), "the translator makes another page stream of the "+r.name+" recording")
	}
}

// Every recording has its facts file and its page stream, and no facts file belongs to a recording that is gone (other tests of the
// package keep their own files in the directory).
func TestFactsFilesMatchTheRecordings(t *testing.T) {
	want := map[string]bool{}
	for _, r := range recordings {
		want["facts-"+r.name+".json"] = true
		if r.stream != "" {
			want[r.stream] = true
		}
	}
	ents, err := os.ReadDir(testdataDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "facts-") && !want[n] {
			t.Errorf("testdata/%s belongs to no recording of facts_test.go: remove it, or add the recording", n)
		}
		delete(want, n)
	}
	for n := range want {
		t.Errorf("testdata/%s is missing (create it with: go test ./internal/parity -run 'TestTerminalFacts|TestFactsStreamsAreCurrent' -update)", n)
	}
}

// factsContract is contract/facts.json.
type factsContract struct{ Differences }

// loadFactsFiles reads the facts file of every recording.
func loadFactsFiles(t testing.TB) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range recordings {
		b, err := os.ReadFile(factsPath(r.name))
		if err != nil {
			t.Fatal(err)
		}
		var f map[string]any
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		out[r.name] = f
	}
	return out
}

// problems of the contract file itself: an entry must name a fact of a recording, say why in words, and sit under a heading that
// means something for facts (the terminal's number is the file's; the page's is the one that differs).
func (c factsContract) problems(files map[string]map[string]any) []string {
	var out []string
	if len(c.WebOnly) > 0 {
		out = append(out, "facts: web_only has no meaning for a fact: the file holds the terminal's number, and the page's number differing from it is a terminal_only or a gap")
	}
	seen := map[string]string{}
	for list, es := range map[string][]Entry{"terminal_only": c.TerminalOnly, "gaps": c.Gaps} {
		for _, e := range es {
			rec, fact, ok := strings.Cut(e.Item, "/")
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("facts: %q under %s is not spelt <recording>/<fact>", e.Item, list))
				continue
			case files[rec] == nil:
				out = append(out, fmt.Sprintf("facts: %q under %s names a recording that does not exist", e.Item, list))
			default:
				if _, ok := files[rec][fact]; !ok {
					out = append(out, fmt.Sprintf("facts: %q under %s names a fact that %s does not hold", e.Item, list, factsFile(rec)))
				}
			}
			if len(strings.TrimSpace(e.Reason)) < minReason {
				out = append(out, fmt.Sprintf("facts: %q under %s needs a reason (at least %d characters)", e.Item, list, minReason))
			}
			if prev, dup := seen[e.Item]; dup {
				out = append(out, fmt.Sprintf("facts: %q is listed under %s and under %s", e.Item, prev, list))
			}
			seen[e.Item] = list
		}
	}
	return out
}

// contract/facts.json is well formed: its entries name facts that exist and give reasons. Whether a listed fact still differs is
// decided by the page's half, which is the side that knows the page's number; the gaps are reported on every run.
func TestFactsContract(t *testing.T) {
	var c factsContract
	if err := Load("facts", &c); err != nil {
		t.Fatal(err)
	}
	for _, p := range c.problems(loadFactsFiles(t)) {
		t.Error(p)
	}
	for _, l := range c.GapLines("facts") {
		t.Log(l)
	}
}

// The comparison names the fact, the recording and both values, finds a fact the file lacks and one the State no longer computes, and
// tolerates the last digit of a figure that two sums reach in different orders.
func TestCompareFactsNamesWhatMoved(t *testing.T) {
	want := map[string]any{"tasks": 4.0, "cost_usd": 0.0914905, "by": map[string]any{"a": 1.0}, "old": 1.0}
	got := map[string]any{"tasks": 3.0, "cost_usd": 0.0914906, "by": map[string]any{"a": 2.0}, "new": 1.0}
	text := strings.Join(compareFacts("shop", want, got), "\n")
	for _, w := range []string{`recording "shop": fact "tasks"`, "holds 4", "computes 3", `fact "by"`, `no such fact`, `"new"`, `no longer computes it`, `"old"`} {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q in:\n%s", w, text)
		}
	}
	if strings.Contains(text, `"cost_usd"`) {
		t.Errorf("a difference of a millionth of a dollar was reported:\n%s", text)
	}
	if p := compareFacts("x", map[string]any{"c": 0.1}, map[string]any{"c": 0.2}); len(p) != 1 {
		t.Errorf("a difference of a tenth was not reported: %v", p)
	}
}

// The contract file's own checks fail for what a developer gets wrong.
func TestFactsContractProblems(t *testing.T) {
	files := map[string]map[string]any{"shop": {"tasks": 1.0}}
	c := factsContract{Differences{
		TerminalOnly: []Entry{{"tasks", "no slash in this item at all"}, {"nobody/tasks", "this recording does not exist anywhere"}, {"shop/nothing", "this fact does not exist in the file"}, {"shop/tasks", "short"}},
		Gaps:         []Entry{{"shop/tasks", "listed under two headings as well"}},
		WebOnly:      []Entry{{"shop/tasks", "meaningless for a fact in every way"}},
	}}
	text := strings.Join(c.problems(files), "\n")
	for _, w := range []string{"web_only has no meaning", "is not spelt <recording>/<fact>", "names a recording that does not exist", "names a fact that", "needs a reason", "is listed under"} {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q in:\n%s", w, text)
		}
	}
}

// The facts file is written the same way every time: sorted keys, indented, one trailing newline.
func TestFactsEncoding(t *testing.T) {
	a := encodeFacts(t, normalize(t, map[string]any{"b": 1, "a": map[string]int{"y": 2, "x": 1}}))
	if string(a) != "{\n  \"a\": {\n    \"x\": 1,\n    \"y\": 2\n  },\n  \"b\": 1\n}\n" {
		t.Errorf("unexpected encoding:\n%s", a)
	}
}
