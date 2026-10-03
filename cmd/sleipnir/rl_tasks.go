package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/env/taskgen"
	"github.com/anemos-labs/sleipnir/internal/rl/recall"
)

// init registers task generation and task-file inspection commands.
func init() {
	rlCommands["taskgen"] = rlTaskgen
	rlCommands["tasks"] = rlTasks
}

// ---- rl tasks ----

func rlTasks(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	usage := `usage: sleipnir rl tasks <command> [flags] FILE

  validate  check every task in FILE and report all problems
  stats     count tasks by kind, tag, language and repository
  filter    write the tasks that match tags, ids or a deterministic sample
  split     assign whole repositories to named splits (train/val/test)
  check     prove each task is sound: the verifier fails on the start and passes with the reference solution
`
	if len(args) == 0 {
		printHelp(stderr, usage)
		return errors.New("rl tasks: a command is required")
	}
	switch args[0] {
	case "validate":
		return tasksValidate(args[1:], stdout, stderr)
	case "stats":
		return tasksStats(args[1:], stdout, stderr)
	case "filter":
		return tasksFilter(args[1:], stdout, stderr)
	case "split":
		return tasksSplit(args[1:], stdout, stderr)
	case "check":
		return tasksCheck(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printHelp(stdout, usage)
		return nil
	}
	printHelp(stderr, usage)
	return fmt.Errorf("rl tasks: unknown command %q", args[0])
}

// oneFile parses interspersed flags and requires exactly one positional tasks-file argument.
func oneFile(fs *flag.FlagSet, args []string, name string) (string, error) {
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return "", err
	}
	if len(pos) != 1 {
		return "", usageError(fs, fmt.Sprintf("%s: pass exactly one tasks file", name))
	}
	return pos[0], nil
}

// tasksValidate loads exactly one tasks file through normal validation and reports its valid task
// count.
func tasksValidate(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl tasks validate", stderr, "rl tasks validate FILE")
	file, err := oneFile(fs, args, "rl tasks validate")
	if err != nil {
		return err
	}
	tasks, err := env.LoadTasks(file)
	if err != nil {
		return fmt.Errorf("rl tasks validate: %w", err)
	}
	fmt.Fprintf(stdout, "%s: %d tasks, all valid\n", file, len(tasks))
	return nil
}

func tasksStats(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl tasks stats", stderr, "rl tasks stats FILE")
	file, err := oneFile(fs, args, "rl tasks stats")
	if err != nil {
		return err
	}
	tasks, err := env.LoadTasks(file)
	if err != nil {
		return fmt.Errorf("rl tasks stats: %w", err)
	}
	kinds, tags, repos, teams := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, t := range tasks {
		kinds[t.Kind]++
		repos[env.RepoKey(t)]++
		teams[orStr(t.Team.Mode, "single")]++
		for _, g := range t.Tags {
			tags[g]++
		}
	}
	fmt.Fprintf(stdout, "%s: %d tasks in %d repositories\n", file, len(tasks), len(repos))
	fmt.Fprintf(stdout, "  kinds: %s\n  teams: %s\n  tags:  %s\n", kvLine(kinds), kvLine(teams), kvLine(tags))
	return nil
}

// orStr returns the default when s is empty.
func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func tasksFilter(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl tasks filter", stderr, "rl tasks filter [flags] FILE")
	tags := fs.String("tag", "", "comma-separated tags a task must carry (prefix with ! to exclude)")
	ids := fs.String("id", "", "comma-separated task ids (path.Match wildcards allowed)")
	n := fs.Int("n", 0, "keep a deterministic sample of this many tasks")
	seed := fs.Int64("seed", 0, "seed of the sample")
	out := fs.String("o", "", "output file (default: stdout)")
	file, err := oneFile(fs, args, "rl tasks filter")
	if err != nil {
		return err
	}
	tasks, err := env.LoadTasks(file)
	if err != nil {
		return fmt.Errorf("rl tasks filter: %w", err)
	}
	kept := env.Filter(tasks, splitList(*tags), splitList(*ids), *n, *seed)
	w, closeOut, err := openOutput(*out, stdout)
	if err != nil {
		return fmt.Errorf("rl tasks filter: %w", err)
	}
	err = env.EncodeTasks(w, kept)
	if cerr := closeOut(err == nil); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("rl tasks filter: %w", err)
	}
	fmt.Fprintf(stderr, "kept %d of %d tasks\n", len(kept), len(tasks))
	return nil
}

func tasksSplit(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl tasks split", stderr, "rl tasks split [flags] FILE")
	spec := fs.String("spec", "train:0.8,test:0.2", "named parts and their shares, e.g. train:0.9,val:0.05,test:0.05")
	seed := fs.Int64("seed", 0, "seed of the assignment")
	prefix := fs.String("out-prefix", "", "write <prefix>.<part>.jsonl (default: next to FILE, without its extension)")
	file, err := oneFile(fs, args, "rl tasks split")
	if err != nil {
		return err
	}
	tasks, err := env.LoadTasks(file)
	if err != nil {
		return fmt.Errorf("rl tasks split: %w", err)
	}
	parts, err := env.Split(tasks, *spec, *seed)
	if err != nil {
		return fmt.Errorf("rl tasks split: %w", err)
	}
	pre := *prefix
	if pre == "" {
		pre = strings.TrimSuffix(file, filepath.Ext(file))
	}
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := pre + "." + name + ".jsonl"
		if err := env.WriteTasks(p, parts[name]); err != nil {
			return fmt.Errorf("rl tasks split: %w", err)
		}
		repos := map[string]bool{}
		for _, t := range parts[name] {
			repos[env.RepoKey(t)] = true
		}
		fmt.Fprintf(stdout, "%s: %d tasks from %d repositories\n", p, len(parts[name]), len(repos))
	}
	return nil
}

// tasksCheck runs every task's verifier on its starting state (it must fail) and
// with the reference solution applied (it must pass): the proof that a task can
// be solved, and that doing nothing does not solve it.
func tasksCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl tasks check", stderr, "rl tasks check [flags] FILE")
	var rf rigFlags
	rf.register(fs)
	goldDir := fs.String("gold", "", "blob store holding the reference solutions (default: --blobs)")
	report := fs.String("report", "", "also write each task's verdict as JSON ({id, ok, skipped, reason}): a script can quarantine what failed")
	file, err := oneFile(fs, args, "rl tasks check")
	if err != nil {
		return err
	}
	tasks, err := env.LoadTasks(file)
	if err != nil {
		return fmt.Errorf("rl tasks check: %w", err)
	}
	blobsDir := orStr(rf.blobs, filepath.Join(filepath.Dir(file), "blobs"))
	hidden, err := events.NewDirBlobs(blobsDir)
	if err != nil {
		return fmt.Errorf("rl tasks check: %w", err)
	}
	gold := hidden
	if *goldDir != "" {
		if gold, err = events.NewDirBlobs(*goldDir); err != nil {
			return fmt.Errorf("rl tasks check: %w", err)
		}
	}
	ws, err := rf.workspaces(stderr)
	if err != nil {
		return fmt.Errorf("rl tasks check: %w", err)
	}
	defer ws.Close()

	// With --verify-repeats a task is held to every run: the start must fail each time and the reference solution pass each
	// time (env.CheckTask), whatever --verify-policy says.
	type verdict struct {
		ID      string `json:"id"`
		OK      bool   `json:"ok"`
		Skipped bool   `json:"skipped,omitempty"`
		Reason  string `json:"reason,omitempty"`
	}
	verdicts := make([]verdict, len(tasks))
	vo := env.VerifyOptions{Workspaces: ws, HiddenBlobs: hidden, Repeats: rf.verifyRepeats}
	sem := make(chan struct{}, max(1, rf.concurrency))
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			v := verdict{ID: t.ID}
			defer func() { verdicts[i] = v }()
			var meta struct {
				GoldBlob  string          `json:"gold_blob"`
				GoldBlobs json.RawMessage `json:"gold_blobs"`
			}
			if len(t.Meta) > 0 {
				if err := json.Unmarshal(t.Meta, &meta); err != nil {
					v.Reason = fmt.Sprintf("invalid reference metadata: %v", err)
					return
				}
			}
			if len(meta.GoldBlobs) > 0 {
				var refs []string
				if err := json.Unmarshal(meta.GoldBlobs, &refs); err != nil {
					v.Reason = fmt.Sprintf("invalid reference metadata: %v", err)
					return
				}
				if len(refs) == 0 {
					v.Reason = "invalid reference metadata: gold_blobs must not be empty"
					return
				}
				if _, err := taskgen.ValidateComposite(ctx, t, gold, vo); err != nil {
					v.Reason = err.Error()
					return
				}
				v.OK = true
				return
			}
			if meta.GoldBlob == "" {
				v.Skipped, v.Reason = true, "no reference solution recorded"
				return
			}
			patch, err := gold.Get(core.Hash(meta.GoldBlob))
			if err != nil {
				v.Reason = fmt.Sprintf("reference solution unavailable: %v", err)
				return
			}
			if _, err := env.CheckTask(ctx, t, patch, vo); err != nil {
				v.Reason = err.Error()
				return
			}
			v.OK = true
		}()
	}
	wg.Wait()
	var bad, skipped int
	for _, v := range verdicts {
		switch {
		case v.Skipped:
			skipped++
			fmt.Fprintf(stdout, "%s: skipped (%s)\n", v.ID, v.Reason)
		case !v.OK:
			bad++
			fmt.Fprintf(stdout, "%s: FAIL: %s\n", v.ID, v.Reason)
		default:
			fmt.Fprintf(stdout, "%s: ok\n", v.ID)
		}
	}
	fmt.Fprintf(stdout, "%d tasks: %d ok, %d failed, %d skipped\n", len(tasks), len(tasks)-bad-skipped, bad, skipped)
	if *report != "" {
		if err := writeJSONFile(*report, verdicts); err != nil {
			return err
		}
	}
	if bad > 0 {
		return errors.New("rl tasks check: some tasks are unsound")
	}
	return nil
}

// ---- rl taskgen ----

func rlTaskgen(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	usage := `usage: sleipnir rl taskgen <generator> [flags]

  git        mine a repository's history: a commit that changes source and tests becomes a task
             (the parent commit is the start, the commit message the prompt, the commit's tests the hidden verifier)
  mutate     inject bugs the project's own tests catch; the reverse patch is the reference solution
  composite  combine independent tasks of one repository into swarm tasks for a manager and workers
  recall     memory tasks: read a fact early, read many other files, then state the fact exactly
             (a small context window makes the agent compact its history in between)
  fixture    authored tasks: a directory per task (task.json, start/, hidden/, solution/), for what mined
             history cannot give (other languages, tasks built from a specification, planted bugs)

Every task is validated in the same environment rollouts use before it is written:
the verifier must fail on the start state and pass with the reference solution.
`
	if len(args) == 0 {
		printHelp(stderr, usage)
		return errors.New("rl taskgen: a generator is required")
	}
	switch args[0] {
	case "git":
		return taskgenGit(ctx, args[1:], stdout, stderr)
	case "mutate":
		return taskgenMutate(ctx, args[1:], stdout, stderr)
	case "composite":
		return taskgenComposite(args[1:], stdout, stderr)
	case "recall":
		return taskgenRecall(args[1:], stdout, stderr)
	case "fixture":
		return taskgenFixture(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printHelp(stdout, usage)
		return nil
	}
	printHelp(stderr, usage)
	return fmt.Errorf("rl taskgen: unknown generator %q", args[0])
}

type multiFlag []string

// String joins repeated flag values with commas for flag formatting.
func (m *multiFlag) String() string { return strings.Join(*m, ",") }

// Set appends one flag occurrence without splitting or validating its value.
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// genCommon are the flags every generator shares.
type genCommon struct {
	repo, repoPath, out, report, idPrefix string
	tags, setup                           multiFlag
	testCmd, rev                          string
	max, concurrency                      int
	timeout                               time.Duration
	langs                                 string
	rig                                   rigFlags
}

// register binds common task-generation, verifier, output, and rig options to a flag set.
func (g *genCommon) register(fs *flag.FlagSet) {
	fs.StringVar(&g.repo, "repo", "", "the repository to generate from (a local path)")
	fs.StringVar(&g.repoPath, "repo-path", "", repoPathUsage)
	fs.StringVar(&g.out, "o", "tasks.jsonl", "output tasks file")
	fs.StringVar(&g.report, "report", "", "also write the generation report (counts and every rejection with its reason) as JSON")
	fs.StringVar(&g.idPrefix, "id-prefix", "", "prefix of task ids (default: the repository directory name)")
	fs.Var(&g.tags, "tag", "tag added to every task, repeatable")
	fs.Var(&g.setup, "setup", "setup command run once per task snapshot, repeatable (default: detected)")
	fs.StringVar(&g.testCmd, "test", "", "test command (default: detected)")
	fs.StringVar(&g.rev, "rev", "", "revision to generate from (default HEAD)")
	fs.IntVar(&g.max, "max", 0, "stop after this many tasks (0: all)")
	fs.IntVar(&g.concurrency, "gen-concurrency", 2, "candidates validated at once")
	fs.DurationVar(&g.timeout, "timeout", 0, "verifier timeout written into tasks (default 5m)")
	fs.StringVar(&g.langs, "lang", "", "comma-separated languages to keep (go, python)")
	g.rig.register(fs)
}

// stores opens the hidden-verifier blob store at the configured location or beside the output
// tasks file.
func (g *genCommon) stores() (hidden events.Blobs, blobsDir string, err error) {
	// --blobs (a rig flag): the store for hidden verifier files and reference
	// solutions; it must travel with the tasks file.
	blobsDir = g.rig.blobs
	if blobsDir == "" {
		blobsDir = filepath.Join(filepath.Dir(g.out), "blobs")
	}
	hidden, err = events.NewDirBlobs(blobsDir)
	return hidden, blobsDir, err
}

func (g *genCommon) finish(tasks []rl.Task, rep *taskgen.Report, blobsDir string, stdout, stderr io.Writer) error {
	nameRepo(tasks, g.repoPath)
	if err := env.ValidateTasks(tasks); err != nil {
		return fmt.Errorf("the generator produced invalid tasks: %w", err)
	}
	if err := env.WriteTasks(g.out, tasks); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d tasks to %s (hidden files and reference solutions in %s)\n", len(tasks), g.out, blobsDir)
	if rep != nil {
		fmt.Fprintf(stdout, "  examined %d commits, %d candidates, %d accepted, in %s\n", rep.Examined, rep.Candidates, rep.Accepted, rep.Duration.Round(time.Second))
		if len(rep.Rejected) > 0 {
			fmt.Fprintf(stdout, "  rejected: %s\n", kvLine(rep.Rejected))
		}
		if len(rep.InfraErrors) > 0 {
			fmt.Fprintf(stdout, "  %d candidates were skipped because of infrastructure errors (see --report)\n", len(rep.InfraErrors))
		}
		if g.report != "" {
			if err := writeJSONFile(g.report, rep); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(stderr, "next: sleipnir rl tasks split %s, then sleipnir rl rollout --tasks <train file> --model <policy>\n", g.out)
	return nil
}

const repoPathUsage = "how the tasks name the repository (default: the absolute path of --repo); a path relative to the directory the rollouts run in keeps the tasks file independent of this machine"

// nameRepo records how the tasks name their repository (see repoPathUsage); "" keeps what the generator wrote.
func nameRepo(tasks []rl.Task, as string) {
	if as == "" {
		return
	}
	as = filepath.ToSlash(as)
	for i := range tasks {
		tasks[i].Repo.Path = as
	}
}

func taskgenGit(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl taskgen git", stderr, "rl taskgen git --repo PATH [flags]")
	var g genCommon
	g.register(fs)
	since := fs.String("since", "", "only commits authored on or after this date (2006-01-02)")
	until := fs.String("until", "", "only commits authored before this date")
	maxCommits := fs.Int("max-commits", 0, "commits to scan (default 20000)")
	maxFiles := fs.Int("max-files", 0, "skip commits changing more files than this (default 12)")
	maxLines := fs.Int("max-lines", 0, "skip commits changing more lines than this (default 800)")
	licenses := fs.String("licenses", "", "comma-separated SPDX ids the repository's licence must be one of")
	repeats := fs.Int("repeats", 0, "run each baseline this many times to catch flaky tests")
	noValidate := fs.Bool("no-validate", false, "skip the baseline and reference-solution runs (tasks are then unverified: for dry runs only)")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if g.repo == "" {
		return errors.New("rl taskgen git: --repo is required")
	}
	hidden, blobsDir, err := g.stores()
	if err != nil {
		return fmt.Errorf("rl taskgen git: %w", err)
	}
	opts := taskgen.GitOptions{
		NoValidate: *noValidate, Rev: g.rev, MaxCommits: *maxCommits, Max: g.max,
		MaxFiles: *maxFiles, MaxLines: *maxLines, Languages: splitList(g.langs), AllowLicenses: splitList(*licenses),
		TestCmd: g.testCmd, Timeout: g.timeout, Concurrency: g.concurrency, Repeats: *repeats,
		HiddenBlobs: hidden, GoldBlobs: hidden, IDPrefix: g.idPrefix, Tags: g.tags,
		Progress: func(m string) { fmt.Fprintln(stderr, m) },
	}
	if len(g.setup) > 0 {
		opts.Setup = []string(g.setup)
	}
	if opts.Since, err = parseDate(*since); err != nil {
		return fmt.Errorf("rl taskgen git: --since: %w", err)
	}
	if opts.Until, err = parseDate(*until); err != nil {
		return fmt.Errorf("rl taskgen git: --until: %w", err)
	}
	if !*noValidate {
		ws, err := g.rig.workspaces(stderr)
		if err != nil {
			return fmt.Errorf("rl taskgen git: %w", err)
		}
		defer ws.Close()
		opts.Workspaces = ws
	}
	tasks, rep, err := taskgen.FromGit(ctx, g.repo, opts)
	if err != nil {
		return fmt.Errorf("rl taskgen git: %w", err)
	}
	return g.finish(tasks, rep, blobsDir, stdout, stderr)
}

func taskgenMutate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl taskgen mutate", stderr, "rl taskgen mutate --repo PATH [flags]")
	var g genCommon
	g.register(fs)
	attempts := fs.Int("max-attempts", 0, "candidate mutations to try (default 20 x --max)")
	seed := fs.Int64("seed", 0, "selects which mutations are tried first; the result is deterministic")
	var files multiFlag
	fs.Var(&files, "file", "restrict mutated files to this path.Match pattern, repeatable")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if g.repo == "" {
		return errors.New("rl taskgen mutate: --repo is required")
	}
	hidden, blobsDir, err := g.stores()
	if err != nil {
		return fmt.Errorf("rl taskgen mutate: %w", err)
	}
	ws, err := g.rig.workspaces(stderr)
	if err != nil {
		return fmt.Errorf("rl taskgen mutate: %w", err)
	}
	defer ws.Close()
	opts := taskgen.MutateOptions{
		Workspaces: ws, Rev: g.rev, Max: g.max, MaxAttempts: *attempts, Seed: *seed,
		Languages: splitList(g.langs), Files: []string(files), TestCmd: g.testCmd,
		Timeout: g.timeout, Concurrency: g.concurrency, IDPrefix: g.idPrefix, Tags: g.tags,
		GoldBlobs: hidden, Progress: func(m string) { fmt.Fprintln(stderr, m) },
	}
	if len(g.setup) > 0 {
		opts.Setup = []string(g.setup)
	}
	tasks, rep, err := taskgen.Mutate(ctx, g.repo, opts)
	if err != nil {
		return fmt.Errorf("rl taskgen mutate: %w", err)
	}
	return g.finish(tasks, rep, blobsDir, stdout, stderr)
}

func taskgenComposite(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl taskgen composite", stderr, "rl taskgen composite -k K [flags] TASKS")
	k := fs.Int("k", 3, "independent tasks combined into one swarm task")
	seed := fs.Int64("seed", 0, "seed of the grouping")
	max := fs.Int("max", 0, "stop after this many composite tasks (0: all)")
	out := fs.String("o", "", "output file (default: <TASKS>.composite.jsonl)")
	file, err := oneFile(fs, args, "rl taskgen composite")
	if err != nil {
		return err
	}
	base, err := env.LoadTasks(file)
	if err != nil {
		return fmt.Errorf("rl taskgen composite: %w", err)
	}
	var opts []taskgen.CompositeOption
	if *max > 0 {
		opts = append(opts, taskgen.WithMax(*max))
	}
	tasks, err := taskgen.Composite(base, *k, *seed, opts...)
	if err != nil {
		return fmt.Errorf("rl taskgen composite: %w", err)
	}
	dest := orStr(*out, strings.TrimSuffix(file, filepath.Ext(file))+".composite.jsonl")
	if err := env.WriteTasks(dest, tasks); err != nil {
		return fmt.Errorf("rl taskgen composite: %w", err)
	}
	fmt.Fprintf(stdout, "wrote %d composite tasks (%d components each) from %d tasks to %s\n", len(tasks), *k, len(base), dest)
	fmt.Fprintf(stderr, "composite verifiers reuse the components' hidden files: keep the blobs directory next to the output (copy or link it)\n")
	return nil
}

func taskgenRecall(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl taskgen recall", stderr, "rl taskgen recall --repo PATH [flags]")
	repo := fs.String("repo", "", "the repository to ask about (a local path)")
	repoPath := fs.String("repo-path", "", repoPathUsage)
	out := fs.String("o", "recall.jsonl", "output tasks file")
	rev := fs.String("rev", "", "revision to build tasks from (default HEAD)")
	maxTasks := fs.Int("max", 10, "tasks wanted")
	files := fs.Int("files", 12, "other files the agent must read between learning the fact and stating it")
	window := fs.Int("window", 12000, "context window in tokens written into each task: small, so the reading overflows it and history is compacted")
	seed := fs.Int64("seed", 0, "selects which facts and files are used; the result is deterministic")
	langs := fs.String("lang", "", "comma-separated languages to use (go, python)")
	idPrefix := fs.String("id-prefix", "recall", "prefix of task ids")
	var tags multiFlag
	fs.Var(&tags, "tag", "tag added to every task, repeatable")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("rl taskgen recall: --repo is required")
	}
	scratch, err := os.MkdirTemp("", "sleipnir-recall-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	g, err := env.NewGit(env.GitOptions{Scratch: scratch})
	if err != nil {
		return fmt.Errorf("rl taskgen recall: %w", err)
	}
	abs, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	tasks, err := recall.Generate(context.Background(), abs, recall.Options{
		Git: g, Rev: *rev, Max: *maxTasks, Files: *files, Window: *window, Seed: *seed,
		Languages: splitList(*langs), IDPrefix: *idPrefix, Tags: []string(tags),
	})
	if err != nil {
		return fmt.Errorf("rl taskgen recall: %w", err)
	}
	nameRepo(tasks, *repoPath)
	if err := env.WriteTasks(*out, tasks); err != nil {
		return fmt.Errorf("rl taskgen recall: %w", err)
	}
	fmt.Fprintf(stdout, "wrote %d recall tasks to %s (each reads %d files under a %d-token window)\n", len(tasks), *out, *files, *window)
	fmt.Fprintf(stderr, "run them like any tasks: sleipnir rl rollout --tasks %s --model <policy> --group 4 ...\n", *out)
	return nil
}

// taskgenFixture turns a directory of authored fixtures into tasks (see taskgen.FixtureSpec). It runs nothing: `rl tasks check`
// then proves each task sound in the environment rollouts use.
func taskgenFixture(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl taskgen fixture", stderr, "rl taskgen fixture --dir DIR [flags]")
	dir := fs.String("dir", "", "directory of fixtures: one subdirectory each, with task.json, start/, hidden/ and solution/")
	ids := fs.String("id", "", "comma-separated fixture ids (path.Match wildcards allowed); default all")
	out := fs.String("o", "tasks.jsonl", "output tasks file")
	repoRoot := fs.String("repo-root", "", "where the fixtures' repositories are written, one per fixture (default: fixture-repos next to -o)")
	prefix := fs.String("repo-path-prefix", "", "how a task names its repository: <prefix>/<id>, resolved against the directory a rollout runs in, so the tasks file is not tied to this machine (default: the base name of --repo-root)")
	blobs := fs.String("blobs", "", "blob store for the hidden files and reference solutions (default: blobs/ next to -o)")
	var tags multiFlag
	fs.Var(&tags, "tag", "tag added to every task, repeatable")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if *dir == "" {
		return errors.New("rl taskgen fixture: --dir is required")
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		return fmt.Errorf("rl taskgen fixture: %w", err)
	}
	var want []string
	if *ids != "" {
		want = splitList(*ids)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(*dir, e.Name(), "task.json")); err != nil {
			continue
		}
		if len(want) > 0 {
			ok := false
			for _, w := range want {
				if m, _ := path.Match(w, e.Name()); m {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return fmt.Errorf("rl taskgen fixture: no fixture in %s matches", *dir)
	}
	sort.Strings(names)
	outDir := filepath.Dir(*out)
	if *repoRoot == "" {
		*repoRoot = filepath.Join(outDir, "fixture-repos")
	}
	if *prefix == "" {
		*prefix = filepath.Base(*repoRoot)
	}
	blobsDir := *blobs
	if blobsDir == "" {
		blobsDir = filepath.Join(outDir, "blobs")
	}
	store, err := events.NewDirBlobs(blobsDir)
	if err != nil {
		return fmt.Errorf("rl taskgen fixture: %w", err)
	}
	g, err := env.NewGit(env.GitOptions{})
	if err != nil {
		return fmt.Errorf("rl taskgen fixture: %w", err)
	}
	defer g.Close()
	var tasks []rl.Task
	for _, n := range names {
		t, err := taskgen.FromFixture(ctx, filepath.Join(*dir, n), taskgen.FixtureOptions{
			RepoRoot: *repoRoot, RepoPathPrefix: *prefix, HiddenBlobs: store, GoldBlobs: store, Git: g,
		})
		if err != nil {
			return fmt.Errorf("rl taskgen fixture: %w", err)
		}
		t.Tags = dedupeStrings(append(t.Tags, []string(tags)...))
		tasks = append(tasks, *t)
	}
	if err := env.ValidateTasks(tasks); err != nil {
		return fmt.Errorf("rl taskgen fixture: %w", err)
	}
	if err := env.WriteTasks(*out, tasks); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d tasks to %s (repositories in %s, hidden files and reference solutions in %s)\n", len(tasks), *out, *repoRoot, blobsDir)
	fmt.Fprintf(stderr, "next: sleipnir rl tasks check %s, run from the directory that holds %s/ so the tasks' relative repositories resolve\n", *out, *prefix)
	return nil
}

// dedupeStrings removes empty and duplicate strings while preserving first occurrence order.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// parseDate accepts YYYY-MM-DD in UTC and treats empty input as an unset time.
func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02", s)
}
