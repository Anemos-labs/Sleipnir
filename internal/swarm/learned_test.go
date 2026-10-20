package swarm

import (
	"fmt"
	"strings"
	"testing"
)

// A role note is a field note: it reaches the role's project memory only when a task of its author is accepted, once,
// grouped by role, and a session without the memory keeps nothing.
func TestRoleNotesReachTheProjectMemoryWhenTheTaskIsAccepted(t *testing.T) {
	var got []string
	s := New(Config{SessionID: "sec", OnLearned: func(role string, texts []string) {
		got = append(got, role+": "+strings.Join(texts, " | "))
	}}, Deps{}, nil)
	s.members = map[string]*member{"be-1": {id: "be-1", role: "backend"}, "be-2": {id: "be-2", role: "backend"}, "fe-1": {id: "fe-1", role: "frontend"}}
	note := secRevTool(t, s, "note")
	r := secRevCall(t, note, "be-1", "backend", map[string]any{"text": "regenerate the client with make gen", "scope": "role"})
	if r.IsError || !strings.Contains(r.Text, "once your task is accepted") {
		t.Fatalf("a role note: %q", r.Text)
	}
	secRevCall(t, note, "be-1", "backend", map[string]any{"text": "a shared fact"})
	secRevCall(t, note, "be-2", "backend", map[string]any{"text": "the db runs in docker", "scope": "role"})
	secRevCall(t, note, "fe-1", "frontend", map[string]any{"text": "tokens live in theme.css", "scope": "role"})

	s.flushLearned(Task{ID: "T1", Owner: "be-1"})
	if fmt.Sprint(got) != "[backend: regenerate the client with make gen]" {
		t.Errorf("after T1: %v", got)
	}
	s.flushLearned(Task{ID: "T1", Owner: "be-1"})
	if len(got) != 1 {
		t.Errorf("a second accept repeated the notes: %v", got)
	}
	got = nil
	s.flushLearned(Task{ID: "T2", Owner: "fe-1", Pool: &PoolState{Members: []string{"be-2", "fe-1"}}})
	if fmt.Sprint(got) != "[backend: the db runs in docker frontend: tokens live in theme.css]" {
		t.Errorf("a pool's accept: %v", got)
	}
}

func TestRoleNotesAreNotKeptWithoutTheMemory(t *testing.T) {
	s := secRevSwarm()
	r := secRevCall(t, secRevTool(t, s, "note"), "be-1", "backend", map[string]any{"text": "x", "scope": "role"})
	if r.IsError || strings.Contains(r.Text, "accepted") {
		t.Errorf("a role note without the memory: %q", r.Text)
	}
	if len(s.learned) != 0 {
		t.Errorf("kept %v", s.learned)
	}
}

func TestRoleNotesPerAgentAreBounded(t *testing.T) {
	s := New(Config{SessionID: "sec", OnLearned: func(string, []string) {}}, Deps{}, nil)
	for i := 0; i < maxLearned+5; i++ {
		s.recordLearned("be-1", "backend", fmt.Sprint("fact ", i))
	}
	s.recordLearned("be-1", "backend", "fact 0")
	if n := len(s.learned["be-1"].texts); n != maxLearned {
		t.Errorf("kept %d", n)
	}
}
