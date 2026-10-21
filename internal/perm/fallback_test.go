package perm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Bypass mode asks nothing: what would have been a question (the high-risk class, an ask rule) is refused, and the refusal says what
// flagged it and asks for a safer way. The person is never prompted.
func TestBypassRefusesWhatWouldHaveAsked(t *testing.T) {
	f := newFixture(t)
	var prompted int32
	e := f.engine(t, Config{Mode: ModeBypass, Ask: []string{"Bash(git push:*)"},
		Prompter: func(context.Context, Request) Decision { atomic.AddInt32(&prompted, 1); return Decision{Allow: true} }})
	for _, c := range []struct{ cmd, why string }{
		{"sudo ls", "elevated privileges"},
		{"git push origin main", "rule Bash(git push:*)"},
	} {
		d := e.Check(context.Background(), f.request(bash(c.cmd)))
		if d.Allow || !strings.HasPrefix(d.Reason, BypassRefusal) || !strings.Contains(d.Reason, c.why) || !strings.Contains(d.Reason, "safer way") {
			t.Errorf("%s: %+v, want a refusal that names %q and asks for a safer way", c.cmd, d, c.why)
		}
		if c := e.Classify(f.request(bash(c.cmd))); c.Verdict != Deny || !strings.HasPrefix(c.Why, BypassRefusal) {
			t.Errorf("Classify(%s) = %+v, want the same refusal", c.Why, c)
		}
	}
	if d := e.Check(context.Background(), f.request(bash("go build ./..."))); !d.Allow {
		t.Errorf("an ordinary command: %+v", d)
	}
	if prompted != 0 {
		t.Errorf("the person was asked %d times", prompted)
	}
}

// A question the person leaves unanswered for FallbackAfter goes to the fallback (the manager) when its danger is at most
// FallbackUpTo: the fallback's answer is the decision, the person's question is withdrawn saying who answered and how, and the
// audit names the fallback. A more dangerous question keeps waiting for the person.
func TestTheManagerAnswersAQuestionThePersonLeft(t *testing.T) {
	f := newFixture(t)
	var settled atomic.Pointer[Settled]
	prompter := func(ctx context.Context, r Request) Decision {
		<-ctx.Done() // the person is away
		if s := (*Settled)(nil); errors.As(context.Cause(ctx), &s) {
			settled.Store(s)
		}
		return Decision{Reason: "no answer"}
	}
	var asked atomic.Int32
	var audits []Audit
	e := f.engine(t, Config{Mode: ModeAcceptEdits, Prompter: prompter, FallbackAfter: 20 * time.Millisecond, FallbackUpTo: DangerMedium,
		Fallback: func(ctx context.Context, r Request) (Decision, string) {
			asked.Add(1)
			return Decision{Allow: true, Reason: "approved by the manager"}, "manager"
		},
		Audit: func(a Audit) { audits = append(audits, a) }})

	// medium: a file outside the workspace
	d := e.Check(context.Background(), f.request(write("{out}/x.txt")))
	if !d.Allow || d.Reason != "approved by the manager" || asked.Load() != 1 {
		t.Fatalf("medium question: %+v, manager asked %d", d, asked.Load())
	}
	if s := settled.Load(); s == nil || !s.Allow || s.By != "manager" {
		t.Errorf("the person's question ended with %+v, want Settled{Allow: true, By: manager}", s)
	}
	if last := audits[len(audits)-1]; last.Kind != "decide" || last.By != "manager" {
		t.Errorf("audit %+v, want a decision by the manager", last)
	}

	// high: sudo is beyond medium, so it is not the manager's; with a deadline for the test, it ends unanswered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	d = e.Check(ctx, f.request(bash("sudo ls")))
	if d.Allow || asked.Load() != 1 {
		t.Errorf("high question: %+v, manager asked %d times in all, want once", d, asked.Load())
	}
}

// The person's own answer stands when it comes first: the fallback is never consulted.
func TestThePersonsAnswerBeatsTheFallback(t *testing.T) {
	f := newFixture(t)
	var asked atomic.Int32
	e := f.engine(t, Config{Mode: ModeAcceptEdits, FallbackAfter: time.Hour, FallbackUpTo: DangerHigh,
		Prompter: func(context.Context, Request) Decision { return Decision{Allow: true} },
		Fallback: func(context.Context, Request) (Decision, string) { asked.Add(1); return Decision{}, "manager" }})
	if d := e.Check(context.Background(), f.request(write("{out}/x.txt"))); !d.Allow || asked.Load() != 0 {
		t.Errorf("%+v, fallback asked %d", d, asked.Load())
	}
}

// The danger of a question: routine is low; the high-risk class, guarded paths and the person's ask rules are high; the rest medium.
func TestTheDangerOfAQuestion(t *testing.T) {
	for _, c := range []struct {
		v    verdict
		r    Request
		want Danger
	}{
		{verdict{kind: vAsk, routine: true}, Request{}, DangerLow},
		{verdict{kind: vAsk}, Request{}, DangerMedium},
		{verdict{kind: vAsk, askRule: true}, Request{}, DangerHigh},
		{verdict{kind: vAsk, tier: TierGuarded}, Request{}, DangerHigh},
		{verdict{kind: vAsk}, Request{Risk: RiskHigh}, DangerHigh},
	} {
		if got := c.v.danger(c.r); got != c.want {
			t.Errorf("%+v %+v: %s, want %s", c.v, c.r, got, c.want)
		}
	}
	for _, s := range []string{"", "low", "Medium", " high "} {
		if _, err := ParseDanger(s); err != nil {
			t.Errorf("ParseDanger(%q): %v", s, err)
		}
	}
	if _, err := ParseDanger("extreme"); err == nil {
		t.Error("ParseDanger(extreme) took it")
	}
}
