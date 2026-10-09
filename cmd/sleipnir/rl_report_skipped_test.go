package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// A run in which a task was skipped for a missing tool reports it as skipped, not as rollouts that have no outcome yet.
func TestRLReportSaysSkippedRolloutsWereSkippedAndNotPending(t *testing.T) {
	dir := fakeRun(t, "run-s", "vendor/model-s", 2, 2, func(task, sample int) bool { return true })
	if err := os.RemoveAll(filepath.Join(dir, "task-01")); err != nil { // its rollouts were skipped: no episode was written
		t.Fatal(err)
	}
	sum, err := json.Marshal(env.Summary{Skipped: 2, Skips: []env.SkipRecord{{Task: "task-01", Rollouts: 2, Reason: "missing ruby"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}

	out := (rlRun{t}).must(rlReport, dir)
	if !strings.Contains(out, "2 were skipped because their tasks require tools this machine lacks") {
		t.Errorf("the report does not say the rollouts were skipped:\n%s", out)
	}
	if strings.Contains(out, "no outcome yet") {
		t.Errorf("skipped rollouts are reported as pending:\n%s", out)
	}
}
