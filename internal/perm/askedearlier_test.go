package perm

import (
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
