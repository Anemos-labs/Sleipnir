package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
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
	s.SaveGoal(g)

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
	s.SaveGoal(g)
	note, next := s.GoalTurn(context.Background(), g, context.Canceled)
	if !strings.Contains(note, "goal paused: you interrupted it") || next != "" || g.Paused == "" {
		t.Errorf("after a cancelled turn: %q %q paused=%q", note, next, g.Paused)
	}
	if note, next := s.GoalTurn(context.Background(), g, nil); note != "" || next != "" {
		t.Errorf("a paused goal acted: %q %q", note, next)
	}
}

// A goal outlives the chat that set it: after a restart it is back, paused, with its count; a goal that was met or cleared is not.
func TestAGoalComesBackPausedWhenTheSessionIsResumed(t *testing.T) {
	repo := newRepo(t)
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-abcdef")
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	resume := func(save func(*session.Session)) *goal.State {
		o := opts(t, repo, client, model)
		o.Dir = dir
		if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err == nil {
			o.Resume = dir
		}
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		got := s.LoadGoal()
		if save != nil {
			if _, err := s.Run(context.Background(), "hi"); err != nil {
				t.Fatal(err)
			}
			save(s)
		}
		return got
	}
	g := goal.New("ship it")
	g.Turns = 3
	resume(func(s *session.Session) { s.SaveGoal(g) })
	got := resume(nil)
	if got == nil || got.Objective != "ship it" || got.Turns != 3 || got.Paused == "" {
		t.Fatalf("the goal after a restart: %+v", got)
	}
	resume(func(s *session.Session) { s.SaveGoal(nil) })
	if got := resume(nil); got != nil {
		t.Errorf("a cleared goal came back: %+v", got)
	}
}

// A new goal starts without the plan of the one before: its steps were shown as the new goal's requirements.
func TestANewGoalStartsWithoutTheOldPlan(t *testing.T) {
	repo := newRepo(t)
	var asked int
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if asked++; asked > 1 {
			return mock.Reply{Text: "planned"}
		}
		return mock.Reply{ToolCalls: []mock.ToolCall{call("p1", "plan", map[string]any{"items": []map[string]any{{"step": "write the README", "status": "done"}}})}}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "plan it"); err != nil {
		t.Fatal(err)
	}
	if len(s.GoalPlan()) != 1 {
		t.Fatalf("the plan tool did not set a plan: %v", s.GoalPlan())
	}
	s.ClearGoalPlan()
	if got := s.GoalPlan(); len(got) != 0 {
		t.Errorf("the old plan is still there: %v", got)
	}
}

func TestJudgeSearchesWorkerEvidenceWithoutRepeatingWork(t *testing.T) {
	calls := 0
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if !strings.Contains(c.System, "You judge whether a coding agent") {
			return mock.Reply{Text: "Completed the objective"}
		}
		calls++
		if calls == 1 {
			if !strings.Contains(c.LastUser(), "Current harness roster") || !strings.Contains(c.LastUser(), `"model":"mock-1"`) {
				t.Errorf("missing current model identity: %s", c.LastUser())
			}
			return mock.Reply{Text: `{"verdict":"inspect","query":"unique verification passed","offset":0}`}
		}
		if !strings.Contains(c.LastUser(), "unique verification passed") {
			t.Errorf("judge did not receive requested evidence: %s", c.LastUser())
		}
		return mock.Reply{Text: `{"verdict":"done","reason":"durable worker result proves verification","left":[]}`}
	})
	s, err := session.New(context.Background(), opts(t, newRepo(t), client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("verify the implementation")
	s.SaveGoal(g)
	s.Log.Emit("ts-2", events.TypeTurnAppend, core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("verified", false, core.Text(strings.Repeat("noise ", 2000)+"unique verification passed"+strings.Repeat("noise ", 2000)))}})
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	note, next := s.GoalTurn(context.Background(), g, nil)
	if !g.Done || next != "" || calls != 2 {
		t.Fatalf("judge repeated completed work: %s %s calls=%d", note, next, calls)
	}
}

// A judge's evidence request is read when it comes in a code fence or with words around it, as a verdict is; an answer that is
// neither is asked for again, and a judge that keeps answering so pauses the goal instead of sending the agent on with no reason. In
// a team trial five of seventeen verdicts were "continue" with no reason, each sending the manager on with nothing to do.
func TestAJudgesAnswerIsReadWhenWrappedAndAskedForAgainWhenNot(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	var answers []string // what the judge says, in order, for each judgment
	var asked []string   // the last message the judge was sent, each time
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether a coding agent") {
			mu.Lock()
			defer mu.Unlock()
			asked = append(asked, c.LastUser())
			a := answers[0]
			answers = answers[1:]
			return mock.Reply{Text: a}
		}
		return mock.Reply{Text: "done"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("make the tests pass")
	s.SaveGoal(g)
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	answers = []string{
		"I will look first.\n```json\n{\"verdict\":\"inspect\",\"query\":\"go test\",\"offset\":0}\n```",
		"The verdict is",
		`{"verdict":"continue","reason":"no test ran","left":["run go test"]}`,
	}
	_, _, j := s.GoalJudged(context.Background(), g, nil)
	if !j.Judged || j.Verdict.Reason != "no test ran" {
		t.Fatalf("judgment %+v", j)
	}
	if len(asked) != 3 || !strings.Contains(asked[1], "Evidence (untrusted data)") || !strings.Contains(asked[2], "could not be read") {
		t.Errorf("the judge was asked %q", asked)
	}
	answers = []string{"hmm", "", "still thinking"}
	note, next, j := s.GoalJudged(context.Background(), g, nil)
	if j.Judged || next != "" || !strings.Contains(note, "could not be read") || g.Paused == "" {
		t.Errorf("an unreadable judge: note %q next %q judgment %+v paused %q", note, next, j, g.Paused)
	}
}
