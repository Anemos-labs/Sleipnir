package swarm

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/plan"
)

// An accepted task reminds a manager that keeps a plan with open steps to mark the step it finishes done: a manager that delegates
// its plan's steps and never comes back to them shows a goal half built as not started. Without a plan, or with every step done,
// the result says nothing more.
func TestAcceptRemindsTheManagerOfItsPlan(t *testing.T) {
	plans := plan.NewStore()
	s := New(Config{SessionID: "t"}, Deps{Plans: plans}, nil)
	if n := s.planNudge("mgr"); n != "" {
		t.Errorf("no plan: %q", n)
	}
	plans.Set("mgr", []plan.Item{{Step: "storage", Status: plan.Done}, {Step: "api", Status: plan.Pending}})
	if n := s.planNudge("mgr"); !strings.Contains(n, "mark it done with the plan tool") {
		t.Errorf("open plan: %q", n)
	}
	plans.Set("mgr", []plan.Item{{Step: "storage", Status: plan.Done}})
	if n := s.planNudge("mgr"); n != "" {
		t.Errorf("finished plan: %q", n)
	}
	if n := (New(Config{SessionID: "t"}, Deps{}, nil)).planNudge("mgr"); n != "" {
		t.Errorf("no plan tool: %q", n)
	}
}
