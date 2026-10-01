package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// fold applies the events to a new State and fails the test if any handler panicked.
func fold(t testing.TB, evs ...events.Event) *State {
	t.Helper()
	st := New()
	apply(t, st, evs...)
	return st
}

func apply(t testing.TB, st *State, evs ...events.Event) {
	t.Helper()
	for _, e := range evs {
		st.Apply(e)
	}
	if p := st.Stats().Panics; p != 0 {
		t.Fatalf("a handler panicked %d time(s): %s", p, st.Stats().LastPanic)
	}
}

// agentOf returns the agent of the snapshot, failing the test if there is none.
func agentOf(t testing.TB, sn *Snapshot, id string) Agent {
	t.Helper()
	a, ok := sn.Agent(id)
	if !ok {
		t.Fatalf("no agent %q in %v", id, agentIDs(sn))
	}
	return a
}

func agentIDs(sn *Snapshot) []string {
	var ids []string
	for _, a := range sn.Agents {
		ids = append(ids, a.ID)
	}
	return ids
}

// js renders a value as JSON for a failure message.
func js(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

// near reports whether two floats are equal to a relative 1e-9 (they are sums of products, in an order the test repeats).
func near(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	m := a
	if m < 0 {
		m = -m
	}
	return d <= 1e-9*(1+m)
}

func sec(n int) time.Duration { return time.Duration(n) * time.Second }

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// newB is a builder with the session already started on a model with a price the State can use.
func newB() *statetest.Builder { return statetest.NewBuilder() }

// startPayload is a session.start for a swarm on "m" priced at 4/0.4 dollars per million input/cache read, with a five minute TTL.
func startPayload(extra map[string]any) map[string]any {
	m := map[string]any{
		"version": "1.2.3", "model": "m", "provider": "prov", "dialect": "openai-chat", "swarm": true,
		"root": "/work/proj", "cwd": "/work/proj/sub", "renderer": "sleipnir-kv/2", "recon_tokens": 25, "shared_hash": "0123456789abcdef0123",
		"models": map[string]any{"m": map[string]any{"source": "given", "context": 1000000, "input_per_m": 4.0, "output_per_m": 20.0,
			"cache_read_per_m": 0.4, "cache_write_5m_per_m": 5.0, "cache_write_1h_per_m": 8.0, "ttl_s": 300}},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func secShared() statetest.Sec { return statetest.Sec{Name: "shared", Tokens: 100} }

func secRole() statetest.Sec { return statetest.Sec{Name: "role", Tokens: 50} }

// statetestSide is the payload of a compactor's model.request.
func statetestSide(agent, req string) map[string]any {
	return map[string]any{"req": req, "agent": agent, "role": "compactor", "kind": "compactor", "model": "m", "provider": "test"}
}

// taskOp is the payload of a board.op that carries the whole state of a task, as internal/swarm/board.go writes it.
func taskOp(op, id, status, owner string, rev uint64, extra map[string]any) map[string]any {
	m := map[string]any{"op": op, "version": rev, "task": id, "status": status, "owner": owner, "line": "", "result": "", "evidence": "",
		"attempts": 0, "rev": rev, "files": []string(nil)}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func rowOf(t testing.TB, sn *Snapshot, id string) ActivityRow {
	t.Helper()
	for _, r := range sn.Activity.Rows {
		if r.Agent == id {
			return r
		}
	}
	t.Fatalf("no activity row for %q", id)
	return ActivityRow{}
}

// anyMark reports whether any cell of the lane carries the marker bit.
func anyMark(r ActivityRow, bit uint8) bool {
	for _, m := range r.Marks {
		if m&bit != 0 {
			return true
		}
	}
	return false
}

// feedHas reports whether the feed has a line of the kind, for the agent, whose text or detail contains frag.
func feedHas(sn *Snapshot, kind FeedKind, agent, frag string) bool {
	for _, l := range sn.Feed {
		if l.Kind == kind && (agent == "" || l.Agent == agent) && (strings.Contains(l.Text, frag) || strings.Contains(l.Detail, frag)) {
			return true
		}
	}
	return false
}
