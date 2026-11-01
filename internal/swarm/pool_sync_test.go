package swarm

import (
	"os"
	"path/filepath"
	"testing"
)

// Members made before the scaffold merged plan the next parent against their own checkout: the pool brings the merged
// work into their trees as it begins, so they read the scaffold where they look for it instead of in the merge commit.
func TestPlanningPoolMembersStartFromMergedWork(t *testing.T) {
	sc := script{
		"fe-1": {act(writeCall("scaffold.txt", "scaffold by fe-1\n")), act(doneCall("T1", "scaffold"))},
	}
	r := newIsoRig(t, isoOpts{cfg: Config{PersistentPools: true, Team: []string{"backend", "backend", "frontend"}, MaxWorkers: 3}, verify: true}, sc.fn())
	r.sw.StartManager()
	members, err := r.sw.ensurePool("backend")
	if err != nil {
		t.Fatal(err)
	}
	r.mustSpawn("frontend", "scaffold", "scaffold.txt")
	r.waitStatus("T1", StatusReview)
	if res := r.accept("T1"); res.IsError {
		t.Fatal(res.Text)
	}
	for _, m := range members {
		rvWait(t, m.id+" to be idle", func() bool { return r.idle(m.id) })
		if _, err := os.Stat(filepath.Join(m.dir, "scaffold.txt")); err == nil {
			t.Fatalf("%s already had the scaffold before its pool began", m.id)
		}
	}
	tk, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "engine", Role: "backend", Files: []string{"engine.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", TaskID: tk.ID, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.sw.Board.Snapshot().Task(tk.ID)
	if got.Pool == nil || len(got.Pool.Members) != 2 {
		t.Fatalf("want a planning pool of two, got %+v", got.Pool)
	}
	for _, m := range members {
		if got := readText(t, filepath.Join(m.dir, "scaffold.txt")); got != "scaffold by fe-1\n" {
			t.Fatalf("%s plans on a stale checkout: scaffold.txt = %q", m.id, got)
		}
	}
}
