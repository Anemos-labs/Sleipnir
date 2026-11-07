package swarm

import (
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
