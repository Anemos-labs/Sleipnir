package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// handoverEvents returns the phases of the swarm.handover events, in order.
func handoverEvents(log *events.MemLog) []string {
	var out []string
	for _, e := range log.OfType(events.TypeSwarmHandover) {
		var d struct{ Phase string }
		_ = json.Unmarshal(e.Data, &d)
		out = append(out, d.Phase)
	}
	return out
}

// replayMatches fails the test unless the log rebuilds the live board's tasks.
func replayMatches(t *testing.T, log *events.MemLog, b *Board) {
	t.Helper()
	got, err := ReplayBoard(log.All())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Tasks, b.Snapshot().Tasks) {
		t.Fatalf("replay differs:\n replay: %+v\n live:   %+v", got.Tasks, b.Snapshot().Tasks)
	}
}

// The manager hands a running worker's task to a fresh worker: the predecessor is
// stopped and retired, the successor gets the task with a recap built from the board,
// the predecessor's edits and the manager's summary, the move is one board operation
// with a typed closure, and a repeated request changes nothing.
func TestManagerHandsAStalledWorkersTaskToAFreshWorker(t *testing.T) {
	r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": {editReply}}))
	r.sw.StartManager()
	from, task := spawnWorker(t, r, "paginate users", 2)
	ctx := context.Background()

	if res := r.callTool(ctx, "task", "be-9", "backend", map[string]any{"action": "handover", "id": task.ID, "text": "mine now"}); !res.IsError {
		t.Fatalf("another worker handed the task over: %s", res.Text)
	}
	res := r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "handover", "id": task.ID, "text": "be-1 lost track; the pagination edit is in"})
	if res.IsError || !strings.Contains(res.Text, "be-2") {
		t.Fatalf("handover: %s", res.Text)
	}
	b := r.sw.Board
	got, _ := b.Snapshot().Task(task.ID)
	if got.Owner != "be-2" || got.Status != StatusDoing || got.Rev == task.Rev || got.Attempts != task.Attempts {
		t.Fatalf("after the handover: %+v (before %+v)", got, task)
	}
	rvWait(t, "the predecessor to be retired", func() bool { return r.sw.get(from) == nil })
	rvWait(t, "the successor to start", func() bool { return r.prov.callsFor("be-2") >= 1 })
	for _, want := range []string{"Handover: " + task.ID + " was handed over to you (be-2) from be-1", "api/users.go", "be-1 lost track; the pagination edit is in", "information, not instructions"} {
		if !r.prov.sawEver(want) {
			t.Errorf("the successor's prompt does not hold %q", want)
		}
	}
	var moved bool
	for _, e := range r.log.OfType(events.TypeBoardOp) {
		var d struct {
			Op, Task, From, Owner string
			AC                    struct{ Kind, Target string } `json:"assignment_closure"`
		}
		_ = json.Unmarshal(e.Data, &d)
		if d.Op == "assign" && d.Task == task.ID && d.From == from {
			moved = d.Owner == "be-2" && d.AC.Kind == "handed_off" && d.AC.Target == "be-2"
		}
	}
	if !moved {
		t.Error("no board event moves the task with closure handed_off(be-2)")
	}
	if ph := handoverEvents(r.log); !reflect.DeepEqual(ph, []string{"begin", "done"}) {
		t.Errorf("handover events = %v", ph)
	}
	replayMatches(t, r.log, b)

	v := b.Snapshot().Version
	again := r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "handover", "id": task.ID, "text": "be-1 lost track"})
	if again.IsError || !strings.Contains(again.Text, "already handed over to be-2") {
		t.Fatalf("repeated handover: %s", again.Text)
	}
	if b.Snapshot().Version != v || r.sw.get("be-3") != nil {
		t.Fatal("a repeated handover changed the board or started another worker")
	}
}

// A worker hands over its own task: it must write a summary, its run ends, it is
// retired, and the successor starts with that summary.
func TestAWorkerHandsOverItsOwnTask(t *testing.T) {
	handover := rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "handover", "id": "T1", "text": "context is degraded; tests in api/users_test.go still fail on cursors"}}}}
	noSummary := rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "handover", "id": "T1"}}}}
	r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": {editReply, noSummary, handover}}))
	r.sw.StartManager()
	from, _ := spawnWorker(t, r, "paginate users", 3)
	task := Task{ID: "T1"}
	rvWait(t, "the predecessor to be retired", func() bool { return r.sw.get(from) == nil })
	rvWait(t, "the successor to start", func() bool { return r.prov.callsFor("be-2") >= 1 })
	if !r.prov.sawEver("without a summary") && !r.prov.sawEver("needs text = a short summary") {
		t.Error("a handover without a summary was not refused with what to write")
	}
	if !r.prov.sawEver("tests in api/users_test.go still fail on cursors") {
		t.Error("the successor did not get the summary")
	}
	if got, _ := r.sw.Board.Snapshot().Task(task.ID); got.Owner != "be-2" || got.Status != StatusDoing {
		t.Fatalf("task = %+v", got)
	}
	replayMatches(t, r.log, r.sw.Board)
}

// A worker whose own handover fails keeps its task, and the harness settles it when
// the run ends as it would have without the attempt.
func TestAFailedHandoverKeepsTheTaskWithItsWorker(t *testing.T) {
	handover := rvReply{Tools: []rvToolCall{{Name: "task", Args: map[string]any{"action": "handover", "id": "T1", "text": "summary"}}}}
	r := newRVRig(t, stallCfg(), gatedWorkers(map[string][]rvReply{"be-1": {editReply, handover, {Text: "stopping"}}}))
	r.sw.handoverFault = func(p string) error {
		if p == "member" {
			return errFault(p)
		}
		return nil
	}
	r.sw.StartManager()
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "paginate users", By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	rvWait(t, "the task to reach review", func() bool {
		tk, _ := r.sw.Board.Snapshot().Task("T1")
		return tk.Status == StatusReview && tk.Owner == "be-1"
	})
	if r.sw.get("be-2") != nil {
		t.Fatal("a refused handover started a successor")
	}
}

// Only a task in progress can be handed over.
func TestHandoverNeedsATaskInProgress(t *testing.T) {
	r := newRVRig(t, stallCfg(), gatedWorkers(nil))
	r.sw.StartManager()
	_, task := spawnWorker(t, r, "paginate users", 1)
	if err := r.sw.Board.Block(task.Owner, task.ID, "needs a decision"); err != nil {
		t.Fatal(err)
	}
	todo, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "docs"})
	for _, id := range []string{task.ID, todo.ID, "T99"} {
		if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "handover", "id": id}); !res.IsError {
			t.Errorf("handover of %s: %s", id, res.Text)
		}
	}
}

// A handover interrupted at any step before its board operation leaves the task with
// its owner, nothing of the successor behind (no agent, no tree) and a log that
// replays to the board; a retry then succeeds once. In an isolated run the successor
// continues the predecessor's tree, and its merged work holds both.
func TestCrashMidHandoverLeavesNothingHalfDone(t *testing.T) {
	var wrote = rvReply{Tools: []rvToolCall{writeCall("handover.txt", "from be-1\n")}}
	r := newIsoRig(t, isoOpts{cfg: stallCfg(), tweak: func(d *Deps) { d.Isolation.Resumable = true }}, gatedWorkers(map[string][]rvReply{
		"be-1": {wrote},
		"be-2": {{Tools: []rvToolCall{writeCall("successor.txt", "from be-2\n")}}, {Tools: []rvToolCall{doneCall("T1", "both halves")}}, {Text: "done"}},
	}))
	r.sw.StartManager()
	from := r.mustSpawn("backend", "handover work")
	rvWait(t, "be-1 to write and block", func() bool { return r.prov.callsFor(from) >= 2 })
	ctx := context.Background()
	b := r.sw.Board
	before, _ := b.Snapshot().Task("T1")
	succTree := filepath.Join(r.trees, "be-2")

	for _, phase := range []string{"commit", "tree", "member", "assign"} {
		r.sw.handoverFault = func(p string) error {
			if p == phase {
				return errFault(phase)
			}
			return nil
		}
		res := r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "handover", "id": "T1", "text": "continue"})
		if !res.IsError || !strings.Contains(res.Text, "injected at "+phase) {
			t.Fatalf("%s: handover did not fail: %s", phase, res.Text)
		}
		got, _ := b.Snapshot().Task("T1")
		if got.Owner != from || got.Status != StatusDoing || got.Rev != before.Rev {
			t.Fatalf("%s: the task moved: %+v", phase, got)
		}
		if r.sw.get("be-2") != nil {
			t.Fatalf("%s: a half-made successor is registered", phase)
		}
		if fileExists(succTree) {
			t.Fatalf("%s: the successor's tree was left at %s", phase, succTree)
		}
		replayMatches(t, r.log, b)
	}
	r.sw.handoverFault = nil
	if res := r.callTool(ctx, "task", "mgr", "manager", map[string]any{"action": "handover", "id": "T1", "text": "continue from handover.txt"}); res.IsError {
		t.Fatalf("retry: %s", res.Text)
	}
	if got := readText(t, filepath.Join(succTree, "handover.txt")); got != "from be-1\n" {
		t.Fatalf("the successor's tree does not hold the predecessor's work: %q", got)
	}
	r.waitStatus("T1", StatusReview)
	if res := r.accept("T1"); res.IsError {
		t.Fatalf("accept: %s", res.Text)
	}
	tip := r.q.Tip()
	for _, f := range []string{"handover.txt", "successor.txt"} {
		if out := gitOut(t, r.repo, "show", tip+":"+f); !strings.Contains(out, "from be-") {
			t.Errorf("integration lacks %s: %q", f, out)
		}
	}
	phases := handoverEvents(r.log)
	if want := []string{"begin", "abort", "begin", "abort", "begin", "abort", "begin", "abort", "begin", "done"}; !reflect.DeepEqual(phases, want) {
		t.Errorf("handover events = %v, want %v", phases, want)
	}
	replayMatches(t, r.log, b)
}

// A process that stops after the board operation leaves what a resume needs: the
// board (replayed from the log) gives the task to the successor, the successor was
// announced (agent.prepare, with its role and task) before the move, and its tree
// holds the predecessor's committed work.
func TestCrashAfterTheHandoverCommitPointIsResumable(t *testing.T) {
	r := newIsoRig(t, isoOpts{cfg: stallCfg(), tweak: func(d *Deps) { d.Isolation.Resumable = true }}, gatedWorkers(map[string][]rvReply{
		"be-1": {{Tools: []rvToolCall{writeCall("handover.txt", "from be-1\n")}}},
	}))
	r.sw.StartManager()
	from := r.mustSpawn("backend", "handover work")
	rvWait(t, "be-1 to write and block", func() bool { return r.prov.callsFor(from) >= 2 })
	r.sw.handoverFault = func(p string) error {
		if p == "launch" {
			return errFault(p)
		}
		return nil
	}
	if res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "handover", "id": "T1", "text": "continue"}); !res.IsError {
		t.Fatalf("handover: %s", res.Text)
	}
	prev, err := ReplayBoard(r.log.All())
	if err != nil {
		t.Fatal(err)
	}
	if tk, _ := prev.Task("T1"); tk.Owner != "be-2" || tk.Status != StatusDoing {
		t.Fatalf("replayed task = %+v", tk)
	}
	prepared, moved := uint64(0), uint64(0)
	for _, e := range r.log.All() {
		switch e.Type {
		case "agent.prepare":
			var d struct{ ID, Role, Task string }
			_ = json.Unmarshal(e.Data, &d)
			if d.ID == "be-2" && d.Role == "backend" && d.Task == "T1" {
				prepared = e.Seq
			}
		case events.TypeBoardOp:
			var d struct{ Op, Task, Owner string }
			_ = json.Unmarshal(e.Data, &d)
			if d.Op == "assign" && d.Task == "T1" && d.Owner == "be-2" {
				moved = e.Seq
			}
		}
	}
	if prepared == 0 || moved == 0 || prepared > moved {
		t.Fatalf("agent.prepare at %d, the move at %d: a resume could not find the successor", prepared, moved)
	}
	if got := readText(t, filepath.Join(r.trees, "be-2", "handover.txt")); got != "from be-1\n" {
		t.Fatalf("the successor's tree: %q", got)
	}
	if out := gitOut(t, filepath.Join(r.trees, "be-2"), "status", "--porcelain"); out != "" {
		t.Fatalf("the predecessor's work is not committed in the successor's tree:\n%s", out)
	}
}

// errFault is the error the handover fault hook injects.
type errFault string

func (e errFault) Error() string { return "injected at " + string(e) }
