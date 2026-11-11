package swarm

import (
	"strings"
	"testing"
)

// Each member's live view says where its pool stands and the one step that is the member's own, through the pool's phases.
func TestPoolStepLineNamesEachMembersNextStep(t *testing.T) {
	b := NewBoard(nil)
	pool, err := b.CreateTask("mgr", TaskSpec{Title: "web app", Role: "frontend", Files: []string{"web/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.beginPool("mgr", pool.ID, []string{"fe-1", "fe-2", "fe-3"}); err != nil {
		t.Fatal(err)
	}
	step := func(agent string) string {
		t.Helper()
		lines := poolStepLines(b.Snapshot(), agent)
		if len(lines) != 1 {
			t.Fatalf("%s has %d pool lines", agent, len(lines))
		}
		return lines[0]
	}
	set := func(fn func(p *PoolState)) {
		t.Helper()
		if err := b.poolUpdate("mgr", pool.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { fn(p); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(agent string, want ...string) {
		t.Helper()
		got := step(agent)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Fatalf("%s's line %q does not say %q", agent, got, w)
			}
		}
	}
	expect("fe-1", "you lead it", "pool propose id="+pool.ID)
	expect("fe-2", "led by fe-1", "only fe-1 proposes", "Do not propose")

	set(func(p *PoolState) {
		p.Agreement, p.Revision = "fe-1 does the shell, fe-2 the reader, fe-3 the settings", 1
	})
	expect("fe-2", "pool agree id="+pool.ID+" revision=1")
	set(func(p *PoolState) { p.Votes = []string{"fe-2"} })
	expect("fe-2", "you agreed; waiting for fe-1, fe-3")
	expect("fe-1", "agree to your own proposal")
	set(func(p *PoolState) { p.Votes = []string{"fe-1", "fe-2", "fe-3"} })
	expect("fe-1", "pool execute id="+pool.ID)
	expect("fe-3", "fe-1 executes it next")

	set(func(p *PoolState) { p.Phase, p.Votes = "executing", nil })
	expect("fe-1", "create one child per share")
	child, err := b.CreateTask("fe-1", TaskSpec{Parent: pool.ID, Title: "reader", Role: "frontend", Files: []string{"web/reader/**"}})
	if err != nil {
		t.Fatal(err)
	}
	expect("fe-1", "assign "+child.ID)
	expect("fe-2", "you have no open child")
	if _, err := b.assignTask(assignReq{by: "fe-1", agent: "fe-2", id: child.ID}); err != nil {
		t.Fatal(err)
	}
	expect("fe-2", "work on your child "+child.ID)

	set(func(p *PoolState) { p.Phase, p.Revision = "reconciling", 2 })
	expect("fe-3", "pool inspect id="+pool.ID, "pool confirm id="+pool.ID+" revision=2")
	if lines := poolStepLines(b.Snapshot(), "be-1"); lines != nil {
		t.Fatalf("a non-member has pool lines: %q", lines)
	}
}
