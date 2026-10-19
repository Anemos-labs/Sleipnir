package perm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fetchOf is a web_fetch request of url.
func fetchOf(url string) Request { return fetchReq(`{"url":"` + url + `"}`) }

// A question about a host's network access that the person refused, or that nobody answered, is not put to them again in the
// session: not for the same URL, not for another URL of that host. Another host is still asked about.
func TestARefusedOrUnansweredHostIsNotAskedAgain(t *testing.T) {
	for _, answer := range []string{"no answer", "denied by user"} {
		t.Run(answer, func(t *testing.T) {
			f := newFixture(t)
			rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Reason: answer} }}
			e := askEngine(t, f, Config{}, rec.prompt)
			first := e.Check(bg, fetchOf("https://soulver.io/"))
			if first.Allow || rec.count() != 1 {
				t.Fatalf("first question: %+v after %d prompts", first, rec.count())
			}
			for _, u := range []string{"https://soulver.io/", "https://soulver.io/docs?page=2", "http://SOULVER.IO/x"} {
				d := e.Check(bg, fetchOf(u))
				if d.Allow || rec.count() != 1 {
					t.Fatalf("%s: %+v; prompts %d, want 1", u, d, rec.count())
				}
				if d.Reason != askedEarlier+first.Reason {
					t.Errorf("%s: reason %q, want the earlier refusal %q after %q", u, d.Reason, first.Reason, askedEarlier)
				}
			}
			if d := e.Check(bg, fetchOf("https://en.wikipedia.org/wiki/Soulver")); d.Allow || rec.count() != 2 {
				t.Fatalf("another host: %+v; prompts %d, want 2", d, rec.count())
			}
		})
	}
}

// Any other refused request is not asked again when it is the same request (promptKey); a different one is asked.
func TestARefusedCommandIsNotAskedAgain(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Reason: "no answer"} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("make")))
	if d := e.Check(bg, f.request(bash("make"))); d.Allow || !strings.HasPrefix(d.Reason, askedEarlier) || rec.count() != 1 {
		t.Fatalf("repeat: %+v; prompts %d", d, rec.count())
	}
	e.Check(bg, f.request(bash("make install")))
	if rec.count() != 2 {
		t.Fatalf("a different command was not asked about: prompts %d", rec.count())
	}
}

// A yes is not recorded as an earlier outcome: one given once is asked for again, and one given for the session becomes a rule
// (remember), so that the host's other URLs pass without a question.
func TestAnApprovalKeepsTheRememberBehavior(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Allow: true} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	for i := 1; i <= 2; i++ {
		if d := e.Check(bg, fetchOf("https://soulver.io/")); !d.Allow || rec.count() != i {
			t.Fatalf("approval once #%d: %+v; prompts %d", i, d, rec.count())
		}
	}
	rec.answer = func(int, Request) Decision { return Decision{Allow: true, Remember: ScopeSession} }
	if d := e.Check(bg, fetchOf("https://soulver.io/")); !d.Allow || rec.count() != 3 {
		t.Fatalf("approval for the session: %+v; prompts %d", d, rec.count())
	}
	if d := e.Check(bg, fetchOf("https://soulver.io/other")); !d.Allow || rec.count() != 3 || strings.HasPrefix(d.Reason, askedEarlier) {
		t.Fatalf("after the session approval: %+v; prompts %d", d, rec.count())
	}
}

// Changing the mode or the rules lets an unanswered question be asked again: the answer may now be another.
func TestAChangeOfModeOrRulesAsksAgain(t *testing.T) {
	f := newFixture(t)
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Reason: "no answer"} }}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, fetchOf("https://soulver.io/"))
	e.SetMode(e.Mode()) // no change
	e.Check(bg, fetchOf("https://soulver.io/"))
	if rec.count() != 1 {
		t.Fatalf("asked again without a change: prompts %d", rec.count())
	}
	other := ModeAcceptEdits
	if e.Mode() == ModeAcceptEdits {
		other = ModeDefault
	}
	e.SetMode(other)
	e.Check(bg, fetchOf("https://soulver.io/"))
	if rec.count() != 2 {
		t.Fatalf("not asked again after the mode changed: prompts %d", rec.count())
	}
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "Read"})
	e.Check(bg, fetchOf("https://soulver.io/"))
	if rec.count() != 3 {
		t.Fatalf("not asked again after a rule was added: prompts %d", rec.count())
	}
	if !e.RemoveRule("Read") {
		t.Fatal("the rule was not removed")
	}
	e.Check(bg, fetchOf("https://soulver.io/"))
	if rec.count() != 4 {
		t.Fatalf("not asked again after a rule was removed: prompts %d", rec.count())
	}
}

// A question that was on screen while the mode changed records nothing when it ends unanswered: the reset holds, and the next
// matching request is asked about again.
func TestAResetDuringAQuestionKeepsTheRecordCleared(t *testing.T) {
	f := newFixture(t)
	shown, release := make(chan struct{}), make(chan struct{})
	rec := &promptRecorder{answer: func(n int, _ Request) Decision {
		if n == 1 {
			close(shown)
			<-release
		}
		return Decision{Reason: "no answer"}
	}}
	e := askEngine(t, f, Config{}, rec.prompt)
	done := make(chan Decision)
	go func() { done <- e.Check(bg, fetchOf("https://soulver.io/")) }()
	<-shown
	other := ModeAcceptEdits
	if e.Mode() == ModeAcceptEdits {
		other = ModeDefault
	}
	e.SetMode(other)
	close(release)
	if d := <-done; d.Allow {
		t.Fatalf("the unanswered question was allowed: %+v", d)
	}
	if d := e.Check(bg, fetchOf("https://soulver.io/page")); d.Allow || strings.HasPrefix(d.Reason, askedEarlier) || rec.count() != 2 {
		t.Fatalf("after a reset during the question: %+v; prompts %d, want 2", d, rec.count())
	}
}

// The person's refusal wins over the manager: a routine question the person refused is not handed to the delegate again, and the
// repeat is recorded as one decision, with no question.
func TestARefusalByThePersonIsNotDelegatedAgain(t *testing.T) {
	f := newFixture(t)
	trees := filepath.Join(f.base, "cache", "wt", "s1")
	wt := filepath.Join(trees, "be-1")
	if err := os.MkdirAll(filepath.Join(wt, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	delegated := 0
	l := &auditLog{}
	rec := &promptRecorder{answer: func(int, Request) Decision { return Decision{Reason: "denied by user"} }}
	e := f.engine(t, Config{Mode: ModeAcceptEdits, TreeParents: []string{trees}, Audit: l.add, Prompter: rec.prompt,
		Delegate: func(context.Context, Request) (Decision, string) {
			delegated++
			if delegated == 1 {
				return Decision{}, "" // passed on to the person
			}
			return Decision{Allow: true, Reason: "approved by the manager"}, "manager"
		},
	})
	e.Confine("be-1", wt)
	req := Request{Agent: "be-1", Role: "backend", Tool: "Bash", Command: "python3 tools/gen.py", Cwd: wt, Summary: "bash"}
	if d := e.Check(bg, req); d.Allow || delegated != 1 || rec.count() != 1 {
		t.Fatalf("first question: %+v; delegated %d, prompts %d", d, delegated, rec.count())
	}
	before := len(l.kinds())
	d := e.Check(bg, req)
	if d.Allow || !strings.HasPrefix(d.Reason, askedEarlier) || delegated != 1 || rec.count() != 1 {
		t.Fatalf("repeat: %+v; delegated %d, prompts %d", d, delegated, rec.count())
	}
	if got := l.kinds()[before:]; !sameStrings(got, []string{"decide:policy"}) {
		t.Errorf("the repeat's audit trail is %v, want one decision by policy", got)
	}
}
