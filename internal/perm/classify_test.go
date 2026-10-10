package perm

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestRememberRulesAndWhy(t *testing.T) {
	f := newFixture(t)
	var shown []Request
	e := f.engine(t, Config{Ask: []string{"Bash(make deploy)"}, Prompter: func(_ context.Context, r Request) Decision {
		shown = append(shown, r)
		return Decision{Allow: false}
	}})
	cases := []struct {
		name    string
		req     rq
		why     string
		rules   []string
		suffix  bool
		askRule bool
	}{
		{"a test command remembers its runner", bash("go test ./..."), "default mode", []string{"Bash(go test:*)"}, true, false},
		{"an exact command", bash("printenv HOME"), "default mode", []string{"Bash(printenv HOME)"}, true, false},
		{"an edit inside the project remembers edits in the project", edit("{root}/main.go"), "writing", []string{"Edit(" + f.root + "/**)"}, true, false},
		{"an ask rule remembers nothing", bash("make deploy"), "rule Bash(make deploy) requires approval", nil, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shown = nil
			e.Check(context.Background(), f.request(c.req))
			if len(shown) != 1 {
				t.Fatalf("asked %d times", len(shown))
			}
			q := shown[0]
			if !strings.Contains(q.Why, c.why) || !strings.HasSuffix(q.Summary, "["+q.Why+"]") {
				t.Fatalf("why %q, summary %q", q.Why, q.Summary)
			}
			if !reflect.DeepEqual(q.RememberRules, c.rules) {
				t.Fatalf("remember rules %q, want %q", q.RememberRules, c.rules)
			}
		})
	}
	// The rules are what a "don't ask again" answer actually adds.
	e2 := f.engine(t, Config{Prompter: func(_ context.Context, r Request) Decision {
		shown = []Request{r}
		return Decision{Allow: true, Remember: ScopeSession}
	}})
	e2.Check(context.Background(), f.request(bash("go test ./pkg")))
	if got := e2.Granted(); !reflect.DeepEqual(got, shown[0].RememberRules) {
		t.Fatalf("granted %q, announced %q", got, shown[0].RememberRules)
	}
}

func TestClassifyNamesTheRuleAndItsOrigin(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{
		Allow: []string{"Bash(go test:*)"}, Ask: []string{"Edit(./docs/**)"}, Deny: []string{"Read(./secrets/**)"},
		RuleOrigin: func(a Action, rule string) string {
			if rule == "Bash(go test:*)" {
				return "--allow flag"
			}
			return ""
		},
		Prompter: func(context.Context, Request) Decision { t.Fatal("Classify must not prompt"); return Decision{} },
	})
	e.AddRule(ScopeSession, Rule{Action: Deny, Tool: "Bash", Pattern: "rm:*"})
	cases := []struct {
		req                          rq
		verdict                      Action
		rule, origin, tier, whyFrags string
	}{
		{bash("go test ./src/..."), Allow, "Bash(go test:*)", "--allow flag", TierRule, "allowed by rule"},
		{bash("go test ./..."), Deny, "Read(./secrets/**)", OriginConfig, TierRule, "denied by rule"}, // ./... reads the denied directory too
		{edit("{root}/docs/a.md"), Ask, "Edit(./docs/**)", OriginConfig, TierRule, "requires approval"},
		{read("{root}/secrets/k"), Deny, "Read(./secrets/**)", OriginConfig, TierRule, "denied by rule"},
		{bash("rm -rf build"), Deny, "Bash(rm:*)", OriginSession, TierRule, "denied by rule"},
		{read("{home}/.ssh/id_rsa"), Deny, "", OriginBuiltIn, TierHard, "built-in protection"},
		{read("{root}/.env"), Deny, "", OriginBuiltIn, TierGuarded, "built-in protection"},
		{read("{root}/main.go"), Allow, "", "", TierMode, "read inside the workspace"},
		{bash("make"), Ask, "", "", TierMode, "default mode"},
	}
	for _, c := range cases {
		got := e.Classify(f.request(c.req))
		if got.Verdict != c.verdict || got.Rule != c.rule || got.Origin != c.origin || got.Tier != c.tier || !strings.Contains(got.Why, c.whyFrags) {
			t.Errorf("%+v: got %+v", c.req, got)
		}
	}
	// No one to ask: a question is a refusal, as Check makes it.
	headless := f.engine(t, Config{})
	if got := headless.Classify(f.request(bash("make"))); got.Verdict != Deny || !IsNoOneToAsk(got.Why) || !got.NoOneToAsk {
		t.Fatalf("headless: %+v", got)
	}
}

func TestRemoveRule(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Allow: []string{"Bash(make lint)"}})
	if e.RemoveRule("Bash(make lint)") {
		t.Fatal("a configuration rule cannot be removed")
	}
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "Bash", Pattern: "make test"})
	e.AddRule(ScopeSession, Rule{Action: Deny, Tool: "Bash", Pattern: "make test"})
	if got := e.Classify(f.request(bash("make test"))); got.Verdict != Deny {
		t.Fatalf("before: %+v", got)
	}
	if !e.RemoveRule("  Bash(make test) ") {
		t.Fatal("a session rule is removed")
	}
	if got := e.Classify(f.request(bash("make test"))); got.Verdict == Allow || got.Rule != "" {
		t.Fatalf("after removal the mode decides: %+v", got)
	}
	if len(e.Granted()) != 0 {
		t.Fatalf("granted keeps a removed rule: %v", e.Granted())
	}
	if e.RemoveRule("Bash(make test)") || e.RemoveRule("") || e.RemoveRule("Bash(") {
		t.Fatal("nothing left to remove")
	}
	// The tests preset added by an answer is a session rule, and removable.
	e.addPreset(PresetTests)
	infos := e.RuleInfos()
	n := 0
	for _, r := range infos {
		if r.Origin == OriginTests && r.Runtime {
			n++
		}
	}
	if n != len(TestsAllow) {
		t.Fatalf("preset rules listed: %d of %d (%+v)", n, len(TestsAllow), infos)
	}
	if !e.RemoveRule(TestsAllow[0]) {
		t.Fatal("a preset rule is removable")
	}
}

func TestDeclinedWith(t *testing.T) {
	if got := DeclinedWith("   "); got != "denied by user" {
		t.Fatalf("empty note: %q", got)
	}
	got := DeclinedWith("use the existing helper instead")
	if !strings.HasPrefix(got, "denied by user"+declinedAdvice) || !strings.HasSuffix(got, " The person says: use the existing helper instead") {
		t.Fatalf("note: %q", got)
	}
	// Through the engine, the refusal reaches the tool as given; an empty note gets the advice.
	f := newFixture(t)
	for _, note := range []string{"", "not now"} {
		e := f.engine(t, Config{Prompter: func(context.Context, Request) Decision { return Decision{Reason: DeclinedWith(note)} }})
		d := e.Check(context.Background(), f.request(bash("make")))
		if d.Allow || strings.Count(d.Reason, declinedAdvice) != 1 {
			t.Fatalf("note %q: %+v", note, d)
		}
	}
}
