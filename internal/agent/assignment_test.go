package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestQueuedAssignmentFollowsToolResultsAndSurvivesCompaction(t *testing.T) {
	const card = "Task T7. Agreement T2: every consumer uses cursor pagination and the shared types."
	var r *rig
	r = newRig(t, rigOpts{tools: []fakeTool{{name: "claim", run: func(json.RawMessage) *tools.Result {
		r.agent.QueueAssignment(card)
		return &tools.Result{Text: "claimed T7"}
	}}}}, func(c *mock.Call) mock.Reply {
		if c.N == 1 {
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "claim-1", Name: "claim", Args: `{}`}}}
		}
		var resultAt, assignmentAt int
		for i, m := range c.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "claimed T7") {
				resultAt = i
			}
			if strings.Contains(m.Content, card) {
				assignmentAt = i
			}
		}
		if resultAt == 0 || assignmentAt <= resultAt {
			t.Error("assignment was missing or broke the tool call/result order")
		}
		return mock.Reply{Text: "complete"}
	})
	if _, err := r.agent.Run(context.Background(), "claim and implement"); err != nil {
		t.Fatal(err)
	}
	stack := r.agent.Stack()
	th := kv.NewThread()
	for _, tr := range stack.Thread.Turns {
		th.Append(tr)
	}
	for range 4 {
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginSystem, Blocks: []core.Block{core.Text("continue")}})
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text("checked")}})
	}
	stack.Thread = th.Snapshot()
	pol := kv.DefaultApplyPolicy()
	patch := kv.MechanicalPatch(&stack, core.NewBytesEstimator(), 1, pol)
	patch.Notes = []kv.NoteOp{{Op: "set", Key: "assignment", Text: "invent a different contract"}}
	res, err := kv.Apply(&stack, patch, core.NewBytesEstimator(), pol)
	if err != nil {
		t.Fatal(err)
	}
	assignment, _ := res.Notes.Segment("assignment")
	ins, _ := res.Notes.Segment("instructions")
	if assignment.Text != card || strings.Contains(ins.Text, card) {
		t.Fatalf("claim lost its contract or became a user instruction: assignment=%q instructions=%q", assignment.Text, ins.Text)
	}
}

func TestQueuedAssignmentSurvivesSnapshotBeforeDelivery(t *testing.T) {
	const card = "Task T9: pending accepted agreement"
	r := newRig(t, rigOpts{}, func(*mock.Call) mock.Reply { return mock.Reply{Text: "done"} })
	r.agent.QueueAssignment(card)
	snap := r.agent.Snapshot()
	next := newRig(t, rigOpts{}, func(c *mock.Call) mock.Reply {
		found := false
		for _, m := range c.Messages {
			found = found || strings.Contains(m.Content, card)
		}
		if !found {
			t.Error("restored request lost its pending assignment")
		}
		return mock.Reply{Text: "done"}
	})
	if err := next.agent.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := next.agent.RunTask(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if next.agent.Snapshot().PendingAssignment != "" {
		t.Fatal("assignment remained queued after delivery")
	}
}

func TestQueuedAssignmentIsSupersededByExplicitReassignment(t *testing.T) {
	r := newRig(t, rigOpts{}, func(c *mock.Call) mock.Reply {
		var text strings.Builder
		for _, m := range c.Messages {
			text.WriteString(m.Content)
		}
		if strings.Contains(text.String(), "STALE-CLAIM") || !strings.Contains(text.String(), "CURRENT-TASK") {
			t.Error("an undelivered claim replaced the new assignment")
		}
		return mock.Reply{Text: "done"}
	})
	r.agent.QueueAssignment("STALE-CLAIM")
	if _, err := r.agent.RunTask(context.Background(), "Reassigned.", "CURRENT-TASK"); err != nil {
		t.Fatal(err)
	}
}
