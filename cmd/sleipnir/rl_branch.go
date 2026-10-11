package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// init registers the branching command.
func init() { rlCommands["branch"] = rlBranch }

// rlBranch continues a finished rollout (the trunk) from some of its savepoints, K times each, into a new run
// directory (env.Runner.Branch).
func rlBranch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl branch", stderr, "rl branch --tasks tasks.jsonl --from RUN/TASK/SAMPLE --at sp:N[,M] --k 4 --model MODEL --out RUN_DIR [flags]")
	var pf policyFlags
	var rf rigFlags
	pf.register(fs)
	rf.register(fs)
	tasksFile := fs.String("tasks", "", "tasks file (JSON lines) that holds the trunk's task")
	from := fs.String("from", "", "the trunk: the sample directory of a finished rollout that took savepoints (RUN/TASK/SAMPLE)")
	at := fs.String("at", "last", "where to branch: sp:N[,M...], every:K (every K-th savepoint), last, compaction (before each compaction), milestone (where the progress score rose) or plateau (where it stalled; needs --score-savepoints)")
	k := fs.Int("k", 4, "branches per point (the siblings of a point are one GRPO group)")
	horizon := fs.Duration("horizon", 0, "agent time a branch gets after its point (0: the rest of the trunk's budget)")
	compactFirst := fs.Bool("compact-first", false, "each branch compacts its context before anything else, when it has enough to fold: competing summaries of the point's state")
	out := fs.String("out", "", "run directory of the branches (rerunning into it resumes)")
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	switch {
	case *from == "" || *tasksFile == "" || *out == "":
		return errors.New("rl branch: --from, --tasks and --out are required")
	case *k < 1:
		return errors.New("rl branch: --k must be at least 1")
	case *horizon < 0:
		return errors.New("rl branch: --horizon must not be negative")
	}
	taskID := filepath.Base(filepath.Dir(filepath.Clean(*from)))
	ts := taskSelection{file: *tasksFile, ids: taskID}
	tasks, err := ts.load()
	if err != nil {
		return fmt.Errorf("rl branch: %w", err)
	}
	if len(tasks) != 1 {
		return fmt.Errorf("rl branch: %s has no task %q (the trunk's directory names it)", *tasksFile, taskID)
	}
	avail, err := env.TrunkSavepoints(*from)
	if err != nil {
		return fmt.Errorf("rl branch: %w", err)
	}
	var points []int
	switch *at {
	case "compaction", "milestone", "plateau":
		if points, err = env.TrunkPointsAt(*from, *at); err == nil && len(points) == 0 {
			err = fmt.Errorf("--at %s: the trunk has no such savepoint", *at)
		}
	default:
		points, err = branchPoints(*at, avail)
	}
	if err != nil {
		return fmt.Errorf("rl branch: %w", err)
	}
	pol, h, err := pf.resolve(fs)
	if err != nil {
		return fmt.Errorf("rl branch: %w", err)
	}
	rg, err := rf.build(h, *out, *tasksFile, stderr)
	if err != nil {
		return fmt.Errorf("rl branch: %w", err)
	}
	defer rg.Close()
	rg.Runner.Progress = progressPrinter(stderr)
	swarm, agents, single, terr := pf.team()
	if terr != nil {
		return fmt.Errorf("rl branch: %w", terr)
	}
	fmt.Fprintf(stderr, "branching %s at savepoints %v, %d each, with %s into %s\n", *from, points, *k, pol.Model, *out)
	sum, err := rg.Runner.Branch(ctx, tasks[0], env.BranchRequest{Trunk: *from, Points: points, Siblings: *k, Horizon: *horizon, CompactFirst: *compactFirst}, env.RolloutOpts{
		RunID: filepath.Base(*out), Policy: pol, Capture: pf.capture, Swarm: swarm, Agents: agents, Single: single,
		RoleModels: pf.roleModels, TargetPrice: rf.target, Seed: pf.seed, KeepFailed: rf.keepFailed, Budget: rf.budget(stderr),
	})
	if sum != nil {
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", " ")
			_ = enc.Encode(sum)
		} else {
			printSummary(stdout, sum)
		}
	}
	if err != nil {
		return fmt.Errorf("rl branch: %w", err)
	}
	return nothingCompleted("rl branch", sum)
}

// branchPoints resolves --at against the trunk's savepoints (in order).
func branchPoints(at string, avail []int) ([]int, error) {
	if len(avail) == 0 {
		return nil, errors.New("the trunk has no consistent savepoint")
	}
	kind, arg, _ := strings.Cut(at, ":")
	switch kind {
	case "last":
		return avail[len(avail)-1:], nil
	case "every":
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--at every:K needs a positive K, not %q", arg)
		}
		var out []int
		for _, sp := range avail {
			if sp%n == 0 {
				out = append(out, sp)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no savepoint of %v is a multiple of %d", avail, n)
		}
		return out, nil
	case "sp":
		var out []int
		for _, f := range strings.Split(arg, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				return nil, fmt.Errorf("--at sp:N[,M] needs savepoint numbers, not %q", f)
			}
			if !slices.Contains(avail, n) {
				return nil, fmt.Errorf("the trunk has no consistent savepoint %d (it has %v)", n, avail)
			}
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("--at is sp:N[,M], every:K or last, not %q", at)
}
