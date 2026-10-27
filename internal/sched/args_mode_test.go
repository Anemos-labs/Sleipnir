package sched

import (
	"slices"
	"testing"
	"time"
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

// The search ends across every clock change of a zone that moves at midnight or at an odd hour, and finds the next run.
func TestNextEndsAcrossClockChanges(t *testing.T) {
	for _, zone := range []string{"America/Santiago", "America/Havana", "Asia/Tehran", "Europe/London", "Australia/Lord_Howe"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no zone data: %v", err)
		}
		c, err := ParseCron("30 0 * * *")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
		done := make(chan struct{})
		go func() {
			defer close(done)
			at := start
			for i := 0; i < 400; i++ {
				n, ok := c.Next(at)
				if !ok || !n.After(at) {
					t.Errorf("%s: Next(%v) = %v %v", zone, at, n, ok)
					return
				}
				at = n
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the search did not end", zone)
		}
	}
}
