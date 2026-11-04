package session

import (
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// The start of a task's current assignment survives a resume's own requeue, and is reset by a requeue that puts the work back on
// the board: one task's, or a worker's tasks released together.
func TestTrackBeganResetsOnARequeueButNotOnAResume(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 3, 17, m, 0, 0, time.UTC) }
	began, blocked := map[string]time.Time{}, map[string]time.Time{}
	trackBegan(began, blocked, "assign", "T5", "doing", "", nil, at(0))
	trackBegan(began, blocked, "update", "T5", "doing", "reading", nil, at(5))
	trackBegan(began, blocked, "requeue", "", "", swarm.ResumedLine, []string{"T5"}, at(10))
	trackBegan(began, blocked, "assign", "T5", "doing", "", nil, at(11))
	if got := began["T5"]; !got.Equal(at(0)) {
		t.Fatalf("after a resume T5 began at %v, want %v", got, at(0))
	}
	trackBegan(began, blocked, "requeue", "", "", "be-4 left the team", []string{"T5", "T6"}, at(20))
	trackBegan(began, blocked, "assign", "T5", "doing", "", nil, at(21))
	if got := began["T5"]; !got.Equal(at(21)) {
		t.Fatalf("after a worker's tasks were released T5 began at %v, want %v", got, at(21))
	}
	trackBegan(began, blocked, "requeue", "T5", "todo", "stopped", nil, at(30))
	if _, ok := began["T5"]; ok {
		t.Fatal("a requeue of T5 kept its start")
	}
}

// The time a task waited blocked is not the work's: its resume moves the start on by the wait, and a requeue forgets a block.
func TestTrackBeganLeavesOutTheTimeATaskWaitedBlocked(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 3, 21, m, 0, 0, time.UTC) }
	began, blocked := map[string]time.Time{}, map[string]time.Time{}
	trackBegan(began, blocked, "assign", "T6", "doing", "", nil, at(0))
	trackBegan(began, blocked, "block", "T6", "blocked", "waits for T5", nil, at(2))
	trackBegan(began, blocked, "resume", "T6", "doing", "", nil, at(46))
	if got := began["T6"]; !got.Equal(at(44)) {
		t.Fatalf("after a 44 minute block T6 began at %v, want %v", got, at(44))
	}
	trackBegan(began, blocked, "block", "T6", "blocked", "waits for T5", nil, at(50))
	trackBegan(began, blocked, "requeue", "T6", "todo", "stopped", nil, at(55))
	trackBegan(began, blocked, "assign", "T6", "doing", "", nil, at(57))
	trackBegan(began, blocked, "resume", "T6", "doing", "", nil, at(58))
	if got := began["T6"]; !got.Equal(at(57)) {
		t.Fatalf("a block before a requeue moved the new start: %v, want %v", got, at(57))
	}
}

// A task still blocked when the team stopped waits until the resume puts it back on the board: a later restart does not count
// that wait as work.
func TestTrackBeganEndsABlockAtTheResumeOfTheRun(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 3, 21, m, 0, 0, time.UTC) }
	began, blocked := map[string]time.Time{}, map[string]time.Time{}
	trackBegan(began, blocked, "assign", "T6", "doing", "", nil, at(0))
	trackBegan(began, blocked, "block", "T6", "blocked", "waits for T5", nil, at(10))
	trackBegan(began, blocked, "requeue", "", "", swarm.ResumedLine, []string{"T6"}, at(40))
	trackBegan(began, blocked, "assign", "T6", "doing", "", nil, at(41))
	trackBegan(began, blocked, "requeue", "", "", swarm.ResumedLine, []string{"T6"}, at(50))
	if got := began["T6"]; !got.Equal(at(30)) {
		t.Fatalf("T6 began at %v, want %v: the 30 minute wait before the first resume counted as work", got, at(30))
	}
	if _, ok := blocked["T6"]; ok {
		t.Fatal("the block outlived the resume")
	}
}
