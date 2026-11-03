package perm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A read-only role's test rules let it run the tests, not write the files a test command is told to write: a compiled test binary
// (go test -c, -o), a profile or coverage report, a pytest report or temporary directory. The read-only probe (PlanOnly) holds to the
// same, while the person's own rule in plan mode is their word for the session and still lets the command write.
func TestReadOnlyTestRulesDoNotWriteNamedOutputs(t *testing.T) {
	root := t.TempDir()
	e, err := NewEngine(Config{Mode: ModeBypass, Root: root, Home: t.TempDir(),
		Roles: map[string]RoleProfile{"reviewer": {Mode: ModePlan, Allow: ReadOnlyAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	probe := e.PlanOnly(ReadOnlyAllow...)
	ctx := context.Background()
	role := func(cmd string) Decision {
		return e.Check(ctx, Request{Agent: "rv-1", Role: "reviewer", Tool: "bash", Command: cmd, Cwd: root})
	}
	plain := func(cmd string) Decision { return probe.Check(ctx, Request{Tool: "bash", Command: cmd, Cwd: root}) }
	for _, cmd := range []string{"go test ./...", "go test -run TestX -count=1 ./...", "pytest -q tests", "go vet ./..."} {
		if d := role(cmd); !d.Allow {
			t.Errorf("a reviewer may not run %q: %s", cmd, d.Reason)
		}
		if d := plain(cmd); !d.Allow {
			t.Errorf("the read-only probe refuses %q: %s", cmd, d.Reason)
		}
	}
	for _, cmd := range []string{
		"go test -o bin/x.test ./...", "go test -c ./...", "go test -c -o=x.test .", "go test -coverprofile=c.out ./...",
		"go test -cpuprofile cpu.out .", "go test -outputdir out -memprofile m.out .", "go test -trace t.out .",
		"pytest --junitxml=out.xml", "pytest --basetemp tmp tests", "python -m pytest --report-log=r.json",
		"node --test --test-reporter-destination=out.txt web",
	} {
		if d := role(cmd); d.Allow {
			t.Errorf("a reviewer may run %q, which writes a file it names", cmd)
		}
		if d := plain(cmd); d.Allow {
			t.Errorf("the read-only probe allows %q, which writes a file it names", cmd)
		}
	}
	own, err := NewEngine(Config{Mode: ModePlan, Root: root, Home: t.TempDir(), Allow: []string{"Bash(go test:*)"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := own.Check(ctx, Request{Tool: "bash", Command: "go test -coverprofile=c.out ./...", Cwd: root}); !d.Allow {
		t.Errorf("the person's own rule in plan mode no longer covers the command: %s", d.Reason)
	}
}

// The workspace's own name is literal in the rules anchored on it: a project in "proj[ab]" is guarded by Edit(./.sleipnir/**)
// however its name reads as a glob.
func TestRulesAnchoredOnAWorkspaceWithGlobCharacters(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj[ab]")
	if err := os.MkdirAll(filepath.Join(root, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(Config{Mode: ModeAcceptEdits, Root: root, Home: t.TempDir(), Ask: []string{"Edit(./.sleipnir/**)"}})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Check(context.Background(), Request{Tool: "write", Writes: true, Paths: []string{filepath.Join(root, ".sleipnir", "config.json")}})
	if d.Allow {
		t.Errorf("a write the ask rule covers was allowed: %+v", d)
	}
}

// A glob with too many matches to judge one by one is still judged for the directory its literal part names: ~/.ssh/* is inside
// ~/.ssh, however many files are there.
func TestAGlobPastTheBudgetKeepsItsDirectory(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20100; i++ {
		if err := os.WriteFile(filepath.Join(ssh, fmt.Sprintf("k%05d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []Config{{Mode: ModeBypass}, {Mode: ModeYolo}, {Mode: ModeDefault, Allow: []string{"Bash(cat:*)"}}, {Mode: ModeBypass, Allow: []string{"Bash(cat:*)"}}} {
		c.Root, c.Home = root, home
		e, err := NewEngine(c)
		if err != nil {
			t.Fatal(err)
		}
		if d := e.Check(context.Background(), Request{Tool: "bash", Command: "cat " + ssh + "/k*", Cwd: root}); d.Allow {
			t.Errorf("%s %v: a glob over ~/.ssh past the budget was allowed: %+v", c.Mode, c.Allow, d)
		}
	}
}

// A yes given to a question is judged against the policy in force when it arrives: the session switched to plan mode while the
// person was asked refuses the write their yes was for.
func TestAYesIsJudgedAgainAgainstThePolicyWhenItArrives(t *testing.T) {
	root := t.TempDir()
	var e *Engine
	var err error
	e, err = NewEngine(Config{Mode: ModeDefault, Root: root, Home: t.TempDir(), Prompter: func(ctx context.Context, r Request) Decision {
		e.SetMode(ModePlan) // the person changed the mode while the question was on their screen, then pressed yes on it
		return Decision{Allow: true}
	}})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Check(context.Background(), Request{Tool: "write", Writes: true, Paths: []string{filepath.Join(root, "a.txt")}})
	if d.Allow || !strings.Contains(d.Reason, "changed while this was asked") {
		t.Errorf("a write the policy now refuses was allowed by the earlier question's yes: %+v", d)
	}
}

// Bypass asks nothing: a yes to a question that arrives after the mode turned to bypass is refused, as a fresh check would refuse
// the question.
func TestAYesThatArrivesInBypassIsRefused(t *testing.T) {
	root := t.TempDir()
	var e *Engine
	var err error
	e, err = NewEngine(Config{Mode: ModeDefault, Root: root, Home: t.TempDir(), Ask: []string{"Bash(echo:*)"},
		Prompter: func(ctx context.Context, r Request) Decision {
			e.SetMode(ModeBypass)
			return Decision{Allow: true}
		}})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Check(context.Background(), Request{Tool: "bash", Command: "echo hi", Cwd: root})
	if d.Allow || !strings.Contains(d.Reason, BypassRefusal) {
		t.Errorf("a yes that arrived in bypass mode was taken: %+v", d)
	}
}
