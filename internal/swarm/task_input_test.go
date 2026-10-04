package swarm

import (
	"strings"
	"testing"
)

func TestTaskMissingIDCanBeCorrectedWithoutChangingTheBoard(t *testing.T) {
	s := secRevSwarm()
	defer s.Shutdown()
	plan, err := s.Board.CreateTask("mgr", TaskSpec{Title: "Agree on the interface", Kind: TaskKindPlan})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Board.Assign("mgr", "rv-1", plan.ID); err != nil {
		t.Fatal(err)
	}
	tool := secRevTool(t, s, "task")
	before := s.Board.Snapshot()
	in := map[string]any{
		"action": "done", "text": "interface agreed",
		"agreement": "All handlers accept context.Context as their first argument.",
	}
	result := secRevCall(t, tool, "rv-1", "reviewer", in)
	if !result.IsError || !strings.Contains(result.Text, "requires id") || !strings.Contains(result.Text, "list") {
		t.Fatalf("missing ID needs an actionable correction, got %+v", result)
	}
	if s.Board.Snapshot() != before {
		t.Fatal("missing ID changed the task board")
	}
	in["id"] = plan.ID
	if result := secRevCall(t, tool, "rv-1", "reviewer", in); result.IsError {
		t.Fatalf("corrected submission failed: %s", result.Text)
	}
	got, _ := s.Board.Snapshot().Task(plan.ID)
	if got.Status != StatusReview || got.Agreement != in["agreement"] {
		t.Fatalf("corrected submission did not preserve manager review: %+v", got)
	}
}

func TestTaskIDActionsRejectMissingIDsBeforeMutation(t *testing.T) {
	s := secRevSwarm()
	defer s.Shutdown()
	tool := secRevTool(t, s, "task")
	for _, action := range []string{"get", "claim", "update", "done", "block", "resume", "accept", "reject", "reopen", "fail"} {
		for _, idCase := range []string{"absent", "empty", "whitespace", "null"} {
			t.Run(action+"/"+idCase, func(t *testing.T) {
				in := map[string]any{"action": action}
				switch idCase {
				case "empty":
					in["id"] = ""
				case "whitespace":
					in["id"] = " \t\n "
				case "null":
					in["id"] = nil
				}
				before := s.Board.Snapshot()
				result := secRevCall(t, tool, "mgr", "manager", in)
				if !result.IsError || !strings.Contains(result.Text, action+" requires id") {
					t.Fatalf("missing task ID: %+v", result)
				}
				if s.Board.Snapshot() != before {
					t.Fatal("invalid input changed the board")
				}
			})
		}
	}
}

func TestTaskIDValidationPreservesActionAndRoleRules(t *testing.T) {
	s := secRevSwarm()
	defer s.Shutdown()
	tool := secRevTool(t, s, "task")
	for _, action := range []string{"create", "accept", "reject", "reopen", "fail"} {
		result := secRevCall(t, tool, "rv-1", "reviewer", map[string]any{"action": action})
		if !result.IsError || !strings.Contains(result.Text, "only the manager") {
			t.Errorf("worker %s lost its role refusal: %+v", action, result)
		}
	}
	for _, action := range []string{"", "unknown"} {
		result := secRevCall(t, tool, "mgr", "manager", map[string]any{"action": action})
		if !result.IsError || !strings.Contains(result.Text, "unknown action") {
			t.Errorf("unknown action requires an action correction: %+v", result)
		}
	}
	for _, in := range []map[string]any{
		{"action": "list"},
		{"action": "create", "title": "A task", "description": "Inspect the interface."},
	} {
		if result := secRevCall(t, tool, "mgr", "manager", in); result.IsError {
			t.Errorf("%s must work without an ID: %s", in["action"], result.Text)
		}
	}
}
