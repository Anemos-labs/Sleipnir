package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// fakeRun writes a run directory of `tasks` tasks with `group` samples each, where passing(task, sample) decides the
// verdict: what rl rollout leaves behind, without running anything.
func fakeRun(t *testing.T, id, model string, tasks, group int, passing func(task, sample int) bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	m := env.Manifest{Schema: env.ManifestSchema, RunID: id, GroupSize: group, Config: env.ManifestConfig{Seed: 1},
		Policy: env.ManifestPolicy{Model: model}}
	put := func(name string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < tasks; i++ {
		tid := fmt.Sprintf("task-%02d", i)
		tag := []string{"even", "odd"}[i%2]
		m.Tasks = append(m.Tasks, env.ManifestTask{ID: tid, Kind: "fix", Repo: "repos/x", Tags: []string{"go", tag}, Team: rl.Team{Mode: "single"}, VerifierVersion: "v1"})
		for s := 0; s < group; s++ {
			pass := passing(i, s)
			ep := rl.Episode{Schema: rl.SchemaEpisode, ID: tid + "/" + fmt.Sprint(s), TaskID: tid, Sample: s,
				Cost: rl.CostRef{USD: 0.01, Requests: 6, WallMs: 5000, Usage: core.Usage{InputTokens: 100, CacheReadTokens: 300, OutputTokens: 20}}}
			ep.Outcome.Claimed = "done"
			ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: pass, Score: map[bool]float64{true: 1}[pass]}
			put(filepath.Join(tid, fmt.Sprint(s), "episode.json"), ep)
		}
	}
	put("manifest.json", m)
	return dir
}

func TestRLReportPrintsTheMetricsInEveryFormat(t *testing.T) {
	dir := fakeRun(t, "run-a", "vendor/model-a", 8, 3, func(task, sample int) bool { return task < 6 })
	r := rlRun{t}

	out := r.must(rlReport, dir, "--by-tag", "--tasks")
	for _, want := range []string{"RUN", "run-a", "vendor/model-a", "single", "24/24", "75%", "SOLVED", "75%", "$/EP", "0.0100", "HIT", "75%", "even", "odd", "task-00", "3/3", "task-07", "0/3", "PASS:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table lacks %q:\n%s", want, out)
		}
	}

	md := r.must(rlReport, dir, "--format", "md")
	if !strings.HasPrefix(md, "| RUN | MODEL |") || !strings.Contains(md, "| --- |") || !strings.Contains(md, "| run-a | vendor/model-a | single | 24/24 | 75% |") {
		t.Errorf("markdown:\n%s", md)
	}

	// JSON is what a baseline is made of: writing it to a file and reading it back gives the same table.
	saved := filepath.Join(t.TempDir(), "baseline.json")
	r.must(rlReport, dir, "--format", "json", "-o", saved)
	var rep env.Report
	b, err := os.ReadFile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rep); err != nil || rep.Completed != 24 || rep.Passed != 18 || rep.Identity == nil || rep.Identity.Model != "vendor/model-a" {
		t.Fatalf("saved report: %v %+v", err, rep)
	}
	if again := r.must(rlReport, saved); !strings.Contains(again, "24/24") || !strings.Contains(again, "75%") {
		t.Errorf("the saved report does not read back:\n%s", again)
	}
}

func TestRLReportRefusesWhatItCannotRead(t *testing.T) {
	r := rlRun{t}
	if _, _, err := r.do(rlReport); err == nil {
		t.Error("no argument must be an error")
	}
	if _, _, err := r.do(rlReport, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a run directory") {
		t.Errorf("an empty directory: %v", err)
	}
	notReport := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(notReport, []byte(`{"schema":"something.else"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.do(rlReport, notReport); err == nil || !strings.Contains(err.Error(), "not a report") {
		t.Errorf("a foreign json file: %v", err)
	}
	if _, _, err := r.do(rlReport, "--format", "xml", t.TempDir()); err == nil {
		t.Error("an unknown format must be an error")
	}
}

func TestRLCompareGatesARegressionAndPassesARunAgainstItself(t *testing.T) {
	good := fakeRun(t, "good", "m", 20, 3, func(int, int) bool { return true })
	again := fakeRun(t, "again", "m", 20, 3, func(int, int) bool { return true })
	bad := fakeRun(t, "bad", "m", 20, 3, func(task, sample int) bool { return task%4 != 0 }) // every fourth task broken
	r := rlRun{t}

	out, errb, err := r.do(rlCompare, good, again, "--gate", "pass_at_1:0.05,mean_usd:10%")
	if err != nil {
		t.Fatalf("a run against an identical one must pass its gates: %v\n%s\n%s", err, out, errb)
	}
	for _, want := range []string{"20 tasks ran in both", "METRIC", "pass_at_1", "VERDICT", "gates:", "ok    pass_at_1:0.05"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	out, _, err = r.do(rlCompare, good, bad, "--gate", "pass_at_1:0.05", "--gate", "mean_usd:10%")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 gates failed") {
		t.Fatalf("a quarter of the tasks broken must fail the quality gate and only that one: %v\n%s", err, out)
	}
	for _, want := range []string{"worse", "FAIL  pass_at_1:0.05", "ok    mean_usd:10%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// Without gates it only reports, and json is parseable.
	out, _, err = r.do(rlCompare, good, bad, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Comparison env.Comparison `json:"comparison"`
		Gates      []any          `json:"gates"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Comparison.Paired != 20 {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if m, ok := got.Comparison.Metric("pass_at_1"); !ok || m.Delta >= 0 || !m.Significant {
		t.Errorf("pass_at_1 %+v", m)
	}

	if _, _, err := r.do(rlCompare, good); err == nil {
		t.Error("one run is not a comparison")
	}
	if _, _, err := r.do(rlCompare, good, bad, "--gate", "pass_at_1:oops"); err == nil {
		t.Error("a malformed gate must be refused before anything runs")
	}
}
