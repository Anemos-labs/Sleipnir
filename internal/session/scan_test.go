package session

import "testing"

// The team a scan view shows is the team's own slots, not the swarm's ceiling, which a saved team can be smaller than.
func TestTeamWorkersCountsTheSlots(t *testing.T) {
	if n := teamWorkers(8, 32); n != 8 {
		t.Fatalf("a team of 8 slots under a ceiling of 32 = %d", n)
	}
	if n := teamWorkers(0, 24); n != 24 {
		t.Fatalf("--swarm 24 without slots = %d", n)
	}
}
