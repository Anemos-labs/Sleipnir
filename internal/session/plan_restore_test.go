package session

import (
	"path/filepath"
	"strings"
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
	call("mgr", map[string]any{"step": "store", "status": "doing"}, map[string]any{"step": "page", "status": "doing"}) // two workers' steps
	call("mgr", map[string]any{"step": "api", "status": "done", "effort": 2, "milestone": "M1"}, map[string]any{"step": "ui", "effort": "6h", "status": "doing"}, map[string]any{"step": "ci", "status": "doing"})
	call("mgr", map[string]any{"step": "a", "status": "doing"}, map[string]any{"step": "b"}, map[string]any{"step": "c", "status": "dancing"}) // refused: no such status
	call("be-1", map[string]any{"step": "write the store"})
	call("be-2", map[string]any{"step": "x", "status": "doing"})
	call("be-2", map[string]any{"step": "a", "status": "doing"}, map[string]any{"step": "b", "status": "doing"}) // refused: a worker's two doing
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	store := plan.NewStore()
	restorePlans(filepath.Join(dir, "events.jsonl"), store)
	if got := store.Get("mgr"); len(got) != 3 || got[0].Status != plan.Done || got[1].Effort != 6 || got[0].Milestone != "M1" || got[2].Status != plan.Doing {
		t.Errorf("manager: %+v", got)
	}
	if got := store.Get("be-1"); len(got) != 1 {
		t.Errorf("worker: %+v", got)
	}
	if got := store.Get("be-2"); len(got) != 1 || got[0].Step != "x" {
		t.Errorf("a worker's refused plan of two steps doing was restored: %+v", got)
	}
	if got := store.Get("main"); len(got) != 0 {
		t.Errorf("a plan from before the goal: %+v", got)
	}
}

// A replay keeps the last plan the tool accepted: a later call it refused (a step over the ceiling) is not restored clipped, and a
// step it accepted is counted as it counted it, without its leading spaces.
func TestAReplayKeepsTheLastAcceptedPlan(t *testing.T) {
	dir := t.TempDir()
	l, err := events.Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	emit := func(typ string, data map[string]any) {
		if _, err := l.Emit("be-1", typ, data); err != nil {
			t.Fatal(err)
		}
	}
	emit(events.TypeToolCall, map[string]any{"id": "c1", "name": "plan", "input": map[string]any{"items": []map[string]any{{"step": strings.Repeat(" ", plan.MaxStepRune) + "task"}}}})
	emit(events.TypeToolResult, map[string]any{"id": "c1", "name": "plan", "error": false})
	emit(events.TypeToolCall, map[string]any{"id": "c2", "name": "plan", "input": map[string]any{"items": []map[string]any{{"step": strings.Repeat("x", plan.MaxStepRune+1)}}}})
	emit(events.TypeToolResult, map[string]any{"id": "c2", "name": "plan", "error": true})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	store := plan.NewStore()
	restorePlans(filepath.Join(dir, "events.jsonl"), store)
	if got := store.Get("be-1"); len(got) != 1 || got[0].Step != "task" {
		t.Errorf("restored: %+v", got)
	}
}

// A legacy call that an earlier version accepted with a step over today's ceiling (it cut the step then) is restored clipped, and
// replaces the plan before it.
func TestAReplayRestoresALegacyPlanWithAStepOverTheCeiling(t *testing.T) {
	dir := t.TempDir()
	l, err := events.Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	emit := func(typ string, data map[string]any) {
		if _, err := l.Emit("be-1", typ, data); err != nil {
			t.Fatal(err)
		}
	}
	emit(events.TypeToolCall, map[string]any{"id": "c1", "name": "plan", "input": map[string]any{"items": []map[string]any{{"step": "older"}}}})
	emit(events.TypeToolResult, map[string]any{"id": "c1", "name": "plan", "error": false})
	emit(events.TypeToolCall, map[string]any{"id": "c2", "name": "plan", "input": map[string]any{"items": []map[string]any{{"step": "newer " + strings.Repeat("x", plan.MaxStepRune)}}}})
	emit(events.TypeToolResult, map[string]any{"id": "c2", "name": "plan", "error": false})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	store := plan.NewStore()
	restorePlans(filepath.Join(dir, "events.jsonl"), store)
	got := store.Get("be-1")
	if len(got) != 1 {
		t.Fatalf("restored %d steps: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].Step, "newer ") || len([]rune(got[0].Step)) != plan.MaxStepRune {
		t.Fatalf("restored a step of %d runes: %.40q", len([]rune(got[0].Step)), got[0].Step)
	}
}
