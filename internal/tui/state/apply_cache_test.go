package state

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// pair applies a main request of the agent and its response.
func pair(t testing.TB, b *statetest.Builder, st *State, agent string, n int, in, read, write int) {
	t.Helper()
	req := fmt.Sprintf("%s.%d", agent, n)
	apply(t, st, b.Request(agent, req, "m", "pk", secShared(), secRole()))
	b.Advance(ms(100))
	apply(t, st, b.Response(agent, req, "m", in, read, write, 10, 0.001))
}

func TestPromptStackFromRequestAndResponse(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	secs := []statetest.Sec{{Name: "shared", Tokens: 100, BP: true}, {Name: "role", Tokens: 50, BP: true}, {Name: "notes", Tokens: 30}}
	req := statetest.RequestPayload("mgr", "mgr.1", "m", "PREFIXKEY0123456789", secs...)
	req["breakpoints"] = []map[string]any{
		{"after": map[string]any{"msg": 0, "blk": 0}, "label": "shared", "ttl": int64(time.Hour)},
		{"after": map[string]any{"msg": 0, "blk": 1}, "label": "role", "ttl": int64(time.Hour)},
		{"after": map[string]any{"msg": 3, "blk": 0}, "label": "thread"},
	}
	req["shared_blocks"], req["shared_tokens"] = 12, 5000
	req["hot"] = "hotblobhash0123456789"
	req["manifest"] = map[string]any{"tools": "toolsblob0123456789abcdef", "system": []string{"sys0123456789abcdef", "sys2"}}
	apply(t, st, b.Emit("mgr", events.TypeModelRequest, req))

	a := agentOf(t, st.Snapshot(), "mgr")
	s := a.Stack
	if s.Req != "mgr.1" || s.Model != "m" || s.Provider != "test" || s.Dialect != "openai-chat" || s.Renderer != "sleipnir-kv/2" || s.Answered {
		t.Errorf("identity %+v", s)
	}
	wantSecs := []Section{{Name: "shared", Hash: "shared-hash-", Tokens: 100, Breakpoint: true}, {Name: "role", Hash: "role-hash-01", Tokens: 50, Breakpoint: true}, {Name: "notes", Hash: "notes-hash-0", Tokens: 30}}
	if js(s.Sections) != js(wantSecs) || s.SectionTokens != 180 {
		t.Errorf("sections %s (%d)", js(s.Sections), s.SectionTokens)
	}
	if js(s.Breakpoints) != js([]Breakpoint{{Label: "shared", TTLSeconds: 3600}, {Label: "role", TTLSeconds: 3600}, {Label: "thread"}}) {
		t.Errorf("breakpoints %s", js(s.Breakpoints))
	}
	if s.Tools != 17 || s.ThreadFrom != 1 || s.ThreadTo != 3 || s.PrefixKey != "PREFIXKEY012" || s.CacheKey != "sl:test:PREFIXKEY0123456789" {
		t.Errorf("recipe %+v", s)
	}
	if s.SharedBlocks != 12 || s.SharedTokens != 5000 {
		t.Errorf("shared prefix with the previous request: %d blocks %d tokens", s.SharedBlocks, s.SharedTokens)
	}
	if s.ToolsHash != "toolsblob012" || s.HotHash != "hotblobhash0" || len(s.SystemHashes) != 2 || s.SystemHashes[0] != "sys012345678" {
		t.Errorf("blob references: %q %q %v", s.ToolsHash, s.HotHash, s.SystemHashes)
	}
	if s.Read != 0 || s.Prompt != 0 || s.Unsectioned != 0 {
		t.Errorf("there is no response yet: %+v", s)
	}
	if a.Status != StatusThinking || a.InFlight != 1 || a.Requests != 0 {
		t.Errorf("status %s inflight %d requests %d", a.Status, a.InFlight, a.Requests)
	}

	resp := statetest.ResponsePayload("mgr.1", "m", 200, 6000, 0, 10, 0.05)
	resp["expected_read"], resp["missed"], resp["anomaly"], resp["total_ms"] = 6500, 500, true, 2000
	b.Advance(sec(2))
	apply(t, st, b.Emit("mgr", events.TypeModelResponse, resp))
	a = agentOf(t, st.Snapshot(), "mgr")
	s = a.Stack
	if !s.Answered || s.RespReq != "mgr.1" || s.RespSeq == 0 || s.Prompt != 6200 || s.Read != 6000 || s.Write != 0 || s.Fresh != 200 {
		t.Errorf("response %+v", s)
	}
	if !near(s.Hit, 6000.0/6200) || s.ExpectedRead != 6500 || s.Missed != 500 || !s.Miss || s.ExpectedCold {
		t.Errorf("the expectation: hit %v expected %d missed %d miss %v cold %v", s.Hit, s.ExpectedRead, s.Missed, s.Miss, s.ExpectedCold)
	}
	if s.Unsectioned != 6200-180 {
		t.Errorf("Unsectioned = %d: the prompt less the sections", s.Unsectioned)
	}
	if a.Requests != 1 || a.InFlight != 0 {
		t.Errorf("requests %d inflight %d", a.Requests, a.InFlight)
	}

	// A second request replaces the recipe and keeps what the last response said until its own answer comes.
	apply(t, st, b.Emit("mgr", events.TypeModelRequest, statetest.RequestPayload("mgr", "mgr.2", "m", "PREFIXKEY0123456789", secs[0])))
	s = agentOf(t, st.Snapshot(), "mgr").Stack
	if s.Req != "mgr.2" || s.Answered || s.Read != 6000 || s.RespReq != "mgr.1" || len(s.Sections) != 1 {
		t.Errorf("the next request: %+v", s)
	}
	// An answer to a request that is not the stack's is not shown as the stack's.
	apply(t, st, b.Emit("mgr", events.TypeModelResponse, statetest.ResponsePayload("mgr.9", "m", 10, 10, 0, 1, 0)))
	if s = agentOf(t, st.Snapshot(), "mgr").Stack; s.Answered || s.Unsectioned != 0 {
		t.Errorf("a response to another request must not claim to answer mgr.2: %+v", s)
	}

	// A provider that answers with nothing cached leaves a prompt of nothing but fresh tokens, and a ratio of 0.
	apply(t, st, b.Request("w", "w.1", "m", "pk2", secs[0]))
	apply(t, st, b.Response("w", "w.1", "m", 0, 0, 0, 0, 0))
	if h := agentOf(t, st.Snapshot(), "w").Hits.Ratios; len(h) != 1 || h[0] != 0 {
		t.Errorf("empty usage gives hit ratio %v", h)
	}
}

func TestPrefixGroupsAndInheritance(t *testing.T) {
	b := newB()
	st := New()
	req := func(agent, n, prefix string, secs ...statetest.Sec) {
		t.Helper()
		apply(t, st, b.Request(agent, agent+"."+n, "m", prefix, secs...))
	}
	shared := statetest.Sec{Name: "shared", Tokens: 100}
	roleA := statetest.Sec{Name: "role", Tokens: 50, Hash: "role-A"}
	roleB := statetest.Sec{Name: "role", Tokens: 70, Hash: "role-B"}
	otherShared := statetest.Sec{Name: "shared", Tokens: 100, Hash: "a-different-shared-layer"}

	req("mgr", "1", "prefix-A", shared, roleA)
	req("w1", "1", "prefix-B", shared, roleB)
	req("w2", "1", "prefix-B", shared, roleB)
	req("x1", "1", "prefix-C", otherShared, roleB)
	sn := st.Snapshot()
	inh := func(id string) bool { return agentOf(t, sn, id).Stack.Inherited }
	if inh("mgr") || !inh("w1") || !inh("w2") || inh("x1") {
		t.Errorf("inherited: mgr %v w1 %v w2 %v x1 %v (the first request of an agent that joins a shared layer another agent used inherits it)", inh("mgr"), inh("w1"), inh("w2"), inh("x1"))
	}
	if len(sn.Prefixes) != 3 || sn.Prefixes[0].Key != "prefix-B" || strings.Join(sn.Prefixes[0].Agents, ",") != "w1,w2" || sn.Prefixes[0].Riders != 2 || sn.Prefixes[0].Tokens != 170 {
		t.Errorf("prefixes %s", js(sn.Prefixes))
	}
	if sn.Prefixes[1].Key != "prefix-A" || sn.Prefixes[2].Key != "prefix-C" {
		t.Errorf("the most ridden first, then by key: %s", js(sn.Prefixes))
	}
	if sn.Prefixes[0].SharedHash != "shared-hash-" {
		t.Errorf("shared hash %q", sn.Prefixes[0].SharedHash)
	}
	// Inherited is a fact about the agent's first request and does not flip later.
	req("x1", "2", "prefix-B", shared, roleB)
	sn = st.Snapshot()
	if inh("x1") {
		t.Error("x1 did not inherit on its first request")
	}
	if a := agentOf(t, sn, "x1"); a.Stack.PrefixKey != "prefix-B" {
		t.Errorf("prefix key %q", a.Stack.PrefixKey)
	}
	// An epoch gives the agent a new prefix key: it leaves one group and joins another; an empty group goes.
	req("w1", "2", "prefix-D", shared, roleB)
	req("w2", "2", "prefix-D", shared, roleB)
	req("x1", "3", "prefix-D", shared, roleB)
	sn = st.Snapshot()
	var keys []string
	for _, p := range sn.Prefixes {
		keys = append(keys, fmt.Sprintf("%s:%s", p.Key, strings.Join(p.Agents, "+")))
	}
	if strings.Join(keys, " ") != "prefix-D:w1+w2+x1 prefix-A:mgr" {
		t.Errorf("groups %v: prefix-B and prefix-C have no riders left", keys)
	}
}

func TestHitHistoryIsOneSamplePerMainResponse(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	pair(t, b, st, "a", 1, 1000, 0, 0)
	pair(t, b, st, "a", 2, 100, 900, 0)
	pair(t, b, st, "a", 3, 10, 990, 0)
	apply(t, st, b.Emit("a", events.TypeModelRequest, statetestSide("a", "a.c1")))
	apply(t, st, b.Emit("a", events.TypeModelResponse, map[string]any{"req": "a.c1", "model": "m", "side": true, "stop": "end_turn",
		"usage": map[string]any{"input_tokens": 1, "cache_read_tokens": 99}}))
	// a request that fails has no response, so it is no sample
	apply(t, st, b.Request("a", "a.4", "m", "pk", secShared()))
	apply(t, st, b.Emit("a", events.TypeModelError, map[string]any{"req": "a.4", "error": "boom"}))

	h := agentOf(t, st.Snapshot(), "a").Hits
	want := []float64{0, 0.9, 0.99}
	if h.First != 0 || len(h.Ratios) != 3 || h.Len() != 3 {
		t.Fatalf("history %+v", h)
	}
	for i, w := range want {
		if !near(h.Ratios[i], w) {
			t.Errorf("ratio %d = %v, want %v", i, h.Ratios[i], w)
		}
	}
}

func TestHitHistoryIsBoundedAndMarkersStayOnTheSameScale(t *testing.T) {
	b := newB()
	st := New()
	n := HistCap + 44
	for i := 1; i <= n; i++ {
		if i == n-2 {
			apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "drift", "diverged": "notes"}))
		}
		pair(t, b, st, "a", i, 10, i, 0)
	}
	h := agentOf(t, st.Snapshot(), "a").Hits
	if len(h.Ratios) != HistCap || h.First != 44 || h.Len() != n {
		t.Fatalf("ring: %d samples, first %d, len %d", len(h.Ratios), h.First, h.Len())
	}
	// The last sample is the last request: read i of prompt 10+i.
	if want := float64(n) / float64(10+n); !near(h.Ratios[len(h.Ratios)-1], want) {
		t.Errorf("last ratio %v, want %v", h.Ratios[len(h.Ratios)-1], want)
	}
	if len(h.Marks) != 1 || h.Marks[0].At != n-3 || h.Marks[0].Kind != MarkAnomaly {
		t.Errorf("marks %+v: the marker precedes request index %d", h.Marks, n-3)
	}
	if at := h.Marks[0].At - h.First; at < 0 || at >= len(h.Ratios) {
		t.Errorf("a marker is drawn at At-First = %d, inside the ring", at)
	}
	if got := (&Snapshot{Agents: []Agent{{ID: "a", Hits: h}}}).CacheHistory("a"); got.Len() != n {
		t.Errorf("CacheHistory = %+v", got)
	}
	if got := (&Snapshot{}).CacheHistory("a"); got.Len() != 0 {
		t.Errorf("no such agent: %+v", got)
	}
}

func TestMarkersPrecedeTheRequestTheyAffect(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	pair(t, b, st, "a", 1, 1000, 0, 0)
	apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "drift", "diverged": "notes", "shared_blocks": 3}))
	pair(t, b, st, "a", 2, 100, 900, 0)
	apply(t, st, b.Emit("a", events.TypeCompactCommit, map[string]any{"reason": "emergency: prompt over 85% of the context window", "snap_tokens": 9000, "retained_tokens": 500, "spine_added": 200, "removed_turns": 4}))
	apply(t, st, b.Emit("a", events.TypeLayerCommit, map[string]any{"scope": "agent", "spine": "abc", "notes": "def"}))
	apply(t, st, b.Emit("a", events.TypeLayerCommit, map[string]any{"scope": "shared-sync", "reason": "epoch", "shared": "x", "role": "y"}))
	apply(t, st, b.Emit("a", events.TypeLayerCommit, map[string]any{"scope": "thinking-strip", "reason": "provider rejected a thinking block"}))
	apply(t, st, b.Request("a", "a.3", "m", "pk", secShared()))
	apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "req": "a.3", "expected_read": 3000, "actual_read": 100, "missed": 2900, "diverged": "", "first_request": false}))
	apply(t, st, b.Response("a", "a.3", "m", 2900, 100, 0, 10, 0.01))

	a := agentOf(t, st.Snapshot(), "a")
	var got []string
	for _, m := range a.Hits.Marks {
		got = append(got, fmt.Sprintf("%d:%s", m.At, m.Kind))
	}
	if strings.Join(got, " ") != "1:anomaly 2:compaction 2:epoch 2:rebase 2:anomaly" {
		t.Errorf("markers %v", got)
	}
	if a.Hits.Marks[0].Text != "drift notes" {
		t.Errorf("marker text %q", a.Hits.Marks[0].Text)
	}
	if a.Stack.Epochs != 1 || a.Stack.LastCommit != "thinking-strip: provider rejected a thinking block" {
		t.Errorf("epochs %d last commit %q", a.Stack.Epochs, a.Stack.LastCommit)
	}
	if a.Compactions != 1 || len(a.Compacts) != 1 || len(a.Anomalies) != 2 {
		t.Errorf("compactions %d/%d anomalies %d", a.Compactions, len(a.Compacts), len(a.Anomalies))
	}
	sn := st.Snapshot()
	if sn.Totals.Anomalies != 2 || sn.Totals.Compactions != 1 || len(sn.Anomalies) != 2 {
		t.Errorf("totals %+v, session anomalies %d", sn.Totals, len(sn.Anomalies))
	}
	if !anyMark(rowOf(t, sn, "a"), ActCompact) || !anyMark(rowOf(t, sn, "a"), ActAnomaly) {
		t.Error("the gantt lane has no compaction or anomaly marker")
	}
}

func TestAnomalyNamesTheLayerAndPricesTheMiss(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Request("a", "a.1", "m", "pk", secShared()))
	apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "req": "a.1", "expected_read": 12000, "actual_read": 2000, "missed": 10000, "diverged": "spine"}))
	apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "drift", "diverged": "tools", "shared_blocks": 2}))
	apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "thinking_binding", "action": "stripped all thinking durably and retried once", "error": "bad block"}))
	apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "req": "a.1", "expected_read": 500, "actual_read": 100}))
	apply(t, st, b.Emit("a", events.TypeCacheAnomaly, map[string]any{}))
	an := agentOf(t, st.Snapshot(), "a").Anomalies
	if len(an) != 5 {
		t.Fatalf("%d anomalies", len(an))
	}
	low := an[0]
	if low.Kind != "low_hit" || low.Layer != "spine" || low.Expected != 12000 || low.Actual != 2000 || low.Missed != 10000 || !low.MissKnown || low.Req != "a.1" {
		t.Errorf("low_hit %+v", low)
	}
	// 10000 tokens that should have cost 0.4 a million cost 4: the difference is 3.6 a million.
	if !near(low.MissUSD, 10000*3.6/1e6) {
		t.Errorf("MissUSD = %v", low.MissUSD)
	}
	if an[1].Kind != "drift" || an[1].Layer != "tools" || an[1].MissKnown {
		t.Errorf("drift %+v", an[1])
	}
	if an[2].Note != "stripped all thinking durably and retried once" {
		t.Errorf("note %q", an[2].Note)
	}
	if an[3].Missed != 400 {
		t.Errorf("a low_hit without a missed count derives it: %d", an[3].Missed)
	}
	if an[4].Kind != "unknown" {
		t.Errorf("an anomaly without a kind is unknown: %q", an[4].Kind)
	}
	sn := st.Snapshot()
	if !feedHas(sn, FeedCache, "a", "cache break: expected ~12k read, got 2.0k (layer spine)") || !feedHas(sn, FeedCache, "a", "the miss cost $0.04") {
		t.Errorf("feed %s", js(sn.Feed))
	}
	if !feedHas(sn, FeedCache, "a", "changed without a declared rebase (layer tools)") {
		t.Errorf("no drift line: %s", js(sn.Feed))
	}

	// With no price for the model the miss is not priced, and says so.
	st2 := New()
	apply(t, st2, b.Emit("", events.TypeSessionStart, map[string]any{"model": "mystery"}))
	apply(t, st2, b.Request("a", "a.1", "mystery", "pk", secShared()))
	apply(t, st2, b.Emit("a", events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "expected_read": 12000, "actual_read": 2000, "missed": 10000}))
	if m := agentOf(t, st2.Snapshot(), "a").Anomalies[0]; m.MissKnown || m.MissUSD != 0 {
		t.Errorf("an unknown price is not guessed: %+v", m)
	}
}

func TestCompactionCommitsSayWhenAndHowMuch(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Request("a", "a.1", "m", "pk", secShared()))
	apply(t, st, b.Response("a", "a.1", "m", 10, 0, 0, 1, 0))

	// A fork compaction at a cold moment: the planner decides to start, the patch is ready, the commit is planned, and lands.
	apply(t, st, b.Emit("a", events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "fork", "reason": "thread is big", "warm": false, "thread_tokens": 31200}))
	if !agentOf(t, st.Snapshot(), "a").Compacting {
		t.Error("a compaction has been decided on: the agent is compacting")
	}
	apply(t, st, b.Emit("a", events.TypeCompactPatch, map[string]any{"stage": "request", "reason": "thread is big"}))
	apply(t, st, b.Emit("a", events.TypeCompactPatch, map[string]any{"stage": "ready", "removed_tokens": 28000, "retained_tokens": 2000}))
	apply(t, st, b.Emit("a", events.TypeCompactPlan, map[string]any{"decision": "commit?", "yes": true, "net_ite": 9000.5, "reason": "cold", "warm": false, "thread_tokens": 31200}))
	apply(t, st, b.Emit("a", events.TypeCompactCommit, map[string]any{"reason": "cold moment", "keep_from": "t9", "removed_turns": 12, "removed_tokens": 28000,
		"retained_tokens": 2000, "snap_tokens": 31200, "spine_added": 400, "masked": 3, "fallback": false, "held_ms": 1234}))
	// One at a warm moment, by masking.
	apply(t, st, b.Emit("a", events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "mask", "reason": "bulky results", "warm": true}))
	apply(t, st, b.Emit("a", events.TypeCompactCommit, map[string]any{"reason": "mask: bulky results", "removed_tokens": 6000, "retained_tokens": 1000, "snap_tokens": 7000, "spine_added": 0, "masked": 5, "fallback": true}))
	// An emergency one has no plan at all.
	apply(t, st, b.Emit("a", events.TypeCompactCommit, map[string]any{"reason": "emergency: prompt over 85% of the context window", "removed_tokens": 100, "retained_tokens": 50, "spine_added": 10}))

	sn := st.Snapshot()
	a := agentOf(t, sn, "a")
	if a.Compacting || a.Compactions != 3 || len(a.Compacts) != 3 {
		t.Fatalf("compacting %v count %d/%d", a.Compacting, a.Compactions, len(a.Compacts))
	}
	c0, c1, c2 := a.Compacts[0], a.Compacts[1], a.Compacts[2]
	if c0.Mode != "fork" || c0.Moment != "cold" || c0.Before != 31200 || c0.After != 2400 || c0.Removed != 28000 || c0.Retained != 2000 || c0.SpineAdded != 400 ||
		c0.RemovedTurn != 12 || c0.Masked != 3 || c0.HeldMs != 1234 || c0.Fallback || c0.Reason != "cold moment" || c0.At != 1 || c0.Agent != "a" {
		t.Errorf("fork: %+v", c0)
	}
	if c1.Mode != "mask" || c1.Moment != "warm" || c1.Reason != "bulky results" || !c1.Fallback || c1.Before != 7000 || c1.After != 1000 {
		t.Errorf("mask: %+v", c1)
	}
	if c2.Mode != "emergency" || c2.Moment != "unknown" || c2.Reason != "prompt over 85% of the context window" {
		t.Errorf("emergency: %+v", c2)
	}
	if c2.Before != 150 { // no snap_tokens: what was removed plus what was kept
		t.Errorf("emergency before = %d", c2.Before)
	}
	if !feedHas(sn, FeedCompact, "a", "compacted 31k → 2.4k") || !feedHas(sn, FeedCompact, "a", "cold moment") || !feedHas(sn, FeedCompact, "a", "warm moment") {
		t.Errorf("feed %s", js(sn.Feed))
	}

	// A rejected patch ends the attempt; a rejected model patch with a fallback does not.
	apply(t, st, b.Emit("a", events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "fork", "warm": true}))
	apply(t, st, b.Emit("a", events.TypeCompactReject, map[string]any{"stage": "model_patch", "reason": "bad json", "fallback": "mechanical"}))
	if !agentOf(t, st.Snapshot(), "a").Compacting {
		t.Error("with a fallback the compaction goes on")
	}
	apply(t, st, b.Emit("a", events.TypeCompactReject, map[string]any{"stage": "stale", "reason": "the thread outgrew the patch"}))
	if agentOf(t, st.Snapshot(), "a").Compacting {
		t.Error("a rejection ends the attempt")
	}
	sn = st.Snapshot()
	if !feedHas(sn, FeedCompact, "a", "unusable") || !feedHas(sn, FeedCompact, "a", "rejected (stale)") {
		t.Errorf("feed %s", js(sn.Feed))
	}
}

func TestTotalsHitRatioIsWeightedByTokens(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Request("a", "a.1", "m", "pk", secShared()))
	apply(t, st, b.Response("a", "a.1", "m", 100, 900, 0, 5, 0.01)) // a small prompt with a hit ratio of 0.9
	apply(t, st, b.Request("a", "a.2", "m", "pk", secShared()))
	apply(t, st, b.Response("a", "a.2", "m", 9000, 1000, 0, 5, 0.02)) // a big one with 0.1
	tot := st.Snapshot().Totals
	if want := 1900.0 / 11000; !near(tot.HitRatio(), want) {
		t.Errorf("hit ratio %v, want %v (tokens read over tokens sent, not the mean of 0.9 and 0.1)", tot.HitRatio(), want)
	}
	if tot.Tokens.Prompt() != 11000 || !near(tot.CostUSD, 0.03) {
		t.Errorf("totals %+v", tot)
	}
	if (Tokens{}).HitRatio() != 0 {
		t.Error("no prompt, no ratio")
	}
}

func TestSavingsAreCacheReadsTimesTheDifferenceOfPrices(t *testing.T) {
	read := func(b *statetest.Builder, st *State, model string, n int, reads int) {
		req := fmt.Sprintf("a.%d", n)
		apply(t, st, b.Request("a", req, model, "pk", secShared()))
		apply(t, st, b.Response("a", req, model, 100, reads, 0, 5, 0))
	}
	t.Run("priced by session.start", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
		read(b, st, "m", 1, 1000)
		read(b, st, "m", 2, 2500)
		sv := st.Snapshot().Totals.Savings
		want := 3500 * (4.0 - 0.4) / 1e6
		if !near(sv.SavedUSD, want) || sv.PricedReadTokens != 3500 || sv.UnpricedReadTokens != 0 || sv.Assumption != "at list price" || !sv.Known() || !sv.Complete() {
			t.Errorf("savings %+v, want %v", sv, want)
		}
		if a := agentOf(t, st.Snapshot(), "a"); !near(a.SavedUSD, want) {
			t.Errorf("agent saved %v", a.SavedUSD)
		}
		ms := st.Snapshot().Models
		if len(ms) != 1 || ms[0].ID != "m" || ms[0].Source != "session" || !ms[0].Known || ms[0].Price.InputPerM != 4 || ms[0].TTLSeconds != 300 || ms[0].ContextTokens != 1_000_000 {
			t.Errorf("models %s", js(ms))
		}
	})
	t.Run("priced by the built-in table when the log did not record a price", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.Emit("", events.TypeSessionStart, map[string]any{"model": "mock-1"}))
		read(b, st, "mock-1", 1, 10000)
		tm, _ := cost.Defaults().Lookup("mock-1")
		want := 10000 * (tm.Price.InputPerM - tm.Price.CacheReadPerM) / 1e6
		sv := st.Snapshot().Totals.Savings
		if !near(sv.SavedUSD, want) || want <= 0 || !sv.Complete() {
			t.Errorf("savings %+v, want %v", sv, want)
		}
		if ms := st.Snapshot().Models; len(ms) != 1 || ms[0].Source != "table" || !ms[0].Known || ms[0].ContextTokens != tm.ContextTokens || tm.ContextTokens == 0 {
			t.Errorf("models %s", js(ms))
		}
	})
	t.Run("a gateway spelling of a table model is priced", func(t *testing.T) {
		b := newB()
		st := New()
		read(b, st, "anthropic/claude-sonnet-4-6-20250101", 1, 1000000)
		sv := st.Snapshot().Totals.Savings
		if want := 1 * (3.0 - 0.30); !near(sv.SavedUSD, want) {
			t.Errorf("saved %v, want %v (claude-sonnet-4-6: input 3, read 0.30 a million)", sv.SavedUSD, want)
		}
	})
	t.Run("a recorded price wins over the table, under another spelling too", func(t *testing.T) {
		b := newB()
		st := New()
		models := map[string]any{"claude-sonnet-4-6": map[string]any{"source": "catalogue", "input_per_m": 6.0, "output_per_m": 30.0, "cache_read_per_m": 0.6, "cache_write_5m_per_m": 7.5, "cache_write_1h_per_m": 12.0}}
		apply(t, st, b.Emit("", events.TypeSessionStart, map[string]any{"model": "claude-sonnet-4-6", "models": models}))
		read(b, st, "anthropic/claude-sonnet-4-6", 1, 1000000)
		if sv := st.Snapshot().Totals.Savings; !near(sv.SavedUSD, 5.4) {
			t.Errorf("saved %v, want 5.4 (the run's own price, 6 less 0.6)", sv.SavedUSD)
		}
	})
	t.Run("an unknown price is an unknown saving, flagged and not guessed", func(t *testing.T) {
		b := newB()
		st := New()
		read(b, st, "mystery-model-9", 1, 5000)
		sv := st.Snapshot().Totals.Savings
		if sv.SavedUSD != 0 || sv.PricedReadTokens != 0 || sv.UnpricedReadTokens != 5000 || sv.Known() || sv.Complete() {
			t.Errorf("savings %+v", sv)
		}
		if ms := st.Snapshot().Models; len(ms) != 1 || ms[0].Known || ms[0].Source != "unknown" {
			t.Errorf("models %s", js(ms))
		}
		// A model that reads nothing saves nothing and leaves nothing unknown.
		read(b, st, "mystery-model-9", 2, 0)
		if sv := st.Snapshot().Totals.Savings; sv.UnpricedReadTokens != 5000 {
			t.Errorf("savings %+v", sv)
		}
		// With both, the figure is a lower bound: known, but not complete.
		apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
		read(b, st, "m", 3, 1000)
		sv = st.Snapshot().Totals.Savings
		if !sv.Known() || sv.Complete() || sv.UnpricedReadTokens != 5000 || sv.PricedReadTokens != 1000 {
			t.Errorf("savings %+v", sv)
		}
	})
	t.Run("the harness's fallback guess is not a price", func(t *testing.T) {
		b := newB()
		st := New()
		models := map[string]any{"m": map[string]any{"source": "fallback", "input_per_m": 3.0, "output_per_m": 15.0, "cache_read_per_m": 0.3}}
		apply(t, st, b.Emit("", events.TypeSessionStart, map[string]any{"model": "m", "models": models}))
		read(b, st, "m", 1, 1000)
		if sv := st.Snapshot().Totals.Savings; sv.SavedUSD != 0 || sv.UnpricedReadTokens != 1000 {
			t.Errorf("savings %+v", sv)
		}
	})
	t.Run("prices that are not sane numbers are not used", func(t *testing.T) {
		b := newB()
		st := New()
		models := map[string]any{"m": map[string]any{"source": "given", "input_per_m": -4.0, "cache_read_per_m": 0.4}, "n": map[string]any{"input_per_m": 4.0, "cache_read_per_m": 1e9}}
		apply(t, st, b.Emit("", events.TypeSessionStart, map[string]any{"model": "m", "models": models}))
		read(b, st, "m", 1, 1000)
		read(b, st, "n", 2, 1000)
		if sv := st.Snapshot().Totals.Savings; sv.SavedUSD != 0 || sv.UnpricedReadTokens != 2000 {
			t.Errorf("savings %+v", sv)
		}
	})
	t.Run("the model the request was made for prices the response", func(t *testing.T) {
		b := newB()
		st := New()
		apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
		apply(t, st, b.Request("a", "a.1", "m", "pk", secShared()))
		apply(t, st, b.Response("a", "a.1", "some-provider-internal-id", 100, 1000000, 0, 5, 0))
		if sv := st.Snapshot().Totals.Savings; !near(sv.SavedUSD, 3.6) {
			t.Errorf("saved %v", sv.SavedUSD)
		}
	})
}

func TestTTLComesFromTheEventsOrIsAFlaggedDefault(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	t0 := b.Now()

	// Explicit-cache request: the breakpoints on the shared and role layers ask for an hour.
	req := statetest.RequestPayload("a", "a.1", "m", "PFX", secShared(), secRole())
	req["breakpoints"] = []map[string]any{{"label": "shared", "ttl": int64(time.Hour)}, {"label": "role", "ttl": int64(time.Hour)}, {"label": "thread"}}
	apply(t, st, b.Emit("a", events.TypeModelRequest, req))
	b.Advance(sec(10))
	resp := statetest.ResponsePayload("a.1", "m", 100, 900, 0, 5, 0)
	resp["total_ms"] = 4000
	apply(t, st, b.Emit("a", events.TypeModelResponse, resp))
	// A request whose model the log recorded a TTL for, and one for a model it did not.
	apply(t, st, b.Request("b", "b.1", "m", "PFX2", secShared()))
	b.Advance(sec(1))
	apply(t, st, b.Response("b", "b.1", "m", 100, 0, 500, 5, 0))
	apply(t, st, b.Request("c", "c.1", "no-ttl-model", "PFX3", secShared()))
	b.Advance(sec(1))
	apply(t, st, b.Response("c", "c.1", "no-ttl-model", 600, 0, 0, 5, 0))

	sn := st.Snapshot()
	byKey := map[string]TTLEntry{}
	for _, e := range sn.TTL {
		byKey[e.Kind+":"+e.Key] = e
	}
	// The response arrived at t0+10s and the call took 4s: the provider refreshed the entry at t0+6s.
	if p := byKey["prefix:PFX"]; p.TTLSeconds != 3600 || p.Default || !p.Last.Equal(t0.Add(sec(6))) || p.How != "read" || strings.Join(p.Agents, ",") != "a" || p.Tokens != 150 {
		t.Errorf("shared prefix with an hour breakpoint: %+v", p)
	}
	// The agent's own prompt rides the thread marker, which has no TTL of its own: the provider's profile (300 s) applies.
	if e := byKey["agent:a"]; e.TTLSeconds != 300 || e.Default || !e.Last.Equal(t0.Add(sec(6))) {
		t.Errorf("agent a: %+v", e)
	}
	if e := byKey["prefix:PFX2"]; e.TTLSeconds != 300 || e.Default || e.How != "write" {
		t.Errorf("a model the session priced: %+v", e)
	}
	if e := byKey["prefix:PFX3"]; e.TTLSeconds != 300 || !e.Default || e.How != "sent" {
		t.Errorf("an unknown model: the default of five minutes, flagged, and the provider reported neither a read nor a write: %+v", e)
	}
	if e := byKey["agent:c"]; !e.Default {
		t.Errorf("agent c: %+v", e)
	}

	// Remaining time, at moments before, inside and after the lifetime.
	e := byKey["prefix:PFX2"]
	for _, c := range []struct {
		now  time.Time
		want time.Duration
		cold bool
	}{{e.Last.Add(-sec(5)), 300 * time.Second, false}, {e.Last, 300 * time.Second, false}, {e.Last.Add(sec(100)), 200 * time.Second, false},
		{e.Last.Add(sec(299)), sec(1), false}, {e.Last.Add(sec(300)), 0, true}, {e.Last.Add(time.Hour), 0, true}} {
		if got := e.Remaining(c.now); got != c.want {
			t.Errorf("Remaining(%v after Last) = %v, want %v", c.now.Sub(e.Last), got, c.want)
		}
	}
	now := e.Last.Add(sec(150))
	var seen int
	for _, s := range st.TTLRemaining(now) {
		if s.Kind == "prefix" && s.Key == "PFX2" {
			seen++
			if s.Remaining != sec(150) || s.Cold || !near(s.Frac, 0.5) {
				t.Errorf("status %+v", s)
			}
		}
		if want := time.Hour - now.Sub(byKey["prefix:PFX"].Last); s.Kind == "prefix" && s.Key == "PFX" && s.Remaining != want {
			t.Errorf("the hour-long prefix has %v left, want %v", s.Remaining, want)
		}
	}
	if seen != 1 {
		t.Error("PFX2 not in TTLRemaining")
	}
	if got := sn.TTLRemaining(now); len(got) != len(st.TTLRemaining(now)) {
		t.Error("Snapshot.TTLRemaining and State.TTLRemaining disagree")
	}
	if cold := st.TTLRemaining(e.Last.Add(time.Hour * 2)); !cold[0].Cold || cold[0].Remaining != 0 || cold[0].Frac != 0 {
		t.Errorf("long after: %+v", cold[0])
	}

	// The default lifetime can be set.
	st2 := NewWith(Options{DefaultTTL: 90 * time.Second})
	apply(t, st2, b.Request("d", "d.1", "whatever", "PFX4", secShared()))
	apply(t, st2, b.Response("d", "d.1", "whatever", 1, 0, 0, 1, 0))
	if e := st2.Snapshot().TTL[0]; e.TTLSeconds != 90 || !e.Default {
		t.Errorf("default TTL: %+v", e)
	}
	// Before any request there is nothing to be cold.
	if got := New().TTLRemaining(t0); len(got) != 0 {
		t.Errorf("TTLRemaining on an empty state: %v", got)
	}
}
