package session_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/perm"
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
			if pushedBack(c) { // asked to search first: it stands by its verdict
				return mock.Reply{Text: `{"verdict":"continue","reason":"nothing was run to show the build works","left":["run go build"]}`}
			}
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

// A judge whose "continue" comes before any evidence request is sent back once to search for what it names as missing, and what it
// finds decides the verdict, though the record it needs is older than the page it was first shown.
func TestAJudgesContinueWithoutASearchIsSentBackToSearch(t *testing.T) {
	var seen []string // what the judge was shown, each call
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if !strings.Contains(c.System, "You judge whether a coding agent") {
			return mock.Reply{Text: "Completed the objective"}
		}
		seen = append(seen, c.LastUser())
		switch len(seen) {
		case 1:
			return mock.Reply{Text: `{"verdict":"continue","reason":"no record of the migration","left":["run the migration"]}`}
		case 2:
			return mock.Reply{Text: `{"verdict":"inspect","query":"migration applied","offset":0}`}
		}
		return mock.Reply{Text: `{"verdict":"done","reason":"the migration applied","left":[]}`}
	})
	s, err := session.New(context.Background(), opts(t, newRepo(t), client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("apply the migration")
	s.SaveGoal(g)
	s.Log.Emit("ts-2", events.TypeTurnAppend, core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("m1", false, core.Text("migration applied"))}})
	for i := 0; i < 30; i++ { // newer records push it off the first page
		s.Log.Emit("ts-2", events.TypeTurnAppend, core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult(fmt.Sprintf("n%d", i), false, core.Text("unrelated output"))}})
	}
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	note, next := s.GoalTurn(context.Background(), g, nil)
	if !g.Done || next != "" || len(seen) != 3 {
		t.Fatalf("after a continue with no search: note %q next %q done=%v calls=%d", note, next, g.Done, len(seen))
	}
	if strings.Contains(seen[0], "migration applied") {
		t.Errorf("the record is on the first page, so no search is needed to find it:\n%s", seen[0])
	}
	if !strings.Contains(seen[1], "search the archive for each item") {
		t.Errorf("the judge was not sent back to search:\n%s", seen[1])
	}
	if !strings.Contains(seen[2], "migration applied") {
		t.Errorf("the judge's search was not served:\n%s", seen[2])
	}
}

// A judge that searched and still finds the goal unmet is believed: its "continue" is not sent back.
func TestAJudgesContinueAfterASearchStands(t *testing.T) {
	calls := 0
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if !strings.Contains(c.System, "You judge whether a coding agent") {
			return mock.Reply{Text: "Completed the objective"}
		}
		if calls++; calls == 1 {
			return mock.Reply{Text: `{"verdict":"inspect","query":"migration applied","offset":0}`}
		}
		return mock.Reply{Text: `{"verdict":"continue","reason":"no record of the migration","left":["run the migration"]}`}
	})
	s, err := session.New(context.Background(), opts(t, newRepo(t), client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("apply the migration")
	s.SaveGoal(g)
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	if _, next := s.GoalTurn(context.Background(), g, nil); g.Done || !strings.Contains(next, "missing: run the migration") || calls != 2 {
		t.Errorf("a continue after a search: next %q done=%v calls=%d", next, g.Done, calls)
	}
}

// A judge request that fails in a way repeating may cure is sent again, so a dropped connection partway through a judgment
// neither pauses the goal nor throws away the evidence requests already answered; after a rate limit it waits as long as the server
// asked.
func TestAJudgeRequestThatFailsOnTheWayIsSentAgain(t *testing.T) {
	defer func(base time.Duration) { agent.RetryBase = base }(agent.RetryBase)
	agent.RetryBase = time.Millisecond
	calls := 0
	var limited time.Time
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if !strings.Contains(c.System, "You judge whether a coding agent") {
			return mock.Reply{Text: "Completed the objective"}
		}
		switch calls++; calls {
		case 1:
			return mock.Reply{Text: `{"verdict":"inspect","query":"migration applied","offset":0}`}
		case 2:
			return mock.Reply{Fault: &mock.Fault{Status: 502, Message: "bad gateway"}}
		case 3:
			limited = time.Now()
			return mock.Reply{Fault: &mock.Fault{Status: 429, Message: "slow down", RetryAfter: time.Second}} // the header is in whole seconds
		}
		if waited := time.Since(limited); waited < time.Second {
			t.Errorf("the retry after a rate limit came %v after it, before the %v the server asked for", waited, time.Second)
		}
		if !strings.Contains(c.LastUser(), "migration applied") {
			t.Errorf("the evidence answered before the failure was lost:\n%s", c.LastUser())
		}
		return mock.Reply{Text: `{"verdict":"done","reason":"the migration applied","left":[]}`}
	})
	s, err := session.New(context.Background(), opts(t, newRepo(t), client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("apply the migration")
	s.SaveGoal(g)
	s.Log.Emit("ts-2", events.TypeTurnAppend, core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("m1", false, core.Text("migration applied"))}})
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	if note, next := s.GoalTurn(context.Background(), g, nil); !g.Done || next != "" || g.Paused != "" || calls != 4 {
		t.Fatalf("after a 502 and a 429: note %q next %q done=%v paused %q calls=%d", note, next, g.Done, g.Paused, calls)
	}
	_ = s.Log.Flush()
	retries := 0
	_ = events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Type == events.TypeModelError && strings.Contains(string(e.Data), "goal:judge") {
			retries++
		}
		return nil
	})
	if retries != 2 {
		t.Errorf("%d judge retries in the log, want 2", retries)
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
		"I will look first (a request is {example} shaped).\n```json\n{\"verdict\":\"inspect\",\"query\":\"go test\",\"offset\":0}\n```",
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
	// an unreadable answer after the last evidence request is no verdict either
	g.Paused = ""
	answers = nil
	for i := 0; i < 24; i++ {
		answers = append(answers, `{"verdict":"inspect","query":"go test","offset":0}`)
	}
	answers = append(answers, "The verdict is", "is", "still")
	if _, next, j := s.GoalJudged(context.Background(), g, nil); j.Judged || next != "" || j.Err == nil || !strings.Contains(j.Err.Error(), "could not be read") {
		t.Errorf("an unreadable last round: next %q judgment %+v", next, j)
	}
}

// Each evidence answer tells the judge how many requests are left, and the last asks for its verdict, which it then gives. A judge
// never told the limit re-read the same records round after round until the judgment ended without a verdict; one that asks for
// more evidence after the last is refused it.
func TestAJudgeIsToldHowManyEvidenceRequestsAreLeft(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	var answers, asked []string
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
	inspect := `{"verdict":"inspect","query":"go test","offset":0}`
	for i := 0; i < 24; i++ {
		answers = append(answers, inspect)
	}
	answers = append(answers, `{"verdict":"continue","reason":"no test ran","left":["run go test"]}`)
	if _, _, j := s.GoalJudged(context.Background(), g, nil); !j.Judged || j.Verdict.Reason != "no test ran" {
		t.Fatalf("judgment %+v", j)
	}
	if len(asked) != 25 || !strings.Contains(asked[1], "(23 evidence requests left.)") || !strings.Contains(asked[21], "(3 evidence requests left; then your final verdict is due.)") ||
		!strings.Contains(asked[24], "No evidence requests are left: reply now with your final verdict") {
		t.Errorf("the judge was told %d times; the 2nd, 22nd and last ended %q / %q / %q", len(asked), tail(asked, 1), tail(asked, 21), tail(asked, 24))
	}
	// a request after the last is not answered: the judgment ends without a verdict
	asked, answers = nil, nil
	for i := 0; i < 25; i++ {
		answers = append(answers, inspect)
	}
	if _, _, j := s.GoalJudged(context.Background(), g, nil); j.Judged || j.Err == nil || !strings.Contains(j.Err.Error(), "inspection limit") || len(asked) != 25 {
		t.Errorf("a request after the last: %d asked, judgment %+v", len(asked), j)
	}
}

// A request the judge already made is answered by pointing at the earlier answer, and a record it already read is not served again:
// a judge re-read one record three times and ran one search twice in a judgment.
func TestAJudgesRepeatedRequestsPointAtTheEarlierAnswer(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	var answers, asked []string
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
		`{"verdict":"inspect","query":"go test"}`,
		`{"verdict":"inspect","query":" go test ","offset":0}`,
		`{"verdict":"inspect","records":[1,2]}`,
		`{"verdict":"inspect","records":[2,3]}`,
		`{"verdict":"inspect","record":3}`,
		`{"verdict":"continue","reason":"no test ran","left":["run go test"]}`,
	}
	if _, _, j := s.GoalJudged(context.Background(), g, nil); !j.Judged || j.Verdict.Reason != "no test ran" {
		t.Fatalf("judgment %+v", j)
	}
	if len(asked) != 6 {
		t.Fatalf("the judge was asked %d times", len(asked))
	}
	if !strings.Contains(asked[2], "You made this same request as request 1") || !strings.Contains(asked[2], "(22 evidence requests left.)") {
		t.Errorf("a repeated search was answered %q", asked[2])
	}
	if strings.Contains(asked[3], "Already shown") {
		t.Errorf("records read for the first time were called repeats: %q", asked[3])
	}
	if !strings.Contains(asked[4], "Already shown above, not repeated: 2 (request 3).") || strings.Contains(asked[4], "Ask for something else") {
		t.Errorf("a request with one new record and one read before was answered %q", asked[4])
	}
	if !strings.Contains(asked[5], "Already shown above, not repeated: 3 (request 4). Ask for something else or give your verdict.") {
		t.Errorf("a record read before was answered %q", asked[5])
	}
}

func tail(s []string, i int) string {
	if i >= len(s) {
		return ""
	}
	return s[i][max(0, len(s[i])-90):]
}

// A goal with milestones is met only when one verdict confirms each with evidence: a judge that says "done" and leaves one out sends the
// agent on to it, the judge's rulings are kept with the goal, and a resumed session has them back with the spec as it was set.
func TestAGoalWithMilestonesIsMetOnlyWhenEachIsConfirmed(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "SPEC.md"), []byte("## Milestones\n- the project builds\n- the tests pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-abcdef")
	var mu sync.Mutex
	var judged []string
	calls := 0
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether a coding agent") {
			mu.Lock()
			defer mu.Unlock()
			judged = append(judged, c.LastUser())
			if len(judged) == 1 {
				return mock.Reply{Text: `{"verdict":"done","reason":"it builds","left":[],"milestones":[{"id":"M1","met":true,"evidence":"record 7: go build passed"}]}`}
			}
			return mock.Reply{Text: `{"verdict":"done","reason":"both shown","left":[],"milestones":[{"id":"M1","met":true,"evidence":"record 7"},{"id":"M2","met":true,"evidence":"record 9: go test passed"}]}`}
		}
		if c.Messages[len(c.Messages)-1].Role == "tool" {
			return mock.Reply{Text: "ran it"}
		}
		mu.Lock()
		calls++
		id := fmt.Sprintf("b%d", calls)
		mu.Unlock()
		return mock.Reply{Text: "running", ToolCalls: []mock.ToolCall{call(id, "bash", map[string]any{"command": "go build ./..."})}}
	})
	o := opts(t, repo, client, model)
	o.Dir = dir
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	g, err := goal.Prepare("ship it\nSpec: SPEC.md", s.Cwd(), s.Root())
	if err != nil {
		t.Fatal(err)
	}
	s.SaveGoal(g)
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	note, next := s.GoalTurn(context.Background(), g, nil)
	if g.Done || next == "" || !strings.Contains(note, "M2") || !strings.Contains(next, "missing: M2: the tests pass") {
		t.Fatalf("done with a milestone unconfirmed: note %q next %q", note, next)
	}
	if !g.Milestones[0].Met || g.Milestones[1].Met {
		t.Errorf("the rulings were not kept: %+v", g.Milestones)
	}
	if !strings.Contains(judged[0], "SPEC (data, the file SPEC.md") || !strings.Contains(judged[0], "- M2 [open] the tests pass") {
		t.Errorf("the judge was not shown the spec and milestones:\n%s", judged[0])
	}
	s.SaveGoal(g)
	s.Close()

	o.Resume = dir
	s2, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	back := s2.LoadGoal()
	if back == nil || back.Spec != "SPEC.md" || !strings.Contains(back.SpecText, "the tests pass") || len(back.Milestones) != 2 || !back.Milestones[0].Met {
		t.Fatalf("the goal after a restart: %+v", back)
	}
	back.Paused = ""
	if _, err := s2.Run(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if note, next := s2.GoalTurn(context.Background(), back, nil); !back.Done || next != "" || !strings.Contains(note, "goal met") {
		t.Errorf("every milestone confirmed: note %q next %q done=%v", note, next, back.Done)
	}
}

// A judge role runs the goal's judge on a model of its own, and its "done" is confirmed by a second judgment on the session's model:
// while the two disagree the agent is sent on with what the confirmation found missing, and the goal is met when both find it met.
func TestAJudgeModelsDoneIsConfirmedOnTheSessionsModel(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	judgedBy := map[string]int{} // judgments per model
	confirmations := 0
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		if !strings.Contains(c.System, "You judge whether a coding agent") {
			return mock.Reply{Text: "it builds"}
		}
		mu.Lock()
		defer mu.Unlock()
		if pushedBack(c) {
			return mock.Reply{Text: `{"verdict":"continue","reason":"no test ran","left":["run go test"]}`}
		}
		judgedBy[c.Model]++ // judgments, not the push-back on one
		if c.Model == "acme/judge" {
			return mock.Reply{Text: `{"verdict":"done","reason":"the build passed (record 3)","left":[]}`}
		}
		if confirmations++; confirmations == 1 {
			return mock.Reply{Text: `{"verdict":"continue","reason":"no test ran","left":["run go test"]}`}
		}
		return mock.Reply{Text: `{"verdict":"done","reason":"build and tests passed","left":[]}`}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: ts.URL}}
	o := session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Offline: true,
		Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
		RoleModels: map[string]string{session.JudgeRole: "gw/acme/judge"},
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var judge session.RoleModel
	for _, r := range s.RoleModels() {
		if r.Role == session.JudgeRole {
			judge = r
		}
	}
	if judge.Model != "gw/acme/judge" || judge.From != "--role-model" {
		t.Errorf("the roles table says the judge runs on %+v", judge)
	}
	g := goal.New("make sure the project builds and its tests pass")
	s.SaveGoal(g)
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	note, next := s.GoalTurn(context.Background(), g, nil)
	if g.Done || !strings.Contains(note, "a second judge did not confirm the goal is met: no test ran") || !strings.Contains(next, "missing: run go test") {
		t.Fatalf("a disputed done: note %q next %q done=%v", note, next, g.Done)
	}
	if _, err := s.Run(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if note, _ := s.GoalTurn(context.Background(), g, nil); !g.Done || note != "goal met: the build passed (record 3)" {
		t.Errorf("an agreed done: note %q done=%v", note, g.Done)
	}
	if judgedBy["acme/judge"] != 2 || judgedBy["acme/main"] != 2 {
		t.Errorf("judgments per model: %v", judgedBy)
	}
}

// pushedBack says a judge call is the harness asking a judge to search before its verdict of continue stands.
func pushedBack(c *mock.Call) bool {
	return strings.Contains(c.LastUser(), "Your verdict came before any evidence request")
}
