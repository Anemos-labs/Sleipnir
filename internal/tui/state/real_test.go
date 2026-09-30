package state

import (
	"math"
	"testing"
)

// foldBytes folds the bytes of a log the way the program folds a log on disk: through a file and Fold.
func foldBytes(t testing.TB, log []byte) *State {
	t.Helper()
	st, err := Fold(writeLog(t, string(log)))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return st
}

func tally2(a Tokens) tally { return tally{a.Input, a.CacheRead, a.CacheWrite, a.Output} }

// checkAgainstOracle compares what the State made of a log with what the oracle counted in it, everywhere the two have a figure in
// common. Every figure it compares is one a screen shows.
func checkAgainstOracle(t *testing.T, sn *Snapshot, o *oracle) {
	t.Helper()
	bad := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format, args...)
	}

	// What the State did with the events.
	if s := sn.Stats; s.Events != o.events || s.Stale != 0 || s.Unknown != 0 || s.Bad != 0 || s.TooBig != 0 || s.Panics != 0 || s.Corrupt != 0 || s.Dropped != 0 {
		bad("stats %+v for %d events: a recorded log is all understood, and nothing is dropped", s, o.events)
	}

	// The agents: exactly those that were spawned, with their roles (a session of one agent has no spawn: its agent is the one that
	// wrote the events).
	if want := len(o.spawn); len(sn.Agents) != want && !(want == 0 && len(sn.Agents) == len(o.actors)) {
		bad("%d agents, %d were spawned (%v), %d wrote events", len(sn.Agents), want, agentIDs(sn), len(o.actors))
	}
	for _, id := range o.spawn {
		a, ok := sn.Agent(id)
		if !ok {
			bad("agent %s was spawned and is not there", id)
			continue
		}
		if a.Role != o.role[id] {
			bad("%s: role %q, spawned as %q", id, a.Role, o.role[id])
		}
	}

	// Each agent's counters.
	for _, a := range sn.Agents {
		id := a.ID
		if a.Requests != o.mainResp[id] || a.Hits.Len() != a.Requests || len(a.Hits.Ratios) != min(a.Requests, HistCap) {
			bad("%s: %d requests, history %d (%d kept), the log has %d main responses", id, a.Requests, a.Hits.Len(), len(a.Hits.Ratios), o.mainResp[id])
		}
		if a.SideRequests != o.sideResp[id] {
			bad("%s: %d side requests, the log has %d", id, a.SideRequests, o.sideResp[id])
		}
		want := tally{}
		if tk := o.tok[id]; tk != nil {
			want = *tk
		}
		if got := tally2(a.Tokens); got != want {
			bad("%s: tokens %+v, the log sums to %+v", id, got, want)
		}
		if !near(a.CostUSD, o.costBy[id]) || !near(a.SavedUSD, o.savedOf[id]) {
			bad("%s: cost %v saved %v, the log says %v and %v", id, a.CostUSD, a.SavedUSD, o.costBy[id], o.savedOf[id])
		}
		if a.ToolCalls != o.calls[id] || a.ToolErrors != o.callErrors[id] {
			bad("%s: %d tool calls, %d failed; the log has %d and %d", id, a.ToolCalls, a.ToolErrors, o.calls[id], o.callErrors[id])
		}
		if a.Compactions != o.commits[id] || len(a.Compacts) != min(o.commits[id], CompactCap) {
			bad("%s: %d compactions (%d kept), the log has %d", id, a.Compactions, len(a.Compacts), o.commits[id])
		}
		if len(a.Anomalies) != min(o.anomalies[id], AnomalyCap) {
			bad("%s: %d anomalies kept, the log has %d", id, len(a.Anomalies), o.anomalies[id])
		}
		if a.Stuck.Nudges != o.nudges[id] || a.Stuck.Stops != o.stops[id] {
			bad("%s: stuck %d nudges %d stops, the log has %d and %d", id, a.Stuck.Nudges, a.Stuck.Stops, o.nudges[id], o.stops[id])
		}
		if n := len(a.Hits.Ratios); n > 0 && !near(a.Hits.Ratios[n-1], o.lastRatio[id]) {
			bad("%s: its newest hit ratio is %v, the log's is %v", id, a.Hits.Ratios[n-1], o.lastRatio[id])
		}
	}

	// The totals.
	tt := sn.Totals
	if tt.Requests != o.count["model.request"] || tt.Responses != o.count["model.response"] || tt.Main+tt.Side != tt.Requests ||
		tt.Main != sum(o.mainReq) || tt.Side != tt.Requests-sum(o.mainReq) {
		bad("totals: %d requests (%d main), %d responses; the log has %d, %d and %d", tt.Requests, tt.Main, tt.Responses,
			o.count["model.request"], sum(o.mainReq), o.count["model.response"])
	}
	if got := tally2(tt.Tokens); got != o.total {
		bad("totals: tokens %+v, the log sums to %+v", got, o.total)
	}
	if o.total.prompt() > 0 {
		want := float64(o.total.read) / float64(o.total.prompt())
		if !near(tt.HitRatio(), want) {
			bad("the token-weighted hit ratio is %v, the log's is %v", tt.HitRatio(), want)
		}
	}
	if !near(tt.CostUSD, o.totalCost) {
		bad("totals: cost %v, the log sums to %v", tt.CostUSD, o.totalCost)
	}
	sv := tt.Savings
	if !near(sv.SavedUSD, o.saved) || sv.PricedReadTokens != o.priced || sv.UnpricedReadTokens != o.unpriced || sv.Assumption != SavingsAssumption {
		bad("savings %+v, the oracle finds %v saved on %d read tokens and %d unpriced", sv, o.saved, o.priced, o.unpriced)
	}
	sumSaved := 0.0
	for _, a := range sn.Agents {
		sumSaved += a.SavedUSD
	}
	if !near(sumSaved, sv.SavedUSD) {
		bad("the agents' savings add up to %v, the total says %v", sumSaved, sv.SavedUSD)
	}
	if tt.ToolCalls != sum(o.calls) || tt.ToolErrors != sum(o.callErrors) || tt.Compactions != sum(o.commits) || tt.Anomalies != sum(o.anomalies) {
		bad("totals: %d tool calls, %d failed, %d compactions, %d anomalies; the log has %d, %d, %d and %d", tt.ToolCalls, tt.ToolErrors,
			tt.Compactions, tt.Anomalies, sum(o.calls), sum(o.callErrors), sum(o.commits), sum(o.anomalies))
	}
	if tt.Retries != o.retries || tt.RateLimited != o.rateLimits || tt.Errors != o.errors || tt.Cancelled != o.cancelled {
		bad("totals: %d retries (%d rate limits), %d errors, %d cancelled; the log has %d, %d, %d and %d", tt.Retries, tt.RateLimited, tt.Errors,
			tt.Cancelled, o.retries, o.rateLimits, o.errors, o.cancelled)
	}

	// Runs that were cancelled (agent.cancel), each agent's latest by phase and cause.
	if tt.RunsCancelled != sum(o.cancels) {
		bad("totals: %d runs cancelled, the log has %d", tt.RunsCancelled, sum(o.cancels))
	}
	for _, a := range sn.Agents {
		if a.Cancel.Count != o.cancels[a.ID] || a.Cancel.Phase != o.lastCancelPhase[a.ID] || a.Cancel.Cause != o.lastCancelCause[a.ID] {
			bad("%s: cancel %+v, the log has %d and the latest was %q (%q)", a.ID, a.Cancel, o.cancels[a.ID], o.lastCancelPhase[a.ID], o.lastCancelCause[a.ID])
		}
	}

	// The permission questions: what was asked, how it was settled and by whom, and that nothing is left waiting in a log that ended.
	if pm := sn.Perms; pm.Asked != o.asked || pm.Allowed != o.allowed || pm.Denied != o.denied || pm.ByUser != o.permBy[PermByUser] ||
		pm.ByNoOne != o.permBy[PermByNoOne] || pm.ByPolicy != o.permBy[PermByPolicy] || pm.Canceled != o.permBy[PermByCanceled] ||
		len(pm.Pending)+pm.Abandoned != o.unanswered || len(pm.Recent) != min(o.allowed+o.denied, PermLog) {
		bad("perms %s; the log has %d asked, %d allowed, %d denied, by %v, %d never answered", js(sn.Perms), o.asked, o.allowed, o.denied, o.permBy, o.unanswered)
	}
	for _, a := range sn.Agents {
		if a.Asking != 0 {
			bad("%s is still asking %d questions at the end of the log", a.ID, a.Asking)
		}
	}

	// Mail.
	if m := sn.Mail.Counts; m.Sent != o.sent || m.Delivered != o.delivered || m.Routed != o.count["mail.route"] || m.Digests != o.count["mail.digest"] ||
		m.Batches != o.count["mail.batch"] || m.Direct != o.direct || m.Ignored != o.count["mail.drop"] || m.Acked != o.count["mail.ack"] {
		bad("mail %+v; the log has %d sent, %d delivered, %d routed, %d digests, %d batches, %d direct", m, o.sent, o.delivered,
			o.count["mail.route"], o.count["mail.digest"], o.count["mail.batch"], o.direct)
	}

	// The board: every task ended where the board's last word on it says.
	if len(sn.Board.Tasks) != len(o.tasks) {
		bad("the board has %d tasks, the log names %d", len(sn.Board.Tasks), len(o.tasks))
	}
	for _, tk := range sn.Board.Tasks {
		if want, ok := o.tasks[tk.ID]; !ok || tk.Status != want {
			bad("task %s is %q, the log's last word on it is %q", tk.ID, tk.Status, want)
		}
	}
	c := sn.Board.Counts
	if c.Todo+c.Running+c.Verifying+c.Merged+c.Failed != len(sn.Board.Tasks) {
		bad("the columns %+v do not add up to the %d tasks", c, len(sn.Board.Tasks))
	}

	// The merge queue.
	if m := sn.Merge.Counts; m.Queued != o.merge["merge.queued"] || m.Merged != o.merge["merge.merged"] || m.Conflicts != o.merge["merge.conflict"] ||
		m.VerifyFail != o.merge["merge.verify_failed"] || m.RolledBack != o.merge["merge.rolled_back"] {
		bad("merge counts %+v; the log has %v", m, o.merge)
	}

	// Supervision and the governor.
	if s := sn.Supervision; s.Holds != o.count["swarm.hold"] || s.Unfinished != o.count["swarm.unfinished"] {
		bad("supervision %+v; the log has %d holds and %d unfinished", s, o.count["swarm.hold"], o.count["swarm.unfinished"])
	}
	if sn.Governor.Episodes != o.count["governor"] {
		bad("%d governor episodes, the log has %d governor events", sn.Governor.Episodes, o.count["governor"])
	}

	// Every agent has a lane of the gantt and every cell of it is a level the screen can draw.
	if len(sn.Activity.Rows) != len(sn.Agents) {
		bad("%d activity rows for %d agents", len(sn.Activity.Rows), len(sn.Agents))
	}
	for _, r := range sn.Activity.Rows {
		if len(r.Levels) != ActivitySeconds || len(r.Marks) != ActivitySeconds {
			bad("%s: a lane of %d levels and %d marks", r.Agent, len(r.Levels), len(r.Marks))
		}
		for _, v := range r.Levels {
			if v > 8 {
				bad("%s: level %d", r.Agent, v)
				break
			}
		}
	}

	// Whatever was folded, the tables are inside their caps and everything in the snapshot is a number.
	checkSnapshotSane(t, sn)
}

// checkSnapshotSane fails on a figure no screen should ever be handed: a number that is not one, or one that cannot be negative.
func checkSnapshotSane(t *testing.T, sn *Snapshot) {
	t.Helper()
	fin := func(what string, f float64) {
		t.Helper()
		if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			t.Errorf("%s is %v", what, f)
		}
	}
	fin("total cost", sn.Totals.CostUSD)
	fin("saved", sn.Totals.Savings.SavedUSD)
	fin("hit ratio", sn.Totals.HitRatio())
	for _, a := range sn.Agents {
		fin(a.ID+" cost", a.CostUSD)
		fin(a.ID+" saved", a.SavedUSD)
		for _, r := range a.Hits.Ratios {
			if math.IsNaN(r) || r < 0 || r > 1 {
				t.Errorf("%s: a hit ratio of %v", a.ID, r)
			}
		}
		if a.Stack.Unsectioned < 0 || a.Stack.Prompt < 0 || a.Stack.Read < 0 {
			t.Errorf("%s: a negative size in its stack: %+v", a.ID, a.Stack)
		}
		if a.Stack.Answered && a.Stack.Prompt < a.Stack.SectionTokens && a.Stack.Unsectioned != 0 {
			t.Errorf("%s: %d tokens of sections in a prompt of %d, and %d left over", a.ID, a.Stack.SectionTokens, a.Stack.Prompt, a.Stack.Unsectioned)
		}
		for _, c := range a.Compacts {
			if c.Before < 0 || c.After < 0 || c.Moment == "" || c.Mode == "" {
				t.Errorf("%s: a compaction of %+v", a.ID, c)
			}
		}
	}
}
