package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// A response that the output limit cut off, with no call to run, is the start of something and not an answer. The first
// benchmark of a real model recorded a 16,000-token generation that ended at the limit as the run's final answer, claimed "done".

// nudges counts the harness's "carry on" turns in the thread, and says whether each is a user-role turn of system origin: the
// nudge is not the person's word, and must not be pinned into the instructions as if it were.
func nudges(t *testing.T, a *agent.Agent) int {
	t.Helper()
	n := 0
	for _, tr := range a.Thread().Snapshot().Turns {
		for _, b := range tr.Blocks {
			if strings.Contains(b.Text, "cut off at the output limit") {
				n++
				if tr.Origin != core.OriginSystem || tr.Role != core.RoleUser {
					t.Errorf("the nudge is a user-role turn of system origin, got %s/%s", tr.Role, tr.Origin)
				}
			}
		}
	}
	return n
}

func TestAnAnswerCutOffAtTheOutputLimitIsNotTheEndOfTheRun(t *testing.T) {
	var mu sync.Mutex
	var requests int
	var second []mock.Msg
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests == 1 {
			return mock.Reply{Text: "The first half of what I wanted to say, and then", Finish: "length"}
		}
		second = c.Messages
		return mock.Reply{Text: "the rest of it."}
	})
	res, err := r.agent.Run(context.Background(), "explain the cache")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("%d requests, want 2: the cut-off response, and the model asked to carry on", requests)
	}
	if res.Text != "the rest of it." || res.Stop != core.StopEnd {
		t.Errorf("result %q (stop %s): the run ends with the answer that was finished, not the one that was cut off", res.Text, res.Stop)
	}
	if got := nudges(t, r.agent); got != 1 {
		t.Errorf("%d nudges in the thread, want 1", got)
	}
	// What the model reads on its second request: its own cut-off text, and then why it is being asked again.
	var sawPartial bool
	for _, m := range second {
		if m.Role == "assistant" && strings.Contains(m.Content, "The first half of what I wanted to say") {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Errorf("the model was not shown its own cut-off response: %+v", second)
	}
	if last := second[len(second)-1]; last.Role != "user" || !strings.Contains(last.Content, "[harness]") || !strings.Contains(last.Content, "tool call") {
		t.Errorf("the last message is %+v: the nudge says what happened and what to do instead", last)
	}
}

func TestAModelThatIsCutOffAgainAndAgainEndsTheRunWithAnErrorAndNotAnAnswer(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		requests++
		return mock.Reply{Text: "I'll implement it: the plan is, and then the plan is, and then", Finish: "length"}
	})
	res, err := r.agent.Run(context.Background(), "build it")
	if !errors.Is(err, agent.ErrOutputLimit) {
		t.Fatalf("err = %v, want ErrOutputLimit: a run whose every response was cut off did not finish", err)
	}
	// The first response and two more, each after a nudge: then the run says what happened.
	if requests != 3 {
		t.Errorf("%d requests, want 3 (a model that is cut off again and again is not asked for ever)", requests)
	}
	if want := "512 tokens, 3 responses in a row"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to name the limit and the count (%q)", err, want)
	}
	if res == nil || !strings.HasPrefix(res.Text, "I'll implement it") || res.Stop != core.StopMaxTokens {
		t.Errorf("result %+v: the last cut-off text and its stop reason stay visible to the caller", res)
	}
	if got := nudges(t, r.agent); got != 2 {
		t.Errorf("%d nudges in the thread, want 2", got)
	}
}

// The count is of responses in a row: a response that was not cut off starts it again, so a long task with the odd long answer
// does not use up the allowance across the whole run.
func TestTheCutOffCountStartsAgainAfterAResponseThatWasNot(t *testing.T) {
	var mu sync.Mutex
	script := []mock.Reply{
		{Text: "part one, and then", Finish: "length"},
		{Text: "part two, and then", Finish: "length"},
		{Text: "looking", ToolCalls: []mock.ToolCall{{ID: "c1", Name: "echo", Args: `{"n":1}`}}},
		{Text: "part three, and then", Finish: "length"},
		{Text: "part four, and then", Finish: "length"},
		{Text: "all of it is done."},
	}
	i := 0
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		rep := script[min(i, len(script)-1)]
		i++
		return rep
	})
	res, err := r.agent.Run(context.Background(), "a long job")
	if err != nil {
		t.Fatalf("err = %v: four cut-off responses, but never three in a row", err)
	}
	if res.Text != "all of it is done." || i != len(script) {
		t.Errorf("result %q after %d requests, want the last of %d", res.Text, i, len(script))
	}
	if got := nudges(t, r.agent); got != 4 {
		t.Errorf("%d nudges in the thread, want 4", got)
	}
}

// A call that was cut off in the middle is not this: its arguments do not parse, the dispatcher says so to the model, and the
// model retries it. Nothing is added to the thread but the answer to the call.
func TestACallCutOffInTheMiddleIsNotAnAnswerThatWasCutOff(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests == 1 {
			return mock.Reply{Text: "writing the file", ToolCalls: []mock.ToolCall{{ID: "c1", Name: "echo", Args: `{"content": "a very long`}}, Finish: "length"}
		}
		return mock.Reply{Text: "done"}
	})
	if _, err := r.agent.Run(context.Background(), "write it"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Errorf("%d requests, want 2", requests)
	}
	if got := nudges(t, r.agent); got != 0 {
		t.Errorf("%d cut-off nudges for a call that was cut off in the middle, want 0", got)
	}
}

// The person watching sees the text stop in the middle of a sentence and then more arrive: the notice says why.
func TestThePersonIsToldWhyTheTextStoppedAndWentOn(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	r := newLimRig(t, func(c *agent.Config) { c.NoCompaction = true }, nil, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests == 1 {
			return mock.Reply{Text: "half of it, and then", Finish: "length"}
		}
		return mock.Reply{Text: "the other half."}
	})
	if _, err := r.agent.Run(context.Background(), "say it all"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range r.sink.all() {
		if strings.Contains(n, "output limit") {
			got = append(got, n)
		}
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "warn: ") || !strings.Contains(got[0], "(512 tokens)") || !strings.Contains(got[0], "1 of 2") {
		t.Errorf("notices about the output limit = %q, want one warning that names the limit and the round", got)
	}
}
