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
}
