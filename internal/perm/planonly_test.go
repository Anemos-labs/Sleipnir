package perm

import (
	"context"
	"path/filepath"
	"testing"
)

// PlanOnly is what plan mode allows on its own: the person's allow rules, which carve exceptions for the session, are not in it, and it stays in
// plan mode whatever the session's mode becomes.
func TestPlanOnlyDropsTheAllowRulesAndStaysInPlanMode(t *testing.T) {
	root := t.TempDir()
	e, err := NewEngine(Config{Mode: ModePlan, Root: root, Home: t.TempDir(), Allow: []string{"Edit(docs/plan.md)", "Bash(rm:*)"}})
	if err != nil {
		t.Fatal(err)
	}
	p := e.PlanOnly()
	if p == nil {
		t.Fatal("no plan-only engine")
	}
	ctx := context.Background()
	plan := Request{Tool: "write", Writes: true, Paths: []string{root + "/docs/plan.md"}}
	rm := Request{Tool: "bash", Command: "rm -rf build", Cwd: root}
	if !e.Check(ctx, plan).Allow || !e.Check(ctx, rm).Allow {
		t.Fatal("the session's own rules should allow both")
	}
	if p.Check(ctx, plan).Allow || p.Check(ctx, rm).Allow {
		t.Error("plan mode alone allowed what only a rule allows")
	}
	if d := p.Check(ctx, Request{Tool: "bash", Command: "git status && ls src | wc -l", Cwd: root}); !d.Allow {
		t.Errorf("plan mode proves a read-only compound command: %+v", d)
	}
	if got := p.Rules(Allow); len(got) != 0 {
		t.Errorf("allow rules in the plan-only engine: %v", got)
	}
	e.SetMode(ModeAcceptEdits)
	if p.Mode() != ModePlan {
		t.Errorf("the plan-only engine follows the session's mode: %q", p.Mode())
	}
}

// PlanOnly is a probe: a request it would ask about is refused by it alone. It puts no question to the manager or the person and records
// nothing in the session's audit (a pool's probe once left "perm.ask" and "perm.decide by no one" in the log for questions nobody was
// asked) and keeps no rule.
func TestPlanOnlyAsksNoOneAndRecordsNothing(t *testing.T) {
	home := t.TempDir()
	var audits, delegated, prompted, persisted int
	e, err := NewEngine(Config{Root: t.TempDir(), Home: home, Mode: ModeAcceptEdits, Ask: []string{"Edit(~/.sleipnir/**)"},
		Audit:    func(Audit) { audits++ },
		Delegate: func(context.Context, Request) (Decision, string) { delegated++; return Decision{}, "" },
		Prompter: func(context.Context, Request) Decision { prompted++; return Decision{Allow: true} },
		Persist:  func(Scope, Rule) { persisted++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	po := e.PlanOnly(ReadOnlyAllow...)
	note := Request{Agent: "be-1", Tool: ToolMemory, Paths: []string{filepath.Join(home, ".sleipnir", "MEMORY.md")}, Writes: true, Risk: RiskMedium}
	if d := po.Check(context.Background(), note); d.Allow {
		t.Fatalf("plan mode wrote: %+v", d)
	}
	if audits+delegated+prompted+persisted != 0 {
		t.Errorf("the probe reached out: audit %d, delegate %d, prompter %d, persist %d", audits, delegated, prompted, persisted)
	}
	// the session's own engine still asks
	if d := e.Check(context.Background(), note); !d.Allow || prompted != 1 {
		t.Errorf("the session's engine: %+v, asked %d", d, prompted)
	}
}

// A read-only role runs the project's static checks as well as its tests: a syntax check and the read-only checkers, never the options
// that make them write the project's files or a report, install, or load a program of the caller's choosing, nor a file outside the
// workspace. A
// reviewer in a bypass session was refused `node --check web/js/app.js` twice in one review and checked the code by eye instead.
func TestReadOnlyRolesRunTheStaticChecks(t *testing.T) {
	root := t.TempDir()
	for _, mode := range []Mode{ModeAcceptEdits, ModeBypass} { // in default mode the session itself asks about checks; a role only narrows it
		e, err := NewEngine(Config{Mode: mode, Root: root, Home: t.TempDir(),
			Roles: map[string]RoleProfile{"reviewer": {Mode: ModePlan, Allow: ReadOnlyAllow}}})
		if err != nil {
			t.Fatal(err)
		}
		check := func(cmd string) Decision {
			return e.Check(context.Background(), Request{Agent: "rv-1", Role: "reviewer", Tool: "bash", Command: cmd, Cwd: root})
		}
		for _, cmd := range []string{
			"node --check web/js/app.js && node --check web/js/views.js", "tsc --noEmit -p .", "eslint web/js",
			"ruff check src", "ruff format --check .", "mypy src", "pyright", "node --test web/js",
			"eslint --format json web/js", "ruff check --output-format json src", "mypy --strict src",
		} {
			if d := check(cmd); !d.Allow {
				t.Errorf("%s: a reviewer may not run %q: %s", mode, cmd, d.Reason)
			}
		}
		for _, cmd := range []string{
			"node --check --require ./evil.js web/js/app.js", "node web/js/app.js", "eslint --fix web/js", "ruff check --fix src",
			"mypy --install-types src", "tsc --noEmit --build", "eslint /etc", "gofmt -l -w .", "ruff format src",
			"node --check web/js/app.js && rm -rf web",
			// a report is a file of their own: attached, in a cluster, or after "="
			"eslint -o report.json web/js", "eslint --output-file=report.json web/js", "ruff check -oout.txt src",
			"ruff check -qo out.txt src", "ruff check --output-file out.txt src", "mypy --junit-xml j.xml src",
			"mypy --html-report h src", "tsc --noEmit --incremental", "tsc --noEmit --tsBuildInfoFile b.json",
			"eslint --suppress-all web/js", "eslint --suppress-rule no-console web/js", "eslint --prune-suppressions web/js",
			`ruff check --config "fix = true" src`, "ruff check --config=other.toml src",
		} {
			if d := check(cmd); d.Allow {
				t.Errorf("%s: a reviewer may run %q", mode, cmd)
			}
		}
	}
}

// A reviewer runs a JavaScript project's tests the way its README says (npm test -- --run for vitest, pnpm test, yarn test) and a
// Rust project's lints (cargo clippy), and reads text and JSON with awk and jq, which the read-only list judges on their own.
// cargo clippy --fix rewrites the sources and stays refused, as eslint --fix does.
func TestReadOnlyRolesRunTheProjectsTestsAndLints(t *testing.T) {
	root := t.TempDir()
	for _, mode := range []Mode{ModeAcceptEdits, ModeBypass} {
		e, err := NewEngine(Config{Mode: mode, Root: root, Home: t.TempDir(),
			Roles: map[string]RoleProfile{"reviewer": {Mode: ModePlan, Allow: ReadOnlyAllow}}})
		if err != nil {
			t.Fatal(err)
		}
		check := func(cmd string) Decision {
			return e.Check(context.Background(), Request{Agent: "rv-1", Role: "reviewer", Tool: "bash", Command: cmd, Cwd: root})
		}
		for _, cmd := range []string{
			"npm test -- --run", "npm test -- --run 2>&1 | tail -20", "pnpm test", "pnpm test -- --run", "yarn test --watch=false",
			"cargo clippy", "cargo clippy --all-targets -- -D warnings", "jq -r .scripts package.json", "awk -F: '{print $1}' x.txt",
			"git config --get remote.origin.url", "ps aux | grep -v grep", "npm ls --depth=0",
		} {
			if d := check(cmd); !d.Allow {
				t.Errorf("%s: a reviewer may not run %q: %s", mode, cmd, d.Reason)
			}
		}
		for _, cmd := range []string{
			"cargo clippy --fix", "cargo clippy --fix --allow-dirty", "cargo clippy --manifest-path /etc/x/Cargo.toml",
			"pnpm install", "yarn add left-pad", "npm test && npm install", "awk '{print > \"x\"}' x.txt", "jq -n env",
			"git config user.name x",
		} {
			if d := check(cmd); d.Allow {
				t.Errorf("%s: a reviewer may run %q", mode, cmd)
			}
		}
	}
}

// A ReadOnly context holds a turn to what plan mode allows on its own, whatever the session's mode and allow rules, and leaves the
// session's mode alone.
func TestReadOnlyRefusesWhatOnlyTheModeOrARuleAllows(t *testing.T) {
	root := t.TempDir()
	e, err := NewEngine(Config{Mode: ModeBypass, Root: root, Home: t.TempDir(), Allow: []string{"Edit(docs/plan.md)", "Bash(rm:*)"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ReadOnly(context.Background())
	plan := Request{Tool: "write", Writes: true, Paths: []string{root + "/docs/plan.md"}}
	rm := Request{Tool: "bash", Command: "rm -rf build", Cwd: root}
	other := Request{Tool: "write", Writes: true, Paths: []string{root + "/main.go"}}
	if !e.Check(context.Background(), plan).Allow || !e.Check(context.Background(), other).Allow {
		t.Fatal("bypass and the rules should allow the writes outside a read-only turn")
	}
	for _, r := range []Request{plan, rm, other} {
		if d := e.Check(ctx, r); d.Allow {
			t.Errorf("a read-only turn allowed %+v: %s", r, d.Reason)
		}
	}
	if d := e.Check(ctx, Request{Tool: "bash", Command: "git status", Cwd: root}); !d.Allow {
		t.Errorf("a read-only turn refused a read: %+v", d)
	}
	if e.Mode() != ModeBypass {
		t.Errorf("the session's mode changed: %q", e.Mode())
	}
}

// A hold applies to every check while it lasts, whatever the context, and the mode stays.
func TestHoldReadOnlyHoldsEveryCheck(t *testing.T) {
	root := t.TempDir()
	e, err := NewEngine(Config{Mode: ModeBypass, Root: root, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	w := Request{Tool: "write", Writes: true, Paths: []string{root + "/main.go"}}
	release := e.HoldReadOnly()
	if d := e.Check(context.Background(), w); d.Allow {
		t.Fatalf("a write was allowed under a hold: %+v", d)
	}
	release()
	release() // a second release changes nothing
	if d := e.Check(context.Background(), w); !d.Allow {
		t.Fatalf("the hold outlived its release: %+v", d)
	}
	if e.Mode() != ModeBypass {
		t.Errorf("the session's mode changed: %q", e.Mode())
	}
}

// A hold that begins while a request waits for its answer still holds it: an approval given during the read-only turn writes nothing.
func TestHoldReadOnlyHoldsARequestAlreadyWaiting(t *testing.T) {
	root := t.TempDir()
	var e *Engine
	var release func()
	e, err := NewEngine(Config{Mode: ModeDefault, Root: root, Home: t.TempDir(), Prompter: func(context.Context, Request) Decision {
		release = e.HoldReadOnly() // the read-only turn starts while the person is asked
		return Decision{Allow: true, Reason: "approved"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	w := Request{Tool: "write", Writes: true, Paths: []string{root + "/main.go"}}
	if d := e.Check(context.Background(), w); d.Allow {
		t.Fatalf("a write approved during a hold was allowed: %+v", d)
	}
	release()
}

// An approval given to be remembered once a hold has begun is not remembered either: nothing outlives the read-only turn.
func TestHoldReadOnlyRemembersNoApprovalGivenDuringIt(t *testing.T) {
	root := t.TempDir()
	var e *Engine
	var persisted []Rule
	release := func() {}
	e, err := NewEngine(Config{Mode: ModeDefault, Root: root, Home: t.TempDir(), Persist: func(_ Scope, r Rule) { persisted = append(persisted, r) },
		Prompter: func(context.Context, Request) Decision {
			release = e.HoldReadOnly()
			return Decision{Allow: true, Remember: ScopeProject}
		}})
	if err != nil {
		t.Fatal(err)
	}
	w := Request{Tool: "write", Writes: true, Paths: []string{root + "/main.go"}}
	if d := e.Check(context.Background(), w); d.Allow {
		t.Fatalf("a write approved during a hold was allowed: %+v", d)
	}
	release()
	if len(persisted) != 0 || len(e.Granted()) != 0 {
		t.Errorf("an approval given during the hold was remembered: persisted %v, granted %v", persisted, e.Granted())
	}
}
