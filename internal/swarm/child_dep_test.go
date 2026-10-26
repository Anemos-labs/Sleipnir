package swarm

import (
	"strings"
	"testing"
)

// A child cannot depend on the task it is part of: the parent finishes only after its children, so the child would wait for ever.
// A reviewer pool's leader created a child of T66 with deps=[T66], and the review stood still: the child "waits for T66" and T66
// "reconciles once every child is accepted". A board saved with such a dep before it was refused does not wait on it.
func TestAChildCannotDependOnItsParent(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	r.refused(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "deps": []string{r.parent.ID}, "title": "verify the gates", "files": []string{"app/a.go"}}),
		"a child that depends on its parent", "cannot depend on "+r.parent.ID, "Leave "+r.parent.ID+" out of deps")
	created := r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "verify the gates", "files": []string{"app/a.go"}}), "create")
	id := strings.Fields(created)[1]

	// Nor on a task further up, and a revision cannot add one either.
	if _, err := r.sw.Board.CreateTask(r.lead, TaskSpec{Parent: id, Deps: []string{r.parent.ID}, Title: "a grandchild", Files: []string{"app/a.go"}}); err == nil || !strings.Contains(err.Error(), "cannot depend on "+r.parent.ID) {
		t.Errorf("a grandchild that depends on its grandparent: %v", err)
	}
	if err := r.sw.Board.revise(r.lead, id, TaskSpec{Deps: []string{r.parent.ID}}, "add a dep"); err == nil || !strings.Contains(err.Error(), "leave "+r.parent.ID+" out of deps") {
		t.Errorf("a revision that makes the child depend on its parent: %v", err)
	}

	// The same dep on a board saved before: the child starts all the same.
	if err := r.sw.Board.mutate("test", "assign", func(d *draft) error {
		i := taskIdx(d.Snapshot, id)
		c := d.Tasks[i]
		c.Deps = []string{r.parent.ID}
		return d.put(i, c)
	}); err != nil {
		t.Fatal(err)
	}
	if ok, dep := depsDone(r.sw.Board.Snapshot(), func() Task { t, _ := r.sw.Board.Snapshot().Task(id); return t }()); !ok {
		t.Fatalf("%s waits for %s, which it is part of", id, dep.ID)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: r.lead, agent: r.second, id: id}); err != nil {
		t.Fatalf("%s, with its parent in deps on a saved board, cannot be assigned: %v", id, err)
	}
}
