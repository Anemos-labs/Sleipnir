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
