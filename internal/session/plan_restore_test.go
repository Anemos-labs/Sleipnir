package session

import (
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/plan"
)

// A resumed session keeps each agent's last saved plan: the plan tool keeps plans in memory, and a manager resumed with none got no
// reminder to tick its steps and no checks of its plan, while the page still showed the old one. A refused plan is not restored,
// and a goal started after a plan clears the main agent's.
func TestAResumedSessionKeepsItsPlans(t *testing.T) {
	dir := t.TempDir()
	l, err := events.Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	call := func(agent string, items ...map[string]any) {
		if _, err := l.Emit(agent, events.TypeToolCall, map[string]any{"id": "c", "name": "plan", "input": map[string]any{"items": items}}); err != nil {
			t.Fatal(err)
		}
	}
	call("main", map[string]any{"step": "old goal's step"})
	if _, err := l.Emit("", "goal.start", map[string]any{"objective": "ship it"}); err != nil {
		t.Fatal(err)
	}
	call("mgr", map[string]any{"step": "api", "status": "done", "effort": 2, "milestone": "M1"}, map[string]any{"step": "ui", "effort": "6h"})
	call("mgr", map[string]any{"step": "a", "status": "doing"}, map[string]any{"step": "b", "status": "doing"}) // refused: two doing
	call("be-1", map[string]any{"step": "write the store"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	store := plan.NewStore()
	restorePlans(filepath.Join(dir, "events.jsonl"), store)
	if got := store.Get("mgr"); len(got) != 2 || got[0].Status != plan.Done || got[1].Effort != 6 || got[0].Milestone != "M1" {
		t.Errorf("manager: %+v", got)
	}
	if got := store.Get("be-1"); len(got) != 1 {
		t.Errorf("worker: %+v", got)
	}
	if got := store.Get("main"); len(got) != 0 {
		t.Errorf("a plan from before the goal: %+v", got)
	}
}
