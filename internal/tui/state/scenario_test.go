package state

import (
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// handBuiltSession is a small swarm session written by hand, second by second, that touches every part of the State: a manager and
// three workers on one prefix and two, tasks through every column of the board, a tool of every status, mail and its digest, leases,
// a cache anomaly and a compaction at a cold moment, an epoch, a stuck worker, a rate limit, a merge queue with a conflict and a
// verification failure, permission questions (answered, refused by policy, cancelled with the run), and the end of the session. Every time in it is written down, so a test can say what
// it expects at each of them.
func handBuiltSession() []events.Event {
	b := statetest.NewBuilder()
	S := statetest.Epoch
	at := func(d time.Duration) *statetest.Builder { return b.At(S.Add(d)) }
	e := func(agent, typ string, data any) { b.Emit(agent, typ, data) }
	msec := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	secs := func(n int) time.Duration { return time.Duration(n) * time.Second }

	e("", events.TypeLogOpen, map[string]any{"schema": 1})
	at(msec(5)).Emit("", events.TypeSessionStart, startPayload(map[string]any{"isolation": "worktree", "mailman": true}))
	at(msec(10)).Spawn("mgr", "manager", "", "")
	at(msec(20)).Emit("mgr", events.TypeUserInput, map[string]any{"text": "add pagination to every list endpoint"})

	// The manager plans: one request on a cold prefix (written, not read), three tasks, three workers, and waits.
	secM := []statetest.Sec{{Name: "shared", Tokens: 120}, {Name: "role", Tokens: 60, BP: true}, {Name: "notes", Tokens: 20}}
	at(msec(30)).Request("mgr", "mgr.1", "m", "prefix-mgr-0001", secM...)
	at(msec(1500)).Emit("mgr", events.TypeModelResponse, withTotal(statetest.ResponsePayload("mgr.1", "m", 400, 0, 4600, 80, 0.0123), 1400))
	for i, task := range []struct{ id, title, role, files string }{{"T1", "orders endpoint", "backend", "orders/**"}, {"T2", "orders table", "frontend", "web/orders/**"}, {"T3", "survey the api", "scout", ""}} {
		at(msec(1600+10*i)).Call("mgr", "mc"+task.id, "task", map[string]any{"action": "create", "title": task.title})
		var files []string
		if task.files != "" {
			files = []string{task.files}
		}
		at(msec(1605+10*i)).Emit("mgr", events.TypeBoardOp, taskOp("create", task.id, "todo", "", uint64(i+1), map[string]any{"title": task.title, "role": task.role, "files": files, "deps": []string{}}))
		at(msec(1608+10*i)).Result("mgr", "mc"+task.id, "task", false, 8)
	}
	for i, w := range []struct{ id, role, task string }{{"be-1", "backend", "T1"}, {"fe-1", "frontend", "T2"}, {"sc-1", "scout", "T3"}} {
		at(secs(2)+msec(10*i)).Emit("mgr", events.TypeBoardOp, taskOp("assign", w.task, "doing", w.id, uint64(10+i), map[string]any{"files": map[string][]string{"T1": {"orders/**"}, "T2": {"web/orders/**"}, "T3": nil}[w.task]}))
		at(secs(2)+msec(10*i+5)).Spawn(w.id, w.role, w.task, "mgr")
		at(secs(2)+msec(10*i+6)).Emit(w.id, events.TypeAgentState, map[string]any{"id": w.id, "state": "running", "line": "starting", "task": w.task})
	}
	at(secs(2)+msec(100)).Call("mgr", "mw", "wait", map[string]any{"until": []string{"T1", "T2", "T3"}, "timeout_sec": 90})

	// The workers ride the shared prefix: their first requests read what the manager wrote.
	shared := statetest.Sec{Name: "shared", Tokens: 120}
	for i, w := range []struct{ id, prefix, role string }{{"be-1", "prefix-be-0002", "backend"}, {"fe-1", "prefix-fe-0003", "frontend"}, {"sc-1", "prefix-sc-0004", "scout"}} {
		at(secs(3)+msec(50*i)).Request(w.id, w.id+".1", "m", w.prefix, shared, statetest.Sec{Name: "role", Tokens: 70 + 5*i, BP: true, Hash: "role-" + w.role})
		at(secs(4)+msec(400*i)).Emit(w.id, events.TypeModelResponse, withTotal(statetest.ResponsePayload(w.id+".1", "m", 300+10*i, 4400, 0, 40, 0.004), 900))
	}
	// be-1 edits a file under a lease; fe-1 runs a command; sc-1 gets stuck on a tool that does not exist.
	at(secs(5)).Call("be-1", "b1", "edit", map[string]any{"path": "/work/proj/orders/list.go"})
	at(secs(5)+msec(5)).Emit("be-1", events.TypeLease, map[string]any{"action": "acquire", "agent": "be-1", "path": "/work/proj/orders/list.go"})
	at(secs(5)+msec(300)).Result("be-1", "b1", "edit", false, 295)
	at(secs(5)).Call("fe-1", "f1", "bash", map[string]any{"command": "npm test -- orders"})
	for i := 0; i < 4; i++ {
		id := "s" + string(rune('a'+i))
		at(secs(5)+msec(100*i)).Call("sc-1", id, "nosuch", map[string]any{})
		at(secs(5)+msec(100*i+20)).Result("sc-1", id, "nosuch", true, 20)
	}
	at(secs(5)+msec(450)).Emit("sc-1", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "[harness] nosuch has now failed the same way 4 times"})
	at(secs(6)).Emit("fe-1", events.TypeLease, map[string]any{"action": "conflict", "agent": "fe-1", "path": "/work/proj/orders/list.go", "holder": "be-1"})
	at(secs(6)+msec(500)).Result("fe-1", "f1", "bash", false, 1500)

	// Mail: fe-1 tells be-1 the schema it needs, through the mailman, who digests it.
	at(secs(7)).Emit("fe-1", events.TypeMailSend, map[string]any{"id": "m1", "from": "fe-1", "to": "be-1", "kind": "request", "text": "schema: GET /orders returns {items,next}"})
	at(secs(7)+msec(1)).Emit("fe-1", events.TypeMailRoute, map[string]any{"id": "m1", "from": "fe-1", "to": "be-1", "kind": "request"})
	at(secs(8)).Emit("swarm", events.TypeMailBatch, map[string]any{"batch": "b1", "mailman": "mm-1", "recipients": 1, "parcels": 1})
	at(secs(9)).Emit("mm-1", events.TypeMailSend, map[string]any{"id": "m2", "from": "mm-1", "to": "be-1", "kind": "request", "text": "digest: fe-1 needs GET /orders as {items,next}", "via": "mm-1", "origins": []string{"fe-1"}})
	at(secs(9)+msec(1)).Emit("be-1", events.TypeMailDeliver, map[string]any{"id": "m2", "from": "mm-1", "via": "mm-1"})
	at(secs(9)+msec(2)).Emit("mm-1", events.TypeMailDigest, map[string]any{"id": "m2", "to": "be-1", "mailman": "mm-1", "parcels": []string{"m1"}, "senders": []string{"fe-1"}, "kind": "request", "frame": "[mail m2 ...]"})

	// be-1's second request misses the cache: the harness says which layer diverged; a compaction follows at a cold moment.
	at(secs(10)).Request("be-1", "be-1.2", "m", "prefix-be-0002", shared, statetest.Sec{Name: "role", Tokens: 70, BP: true, Hash: "role-backend"})
	at(secs(10)+msec(10)).Emit("be-1", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "req": "be-1.2", "expected_read": 4400, "actual_read": 200, "missed": 4200, "diverged": "notes"})
	miss := withTotal(statetest.ResponsePayload("be-1.2", "m", 4500, 200, 0, 60, 0.02), 1200)
	miss["expected_read"], miss["missed"], miss["anomaly"] = 4400, 4200, true
	at(secs(11)).Emit("be-1", events.TypeModelResponse, miss)
	at(secs(12)).Emit("be-1", events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "fork", "reason": "thread is large", "warm": false, "thread_tokens": 31200})
	at(secs(13)).Emit("be-1", events.TypeCompactPlan, map[string]any{"decision": "commit?", "yes": true, "net_ite": 9000, "reason": "cold", "warm": false, "thread_tokens": 31200})
	at(secs(13)+msec(10)).Emit("be-1", events.TypeCompactCommit, map[string]any{"reason": "cold", "removed_turns": 12, "removed_tokens": 28800, "retained_tokens": 2000, "snap_tokens": 31200, "spine_added": 400, "held_ms": 900})
	at(secs(13)+msec(11)).Emit("be-1", events.TypeLayerCommit, map[string]any{"scope": "agent", "spine": "s1", "notes": "n1"})

	// A rate limit: one request is retried; the governor slows the swarm.
	at(secs(14)).Request("fe-1", "fe-1.2", "m", "prefix-fe-0003", shared, statetest.Sec{Name: "role", Tokens: 75, BP: true, Hash: "role-frontend"})
	at(secs(14)+msec(100)).Emit("fe-1", events.TypeModelError, map[string]any{"req": "fe-1.2", "kind": "rate_limit", "status": 429, "attempt": 1, "delay_ms": 800})
	at(secs(14)+msec(105)).Emit("swarm", events.TypeGovernor, map[string]any{"action": "rate-limited", "rate_per_min": 384.0, "pause_ms": 1000, "retry_after_ms": 1000, "inflight": 4, "queued": 2})
	at(secs(15)+msec(500)).Emit("fe-1", events.TypeModelResponse, withTotal(statetest.ResponsePayload("fe-1.2", "m", 200, 4700, 0, 30, 0.003), 600))

	// A shared epoch: every agent will re-write the prefix; be-1 syncs first.
	at(secs(16)).Emit("swarm", events.TypeLayerCommit, map[string]any{"scope": "shared-epoch", "reason": "phase boundary", "hash": "abc123def456"})
	at(secs(16)+msec(50)).Emit("be-1", events.TypeLayerCommit, map[string]any{"scope": "shared-sync", "reason": "epoch (registration)", "shared": "x", "role": "y"})

	// Work finishes; the merge queue integrates be-1's tree and bounces fe-1's.
	at(secs(17)).PermAsk("be-1", "backend", "bash", "go test ./orders/...", "not on the read-only list")
	at(secs(17)+msec(800)).PermDecide("be-1", "backend", "bash", "go test ./orders/...", "allowed by user for the session", true, "user", "session")
	at(secs(18)).Emit("be-1", events.TypeWorkspaceCreate, map[string]any{"path": "/cache/w/be-1", "branch": "sleipnir/s/be-1", "base": "abc", "mode": "worktree"})
	at(secs(18)+msec(10)).Emit("fe-1", events.TypeWorkspaceCreate, map[string]any{"path": "/cache/w/fe-1", "branch": "sleipnir/s/fe-1", "base": "abc", "mode": "worktree"})
	at(secs(19)).Emit("be-1", events.TypeMergeQueued, map[string]any{"task": "T1: orders endpoint", "position": 1})
	at(secs(19)+msec(5)).Emit("fe-1", events.TypeMergeQueued, map[string]any{"task": "T2: orders table", "position": 2})
	at(secs(20)).Emit("be-1", events.TypeMergeMerged, map[string]any{"task": "T1: orders endpoint", "before": "b0b0", "after": "0123456789abcdef0123456789abcdef01234567", "commits": 1, "files": []string{"orders/list.go"}, "file_count": 1, "verified": true})
	at(secs(20)+msec(1)).Emit("be-1", events.TypeTaskMerge, map[string]any{"task": "T1", "outcome": "merged", "commit": "0123456789abcdef0123456789abcdef01234567", "files": []string{"orders/list.go"}})
	at(secs(20)+msec(2)).Emit("be-1", events.TypeBoardOp, taskOp("finish", "T1", "review", "be-1", 30, map[string]any{"result": "added pagination", "evidence": "edited 1 file; last test passed"}))
	at(secs(20)+msec(3)).Emit("be-1", events.TypeLease, map[string]any{"action": "release", "agent": "be-1"})
	at(secs(20)+msec(4)).Emit("be-1", events.TypeAgentEnd, map[string]any{"id": "be-1", "state": "idle", "evidence": "edited 1 (list.go); last test \"go test ./orders/...\" passed"})
	at(secs(21)).Emit("fe-1", events.TypeMergeConflict, map[string]any{"task": "T2: orders table", "files": []string{"web/orders/table.tsx"}, "hunks": 2, "tip": "t", "theirs": "h"})
	at(secs(21)+msec(1)).Emit("fe-1", events.TypeTaskMerge, map[string]any{"task": "T2", "outcome": "conflict", "files": []string{"web/orders/table.tsx"}})
	at(secs(22)).Emit("mgr", events.TypeBoardOp, taskOp("finish", "T1", "done", "be-1", 31, map[string]any{"result": "added pagination"}))
	at(secs(22)+msec(500)).Emit("sc-1", events.TypeBoardOp, taskOp("finish", "T3", "review", "sc-1", 32, map[string]any{"result": "surveyed"}))
	at(secs(22)+msec(600)).Emit("sc-1", events.TypeAgentEnd, map[string]any{"id": "sc-1", "state": "failed", "evidence": "no files edited"})
	at(secs(23)).Emit("mgr", events.TypeBoardOp, map[string]any{"op": "alert", "kind": "stuck", "text": "sc-1 repeated a failing call", "key": "stuck:sc-1", "version": 40})
	// fe-1, sent back to its conflict, reads a key that no one may read (refused by policy, without a question) and asks to install
	// something; its run is cancelled while the question waits: the engine answers "canceled", and then the run says so.
	at(secs(23)+msec(100)).PermDecide("fe-1", "frontend", "read", "", "a protected path: ~/.ssh", false, "policy", "", "/home/u/.ssh/id_rsa")
	at(secs(23)+msec(200)).PermAsk("fe-1", "frontend", "bash", "npm install", "reaches the network")
	at(secs(23)+msec(700)).PermDecide("fe-1", "frontend", "bash", "npm install", "approval canceled: context canceled", false, "canceled", "")
	at(secs(23)+msec(701)).Cancel("fe-1", "tools", "canceled", 2)
	at(secs(24)).Emit("mgr", events.TypeModelResponse, withTotal(statetest.ResponsePayload("mgr.1", "m", 10, 4900, 0, 20, 0.002), 500))
	at(secs(25)).Emit("mgr", events.TypeAgentState, map[string]any{"id": "mgr", "state": "done", "line": "", "task": ""})
	at(secs(25)+msec(10)).Emit("swarm", events.TypeSwarmIntegration, map[string]any{"branch": "sleipnir/s/integration", "tip": "0123456789abcdef", "applied": true, "files": []string{"orders/list.go"}})
	at(secs(26)).Emit("", events.TypeSessionEnd, map[string]any{"cost_usd": 0.0478, "reason": "completed"})
	return b.Events()
}

// withTotal adds the time the provider call took to a response payload.
func withTotal(m map[string]any, totalMs int) map[string]any {
	m["total_ms"] = totalMs
	return m
}
