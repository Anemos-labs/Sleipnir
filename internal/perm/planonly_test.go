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
	e, err := NewEngine(Config{Root: t.TempDir(), Home: home, Mode: ModeBypass, Ask: []string{"Edit(~/.sleipnir/**)"},
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
