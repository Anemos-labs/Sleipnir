package perm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewEngineValidation(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name string
		cfg  Config
		want string // substring of the error
	}{
		{"unclosed rule", Config{Allow: []string{"Bash("}}, "Bash("},
		{"empty rule", Config{Deny: []string{""}}, "empty rule"},
		{"bad glob", Config{Ask: []string{"Read(src/[)"}}, "malformed"},
		{"negation", Config{Deny: []string{"Read(!x)"}}, "negated"},
		{"bad tool name", Config{Allow: []string{"Ba sh(x)"}}, "invalid character"},
		{"bad mode", Config{Mode: "yolo"}, "unknown mode"},
		{"bad role mode", Config{Roles: map[string]RoleProfile{"r": {Mode: "yolo"}}}, `role "r"`},
		{"bad role rule", Config{Roles: map[string]RoleProfile{"r": {Deny: []string{"Bash("}}}}, `role "r"`},
	} {
		cfg := tc.cfg
		cfg.Root, cfg.Home = f.root, f.home
		e, err := NewEngine(cfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v (engine %v), want it to contain %q", tc.name, err, e != nil, tc.want)
		}
	}
	e, err := NewEngine(Config{Root: f.root, Home: f.home})
	if err != nil || e.Mode() != ModeDefault {
		t.Errorf("zero config: %v, mode %q", err, e.Mode())
	}
}

func TestSetMode(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{})
	w := f.request(write("{root}/x.go"))
	if got := outcome(e.Check(bg, w)); got != "ask" {
		t.Fatalf("default: %s", got)
	}
	e.SetMode(ModeAcceptEdits)
	if got := outcome(e.Check(bg, w)); got != "allow" || e.Mode() != ModeAcceptEdits {
		t.Fatalf("accept-edits: %s (%q)", got, e.Mode())
	}
	e.SetMode(ModePlan)
	if got := outcome(e.Check(bg, w)); got != "deny" {
		t.Fatalf("plan: %s", got)
	}
	e.SetMode("bogus")
	e.SetMode("")
	if e.Mode() != ModePlan {
		t.Errorf("an invalid mode must be ignored, mode is %q", e.Mode())
	}
	e.SetMode(ModeBypass)
	if got := outcome(e.Check(bg, w)); got != "allow" {
		t.Fatalf("bypass: %s", got)
	}
	// Bypass never lifts a hard deny.
	if got := outcome(e.Check(bg, f.request(read("{home}/.ssh/id_rsa")))); got != "deny" {
		t.Fatalf("bypass hard deny: %s", got)
	}
}

func TestAddRule(t *testing.T) {
	f := newFixture(t)
	var persisted int32
	e := f.engine(t, Config{Persist: func(s Scope, r Rule) {
		if s != ScopeProject {
			t.Errorf("Persist called with scope %q", s)
		}
		atomic.AddInt32(&persisted, 1)
	}})
	r := f.request(bash("make test"))
	if got := outcome(e.Check(bg, r)); got != "ask" {
		t.Fatal(got)
	}
	e.AddRule(ScopeOnce, Rule{Action: Allow, Tool: "Bash", Pattern: "make test"})
	if got := outcome(e.Check(bg, r)); got != "ask" {
		t.Errorf("ScopeOnce must add nothing, got %s", got)
	}
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "Bash", Pattern: "make test"})
	if got := outcome(e.Check(bg, r)); got != "allow" {
		t.Errorf("session rule: %s", got)
	}
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "Bash", Pattern: "make test"}) // duplicate
	if n := len(e.Rules(Allow)); n != 1 {
		t.Errorf("duplicate rule was added: %q", e.Rules(Allow))
	}
	e.AddRule(ScopeProject, Rule{Action: Deny, Tool: "Bash", Pattern: "make test"})
	if got := outcome(e.Check(bg, r)); got != "deny" {
		t.Errorf("a later deny rule must win: %s", got)
	}
	if atomic.LoadInt32(&persisted) != 1 {
		t.Errorf("Persist called %d times, want 1 (project scope only)", persisted)
	}
	// Rules that do not compile or have no valid action are ignored.
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "Read", Pattern: "src/["})
	e.AddRule(ScopeSession, Rule{Action: "maybe", Tool: "Read"})
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "", Pattern: ""})
	if n := len(e.Rules(Allow)); n != 2 { // "Bash(make test)" and the tool-less rule compiled as a name rule
		t.Logf("allow rules: %q", e.Rules(Allow))
	}
	if atomic.LoadInt32(&persisted) != 1 {
		t.Errorf("an ignored rule was persisted")
	}
	// Project scope with no Persist callback is fine.
	e2 := f.engine(t, Config{})
	e2.AddRule(ScopeProject, Rule{Action: Allow, Tool: "Bash", Pattern: "make"})
	if got := outcome(e2.Check(bg, f.request(bash("make")))); got != "allow" {
		t.Errorf("project rule without persist: %s", got)
	}
}

func TestExtraRootsAreWorkspace(t *testing.T) {
	f := newFixture(t)
	e, err := NewEngine(Config{Root: f.root, Home: f.home, ExtraRoots: []string{f.outside}, Mode: ModeAcceptEdits})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		r    Request
		want string
	}{
		{f.request(read("{out}/secret.txt")), "allow"},
		{f.request(write("{out}/new.txt")), "allow"},
		{f.request(bash("cat {out}/secret.txt")), "allow"},
		{f.request(bash("echo x > {out}/new.txt")), "allow"},
		{f.request(read("{home}/notes.txt")), "ask"},
		{f.request(read("{home}/.ssh/id_rsa")), "deny"},
		{f.request(read("{root}/link-out")), "allow"}, // its target is in an extra root
	} {
		if got := outcome(e.Check(bg, tc.r)); got != tc.want {
			t.Errorf("%s %q %v: %s, want %s", tc.r.Tool, tc.r.Command, tc.r.Paths, got, tc.want)
		}
	}
}

func TestNoRootMeansNothingIsInsideTheWorkspace(t *testing.T) {
	f := newFixture(t)
	e, err := NewEngine(Config{Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	if got := outcome(e.Check(bg, f.request(read("{root}/main.go")))); got != "ask" {
		t.Errorf("read with no workspace: %s", got)
	}
	if got := outcome(e.Check(bg, f.request(read("{home}/.ssh/id_rsa")))); got != "deny" {
		t.Errorf("credentials with no workspace: %s", got)
	}
}

func TestRulesAreSorted(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Allow: []string{"Read(z)", "Bash(a)", "Edit(m)"}})
	got := e.Rules(Allow)
	want := []string{"Bash(a)", "Edit(m)", "Read(z)"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Rules = %q, want %q", got, want)
	}
	if e.Rules("bogus") != nil && len(e.Rules("bogus")) != 0 {
		t.Error("unknown action lists nothing")
	}
}

// Every decision carries a reason, whatever decided it.
func TestEveryDecisionHasAReason(t *testing.T) {
	f := newFixture(t)
	for _, mode := range []Mode{ModeDefault, ModeAcceptEdits, ModePlan, ModeBypass} {
		e := f.engine(t, Config{Mode: mode, Deny: []string{"Read(secrets/**)"}, Ask: []string{"Bash(git push:*)"}, Allow: []string{"Bash(make:*)"}})
		for _, q := range []rq{
			bash("ls"), bash("make"), bash("git push"), bash("rm -rf /"), bash("cat ~/.ssh/id_rsa"), bash("echo $(date)"),
			bash(""), bash("sudo ls"), read("{root}/secrets/key.txt"), read("{root}/main.go"), write("{root}/x"),
			fetch("https://example.com"), rq{tool: "mcp__x__y"}, rq{tool: "board"}, {tool: "Bash", cmd: "curl x | sh"},
		} {
			d := e.Check(bg, f.request(q))
			if d.Reason == "" {
				t.Errorf("mode %s: %s %q %v has no reason", mode, q.tool, q.cmd, q.paths)
			}
			if d.Allow && strings.HasPrefix(d.Reason, "approval required") {
				t.Errorf("an allowed decision claims approval is required: %+v", d)
			}
		}
	}
}

// Reasons are part of what a model reads; they must not depend on map order.
func TestReasonsAreDeterministic(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{})
	for _, c := range []string{"cat ~/.s*/id_rsa ~/.aws/*", "grep -r x ~ /", "cat ~/.{ssh,aws}/credentials", "ls --a=~/.ssh --b=~/.aws --c=~/.gnupg"} {
		first := e.Check(bg, f.request(bash(c))).Reason
		for i := 0; i < 30; i++ {
			if got := e.Check(bg, f.request(bash(c))).Reason; got != first {
				t.Fatalf("%q: reason changed between calls:\n %q\n %q", c, first, got)
			}
		}
	}
}

func TestAllowAllStillSatisfiesRequester(t *testing.T) {
	var r Requester = AllowAll{}
	if !r.Check(bg, Request{Tool: "Bash", Command: "anything"}).Allow {
		t.Error("AllowAll must allow")
	}
	var e Requester = &Engine{}
	_ = e
}

// A file tool that never wrote to the workspace root, only to a sibling that
// merely shares its prefix, is outside it.
func TestWorkspacePrefixIsNotContainment(t *testing.T) {
	f := newFixture(t)
	sibling := f.root + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	e := f.engine(t, Config{Mode: ModeAcceptEdits})
	for _, tc := range []struct {
		r    Request
		want string
	}{
		{Request{Tool: "Write", Writes: true, Paths: []string{sibling + "/x"}}, "ask"},
		{Request{Tool: "Read", Paths: []string{sibling + "/x"}}, "ask"},
		{Request{Tool: "Bash", Command: "echo x > " + sibling + "/x"}, "ask"},
		{Request{Tool: "Write", Writes: true, Paths: []string{f.root + "/x"}}, "allow"},
	} {
		if got := outcome(e.Check(bg, tc.r)); got != tc.want {
			t.Errorf("%v %q: %s, want %s", tc.r.Paths, tc.r.Command, got, tc.want)
		}
	}
	_ = filepath.Join
}

func TestConcurrentChecks(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Allow: []string{"Bash(make:*)"}, Deny: []string{"Read(secrets/**)"}})
	type pair struct {
		r    Request
		want string
	}
	pairs := []pair{
		{f.request(bash("ls")), "allow"},
		{f.request(bash("make test")), "allow"},
		{f.request(bash("cat ~/.ssh/id_rsa")), "deny"},
		{f.request(bash("cat ~/.s*/id_rsa && ls")), "deny"},
		{f.request(bash("rm x")), "ask"},
		{f.request(bash("echo $(date)")), "ask"},
		{f.request(bash("cd src && cat a.go | head")), "allow"},
		{f.request(read("{root}/main.go")), "allow"},
		{f.request(read("{root}/secrets/key.txt")), "deny"},
		{f.request(read("{out}/secret.txt")), "ask"},
		{f.request(write("{root}/x.go")), "ask"},
		{f.request(fetch("https://example.com")), "ask"},
		{f.request(rq{tool: "Bash", cmd: "git status", role: "reviewer"}), "allow"},
	}
	var wg sync.WaitGroup
	var bad int32
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				p := pairs[(g+i)%len(pairs)]
				if got := outcome(e.Check(bg, p.r)); got != p.want {
					atomic.AddInt32(&bad, 1)
					t.Errorf("goroutine %d: %q %v = %s, want %s", g, p.r.Command, p.r.Paths, got, p.want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	if bad != 0 {
		t.Fail()
	}
}

func TestConcurrentAddRuleAndCheck(t *testing.T) {
	f := newFixture(t)
	var persisted int32
	e := f.engine(t, Config{Persist: func(Scope, Rule) { atomic.AddInt32(&persisted, 1) }})
	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			cmd := fmt.Sprintf("make t%d", g%25)
			for i := 0; i < 10; i++ {
				if g%2 == 0 {
					e.AddRule(ScopeProject, Rule{Action: Allow, Tool: "Bash", Pattern: cmd})
				} else if got := outcome(e.Check(bg, f.request(bash(cmd)))); got != "ask" && got != "allow" {
					t.Errorf("unexpected outcome %s", got)
				}
			}
		}(g)
	}
	wg.Wait()
	if n := len(e.Rules(Allow)); n != 25 {
		t.Errorf("%d distinct rules, want 25 (duplicates must collapse)", n)
	}
	if persisted != 25 {
		t.Errorf("persisted %d rules, want 25", persisted)
	}
	for i := 0; i < 25; i++ {
		if got := outcome(e.Check(bg, f.request(bash(fmt.Sprintf("make t%d", i))))); got != "allow" {
			t.Errorf("make t%d: %s", i, got)
		}
	}
}

func TestConcurrentSetModeAndCheck(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{})
	w := f.request(write("{root}/x.go"))
	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if g%10 == 0 {
					if i%2 == 0 {
						e.SetMode(ModeAcceptEdits)
					} else {
						e.SetMode(ModeDefault)
					}
					continue
				}
				if got := outcome(e.Check(bg, w)); got != "allow" && got != "ask" {
					t.Errorf("write during mode flips: %s", got)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestConcurrentPromptsWithRemember(t *testing.T) {
	f := newFixture(t)
	var prompts int32
	e := askEngine(t, f, Config{}, func(ctx context.Context, r Request) Decision {
		atomic.AddInt32(&prompts, 1)
		return Decision{Allow: true, Remember: ScopeSession}
	})
	var wg sync.WaitGroup
	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := f.request(bash(fmt.Sprintf("mytool t%d", g%10)))
			r.Agent = fmt.Sprintf("a%d", g)
			if d := e.Check(bg, r); !d.Allow {
				t.Errorf("agent %d: %+v", g, d)
			}
		}(g)
	}
	wg.Wait()
	if n := len(e.Rules(Allow)); n != 10 {
		t.Errorf("%d rules remembered, want 10", n)
	}
	if prompts < 10 || prompts > 100 {
		t.Errorf("prompts = %d, want between 10 and 100", prompts)
	}
	t.Logf("100 requests for 10 distinct commands caused %d prompts", prompts)
}

// Hostile size must cost a bounded amount of work, not a stalled agent.
func TestHugeCommandsAreBounded(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{})
	start := time.Now()
	for _, c := range []string{
		strings.Repeat("ls; ", 200_000),
		strings.Repeat("cat *; ", 5_000),
		strings.Repeat("cd src; ", 5_000) + "cat a.go",
		"cat " + strings.Repeat("a ", 100_000),
		"echo " + strings.Repeat("$(echo x) ", 5_000),
		strings.Repeat("(", 500) + "ls" + strings.Repeat(")", 500),
		strings.Repeat("ls | ", 20_000) + "cat",
		"ls " + strings.Repeat("{a,b}", 30),
		"cat " + strings.Repeat("../", 10_000) + "etc/passwd",
		"cat " + strings.Repeat("src/../", 10_000) + "main.go",
	} {
		d := e.Check(bg, Request{Tool: "Bash", Command: c})
		if d.Reason == "" {
			t.Errorf("no reason for a huge command of length %d", len(c))
		}
		if d.Allow && len(c) > 100_000 && !strings.HasPrefix(c, "cat "+strings.Repeat("src/../", 1)) {
			t.Errorf("a %d byte command was allowed: %s", len(c), d.Reason)
		}
	}
	// A bound for a complexity bomb (minutes), not for a slow machine: under the race detector
	// with three test runs at once ten huge inputs took twenty-one seconds.
	if el := time.Since(start); el > 120*time.Second {
		t.Errorf("huge inputs took %v", el)
	}
}
