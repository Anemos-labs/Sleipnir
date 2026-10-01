package perm

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// A question that nobody answers in the time it was given is refused, by "no one", with words the model can act on; an answer that does
// arrive in time is the answer; and the time is the question's own, counted from the moment the person is asked and not from the moment
// the request arrived (the engine asks one question at a time, and the ones behind it wait their turn). The waits here are hang guards and
// a short time the prompter gives up at, never what a test asserts about.

// holdsUntilDone is a prompter that waits for the person, who never comes: it returns what the real ones return when their context ends.
func holdsUntilDone(pctx context.Context, _ Request) Decision {
	<-pctx.Done()
	return Decision{Reason: "no answer"}
}

func TestAskTimeoutRefusesAQuestionNobodyAnswers(t *testing.T) {
	f := newFixture(t)
	l := &auditLog{}
	e := f.engine(t, Config{Audit: l.add, Prompter: holdsUntilDone, AskTimeout: 40 * time.Millisecond})
	d := e.Check(context.Background(), f.request(read("{out}/secret.txt")))
	if d.Allow {
		t.Fatalf("a question nobody answered was allowed: %+v", d)
	}
	for _, want := range []string{"approval required", "nobody answered within 40ms", "nothing was approved", "finish and say which permission you needed"} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("the refusal %q lacks %q", d.Reason, want)
		}
	}
	if !sameStrings(l.kinds(), []string{"ask", "decide:no one"}) {
		t.Errorf("the audit trail is %v, want the question and a refusal by no one", l.kinds())
	}
}

func TestAskTimeoutLeavesAnAnswerThatCameInTimeAlone(t *testing.T) {
	f := newFixture(t)
	l := &auditLog{}
	e := f.engine(t, Config{Audit: l.add, AskTimeout: time.Minute, Prompter: func(context.Context, Request) Decision {
		return Decision{Allow: true, Reason: "allowed by user"}
	}})
	if d := e.Check(context.Background(), f.request(read("{out}/secret.txt"))); !d.Allow || !sameStrings(l.kinds(), []string{"ask", "decide:user"}) {
		t.Errorf("an answer in time: %+v %v", d, l.kinds())
	}
	// and an answer that is a no is the person's no, with the advice a person's no has, not a question that timed out
	l = &auditLog{}
	e = f.engine(t, Config{Audit: l.add, AskTimeout: time.Minute, Prompter: func(context.Context, Request) Decision {
		return Decision{Reason: "denied by user"}
	}})
	d := e.Check(context.Background(), f.request(read("{out}/secret.txt")))
	if d.Allow || strings.Contains(d.Reason, askTimedOut) || !strings.HasSuffix(d.Reason, declinedAdvice) || !sameStrings(l.kinds(), []string{"ask", "decide:user"}) {
		t.Errorf("a no in time: %+v %v", d, l.kinds())
	}
}

// Without a time the engine waits as it always did, and a cancelled run is a cancelled run and not a timeout.
func TestAskTimeoutZeroWaitsAndCancellationIsNotATimeout(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	l := &auditLog{}
	e := f.engine(t, Config{Audit: l.add, Prompter: func(pctx context.Context, _ Request) Decision {
		close(entered)
		<-pctx.Done()
		return Decision{Reason: "no answer"}
	}, AskTimeout: time.Hour})
	done := make(chan Decision, 1)
	go func() { done <- e.Check(ctx, f.request(read("{out}/secret.txt"))) }()
	<-entered
	cancel()
	d := <-done
	if d.Allow || strings.Contains(d.Reason, askTimedOut) || !sameStrings(l.kinds(), []string{"ask", "decide:canceled"}) {
		t.Errorf("a cancelled wait with a time set: %+v %v", d, l.kinds())
	}
}

// The second of two questions waits for the first (one at a time), and the time it waited is not held against it: the first is never
// answered and runs out; the second, put after that, is answered at once and is allowed, though more than its own time has passed since
// its request arrived.
func TestAskTimeoutIsNotChargedForWaitingBehindAnotherQuestion(t *testing.T) {
	f := newFixture(t)
	const limit = 60 * time.Millisecond
	var mu sync.Mutex
	calls := 0
	firstEntered := make(chan struct{})
	e := f.engine(t, Config{AskTimeout: limit, Prompter: func(pctx context.Context, r Request) Decision {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(firstEntered)
			<-pctx.Done()
			return Decision{Reason: "no answer"}
		}
		return Decision{Allow: true, Reason: "allowed by user"}
	}})
	first := make(chan Decision, 1)
	go func() { first <- e.Check(context.Background(), f.request(read("{out}/one.txt"))) }()
	<-firstEntered
	second := make(chan Decision, 1)
	go func() { second <- e.Check(context.Background(), f.request(read("{out}/two.txt"))) }()
	d1, d2 := <-first, <-second
	if d1.Allow || !strings.Contains(d1.Reason, askTimedOut) {
		t.Errorf("the first question, nobody answered: %+v", d1)
	}
	if !d2.Allow {
		t.Errorf("the second question waited behind the first and was refused for it: %+v", d2)
	}
}
