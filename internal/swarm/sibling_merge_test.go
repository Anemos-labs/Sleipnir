package swarm

import "testing"

// A pool child that merges tells the members working on its siblings in trees of their own how to bring it in. In a team trial a
// backend worker whose sibling had just merged the scaffold spent 25 minutes reading the integration branch with git show, file by
// file, because nothing said that claiming its own task again refreshes its checkout.
func TestAMergedPoolChildTellsItsSiblingsHowToBringItIn(t *testing.T) {
	r := newIsoRig(t, isoOpts{cfg: Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend", "backend"}}}, idleModel)
	r.mustSpawn("backend", "scaffold", "src/**")
	parent := r.sw.Board.Snapshot().Tasks[0]
	if parent.Pool == nil || len(parent.Pool.Members) != 3 {
		t.Fatalf("%s is no pool of three: %+v", parent.ID, parent.Pool)
	}
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	var children []Task
	for i, m := range parent.Pool.Members {
		c, err := r.sw.Board.CreateTask(parent.Owner, TaskSpec{Parent: parent.ID, Title: "part " + m, Role: "backend", Files: []string{"src/p" + string(rune('1'+i)) + ".py"}})
		if err != nil {
			t.Fatal(err)
		}
		if c, err = r.sw.Board.assignTask(assignReq{by: parent.Owner, agent: m, id: c.ID}); err != nil {
			t.Fatal(err)
		}
		children = append(children, c)
	}
	for _, m := range parent.Pool.Members {
		if mm := r.sw.get(m); mm == nil || mm.tree == nil {
			t.Fatalf("%s has no tree of its own", m)
		}
	}
	merged := children[0]
	r.sw.tellSiblingsOfMerge(merged, merged.Owner)
	got := harnessMail(r.rvRig, merged.ID+" (part ")
	for _, c := range children[1:] {
		if got[c.Owner] != 1 {
			t.Errorf("%s (on %s) was told %d times, want once: %v", c.Owner, c.ID, got[c.Owner], got)
		}
	}
	if got[merged.Owner] != 0 {
		t.Errorf("the member whose work merged was told about it: %v", got)
	}
	want := "Call task claim on " + children[1].ID
	if n := harnessMail(r.rvRig, want); n[children[1].Owner] != 1 {
		t.Errorf("the notice does not say %q", want)
	}
	other, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "unrelated", Role: "backend", Files: []string{"docs/**"}})
	r.sw.tellSiblingsOfMerge(other, "mgr")
	if n := harnessMail(r.rvRig, other.ID+" ("); len(n) != 0 {
		t.Errorf("a top-level task told others: %v", n)
	}
}
