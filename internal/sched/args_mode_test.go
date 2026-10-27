package sched

import (
	"slices"
	"testing"
)

// A job always runs with its mode named: one with none would choose bypass for itself, nobody being there to answer.
func TestArgsAlwaysNameTheMode(t *testing.T) {
	for mode, want := range map[string]string{"": "default", "plan": "plan", "accept-edits": "accept-edits"} {
		args := Args(Job{Goal: "g", Mode: mode})
		i := slices.Index(args, "--mode")
		if i < 0 || i+1 >= len(args) || args[i+1] != want {
			t.Errorf("mode %q: %q", mode, args)
		}
	}
}
