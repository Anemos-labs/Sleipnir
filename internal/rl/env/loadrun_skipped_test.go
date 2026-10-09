package env

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// A rollout skipped for a missing tool writes no episode, so reading the run directory back must not mistake it for one that
// has yet to run: the run's summary names the skipped rollouts, and the report counts them apart from the pending ones.
func TestLoadRunCountsSkippedRolloutsApartFromPendingOnes(t *testing.T) {
	dir := writeRun(t, runSpec{
		model: "m/x", group: 3,
		tasks: []ManifestTask{mtask("a", "v1"), mtask("needs-ruby", "v1")},
		episodes: map[string]*rl.Episode{
			"a/0": episode(true, "done", core.Usage{}, 0, nil),
			"a/1": episode(false, "done", core.Usage{}, 0, nil),
			// a/2 has not run; needs-ruby/0..2 were skipped
		},
	})
	sum, err := json.Marshal(Summary{Skipped: 3, Skips: []SkipRecord{{Task: "needs-ruby", Rollouts: 3, Reason: "missing ruby"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := LoadRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Completed != 2 || rep.Pending != 1 || rep.Skipped != 3 {
		t.Fatalf("completed %d pending %d skipped %d, want 2 1 3", rep.Completed, rep.Pending, rep.Skipped)
	}

	// Without the summary nothing says why the episodes are missing: they are pending, as before.
	if err := os.Remove(filepath.Join(dir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	if rep, err = LoadRun(dir); err != nil || rep.Pending != 4 || rep.Skipped != 0 {
		t.Fatalf("without a summary: pending %d skipped %d (%v), want 4 and 0", rep.Pending, rep.Skipped, err)
	}
}
