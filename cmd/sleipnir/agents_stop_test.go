package main

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// /agents stop ID needs a team and exactly one id; anything else after /agents is answered with the usage. (The stop itself is
// swarm.StopAgent's, tested in internal/swarm.)
func TestAgentsStopSlashRefusesWhatItCannotStop(t *testing.T) {
	var b strings.Builder
	agentsSlash(&b, &session.Session{}, []string{"stop", "be-1"})
	if !strings.Contains(b.String(), "single agent has no workers") {
		t.Fatalf("single agent: %q", b.String())
	}
	for _, args := range [][]string{{"stop"}, {"kill", "be-1"}, {"stop", "be-1", "be-2"}} {
		b.Reset()
		agentsSlash(&b, &session.Session{}, args)
		if !strings.HasPrefix(b.String(), "usage: /agents") {
			t.Fatalf("%q: %q", args, b.String())
		}
	}
}
