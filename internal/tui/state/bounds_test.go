package state

import (
	"encoding/json"
	"testing"
)

// checkBounds fails the test if any ring, table or list of the State, or of the snapshot made from it, is longer than the cap the
// package documents (limits.go). It looks at the State's own tables, not at bytes: a bound on a length is a bound on memory.
func checkBounds(t testing.TB, st *State) {
	t.Helper()
	st.mu.RLock()
	defer st.mu.RUnlock()
	over := func(what string, n, limit int) {
		t.Helper()
		if n > limit {
			t.Errorf("%s holds %d, over its bound of %d", what, n, limit)
		}
	}
	over("agents", len(st.agents), MaxAgents)
	over("tasks", len(st.tasks), MaxTasks)
	over("feed", st.feed.len(), FeedCap)
	over("mail ring", st.mail.ring.len(), MailCap)
	over("mail id index", len(st.mail.byID), MailCap)
	over("merge recent", st.merge.recent.len(), MergeCap)
	over("merge waiting", len(st.merge.waiting), MaxMergeWaiting)
	over("leases", len(st.leases.held), MaxLeases)
	over("prefixes", len(st.prefixes), MaxPrefixes)
	over("shared layers", len(st.shared), MaxPrefixes)
	over("models", len(st.models), MaxModels)
	over("notes", len(st.board.notes), MaxNotes)
	over("alerts", len(st.board.alerts), MaxAlerts)
	over("pending permissions", len(st.perms.pending), MaxPending)
	over("answered permissions", st.perms.recent.len(), PermLog)
	over("anomaly log", st.anoms.len(), AnomalyLog)
	over("unknown type names", len(st.stats.UnknownTypes), MaxUnknownTypes)
	// What each agent says it waits on is what the table of questions says, or less (a question whose agent was not tracked when it
	// was put is not counted for an agent that is tracked later), and an agent that waits on one is at work.
	waits := map[string]int{}
	for _, q := range st.perms.pending {
		waits[q.Agent]++
		over("paths of a question", len(q.Paths), MaxPermPaths)
	}
	for id, a := range st.agents {
		if a.asks < 0 || a.asks > waits[id] || a.Asking != a.asks {
			t.Errorf("%s waits on %d questions (shown %d), the table has %d", id, a.asks, a.Asking, waits[id])
		}
		if a.asks > 0 && (a.run == runIdle || a.run == runDone || a.run == runError || a.Status != StatusAsking) {
			t.Errorf("%s waits on %d questions and is %s (run %d)", id, a.asks, a.Status, a.run)
		}
		if a.Status == StatusAsking && a.asks == 0 {
			t.Errorf("%s is asking and waits on nothing", id)
		}
	}
	for id, a := range st.agents {
		over(id+" hits", a.hits.len(), HistCap)
		over(id+" marks", a.marks.len(), MarkCap)
		over(id+" compactions", a.compacts.len(), CompactCap)
		over(id+" anomalies", a.anoms.len(), AnomalyCap)
		over(id+" open tools", len(a.tools), MaxOpenTools)
		over(id+" open requests", len(a.reqs), MaxOpenRequests)
		over(id+" leases", len(a.leaseOrder), MaxLeasesPerAgent)
		over(id+" sections", len(a.Stack.Sections), MaxSections)
		over(id+" breakpoints", len(a.Stack.Breakpoints), MaxBreakpoints)
	}
	for k, g := range st.prefixes {
		over("riders of "+k, len(g.riders), MaxRiders)
		for id := range g.riders {
			if st.agents[id] == nil {
				t.Errorf("prefix %s has a rider, %s, that is not an agent", k, id)
			}
		}
	}
	orphans := 0
	for path, l := range st.leases.held {
		if a := st.agents[l.Agent]; a == nil {
			if orphans++; orphans <= 3 {
				t.Errorf("lease %s is held by %s, who is not an agent", path, l.Agent)
			}
		}
	}
	unlisted := 0
	for id, a := range st.agents {
		for _, p := range a.leaseOrder {
			if l, ok := st.leases.held[p]; !ok || l.Agent != id {
				if unlisted++; unlisted <= 3 {
					t.Errorf("%s lists a lease on %s that the table does not give it", id, p)
				}
			}
		}
	}
	held := map[string]int{}
	for _, l := range st.leases.held {
		held[l.Agent]++
	}
	for id, n := range held {
		if a := st.agents[id]; a != nil && n != len(a.leaseOrder) {
			t.Errorf("%s holds %d leases in the table and lists %d", id, n, len(a.leaseOrder))
			break
		}
	}
}

// The State stays inside its caps over a log of two hundred thousand events (sixty thousand under the race detector, which makes the
// fold five times slower, and twenty thousand with -short: enough of each to fill the rings and tables that fill first).
func TestStateStaysBoundedOverALongLog(t *testing.T) {
	n := 200_000
	switch {
	case testing.Short():
		n = 20_000
	case raceEnabled:
		n = 60_000
	}
	evs := genEvents(n, 99)
	st := New()
	checkpoints := map[int]bool{n / 4: true, n / 2: true, n - 1: true}
	for i, e := range evs {
		st.Apply(e)
		if checkpoints[i] {
			checkBounds(t, st)
		}
	}
	s := st.Stats()
	if s.Panics != 0 || s.Events != n {
		t.Fatalf("stats %+v, %s", s, s.LastPanic)
	}
	sn := st.Snapshot()
	// The rings are full, which proves the log was long enough to fill them: the test is not vacuous. (The shorter log of -short
	// fills the tables that fill fastest, and the ones that need the whole log are only required to be started.)
	if len(sn.Agents) != MaxAgents || len(sn.Feed) != FeedCap || len(sn.Mail.Recent) != MailCap {
		t.Errorf("agents %d feed %d mail %d", len(sn.Agents), len(sn.Feed), len(sn.Mail.Recent))
	}
	if n >= 60_000 && len(sn.Board.Tasks) != MaxTasks || len(sn.Board.Tasks) == 0 {
		t.Errorf("tasks %d", len(sn.Board.Tasks))
	}
	if len(sn.Anomalies) != AnomalyLog || len(sn.Models) == 0 || len(sn.Prefixes) == 0 || s.Dropped == 0 {
		t.Errorf("anomalies %d models %d prefixes %d dropped %d", len(sn.Anomalies), len(sn.Models), len(sn.Prefixes), s.Dropped)
	}
	if len(sn.Merge.Recent) == 0 || len(sn.Leases.Held) == 0 || len(sn.TTL) == 0 {
		t.Errorf("merge %d leases %d ttl %d", len(sn.Merge.Recent), len(sn.Leases.Held), len(sn.TTL))
	}
	full := 0
	for _, a := range sn.Agents {
		if len(a.Hits.Ratios) == HistCap {
			full++
		}
		if a.Hits.Len() != a.Requests {
			t.Fatalf("%s: history %d, requests %d", a.ID, a.Hits.Len(), a.Requests)
		}
	}
	bs, err := json.Marshal(sn)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) > 40<<20 {
		t.Errorf("the snapshot of a full state is %d bytes", len(bs))
	}
	t.Logf("%d events: %d agents (%d with a full history), snapshot %d KiB, %d dropped", n, len(sn.Agents), full, len(bs)>>10, s.Dropped)
	checkBounds(t, st)
}
