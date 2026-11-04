package state

import (
	"testing"
	"time"
)

// A task's history keeps the newest MaxStretches stretches, and SentBack counts the returns to an earlier column, not a failure.
func TestTaskStretchesAndSentBack(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := &taskState{}
	for i, st := range []string{"todo", "doing", "review", "doing", "review", "done"} {
		ts.Status = st
		ts.stretch(at.Add(time.Duration(i) * time.Minute))
	}
	got := ts.Task
	if len(got.History) != 6 || got.History[1].State != TaskRunning || got.History[2].State != TaskVerifying {
		t.Fatalf("history = %+v", got.History)
	}
	if n := got.SentBack(); n != 1 {
		t.Fatalf("SentBack = %d, want 1 (verifying back to running)", n)
	}
	ts.Status = "failed"
	ts.stretch(at.Add(time.Hour))
	if n := ts.Task.SentBack(); n != 1 {
		t.Fatalf("a failure is not a return: SentBack = %d", n)
	}
	for i := 0; i < 2*MaxStretches; i++ {
		ts.stretch(at.Add(time.Duration(i) * time.Second))
	}
	if len(ts.History) != MaxStretches {
		t.Fatalf("kept %d stretches, want %d", len(ts.History), MaxStretches)
	}
}
