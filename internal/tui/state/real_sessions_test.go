package state

import (
	"strings"
	"testing"
	"time"
)

// The demo is the repository's own scripted team, through the real harness, against the mock endpoint: 14 agents, 52 requests, the
// shared prefix doing what it is for. Everything the State says about it is checked against the oracle, and then against what the
// demo is known to be.
func TestTheStateOfARealDemoSessionAgreesWithAnIndependentCount(t *testing.T) {
	log := demoLog(t)
	evs := decodeLog(t, log)
	o := readOracle(t, log, recordedPrices(evs))
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)

	// What the demo is: one manager, eight scouts, four writers, one reviewer; every agent is a real one that sent requests.
	roles := map[string]int{}
	for _, a := range sn.Agents {
		roles[a.Role]++
		if a.Requests == 0 || a.Status != StatusDone || a.Stack.Req == "" || a.Hits.Len() != a.Requests {
			t.Errorf("%s: %d requests, status %s, stack %q", a.ID, a.Requests, a.Status, a.Stack.Req)
		}
	}
	if want := map[string]int{"manager": 1, "scout": 8, "docs": 4, "reviewer": 1}; len(roles) != len(want) || roles["manager"] != 1 || roles["scout"] != 8 || roles["docs"] != 4 || roles["reviewer"] != 1 {
		t.Errorf("roles %v, want %v", roles, want)
	}
	if first := sn.Agents[0]; first.ID != "mgr" {
		t.Errorf("the manager comes first: %s", first.ID)
	}
	if !sn.Session.Ended || !sn.Session.Swarm || sn.Session.Goal == "" || sn.Session.Model != "demo-model" || sn.Session.SharedHash == "" {
		t.Errorf("session %+v", sn.Session)
	}

	// A non-zero shared prefix, and every worker found it: its first request joined a prefix that an earlier request had used.
	if len(sn.Prefixes) < 4 {
		t.Fatalf("%d prefixes: a manager, scouts, writers and a reviewer each have theirs", len(sn.Prefixes))
	}
	riders := 0
	for _, p := range sn.Prefixes {
		if p.Tokens <= 0 || p.SharedHash != sn.Session.SharedHash {
			t.Errorf("prefix %+v: no size, or not built on the shared layer %s", p, sn.Session.SharedHash)
		}
		riders += p.Riders
	}
	if riders != len(sn.Agents) || sn.Prefixes[0].Riders != 8 || sn.Prefixes[0].Role != "scout" {
		t.Errorf("the eight scouts ride one prefix, and every agent rides one: %d riders, the biggest %+v", riders, sn.Prefixes[0])
	}
	for _, a := range sn.Agents {
		if a.Requests >= 2 && a.Stack.SharedTokens == 0 {
			t.Errorf("%s: its second request shares nothing with its first", a.ID)
		}
		if a.ID != "mgr" && !a.Stack.Inherited {
			t.Errorf("%s did not join a prefix that was already in use", a.ID)
		}
		if a.Stack.ExpectedRead == 0 && a.Requests > 1 {
			t.Errorf("%s expected nothing from the cache", a.ID)
		}
	}

	// The savings, recomputed without any of the code under test: every cache read saved the difference of the two prices the
	// session recorded for its model (the demo's round numbers: $0.50 and $0.125 per million), so it is the reads times 0.375 per
	// million. The figure is at list price, and says so.
	want := float64(o.total.read) * (0.50 - 0.125) / 1e6
	sv := sn.Totals.Savings
	if !near(sv.SavedUSD, want) || sv.SavedUSD <= 0 || !sv.Complete() || !sv.Known() || sv.Assumption != "at list price" {
		t.Errorf("savings %+v, want %v saved, all of it priced", sv, want)
	}
	if m := sn.Models; len(m) != 1 || m[0].ID != "demo-model" || m[0].Source != "session" || !m[0].Known || m[0].Price.InputPerM != 0.5 || m[0].Price.CacheReadPerM != 0.125 || m[0].TTLSeconds != 300 || m[0].ContextTokens != 1_000_000 {
		t.Errorf("models %+v", m)
	}
	if hr := sn.Totals.HitRatio(); hr < 0.6 || hr > 1 {
		t.Errorf("the team shares one cache: a token-weighted hit ratio of %v", hr)
	}

	// Every agent has a TTL entry, from the TTL the session recorded; the prefixes have theirs.
	kinds := map[string]int{}
	for _, e := range sn.TTL {
		kinds[e.Kind]++
		if e.Default || e.TTLSeconds != 300 || e.Last.IsZero() {
			t.Errorf("ttl %+v: the session said five minutes", e)
		}
	}
	if kinds["agent"] != len(sn.Agents) || kinds["prefix"] != len(sn.Prefixes) {
		t.Errorf("ttl entries %v for %d agents and %d prefixes", kinds, len(sn.Agents), len(sn.Prefixes))
	}
	for _, e := range sn.TTLRemaining(sn.Clock) {
		if e.Cold || e.Remaining <= 0 {
			t.Errorf("at the end of a run of a second, %+v is cold", e)
		}
	}
	if cold := sn.TTLRemaining(sn.Clock.Add(301 * time.Second)); len(cold) == 0 || !cold[0].Cold {
		t.Errorf("five minutes later the cache is cold: %+v", cold)
	}
}

// Without the prices the session recorded, the State does not guess: the saving is flagged as unknown and the cache reads it could
// not price are counted, and the TTL is flagged as the default.
func TestADemoSessionWithoutItsPricesHasNoPricedSavings(t *testing.T) {
	log := rewriteLog(t, demoLog(t), func(e obj) {
		if e.str("type") == "session.start" {
			delete(e.sub("data"), "models")
		}
	})
	o := readOracle(t, log, nobody)
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)
	sv := sn.Totals.Savings
	if sv.SavedUSD != 0 || sv.PricedReadTokens != 0 || sv.UnpricedReadTokens != o.total.read || sv.UnpricedReadTokens == 0 || sv.Known() || sv.Complete() || sv.Assumption != SavingsAssumption {
		t.Errorf("savings %+v: nothing may be guessed for a model nobody has a price for", sv)
	}
	for _, a := range sn.Agents {
		if a.SavedUSD != 0 {
			t.Errorf("%s saved %v of nothing", a.ID, a.SavedUSD)
		}
	}
	if m := sn.Models; len(m) != 1 || m[0].ID != "demo-model" || m[0].Known || m[0].Source != "unknown" {
		t.Errorf("models %+v: the model is flagged as one whose price is unknown", m)
	}
	for _, e := range sn.TTL {
		if !e.Default || e.TTLSeconds != int(DefaultTTL.Seconds()) {
			t.Errorf("ttl %+v: the log did not say, so the five minute default applies, and is flagged", e)
		}
	}
}

// A model that is in the repository's price table is priced from it, by id: the same session as a model of the table, with no
// prices of its own recorded.
func TestADemoSessionOfATableModelIsPricedFromTheTable(t *testing.T) {
	log := rewriteLog(t, demoLog(t), func(e obj) {
		renameModel(e, "demo-model", "mock-1")
		if e.str("type") == "session.start" {
			delete(e.sub("data"), "models")
		}
	})
	o := readOracle(t, log, tablePrices())
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)
	want := float64(o.total.read) * (4 - 0.4) / 1e6 // mock-1 in internal/cost/defaults.go
	sv := sn.Totals.Savings
	if !near(sv.SavedUSD, want) || sv.SavedUSD <= 0 || !sv.Complete() {
		t.Errorf("savings %+v, want %v", sv, want)
	}
	if m := sn.Models; len(m) != 1 || m[0].ID != "mock-1" || m[0].Source != "table" || !m[0].Known || m[0].Price.InputPerM != 4 || m[0].Price.CacheReadPerM != 0.4 || m[0].ContextTokens != 1_000_000 {
		t.Errorf("models %+v", m)
	}
}

// renameModel changes every string of the event that is the model's id.
func renameModel(v any, from, to string) {
	switch x := v.(type) {
	case obj:
		for k, el := range x {
			if s, ok := el.(string); ok && s == from {
				x[k] = to
				continue
			}
			renameModel(el, from, to)
		}
	case map[string]any:
		renameModel(obj(x), from, to)
	case []any:
		for i, el := range x {
			if s, ok := el.(string); ok && s == from {
				x[i] = to
				continue
			}
			renameModel(el, from, to)
		}
	}
}

// The rich session has what the demo has not: mail, compaction, a stuck agent, a refused request, a cold cache, a manager that is
// held to its board. The recording itself says it is not vacuous.
func TestTheStateOfARealRichSessionAgreesWithAnIndependentCount(t *testing.T) {
	log := richLog(t)
	evs := decodeLog(t, log)
	o := readOracle(t, log, recordedPrices(evs))
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)

	// What the script made happen must have happened, or the checks above compared nothing. (The compactor model's own requests are
	// not on the list: a fork compaction is a background job that the harness's safety net, an emergency compaction, can get ahead
	// of, and under load it does; the State counts them all the same, and the oracle checks it.)
	for what, n := range map[string]int{
		"compactions": sum(o.commits), "cache anomalies": sum(o.anomalies), "stuck nudges": sum(o.nudges), "stuck stops": sum(o.stops),
		"mail sent": o.sent, "mail delivered": o.delivered, "retries": o.retries, "rate limits": o.rateLimits,
		"governor events": o.count["governor"], "manager holds": o.count["swarm.hold"], "unfinished": o.count["swarm.unfinished"],
		"leases": o.count["lease"],
	} {
		if n == 0 {
			t.Errorf("the rich session has no %s: the script no longer makes them happen", what)
		}
	}

	// The session ran the model in a window of richWindow tokens, which is what session.start records for it (the window the harness
	// compacts against, not the model's own million), and what a context gauge is drawn against.
	if m := sn.Models; len(m) != 1 || m[0].ID != "rich-model" || m[0].ContextTokens != richWindow || m[0].Price.InputPerM != richPrices.InputPerM {
		t.Errorf("models %+v", m)
	}

	// The scout that kept reading compacted; each compaction says what it was before and after, and how.
	sc, _ := sn.Agent("sc-1")
	if sc.Compactions == 0 || len(sc.Compacts) == 0 || len(sc.Hits.Marks) == 0 {
		t.Fatalf("sc-1 did not compact: %+v", sc.Compacts)
	}
	if f, ok := sn.ContextFill(sc); !ok || f <= 0.1 || f > 1 {
		t.Errorf("the scout's prompt fills %v of a window of %d tokens (%v)", f, richWindow, ok)
	}
	for _, c := range sc.Compacts {
		// The window is small enough that the harness's safety net (an emergency compaction, which has no plan and no choice of
		// moment, and squeezes the bulky results it folds) usually gets there before the compactor model's patch; when the patch
		// lands first it is a fork compaction, which the planner timed, and the compactor here has no answer, so it is the harness's
		// mechanical patch. Which of the two comes first is a matter of timing, and either is a compaction.
		ok := c.Before > c.After && c.After > 0 && c.Fallback && c.Reason != ""
		switch c.Mode {
		case "emergency":
			ok = ok && c.Moment == "unknown" && c.Squeezed > 0 && c.SqueezedTokens > 0
		case "fork":
			ok = ok && (c.Moment == "warm" || c.Moment == "cold")
		default:
			ok = false
		}
		if !ok {
			t.Errorf("a compaction of %+v", c)
		}
		if c.At > sc.Hits.Len() {
			t.Errorf("a compaction at %d of %d requests", c.At, sc.Hits.Len())
		}
	}
	marks := map[MarkKind]int{}
	for _, m := range sc.Hits.Marks {
		marks[m.Kind]++
	}
	if marks[MarkCompaction] != sc.Compactions {
		t.Errorf("%d compaction markers for %d compactions: %v", marks[MarkCompaction], sc.Compactions, marks)
	}
	if !feedHas(sn, FeedCompact, "sc-1", "compacted") {
		t.Error("the feed does not mention the compaction")
	}

	// The scout that repeated a failing call: a nudge first and then its run was stopped, which failed it and put its task back.
	fl, _ := sn.Agent("sc-2")
	if fl.Stuck.Nudges != 1 || fl.Stuck.Stops != 1 || fl.Stuck.Phase != "stop" || fl.Status != StatusError || fl.EndState != "failed" || fl.ToolErrors != 8 {
		t.Errorf("sc-2: %+v status %s end %q", fl.Stuck, fl.Status, fl.EndState)
	}
	if tk := taskOf(t, sn, "T2"); tk.Status != "todo" || tk.Attempts != 1 || tk.State != TaskTodo {
		t.Errorf("T2 went back to the pool: %+v", tk)
	}
	if !feedHas(sn, FeedStuck, "sc-2", "") {
		t.Error("the feed does not mention the stuck agent")
	}

	// The refused request: one retry of the rate limit kind, a governor episode that paused admission, none of it an error.
	if sn.Totals.Retries != 1 || sn.Totals.RateLimited != 1 || sn.Totals.Errors != 0 || sn.Governor.Episodes != 1 || sn.Governor.RateLimited != 1 || sn.Governor.PauseUntil.IsZero() {
		var errs []string
		for _, l := range sn.Feed {
			if l.Kind == FeedError {
				errs = append(errs, l.Agent+": "+l.Text)
			}
		}
		t.Errorf("totals %+v governor %+v; errors in the feed: %q", sn.Totals, sn.Governor, errs)
	}
	if !feedHas(sn, FeedRetry, "", "rate_limit") {
		t.Error("the feed does not mention the retry")
	}

	// The cold cache: the anomaly names the request, what was expected and what was read, and what the miss cost at list price.
	if len(sn.Anomalies) == 0 {
		t.Fatal("no anomaly")
	}
	for _, an := range sn.Anomalies {
		if an.Kind != "low_hit" || an.Actual >= an.Expected || an.Missed <= 0 || an.Req == "" || !an.MissKnown || an.MissUSD <= 0 {
			t.Errorf("anomaly %+v", an)
		}
		if a, _ := sn.Agent(an.Agent); len(a.Anomalies) == 0 || !anyMarkOf(a, MarkAnomaly) {
			t.Errorf("%s has an anomaly in the log and none on its own history", an.Agent)
		}
	}

	// The mail: the writer's two notes and the harness's, all delivered, the lane of the writer marked.
	if m := sn.Mail.Counts; m.Sent != 3 || m.Delivered != 3 || len(sn.Mail.Recent) != 3 {
		t.Errorf("mail %+v", sn.Mail)
	}
	for _, m := range sn.Mail.Recent {
		if m.Stage != MailDelivered || m.Tokens <= 0 || m.From == "" || m.To == "" || m.Delivered.IsZero() {
			t.Errorf("message %+v", m)
		}
	}
	if !anyMark(rowOf(t, sn, "dc-1"), ActMail) || !anyMark(rowOf(t, sn, "sc-2"), ActStuck) || !anyMark(rowOf(t, sn, "sc-1"), ActCompact) {
		t.Error("the gantt does not mark the mail, the stuck agent and the compaction")
	}

	// The writer took the file its task names while it worked on it, and gave it up when its task was handed in: the lease is
	// there at the moment of the acquire, in the table and on the agent, by its path in the project and not by the one the
	// harness wrote, and it is gone at the end.
	if l := sn.Leases; len(l.Held) != 0 {
		t.Errorf("leases still held at the end: %+v", l.Held)
	}
	var acquired uint64
	for _, e := range evs {
		if e.str("type") == "lease" && e.sub("data").str("action") == "acquire" {
			acquired = uint64(e.num("seq"))
		}
	}
	mid, err := FoldUntil(writeLog(t, string(log)), acquired)
	if err != nil {
		t.Fatal(err)
	}
	msn := mid.Snapshot()
	dc, _ := msn.Agent("dc-1")
	if h := msn.Leases.Held; len(h) != 1 || h[0].Agent != "dc-1" || h[0].Path != "summaries/notes.md" || h[0].Seq != acquired ||
		len(dc.Leases) != 1 || dc.Leases[0] != "summaries/notes.md" || len(dc.Scope) != 1 || dc.Scope[0] != "summaries/notes.md" {
		t.Errorf("at the acquire: %+v, dc-1 leases %v scope %v", msn.Leases.Held, dc.Leases, dc.Scope)
	}

	// The manager was held to its board three times, and the run ended with work unfinished.
	if s := sn.Supervision; s.Holds != 3 || s.Unfinished != 1 || !strings.Contains(s.LastHold, "T2") {
		t.Errorf("supervision %+v", s)
	}
	if mg := sn.Agents[0]; mg.ID != "mgr" || mg.Status != StatusDone {
		t.Errorf("manager %+v", mg)
	}
}

func anyMarkOf(a Agent, k MarkKind) bool {
	for _, m := range a.Hits.Marks {
		if m.Kind == k {
			return true
		}
	}
	return false
}

func taskOf(t testing.TB, sn *Snapshot, id string) Task {
	t.Helper()
	for _, tk := range sn.Board.Tasks {
		if tk.ID == id {
			return tk
		}
	}
	t.Fatalf("no task %s", id)
	return Task{}
}

// The isolated session has the merge queue: seven submissions that merge, one of which bounces on a conflict and one on the
// verifier, after which they merge too.
func TestTheStateOfARealIsolatedSessionAgreesWithAnIndependentCount(t *testing.T) {
	log := isoLog(t)
	evs := decodeLog(t, log)
	o := readOracle(t, log, recordedPrices(evs))
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)

	m := sn.Merge
	if !m.Seen || m.Counts.Queued != 9 || m.Counts.Merged != 7 || m.Counts.Conflicts != 1 || m.Counts.VerifyFail != 1 || m.Counts.RolledBack != 1 || m.Counts.Bounced != 2 || len(m.Waiting) != 0 {
		t.Errorf("merge %+v", m)
	}
	if m.Trees != 0 {
		t.Errorf("%d trees alive at the end: every one was removed", m.Trees)
	}
	if in := m.Integration; in == nil || !in.Applied || in.Committed || in.Files != 6 || in.Branch == "" || in.Tip == "" {
		t.Errorf("integration %+v", in)
	}
	stages := map[MergeStage]int{}
	for _, e := range m.Recent {
		stages[e.Stage]++
		if e.Task == "" || e.Agent == "" || e.Title == "" || e.Seq == 0 {
			t.Errorf("entry %+v", e)
		}
		switch e.Stage {
		case MergeMerged:
			if len(e.Commit) != 12 || len(e.Files) == 0 {
				t.Errorf("merged entry %+v", e)
			}
		case MergeConflict:
			if e.Reason == "" || len(e.Files) == 0 {
				t.Errorf("conflict entry %+v", e)
			}
		case MergeVerifyFail:
			if e.ExitCode == nil || *e.ExitCode == 0 || e.Reason == "" {
				t.Errorf("verifier entry %+v", e)
			}
		}
	}
	if stages[MergeMerged] != 7 || stages[MergeConflict] != 1 || stages[MergeVerifyFail] != 1 {
		t.Errorf("settled submissions %v", stages)
	}
	for _, tk := range sn.Board.Tasks {
		if tk.State != TaskMerged || tk.Merge != "merged" || len(tk.Commit) != 12 {
			t.Errorf("task %+v: every task ended merged, with the commit that holds it", tk)
		}
	}
	if sn.Session.Isolation != "worktree" || sn.Board.Counts.Merged != 7 {
		t.Errorf("isolation %q, columns %+v", sn.Session.Isolation, sn.Board.Counts)
	}
	ids := agentIDs(sn)
	if ids[0] != "mgr" || ids[1] != "be-1" || ids[7] != "be-7" {
		t.Errorf("the manager, then the workers by number: %v", ids)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "_") {
			t.Errorf("%s is the harness's own tree, not an agent", id)
		}
	}
}

// The mailman session has the mode that digests the workers' mail for the manager: twelve messages are routed to the mailman's
// ledger, handed to it in a batch, and reach the manager as one digest that stands for them all.
func TestTheStateOfARealMailmanSessionAgreesWithAnIndependentCount(t *testing.T) {
	log := mailmanLog(t)
	evs := decodeLog(t, log)
	o := readOracle(t, log, recordedPrices(evs))
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)

	for what, n := range map[string]int{"routed messages": o.count["mail.route"], "batches": o.count["mail.batch"], "digests": o.count["mail.digest"], "parcels": o.parcels} {
		if n == 0 {
			t.Errorf("the mailman session has no %s: the script no longer makes them happen", what)
		}
	}
	// Twelve messages are sent, each routed to the mailman or (if it could not take it) delivered directly; the mailman writes one
	// message per recipient and batch.
	if o.count["mail.route"]+o.direct != 12 || o.sent != 12+o.count["mail.digest"] {
		t.Errorf("twelve messages sent (%d routed, %d direct) and a digest of each batch (%d sends, %d digests)", o.count["mail.route"], o.direct, o.sent, o.count["mail.digest"])
	}

	// The mailman is an agent of the harness, not of the team: a service, last in the table, never the one the screens focus on.
	mm, ok := sn.Agent("mm-1")
	if !ok || !mm.Service || mm.Role != "mailman" || sn.Agents[len(sn.Agents)-1].ID != "mm-1" || sn.Main() != "mgr" || sn.Cycle("mgr", 1) == "mm-1" || sn.Cycle("mgr", -1) == "mm-1" {
		t.Errorf("mailman %+v, main %q, agents %v", mm, sn.Main(), agentIDs(sn))
	}
	if !sn.Session.Mailman || sn.Session.Isolation != "" {
		t.Errorf("session %+v", sn.Session)
	}
	if mm.Requests == 0 || mm.Hits.Len() != mm.Requests {
		t.Errorf("the mailman is a model too: %+v", mm.Hits)
	}

	// Every parcel the digests stand for is marked as digested, and the digest itself was delivered to the manager by the mailman.
	digested, digests := 0, 0
	for _, m := range sn.Mail.Recent {
		switch {
		case m.Stage == MailDigested:
			digested++
			if m.To != "mgr" || m.From == "mm-1" {
				t.Errorf("parcel %+v", m)
			}
		case m.Via == "mm-1":
			digests++
			if m.From != "mm-1" || m.To != "mgr" || m.Stage != MailDelivered || len(m.Origins) == 0 || m.Tokens <= 0 || m.Delivered.IsZero() {
				t.Errorf("digest %+v", m)
			}
		}
	}
	if digested != o.parcels || digests != o.count["mail.digest"] {
		t.Errorf("%d parcels digested and %d digests seen; the log has %d and %d", digested, digests, o.parcels, o.count["mail.digest"])
	}
	if !feedHas(sn, FeedMail, "mm-1", "(digest)") || !feedHas(sn, FeedMail, "be-1", "progress be-1") {
		t.Error("the feed does not show the workers' messages and the mailman's digest")
	}
	if !anyMark(rowOf(t, sn, "be-1"), ActMail) || !anyMark(rowOf(t, sn, "mgr"), ActMail) {
		t.Error("the gantt does not mark the mail")
	}
}
