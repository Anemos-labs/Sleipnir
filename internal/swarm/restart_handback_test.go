package swarm

import (
	"fmt"
	"testing"
	"time"
)

// With persistent pools a task a worker stopped on returns to todo and the repair pass starts it on that worker again. After the
// worker's automatic restarts on it ran out, it does not: the manager was told the harness does not restart that worker on it
// again, and the task is the manager's to assign. In a long team run five workers on one provider's model stopped with "no
// endpoints available"; the repair pass started each on its task again every few minutes, the manager read "retrying with its
// existing worker" and reassigned nothing, and the workers on another model stayed idle.
func TestATaskWhoseWorkerRanOutOfRestartsIsLeftToTheManager(t *testing.T) {
	r, clock, id := restartWaiting(t, Config{PersistentPools: true})
	for n := 1; n <= maxAutoRestarts; n++ {
		clock.Advance(restartBackoff << (n - 1))
		r.sw.superviseOnce(clock.Now())
		want := n + 1
		// The next restart is due from the clock as it stands once it is logged; the supervisor passes over one not yet logged.
		rvWait(t, fmt.Sprintf("failed run %d", want), func() bool { e, _ := ends(r, id); return e == want && r.idle(id) && (n == maxAutoRestarts || scheduledOf(r, id)) })
	}
	rvWait(t, "the manager's line", func() bool { return mailSent(r, "does not restart it again") == 1 })
	line := "T1 returned to todo after attempt 3: " + id + " stopped (step limit reached); assign it with spawn, to another worker if the failure is " + id + "'s own (its model or provider). The harness restarted " + id + " automatically twice on this work, and does not restart it again."
	if mailSent(r, line) != 1 || mailSent(r, "retrying with its existing worker") != 0 {
		t.Error("the manager's line does not leave the task to the manager")
	}
	for i := 1; i <= 3; i++ {
		r.sw.repairTick(clock.Now().Add(time.Duration(i) * time.Hour))
	}
	if e, _ := ends(r, id); e != maxAutoRestarts+1 || r.running(id) {
		t.Fatalf("the repair pass started %s on T1 again", id)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusTodo || tk.Owner != "" {
		t.Fatalf("T1 = %s/%s, want todo and unowned", tk.Status, tk.Owner)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Agent: id, TaskID: "T1", By: "mgr"}); err != nil {
		t.Fatalf("the manager's own choice of worker was refused: %v", err)
	}
	if tk, _ := r.sw.Board.Snapshot().Task("T1"); tk.Status != StatusDoing || tk.Owner != id {
		t.Fatalf("T1 = %s/%s after the manager assigned it", tk.Status, tk.Owner)
	}
}
