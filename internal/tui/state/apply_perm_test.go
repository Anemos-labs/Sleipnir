package state

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// The permission events are perm.ask (the engine has to put a question) and perm.decide (it was answered, or a request was refused
// outright), written by internal/session/permaudit.go from perm.Audit. What they carry: tool, reason, command (for a shell call),
// paths (at most five), role; and in perm.decide allow, by ("user", "no one", "policy" or "canceled") and remember. They carry no
// id. The tests below are built from events of exactly that shape (statetest's PermAsk and PermDecide write it, and the test of the
// real permission session compares the builder's key sets with the harness's own), and the real harness's sessions are folded in
// real_perm_test.go.

// An asking agent has a question pending until the decision for that very request arrives, and shows it is held, not working.
func TestAPermissionQuestionIsPendingUntilItsDecisionArrives(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
	apply(t, st, b.Request("w-1", "w-1.1", "m", "pk", secShared()))
	apply(t, st, b.Call("w-1", "c1", "bash", map[string]any{"command": "rm -rf build"}))
	b.Advance(ms(10))
	ask := b.PermAsk("w-1", "backend", "bash", "rm -rf build", "not a read-only command")
	apply(t, st, ask)

	sn := st.Snapshot()
	if len(sn.Perms.Pending) != 1 || sn.Perms.Asked != 1 || sn.Perms.Allowed != 0 || len(sn.Perms.Recent) != 0 {
		t.Fatalf("perms after the question: %s", js(sn.Perms))
	}
	q := sn.Perms.Pending[0]
	if q.Seq != ask.Seq || !q.T.Equal(ask.TS) || q.Agent != "w-1" || q.Role != "backend" || q.Tool != "bash" || q.Command != "rm -rf build" ||
		q.Reason != "not a read-only command" || q.Summary != "bash rm -rf build" || len(q.Paths) != 0 {
		t.Errorf("the question: %+v", q)
	}
	if a := agentOf(t, sn, "w-1"); a.Status != StatusAsking || a.Asking != 1 || a.Tool != "bash" || !a.Status.Active() {
		t.Errorf("a held agent: status %s, asking %d, tool %q", a.Status, a.Asking, a.Tool)
	}
	var line FeedLine
	for _, l := range sn.Feed {
		if l.Kind == FeedPerm {
			line = l
		}
	}
	if line.Glyph != GlyphAsk || line.Agent != "w-1" || line.Text != "w-1 asks permission: bash rm -rf build" || line.Detail != "not a read-only command" {
		t.Errorf("the feed line of the question: %+v", line)
	}

	// Another agent's answer to the same request, or this agent's to another, does not settle it.
	b.Advance(ms(1000))
	apply(t, st, b.PermDecide("w-2", "backend", "bash", "rm -rf build", "no", false, "user", ""))
	apply(t, st, b.PermDecide("w-1", "backend", "bash", "rm -rf other", "no", false, "user", ""))
	if n := len(st.Snapshot().Perms.Pending); n != 1 {
		t.Fatalf("%d questions pending after two answers that were not for it", n)
	}

	b.Advance(ms(1500))
	dec := b.PermDecide("w-1", "backend", "bash", "rm -rf build", "approved", true, "user", "session")
	apply(t, st, dec)
	sn = st.Snapshot()
	p := sn.Perms
	if len(p.Pending) != 0 || p.Asked != 1 || p.Allowed != 1 || p.Denied != 2 || p.ByUser != 3 || p.Abandoned != 0 {
		t.Errorf("perms after the answer: %s", js(p))
	}
	d := p.Recent[len(p.Recent)-1]
	if !d.Asked || !d.Allow || d.By != PermByUser || d.Remember != "session" || d.Reason != "approved" || d.Seq != dec.Seq || !d.T.Equal(dec.TS) ||
		d.WaitedMs != 2500 || d.Ask.Seq != ask.Seq || d.Ask.Summary != "bash rm -rf build" || d.Ask.Reason != "not a read-only command" {
		t.Errorf("the decision: %+v", d)
	}
	if a := agentOf(t, sn, "w-1"); a.Status != StatusTool || a.Asking != 0 {
		t.Errorf("after the answer the agent works again: status %s, asking %d", a.Status, a.Asking)
	}
	if !feedHas(sn, FeedPerm, "w-1", "allowed: bash rm -rf build") || !feedHas(sn, FeedPerm, "w-1", "by you, kept for the session") {
		t.Errorf("the feed lacks the answer:\n%s", js(sn.Feed))
	}
}

// Every way a request is settled, who settled it, and what the screen can say about it.
func TestEveryWayAPermissionIsSettledIsRecordedWithWhoDecided(t *testing.T) {
	for _, c := range []struct {
		name            string
		asked           bool // a perm.ask comes first
		allow           bool
		by, remember    string
		reason          string
		wantGlyph       string
		wantDetail      string
		by1, by2, by3   int // ByUser, ByNoOne, ByPolicy
		canceled        int
		allowed, denied int
	}{
		{name: "a person says yes", asked: true, allow: true, by: "user", remember: "project", wantGlyph: GlyphOK, wantDetail: "by you, kept for the project", by1: 1, allowed: 1},
		{name: "a person says no", asked: true, by: "user", reason: "not now", wantGlyph: GlyphFail, wantDetail: "by you: not now", by1: 1, denied: 1},
		{name: "nobody could be asked", asked: true, by: "no one", reason: "approval required: writes outside the workspace", wantGlyph: GlyphFail,
			wantDetail: "no one to ask: approval required: writes outside the workspace", by2: 1, denied: 1},
		{name: "a rule refuses without a question", by: "policy", reason: "a protected path: ~/.ssh", wantGlyph: GlyphFail, wantDetail: "by policy: a protected path: ~/.ssh", by3: 1, denied: 1},
		{name: "the run was cancelled while it waited", asked: true, by: "canceled", reason: "approval canceled: context canceled", wantGlyph: GlyphFail,
			wantDetail: "cancelled while it waited: approval canceled: context canceled", canceled: 1, denied: 1},
		{name: "a word nobody knows", asked: true, allow: true, by: "robot", wantGlyph: GlyphOK, wantDetail: "by robot", allowed: 1},
		{name: "no word at all", asked: true, by: "", wantGlyph: GlyphFail, wantDetail: "by unknown", denied: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
			if c.asked {
				apply(t, st, b.PermAsk("w-1", "backend", "write", "", "writes outside the workspace", "/etc/hosts"))
			}
			b.Advance(ms(40))
			apply(t, st, b.PermDecide("w-1", "backend", "write", "", c.reason, c.allow, c.by, c.remember, "/etc/hosts"))
			sn := st.Snapshot()
			p := sn.Perms
			asked := 0
			if c.asked {
				asked = 1
			}
			if len(p.Pending) != 0 || p.Asked != asked || p.Allowed != c.allowed || p.Denied != c.denied || p.ByUser != c.by1 || p.ByNoOne != c.by2 ||
				p.ByPolicy != c.by3 || p.Canceled != c.canceled || p.Abandoned != 0 || len(p.Recent) != 1 {
				t.Fatalf("perms: %s", js(p))
			}
			d := p.Recent[0]
			if d.Asked != c.asked || d.Allow != c.allow || d.By != c.by || d.Remember != c.remember || d.Reason != c.reason || d.Ask.Tool != "write" ||
				d.Ask.Summary != "write /etc/hosts" || len(d.Ask.Paths) != 1 || d.Ask.Agent != "w-1" {
				t.Errorf("decision %+v", d)
			}
			if c.asked && (d.Ask.Seq == 0 || d.Ask.Reason != "writes outside the workspace" || d.WaitedMs != 40) {
				t.Errorf("a question that was asked: %+v waited %d", d.Ask, d.WaitedMs)
			}
			if !c.asked && (d.Ask.Seq != 0 || !d.Ask.T.IsZero() || d.Ask.Reason != "" || d.WaitedMs != 0) {
				t.Errorf("a refusal that was never asked has a question: %+v", d.Ask)
			}
			last := sn.Feed[len(sn.Feed)-1]
			if last.Kind != FeedPerm || last.Glyph != c.wantGlyph || !strings.HasPrefix(last.Text, map[bool]string{true: "allowed: ", false: "denied: "}[c.allow]) || last.Detail != c.wantDetail {
				t.Errorf("feed line %+v, want glyph %s detail %q", last, c.wantGlyph, c.wantDetail)
			}
		})
	}
}

// The log gives questions no id. Two agents asking for the same thing are two questions; one agent asking the same thing twice is
// answered in the order it asked; a different command or different paths is a different request.
func TestQuestionsArePairedWithTheirAnswersByWhoWhatAndWhich(t *testing.T) {
	b := newB()
	st := New()
	pending := func() []string {
		var out []string
		for _, q := range st.Snapshot().Perms.Pending {
			out = append(out, fmt.Sprintf("%s:%s:%d", q.Agent, q.Command+strings.Join(q.Paths, ","), q.Seq))
		}
		return out
	}
	q1 := b.PermAsk("w-1", "", "bash", "make", "r")
	q2 := b.PermAsk("w-2", "", "bash", "make", "r")
	q3 := b.PermAsk("w-1", "", "bash", "make", "r") // the same request again, while the first is unanswered
	q4 := b.PermAsk("w-1", "", "bash", "make test", "r")
	q5 := b.PermAsk("w-1", "", "read", "", "r", "/a/x")
	q6 := b.PermAsk("w-1", "", "read", "", "r", "/a/y")
	apply(t, st, q1, q2, q3, q4, q5, q6)
	if got := len(pending()); got != 6 {
		t.Fatalf("%d pending", got)
	}
	answer := func(e events.Event) uint64 { // the seq of the question that the answer settled
		t.Helper()
		apply(t, st, e)
		r := st.Snapshot().Perms.Recent
		return r[len(r)-1].Ask.Seq
	}
	if got := answer(b.PermDecide("w-2", "", "bash", "make", "", true, "user", "")); got != q2.Seq {
		t.Errorf("w-2's answer settled seq %d, want %d", got, q2.Seq)
	}
	if got := answer(b.PermDecide("w-1", "", "bash", "make", "", true, "user", "")); got != q1.Seq {
		t.Errorf("w-1's first answer settled seq %d: the oldest identical question goes first (want %d)", got, q1.Seq)
	}
	if got := answer(b.PermDecide("w-1", "", "read", "", "", true, "user", "", "/a/y")); got != q6.Seq {
		t.Errorf("an answer for other paths settled seq %d, want %d", got, q6.Seq)
	}
	if got := answer(b.PermDecide("w-1", "", "bash", "make", "", false, "user", "")); got != q3.Seq {
		t.Errorf("w-1's second answer settled seq %d, want %d", got, q3.Seq)
	}
	if left, want := pending(), []string{fmt.Sprintf("w-1:make test:%d", q4.Seq), fmt.Sprintf("w-1:/a/x:%d", q5.Seq)}; !slices.Equal(left, want) {
		t.Errorf("what is still pending: %v, want %v", left, want)
	}
	if a := agentOf(t, st.Snapshot(), "w-1"); a.Asking != 2 || a.Status != StatusAsking {
		t.Errorf("w-1 still waits on two questions: %s asking %d", a.Status, a.Asking)
	}
	if a := agentOf(t, st.Snapshot(), "w-2"); a.Asking != 0 {
		t.Errorf("w-2 asking %d", a.Asking)
	}
}

// A question does not outlive its asker: when the agent's run ends (by the swarm's word, by its own end or by a cancellation) or the
// session ends, what it waited for is over. The harness answers "canceled" before a run ends; a log that was cut short does not.
func TestAQuestionEndsWithTheRunOfItsAgent(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(b *statetest.Builder) events.Event
	}{
		{"agent.end", func(b *statetest.Builder) events.Event {
			return b.Emit("w-1", events.TypeAgentEnd, map[string]any{"id": "w-1", "state": "idle", "evidence": ""})
		}},
		{"agent.state idle", func(b *statetest.Builder) events.Event {
			return b.Emit("w-1", events.TypeAgentState, map[string]any{"id": "w-1", "state": "idle", "line": "", "task": ""})
		}},
		{"agent.state failed", func(b *statetest.Builder) events.Event {
			return b.Emit("w-1", events.TypeAgentState, map[string]any{"id": "w-1", "state": "failed", "line": "", "task": ""})
		}},
		{"agent.cancel", func(b *statetest.Builder) events.Event { return b.Cancel("w-1", "tools", "canceled", 2) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"), b.Spawn("w-2", "backend", "T2", "mgr"))
			apply(t, st, b.PermAsk("w-1", "", "bash", "make", "r"), b.PermAsk("w-2", "", "bash", "make", "r"), b.PermAsk("w-1", "", "bash", "go test", "r"))
			if a := agentOf(t, st.Snapshot(), "w-1"); a.Status != StatusAsking || a.Asking != 2 {
				t.Fatalf("before: %s %d", a.Status, a.Asking)
			}
			apply(t, st, c.end(b))
			sn := st.Snapshot()
			if a := agentOf(t, sn, "w-1"); a.Asking != 0 || a.Status == StatusAsking || a.Status.Active() {
				t.Errorf("w-1 after its run ended: %s asking %d", a.Status, a.Asking)
			}
			if a := agentOf(t, sn, "w-2"); a.Asking != 1 || a.Status != StatusAsking {
				t.Errorf("another agent's question is not touched: %s asking %d", a.Status, a.Asking)
			}
			if p := sn.Perms; len(p.Pending) != 1 || p.Pending[0].Agent != "w-2" || p.Abandoned != 2 || p.Asked != 3 {
				t.Errorf("perms: %s", js(p))
			}
			// An answer that comes after all is recorded, as one whose question the State no longer has.
			apply(t, st, b.PermDecide("w-1", "", "bash", "make", "approval canceled", false, "canceled", ""))
			p := st.Snapshot().Perms
			if d := p.Recent[len(p.Recent)-1]; d.Asked || d.By != PermByCanceled || len(p.Pending) != 1 || p.Canceled != 1 {
				t.Errorf("the late answer: %+v in %s", d, js(p))
			}
		})
	}
}

func TestTheSessionEndingEndsEveryQuestion(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
	apply(t, st, b.PermAsk("w-1", "", "bash", "make", "r"))
	apply(t, st, b.PermAsk("", "", "bash", "make", "r")) // a question of no agent (kernel-level)
	apply(t, st, b.PermAsk("ghost", "", "read", "", "r", "/x"))
	if p := st.Snapshot().Perms; len(p.Pending) != 3 {
		t.Fatalf("%s", js(p))
	}
	apply(t, st, b.Emit("", events.TypeSessionEnd, map[string]any{"cost_usd": 0, "reason": "completed"}))
	sn := st.Snapshot()
	if p := sn.Perms; len(p.Pending) != 0 || p.Abandoned != 3 || p.Asked != 3 {
		t.Errorf("perms %s", js(p))
	}
	for _, a := range sn.Agents {
		if a.Asking != 0 || a.Status == StatusAsking {
			t.Errorf("%s still asks after the session ended: %s %d", a.ID, a.Status, a.Asking)
		}
	}
}

func TestOnlyMaxPendingQuestionsAreKeptAndTheOldestMakesRoom(t *testing.T) {
	b := newB()
	st := New()
	for i := 0; i < MaxPending+4; i++ {
		apply(t, st, b.PermAsk("w-1", "", "bash", fmt.Sprintf("cmd %d", i), "r"))
	}
	sn := st.Snapshot()
	if len(sn.Perms.Pending) != MaxPending || sn.Perms.Pending[0].Command != "cmd 4" || sn.Perms.Asked != MaxPending+4 || sn.Stats.Dropped != 4 {
		t.Errorf("%d pending, first %q, asked %d, dropped %d", len(sn.Perms.Pending), sn.Perms.Pending[0].Command, sn.Perms.Asked, sn.Stats.Dropped)
	}
	if a := agentOf(t, sn, "w-1"); a.Asking != MaxPending {
		t.Errorf("w-1 asking %d, want the %d that are kept", a.Asking, MaxPending)
	}
	// The ring of answers keeps the newest.
	for i := 0; i < PermLog+3; i++ {
		apply(t, st, b.PermDecide("w-1", "", "bash", fmt.Sprintf("cmd %d", MaxPending+3-i), "", true, "user", ""))
	}
	sn = st.Snapshot()
	if len(sn.Perms.Recent) != PermLog || sn.Perms.Allowed != PermLog+3 {
		t.Errorf("%d recent, %d allowed", len(sn.Perms.Recent), sn.Perms.Allowed)
	}
}

// A decision that does not say what it decided is shown as a refusal, never as a consent, and is counted as a payload that lacked
// an essential field.
func TestADecisionThatDoesNotSayWhatItDecidedIsARefusal(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.PermAsk("w-1", "", "bash", "make", "r"))
	apply(t, st, b.Emit("w-1", events.TypePermDecide, map[string]any{"tool": "bash", "command": "make", "by": "user", "reason": "ok"}))
	sn := st.Snapshot()
	if p := sn.Perms; p.Allowed != 0 || p.Denied != 1 || len(p.Pending) != 0 || p.Recent[0].Allow || !p.Recent[0].Asked || sn.Stats.Bad != 1 {
		t.Errorf("perms %s, bad %d", js(p), sn.Stats.Bad)
	}
	if !feedHas(sn, FeedPerm, "w-1", "denied: bash make") {
		t.Errorf("feed %s", js(sn.Feed))
	}
}

// What the producer writes is bounded (a command of 400 characters, five paths of 200), but the State does not rely on it: what it
// keeps is one clean line, and paths under the project are shown relative to it.
func TestPermissionTextIsBoundedCleanAndPathsAreRelative(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil))) // root /work/proj
	long := strings.Repeat("ab cd\n", 200)
	paths := []string{"/work/proj/src/a.go", "/work/proj", "/work/project/x", "/etc/hosts", "\x1b[2J/evil", "/6", "/7", "/8"}
	apply(t, st, b.PermAsk("w-1\x1b[31m", "role\n", "bash\x00", "echo \x1b]52;c;AAAA\x07\n"+long, long, paths...))
	q := st.Snapshot().Perms.Pending[0]
	if len(q.Paths) != MaxPermPaths || q.Paths[0] != "src/a.go" || q.Paths[1] != "/work/proj" || q.Paths[2] != "/work/project/x" || q.Paths[3] != "/etc/hosts" {
		t.Errorf("paths %q", q.Paths)
	}
	for what, s := range map[string]string{"agent": q.Agent, "role": q.Role, "tool": q.Tool, "command": q.Command, "reason": q.Reason, "summary": q.Summary, "path": q.Paths[4]} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				t.Errorf("%s %q holds a control character", what, s)
				break
			}
		}
	}
	if n := len([]rune(q.Command)); n > textPath {
		t.Errorf("command of %d runes", n)
	}
	if n := len([]rune(q.Summary)); n > textLine {
		t.Errorf("summary of %d runes", n)
	}
	if n := len([]rune(q.Reason)); n > textLine {
		t.Errorf("reason of %d runes", n)
	}
}

func TestPermissionSnapshotsShareNothingWithTheState(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.PermAsk("w-1", "", "read", "", "r", "/a", "/b"))
	apply(t, st, b.PermAsk("w-1", "", "read", "", "r", "/c", "/d"))
	apply(t, st, b.PermDecide("w-1", "", "read", "", "", true, "user", "", "/a", "/b"))
	want := js(st.Snapshot().Perms)
	sn := st.Snapshot()
	for i := range sn.Perms.Pending {
		sn.Perms.Pending[i].Paths[0] = "scribble"
	}
	for i := range sn.Perms.Recent {
		sn.Perms.Recent[i].Ask.Paths[0] = "scribble"
	}
	if got := js(st.Snapshot().Perms); got != want {
		t.Errorf("writing to a snapshot reached the State:\n%s\nwas:\n%s", got, want)
	}
}

// A held tool call is proof that the agent is working, as any tool call is: a question of an agent the State believed had stopped
// puts it back to work, and waiting.
func TestAQuestionPutsAnAgentThatHadStoppedBackToWork(t *testing.T) {
	for _, state := range []string{"idle", "done", "failed"} {
		t.Run(state, func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
			apply(t, st, b.Emit("w-1", events.TypeAgentState, map[string]any{"id": "w-1", "state": state, "line": "", "task": ""}))
			apply(t, st, b.PermAsk("w-1", "backend", "bash", "make", "r"))
			if a := agentOf(t, st.Snapshot(), "w-1"); a.Status != StatusAsking || a.Asking != 1 {
				t.Errorf("after the question: %s asking %d", a.Status, a.Asking)
			}
			apply(t, st, b.PermDecide("w-1", "backend", "bash", "make", "ok", true, "user", ""))
			if a := agentOf(t, st.Snapshot(), "w-1"); a.Status == StatusAsking || a.Asking != 0 || !a.Status.Active() {
				t.Errorf("after the answer: %s asking %d", a.Status, a.Asking)
			}
		})
	}
}

// The other ways an agent stops working end its questions as well: a request that failed for good, one that was cancelled, and a
// final answer (no call is held at a question once the model has answered without asking for one).
func TestAnAgentThatStopsWorkingIsNotWaitingOnQuestions(t *testing.T) {
	for _, c := range []struct {
		name string
		stop func(b *statetest.Builder) events.Event
	}{
		{"a request failed", func(b *statetest.Builder) events.Event {
			return b.Emit("w-1", events.TypeModelError, map[string]any{"req": "w-1.1", "error": "provider: server (http 500): boom"})
		}},
		{"a request was cancelled", func(b *statetest.Builder) events.Event {
			return b.Emit("w-1", events.TypeModelError, map[string]any{"req": "w-1.1", "error": "provider: network: request cancelled"})
		}},
		{"a final answer", func(b *statetest.Builder) events.Event {
			p := statetest.ResponsePayload("w-1.1", "m", 10, 0, 0, 5, 0)
			p["stop"] = "end_turn"
			return b.Emit("w-1", events.TypeModelResponse, p)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newB()
			st := New()
			apply(t, st, b.Request("w-1", "w-1.1", "m", "pk", secShared()))
			apply(t, st, b.PermAsk("w-1", "", "bash", "make", "r"))
			apply(t, st, c.stop(b))
			sn := st.Snapshot()
			if a := agentOf(t, sn, "w-1"); a.Asking != 0 || a.Status == StatusAsking || len(sn.Perms.Pending) != 0 || sn.Perms.Abandoned != 1 {
				t.Errorf("status %s asking %d, %d pending, %d abandoned", a.Status, a.Asking, len(sn.Perms.Pending), sn.Perms.Abandoned)
			}
		})
	}
}
