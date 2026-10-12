package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

// Verifying a finished rollout again (Reverify). A rollout's verdict and its
// progress curve are what its task's verifier measured while it ran. When that
// verifier is fixed afterwards (a suite scorer that learned to score the rest
// of a package that one crashing test took down), the recorded scores keep the
// old verifier's mistakes, and so do the rewards and the best-savepoint cuts
// built from them. Reverify measures a finished sample again with the task as a
// tasks file defines it now: the final patch, with the full verifier as the
// runner's verification ran it, and the work tree of every consistent
// savepoint, in progress mode as the runner scored them. Nothing the agents did
// changes: only the verdict, Episode.Progress and progress.jsonl are replaced.
// What they were is kept in reverify.jsonl, and the new verifier's log and the
// final patch it checked go to <sample>/reverify/. The rewards are recomputed
// by rl reward, which reads the new verdict and curve.

// reverifyLog records each Reverify of a sample: what it replaced.
const reverifyLog = "reverify.jsonl"

// reverifyDir receives the verifier log and the patch of a Reverify's final
// verification (the runner's own stay where they are).
const reverifyDir = "reverify"

// ReverifyOptions configures Reverify.
type ReverifyOptions struct {
	// Workspaces builds the clean checkouts; HiddenBlobs resolves the task's
	// hidden files (as for the runner).
	Workspaces  *Workspaces
	HiddenBlobs events.Blobs
	// Final verifies the final patch again; Savepoints scores the tree of each
	// consistent savepoint again. At least one is required.
	Final, Savepoints bool
	// Repeats and PassPolicy are the final verification's (VerifyOptions).
	Repeats    int
	PassPolicy string
	// ProgressSample and ProgressBudget are the progress mode of a savepoint's
	// score, as Runner.ProgressSample and Runner.ProgressBudget (defaults for
	// zero).
	ProgressSample int
	ProgressBudget time.Duration
	// DryRun measures without writing anything into the sample directory.
	DryRun bool
	// Now stamps the reverify.jsonl entry (time.Now when nil).
	Now func() time.Time
}

// ReverifyResult is what Reverify measured and what it replaced. Before and
// After are set when the final patch was verified; ProgressBefore and
// ProgressAfter when the savepoints were scored.
type ReverifyResult struct {
	Version        string             `json:"version"`
	Before         *rl.Verdict        `json:"before,omitempty"`
	After          *rl.Verdict        `json:"after,omitempty"`
	ProgressBefore []rl.ProgressPoint `json:"progress_before,omitempty"`
	ProgressAfter  []rl.ProgressPoint `json:"progress_after,omitempty"`
}

// reverifyEntry is one line of reverify.jsonl.
type reverifyEntry struct {
	At time.Time `json:"at"`
	ReverifyResult
}

// Reverify verifies the finished rollout in the sample directory dir again
// with task (see the comment above) and updates ep, the sample's episode, to
// what it measured: Outcome.Verifier with Final, Progress with Savepoints.
// Unless DryRun, it writes ep to episode.json, the new points to progress.jsonl
// and appends an entry to reverify.jsonl. task must be the task the sample ran
// (the same id) with the same start: a checkout of it must have the tree the
// sample started from. A savepoint whose tree is gone from the sample's store,
// or a verification that cannot run, is an error and nothing is written.
func Reverify(ctx context.Context, dir string, ep *rl.Episode, task rl.Task, o ReverifyOptions) (ReverifyResult, error) {
	var res ReverifyResult
	switch {
	case ep == nil:
		return res, errors.New("reverify: no episode")
	case ep.TaskID != task.ID:
		return res, fmt.Errorf("reverify: the sample ran task %q, not %q", ep.TaskID, task.ID)
	case !o.Final && !o.Savepoints:
		return res, errors.New("reverify: nothing to verify (neither the final patch nor the savepoints)")
	case o.Workspaces == nil:
		return res, errors.New("reverify: no workspaces")
	case !needsCommand(task):
		return res, fmt.Errorf("reverify: task %s has no verifier command: an answer check has nothing to measure again", task.ID)
	}
	if err := ValidateTask(task); err != nil {
		return res, fmt.Errorf("reverify: %w", err)
	}
	m := o.Workspaces
	snap, err := m.snapshotFor(ctx, task)
	if err != nil {
		return res, fmt.Errorf("reverify: %w", err)
	}
	base := snap.meta.BaseTree
	if ep.Env.TreeHash != "" && ep.Env.TreeHash != base {
		return res, fmt.Errorf("reverify: task %s starts from tree %s, the sample from %s: not the start it ran", task.ID, base, ep.Env.TreeHash)
	}
	res.Version = VerifierVersion(task)

	var recs []savepointRecord
	if o.Savepoints {
		if recs, err = readSavepoints(dir); err != nil {
			return res, fmt.Errorf("reverify: %w", err)
		}
		for _, rec := range recs {
			if rec.Workspace.BaseTree != base {
				return res, fmt.Errorf("reverify: savepoint %d was taken over tree %s, task %s starts from %s", rec.N, rec.Workspace.BaseTree, task.ID, base)
			}
		}
	}

	var after *rl.Verdict
	if o.Final {
		patch, err := finalPatch(dir, ep)
		if err != nil {
			return res, fmt.Errorf("reverify: %w", err)
		}
		vo := VerifyOptions{Workspaces: m, HiddenBlobs: o.HiddenBlobs, Repeats: o.Repeats, PassPolicy: o.PassPolicy}
		if !o.DryRun {
			store, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
			if err != nil {
				return res, fmt.Errorf("reverify: %w", err)
			}
			vo.Store, vo.OutDir = store, filepath.Join(dir, reverifyDir)
		}
		vr, err := VerifyPatch(ctx, task, patch, vo)
		if err != nil {
			return res, fmt.Errorf("reverify: the final patch: %w", err)
		}
		v := vr.Verdict()
		after = &v
	}

	var points []rl.ProgressPoint
	progressEnv, progressTimeout := progressMode(o.ProgressSample, o.ProgressBudget)
	for _, rec := range recs {
		patch, err := m.diffTrees(ctx, savepointStoreDir(dir), base, rec.Tree, maxRestoreBytes)
		if err != nil {
			return res, fmt.Errorf("reverify: savepoint %d: %w", rec.N, err)
		}
		vr, err := VerifyPatch(ctx, task, patch, VerifyOptions{Workspaces: m, HiddenBlobs: o.HiddenBlobs, Env: progressEnv, Timeout: progressTimeout})
		if err != nil {
			return res, fmt.Errorf("reverify: savepoint %d: %w", rec.N, err)
		}
		points = append(points, rl.ProgressPoint{Savepoint: rec.N, Seq: rec.Seq, ActiveWallMs: rec.ActiveWallMs, At: rec.At, Score: vr.Score,
			Pass: vr.Pass, TimedOut: vr.TimedOut, Ms: vr.Ms})
	}

	if after != nil {
		if ep.Outcome.Verifier != nil {
			was := ep.Outcome.Verifier.Clone()
			res.Before = &was
		}
		res.After = after
		ep.Outcome.Verifier = after
	}
	if o.Savepoints {
		res.ProgressBefore, res.ProgressAfter = ep.Progress, points
		ep.Progress = points
	}
	if o.DryRun {
		return res, nil
	}
	if o.Savepoints {
		var b []byte
		for _, p := range points {
			line, err := json.Marshal(p)
			if err != nil {
				return res, fmt.Errorf("reverify: progress point: %w", err)
			}
			b = append(append(b, line...), '\n')
		}
		if err := atomicWriteFile(filepath.Join(dir, progressFile), b, 0o600); err != nil {
			return res, fmt.Errorf("reverify: %w", err)
		}
	}
	if err := writeJSONAtomic(filepath.Join(dir, "episode.json"), ep); err != nil {
		return res, fmt.Errorf("reverify: %w", err)
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if err := appendJSONLine(filepath.Join(dir, reverifyLog), reverifyEntry{At: now().UTC(), ReverifyResult: res}); err != nil {
		return res, fmt.Errorf("reverify: %w", err)
	}
	return res, nil
}

// finalPatch is the patch the sample's verification checked: the episode's
// Outcome.Diff from the sample's blob store, else the diff.patch the
// verification wrote next to it. An episode without either made no change.
func finalPatch(dir string, ep *rl.Episode) ([]byte, error) {
	if ep.Outcome.Diff != "" {
		store, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
		if err != nil {
			return nil, err
		}
		b, err := store.Get(core.Hash(ep.Outcome.Diff))
		if err != nil {
			return nil, fmt.Errorf("the final patch (blob %s): %w", ep.Outcome.Diff, err)
		}
		return b, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "diff.patch"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}
