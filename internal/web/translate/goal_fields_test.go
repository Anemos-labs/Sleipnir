package translate

import (
	"testing"
	"time"
)

// A goal that has run continuation turns, or that the judge has given a reason for, sends both: the page reads each of them and
// every field of a goal event is omitted when empty.
func TestAGoalCarriesItsTurnsAndReason(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(b.add(time.Second, "", "goal.state", map[string]any{"goal": map[string]any{"Objective": "Build the shop", "Max": 20, "Turns": 3, "Reason": "T2 unfinished"}}))
	goals := ofKind(h.decoded(), "goal")
	if len(goals) != 1 {
		t.Fatalf("goals: %v", goals)
	}
	if goals[0]["turns"] != float64(3) || goals[0]["reason"] != "T2 unfinished" {
		t.Errorf("the goal lost its turns or its reason: %v", goals[0])
	}
}
