package state

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// seqOfFirst is the seq of the first event of the type in a decoded log (0: there is none).
func seqOfFirst(evs []obj, typ string) uint64 {
	for _, e := range evs {
		if e.str("type") == typ {
			return uint64(e.num("seq"))
		}
	}
	return 0
}

// A person is at the prompt of a real session. What the engine asks, and how each question is settled, is what the State shows.
func TestTheStateOfARealPromptedPermissionSession(t *testing.T) {
	skipWhereTheEngineDoesNotKnowThePaths(t)
	log := promptedLog(t)
	evs := decodeLog(t, log)
	o := readOracle(t, log, recordedPrices(evs))
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)

	// What the script made happen must have happened, or the checks compared nothing: three questions, four decisions, one of them
	// by policy with no question before it, and a call whose name the harness had to repair.
	if o.asked != 3 || o.allowed+o.denied != 4 || o.permBy["policy"] != 1 || o.unanswered != 0 {
		t.Fatalf("the script no longer makes the harness ask what it asked: %d asked, %d decided, by %v", o.asked, o.allowed+o.denied, o.permBy)
	}
	repaired := 0
	for _, e := range evs {
		if d := e.sub("data"); e.str("type") == "tool.call" && d.str("name") == "functions.write" && d.str("as") == "write" {
			repaired++
		}
	}
	if repaired != 1 {
		t.Fatalf("%d tool calls with a repaired name in the log", repaired)
	}

	p := sn.Perms
	if p.Asked != 3 || p.Allowed != 2 || p.Denied != 2 || p.ByUser != 3 || p.ByPolicy != 1 || p.ByNoOne != 0 || p.Canceled != 0 || p.Abandoned != 0 ||
		len(p.Pending) != 0 || len(p.Recent) != 4 {
		t.Fatalf("perms: %s", js(p))
	}
	type want struct {
		summary, by, remember, reasonHas string
		asked, allow                     bool
	}
	for i, w := range []want{
		{summary: "write notes/a.txt", by: PermByUser, remember: "session", reasonHas: "allowed by user for the session", asked: true, allow: true},
		{summary: "bash rm -rf build", by: PermByUser, reasonHas: "denied by user", asked: true},
		{summary: "read ", by: PermByPolicy, reasonHas: "built-in protection", asked: false},
		{summary: "read ", by: PermByUser, reasonHas: "allowed by user", asked: true, allow: true},
	} {
		d := p.Recent[i]
		if !strings.HasPrefix(d.Ask.Summary, w.summary) || d.By != w.by || d.Remember != w.remember || !strings.Contains(d.Reason, w.reasonHas) || d.Asked != w.asked ||
			d.Allow != w.allow || d.Ask.Agent != "main" || d.Ask.Role != "worker" || d.WaitedMs < 0 {
			t.Errorf("decision %d: %+v, want %+v", i, d, w)
		}
		if w.asked && (d.Ask.Seq == 0 || d.Ask.Reason == "") {
			t.Errorf("decision %d was asked and does not say what: %+v", i, d.Ask)
		}
		if !w.asked && (d.Ask.Seq != 0 || d.Ask.Reason != "") {
			t.Errorf("decision %d was never asked and has a question: %+v", i, d.Ask)
		}
	}
	// The paths: inside the project they are shown relative to it, outside they stay whole.
	if w := p.Recent[0].Ask; len(w.Paths) != 1 || w.Paths[0] != "notes/a.txt" || w.Command != "" || w.Tool != "write" {
		t.Errorf("the write: %+v", w)
	}
	if b := p.Recent[1].Ask; b.Command != "rm -rf build" || len(b.Paths) != 0 || b.Tool != "bash" {
		t.Errorf("the command: %+v", b)
	}
	for i, tail := range map[int]string{2: "/home/.ssh/id_rsa", 3: "/outside/secret.txt"} {
		if ps := p.Recent[i].Ask.Paths; len(ps) != 1 || !strings.HasSuffix(filepath.ToSlash(ps[0]), tail) || !filepath.IsAbs(ps[0]) {
			t.Errorf("decision %d: paths %q, want one ending in %s", i, ps, tail)
		}
	}

	// The agent: one, not a swarm, working again after every answer, finished at the end; two of its five calls were refused.
	a := agentOf(t, sn, "main")
	if sn.Session.Swarm || len(sn.Agents) != 1 || a.Status != StatusIdle || a.Asking != 0 || a.Role != "worker" || a.ToolCalls != 5 || a.ToolErrors != 2 || a.Cancel.Count != 0 {
		t.Errorf("agent %s status %s asking %d calls %d errors %d", a.ID, a.Status, a.Asking, a.ToolCalls, a.ToolErrors)
	}
	// The call that was written as functions.write is shown as the write that ran, and the name as written is nowhere on screen.
	writes := 0
	for _, l := range sn.Feed {
		if strings.Contains(l.Text, "functions.") {
			t.Errorf("the feed shows a name as the model wrote it: %q", l.Text)
		}
		if l.Kind == FeedTool && l.Text == "write notes/a.txt" {
			writes++
		}
	}
	if writes != 2 {
		t.Errorf("%d write lines in the feed, want the two writes (one of them called functions.write)", writes)
	}
	for _, frag := range []string{"main asks permission: write notes/a.txt", "allowed: write notes/a.txt", "by you, kept for the session", "denied: bash rm -rf build", "by you: denied by user", "by policy: built-in protection"} {
		if !feedHas(sn, FeedPerm, "main", frag) {
			t.Errorf("the feed lacks %q:\n%s", frag, js(sn.Feed))
		}
	}

	// At the moment the first question was put, before its answer, the agent is held at it: the real ask, folded alone.
	ask := seqOfFirst(evs, "perm.ask")
	mid, err := FoldUntil(writeLog(t, string(log)), ask)
	if err != nil {
		t.Fatal(err)
	}
	msn := mid.Snapshot()
	ma := agentOf(t, msn, "main")
	if len(msn.Perms.Pending) != 1 || msn.Perms.Pending[0].Seq != ask || msn.Perms.Pending[0].Summary != "write notes/a.txt" || ma.Status != StatusAsking || ma.Asking != 1 || ma.Tool != "write" {
		t.Errorf("at the question: %s, status %s asking %d tool %q", js(msn.Perms.Pending), ma.Status, ma.Asking, ma.Tool)
	}
}

// With no one to ask, the engine still writes its question and then its refusal, and says there was no one. Nothing is remembered, so
// the second write is asked about again (the same script as the prompted session, whose first answer let it through unasked).
func TestTheStateOfARealPermissionSessionWithNoOneToAsk(t *testing.T) {
	skipWhereTheEngineDoesNotKnowThePaths(t)
	log := noOneLog(t)
	evs := decodeLog(t, log)
	o := readOracle(t, log, recordedPrices(evs))
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)

	p := sn.Perms
	if p.Asked != 4 || p.Allowed != 0 || p.Denied != 5 || p.ByNoOne != 4 || p.ByUser != 0 || p.ByPolicy != 1 || len(p.Pending) != 0 || len(p.Recent) != 5 {
		t.Fatalf("perms: %s", js(p))
	}
	for i, d := range p.Recent {
		policy := i == 2 // the key in ~/.ssh: refused by a rule, no one is asked even when there is someone to ask
		switch {
		case policy && (d.Asked || d.By != PermByPolicy):
			t.Errorf("decision %d: %+v", i, d)
		case !policy && (!d.Asked || d.By != PermByNoOne || !strings.HasPrefix(d.Reason, "approval required")):
			t.Errorf("decision %d: %+v", i, d)
		}
		if d.Allow || d.Remember != "" {
			t.Errorf("decision %d allows or remembers: %+v", i, d)
		}
	}
	if a := agentOf(t, sn, "main"); a.ToolCalls != 5 || a.ToolErrors != 5 || a.Status != StatusIdle {
		t.Errorf("agent: %d calls, %d errors, %s", a.ToolCalls, a.ToolErrors, a.Status)
	}
	if !feedHas(sn, FeedPerm, "main", "no one to ask: approval required") {
		t.Errorf("feed:\n%s", js(sn.Feed))
	}
}

// Ctrl-C while a question waits: the engine answers "canceled" and the agent says its run was cancelled.
func TestTheStateOfARealSessionCancelledWhileAQuestionWaited(t *testing.T) {
	log := askingLog(t)
	evs := decodeLog(t, log)
	o := readOracle(t, log, recordedPrices(evs))
	sn := foldBytes(t, log).Snapshot()
	checkAgainstOracle(t, sn, o)
	if o.asked != 1 || o.permBy["canceled"] != 1 || sum(o.cancels) != 1 {
		t.Fatalf("the script no longer cancels a run at a question: %d asked, by %v, %d cancels", o.asked, o.permBy, sum(o.cancels))
	}

	p := sn.Perms
	if p.Asked != 1 || p.Denied != 1 || p.Canceled != 1 || len(p.Pending) != 0 || p.Abandoned != 0 || len(p.Recent) != 1 || !p.Recent[0].Asked || p.Recent[0].By != PermByCanceled {
		t.Errorf("perms: %s", js(p))
	}
	a := agentOf(t, sn, "main")
	// The cancel lands after the tool call has come back with its refusal, so the harness reports the phase "between": see CancelBetween.
	if a.Status != StatusIdle || a.Asking != 0 || a.Cancel.Count != 1 || a.Cancel.Phase != CancelBetween || a.Cancel.Cause != CauseCanceled || a.Cancel.Steps != 1 || a.Errors != 0 {
		t.Errorf("agent: status %s asking %d cancel %+v errors %d", a.Status, a.Asking, a.Cancel, a.Errors)
	}
	if tt := sn.Totals; tt.RunsCancelled != 1 || tt.Errors != 0 {
		t.Errorf("totals %+v", tt)
	}
	if !sn.Session.Ended || !feedHas(sn, FeedCancel, "main", "main's run was cancelled") || !feedHas(sn, FeedCancel, "main", "after 1 step") || !feedHas(sn, FeedPerm, "main", "cancelled while it waited") {
		t.Errorf("feed:\n%s", js(sn.Feed))
	}

	// Between the question and its answer the agent is held; one event later it is not.
	ask := seqOfFirst(evs, "perm.ask")
	path := writeLog(t, string(log))
	held, err := FoldUntil(path, ask)
	if err != nil {
		t.Fatal(err)
	}
	if ha := agentOf(t, held.Snapshot(), "main"); ha.Status != StatusAsking || len(held.Snapshot().Perms.Pending) != 1 {
		t.Errorf("held at the question: %s", ha.Status)
	}
	decided, err := FoldUntil(path, seqOfFirst(evs, "perm.decide"))
	if err != nil {
		t.Fatal(err)
	}
	if da := agentOf(t, decided.Snapshot(), "main"); da.Asking != 0 || da.Status == StatusAsking || len(decided.Snapshot().Perms.Pending) != 0 {
		t.Errorf("after the answer: %s", da.Status)
	}
}

// Ctrl-C while a request is out. The transport's words for it are "provider: network: request cancelled", and the request is not a
// failure. Whatever the words, agent.cancel says the run was cancelled in its request, and then it was not a failure either.
func TestTheStateOfARealSessionCancelledInARequest(t *testing.T) {
	log := modelCancelLog(t)
	evs := decodeLog(t, log)
	var said string
	for _, e := range evs {
		if e.str("type") == "model.error" {
			said = e.sub("data").str("error")
		}
	}
	if said != "provider: network: request cancelled" {
		t.Fatalf("the harness's words for a cancelled request are %q: isCancelText has to know them", said)
	}
	for name, l := range map[string][]byte{
		"as the harness writes it": log,
		"in words nothing knows": rewriteLog(t, log, func(e obj) {
			if e.str("type") == "model.error" {
				e.sub("data")["error"] = "provider: transport: the operation was aborted"
			}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			o := readOracle(t, l, recordedPrices(decodeLog(t, l)))
			sn := foldBytes(t, l).Snapshot()
			if name == "as the harness writes it" {
				checkAgainstOracle(t, sn, o)
			}
			a := agentOf(t, sn, "main")
			if a.Status != StatusIdle || a.Errors != 0 || a.InFlight != 0 || a.Cancel != (Cancel{Count: 1, Phase: CancelModel, Cause: CauseCanceled, Steps: 1, At: a.Cancel.At}) || a.Cancel.At.IsZero() {
				t.Errorf("agent: status %s errors %d in flight %d cancel %+v", a.Status, a.Errors, a.InFlight, a.Cancel)
			}
			if tt := sn.Totals; tt.Errors != 0 || tt.Cancelled != 1 || tt.RunsCancelled != 1 {
				t.Errorf("totals %+v", tt)
			}
			if !feedHas(sn, FeedNote, "main", "request cancelled") || feedHas(sn, FeedError, "", "") || !feedHas(sn, FeedCancel, "main", "while it waited for the model") {
				t.Errorf("feed:\n%s", js(sn.Feed))
			}
		})
	}
}

// keysOf is the keys the payloads of the events of a type carry, over a number of logs.
func keysOf(logs [][]byte, t testing.TB, typ string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, l := range logs {
		for _, e := range decodeLog(t, l) {
			if e.str("type") == typ {
				for k := range e.sub("data") {
					seen[k] = true
				}
			}
		}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOfPayload(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// statetest's builders write perm.ask, perm.decide and agent.cancel, and the unit tests are made of what they write: so what they
// write has to be what the harness writes. If a producer adds a member (an id to perm.ask, say) or drops one, this is what says so.
func TestTheBuildersWriteTheKeysTheHarnessWrites(t *testing.T) {
	b := statetest.NewBuilder()
	ask := b.PermAsk("a", "worker", "bash", "ls", "why", "/x")
	dec := b.PermDecide("a", "worker", "bash", "ls", "why", true, "user", "session", "/x")
	can := b.Cancel("a", "model", "canceled", 1)
	keysOfEvent := func(e events.Event) []string { return keysOfPayload(decodeLog(t, []byte(lineOf(e)))[0].sub("data")) }
	logs := [][]byte{promptedLog(t), noOneLog(t)}
	if got, want := keysOf(logs, t, "perm.ask"), keysOfEvent(ask); !reflect.DeepEqual(got, want) {
		t.Errorf("perm.ask: the harness writes %v, the builder %v", got, want)
	}
	if got, want := keysOf(logs, t, "perm.decide"), keysOfEvent(dec); !reflect.DeepEqual(got, want) {
		t.Errorf("perm.decide: the harness writes %v, the builder %v", got, want)
	}
	if got, want := keysOf([][]byte{askingLog(t), modelCancelLog(t)}, t, "agent.cancel"), keysOfEvent(can); !reflect.DeepEqual(got, want) {
		t.Errorf("agent.cancel: the harness writes %v, the builder %v", got, want)
	}
	// The repaired name of a tool call is written as "as", and only when it was repaired (the other calls of the log have none).
	if got := keysOf([][]byte{promptedLog(t)}, t, "tool.call"); !reflect.DeepEqual(got, []string{"as", "id", "input", "name"}) {
		t.Errorf("tool.call keys %v", got)
	}
}
