package perm

import (
	"context"
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
