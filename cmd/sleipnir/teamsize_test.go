package main

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// A team size a session would refuse is refused first, before a model is asked for: a negative size, one under the minimum, one over
// the ceiling (swarm.max_workers, or the default ceiling when it is not set).
func TestCheckTeamSize(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SLEIPNIR_HOME", t.TempDir())
	for _, n := range []int{0, session.MinTeamWorkers, session.DefaultMaxTeamWorkers} {
		if err := checkTeamSize(n); err != nil {
			t.Errorf("--swarm %d: %v", n, err)
		}
	}
	for n, want := range map[int]string{-3: "0 (a single agent) or at least", 3: "at least 8", 1000: "at most 64"} {
		err := checkTeamSize(n)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--swarm %d: %v, want %q", n, err, want)
		}
	}
	t.Setenv("SLEIPNIR_SWARM_MAX_WORKERS", "200")
	if err := checkTeamSize(100); err != nil {
		t.Errorf("--swarm 100 under a raised ceiling: %v", err)
	}
}
