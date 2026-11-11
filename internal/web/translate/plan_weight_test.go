package translate

import (
	"fmt"
	"testing"
	"time"
)

// The main agent's plan carries each step's estimate and milestone, which the page weighs the goal's progress by; a plan with
// neither sends neither field.
func TestAPlanCarriesItsEstimatesAndMilestones(t *testing.T) {
	start := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, Config{StartedAt: start})
	h.feed(
		ev(1, start.Add(time.Second), "mgr", "tool.call", map[string]any{"id": "p1", "name": "plan", "input": map[string]any{"items": []map[string]any{
			{"step": "api", "status": "done", "effort": 2, "milestone": "m1"}, {"step": "ui", "effort": "6h", "milestone": "M2"}, {"step": "release"}}}}),
		ev(2, start.Add(2*time.Second), "mgr", "tool.call", map[string]any{"id": "p2", "name": "plan", "input": map[string]any{"items": []map[string]any{
			{"step": "just this"}}}}),
	)
	plans := ofKind(h.decoded(), "plan")
	if len(plans) != 2 {
		t.Fatalf("plans: %v", plans)
	}
	if got := fmt.Sprint(plans[0]["eff"], plans[0]["mile"]); got != "[2 6 0] [M1 M2 ]" {
		t.Errorf("weighed plan: %v", plans[0])
	}
	if _, ok := plans[1]["eff"]; ok {
		t.Errorf("a plan without estimates: %v", plans[1])
	}
	if _, ok := plans[1]["mile"]; ok {
		t.Errorf("a plan without milestones: %v", plans[1])
	}
}

// A plan call the tool refused does not stay on the page: the plan before it is shown again.
func TestARefusedPlanIsTakenBack(t *testing.T) {
	start := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, Config{StartedAt: start})
	h.feed(
		ev(1, start.Add(time.Second), "mgr", "tool.call", map[string]any{"id": "p1", "name": "plan", "input": map[string]any{"items": []map[string]any{{"step": "kept"}}}}),
		ev(2, start.Add(2*time.Second), "mgr", "tool.result", map[string]any{"id": "p1", "name": "plan", "error": false}),
		ev(3, start.Add(3*time.Second), "mgr", "tool.call", map[string]any{"id": "p2", "name": "plan", "input": map[string]any{"items": []map[string]any{{"step": "refused"}}}}),
		ev(4, start.Add(4*time.Second), "mgr", "tool.result", map[string]any{"id": "p2", "name": "plan", "error": true}),
	)
	plans := ofKind(h.decoded(), "plan")
	if len(plans) == 0 || fmt.Sprint(plans[len(plans)-1]["steps"]) != "[kept]" {
		t.Fatalf("plans: %v", plans)
	}
}
