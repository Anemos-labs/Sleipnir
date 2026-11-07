package swarm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The note and mail tools, as the model calls them: what it is told comes back from the board and the router, and a tool that is not the
// agent's to use says so in words that name the way out.

func TestNoteToolRecordsAFactAndSaysTheSameThingOnceMore(t *testing.T) {
	s := secRevSwarm()
	note := secRevTool(t, s, "note")
	call := func(agent, role string, in map[string]any) (string, bool) {
		r := secRevCall(t, note, agent, role, in)
		return r.Text, r.IsError
	}
	text, bad := call("be-1", "backend", map[string]any{"text": "tests need -tags=integration"})
	if bad || !strings.HasPrefix(text, "noted (#") {
		t.Fatalf("a first note: %q (error %v)", text, bad)
	}
	// the default scope is the whole team's, whichever role found it, and the same fact from another agent is the same note
	again, bad := call("fe-1", "frontend", map[string]any{"text": "tests need -tags=integration", "scope": "shared"})
	if bad || again != text {
		t.Errorf("the same shared fact from another agent: %q (error %v), want %q", again, bad, text)
	}
	snap := s.Board.Snapshot()
	if len(snap.Notes) != 1 || snap.Notes[0].Scope != "shared" || snap.Notes[0].Role != "" || snap.Notes[0].From != "be-1" {
		t.Errorf("the board has %+v, want one shared note by the first to find it", snap.Notes)
	}
	// a role's note belongs to that role: the same words from another role are another note
	if _, bad := call("be-1", "backend", map[string]any{"text": "use make, not go build", "scope": "role"}); bad {
		t.Fatal("a role note was refused")
	}
	if _, bad := call("fe-1", "frontend", map[string]any{"text": "use make, not go build", "scope": "role"}); bad {
		t.Fatal("the same role note from another role was refused")
	}
	if n := len(s.Board.Snapshot().Notes); n != 3 {
		t.Errorf("%d notes, want 3 (one shared, one of each role)", n)
	}
}

func TestNoteToolRefusesWhatIsNotAFact(t *testing.T) {
	s := secRevSwarm()
	note := secRevTool(t, s, "note")
	for _, tc := range []struct {
		name string
		in   map[string]any
		role string
		want string
	}{
		{"empty", map[string]any{"text": "   "}, "backend", "empty note"},
		{"another scope", map[string]any{"text": "x", "scope": "everyone"}, "backend", "scope must be shared or role"},
		{"not JSON members", map[string]any{"text": 7}, "backend", "invalid"},
	} {
		r := secRevCall(t, note, "a-1", tc.role, tc.in)
		if !r.IsError || !strings.Contains(strings.ToLower(r.Text), strings.ToLower(tc.want)) {
			t.Errorf("%s: %q (error %v), want a refusal that says %q", tc.name, r.Text, r.IsError, tc.want)
		}
	}
	if n := len(s.Board.Snapshot().Notes); n != 0 {
		t.Errorf("%d notes were left by refusals", n)
	}
}

// The mailman only delivers: with the mailman on, a note from it is refused and says what its one tool is for; with it off there is no such role.
func TestNoteToolIsNotTheMailmans(t *testing.T) {
	s := New(Config{SessionID: "sec", Mailman: true}, Deps{}, nil)
	r := secRevCall(t, secRevTool(t, s, "note"), "mm-1", "mailman", map[string]any{"text": "a fact"})
	if !r.IsError || !strings.Contains(r.Text, "only delivers mail") || !strings.Contains(r.Text, "use the mail tool") {
		t.Errorf("a note from the mailman: %q (error %v)", r.Text, r.IsError)
	}
	if n := len(s.Board.Snapshot().Notes); n != 0 {
		t.Errorf("%d notes after a refusal", n)
	}
}

// What one agent can add is what every agent's prompt carries: the cap per agent is said, with the way out.
func TestNoteToolStopsAnAgentThatFloodsTheTeamsContext(t *testing.T) {
	s := secRevSwarm()
	note := secRevTool(t, s, "note")
	var last string
	for i := 0; i < 50; i++ {
		r := secRevCall(t, note, "chatty", "backend", map[string]any{"text": "fact number " + string(rune('a'+i%26)) + string(rune('A'+i/26))})
		if r.IsError {
			last = r.Text
			break
		}
	}
	if !strings.Contains(last, "8 notes on the board (#1,") || !strings.Contains(last, "withdraw=<id>") || !strings.Contains(last, "fold your facts into one note") {
		t.Fatalf("an agent that adds notes without end was told %q", last)
	}
	// Withdrawing one makes room: nothing else ever would.
	if r := secRevCall(t, note, "chatty", "backend", map[string]any{"withdraw": 3}); r.IsError || !strings.Contains(r.Text, "withdrew note #3") {
		t.Fatalf("withdraw: %q (error %v)", r.Text, r.IsError)
	}
	if r := secRevCall(t, note, "chatty", "backend", map[string]any{"text": "the corrected fact"}); r.IsError {
		t.Fatalf("a note after a withdrawal: %q", r.Text)
	}
}

// A note is withdrawn only by its author, and a wrong id names the agent's own notes.
func TestNoteWithdrawIsTheAuthorsAndSaysWhichNotesAreYours(t *testing.T) {
	s := secRevSwarm()
	note := secRevTool(t, s, "note")
	secRevCall(t, note, "be-1", "backend", map[string]any{"text": "tests run with npm test"})
	secRevCall(t, note, "be-2", "backend", map[string]any{"text": "the CLI is bin/cronx"})
	r := secRevCall(t, note, "be-2", "backend", map[string]any{"withdraw": 1})
	if !r.IsError || !strings.Contains(r.Text, "be-1's") {
		t.Errorf("withdrawing another's note: %q (error %v)", r.Text, r.IsError)
	}
	r = secRevCall(t, note, "be-2", "backend", map[string]any{"withdraw": 9})
	if !r.IsError || !strings.Contains(r.Text, "yours are #2") {
		t.Errorf("withdrawing a note that is not there: %q (error %v)", r.Text, r.IsError)
	}
	if n := len(s.Board.Snapshot().Notes); n != 2 {
		t.Errorf("%d notes after refused withdrawals, want 2", n)
	}
	if r := secRevCall(t, note, "be-2", "backend", map[string]any{"withdraw": 2}); r.IsError {
		t.Fatalf("withdrawing one's own note: %q", r.Text)
	}
	if ns := s.Board.Snapshot().Notes; len(ns) != 1 || ns[0].ID != 1 {
		t.Errorf("notes after the withdrawal: %+v", ns)
	}
}

// A field the note tool does not have is refused and adds nothing: in a real run {"action": "remove", "text": ...} was taken as a
// new note with the text meant to be removed, and the agent was told "noted".
func TestNoteToolRefusesAFieldItDoesNotHave(t *testing.T) {
	s := secRevSwarm()
	r := secRevCall(t, secRevTool(t, s, "note"), "be-1", "backend", map[string]any{"action": "remove", "text": "a stale fact"})
	if !r.IsError || !strings.Contains(r.Text, `no field "action"`) || !strings.Contains(r.Text, "withdraw=<id>") {
		t.Errorf("a note with an action: %q (error %v)", r.Text, r.IsError)
	}
	if n := len(s.Board.Snapshot().Notes); n != 0 {
		t.Errorf("%d notes after a refused call", n)
	}
}

func TestMailToolSendsToAnAgentAndSaysNoToOneThatIsNotThere(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 2}, func(ctx context.Context, c *rvCall) rvReply {
		time.Sleep(300 * time.Millisecond) // the worker is there while the test mails it
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "API", Files: []string{"api/**"}, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res := r.callTool(ctx, "mail", "mgr", "manager", map[string]any{"to": id, "text": "the contract changed", "kind": "contract"})
	if res.IsError || !strings.HasPrefix(res.Text, "sent ") || !strings.Contains(res.Text, " to "+id) {
		t.Errorf("a mail to a worker: %q (error %v)", res.Text, res.IsError)
	}
	for name, in := range map[string]map[string]any{
		"an agent that is not there": {"to": "nobody-9", "text": "hello"},
		"a kind that is not one":     {"to": id, "text": "hello", "kind": "shout"},
		"nothing to say":             {"to": id, "text": "  "},
	} {
		if res := r.callTool(ctx, "mail", "mgr", "manager", in); !res.IsError {
			t.Errorf("%s: sent: %q", name, res.Text)
		}
	}
}

// Widening a parent does not widen its open children, and the manager is told which children keep their own scopes: in a team run
// the manager widened T4 for a file, T9's worker was refused writing it, and T9 needed a second widening.
func TestWideningAParentNamesTheChildrenThatKeepTheirScopes(t *testing.T) {
	r := newPoolRig(t)
	if err := r.sw.Board.poolUpdate("mgr", r.parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	kid, err := r.sw.Board.CreateTask(r.lead, TaskSpec{Parent: r.parent.ID, Title: "pages", Role: "backend", Files: []string{"app/pages/**"}})
	if err != nil {
		t.Fatal(err)
	}
	got := r.must(r.task("mgr", map[string]any{"action": "update", "id": r.parent.ID, "files": []string{"app/**", "types/**"}}), "widen the parent")
	if !strings.Contains(got, "keep their own scopes") || !strings.Contains(got, kid.ID+" (todo: app/pages/**)") || !strings.Contains(got, "widen that child too") {
		t.Fatalf("widening a parent with an open child: %q", got)
	}
	got = r.must(r.task("mgr", map[string]any{"action": "update", "id": kid.ID, "files": []string{"app/pages/**", "types/**"}}), "widen the child")
	if got != "updated "+kid.ID {
		t.Fatalf("a task with no children: %q", got)
	}
}

// A review task created with no deps while implementation work is open is told what it will not see; one that waits for that
// work, and one created once nothing is open, are not.
func TestAReviewThatWaitsForNothingNamesTheOpenWork(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "ok"} })
	create := func(spec map[string]any) string {
		spec["action"] = "create"
		return r.callTool(context.Background(), "task", "mgr", "manager", spec).Text
	}
	create(map[string]any{"title": "store", "role": "backend", "description": "the store"})
	create(map[string]any{"title": "web ui", "role": "frontend", "description": "the ui"})
	got := create(map[string]any{"title": "review", "role": "reviewer", "description": "review the result"})
	if !strings.Contains(got, "It waits for nothing while T1 and T2 are still open") || !strings.Contains(got, "task revise id=T3 deps=[...]") {
		t.Fatalf("a depless review was told %q", got)
	}
	if got := create(map[string]any{"title": "review 2", "role": "reviewer", "description": "review", "deps": []string{"T1", "T2"}}); strings.Contains(got, "waits for nothing") {
		t.Fatalf("a review with deps was told %q", got)
	}
}
