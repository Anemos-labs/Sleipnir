package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// stepTools are an edit tool and a read tool that always succeed.
func stepTools() []fakeTool {
	ok := func(json.RawMessage) *tools.Result { return &tools.Result{Text: "ok"} }
	return []fakeTool{{name: "edit", run: ok}, {name: "read", readOnly: true, run: ok}}
}

// stepScript answers each request with the call next returns for its 1-based number, or with a final answer when next
// returns "". It records whether a request carried the wrap-up note, and at which number.
type stepScript struct {
	mu      sync.Mutex
	n       int
	noteAt  int
	noteMsg string
	next    func(n int, sawNote bool) (tool, args string)
}

func (s *stepScript) respond(c *mock.Call) mock.Reply {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.noteAt == 0 {
		for _, m := range c.Messages {
			if strings.Contains(m.Content, "[harness] Your last") {
				s.noteAt, s.noteMsg = s.n, m.Content
			}
		}
	}
	tool, args := s.next(s.n, s.noteAt > 0)
	if tool == "" {
		return mock.Reply{Text: "done"}
	}
	return mock.Reply{ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("c%d", s.n), Name: tool, Args: args}}}
}

func (s *stepScript) requests() (n, noteAt int, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n, s.noteAt, s.noteMsg
}

func stepRig(t *testing.T, window int, extend bool, next func(n int, sawNote bool) (string, string)) (*limRig, *stepScript) {
	t.Helper()
	sc := &stepScript{next: next}
	r := newLimRig(t, func(c *agent.Config) {
		c.MaxSteps, c.ExtendSteps = window, extend
	}, stepTools(), sc.respond)
	return r, sc
}

func newEdit(n int) (string, string) {
	return "edit", fmt.Sprintf(`{"path":"f%d.go","new":"v%d"}`, n, n)
}

// A run that keeps making new edits is not stopped by its step limit: each window it fills with progress is followed by
// another, and the person is told.
func TestAWindowWithNewEditsIsFollowedByAnother(t *testing.T) {
	r, sc := stepRig(t, 10, true, func(n int, _ bool) (string, string) {
		if n > 25 {
			return "", ""
		}
		return newEdit(n)
	})
	res, err := r.agent.Run(context.Background(), "build it")
	if err != nil {
		t.Fatalf("a run that makes progress ended with %v", err)
	}
	if n, noteAt, _ := sc.requests(); n != 26 || res.Steps != 26 || noteAt != 0 {
		t.Fatalf("requests %d, steps %d, wrap-up note at request %d; want 26, 26 and none", n, res.Steps, noteAt)
	}
	var extended int
	for _, n := range r.sink.all() {
		if strings.HasPrefix(n, "info: ") && strings.Contains(n, "new edits in the last 10: the run goes on for another 10") {
			extended++
		}
	}
	if extended != 2 {
		t.Fatalf("notices %q: want two windows extended", r.sink.all())
	}
}

// A window without progress is followed by a few steps to wrap up, announced to the agent in the results of the window's
// last step, and then the run ends at the step limit as it always did.
func TestAWindowWithoutProgressEndsAfterAWrapUp(t *testing.T) {
	r, sc := stepRig(t, 20, true, func(int, bool) (string, string) { return "read", `{}` })
	_, err := r.agent.Run(context.Background(), "look around")
	if !errors.Is(err, agent.ErrStepLimit) {
		t.Fatalf("err = %v, want the step limit", err)
	}
	n, noteAt, note := sc.requests()
	if n != 22 || noteAt != 22 {
		t.Fatalf("requests %d, note seen by request %d; want 22 requests (20 and 2 to wrap up), the note in the last", n, noteAt)
	}
	for _, want := range []string{"Your last 20 steps made no change to any file", "This run ends after 1 more step.",
		"give your answer: what is done, what is left"} {
		if !strings.Contains(note, want) {
			t.Fatalf("wrap-up note %q lacks %q", note, want)
		}
	}
	stuck := eventData(t, r.log, events.TypeAgentStuck)
	if len(stuck) != 1 || stuck[0]["phase"] != "steps" {
		t.Fatalf("agent.stuck events %v: want one of phase steps", stuck)
	}
}

// An agent told to wrap up that answers ends its run the ordinary way, and the instruction it got is the one its caller set.
func TestAnAgentThatWrapsUpEndsCleanly(t *testing.T) {
	sc := &stepScript{}
	sc.next = func(_ int, sawNote bool) (string, string) {
		if sawNote {
			return "", ""
		}
		return "read", `{}`
	}
	r := newLimRig(t, func(c *agent.Config) {
		c.MaxSteps, c.ExtendSteps, c.WrapUp = 50, true, "Hand the task over."
	}, stepTools(), sc.respond)
	if _, err := r.agent.Run(context.Background(), "look around"); err != nil {
		t.Fatalf("an agent that answered after the wrap-up note ended with %v", err)
	}
	n, noteAt, note := sc.requests()
	if n != 52 || noteAt != 52 || !strings.Contains(note, "This run ends after 4 more steps. ") || !strings.HasSuffix(strings.TrimSpace(note), "Hand the task over.") {
		t.Fatalf("requests %d, note at %d: %q", n, noteAt, note)
	}
}

// Writing the same two versions of a file in turn is not progress: only edits the run has not made before count.
func TestRepeatedEditsDoNotExtendARun(t *testing.T) {
	r, sc := stepRig(t, 10, true, func(n int, _ bool) (string, string) { return newEdit(n % 2) })
	_, err := r.agent.Run(context.Background(), "build it")
	if !errors.Is(err, agent.ErrStepLimit) {
		t.Fatalf("err = %v, want the step limit", err)
	}
	if n, noteAt, note := sc.requests(); n != 12 || noteAt != 12 || !strings.Contains(note, "made only 2 new edits") {
		t.Fatalf("requests %d, note at %d: %q; want 12 requests and the note in the last", n, noteAt, note)
	}
}

// Without ExtendSteps the limit is a ceiling, whatever the agent is doing: a rollout's budget is part of its task.
func TestWithoutExtendStepsTheLimitIsACeiling(t *testing.T) {
	r, sc := stepRig(t, 10, false, func(n int, _ bool) (string, string) { return newEdit(n) })
	_, err := r.agent.Run(context.Background(), "build it")
	if !errors.Is(err, agent.ErrStepLimit) {
		t.Fatalf("err = %v, want the step limit", err)
	}
	if n, noteAt, _ := sc.requests(); n != 10 || noteAt != 0 {
		t.Fatalf("requests %d, note at %d; want 10 and none", n, noteAt)
	}
	if len(r.sink.all()) != 0 || len(eventData(t, r.log, events.TypeAgentStuck)) != 0 {
		t.Fatalf("a fixed limit said something: notices %q", r.sink.all())
	}
}

// The note counts the edits of the window that ended, not one the first wrap-up step made after it.
func TestTheWrapUpNoteCountsTheWindowThatEnded(t *testing.T) {
	r, sc := stepRig(t, 10, true, func(n int, _ bool) (string, string) {
		if n <= 2 || n == 11 {
			return newEdit(n)
		}
		return "read", `{}`
	})
	if _, err := r.agent.Run(context.Background(), "build it"); !errors.Is(err, agent.ErrStepLimit) {
		t.Fatalf("err = %v, want the step limit", err)
	}
	if _, noteAt, note := sc.requests(); noteAt != 12 || !strings.Contains(note, "Your last 10 steps made only 2 new edits.") {
		t.Fatalf("note at %d: %q", noteAt, note)
	}
}
