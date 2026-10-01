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
	if !strings.Contains(last, "proposed notes waiting") || !strings.Contains(last, "fold your facts into one note") {
		t.Fatalf("an agent that adds notes without end was told %q", last)
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
