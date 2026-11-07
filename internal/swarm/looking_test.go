package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
)

// lookRig is a manager's hooks in a team with free slots, and a way to make one of its calls.
func lookRig(t *testing.T, team []string) (*rvRig, func(tool string, in map[string]any) string) {
	t.Helper()
	r := newRVRig(t, Config{MaxWorkers: 8, Team: team}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	h := &holdHooks{s: r.sw, m: r.sw.get("mgr")}
	return r, func(tool string, in map[string]any) string {
		b, _ := json.Marshal(in)
		o := h.BeforeTool(context.Background(), agent.ToolHookCall{Agent: "mgr", Role: "manager", Tool: tool, Input: b})
		if o.Veto {
			t.Fatalf("%s was vetoed: %s", tool, o.Reason)
		}
		return o.Context
	}
}

var readMain = map[string]any{"path": "main.go"}

// A manager that keeps reading while ready tasks could start is told, on the third read, which spawns to make; a spawn ends
// the run of reads, and the count starts again. A manager that had decided to spawn T131 to T136 read for forty minutes.
func TestAManagerReadingWhileReadyTasksWaitIsToldWhichSpawnsToMake(t *testing.T) {
	r, call := lookRig(t, []string{"backend", "backend", "frontend", "tester"})
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T1", Title: "incident API", Role: "backend", Status: StatusTodo},
		{ID: "T2", Title: "incident page", Role: "frontend", Status: StatusTodo},
		{ID: "T3", Title: "after the API", Role: "backend", Status: StatusTodo, Deps: []string{"T1"}},
	}})
	if got := call("read", readMain) + call("task", map[string]any{"action": "get", "id": "T1"}); got != "" {
		t.Fatalf("the first two looks carried %q", got)
	}
	got := call("bash", map[string]any{"command": "git log --oneline -5"})
	if !strings.Contains(got, "Stop reading: T1 and T2 are ready") || !strings.Contains(got, "spawn task=T1 role=backend, spawn task=T2 role=frontend") ||
		!strings.Contains(got, "3 free slots") || strings.Contains(got, "T3") {
		t.Fatalf("the third look carried %q", got)
	}
	if got := call("plan", map[string]any{"items": []any{}}); got != "" {
		t.Fatalf("plan carried %q", got)
	}
	call("spawn", map[string]any{"task": "T9"}) // ends the run, whatever it answers
	if got := call("read", readMain) + call("read", readMain); got != "" {
		t.Fatalf("two reads after a spawn carried %q", got)
	}
	if got := call("read", readMain); !strings.Contains(got, "Stop reading") {
		t.Fatalf("the third read after a spawn carried %q", got)
	}
}

// A submission waiting for the manager comes first: the line names it and the decision to make.
func TestAManagerReadingWhileASubmissionWaitsIsToldToDecideIt(t *testing.T) {
	r, call := lookRig(t, []string{"backend", "reviewer"})
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T1", Title: "rollouts", Role: "backend", Status: StatusTodo},
		{ID: "T2", Title: "next", Role: "backend", Status: StatusTodo},
	}})
	if err := r.sw.Board.Claim("be-1", "T1"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.submitAgreementAt("be-1", "T1", 0, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	call("read", readMain)
	call("grep", map[string]any{"pattern": "x"})
	got := call("read", readMain)
	if !strings.Contains(got, "T1 waits in review") || !strings.Contains(got, "task accept id=T1") || strings.Contains(got, "spawn task=") {
		t.Fatalf("the third look carried %q", got)
	}
}

// On an empty board with the whole team idle, the line comes after lookIdleAfter looks and names the scout when the team has one;
// a team without free capacity or ready work gets no line.
func TestAManagerExploringAloneIsToldToHandTheReadingToTheTeam(t *testing.T) {
	_, call := lookRig(t, []string{"scout", "backend"})
	var got []string
	for i := 0; i < lookIdleAfter; i++ {
		got = append(got, call("grep", map[string]any{"pattern": "Handle"}))
	}
	if strings.Join(got[:lookIdleAfter-1], "") != "" {
		t.Fatalf("the looks before the bound carried %q", got)
	}
	if last := got[lookIdleAfter-1]; !strings.Contains(last, "Stop exploring: 6 calls") || !strings.Contains(last, "spawn role=scout") {
		t.Fatalf("look %d carried %q", lookIdleAfter, last)
	}

	_, call = lookRig(t, []string{"backend"})
	for i := 0; i < lookIdleAfter-1; i++ {
		call("read", readMain)
	}
	if last := call("read", readMain); strings.Contains(last, "scout") || !strings.Contains(last, "task create") {
		t.Fatalf("a team without a scout was told %q", last)
	}
}

func TestLookingTool(t *testing.T) {
	for _, c := range []struct {
		tool, in        string
		looking, counts bool
	}{
		{"read", `{}`, true, true}, {"task", `{"action":"list"}`, true, true},
		{"bash", `{"command":"git log --oneline -5 && sed -n 1,40p main.go"}`, true, true}, {"bash", `{"command":"go test ./..."}`, true, true},
		{"bash", `{"command":"git commit -am wip"}`, false, true}, {"bash", `{"command":"echo x > notes.md"}`, false, true},
		{"bash", `{}`, false, true},
		{"task", `{"action":"accept"}`, false, true}, {"spawn", `{}`, false, true}, {"write", `{}`, false, true},
		{"plan", `{}`, false, false}, {"memory", `{}`, false, false},
	} {
		if l, n := lookingTool(c.tool, json.RawMessage(c.in)); l != c.looking || n != c.counts {
			t.Errorf("%s %s: %v %v", c.tool, c.in, l, n)
		}
	}
}

// A shell command that may change something ends the run of looks, and gets no line: two reads, then a commit.
func TestAManagerShellCommandThatChangesSomethingIsNotARead(t *testing.T) {
	r, call := lookRig(t, []string{"backend"})
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{{ID: "T1", Title: "api", Role: "backend", Status: StatusTodo}}})
	call("read", readMain)
	call("read", readMain)
	if got := call("bash", map[string]any{"command": "git commit -am wip"}); got != "" {
		t.Fatalf("a commit after two reads carried %q", got)
	}
	if got := call("read", readMain) + call("read", readMain); got != "" {
		t.Fatalf("two reads after the commit carried %q", got)
	}
}

// The call is judged by the input it runs with: a task list that the wrapped hooks rewrite into a task accept is a decision.
func TestTheLookCountJudgesTheRewrittenInput(t *testing.T) {
	r, _ := lookRig(t, []string{"backend"})
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{{ID: "T1", Title: "api", Role: "backend", Status: StatusTodo}}})
	h := &holdHooks{s: r.sw, m: r.sw.get("mgr"), inner: rewriteHooks{to: json.RawMessage(`{"action":"accept","id":"T1"}`)}}
	for i := 0; i < lookEvery; i++ {
		o := h.BeforeTool(context.Background(), agent.ToolHookCall{Agent: "mgr", Role: "manager", Tool: "task", Input: json.RawMessage(`{"action":"list"}`)})
		if strings.Contains(o.Context, "Stop reading") {
			t.Fatalf("call %d, rewritten into a decision, carried %q", i+1, o.Context)
		}
	}
}

// A ready task is offered only where a member could start it: a team that does not limit roles still stops at MaxWorkers.
func TestAReadyTaskIsNotOfferedPastTheWorkerLimit(t *testing.T) {
	r, call := lookRig(t, nil)
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{{ID: "T1", Title: "api", Role: "backend", Status: StatusTodo}}})
	r.sw.cfg.MaxWorkers = 0 // every place taken, and no idle member
	for i := 0; i < lookEvery; i++ {
		if got := call("read", readMain); strings.Contains(got, "spawn task=") {
			t.Fatalf("read %d at the worker limit carried %q", i+1, got)
		}
	}
	r.sw.cfg.MaxWorkers = 8
	for i := 0; i < lookEvery-1; i++ {
		call("read", readMain)
	}
	if got := call("read", readMain); !strings.Contains(got, "spawn task=T1 role=backend") || !strings.Contains(got, "8 free slots") {
		t.Fatalf("under the limit the line was %q", got)
	}
}

// A plan in review is not said to be built and tested: its text is its evidence.
func TestAPlanInReviewIsNotSaidToBeBuilt(t *testing.T) {
	r, call := lookRig(t, []string{"backend"})
	r.sw.Board.Restore(&Snapshot{Tasks: []Task{{ID: "T1", Title: "design", Role: "backend", Kind: TaskKindPlan, Status: StatusTodo}}})
	if err := r.sw.Board.Claim("be-1", "T1"); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.submitAgreementAt("be-1", "T1", 0, "the plan", "", "the API takes JSON and answers 200"); err != nil {
		t.Fatal(err)
	}
	var got string
	for i := 0; i < lookEvery; i++ {
		got = call("read", readMain)
	}
	if !strings.Contains(got, "T1 waits in review") || !strings.Contains(got, "a plan or report is its own text") {
		t.Fatalf("the line for a plan in review was %q", got)
	}
}

// A submission is said to be built and tested only where a verifier ran on it: without one, the line says that nothing but its
// worker checked it, and leaves the manager's own check to the doubt the evidence leaves.
func TestASubmissionIsSaidToBeTestedOnlyWhereAVerifierRuns(t *testing.T) {
	for _, verify := range []string{"", "go test ./..."} {
		r, call := lookRig(t, []string{"backend"})
		r.sw.cfg.VerifyCmd = verify
		r.sw.Board.Restore(&Snapshot{Tasks: []Task{{ID: "T1", Title: "rollouts", Role: "backend", Status: StatusTodo}}})
		if err := r.sw.Board.Claim("be-1", "T1"); err != nil {
			t.Fatal(err)
		}
		if err := r.sw.Board.submitAgreementAt("be-1", "T1", 0, "done", "", ""); err != nil {
			t.Fatal(err)
		}
		var got string
		for i := 0; i < lookEvery; i++ {
			got = call("read", readMain)
		}
		said := strings.Contains(got, "built and tested") || strings.Contains(got, "run its verifier")
		if verify != "" && !strings.Contains(got, `run its verifier ("go test ./...") on each submission`) {
			t.Fatalf("the verifier is not named as it is: %q", got)
		}
		if verify == "" && (said || !strings.Contains(got, "No verifier is configured") || !strings.Contains(got, "task accept id=T1")) {
			t.Fatalf("without a verifier the line was %q", got)
		}
		if verify != "" && !said {
			t.Fatalf("with a verifier the line was %q", got)
		}
	}
}
