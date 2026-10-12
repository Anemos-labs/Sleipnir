package env

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// A rollout that a wrong verifier scored is verified again with the fixed task:
// the final patch, and every consistent savepoint in progress mode, replace the
// verdict, the curve and progress.jsonl, and what they were goes to
// reverify.jsonl. A dry run measures the same and writes nothing; a task the
// sample did not run, or one with another start, is refused.
func TestReverifyReplacesTheVerdictAndTheCurve(t *testing.T) {
	// final scores the fixed tree at the end, progress in progress mode; the start scores 0.
	scorer := func(final, progress string) string {
		return `if ! tr -d ' \t\n' < mathx.go | grep -q 'ifa>b{returna}'; then echo '{"score": 0}'; ` +
			`elif [ -n "$SUITE_SAMPLE" ]; then echo '{"score": ` + progress + `}'; else echo '{"score": ` + final + `}'; fi`
	}
	f := newRunnerFixture(t, func(f *runnerFixture) {
		f.r.InfraRetries = -1
		f.r.SavepointEvery = 20 * time.Millisecond
		f.r.ScoreSavepoints = true
		f.r.Concurrency = 1
		f.task.Verifier = rl.Verifier{Pass: "json-score", TimeoutS: 60, Cmd: scorer("0.5", "0.25")}
	})
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		log, err := events.Open(spec.RunDir, "fake")
		if err != nil {
			return RunResult{}, err
		}
		defer log.Close()
		mustWrite(t, filepath.Join(spec.Workspace, "mathx.go"), fixedMath)
		n := 0
		spec.Savepoints.Bind(func(ctx context.Context, capture func(context.Context) error) (SavepointInfo, error) {
			size, err := log.FlushedSize()
			if err != nil {
				return SavepointInfo{}, err
			}
			n++
			return SavepointInfo{N: n, Consistent: true, LogBytes: size}, capture(ctx)
		})
		waitFor(t, "three savepoints, one scored", func() bool { return indexLen(spec.RunDir) >= 3 && len(readProgress(spec.RunDir)) > 0 })
		spec.Savepoints.Bind(nil)
		return RunResult{Claimed: "done"}, nil
	}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if r := sum.Results[0]; r.Status != StatusOK || r.Pass || r.Score != 0.5 {
		t.Fatalf("result %+v: want the broken verifier's 0.5", r)
	}
	dir := f.sampleDir(f.task.ID, 0)
	recs, err := readSavepoints(dir)
	if err != nil || len(recs) < 3 {
		t.Fatalf("savepoints %d: %v", len(recs), err)
	}
	fixed := f.task
	fixed.Verifier.Cmd = scorer("1", "0.75")
	o := ReverifyOptions{Workspaces: f.m, Final: true, Savepoints: true, Now: func() time.Time { return time.Unix(1, 0) }}
	ctx := context.Background()

	episodeFile := filepath.Join(dir, "episode.json")
	was, err := os.ReadFile(episodeFile)
	if err != nil {
		t.Fatal(err)
	}
	dry := o
	dry.DryRun = true
	res, err := Reverify(ctx, dir, f.episode(f.task.ID, 0), fixed, dry)
	if err != nil || res.After == nil || res.After.Score != 1 || len(res.ProgressAfter) != len(recs) {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if now, _ := os.ReadFile(episodeFile); !bytes.Equal(now, was) {
		t.Error("a dry run rewrote episode.json")
	}
	for _, p := range []string{reverifyLog, reverifyDir} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("a dry run wrote %s", p)
		}
	}

	// A savepoint that cannot be scored fails the sample before anything is written into it.
	index := filepath.Join(dir, savepointDir, savepointIndex)
	good, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	bad := recs[len(recs)-1]
	bad.N, bad.Tree = bad.N+1, strings.Repeat("e", len(bad.Tree))
	if err := appendSavepoint(dir, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := Reverify(ctx, dir, f.episode(f.task.ID, 0), fixed, o); err == nil || !strings.Contains(err.Error(), "savepoint") {
		t.Fatalf("a savepoint whose tree is gone: %v", err)
	}
	if now, _ := os.ReadFile(episodeFile); !bytes.Equal(now, was) {
		t.Error("a failed reverify rewrote episode.json")
	}
	for _, p := range []string{reverifyLog, reverifyDir} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("a failed reverify wrote %s", p)
		}
	}
	if err := os.WriteFile(index, good, 0o600); err != nil {
		t.Fatal(err)
	}

	ep := f.episode(f.task.ID, 0)
	res, err = Reverify(ctx, dir, ep, fixed, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Before == nil || res.Before.Score != 0.5 || res.After == nil || res.After.Score != 1 || !res.After.Pass {
		t.Errorf("final verdict %+v -> %+v: want 0.5 -> 1, passing", res.Before, res.After)
	}
	if res.Version != VerifierVersion(fixed) || res.After.Version != res.Version {
		t.Errorf("version %q, verdict %q: want the fixed task's %q", res.Version, res.After.Version, VerifierVersion(fixed))
	}
	if len(res.ProgressBefore) == 0 || res.ProgressBefore[0].Score != 0.25 {
		t.Errorf("progress before %+v: want the broken verifier's points", res.ProgressBefore)
	}
	got := f.episode(f.task.ID, 0)
	if v := got.Outcome.Verifier; v == nil || v.Score != 1 || !v.Pass {
		t.Errorf("episode.json verdict %+v: want the new one", v)
	}
	for _, pts := range [][]rl.ProgressPoint{got.Progress, readProgress(dir)} {
		if len(pts) != len(recs) {
			t.Fatalf("progress %+v: want a point for each of the %d savepoints", pts, len(recs))
		}
		for i, p := range pts {
			if p.Savepoint != recs[i].N || p.Score != 0.75 || p.At.IsZero() || p.ActiveWallMs != recs[i].ActiveWallMs {
				t.Errorf("point %d %+v: want savepoint %d scored 0.75 in progress mode", i, p, recs[i].N)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, reverifyLog)); err != nil || strings.Count(string(b), "\n") != 1 || !strings.Contains(string(b), `"score":0.5`) {
		t.Errorf("reverify.jsonl %q %v: want one entry keeping the old verdict", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, reverifyDir, "verifier.log")); err != nil {
		t.Errorf("the new verifier log: %v", err)
	}

	other := fixed
	other.ID = "another-task"
	if _, err := Reverify(ctx, dir, f.episode(f.task.ID, 0), other, o); err == nil {
		t.Error("a task the sample did not run was accepted")
	}
	moved := f.episode(f.task.ID, 0)
	moved.Env.TreeHash = strings.Repeat("0", len(moved.Env.TreeHash))
	if _, err := Reverify(ctx, dir, moved, fixed, o); err == nil || !strings.Contains(err.Error(), "not the start it ran") {
		t.Errorf("a sample from another start: %v", err)
	}
}

// A rollout run without savepoints has no savepoint index: verifying it again
// with the default options measures the final patch and keeps its progress.
func TestReverifyWithoutSavepoints(t *testing.T) {
	scorer := func(final string) string {
		return `if tr -d ' \t\n' < mathx.go | grep -q 'ifa>b{returna}'; then echo '{"score": ` + final + `}'; else echo '{"score": 0}'; fi`
	}
	f := newRunnerFixture(t, func(f *runnerFixture) {
		f.r.Concurrency = 1
		f.task.Verifier = rl.Verifier{Pass: "json-score", TimeoutS: 60, Cmd: scorer("0.5")}
	})
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		mustWrite(t, filepath.Join(spec.Workspace, "mathx.go"), fixedMath)
		return RunResult{Claimed: "done"}, nil
	}
	if r := f.rollout([]rl.Task{f.task}, 1, f.opts()).Results[0]; r.Status != StatusOK || r.Score != 0.5 {
		t.Fatalf("result %+v", r)
	}
	fixed := f.task
	fixed.Verifier.Cmd = scorer("1")
	ep := f.episode(f.task.ID, 0)
	res, err := Reverify(context.Background(), f.sampleDir(f.task.ID, 0), ep, fixed, ReverifyOptions{Workspaces: f.m, Final: true, Savepoints: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NoSavepoints || res.After == nil || res.After.Score != 1 || res.ProgressAfter != nil || ep.Progress != nil {
		t.Errorf("result %+v, progress %+v: want the final verdict alone, the curve untouched", res, ep.Progress)
	}
}
