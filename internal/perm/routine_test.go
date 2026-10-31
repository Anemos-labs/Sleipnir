package perm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A routine question goes to the delegate first, and its answer stands; everything that is not routine goes to the person, as
// without a delegate.
func TestTheDelegateSettlesRoutineQuestionsOnly(t *testing.T) {
	var mu sync.Mutex
	var delegated, prompted []string
	var audits []Audit
	f := newFixture(t)
	trees := filepath.Join(f.base, "cache", "wt", "s1")
	wt := filepath.Join(trees, "be-1")
	for _, d := range []string{".sleipnir", "bin"} {
		if err := os.MkdirAll(filepath.Join(wt, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e := f.engine(t, Config{Mode: ModeAcceptEdits, TreeParents: []string{trees}, Ask: guardedAsk,
		Prompter: func(_ context.Context, r Request) Decision {
			mu.Lock()
			prompted = append(prompted, r.Command)
			mu.Unlock()
			return Decision{Allow: true}
		},
		Delegate: func(_ context.Context, r Request) (Decision, string) {
			mu.Lock()
			delegated = append(delegated, r.Command)
			mu.Unlock()
			if strings.Contains(r.Command, "pass-it-on") {
				return Decision{}, ""
			}
			if r.Why == "" {
				t.Errorf("the delegate was not told why: %+v", r)
			}
			return Decision{Allow: true, Reason: "approved by the manager"}, "manager"
		},
		Audit: func(a Audit) {
			mu.Lock()
			audits = append(audits, a)
			mu.Unlock()
		},
	})
	e.Confine("be-1", wt)
	routine := []string{
		"PORT=18098 ./bin/app &", "python3 tools/gen.py", "node server.js --port 8080", "sqlite3 notes.db .tables",
		"./bin/app pass-it-on",
	}
	personal := []string{
		"curl -s https://example.com/", "curl -L http://127.0.0.1:8080/", "npm install left-pad", "go get example.com/x", "kill 4242", "git push origin HEAD",
		"cp bin/app " + f.outside + "/app", "gofmt -w .sleipnir/hook.go", "sudo ls", "echo $(date) > x", "LD_PRELOAD=x ./bin/app",
		"./bin/app > /tmp/out.txt", "docker run alpine", "./bin/app --data $HOME/x",
	}
	for _, cmd := range append(append([]string(nil), routine...), personal...) {
		if d := e.Check(bg, Request{Agent: "be-1", Role: "backend", Tool: "Bash", Command: cmd, Cwd: wt, Summary: "bash"}); !d.Allow {
			t.Errorf("%q: %+v", cmd, d)
		}
	}
	want := map[string]bool{}
	for _, c := range routine {
		want[c] = true
	}
	for _, c := range delegated {
		if !want[c] {
			t.Errorf("%q was put to the delegate", c)
		}
		delete(want, c)
	}
	for c := range want {
		t.Errorf("%q was not put to the delegate", c)
	}
	if strings.Join(prompted, "\n") != "./bin/app pass-it-on\n"+strings.Join(personal, "\n") {
		t.Errorf("the person was asked %q", prompted)
	}
	byManager := 0
	for _, a := range audits {
		if a.Kind == "decide" && a.By == "manager" {
			byManager++
		}
	}
	if byManager != len(routine)-1 {
		t.Errorf("%d decisions by the manager in the audit, want %d", byManager, len(routine)-1)
	}
	// a delegate's yes is not remembered
	if got := e.Rules(Allow); len(got) != 0 {
		t.Errorf("rules after the delegate's answers: %q", got)
	}
}

// The manager answers for the policy in force when it was asked: a deny rule added or a stricter mode chosen during the wait wins
// over its yes, and a question that is no longer routine goes to the person.
func TestADelegatedYesIsJudgedAgainAfterTheWait(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		name   string
		change func(*Engine)
		want   string
		asked  bool
	}{
		{"nothing changed", func(*Engine) {}, "allow", false},
		{"a deny rule added", func(e *Engine) { e.AddRule(ScopeSession, Rule{Action: Deny, Tool: "Bash", Pattern: "python3:*"}) }, "deny", false},
		{"plan mode chosen", func(e *Engine) { e.SetMode(ModePlan) }, "deny", false},
		{"an ask rule added", func(e *Engine) { e.AddRule(ScopeSession, Rule{Action: Ask, Tool: "Bash", Pattern: "python3:*"}) }, "allow", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var e *Engine
			asked := false
			e = f.engine(t, Config{Mode: ModeAcceptEdits,
				Prompter: func(context.Context, Request) Decision { asked = true; return Decision{Allow: true} },
				Delegate: func(context.Context, Request) (Decision, string) {
					c.change(e)
					return Decision{Allow: true, Reason: "approved by the manager"}, "manager"
				},
			})
			d := e.Check(bg, Request{Agent: "be-1", Tool: "Bash", Command: "python3 tools/gen.py", Cwd: f.root})
			if got := outcome(d); got != c.want || asked != c.asked {
				t.Errorf("= %s (%s), person asked %v; want %s, asked %v", got, d.Reason, asked, c.want, c.asked)
			}
		})
	}
}
