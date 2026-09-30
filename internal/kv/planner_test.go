package kv

import (
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/cost"
)

func priced() State {
	return State{PrefixTokens: 12_000, NotesTokens: 600, SpineTokens: 1_200, ThreadTokens: 25_000, PromptTokens: 39_000, ContextWindow: 1_000_000,
		Warm: true, W: cost.Weights{Read: 0.1, Write5m: 1.25, Output: 5}, Write: 1.25, Explicit: true}
}

func TestPlannerStartMatrix(t *testing.T) {
	pl := DefaultPlanner()
	for _, c := range []struct {
		name    string
		mut     func(*State)
		yes     bool
		mode    Mode
		reasonC string
	}{
		{"warm, soft limit, pays for its own fork", func(s *State) {}, true, ModeFork, "soft limit"},
		{"warm, soft limit, one request left: the fork would not pay", func(s *State) { s.Remaining = 1 }, false, ModeFork, "not pay for its own call"},
		{"warm, no prices: thresholds decide", func(s *State) { s.W = cost.Weights{}; s.Write = 0; s.Remaining = 1 }, true, ModeFork, "soft limit"},
		{"warm, below the soft limit", func(s *State) { s.ThreadTokens, s.PromptTokens = 10_000, 24_000 }, false, ModeFork, "no pressure"},
		{"hard limit, warm: fork", func(s *State) { s.ThreadTokens = 70_000 }, true, ModeFork, "hard limit"},
		{"hard limit, cold, something to mask: mask first", func(s *State) { s.ThreadTokens, s.Warm, s.MaskableTokens, s.SinceMask = 70_000, false, 9_000, 50 }, true, ModeMask, "masking first"},
		{"hard limit, cold, nothing to mask: fall through to a fork, never stall", func(s *State) { s.ThreadTokens, s.Warm = 70_000, false }, true, ModeFork, "hard limit"},
		{"hard limit, cold, mask commit was just made: fork", func(s *State) { s.ThreadTokens, s.Warm, s.MaskableTokens, s.SinceMask = 70_000, false, 9_000, 1 }, true, ModeFork, "hard limit"},
		{"window pressure, cold, nothing to mask", func(s *State) { s.PromptTokens, s.Warm = 700_000, false }, true, ModeFork, "context window"},
		{"cold, mask worth it", func(s *State) { s.ThreadTokens, s.Warm, s.MaskableTokens, s.SinceMask = 8_000, false, 3_000, 50 }, true, ModeMask, "shrink for free"},
		{"cold, mask saves too little", func(s *State) { s.ThreadTokens, s.Warm, s.MaskableTokens, s.SinceMask = 8_000, false, 900, 50 }, false, ModeFork, "no pressure"},
		{"cold, soft limit, nothing to mask: a fork would pay a cold write", func(s *State) { s.Warm = false }, false, ModeFork, "nothing worth masking"},
		{"cold, thread below the minimum", func(s *State) { s.ThreadTokens, s.Warm, s.MaskableTokens, s.SinceMask = 1_000, false, 3_000, 50 }, false, ModeFork, "no pressure"},
	} {
		s := priced()
		c.mut(&s)
		d := pl.ShouldStart(s)
		if d.Yes != c.yes || (d.Yes && d.Mode != c.mode) || !strings.Contains(d.Reason, c.reasonC) {
			t.Errorf("%s: got yes=%v mode=%v %q, want yes=%v mode=%v containing %q", c.name, d.Yes, d.Mode, d.Reason, c.yes, c.mode, c.reasonC)
		}
	}
}

func TestShouldCommitJudgesTheLiveThread(t *testing.T) {
	pl := DefaultPlanner()
	// A marginal patch: it covers little of a mostly retained thread and a big
	// spine block is re-written on an explicit cache.
	s := priced()
	s.ThreadTokens, s.SpineTokens, s.Remaining = 24_000, 2_800, 25
	o := Outcome{SnapTokens: 24_000, SpineAdded: 40, RetainedTokens: 20_000, SpineAfter: 2_840, TailTokens: 3_000}
	if d := pl.ShouldCommit(s, o); d.Yes {
		t.Fatalf("a marginal patch is held while the thread is under the limits: %+v", d)
	}
	// The same patch once the LIVE thread is over the hard limit: commit, whatever it costs.
	hard := s
	hard.ThreadTokens = 61_000
	if d := pl.ShouldCommit(hard, o); !d.Yes || !strings.Contains(d.Reason, "hard limit") {
		t.Fatalf("hard limit is judged on the live thread: %+v", d)
	}
	win := s
	win.PromptTokens = 700_000
	if d := pl.ShouldCommit(win, o); !d.Yes {
		t.Fatalf("window pressure forces the commit: %+v", d)
	}
	// Cold: free.
	cold := s
	cold.Warm = false
	if d := pl.ShouldCommit(cold, o); !d.Yes || d.NetITE <= 0 {
		t.Fatalf("cold commits are free: %+v", d)
	}
	// A patch that does not shrink anything never commits, pressure or not.
	if d := pl.ShouldCommit(hard, Outcome{SnapTokens: 5_000, SpineAdded: 3_000, RetainedTokens: 3_000}); d.Yes {
		t.Fatalf("non-shrinking patch: %+v", d)
	}
	// A notes rewrite makes every commit dearer (notes and the whole spine follow it).
	big := priced()
	big.Remaining = 25
	ob := Outcome{SnapTokens: 40_000, SpineAdded: 600, RetainedTokens: 6_000, SpineAfter: 1_800}
	base, _ := commitEconomics(big, ob)
	ob.NotesChanged, ob.NotesAfter = true, 900
	withNotes, _ := commitEconomics(big, ob)
	if withNotes <= base {
		t.Fatalf("changing notes must cost more: %.0f vs %.0f", withNotes, base)
	}
	// On an automatic cache the old spine survives, unless eviction changed its front.
	auto := big
	auto.Explicit, auto.Write, auto.W = false, 1, cost.Weights{Read: 0.25, Write5m: 1}
	o2 := Outcome{SnapTokens: 40_000, SpineAdded: 600, RetainedTokens: 6_000, SpineAfter: 1_800}
	keep, _ := commitEconomics(auto, o2)
	o2.SpineRewritten = true
	evicted, _ := commitEconomics(auto, o2)
	if evicted <= keep {
		t.Fatalf("evicting the front of the spine re-writes all of it on any cache: %.0f vs %.0f", evicted, keep)
	}
}

func TestStalePatchesAreDiscarded(t *testing.T) {
	pl := DefaultPlanner()
	if stale, _ := pl.Stale(20_000, 21_000, 2); stale {
		t.Fatal("a fresh patch is not stale")
	}
	if stale, why := pl.Stale(20_000, 21_000, 9); !stale || !strings.Contains(why, "held") {
		t.Fatalf("held too long: %v %q", stale, why)
	}
	if stale, why := pl.Stale(20_000, 27_000, 2); !stale || !strings.Contains(why, "grew") {
		t.Fatalf("outgrown: %v %q", stale, why)
	}
	if stale, _ := pl.Stale(4_000, 6_500, 2); stale {
		t.Fatal("small threads get an absolute growth allowance")
	}
}
