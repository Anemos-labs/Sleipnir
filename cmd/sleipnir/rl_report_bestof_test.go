package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

func TestRLReportNamesTheBestOfEachGroupAndTheEfficiencyMeans(t *testing.T) {
	// One task, four samples: 0 passes cache-hostile, 1 passes cache-friendly, 2 passes as cheaply but
	// re-reads, 3 loops and fails.
	dir := fakeRun(t, "run-b", "vendor/model-b", 1, 4, func(task, sample int) bool { return sample < 3 })
	edits := map[int]func(*rl.Episode){
		0: func(ep *rl.Episode) {
			ep.Cost.ITE = 90_000
			ep.Signals = map[string]float64{rl.SigToolCalls: 6, rl.SigFinalAnswerChars: 40}
		},
		1: func(ep *rl.Episode) {
			ep.Cost.ITE = 40_000
			ep.Signals = map[string]float64{rl.SigToolCalls: 6, rl.SigFinalAnswerChars: 40}
		},
		2: func(ep *rl.Episode) {
			ep.Cost.ITE = 40_000
			ep.Signals = map[string]float64{rl.SigToolCalls: 8, rl.SigRepeatedReads: 2, rl.SigFinalAnswerChars: 40}
		},
		3: func(ep *rl.Episode) {
			ep.Cost.ITE = 20_000
			ep.Signals = map[string]float64{rl.SigToolCalls: 16, rl.SigStuckWarnings: 1, rl.SigStuckStops: 1, rl.SigToolErrors: 8}
			ep.Flags = []string{rl.FlagLooped}
		},
	}
	for s, edit := range edits {
		p := filepath.Join(dir, "task-00", string(rune('0'+s)), "episode.json")
		var ep rl.Episode
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &ep); err != nil {
			t.Fatal(err)
		}
		edit(&ep)
		if b, err = json.Marshal(ep); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := rlRun{t}
	out := r.must(rlReport, dir, "--tasks")
	for _, want := range []string{"best of group", "task-00", "BEST", "40000", "per episode: 9.0 tool calls", "0.25 guard stops (25% looped)", "0.50 repeated reads"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	var rep env.Report
	if err := json.Unmarshal([]byte(r.must(rlReport, dir, "--format", "json")), &rep); err != nil {
		t.Fatal(err)
	}
	best := rep.PerTask[0].Best
	if best == nil || best.Sample != 1 || !best.Key.Verified || best.Key.ITE != 40_000 {
		t.Fatalf("best of group = %+v, want the cache-friendly sample 1", best)
	}
	e := rep.Efficiency
	if e == nil {
		t.Fatal("the report has no efficiency")
	}
	if e.ToolCalls != 9 || e.StuckStops != 0.25 || e.LoopRate != 0.25 || e.RepeatedReads != 0.5 || e.Waste != 3 {
		t.Fatalf("efficiency = %+v", e)
	}
}

// A report saved before the efficiency signals existed has none: rl report must not print its missing means as measured zeros,
// while a report that measured zeros still prints them.
func TestRLReportPrintsEfficiencyOnlyWhereItWasMeasured(t *testing.T) {
	write := func(name, body string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const head = `{"schema":"sleipnir.rl.eval/2","run_id":"saved","tasks":1,"samples":2,"completed":2,"passed":1`
	r := rlRun{t}
	if out := r.must(rlReport, write("old.json", head+`}`)); strings.Contains(out, "per episode:") {
		t.Errorf("a report without efficiency prints measured zeros:\n%s", out)
	}
	out := r.must(rlReport, write("measured.json", head+`,"efficiency":{"tool_calls":0,"waste":0}}`))
	if !strings.Contains(out, "per episode: 0.0 tool calls") {
		t.Errorf("a measured zero is not printed:\n%s", out)
	}
}

// A swarm run's report adds how its board closed tasks to the notes, and the JSON report carries the same counts; a
// run without a board has neither.
func TestRLReportShowsHowTheBoardClosedItsTasks(t *testing.T) {
	dir := fakeRun(t, "run-c", "vendor/model-c", 1, 2, func(task, sample int) bool { return true })
	closures := []map[string]int{{"done:verified": 2, "handed_off": 1}, {"done:verified": 1, "failed:superseded": 1}}
	for s, c := range closures {
		p := filepath.Join(dir, "task-00", string(rune('0'+s)), "episode.json")
		var ep rl.Episode
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &ep); err != nil {
			t.Fatal(err)
		}
		ep.Outcome.Closures = c
		if b, err = json.Marshal(ep); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := rlRun{t}
	out := r.must(rlReport, dir)
	if want := "board closures: done:verified 3, failed:superseded 1, handed_off 1"; !strings.Contains(out, want) {
		t.Errorf("the report lacks %q:\n%s", want, out)
	}
	var rep env.Report
	if err := json.Unmarshal([]byte(r.must(rlReport, dir, "--format", "json")), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Closures["done:verified"] != 3 || rep.Closures["failed:superseded"] != 1 || rep.Closures["handed_off"] != 1 {
		t.Errorf("report closures = %v", rep.Closures)
	}

	plain := fakeRun(t, "run-d", "vendor/model-d", 1, 2, func(task, sample int) bool { return true })
	if out := r.must(rlReport, plain); strings.Contains(out, "board closures") {
		t.Errorf("a run without a board lists closures:\n%s", out)
	}
}
