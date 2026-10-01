package env

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// runDir writes what `rl rollout` leaves behind, by hand: a manifest, the episodes it names (nil = not run yet, an
// episode with FlagInfraError = an infrastructure stub) and a ledger.
type runSpec struct {
	model    string
	group    int
	tasks    []ManifestTask
	episodes map[string]*rl.Episode // "<task>/<sample>"
	ledger   []LedgerEntry
}

func writeRun(t *testing.T, s runSpec) string {
	t.Helper()
	dir := t.TempDir()
	m := Manifest{Schema: ManifestSchema, RunID: "run-x", Updated: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), GroupSize: s.group,
		Config: ManifestConfig{Seed: 7, Concurrency: 3}, Policy: ManifestPolicy{Model: s.model, Endpoint: "https://ep.example"}, Tasks: s.tasks}
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
	put("manifest.json", m)
	for key, ep := range s.episodes {
		task, sample, _ := strings.Cut(key, "/")
		if ep != nil {
			ep.TaskID, ep.Sample = task, atoi(sample)
			put(filepath.Join(task, sample, "episode.json"), ep)
		}
	}
	if len(s.ledger) > 0 {
		var b strings.Builder
		for _, e := range s.ledger {
			l, _ := json.Marshal(e)
			b.Write(l)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, LedgerFile), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func episode(pass bool, claimed string, usage core.Usage, usd float64, sig map[string]float64, flags ...string) *rl.Episode {
	ep := &rl.Episode{Schema: rl.SchemaEpisode, Flags: flags, Signals: sig,
		Harness: rl.HarnessRef{Version: "v1", Commit: "abc", Renderer: "sleipnir-kv/2"},
		Cost:    rl.CostRef{Usage: usage, USD: usd, Requests: 5, WallMs: 4000}}
	ep.Outcome.Claimed = claimed
	ep.Outcome.Verifier = &rl.Verdict{Kind: "verifier", Pass: pass, Score: map[bool]float64{true: 1}[pass]}
	return ep
}

func mtask(id, verifier string, tags ...string) ManifestTask {
	return ManifestTask{ID: id, Kind: "fix", Repo: "repos/x", Tags: tags, Team: rl.Team{Mode: "single"}, VerifierVersion: verifier}
}

// Episodes written before the harness stopped saying "done" for a run the clock cut off carry the claim and the budget flag. The
// flag is what the runner saw and the claim was a default, so such an episode is not a false claim.
func TestARunTheClockCutOffIsNotAFalseDoneEvenWhenTheEpisodeSaysDone(t *testing.T) {
	dir := writeRun(t, runSpec{
		model: "m/x", group: 3,
		tasks: []ManifestTask{mtask("a", "v1")},
		episodes: map[string]*rl.Episode{
			"a/0": episode(false, "done", core.Usage{}, 0, nil),                        // said it was done and was wrong
			"a/1": episode(false, "done", core.Usage{}, 0, nil, rl.FlagBudgetExceeded), // the clock ended it; "done" is the old default
			"a/2": episode(false, "budget", core.Usage{}, 0, nil, rl.FlagBudgetExceeded),
		},
	})
	rep, err := LoadRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !near(rep.FalseDone, 1.0/3) {
		t.Errorf("false done %v, want 1/3: only the first episode said it was done and was wrong", rep.FalseDone)
	}
	if !near(rep.BudgetRate, 2.0/3) {
		t.Errorf("budget rate %v, want 2/3", rep.BudgetRate)
	}
}

func TestLoadRunRecomputesTheReportFromTheEpisodes(t *testing.T) {
	use := func(in, read, write, out int) core.Usage {
		return core.Usage{InputTokens: in, CacheReadTokens: read, CacheWrite5mTokens: write, OutputTokens: out, ReasoningTokens: out / 2}
	}
	dir := writeRun(t, runSpec{
		model: "m/x", group: 2,
		tasks: []ManifestTask{mtask("a", "v1", "go"), mtask("b", "v1", "go", "hard"), mtask("c", "v1")},
		episodes: map[string]*rl.Episode{
			"a/0": episode(true, "done", use(100, 300, 0, 10), 0.01, map[string]float64{rl.SigToolErrors: 1, "request_retries": 2}),
			"a/1": episode(true, "done", use(100, 300, 0, 10), 0.01, map[string]float64{rl.SigInvalidToolCalls: 3}),
			"b/0": episode(true, "done", use(200, 0, 100, 20), 0.03, map[string]float64{rl.SigCacheAnomalies: 1, "request_errors": 1}),
			"b/1": episode(false, "done", use(100, 100, 0, 10), 0.02, nil, rl.FlagHackEscape), // claimed done, failed, flagged
			"c/0": episode(false, "", core.Usage{}, 0, nil, rl.FlagInfraError),                // an infrastructure stub
			// c/1 has not run
		},
	})
	rep, err := LoadRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Completed != 4 || rep.Infra != 1 || rep.Pending != 1 || rep.Passed != 3 {
		t.Fatalf("completed %d infra %d pending %d passed %d, want 4 1 1 3", rep.Completed, rep.Infra, rep.Pending, rep.Passed)
	}
	if !near(rep.PassAt1, 0.75) || !near(rep.Solved, 1) { // tasks a (1.0) and b (0.5); c has no answer and is left out
		t.Errorf("pass@1 %v solved %v, want 0.75 and 1", rep.PassAt1, rep.Solved)
	}
	if rep.PassLow >= 0.75 || rep.PassHigh <= 0.75 || rep.PassLow <= 0.2 || rep.PassHigh >= 1 {
		t.Errorf("the 95%% interval of 3/4 is wide but not everything: [%v, %v]", rep.PassLow, rep.PassHigh)
	}
	want := Tokens{Input: 500, CacheRead: 700, CacheWrite: 100, Output: 50, Reasoning: 25}
	if rep.Tokens != want {
		t.Errorf("tokens %+v, want %+v", rep.Tokens, want)
	}
	if !near(rep.HitRatio, 700.0/1300) {
		t.Errorf("hit ratio %v, want 700/1300 (tokens, not requests)", rep.HitRatio)
	}
	if !near(rep.USDTotal, 0.07) || !near(rep.USDPerPass, 0.07/3) {
		t.Errorf("usd %v per pass %v", rep.USDTotal, rep.USDPerPass)
	}
	if !near(rep.ToolErrors, 0.25) || !near(rep.InvalidCalls, 0.75) || !near(rep.Retries, 0.5) || !near(rep.RequestErrors, 0.25) || !near(rep.CacheAnomalies, 0.25) {
		t.Errorf("friction per episode: tool %v invalid %v retries %v reqerr %v anomalies %v", rep.ToolErrors, rep.InvalidCalls, rep.Retries, rep.RequestErrors, rep.CacheAnomalies)
	}
	if !near(rep.FalseDone, 0.25) || !near(rep.HackRate, 0.25) {
		t.Errorf("false done %v hack %v, want 0.25 each", rep.FalseDone, rep.HackRate)
	}
	if id := rep.Identity; id == nil || id.Model != "m/x" || id.Group != 2 || id.Seed != 7 || id.Mode != "single" || id.Renderer != "sleipnir-kv/2" || id.Tasks != 3 {
		t.Errorf("identity %+v", id)
	}
	if rep.Model != "m/x" || rep.RunID != "run-x" || !rep.Created.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("model %q run %q created %v: a loaded report must not stamp the time it was loaded", rep.Model, rep.RunID, rep.Created)
	}

	// Rescoring rewrites episode.json; the next report is what is on disk now, not what the rollout once summarised.
	path := filepath.Join(dir, "b", "1", "episode.json")
	raw, _ := os.ReadFile(path)
	var ep rl.Episode
	if err := json.Unmarshal(raw, &ep); err != nil {
		t.Fatal(err)
	}
	ep.Flags = nil
	raw, _ = json.Marshal(ep)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if again, err := LoadRun(dir); err != nil || !near(again.HackRate, 0) {
		t.Errorf("after the flag was removed: hack rate %v, err %v", again.HackRate, err)
	}
}

func TestLoadRunReadsTheLedgerAndTellsSuitesApart(t *testing.T) {
	spec := runSpec{
		model: "m/x", group: 1, tasks: []ManifestTask{mtask("a", "v1"), mtask("b", "v1")},
		episodes: map[string]*rl.Episode{"a/0": episode(true, "done", core.Usage{}, 0.01, nil), "b/0": episode(false, "", core.Usage{}, 0.02, nil)},
		ledger: []LedgerEntry{
			{Task: "a", Attempt: 1, Status: StatusInfra, CostUSD: 0.004, Requests: 3, Retries: 2},
			{Task: "a", Attempt: 2, Status: StatusOK, CostUSD: 0.01, Requests: 5},
			{Task: "b", Attempt: 1, Status: StatusOK, CostUSD: 0.02, Requests: 8, Retries: 1},
		},
	}
	rep, err := LoadRun(writeRun(t, spec))
	if err != nil {
		t.Fatal(err)
	}
	a := rep.Attempts
	if a == nil || a.Attempts != 3 || a.Wasted != 1 || a.Requests != 16 || a.WastedRequests != 3 || a.Retries != 3 || !near(a.SpentUSD, 0.034) || !near(a.WastedUSD, 0.004) {
		t.Fatalf("attempts %+v", a)
	}

	digest := func(tasks ...ManifestTask) string {
		s := spec
		s.tasks = tasks
		rep, err := LoadRun(writeRun(t, s))
		if err != nil {
			t.Fatal(err)
		}
		return rep.Identity.TasksDigest
	}
	same := digest(mtask("a", "v1"), mtask("b", "v1"))
	if same != rep.Identity.TasksDigest || digest(mtask("b", "v1"), mtask("a", "v1")) != same {
		t.Error("the digest of a suite does not depend on the order of its tasks")
	}
	if digest(mtask("a", "v1"), mtask("b", "v2")) == same {
		t.Error("a task whose verifier changed is not the same suite")
	}
	if digest(mtask("a", "v1")) == same {
		t.Error("a smaller suite is not the same suite")
	}
}

func TestLoadRunRefusesWhatIsNotARunDirectory(t *testing.T) {
	if _, err := LoadRun(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a run directory") {
		t.Errorf("error %v", err)
	}
}
