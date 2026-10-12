package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// init registers the reverification command.
func init() { rlCommands["reverify"] = rlReverify }

// rlReverify verifies finished rollouts again with the tasks as a tasks file defines them now (env.Reverify): the final
// patch and the savepoints' work trees, replacing the verdicts and progress curves a since-fixed verifier recorded.
func rlReverify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl reverify", stderr, "rl reverify --tasks FILE [flags] RUN_DIR|SAMPLE_DIR...")
	var rf rigFlags
	rf.register(fs)
	tasksFile := fs.String("tasks", "", "tasks file (JSON lines) with the rollouts' tasks as they are to be verified now (see rl tasks update-scorer)")
	final := fs.Bool("final", true, "verify the final patch again (the episode's verdict)")
	savepoints := fs.Bool("savepoints", true, "score the work tree of every consistent savepoint again, in progress mode (progress.jsonl, the episode's progress)")
	dry := fs.Bool("dry-run", false, "print what would change without writing anything into the run")
	asJSON := fs.Bool("json", false, "print each sample's result as a JSON line")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	switch {
	case *tasksFile == "":
		return usageError(fs, "rl reverify: --tasks is required")
	case len(paths) == 0:
		return usageError(fs, "rl reverify: pass one or more run or sample directories")
	case !*final && !*savepoints:
		return usageError(fs, "rl reverify: nothing to do with --final=false and --savepoints=false")
	}
	if err := rf.validate(); err != nil {
		return fmt.Errorf("rl reverify: %w", err)
	}
	tasks, err := env.LoadTasks(*tasksFile)
	if err != nil {
		return fmt.Errorf("rl reverify: %w", err)
	}
	byID := map[string]rl.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	samples, err := findSamples(paths)
	if err != nil {
		return fmt.Errorf("rl reverify: %w", err)
	}
	blobsDir := orStr(rf.blobs, filepath.Join(filepath.Dir(*tasksFile), "blobs"))
	hidden, err := events.NewDirBlobs(blobsDir)
	if err != nil {
		return fmt.Errorf("rl reverify: %w", err)
	}
	// Confined, the verifier sees neither the blob store nor the runs it measures.
	ws, err := rf.workspaces(stderr, append([]string{blobsDir}, paths...)...)
	if err != nil {
		return fmt.Errorf("rl reverify: %w", err)
	}
	defer ws.Close()
	o := env.ReverifyOptions{Workspaces: ws, HiddenBlobs: hidden, Final: *final, Savepoints: *savepoints, Repeats: rf.verifyRepeats,
		PassPolicy: rf.verifyPolicy, ProgressSample: rf.progressSample, ProgressBudget: rf.progressBudget, DryRun: *dry}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	if !*asJSON {
		fmt.Fprintln(tw, "EPISODE\tSCORE BEFORE\tSCORE AFTER\tPASS\tSAVEPOINTS\tBEST BEFORE\tBEST AFTER")
	}
	enc := json.NewEncoder(stdout)
	var done, failed int
	for _, s := range samples {
		ep := s.Episode
		if ep.Has(rl.FlagInfraError) {
			continue
		}
		task, ok := byID[ep.TaskID]
		if !ok {
			fmt.Fprintf(stderr, "rl reverify: %s: %s has no task %q\n", s.Dir, *tasksFile, ep.TaskID)
			failed++
			continue
		}
		res, err := env.Reverify(ctx, s.Dir, ep, task, o)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(stderr, "rl reverify: %s: %v\n", s.Dir, err)
			failed++
			continue
		}
		done++
		if *asJSON {
			_ = enc.Encode(struct {
				Episode string `json:"episode"`
				Dir     string `json:"dir"`
				env.ReverifyResult
			}{ep.ID, s.Dir, res})
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", ep.ID, verdictScore(res.Before), verdictScore(res.After), verdictPass(res.After),
			pointCount(res, *savepoints), bestScore(res.ProgressBefore, *savepoints), bestScore(res.ProgressAfter, *savepoints))
	}
	if !*asJSON {
		tw.Flush()
		note := "; run rl reward on the run to recompute the rewards from them"
		if *dry {
			note = " (dry run: nothing written)"
		}
		fmt.Fprintf(stdout, "%d episodes verified again, %d failed%s\n", done, failed, note)
	}
	if failed > 0 {
		return errors.New("rl reverify: some episodes could not be verified again")
	}
	return nil
}

// verdictScore formats a verdict's score for the table ("-" without one).
func verdictScore(v *rl.Verdict) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.3f", v.Score)
}

// verdictPass formats a verdict's pass for the table ("-" without one).
func verdictPass(v *rl.Verdict) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(v.Pass)
}

// pointCount is the table's savepoints column: how many points the curve had and has.
func pointCount(res env.ReverifyResult, scored bool) string {
	if !scored {
		return "-"
	}
	return fmt.Sprintf("%d -> %d", len(res.ProgressBefore), len(res.ProgressAfter))
}

// bestScore is the highest score of a progress curve ("-" when the savepoints were not scored or it has no point).
func bestScore(pts []rl.ProgressPoint, scored bool) string {
	if !scored || len(pts) == 0 {
		return "-"
	}
	best := pts[0].Score
	for _, p := range pts[1:] {
		best = max(best, p.Score)
	}
	return fmt.Sprintf("%.3f", best)
}
