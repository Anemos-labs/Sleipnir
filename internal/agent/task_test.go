package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// Run is for what a person typed; RunTask is for what the harness hands over (a swarm's
// kickoff or a reused worker's next assignment). The two differ in whose word the input
// is: compaction pins a person's words into the instructions the agent obeys, and a
// task never.

func lastInputEvent(t *testing.T, r *rig) map[string]any {
	t.Helper()
	evs := r.log.OfType(events.TypeUserInput)
	if len(evs) == 0 {
		t.Fatal("no user.input event")
	}
	var m map[string]any
	if err := json.Unmarshal(evs[len(evs)-1].Data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunTaskTurnSaysWhoSpeaksAndCarriesTheAssignment(t *testing.T) {
	var seen string
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		seen = c.LastUser()
		return mock.Reply{Text: "on it"}
	})
	if _, err := r.agent.RunTask(context.Background(), "New assignment. It replaces the one in <my-notes>.", "You are be-1. Your assignment is task T-2: add retry."); err != nil {
		t.Fatal(err)
	}
	first := r.agent.Thread().Snapshot().Turns[0]
	if first.Role != core.RoleUser || first.Origin != core.OriginTask {
		t.Fatalf("the input turn is %s/%s, want user/task", first.Role, first.Origin)
	}
	if len(first.Blocks) != 2 || kv.IsTask(first.Blocks[0]) || !kv.IsTask(first.Blocks[1]) ||
		first.Blocks[0].Text != "New assignment. It replaces the one in <my-notes>." || first.Blocks[1].Text != "You are be-1. Your assignment is task T-2: add retry." {
		t.Fatalf("blocks: %+v", first.Blocks)
	}
	// The model reads both, as text.
	if !strings.Contains(seen, "New assignment") || !strings.Contains(seen, "add retry") {
		t.Fatalf("the model was sent %q", seen)
	}
	ev := lastInputEvent(t, r)
	if ev["origin"] != "task" || ev["text"] != "New assignment. It replaces the one in <my-notes>." || ev["assignment"] != "You are be-1. Your assignment is task T-2: add retry." {
		t.Fatalf("user.input: %v", ev)
	}
}

func TestRunTaskKickoffWithoutAnAssignmentIsOneBlock(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	if _, err := r.agent.RunTask(context.Background(), "Begin task T-1: fix it. Your assignment and scope are in <my-notes>.", ""); err != nil {
		t.Fatal(err)
	}
	first := r.agent.Thread().Snapshot().Turns[0]
	if first.Origin != core.OriginTask || len(first.Blocks) != 1 || kv.IsTask(first.Blocks[0]) {
		t.Fatalf("turn: %+v", first)
	}
	if ev := lastInputEvent(t, r); ev["origin"] != "task" || ev["assignment"] != nil {
		t.Fatalf("user.input: %v", ev)
	}
}

// A person's input keeps the plain shape it always had, byte for byte.
func TestRunInputIsTheUsersAndKeepsItsEventShape(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	if _, err := r.agent.Run(context.Background(), "hello there"); err != nil {
		t.Fatal(err)
	}
	if first := r.agent.Thread().Snapshot().Turns[0]; first.Origin != core.OriginUser || len(first.Blocks) != 1 || first.Blocks[0].MediaType != "" {
		t.Fatalf("turn: %+v", first)
	}
	evs := r.log.OfType(events.TypeUserInput)
	if got := string(evs[0].Data); got != `{"text":"hello there"}` {
		t.Fatalf("user.input data = %s", got)
	}
}

// Nothing to hand over: the run continues from the thread as it stands, as Run("") does.
func TestRunTaskWithNothingAddsNoTurn(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	if _, err := r.agent.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	before := len(r.agent.Thread().Snapshot().Turns)
	events0 := len(r.log.OfType(events.TypeUserInput))
	if _, err := r.agent.RunTask(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if got := len(r.log.OfType(events.TypeUserInput)); got != events0 {
		t.Fatalf("an empty task logged an input: %d -> %d", events0, got)
	}
	if after := len(r.agent.Thread().Snapshot().Turns); after <= before {
		t.Fatalf("the run did not continue (turns %d -> %d)", before, after)
	}
}

// The whole path: a reused worker gets a new assignment as a task turn; when that turn
// is folded, the notes' assignment is the new card and the instructions section holds
// nothing the harness said.
func TestRunTaskAssignmentSurvivesCompactionAsTheAssignmentNotAnInstruction(t *testing.T) {
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 1_000_000 // the background planner stays out of it
	pl.HardThreadTokens = 2_000_000
	notes := kv.NewLayer("notes:be-1", kv.KindNotes, 1, []kv.Segment{{Key: "assignment", Text: "You are be-1. Your assignment is task T-1: build the parser.", Vol: kv.VolFrozen}})
	r := newRig(t, rigOpts{planner: pl, notes: notes}, scriptedWork(8, func(c *mock.Call) string {
		n := 0
		for _, m := range c.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		return compactorPatch(2*n - 3)(c)
	}))
	card := "You are be-1. Your assignment is task T-2: add retry to the client.\nScope: client/**"
	if _, err := r.agent.RunTask(context.Background(), "New assignment. It replaces the assignment in <my-notes>: the previous task is finished.", card); err != nil {
		t.Fatal(err)
	}
	rep, err := r.agent.CompactNow(context.Background(), "")
	if err != nil || rep.Mode != "model" || rep.FoldedTurns == 0 {
		t.Fatalf("compaction: %+v %v", rep, err)
	}
	stk := r.agent.Stack()
	as, ok := stk.Notes.Segment("assignment")
	if !ok || as.Text != card {
		t.Fatalf("the assignment section is %q, want the new card", as.Text)
	}
	if ins, ok := stk.Notes.Segment("instructions"); ok {
		for _, leak := range []string{"T-2", "New assignment", "add retry"} {
			if strings.Contains(ins.Text, leak) {
				t.Fatalf("the harness's task was pinned as the user's instructions (%q): %s", leak, ins.Text)
			}
		}
	}
}
