package swarm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A worker that is still making new edits when it reaches its role's step limit keeps its task and finishes it; with
// FixedSteps (a rollout, or a person who named a limit) the limit stops it as before.
func TestAWorkerStillEditingAtItsStepLimitKeepsItsTask(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fixed=%v", fixed), func(t *testing.T) {
			r := newRVRig(t, Config{MaxWriters: 4, FixedSteps: fixed}, func(_ context.Context, c *rvCall) rvReply {
				switch {
				case c.Role != "backend":
					return rvReply{Text: "ok"}
				case c.Assistants < 9:
					return rvReply{Tools: []rvToolCall{{"edit", map[string]any{"path": fmt.Sprintf("api/f%d.go", c.Assistants)}}}}
				case c.Assistants == 9:
					return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "built"}}}}
				}
				return rvReply{Text: "built the API"}
			})
			be := r.sw.roles["backend"]
			be.MaxSteps = 4
			r.sw.roles["backend"] = be
			if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "build the API", Files: []string{"api/**"}, By: "mgr"}); err != nil {
				t.Fatal(err)
			}
			stopped := func() bool {
				for _, e := range r.log.OfType(events.TypeAgentState) {
					if strings.Contains(string(e.Data), "step limit reached") {
						return true
					}
				}
				return false
			}
			if fixed {
				rvWait(t, "the step limit to stop the worker", stopped)
				return
			}
			rvWait(t, "the task to reach review", func() bool {
				tk, _ := r.sw.Board.Snapshot().Task("T1")
				return tk.Status == StatusReview || tk.Status == StatusDone || stopped()
			})
			if stopped() {
				t.Fatal("the step limit stopped a worker that was making edits")
			}
		})
	}
}
