package swarm

import (
	"context"
	"strings"
	"testing"
)

// An agent line says when the agent's task is already closed: an agent keeps its task until its run ends, and "dc-1 docs running
// T29" read as T29 still being worked on minutes after it was accepted.
func TestAgentLineSaysItsTaskIsClosed(t *testing.T) {
	snap := &Snapshot{Tasks: []Task{{ID: "T29", Status: StatusDone}, {ID: "T30", Status: StatusDoing}}}
	if got := agentLine(AgentInfo{ID: "dc-1", Role: "docs", State: "running", Task: "T29", Line: "thinking"}, snap); got != "dc-1 docs running T29 (done) — thinking" {
		t.Errorf("closed task: %q", got)
	}
	if got := agentLine(AgentInfo{ID: "be-1", Role: "backend", State: "running", Task: "T30"}, snap); got != "be-1 backend running T30" {
		t.Errorf("open task: %q", got)
	}
}

// A verification that fails in a shared checkout names the other work in progress there, whose unfinished edits it ran over too:
// a manager twice reported a failure from another worker's edits as the accepted task's own.
func TestAFailedAcceptNamesTheWorkInProgressBesideIt(t *testing.T) {
	s := New(Config{SessionID: "t"}, Deps{}, nil)
	task := Task{ID: "T14", Status: StatusReview, Owner: "rv-1"}
	if got := s.inFlightNote(task); got != "" {
		t.Errorf("nothing else in progress: %q", got)
	}
	s.Board.Restore(&Snapshot{Tasks: []Task{
		{ID: "T15", Status: StatusTodo, Files: []string{"main.go"}},
		{ID: "T16", Status: StatusTodo},
		{ID: "T17", Status: StatusTodo},
	}})
	for _, c := range [][2]string{{"be-1", "T15"}, {"rv-1", "T16"}} {
		if err := s.Board.claim(c[0], c[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	got := s.inFlightNote(task)
	if !strings.Contains(got, "(T15 (be-1: main.go))") || strings.Contains(got, "T16") || strings.Contains(got, "T17") || !strings.Contains(got, "not of T14") {
		t.Errorf("note = %q", got)
	}
}

// A team that keeps its workers does not offer fail, which it refuses, and its refusal names the call for a superseded task.
func TestAPersistentTeamDescribesNoFail(t *testing.T) {
	keep := &taskTool{s: New(Config{SessionID: "t", PersistentPools: true}, Deps{}, nil)}
	if d := keep.Spec().Description; strings.Contains(d, "fail (id, reason") || !strings.Contains(d, "resolve closes one that is superseded") {
		t.Errorf("persistent: %s", d)
	}
	plain := &taskTool{s: New(Config{SessionID: "t"}, Deps{}, nil)}
	if d := plain.Spec().Description; !strings.Contains(d, "fail (id, reason") {
		t.Errorf("plain: %s", d)
	}
}

// A spawn that gives the task to the role's pool says so, and how to give small work to one member: a manager that named be-1
// read "be-1 started" and learned of the pool only when its plan round held both members.
func TestASpawnThatStartsAPoolSaysSo(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend"}}, idleModel)
	task, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "stdin for --write", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	res := r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": task.ID})
	if res.IsError || !strings.Contains(res.Text, "leads "+task.ID+" as the backend pool (") || !strings.Contains(res.Text, "spawn with solo=true") {
		t.Fatalf("spawn: %s", res.Text)
	}
	byTitle := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend"}}, idleModel)
	if res := byTitle.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": "stdin for --check"}); res.IsError || !strings.Contains(res.Text, "as the backend pool (") {
		t.Errorf("a spawn by title: %s", res.Text)
	}
	r2 := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 3, Team: []string{"backend", "backend"}}, idleModel)
	solo, _ := r2.sw.Board.CreateTask("mgr", TaskSpec{Title: "--version", Role: "backend"})
	if res := r2.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "backend", "task": solo.ID, "solo": true}); res.IsError || strings.Contains(res.Text, "pool") {
		t.Errorf("a solo spawn: %s", res.Text)
	}
}

// The task tool refuses a field it does not have, naming what it takes: a tester's 5,600-character table sent as result= was
// dropped, and its task read "completed". A member's id that does not exist yet is explained.
func TestTheTaskToolRefusesAFieldItDoesNotHave(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 3, Team: []string{"backend", "tester"}}, idleModel)
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "list", "result": "a long table"})
	if !res.IsError || !strings.Contains(res.Text, `no field "result"`) || !strings.Contains(res.Text, "text (a done's result") {
		t.Errorf("result=: %s", res.Text)
	}
	task, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "e2e", Role: "tester"})
	res = r.callTool(context.Background(), "spawn", "mgr", "manager", map[string]any{"role": "tester", "task": task.ID, "agent": "ts-1"})
	if !res.IsError || !strings.Contains(res.Text, "gets its id when it first starts") {
		t.Errorf("agent=ts-1 before it exists: %s", res.Text)
	}
}

// The review line names the manager's own edits as such, not as "manager changes", which a manager took for a commit.
func TestTheReviewLineNamesTheManagersEdits(t *testing.T) {
	if got := shownMerge("manager changes"); got != "the manager's own edits" {
		t.Errorf("shownMerge = %q", got)
	}
	if got := shownMerge("T7"); got != "T7" {
		t.Errorf("shownMerge(T7) = %q", got)
	}
}
