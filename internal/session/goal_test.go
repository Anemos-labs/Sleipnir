package session_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A standing goal is judged on evidence and sent on with what is missing: the agent says it is done having run nothing, the judge says that is
// not evidence and names what is missing, the next turn carries that, and when a command has run the judge finds the goal met.
func TestAGoalIsJudgedOnEvidenceAndSentOnWithWhatIsMissing(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	var judged []string // what the judge was shown, each time
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether a coding agent") {
			mu.Lock()
			defer mu.Unlock()
			judged = append(judged, c.LastUser())
			if len(judged) == 1 {
				return mock.Reply{Text: `{"verdict":"continue","reason":"nothing was run to show the build works","left":["run go build"]}`}
			}
			return mock.Reply{Text: `{"verdict":"done","reason":"go build ran and passed","left":[]}`}
		}
		if strings.Contains(c.LastUser(), "[standing goal, continuation 1") {
			if c.Messages[len(c.Messages)-1].Role == "tool" {
				return mock.Reply{Text: "go build passed"}
			}
			return mock.Reply{Text: "running it", ToolCalls: []mock.ToolCall{call("b1", "bash", map[string]any{"command": "go build ./..."})}}
		}
		return mock.Reply{Text: "I think it is done"} // the first turn: a claim, no work
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("make sure the project builds")

	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	before := s.Cost()
	note, next := s.GoalTurn(context.Background(), g, nil)
	if s.Cost() <= before {
		t.Errorf("the judge's request is not in the session's cost: %v before, %v after", before, s.Cost())
	}
	if !strings.Contains(note, "goal not met yet: nothing was run") || !strings.Contains(note, "continuation 1 of 20") {
		t.Errorf("the note after a claim with no work: %q", note)
	}
	if !strings.Contains(next, "missing: run go build") || !strings.Contains(next, "make sure the project builds") {
		t.Fatalf("the next turn does not say what is missing:\n%s", next)
	}
	if strings.Contains(judged[0], "bash") {
		t.Errorf("the first judgement was shown a command that did not run:\n%s", judged[0])
	}

	if _, err := s.Run(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	note, next = s.GoalTurn(context.Background(), g, nil)
	if note != "goal met: go build ran and passed" || next != "" || !g.Done {
		t.Errorf("after the command ran: note %q next %q done=%v", note, next, g.Done)
	}
	if !strings.Contains(judged[1], "bash") || !strings.Contains(judged[1], "go build") {
		t.Errorf("the second judgement was not shown the command that ran:\n%s", judged[1])
	}
}

// A turn that was interrupted pauses the goal, and one that is paused does nothing until the person goes on.
func TestAnInterruptedTurnPausesTheGoal(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("x")
	note, next := s.GoalTurn(context.Background(), g, context.Canceled)
	if !strings.Contains(note, "goal paused: you interrupted it") || next != "" || g.Paused == "" {
		t.Errorf("after a cancelled turn: %q %q paused=%q", note, next, g.Paused)
	}
	if note, next := s.GoalTurn(context.Background(), g, nil); note != "" || next != "" {
		t.Errorf("a paused goal acted: %q %q", note, next)
	}
}
