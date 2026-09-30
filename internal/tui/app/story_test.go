package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

func startPayload() map[string]any {
	return map[string]any{
		"version": "1.2.3", "model": "m", "provider": "prov", "dialect": "openai-chat", "swarm": true,
		"root": "/work/shop", "cwd": "/work/shop", "renderer": "sleipnir-kv/2", "recon_tokens": 25, "shared_hash": "0123456789abcdef0123",
		"models": map[string]any{"m": map[string]any{"source": "given", "context": 200000, "input_per_m": 3.0, "output_per_m": 15.0,
			"cache_read_per_m": 0.3, "cache_write_5m_per_m": 3.75, "cache_write_1h_per_m": 6.0, "ttl_s": 300}},
	}
}

func taskOp(op, id, status, owner string, rev uint64, title string) map[string]any {
	return map[string]any{"op": op, "version": rev, "task": id, "status": status, "owner": owner, "title": title, "line": "", "result": "",
		"evidence": "", "attempts": 1, "rev": rev, "files": []string{"shop/" + id}}
}

// story is a session made by hand with what the views exist to show: a manager and two workers riding one prefix, a hit ratio that
// climbs, breaks on a drifted layer and recovers, a compaction at a cold moment, a message, tasks on the board and a merge.
func story(tb testing.TB) []events.Event {
	tb.Helper()
	b := statetest.NewBuilder()
	b.Emit("", events.TypeSessionStart, startPayload())
	b.Emit("mgr", events.TypeUserInput, map[string]any{"text": "Build the shop: a catalogue, a cart and the checkout", "origin": "user"})
	b.Spawn("mgr", "manager", "", "")
	b.Spawn("w1", "backend", "the catalogue api", "mgr")
	b.Spawn("w2", "frontend", "the cart page", "mgr")
	b.Emit("mgr", events.TypeBoardOp, taskOp("create", "T1", "doing", "w1", 1, "the catalogue api"))
	b.Emit("mgr", events.TypeBoardOp, taskOp("create", "T2", "doing", "w2", 2, "the cart page"))
	b.Emit("mgr", events.TypeBoardOp, taskOp("create", "T3", "todo", "", 3, "the checkout"))
	secs := func() []statetest.Sec {
		return []statetest.Sec{{Name: "shared", Tokens: 900, BP: true}, {Name: "role", Tokens: 300, BP: true}, {Name: "notes", Tokens: 120}, {Name: "spine", Tokens: 60}}
	}
	for i := 1; i <= 9; i++ {
		b.Advance(4 * time.Second)
		req := fmt.Sprintf("mgr.%d", i)
		b.Request("mgr", req, "m", "pk", secs()...)
		read := 4800 + 300*i
		if i == 1 {
			read = 0
		}
		if i == 6 { // a layer drifted: the prefix stopped matching
			b.Emit("mgr", events.TypeCacheAnomaly, map[string]any{"kind": "drift", "diverged": "notes", "shared_blocks": 2})
			read = 1200
		}
		b.Advance(2 * time.Second)
		b.Response("mgr", req, "m", 6000+400*i-read, read, 0, 200, 0.004)
		if i == 7 { // the thread is folded at a moment the cache is cold
			b.Emit("mgr", events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "fork", "reason": "thread is big", "warm": false, "thread_tokens": 31200})
			b.Emit("mgr", events.TypeCompactCommit, map[string]any{"reason": "cold moment", "keep_from": "t9", "removed_turns": 12, "removed_tokens": 28000,
				"retained_tokens": 2000, "snap_tokens": 31200, "spine_added": 400, "masked": 3, "fallback": false, "held_ms": 1234})
		}
	}
	for i := 1; i <= 4; i++ {
		for _, w := range []string{"w1", "w2"} {
			b.Advance(time.Second)
			req := fmt.Sprintf("%s.%d", w, i)
			b.Request(w, req, "m", "pk", secs()...)
			b.Advance(time.Second)
			read := 5000 + 200*i
			if i == 1 {
				read = 1200 // the shared layers were read, the rest written
			}
			b.Response(w, req, "m", 6400-read, read, 0, 150, 0.003)
		}
	}
	b.Emit("w1", events.TypeMailSend, map[string]any{"id": "m1", "from": "w1", "to": "w2", "kind": "info", "text": "schema is ready: GET /orders returns {items, next}"})
	b.Emit("w2", events.TypeMailSend, map[string]any{"id": "m2", "from": "w2", "to": "mgr", "kind": "request", "text": "need the price field in the catalogue"})
	b.Emit("mgr", events.TypeBoardOp, taskOp("finish", "T1", "done", "w1", 4, "the catalogue api"))
	return b.Events()
}

func storyState(tb testing.TB) *state.State {
	tb.Helper()
	st := state.New()
	for _, e := range story(tb) {
		st.Apply(e)
	}
	return st
}

func storySnapshot(tb testing.TB) *state.Snapshot { return storyState(tb).Snapshot() }
