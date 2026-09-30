package inspect

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The oracle for the recorded fixture: decode the raw JSONL generically and sum
// what the model reports, without any of the inspector's code.
func fixtureOracle(t *testing.T) (reqs map[string]int, in, read, write, out int, usd float64, counts map[string]int) {
	t.Helper()
	f, err := os.Open(filepath.Join(fixtureDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reqs, counts = map[string]int{}, map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		counts[e.Type]++
		switch e.Type {
		case "model.request":
			reqs[e.Data["kind"].(string)]++
		case "model.response":
			u := e.Data["usage"].(map[string]any)
			in += int(u["input_tokens"].(float64))
			read += int(u["cache_read_tokens"].(float64))
			write += int(u["cache_write_5m_tokens"].(float64)) + int(u["cache_write_1h_tokens"].(float64))
			out += int(u["output_tokens"].(float64))
			usd += e.Data["cost_usd"].(float64)
		}
	}
	return
}

func TestFixtureMatchesAnIndependentSum(t *testing.T) {
	requireFixture(t)
	reqs, in, read, write, out, usd, counts := fixtureOracle(t)
	s := mustLoad(t, fixtureDir)
	sum := s.Summary()

	tot := sum.Totals
	if tot.Main != reqs["main"] || tot.Side != reqs["compactor"] || tot.Requests != reqs["main"]+reqs["compactor"] {
		t.Errorf("requests main/side/all = %d/%d/%d, want %d/%d", tot.Main, tot.Side, tot.Requests, reqs["main"], reqs["compactor"])
	}
	if tot.Input != int64(in) || tot.CacheRead != int64(read) || tot.CacheWrite != int64(write) || tot.Output != int64(out) {
		t.Errorf("tokens in/read/write/out = %d/%d/%d/%d, want %d/%d/%d/%d", tot.Input, tot.CacheRead, tot.CacheWrite, tot.Output, in, read, write, out)
	}
	near(t, "reported cost", sum.Cost.Reported, usd)
	if tot.Pending != 0 || tot.Failed != 0 {
		t.Errorf("pending/failed = %d/%d, want 0/0", tot.Pending, tot.Failed)
	}
	if got := sum.Compaction.Commits; got != counts["compact.commit"] {
		t.Errorf("commits = %d, want %d", got, counts["compact.commit"])
	}
	if got := sum.Compaction.Rejects; got != counts["compact.reject"] {
		t.Errorf("rejects = %d, want %d", got, counts["compact.reject"])
	}
	if sum.Log.Events != int64(counts["turn.append"]+counts["model.request"]+counts["model.response"]+counts["tool.call"]+counts["tool.result"]+
		counts["compact.plan"]+counts["compact.patch"]+counts["compact.commit"]+counts["compact.reject"]+counts["layer.commit"]+counts["user.input"]) {
		t.Errorf("event count %d does not match the per-type counts %v", sum.Log.Events, counts)
	}
	if sum.Log.TornBytes != 0 || sum.Log.BadLines != 0 {
		t.Errorf("torn/bad = %d/%d, want 0/0", sum.Log.TornBytes, sum.Log.BadLines)
	}
	if want := float64(read) / float64(in+read+write); sum.Cache.HitRatio < want-1e-9 || sum.Cache.HitRatio > want+1e-9 {
		t.Errorf("hit ratio = %f, want %f", sum.Cache.HitRatio, want)
	}
}

func TestFixtureStory(t *testing.T) {
	requireFixture(t)
	s := mustLoad(t, fixtureDir)
	sum := s.Summary()
	if sum.Session.Model != "mock-1" || sum.Session.Provider != "mock" || sum.Session.Renderer != "sleipnir-kv/1" {
		t.Errorf("session meta = %+v", sum.Session)
	}
	if sum.Session.Goal != "please build the whole thing" {
		t.Errorf("goal = %q", sum.Session.Goal)
	}
	if sum.State != StateIdle {
		t.Errorf("state = %s: a log with no session.end and an old mtime is idle", sum.State)
	}
	c := sum.Cache
	if c.FirstRequests != 1 || c.ColdStarts != 1 || c.WarmFirst != 0 {
		t.Errorf("first/cold/warmFirst = %d/%d/%d, want 1/1/0", c.FirstRequests, c.ColdStarts, c.WarmFirst)
	}
	if c.SteadyHitRatio < 0.85 {
		t.Errorf("steady-state hit ratio = %.3f, want >= 0.85 (docs/CACHE-DESIGN.md section 9)", c.SteadyHitRatio)
	}
	if c.RebaseRequests != 4 || c.RebaseHitRatio > 0.5 {
		t.Errorf("rebase requests/hit = %d/%.3f, want 4 requests reading under half of their prompt", c.RebaseRequests, c.RebaseHitRatio)
	}
	if c.AvgContext <= 0 || c.AvgContextNaive <= c.AvgContext {
		t.Errorf("average context %.0f vs naive %.0f: compaction must make the context smaller", c.AvgContext, c.AvgContextNaive)
	}
	if sum.Anomalies.Total != 0 || sum.Anomalies.Undeclared != 0 {
		t.Errorf("anomalies = %+v, want none: every change in this run is a declared commit", sum.Anomalies)
	}
	if len(sum.RL.Renderers) != 1 || sum.RL.WireHashes != 27 || sum.RL.TokenTraces != 27 {
		t.Errorf("RL fields = %+v, want wire hashes and token traces on all 27 responses", sum.RL)
	}

	// Compaction: one model patch, three mechanical fallbacks.
	comps := s.Compactions().Compactions
	if len(comps) != 4 {
		t.Fatalf("compactions = %d, want 4", len(comps))
	}
	if comps[0].Patch != "model" || comps[0].Mode != "fork" || !comps[0].NotesChanged || comps[0].Removed != 1984 || comps[0].Spine != 20 {
		t.Errorf("first compaction = %+v", comps[0])
	}
	for i, c := range comps[1:] {
		if c.Patch != "mechanical" || !strings.Contains(c.FallbackReason, "nothing to compact") {
			t.Errorf("compaction %d: patch %q fallback %q, want a mechanical fallback with the reject reason", i+2, c.Patch, c.FallbackReason)
		}
	}
	for _, c := range comps {
		if c.Status != "committed" || c.Next == nil || c.Calls != 1 || c.CompactorUSD <= 0 || c.Warm == nil || !*c.Warm {
			t.Errorf("compaction %d = status %s next %v calls %d usd %f", c.N, c.Status, c.Next, c.Calls, c.CompactorUSD)
		}
		if c.Net != c.Removed-c.Spine {
			t.Errorf("compaction %d net %d != removed %d - spine %d", c.N, c.Net, c.Removed, c.Spine)
		}
	}
	if comps[0].Next.Req != "be-1.10" || comps[0].Next.Hit > 0.31 || comps[0].Next.Rewrite < 6000 {
		t.Errorf("first commit's next request = %+v, want be-1.10 re-processing ~6.7k tokens", comps[0].Next)
	}
}

func TestFixtureLayerStack(t *testing.T) {
	requireFixture(t)
	s := mustLoad(t, fixtureDir)

	// be-1.9 is steady state: the previous prefix is intact and only the thread grew.
	rep, ok := s.Layers("be-1.9", false)
	if !ok {
		t.Fatal("no be-1.9")
	}
	state := map[string]string{}
	for _, l := range rep.Layers {
		state[l.Key] = l.State
	}
	if state["G0"] != LayerCached || state["G1"] != LayerCached || state["G5"] != LayerAppended || state["G2"] != LayerAbsent {
		t.Errorf("be-1.9 states = %v", state)
	}
	if rep.Rebase != "" || rep.FirstChange != "" {
		t.Errorf("be-1.9 rebase %q first change %q, want neither", rep.Rebase, rep.FirstChange)
	}

	// be-1.10 follows the first commit: notes and spine appear, the thread is
	// rewritten, and G0..G1 stay cached: a rebase is a partial rewrite.
	rep, _ = s.Layers("be-1.10", true)
	state = map[string]string{}
	for _, l := range rep.Layers {
		state[l.Key] = l.State
	}
	if state["G0"] != LayerCached || state["G1"] != LayerCached || state["G3"] != LayerNew || state["G4"] != LayerNew || state["G5"] != LayerRewritten {
		t.Errorf("be-1.10 states = %v", state)
	}
	if rep.Rebase != "commit" || rep.FirstChange != "G3" || rep.Prev != "be-1.9" || rep.Next != "be-1.11" {
		t.Errorf("be-1.10 rebase %q first change %q prev %q next %q", rep.Rebase, rep.FirstChange, rep.Prev, rep.Next)
	}
	if rep.Read != 2944 || rep.Read+rep.Write+rep.Fresh != rep.Prompt || rep.Prompt != 9687 {
		t.Errorf("billing split read/write/fresh = %d/%d/%d of %d", rep.Read, rep.Write, rep.Fresh, rep.Prompt)
	}
	sum := 0
	for _, l := range rep.Layers {
		sum += l.Tokens
		if l.Read+l.Write+l.Fresh != l.Tokens {
			t.Errorf("%s: read+write+fresh = %d, tokens %d", l.Key, l.Read+l.Write+l.Fresh, l.Tokens)
		}
	}
	if sum != rep.Prompt {
		t.Errorf("layer tokens sum to %d, want the provider's prompt size %d", sum, rep.Prompt)
	}
	byKey := map[string]LayerInfo{}
	for _, l := range rep.Layers {
		byKey[l.Key] = l
	}
	if g := byKey["G0"]; g.Source != "blob" || g.Tokens < 1000 || !strings.Contains(g.Text, "You are Sleipnir") || !strings.Contains(g.Text, "tools (2)") {
		t.Errorf("G0 = source %s tokens %d text %.40q", g.Source, g.Tokens, g.Text)
	}
	if g := byKey["G3"]; !strings.Contains(g.Text, "<my-notes>") || g.Hash != "4057d109f677" {
		t.Errorf("G3 = hash %s text %.40q", g.Hash, g.Text)
	}
	if byKey["G5"].Source != "derived" || byKey["G5"].Hash != "t6–t19" {
		t.Errorf("G5 = source %s hash %q", byKey["G5"].Source, byKey["G5"].Hash)
	}
	// Notes did not exist before: no diff, but a note says so.
	if len(rep.Diffs) != 0 {
		t.Errorf("diffs = %+v, want none: nothing that existed before changed", rep.Diffs)
	}

	// be-1.13 follows the second commit: the spine changed, so the first changed
	// layer is G4, and the notes layer before it stays cached.
	rep, _ = s.Layers("be-1.13", true)
	if rep.FirstChange != "G4" {
		t.Errorf("be-1.13 first change %q, want G4", rep.FirstChange)
	}
	state = map[string]string{}
	for _, l := range rep.Layers {
		state[l.Key] = l.State
	}
	if state["G3"] != LayerCached || state["G4"] != LayerRewritten || state["G5"] != LayerRewritten {
		t.Errorf("be-1.13 states = %v", state)
	}
	if len(rep.Diffs) != 1 || rep.Diffs[0].Layer != "G4" || rep.Diffs[0].At <= 0 || !strings.HasPrefix(rep.Diffs[0].Before[:rep.Diffs[0].Split], "<history>") {
		t.Errorf("be-1.13 diffs = %+v, want the spine's first differing byte", rep.Diffs)
	}
}

// ---- hand-made logs: exact numbers ----------------------------------------------------------------------

// costScenario is one agent on claude-sonnet-5-5 (input $2/M, output $10/M, cache
// read $0.2/M, 5-minute write $2.5/M, 5 minute cache lifetime) with a commit and
// an idle gap between the second and third request.
func costScenario(t *testing.T) *Session {
	b := newEvb(t)
	secs := []m{sec("shared", 8000, h64("shared"))}
	b.at(0)
	b.emit("", "session.start", m{"model": "claude-sonnet-5-5", "provider": "test"})
	b.req("a", "a.1", secs, nil)
	b.at(2*time.Second).resp("a", "a.1", 0, 0, 10000, 100, m{"cost_usd": 0.03, "gateway_cost": true})
	b.at(60 * time.Second)
	b.req("a", "a.2", secs, nil)
	b.at(62*time.Second).resp("a", "a.2", 500, 10000, 1500, 200, nil)
	b.at(63*time.Second).emit("a", "compact.plan", m{"decision": "start", "mode": "fork", "reason": "pressure", "warm": true, "thread_tokens": 10000})
	b.at(64*time.Second).emit("a", "compact.commit", m{"reason": "pays back within 2 turns", "removed_tokens": 6000, "retained_tokens": 4000, "spine_added": 100, "removed_turns": 5, "fallback": false, "held_ms": 5})
	b.at(700 * time.Second)
	b.req("a", "a.3", secs, m{"thread_from": 6})
	b.at(702*time.Second).resp("a", "a.3", 600, 0, 6000, 50, nil)
	return mustLoad(t, b.dir)
}

func TestCostAgainstTwoCounterfactuals(t *testing.T) {
	s := costScenario(t)
	sum := s.Summary()
	c := sum.Cost

	// Actual: usage x table prices.
	near(t, "actual.uncached", c.Actual.Uncached, 1100*2/1e6)
	near(t, "actual.read", c.Actual.Read, 10000*0.2/1e6)
	near(t, "actual.write", c.Actual.Write, 17500*2.5/1e6)
	near(t, "actual.output", c.Actual.Output, 350*10/1e6)
	near(t, "actual.total", c.Actual.Total, 0.05145)

	// No cache: every prompt token at the plain input price.
	near(t, "nocache.uncached", c.NoCache.Uncached, 28600*2/1e6)
	near(t, "nocache.total", c.NoCache.Total, 28600*2/1e6+350*10/1e6)
	if c.NoCache.Read != 0 || c.NoCache.Write != 0 {
		t.Errorf("no-cache bill has read %f / write %f", c.NoCache.Read, c.NoCache.Write)
	}

	// Naive: request 2 is warm (10000 read, growth written); request 3 sees a
	// context of 6600 + 5900 folded tokens and is cold (idle gap over 5 minutes).
	near(t, "naive.read", c.Naive.Read, 10000*0.2/1e6)
	near(t, "naive.write", c.Naive.Write, (10000+2000+12500)*2.5/1e6)
	near(t, "naive.total", c.Naive.Total, 0.026+0.009+0.03175)
	near(t, "saved vs no cache", c.SavedNoCache, c.NoCache.Total-c.Actual.Total)
	near(t, "saved vs naive", c.SavedNaive, 0.06675-0.05145)
	near(t, "saved vs naive pct", c.SavedNaivePct, (0.06675-0.05145)/0.06675)

	// The log's own costs are reported separately: only request 1 carried cost_usd.
	near(t, "reported", c.Reported, 0.03)
	if c.ReportedRequests != 1 || c.GatewayRequests != 1 {
		t.Errorf("reported/gateway requests = %d/%d, want 1/1", c.ReportedRequests, c.GatewayRequests)
	}
	if c.Divergence != 0 {
		t.Errorf("divergence = %f, want 0 while some responses carry no cost", c.Divergence)
	}
	if len(c.Prices) != 1 || c.Prices[0].Source != "table" || c.Prices[0].TTLSeconds != 300 || !c.Prices[0].Explicit || c.Prices[0].Requests != 3 {
		t.Errorf("prices = %+v", c.Prices)
	}
	if len(c.Assumptions) < 4 || !strings.Contains(strings.Join(c.Assumptions, " "), "ESTIMATE") {
		t.Errorf("assumptions must be written out and label the naive bill an estimate: %v", c.Assumptions)
	}

	// Per-request rows carry the same numbers.
	r3 := reqByID(t, s, "a.3")
	near(t, "a.3 priced usd", r3.USD, 0.0167)
	near(t, "a.3 naive", r3.Naive, 0.03175)
	if r3.NaiveCtx != 12500 || r3.Prompt != 6600 {
		t.Errorf("a.3 context %d naive %d, want 6600 and 12500", r3.Prompt, r3.NaiveCtx)
	}
	r1 := reqByID(t, s, "a.1")
	near(t, "a.1 reported usd", r1.USD, 0.03) // the log's figure wins for the row
	// The row says where its cost came from and keeps the table price beside it.
	near(t, "a.1 priced", r1.Priced, 10000*2.5/1e6+100*10/1e6)
	if !r1.Gateway || r3.Gateway || reqByID(t, s, "a.2").Gateway {
		t.Errorf("gateway flags a.1/a.2/a.3 = %v/%v/%v, want only a.1", r1.Gateway, reqByID(t, s, "a.2").Gateway, r3.Gateway)
	}
	near(t, "a.3 priced equals usd when the log has no cost", r3.Priced, r3.USD)
}

func TestCacheStatistics(t *testing.T) {
	s := costScenario(t)
	sum := s.Summary()
	c := sum.Cache
	if c.ColdStarts != 2 || c.FirstRequests != 1 || c.WarmFirst != 0 {
		t.Errorf("cold/first/warm-first = %d/%d/%d, want 2/1/0 (the first request and the one after the idle gap)", c.ColdStarts, c.FirstRequests, c.WarmFirst)
	}
	near(t, "hit ratio", c.HitRatio, 10000.0/28600)
	near(t, "steady hit ratio", c.SteadyHitRatio, 10000.0/12000)
	near(t, "rebase hit ratio", c.RebaseHitRatio, 0)
	if c.SteadyRequests != 1 || c.RebaseRequests != 1 {
		t.Errorf("steady/rebase requests = %d/%d, want 1/1", c.SteadyRequests, c.RebaseRequests)
	}
	near(t, "average context", c.AvgContext, (10000+12000+6600)/3.0)
	near(t, "naive average context", c.AvgContextNaive, (10000+12000+12500)/3.0)
	near(t, "context reduction", c.ContextReduction, 1-(28600/3.0)/(34500/3.0))
	if c.MaxContext != 12000 {
		t.Errorf("max context = %d, want 12000", c.MaxContext)
	}
	comp := sum.Compaction
	if comp.Commits != 1 || comp.Folded != 6000 || comp.SpineAdded != 100 || comp.Net != 5900 || comp.Rebases != 1 {
		t.Errorf("compaction totals = %+v", comp)
	}
	r3 := reqByID(t, s, "a.3")
	if r3.Rebase != "commit" || !r3.Cold || r3.First || !r3.Done {
		t.Errorf("a.3 = %+v: a cold request right after a commit", r3)
	}
	if got := reqByID(t, s, "a.2"); got.Rebase != "" || got.Cold || got.Hit < 0.83 {
		t.Errorf("a.2 = %+v: a warm steady-state request", got)
	}
	// The commit's next request is a.3: it paid to re-process 6600 tokens.
	cm := s.Compactions().Compactions[0]
	if cm.Next == nil || cm.Next.Req != "a.3" || cm.Next.Rewrite != 6600 || cm.PromptBefore != 12000 {
		t.Errorf("commit next = %+v prompt before %d", cm.Next, cm.PromptBefore)
	}
}

func TestUnknownModelUsesFallbackPricesAndSaysSo(t *testing.T) {
	b := newEvb(t)
	b.req("a", "a.1", nil, m{"model": "acme/frobnicator-9"})
	b.resp("a", "a.1", 1000, 0, 0, 10, m{"model": "acme/frobnicator-9"})
	sum := mustLoad(t, b.dir).Summary()
	if len(sum.Cost.Prices) != 1 || sum.Cost.Prices[0].Source != "fallback" {
		t.Fatalf("prices = %+v, want a fallback entry", sum.Cost.Prices)
	}
	found := false
	for _, w := range sum.Warnings {
		if strings.Contains(w, "frobnicator") && strings.Contains(w, "fallback") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming the model with fallback prices", sum.Warnings)
	}
}

func TestCostFallsBackToPricedWhenTheLogHasNoCost(t *testing.T) {
	b := newEvb(t)
	b.req("a", "a.1", nil, nil)
	b.resp("a", "a.1", 1000, 0, 0, 10, nil)
	s := mustLoad(t, b.dir)
	sum := s.Summary()
	near(t, "row usd", reqByID(t, s, "a.1").USD, (1000*2+10*10)/1e6)
	if sum.Cost.ReportedRequests != 0 || sum.Cost.Reported != 0 {
		t.Errorf("reported = %f over %d requests, want none reported", sum.Cost.Reported, sum.Cost.ReportedRequests)
	}
}

func TestNoReadReportsIsFlagged(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 3000, h64("s"))}
	for i := 1; i <= 4; i++ {
		id := "a." + string(rune('0'+i))
		b.at(time.Duration(i) * time.Second)
		b.req("a", id, secs, m{"shared_tokens": 3000 * (i - 1), "thread_to": i})
		b.resp("a", id, 4000+i*100, 0, 0, 10, m{"expected_read": 3000 * (i - 1)})
	}
	sum := mustLoad(t, b.dir).Summary()
	if !sum.Cache.NoReadReports {
		t.Error("NoReadReports = false: the guard expected a cached prefix but no request ever read from cache")
	}
	found := false
	for _, w := range sum.Warnings {
		found = found || strings.Contains(w, "cache usage")
	}
	if !found {
		t.Errorf("warnings = %v", sum.Warnings)
	}
}

func TestSeriesIsDownsampledAndKeepsTotals(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 3000, h64("s"))}
	n := 1200
	var wantRead int64
	for i := 1; i <= n; i++ {
		id := "a." + itoa(i)
		b.at(time.Duration(i) * time.Second)
		b.req("a", id, secs, nil)
		b.resp("a", id, 100, 900+i, 0, 5, nil)
		wantRead += int64(900 + i)
	}
	s := mustLoad(t, b.dir)
	ser := s.Summary().Series
	if len(ser.Points) == 0 || len(ser.Points) > 480 || ser.Window != n {
		t.Fatalf("series = %d points over a window of %d", len(ser.Points), ser.Window)
	}
	var read int64
	var reqs int
	for _, p := range ser.Points {
		read += p.Read
		reqs += p.N
	}
	if read != wantRead || reqs != n {
		t.Errorf("downsampled series read %d over %d requests, want %d over %d", read, reqs, wantRead, n)
	}
	for i := 1; i < len(ser.Points); i++ {
		if ser.Points[i].T < ser.Points[i-1].T || ser.Points[i].I <= ser.Points[i-1].I {
			t.Fatalf("series not ordered at %d: %+v then %+v", i, ser.Points[i-1], ser.Points[i])
		}
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Join(strings.Fields(sprint(i)), ""))
}

// ---- compaction episodes ----------------------------------------------------------------------------------

func TestCompactionEpisodes(t *testing.T) {
	type step func(b *evb)
	yes, no := true, false
	_ = no
	cases := []struct {
		name  string
		steps []step
		check func(t *testing.T, c []Compaction, tot CompactionTotals)
	}{
		{
			name: "fork with a model patch",
			steps: []step{
				func(b *evb) {
					b.emit("a", "compact.plan", m{"decision": "start", "mode": "fork", "reason": "thread 9k over soft limit 8k", "warm": true, "thread_tokens": 9000})
					b.req("a", "a.c1", nil, m{"kind": "compactor", "role": "compactor"})
					b.emit("a", "compact.patch", m{"stage": "request", "reason": "x"})
					b.resp("a", "a.c1", 300, 8000, 0, 120, m{"side": true, "cost_usd": 0.004})
					b.emit("a", "compact.patch", m{"stage": "ready", "removed_tokens": 6000, "retained_tokens": 3000, "spine_added": 80, "masked": 2, "cost_ite": 1500, "fallback": false})
					b.emit("a", "compact.plan", m{"decision": "commit?", "yes": yes, "net_ite": 5000, "reason": "pays back within 3 turns", "warm": true, "thread_tokens": 9000})
					b.emit("a", "compact.commit", m{"reason": "pays back within 3 turns", "removed_tokens": 6000, "retained_tokens": 3000, "spine_added": 80, "masked": 2, "notes_changed": true, "fallback": false, "held_ms": 40})
				},
			},
			check: func(t *testing.T, c []Compaction, tot CompactionTotals) {
				if len(c) != 1 {
					t.Fatalf("episodes = %d, want 1", len(c))
				}
				e := c[0]
				if e.Mode != "fork" || e.Patch != "model" || e.Status != "committed" || e.Trigger != "thread 9k over soft limit 8k" || e.Reason != "pays back within 3 turns" {
					t.Errorf("episode = %+v", e)
				}
				if e.Removed != 6000 || e.Spine != 80 || e.Net != 5920 || e.Masked != 2 || !e.NotesChanged || e.Calls != 1 || e.CompactorITE != 1500 || e.HeldMs != 40 || e.NetITE != 5000 {
					t.Errorf("episode numbers = %+v", e)
				}
				near(t, "compactor usd", e.CompactorUSD, 0.004)
				if len(e.Steps) != 5 || e.Steps[0].Kind != "start" || e.Steps[4].Kind != "commit" {
					t.Errorf("steps = %+v", e.Steps)
				}
				if tot.Commits != 1 || tot.Fork != 1 || tot.Fallbacks != 0 || tot.Net != 5920 {
					t.Errorf("totals = %+v", tot)
				}
			},
		},
		{
			name: "fork falling back to a mechanical patch",
			steps: []step{
				func(b *evb) {
					b.emit("a", "compact.plan", m{"decision": "start", "mode": "fork", "reason": "pressure", "warm": true, "thread_tokens": 9000})
					b.emit("a", "compact.reject", m{"stage": "model_patch", "reason": "unparseable patch", "fallback": "mechanical"})
					b.emit("a", "compact.patch", m{"stage": "ready", "removed_tokens": 5000, "retained_tokens": 2000, "spine_added": 40, "mechanical_lines": 3, "fallback": true})
					b.emit("a", "compact.plan", m{"decision": "commit?", "yes": true, "net_ite": 100, "reason": "ok", "thread_tokens": 7000})
					b.emit("a", "compact.commit", m{"reason": "ok", "removed_tokens": 5000, "retained_tokens": 2000, "spine_added": 40, "mechanical_lines": 3, "fallback": true})
				},
			},
			check: func(t *testing.T, c []Compaction, tot CompactionTotals) {
				e := c[0]
				if e.Patch != "mechanical" || e.FallbackReason != "unparseable patch" || e.MechLines != 3 || tot.Fallbacks != 1 || tot.Rejects != 1 {
					t.Errorf("episode = %+v totals = %+v", e, tot)
				}
			},
		},
		{
			name: "cold-moment mask needs no model call and is not a fallback",
			steps: []step{
				func(b *evb) {
					b.emit("a", "compact.plan", m{"decision": "start", "mode": "mask", "reason": "cache is cold", "warm": false, "thread_tokens": 9000})
					b.emit("a", "compact.commit", m{"reason": "mask: cache is cold", "removed_tokens": 3000, "retained_tokens": 6000, "spine_added": 0, "masked": 5, "fallback": true})
				},
			},
			check: func(t *testing.T, c []Compaction, tot CompactionTotals) {
				e := c[0]
				if e.Mode != "mask" || e.Patch != "mechanical" || e.Reason != "cache is cold" || e.Warm == nil || *e.Warm || e.Calls != 0 || e.Masked != 5 {
					t.Errorf("episode = %+v", e)
				}
				if tot.Mask != 1 || tot.Fallbacks != 0 || tot.Fork != 0 {
					t.Errorf("totals = %+v: a mask commit is neither a fork nor a fallback", tot)
				}
			},
		},
		{
			name: "emergency compaction has no plan",
			steps: []step{
				func(b *evb) {
					b.emit("a", "compact.commit", m{"reason": "emergency: prompt over 85% of the context window", "removed_tokens": 40000, "retained_tokens": 10000, "spine_added": 200, "fallback": true})
				},
			},
			check: func(t *testing.T, c []Compaction, tot CompactionTotals) {
				e := c[0]
				if len(c) != 1 || e.Mode != "emergency" || e.Reason != "prompt over 85% of the context window" || e.Status != "committed" || tot.Emergency != 1 || e.ThreadTokens != 50000 {
					t.Errorf("episode = %+v totals %+v", e, tot)
				}
			},
		},
		{
			name: "a patch held until the economics allow it",
			steps: []step{
				func(b *evb) {
					b.emit("a", "compact.plan", m{"decision": "start", "mode": "fork", "reason": "soft limit", "warm": true, "thread_tokens": 9000})
					b.emit("a", "compact.patch", m{"stage": "ready", "removed_tokens": 900, "retained_tokens": 8000, "spine_added": 200})
					b.emit("a", "compact.plan", m{"decision": "commit?", "yes": false, "net_ite": -300, "reason": "does not pay back yet", "warm": true, "thread_tokens": 9000})
					b.emit("a", "compact.plan", m{"decision": "commit?", "yes": false, "net_ite": -100, "reason": "does not pay back yet", "warm": true, "thread_tokens": 9200})
				},
			},
			check: func(t *testing.T, c []Compaction, tot CompactionTotals) {
				e := c[0]
				if e.Status != "ready" || e.Holds != 2 || tot.Held != 2 || tot.Commits != 0 {
					t.Errorf("episode = %+v totals %+v", e, tot)
				}
			},
		},
		{
			name: "a commit rejected by the compare-and-swap ends the attempt",
			steps: []step{
				func(b *evb) {
					b.emit("a", "compact.plan", m{"decision": "start", "mode": "fork", "reason": "soft limit", "warm": true, "thread_tokens": 9000})
					b.emit("a", "compact.patch", m{"stage": "ready", "removed_tokens": 5000, "retained_tokens": 4000, "spine_added": 50})
					b.emit("a", "compact.reject", m{"reason": "commit rejected: thread changed underneath"})
					b.emit("a", "compact.plan", m{"decision": "start", "mode": "fork", "reason": "soft limit again", "warm": true, "thread_tokens": 9500})
				},
			},
			check: func(t *testing.T, c []Compaction, tot CompactionTotals) {
				if len(c) != 2 || c[0].Status != "failed" || len(c[0].Rejects) != 1 || c[1].Status != "running" || tot.Commits != 0 || tot.Rejects != 1 {
					t.Errorf("episodes = %+v", c)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newEvb(t)
			for i, st := range tc.steps {
				b.at(time.Duration(i+1) * time.Second)
				st(b)
			}
			s := mustLoad(t, b.dir)
			rep := s.Compactions()
			tc.check(t, rep.Compactions, rep.Totals)
		})
	}
}

// ---- anomalies ---------------------------------------------------------------------------------------------------

func TestAnomalyLinkingAndExplanation(t *testing.T) {
	b := newEvb(t)
	shared, notes := h64("shared"), h64("notes")
	secs := []m{sec("shared", 4000, shared), sec("notes", 300, notes)}
	// a.1, a.2: a steady agent.
	b.at(0).req("a", "a.1", secs, m{"cache_key": "k1", "thread_to": 1, "manifest": m{"tools": h64("tools"), "system": []string{h64("sys")}}})
	b.at(time.Second).resp("a", "a.1", 200, 0, 5000, 10, nil)
	b.at(10*time.Second).req("a", "a.2", secs, m{"cache_key": "k1", "thread_to": 3, "manifest": m{"tools": h64("tools"), "system": []string{h64("sys")}}})
	b.at(11*time.Second).resp("a", "a.2", 200, 5000, 300, 10, m{"expected_read": 5000})
	// The guard reports drift, then a.3 arrives with a different tool list: no declared rebase.
	b.at(20*time.Second).emit("a", "cache.anomaly", m{"kind": "drift", "diverged": "tools", "shared_blocks": 0})
	b.at(20*time.Second).req("a", "a.3", secs, m{"cache_key": "k1", "thread_to": 5, "manifest": m{"tools": h64("tools2"), "system": []string{h64("sys")}}})
	b.at(21*time.Second).resp("a", "a.3", 200, 0, 5600, 10, m{"expected_read": 0})
	// a.4: a low read with a routing key change, warm agent.
	b.at(30*time.Second).req("a", "a.4", secs, m{"cache_key": "k2", "thread_to": 7, "manifest": m{"tools": h64("tools2"), "system": []string{h64("sys")}}})
	b.at(30*time.Second).emit("a", "cache.anomaly", m{"kind": "low_hit", "req": "a.4", "expected_read": 5800, "actual_read": 1000, "diverged": ""})
	b.at(31*time.Second).resp("a", "a.4", 200, 1000, 4800, 10, m{"expected_read": 5800, "anomaly": true})
	// a.5: the notes layer changes with no declared rebase and the guard says nothing.
	b.at(40*time.Second).req("a", "a.5", []m{sec("shared", 4000, shared), sec("notes", 320, h64("notes2"))}, m{"cache_key": "k2", "thread_to": 9, "manifest": m{"tools": h64("tools2"), "system": []string{h64("sys")}}})
	b.at(41*time.Second).resp("a", "a.5", 200, 4000, 1500, 10, nil)

	s := mustLoad(t, b.dir)
	rep := s.Anomalies()
	if rep.Totals.Total != 2 || rep.Totals.Drift != 1 || rep.Totals.LowHit != 1 || rep.Totals.Undeclared != 1 {
		t.Fatalf("totals = %+v, want 1 drift, 1 low read and 1 undeclared change the guard did not flag", rep.Totals)
	}
	if rep.Checked != 4 {
		t.Errorf("checked = %d, want 4 consecutive comparisons", rep.Checked)
	}
	if len(rep.Anomalies) != 3 {
		t.Fatalf("anomalies = %d, want the two guard findings and the undeclared change", len(rep.Anomalies))
	}
	drift, low, und := rep.Anomalies[0], rep.Anomalies[1], rep.Anomalies[2]
	if drift.Kind != "drift" || drift.Req != "a.3" || drift.Severity != "error" || drift.Diverged != "tools" || len(drift.Changed) != 1 || drift.Changed[0] != "G0" {
		t.Errorf("drift = %+v", drift)
	}
	if !strings.Contains(strings.Join(drift.Explain, " "), "tool list changed") {
		t.Errorf("drift explanation must say what a tools change means: %v", drift.Explain)
	}
	if low.Kind != "low_hit" || low.Req != "a.4" || low.Severity != "warning" || !low.KeyChanged || low.Expected != 5800 || low.Actual != 1000 || low.GapMs != 10000 {
		t.Errorf("low read = %+v", low)
	}
	if !strings.Contains(strings.Join(low.Causes, " "), "routing key changed") {
		t.Errorf("a routing key change must be listed as a cause: %v", low.Causes)
	}
	if und.Kind != "undeclared" || und.Req != "a.5" || und.Diverged != "notes" || und.Severity != "warning" {
		t.Errorf("undeclared = %+v", und)
	}
	for i, a := range rep.Anomalies {
		if a.N != i+1 || a.Title == "" || len(a.Explain) == 0 {
			t.Errorf("anomaly %d not fully described: %+v", i, a)
		}
	}
	// Rows carry the flag and its kind, so the chart can mark them.
	if r := reqByID(t, s, "a.3"); !r.Anomaly || r.AnomalyKind != "drift" || !r.Undeclared {
		t.Errorf("a.3 row = %+v", r)
	}
	if r := reqByID(t, s, "a.4"); !r.Anomaly || r.AnomalyKind != "low_hit" {
		t.Errorf("a.4 row = %+v", r)
	}
	if r := reqByID(t, s, "a.5"); r.Anomaly || !r.Undeclared || len(r.Changed) != 1 || r.Changed[0] != "G3" {
		t.Errorf("a.5 row = %+v", r)
	}
}

func TestLowReadAfterAnIdleGapNamesTheTTL(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 4000, h64("shared"))}
	b.at(0).req("a", "a.1", secs, nil)
	b.at(time.Second).resp("a", "a.1", 0, 0, 5000, 10, nil)
	b.at(20*time.Minute).req("a", "a.2", secs, m{"thread_to": 3})
	b.at(20*time.Minute).emit("a", "cache.anomaly", m{"kind": "low_hit", "req": "a.2", "expected_read": 5000, "actual_read": 0})
	b.at(20*time.Minute+time.Second).resp("a", "a.2", 5200, 0, 0, 10, m{"anomaly": true, "expected_read": 5000})
	rep := mustLoad(t, b.dir).Anomalies()
	if len(rep.Anomalies) != 1 || !strings.Contains(strings.Join(rep.Anomalies[0].Causes, " "), "cache lifetime") || rep.Anomalies[0].TTLMs != 300000 {
		t.Errorf("anomalies = %+v, want the idle gap named against the 5 minute cache lifetime", rep.Anomalies)
	}
}

func TestDeclaredRebasesAreNotAnomalies(t *testing.T) {
	b := newEvb(t)
	shared := h64("shared")
	man := func(sys string) m { return m{"manifest": m{"tools": h64("t"), "system": []string{sys}}} }
	_ = man
	b.at(0).req("a", "a.1", []m{sec("shared", 4000, shared), sec("spine", 100, h64("sp1"))}, m{"thread_to": 1})
	b.at(time.Second).resp("a", "a.1", 0, 0, 5000, 10, nil)
	// A commit precedes a.2: the spine changes and the thread restarts.
	b.at(5*time.Second).emit("a", "compact.commit", m{"reason": "ok", "removed_tokens": 3000, "retained_tokens": 1000, "spine_added": 60})
	b.at(6*time.Second).req("a", "a.2", []m{sec("shared", 4000, shared), sec("spine", 160, h64("sp2"))}, m{"thread_from": 5, "thread_to": 6})
	b.at(7*time.Second).resp("a", "a.2", 200, 4000, 900, 10, nil)
	// A shared-layer sync precedes a.3: the shared pin changes.
	b.at(8*time.Second).emit("a", "layer.commit", m{"scope": "shared-sync", "reason": "epoch"})
	b.at(9*time.Second).req("a", "a.3", []m{sec("shared", 4100, h64("shared2")), sec("spine", 160, h64("sp2"))}, m{"thread_from": 5, "thread_to": 8})
	b.at(10*time.Second).resp("a", "a.3", 200, 0, 5000, 10, nil)
	s := mustLoad(t, b.dir)
	if r := reqByID(t, s, "a.2"); r.Rebase != "commit" || r.Undeclared || len(r.Changed) != 2 {
		t.Errorf("a.2 = %+v, want a declared commit changing the spine and the thread", r)
	}
	if r := reqByID(t, s, "a.3"); r.Rebase != "sync" || r.Undeclared {
		t.Errorf("a.3 = %+v, want a declared shared-layer sync", r)
	}
	if sum := s.Summary(); sum.Anomalies.Undeclared != 0 || sum.Compaction.Rebases != 2 {
		t.Errorf("anomalies %+v rebases %d, want none undeclared and two declared rebases", sum.Anomalies, sum.Compaction.Rebases)
	}
}

// ---- requests: retries, failures, odd ids ------------------------------------------------------------------------

func TestRequestLifecycleEdgeCases(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 2000, h64("s"))}
	// A retried request succeeds; a failed one never answers; one is still in flight.
	b.at(0).req("a", "a.1", secs, nil)
	b.at(time.Second).emit("a", "model.error", m{"req": "a.1", "kind": "rate_limit", "status": 429, "attempt": 1, "delay_ms": 2000})
	b.at(4*time.Second).resp("a", "a.1", 100, 0, 2000, 10, nil)
	b.at(5*time.Second).req("a", "a.2", secs, nil)
	b.at(6*time.Second).emit("a", "model.error", m{"req": "a.2", "error": "provider: overloaded (http 529): try later"})
	b.at(7*time.Second).req("a", "a.3", secs, nil)
	// A response whose request was never seen (a log that starts mid-session).
	b.at(8*time.Second).resp("b", "b.9", 500, 1500, 0, 5, nil)
	// A duplicate id after completion (a resumed session restarts its counters).
	b.at(9*time.Second).req("a", "a.1", secs, nil)
	b.at(10*time.Second).resp("a", "a.1", 100, 2000, 0, 10, nil)

	s := mustLoad(t, b.dir)
	sum := s.Summary()
	tot := sum.Totals
	if tot.Requests != 5 || tot.Failed != 1 || tot.Pending != 1 || tot.Retries != 1 || tot.RateLimited != 1 {
		t.Errorf("totals = %+v, want 5 requests, 1 failed, 1 pending, 1 retry (429)", tot)
	}
	if r := reqByID(t, s, "a.2"); !r.Failed || !strings.Contains(r.Err, "overloaded") || r.Done {
		t.Errorf("a.2 = %+v", r)
	}
	if r := reqByID(t, s, "a.3"); r.Done || r.Failed {
		t.Errorf("a.3 = %+v, want in flight", r)
	}
	if r := reqByID(t, s, "b.9"); !r.Done || r.Prompt != 2000 || r.Agent != "b" {
		t.Errorf("b.9 = %+v: a response with no request is still a request", r)
	}
	if sum.Swarm.PeakInFlight < 2 {
		t.Errorf("peak in flight = %d, want at least 2", sum.Swarm.PeakInFlight)
	}
	if g := s.Swarm().Governor; g.Errors["rate_limit"] != 1 || g.RateLimited != 1 || g.Retries != 1 {
		t.Errorf("governor = %+v", g)
	}
	// Agents' states: a has an in-flight request, so it is running.
	for _, a := range s.Agents() {
		if a.ID == "a" && a.State != "running" && a.State != "idle" {
			t.Errorf("agent a state = %s", a.State)
		}
	}
	found := false
	for _, w := range sum.Warnings {
		found = found || strings.Contains(w, "failed without a response")
	}
	if !found {
		t.Errorf("warnings = %v, want the failed request mentioned", sum.Warnings)
	}
}

func TestPagingRequestsByRevision(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 2000, h64("s"))}
	for i := 1; i <= 10; i++ {
		b.at(time.Duration(i)*time.Second).req("a", "a."+itoa(i), secs, nil)
	}
	s := mustLoad(t, b.dir)
	page := s.Requests(RequestQuery{Limit: 4})
	if len(page.Requests) != 4 || !page.More || page.Total != 10 {
		t.Fatalf("first page = %d rows more=%v total=%d", len(page.Requests), page.More, page.Total)
	}
	seen := map[string]bool{}
	for _, r := range page.Requests {
		seen[r.ID] = true
	}
	cursor := page.Rev
	for page.More {
		page = s.Requests(RequestQuery{Since: cursor, Limit: 4})
		for _, r := range page.Requests {
			seen[r.ID] = true
		}
		cursor = page.Rev
	}
	if len(seen) != 10 {
		t.Fatalf("paging by revision returned %d of 10 requests", len(seen))
	}
	// A pending request that completes later is returned again, once.
	b.at(20*time.Second).resp("a", "a.3", 100, 0, 2000, 5, nil)
	mustTail(t, s)
	page = s.Requests(RequestQuery{Since: cursor, Limit: 100})
	if len(page.Requests) != 1 || page.Requests[0].ID != "a.3" || !page.Requests[0].Done {
		t.Fatalf("delta = %+v, want only the request that just completed", page.Requests)
	}
	if again := s.Requests(RequestQuery{Since: page.Rev}); len(again.Requests) != 0 {
		t.Errorf("nothing changed, but got %d rows", len(again.Requests))
	}
	if page := s.Requests(RequestQuery{Tail: true, Limit: 3}); len(page.Requests) != 3 || page.Requests[2].ID != "a.10" || !page.More {
		t.Errorf("tail = %+v", page)
	}
	if page := s.Requests(RequestQuery{Agent: "nobody"}); len(page.Requests) != 0 {
		t.Errorf("unknown agent returned rows: %+v", page)
	}
	if page := s.Requests(RequestQuery{Kind: "side", Tail: true}); len(page.Requests) != 0 {
		t.Errorf("no side requests expected: %+v", page)
	}
}

func TestSessionStateAndMeta(t *testing.T) {
	now := t0.Add(time.Hour)
	cases := []struct {
		name  string
		setup func(b *evb)
		now   time.Time
		want  string
	}{
		{"no events", func(b *evb) {}, now, StateEmpty},
		{"ended", func(b *evb) {
			b.emit("", "session.start", m{"model": "x"})
			b.at(time.Minute).emit("", "session.end", m{"cost_usd": 1.5})
		}, now, StateEnded},
		{"recent write", func(b *evb) { b.at(59*time.Minute+55*time.Second).emit("", "log.open", nil) }, now, StateLive},
		{"old and unended", func(b *evb) { b.emit("", "log.open", nil) }, now, StateIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newEvb(t)
			tc.setup(b)
			if b.seq == 0 {
				b.raw("")
			}
			s, err := LoadWith(b.dir, Options{Now: func() time.Time { return tc.now }})
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Summary().State; got != tc.want {
				t.Errorf("state = %s, want %s", got, tc.want)
			}
		})
	}
	b := newEvb(t)
	b.emit("", "session.start", m{"model": "m1", "provider": "p", "renderer": "r/1", "swarm": true, "root": "/work/proj", "recon_tokens": 7, "version": "v9"})
	b.at(90*time.Second).emit("", "session.end", m{"cost_usd": 2.5})
	sum := mustLoad(t, b.dir).Summary().Session
	if sum.Model != "m1" || sum.Provider != "p" || !sum.Swarm || sum.Root != "proj" || sum.ReconTokens != 7 || sum.Version != "v9" || sum.DurationMs != 90000 || !sum.Ended || sum.EndCostUSD != 2.5 {
		t.Errorf("session meta = %+v (the root is shown by base name only)", sum)
	}
}

func TestToolStatsAndAgentState(t *testing.T) {
	b := newEvb(t)
	b.at(0).emit("a", "tool.call", m{"id": "c1", "name": "read", "input": m{"path": "internal/x/y.go"}})
	b.at(time.Second).emit("a", "tool.result", m{"id": "c1", "name": "read", "error": false, "chars": 500, "truncated": false, "ms": 30})
	b.at(2*time.Second).emit("a", "tool.call", m{"id": "c2", "name": "bash", "input": m{"command": "go test ./..."}})
	b.at(3*time.Second).emit("a", "tool.result", m{"id": "c2", "name": "bash", "error": true, "chars": 9000, "truncated": true, "ms": 1500})
	b.at(4*time.Second).emit("a", "tool.call", m{"id": "c3", "name": "bash", "input": m{"command": "go vet ./..."}})
	b.at(5*time.Second).emit("a", "tool.result", m{"id": "c3", "name": "bash", "error": false, "chars": 10, "truncated": false, "ms": 500})
	b.at(6*time.Second).emit("a", "tool.result", m{"id": "orphan", "name": "grep", "error": false, "chars": 1, "ms": 5}) // its call was lost
	b.at(7*time.Second).emit("a", "tool.call", m{"id": "c4", "name": "wait", "input": m{}})
	s, err := LoadWith(b.dir, Options{Now: func() time.Time { return t0.Add(8 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.Agent("a")
	if !ok {
		t.Fatal("no agent a")
	}
	byName := map[string]ToolStat{}
	for _, ts := range d.Tools {
		byName[ts.Name] = ts
	}
	bash := byName["bash"]
	if bash.Calls != 2 || bash.Errors != 1 || bash.Truncated != 1 || bash.TotalMs != 2000 || bash.MaxMs != 1500 || bash.AvgMs != 1000 || bash.Chars != 9010 {
		t.Errorf("bash = %+v", bash)
	}
	if g := byName["grep"]; g.Calls != 1 {
		t.Errorf("grep = %+v: a result without its call still counts once", g)
	}
	if d.Agent.ToolCalls != 5 || d.Agent.ToolErrors != 1 {
		t.Errorf("agent tool calls/errors = %d/%d, want 5/1", d.Agent.ToolCalls, d.Agent.ToolErrors)
	}
	if d.Agent.State != "waiting" || d.Agent.Line != "waiting for the team" {
		t.Errorf("state %q line %q: an open wait call means the agent is waiting", d.Agent.State, d.Agent.Line)
	}
	if d.Tools[0].Name != "bash" {
		t.Errorf("tools are ordered by calls: %v", d.Tools)
	}
	if _, ok := s.Agent("../../etc/passwd"); ok {
		t.Error("an agent id is a map key, not a path")
	}
}

func TestPerAgentTotalsAddUp(t *testing.T) {
	dir, res := synthDir(t, synthCfg{Workers: 4, Steps: 25, Anomalies: true})
	s := mustLoad(t, dir)
	sum := s.Summary()
	if sum.Totals.Requests != res.Requests || sum.Totals.Main != res.Main || sum.Totals.Side != res.Side {
		t.Errorf("requests/main/side = %d/%d/%d, want %d/%d/%d", sum.Totals.Requests, sum.Totals.Main, sum.Totals.Side, res.Requests, res.Main, res.Side)
	}
	u := res.Usage
	if sum.Totals.Input != int64(u.InputTokens) || sum.Totals.CacheRead != int64(u.CacheReadTokens) || sum.Totals.CacheWrite != int64(u.CacheWriteTokens()) || sum.Totals.Output != int64(u.OutputTokens) {
		t.Errorf("token totals %+v, want %+v", sum.Totals, u)
	}
	near(t, "reported cost", sum.Cost.Reported, res.ReportedUSD)
	// The synthetic session is priced exactly as the table prices it.
	near(t, "priced equals reported", sum.Cost.Actual.Total, res.ReportedUSD)
	if sum.Compaction.Commits != res.Commits || sum.Anomalies.Drift != res.Drift || sum.Anomalies.LowHit != res.LowHit {
		t.Errorf("commits/drift/low = %d/%d/%d, want %d/%d/%d", sum.Compaction.Commits, sum.Anomalies.Drift, sum.Anomalies.LowHit, res.Commits, res.Drift, res.LowHit)
	}
	var reqs, mainN, sideN, commits int
	var read, write, in, out int64
	var usd float64
	for _, a := range s.Agents() {
		reqs += a.Requests
		mainN += a.Main
		sideN += a.Side
		commits += a.Compactions
		read += a.CacheRead
		write += a.CacheWrite
		in += a.Input
		out += a.Output
		usd += a.CostUSD
		if a.Main != res.PerAgentMain[a.ID] {
			t.Errorf("agent %s main requests = %d, want %d", a.ID, a.Main, res.PerAgentMain[a.ID])
		}
	}
	if reqs != sum.Totals.Requests || mainN != sum.Totals.Main || sideN != sum.Totals.Side || commits != sum.Compaction.Commits ||
		read != sum.Totals.CacheRead || write != sum.Totals.CacheWrite || in != sum.Totals.Input || out != sum.Totals.Output {
		t.Errorf("per-agent sums (%d reqs, %d commits, %d read) do not add up to the totals %+v", reqs, commits, read, sum.Totals)
	}
	near(t, "per-agent cost sums to the total", usd, sum.Cost.Reported)
	// Fan-out: every worker's first request read the shared prefix another agent had written.
	if sum.Cache.FirstRequests != 5 || sum.Cache.WarmFirst != 4 {
		t.Errorf("first/warm-first = %d/%d, want 5 first requests of which 4 read the shared prefix", sum.Cache.FirstRequests, sum.Cache.WarmFirst)
	}
	for id, r := range res.FirstRequestReads {
		if id != "mgr" && r == 0 {
			t.Errorf("worker %s first request read nothing", id)
		}
	}
	if sum.Cache.SteadyHitRatio < 0.8 || sum.Cache.SteadyHitRatio > 0.99 {
		t.Errorf("steady hit ratio = %.3f", sum.Cache.SteadyHitRatio)
	}
	if sum.Cost.SavedNoCache <= 0 || sum.Cost.NoCache.Total <= sum.Cost.Actual.Total {
		t.Errorf("a cache with an 80%% hit ratio must beat no cache: %+v", sum.Cost)
	}
}
