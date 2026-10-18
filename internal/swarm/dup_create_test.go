package swarm

import (
	"strings"
	"sync"
	"testing"
)

// A pool leader's second create under the same parent with the same title (after
// lowercasing and collapsing whitespace) is refused with a tool error that names the
// open task and says to use it. In a live run one leader response held about seventy
// such creates, and each made a new child.
func TestCreateRefusesAnOpenTwinUnderTheSameParent(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	first := r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "re-verify T4 gate at tip", "files": []string{"app/a.go"}}), "create")
	if !strings.HasPrefix(first, "created T2 ") {
		t.Fatalf("setup: %q", first)
	}
	r.refused(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "  Re-verify   T4 gate AT tip ", "files": []string{"app/a.go"}}),
		"a twin create", "T2", "task get id=T2", "instead of creating another")
	r.refused(r.task("mgr", map[string]any{"action": "create", "parent": r.parent.ID, "role": "backend", "title": "re-verify t4 gate at tip", "files": []string{"app/a.go"}}),
		"the manager's twin create", "task get id=T2")
	if n := len(r.sw.Board.Snapshot().Tasks); n != 2 {
		t.Fatalf("board holds %d tasks, want 2", n)
	}
}

// Top-level creates are checked the same way: the manager's twin of an open task is refused.
func TestCreateRefusesAnOpenTopLevelTwin(t *testing.T) {
	b := NewBoard(nil)
	first, err := b.CreateTask("mgr", TaskSpec{Title: "Paginate GET /users", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.CreateTask("mgr", TaskSpec{Title: "paginate get /users", Role: "backend"})
	if err == nil || !strings.Contains(err.Error(), "task get id="+first.ID) {
		t.Fatalf("top-level twin: %v", err)
	}
}

// Parallel identical creates (one model response with many tool calls) leave exactly
// one task: the check runs inside the board's locked create.
func TestConcurrentTwinCreatesLeaveOneTask(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	const n = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spec := TaskSpec{Title: "c5 re-verify T4 gate at tip after rev7 rejection", Parent: r.parent.ID, Role: "backend", Files: []string{"app/a.go"}}
			if _, err := r.sw.Board.CreateTask(r.lead, spec); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	children := 0
	for _, task := range r.sw.Board.Snapshot().Tasks {
		if task.Parent == r.parent.ID {
			children++
		}
	}
	if ok != 1 || children != 1 {
		t.Fatalf("%d creates succeeded and %d children exist, want 1 and 1", ok, children)
	}
}

// A different title, or a twin that is done or failed, does not block a create.
func TestCreateAllowsDistinctOrClosedTwins(t *testing.T) {
	r := newPoolRig(t)
	r.executing()
	create := func(title string) string {
		return r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": title, "files": []string{"app/a.go"}}).Text
	}
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "verify", "files": []string{"app/a.go"}}), "create T2")
	r.must(r.task(r.lead, map[string]any{"action": "create", "parent": r.parent.ID, "title": "verify again", "files": []string{"app/a.go"}}), "a different title")

	if err := r.sw.Board.resolve("mgr", "T2", "not needed"); err != nil {
		t.Fatal(err)
	}
	if got := create("verify"); !strings.HasPrefix(got, "created T4 ") {
		t.Fatalf("a done twin blocked the create: %q", got)
	}
	if err := r.sw.Board.Fail("mgr", "T4", closeAs(CloseCanceled), "gave up"); err != nil {
		t.Fatal(err)
	}
	if got := create("Verify"); !strings.HasPrefix(got, "created T5 ") {
		t.Fatalf("a failed twin blocked the create: %q", got)
	}
}
