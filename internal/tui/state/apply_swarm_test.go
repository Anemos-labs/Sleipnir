package state

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

func TestMailFollowsAMessageFromSendToDelivery(t *testing.T) {
	b := newB()
	st := New()
	text := strings.Repeat("x", 36) // 36 bytes at 3.6 bytes a token: 10 tokens
	apply(t, st, b.Emit("be-1", events.TypeMailSend, map[string]any{"id": "m1", "from": "be-1", "to": "mgr", "kind": "request", "text": text}))
	b.Advance(ms(40))
	apply(t, st, b.Emit("mgr", events.TypeMailDeliver, map[string]any{"id": "m1", "from": "be-1"}))
	apply(t, st, b.Emit("fe-1", events.TypeMailSend, map[string]any{"id": "m2", "from": "fe-1", "to": "be-1", "kind": "info", "text": text + "y"}))
	apply(t, st, b.Emit("be-1", events.TypeMailDeliver, map[string]any{"id": "m2", "from": "fe-1"}))
	apply(t, st, b.Emit("be-1", events.TypeMailAck, map[string]any{"id": "m2"}))
	// Mail to someone who is gone is refused, and counted as ignored.
	apply(t, st, b.Emit("fe-1", events.TypeMailSend, map[string]any{"id": "m3", "from": "fe-1", "to": "gone", "text": "hello"}))
	apply(t, st, b.Emit("gone", "mail.drop", map[string]any{"id": "m3", "from": "fe-1", "reason": "gone has been retired"}))

	sn := st.Snapshot()
	m := sn.Mail
	if m.Counts != (MailCounts{Sent: 3, Delivered: 2, Ignored: 1, Acked: 1}) {
		t.Errorf("counts %+v", m.Counts)
	}
	if len(m.Recent) != 3 {
		t.Fatalf("recent %s", js(m.Recent))
	}
	m1, m2, m3 := m.Recent[0], m.Recent[1], m.Recent[2]
	if m1.ID != "m1" || m1.From != "be-1" || m1.To != "mgr" || m1.Kind != "request" || m1.Summary != text || m1.Tokens != 10 || m1.Stage != MailDelivered || !m1.Delivered.Equal(m1.Sent.Add(ms(40))) {
		t.Errorf("m1 %+v", m1)
	}
	if m2.Tokens != 11 || !m2.Acked || m2.Stage != MailDelivered || m2.Seq == 0 {
		t.Errorf("m2 %+v (37 bytes are 11 tokens)", m2)
	}
	if m3.Stage != MailDropped || m3.Reason != "gone has been retired" {
		t.Errorf("m3 %+v", m3)
	}
	if !anyMark(rowOf(t, sn, "be-1"), ActMail) || !anyMark(rowOf(t, sn, "mgr"), ActMail) {
		t.Error("mail marks the lanes of both ends")
	}
	if !feedHas(sn, FeedMail, "be-1", "be-1 → mgr: "+text) || !feedHas(sn, FeedMail, "be-1", "request · 10 tok") || !feedHas(sn, FeedMail, "gone", "was not delivered") {
		t.Errorf("feed %s", js(sn.Feed))
	}
}

func TestMailmanRoutesDigestsAndDeliversDirectly(t *testing.T) {
	b := newB()
	st := New()
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("m%d", i)
		apply(t, st, b.Emit("be-1", events.TypeMailSend, map[string]any{"id": id, "from": "be-1", "to": "mgr", "kind": "info", "text": "parcel " + id}))
		apply(t, st, b.Emit("be-1", events.TypeMailRoute, map[string]any{"id": id, "from": "be-1", "to": "mgr", "kind": "info"}))
	}
	apply(t, st, b.Emit("swarm", events.TypeMailBatch, map[string]any{"batch": "b1", "mailman": "mm-1", "recipients": 1, "parcels": 3}))
	apply(t, st, b.Emit("mm-1", events.TypeMailSend, map[string]any{"id": "m4", "from": "mm-1", "to": "mgr", "kind": "info", "text": "digest: three parcels", "via": "mm-1", "origins": []string{"be-1 x3"}}))
	apply(t, st, b.Emit("mgr", events.TypeMailDeliver, map[string]any{"id": "m4", "from": "mm-1", "via": "mm-1"}))
	apply(t, st, b.Emit("mm-1", events.TypeMailDigest, map[string]any{"id": "m4", "to": "mgr", "mailman": "mm-1", "parcels": []string{"m1", "m2", "m3"}, "senders": []string{"be-1 x3"}, "kind": "info", "frame": "[mail m4 ...]"}))
	apply(t, st, b.Emit("swarm", events.TypeMailDirect, map[string]any{"reason": "the mailman is down for a while", "n": 2, "ids": []string{"m5", "m6"}}))
	apply(t, st, b.Emit("swarm", events.TypeMailDirect, map[string]any{"reason": "the mailroom is full", "n": 1, "ids": []string{"m7"}}))
	apply(t, st, b.Emit("swarm", events.TypeMailmanState, map[string]any{"state": "down", "reason": "it failed twice in a row", "for": "60s"}))
	apply(t, st, b.Emit("swarm", events.TypeMailmanState, map[string]any{"state": "up"}))

	sn := st.Snapshot()
	c := sn.Mail.Counts
	if c != (MailCounts{Sent: 4, Routed: 3, Delivered: 1, Digests: 1, Direct: 3, Batches: 1}) {
		t.Errorf("counts %+v", c)
	}
	var stages []string
	for _, m := range sn.Mail.Recent {
		stages = append(stages, string(m.Stage))
	}
	if strings.Join(stages, " ") != "digested digested digested delivered" {
		t.Errorf("stages %v: the parcels a digest stands for are digested", stages)
	}
	dg := sn.Mail.Recent[3]
	if dg.Via != "mm-1" || strings.Join(dg.Origins, ",") != "be-1 x3" || dg.From != "mm-1" {
		t.Errorf("digest %+v", dg)
	}
	if mm := sn.Mail.Mailman; mm.State != "up" || mm.Outages != 1 || mm.Reason != "" {
		t.Errorf("mailman %+v: back up, with no reason to be down", mm)
	}
	if !feedHas(sn, FeedMail, "mm-1", "mm-1 → mgr (digest)") || !feedHas(sn, FeedMail, "", "the mailman is down") {
		t.Errorf("feed %s", js(sn.Feed))
	}
}

func TestMailRingIsBoundedAndAnEventOutOfOrderStillCounts(t *testing.T) {
	b := newB()
	st := New()
	// A delivery whose send was never seen makes a stub of the message.
	apply(t, st, b.Emit("mgr", events.TypeMailDeliver, map[string]any{"id": "early", "from": "be-1"}))
	if m := st.Snapshot().Mail; len(m.Recent) != 1 || m.Recent[0].From != "be-1" || m.Recent[0].To != "mgr" || m.Recent[0].Stage != MailDelivered || m.Counts.Delivered != 1 || m.Counts.Sent != 0 {
		t.Errorf("stub %s", js(m))
	}
	// An ack or a digest of a message that is not there changes nothing but the count.
	apply(t, st, b.Emit("mgr", events.TypeMailAck, map[string]any{"id": "nobody"}))
	apply(t, st, b.Emit("mm", events.TypeMailDigest, map[string]any{"id": "x", "parcels": []string{"nobody"}}))
	n := MailCap + 30
	for i := 1; i <= n; i++ {
		apply(t, st, b.Emit("a", events.TypeMailSend, map[string]any{"id": fmt.Sprintf("s%d", i), "from": "a", "to": "b", "text": "t"}))
	}
	// A delivery of a message the ring has forgotten is a stub again; one it still has is updated in place.
	apply(t, st, b.Emit("b", events.TypeMailDeliver, map[string]any{"id": "s1", "from": "a"}))
	apply(t, st, b.Emit("b", events.TypeMailDeliver, map[string]any{"id": fmt.Sprintf("s%d", n), "from": "a"}))
	m := st.Snapshot().Mail
	if len(m.Recent) != MailCap {
		t.Fatalf("%d messages kept", len(m.Recent))
	}
	if m.Counts.Sent != n {
		t.Errorf("the counts are exact though the ring forgets: %+v", m.Counts)
	}
	last := m.Recent[len(m.Recent)-1]
	if last.ID != "s1" || last.Stage != MailDelivered {
		t.Errorf("last %+v: the forgotten s1 came back as a stub", last)
	}
	if prev := m.Recent[len(m.Recent)-2]; prev.ID != fmt.Sprintf("s%d", n) || prev.Stage != MailDelivered {
		t.Errorf("a message still in the ring is updated in place: %+v", prev)
	}
	if got := len(st.mail.byID); got > MailCap+1 {
		t.Errorf("the id index grew to %d", got)
	}
}

func TestLeasesAreTheFilesAnAgentHolds(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil))) // root /work/proj
	lease := func(agent, action, path, holder string) {
		t.Helper()
		m := map[string]any{"action": action, "agent": agent}
		if path != "" {
			m["path"] = path
		}
		if holder != "" {
			m["holder"] = holder
		}
		apply(t, st, b.Emit(agent, events.TypeLease, m))
	}
	lease("w-1", "acquire", "/work/proj/src/a.go", "")
	lease("w-1", "acquire", "/work/proj/src/b.go", "")
	lease("w-2", "acquire", "/work/proj/docs/x.md", "")
	lease("w-2", "acquire", "/elsewhere/y.md", "")
	sn := st.Snapshot()
	if got := agentOf(t, sn, "w-1").Leases; strings.Join(got, ",") != "src/a.go,src/b.go" {
		t.Errorf("w-1 leases %v: paths under the project root are shown relative to it", got)
	}
	if got := agentOf(t, sn, "w-2").Leases; strings.Join(got, ",") != "/elsewhere/y.md,docs/x.md" {
		t.Errorf("w-2 leases %v", got)
	}
	var held []string
	for _, l := range sn.Leases.Held {
		held = append(held, l.Agent+":"+l.Path)
	}
	if strings.Join(held, " ") != "w-2:/elsewhere/y.md w-2:docs/x.md w-1:src/a.go w-1:src/b.go" {
		t.Errorf("held %v", held)
	}
	// The lease changes hands when the old one expired and another agent took the file.
	lease("w-2", "acquire", "/work/proj/src/a.go", "")
	if got := agentOf(t, st.Snapshot(), "w-1").Leases; strings.Join(got, ",") != "src/b.go" {
		t.Errorf("w-1 leases %v: src/a.go is w-2's now", got)
	}
	lease("w-3", "conflict", "/work/proj/src/a.go", "w-2")
	lease("w-3", "scope", "/work/proj/other/z.go", "")
	lease("w-3", "overlap", "src/q.go", "w-1")
	lease("w-1", "release", "", "")
	sn = st.Snapshot()
	if got := agentOf(t, sn, "w-1").Leases; len(got) != 0 {
		t.Errorf("released: %v", got)
	}
	if l := sn.Leases; l.Conflicts != 1 || l.ScopeViolations != 1 || l.Overlaps != 1 || len(l.Held) != 3 {
		t.Errorf("leases %s", js(l))
	}
	for _, frag := range []string{"w-3 wanted src/a.go, which is leased to w-2", "outside its task's scope", "edit the same file in separate trees"} {
		if !feedHas(sn, FeedLease, "w-3", frag) {
			t.Errorf("feed lacks %q:\n%s", frag, js(sn.Feed))
		}
	}
}

func TestLeaseTablesAreBounded(t *testing.T) {
	b := newB()
	st := New()
	for i := 0; i < MaxLeasesPerAgent+5; i++ {
		b.Advance(ms(1))
		apply(t, st, b.Emit("w-1", events.TypeLease, map[string]any{"action": "acquire", "agent": "w-1", "path": fmt.Sprintf("/f/%03d", i)}))
	}
	a := agentOf(t, st.Snapshot(), "w-1")
	if len(a.Leases) != MaxLeasesPerAgent || a.Leases[0] != "/f/005" {
		t.Errorf("an agent holds at most %d: %d %v", MaxLeasesPerAgent, len(a.Leases), a.Leases[:1])
	}
	if n := len(st.Snapshot().Leases.Held); n != MaxLeasesPerAgent {
		t.Errorf("the ones it let go of are not in the table: %d", n)
	}
	st2 := New()
	for i := 0; i < MaxLeases+40; i++ {
		b.Advance(ms(1))
		ag := fmt.Sprintf("w-%d", i%40)
		apply(t, st2, b.Emit(ag, events.TypeLease, map[string]any{"action": "acquire", "agent": ag, "path": fmt.Sprintf("/g/%04d", i)}))
	}
	if n := len(st2.Snapshot().Leases.Held); n > MaxLeases {
		t.Errorf("the table holds %d leases", n)
	}
	for _, a := range st2.Snapshot().Agents {
		if len(a.Leases) > MaxLeasesPerAgent {
			t.Errorf("%s holds %d", a.ID, len(a.Leases))
		}
	}
}

func TestGovernorCountsRateLimitEpisodesAndRequestsPerMinute(t *testing.T) {
	b := newB()
	st := New()
	t0 := b.Now()
	for i := 0; i < 5; i++ {
		apply(t, st, b.Request("a", fmt.Sprintf("a.%d", i), "m", "pk", secShared()))
		b.Advance(sec(1))
	}
	apply(t, st, b.Emit("swarm", events.TypeGovernor, map[string]any{"action": "rate-limited", "rate_per_min": 192.0, "pause_ms": 5000, "retry_after_ms": 5000, "inflight": 3, "queued": 7}))
	govAt := b.Now()
	g := st.SnapshotAt(t0.Add(sec(5))).Governor
	if g.Episodes != 1 || g.RatePerMin != 192 || g.Inflight != 3 || g.Queued != 7 || !g.PauseUntil.Equal(govAt.Add(sec(5))) || !g.LastAt.Equal(govAt) {
		t.Errorf("governor %+v", g)
	}
	if g.RPM != 5 {
		t.Errorf("rpm %d: five requests in the last minute", g.RPM)
	}
	// Requests leave the window second by second: at t0+62s the requests of seconds 0, 1 and 2 are a minute old or more.
	for _, c := range []struct {
		at   time.Duration
		want int
	}{{sec(30), 5}, {sec(59), 5}, {sec(60), 4}, {sec(62), 2}, {sec(64), 0}, {sec(3600), 0}} {
		if got := st.SnapshotAt(t0.Add(c.at)).Governor.RPM; got != c.want {
			t.Errorf("rpm at +%v = %d, want %d", c.at, got, c.want)
		}
	}
	if !feedHas(st.Snapshot(), FeedRetry, "", "the swarm slowed to 192 requests a minute") {
		t.Errorf("feed %s", js(st.Snapshot().Feed))
	}
}

func TestSupervisionAndBudgetEvents(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("mgr", events.TypeSwarmHold, map[string]any{"reason": "Not finished: running: be-1 (T3)"}))
	apply(t, st, b.Emit("mgr", events.TypeSwarmUnfinished, map[string]any{"reason": "T5 not started"}))
	apply(t, st, b.Emit("mgr", events.TypeSwarmWake, map[string]any{"n": 1, "note": "While you were idle: T1 is in review (be-1)"}))
	apply(t, st, b.Emit("mgr", events.TypeSwarmWakePaused, map[string]any{"n": 8}))
	apply(t, st, b.Emit("be-1", events.TypeSwarmWakeLimit, map[string]any{"limit": 40, "task": "T1"}))
	apply(t, st, b.Emit("swarm", "swarm.shutdown", map[string]any{"waited": "10s"}))
	apply(t, st, b.Emit("swarm", "swarm.budget", map[string]any{"budget_usd": 20.0, "spent_usd": 20.4}))
	sn := st.Snapshot()
	if s := sn.Supervision; s.Holds != 1 || s.Unfinished != 1 || s.Wakes != 1 || s.WakePaused != 1 || s.WakeLimits != 1 || s.LastHold != "Not finished: running: be-1 (T3)" || !strings.HasPrefix(s.LastWake, "While you were idle") {
		t.Errorf("supervision %+v", s)
	}
	tot := sn.Totals
	if !tot.BudgetSpent || tot.BudgetUSD != 20 || tot.BudgetSpentUSD != 20.4 {
		t.Errorf("budget %+v", tot)
	}
	for _, frag := range []string{"sent back to it", "run ended with work unfinished", "idle manager was woken", "will not be woken again", "no longer wakes be-1", "shut down", "budget of $20.0 is spent"} {
		found := false
		for _, l := range sn.Feed {
			if strings.Contains(l.Text, frag) {
				found = true
			}
		}
		if !found {
			t.Errorf("feed lacks %q:\n%s", frag, js(sn.Feed))
		}
	}
}

func TestBackgroundJobsAndFaultsReachTheFeed(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("w-1", events.TypeToolJob, map[string]any{"id": "j1", "agent": "w-1", "command": "npm run dev", "status": "started"}))
	apply(t, st, b.Emit("w-1", events.TypeToolJob, map[string]any{"id": "j1", "agent": "w-1", "command": "npm run dev", "status": "exited", "exit": 2, "duration_ms": 1500}))
	apply(t, st, b.Emit("w-1", events.TypeToolJob, map[string]any{"id": "j2", "agent": "w-1", "command": "sleep 99", "status": "killed", "exit": 137, "duration_ms": 90000}))
	apply(t, st, b.Emit("w-1", events.TypeToolJob, map[string]any{"id": "j3", "agent": "w-1", "command": "make", "status": "exited", "exit": 0, "duration_ms": 61000}))
	apply(t, st, b.Emit("w-1", "tool.timeout", map[string]any{"id": "c1", "name": "bash", "limit_ms": 1800000}))
	apply(t, st, b.Emit("w-1", "tool.panic", map[string]any{"name": "grep", "stack": "goroutine 1 ..."}))
	apply(t, st, b.Emit("w-1", "agent.panic", map[string]any{"id": "w-1", "panic": "boom", "where": "compactor"}))
	apply(t, st, b.Emit("swarm", "agent.abandon", map[string]any{"id": "w-1"}))
	sn := st.Snapshot()
	for _, c := range []struct {
		kind FeedKind
		frag string
	}{{FeedJob, "background job j1 exited 2: npm run dev"}, {FeedJob, "background job j2 was killed"}, {FeedJob, "background job j3 finished: make"}, {FeedError, "tool bash ran too long"}, {FeedError, "tool grep crashed"}, {FeedError, "w-1 crashed"}, {FeedError, "did not stop when asked"}} {
		if !feedHas(sn, c.kind, "", c.frag) {
			t.Errorf("feed lacks %s %q:\n%s", c.kind, c.frag, js(sn.Feed))
		}
	}
	if !feedHas(sn, FeedJob, "", "1.5 s") || !feedHas(sn, FeedJob, "", "1m01s") {
		t.Errorf("durations: %s", js(sn.Feed))
	}
	if a := agentOf(t, sn, "w-1"); a.Status != StatusError {
		t.Errorf("an abandoned run is an error: %s", a.Status)
	}
}
