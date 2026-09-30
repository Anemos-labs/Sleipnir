package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/adv"
	"github.com/reee344/sleipnir/internal/rl/env"
	"github.com/reee344/sleipnir/internal/rl/export"
	"github.com/reee344/sleipnir/internal/rl/redact"
	"github.com/reee344/sleipnir/internal/rl/reward"
	"github.com/reee344/sleipnir/internal/rl/traj"
)

func init() { extraCommands["rl"] = cmdRL }

const rlUsage = `usage: sleipnir rl <command> [flags] [args]

The harness is an RL environment: it runs a policy on tasks, verifies the result
in a clean checkout, scores it, and writes trainer-ready data (docs/TRAINING-DATA.md).

  taskgen    make tasks from git history, compose swarm tasks, generate recall tasks or mutations
  tasks      validate, filter, split and check task files
  rollout    run G samples per task with a policy and write a run directory
  eval       run held-out tasks and report pass@k, cost and protocol quality
  serve      HTTP rollout server for a trainer
  reward     re-score a run directory with different reward weights
  export     write trainer-ready data: steps, tokens, groups, sft, dpo, kto, atif, canonical
  expand     turn a deduplicated canonical export back into inline form
  verify     replay every recorded prompt and check it against its wire hash
  show       summarise a run directory, or one episode of it

Run "sleipnir rl <command> -h" for a command's flags.
`

var rlCommands = map[string]func(context.Context, []string, io.Writer, io.Writer) error{
	"export": rlExport,
	"expand": rlExpand,
	"verify": rlVerify,
	"reward": rlReward,
	"show":   rlShow,
}

func cmdRL(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, rlUsage)
		return errors.New("rl: a command is required")
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, rlUsage)
		return nil
	}
	fn, ok := rlCommands[args[0]]
	if !ok {
		fmt.Fprint(os.Stderr, rlUsage)
		return fmt.Errorf("rl: unknown command %q", args[0])
	}
	return fn(ctx, args[1:], os.Stdout, os.Stderr)
}

// newFlags makes a flag set that reports errors instead of exiting, so the
// commands can be driven from tests.
func newFlags(name string, stderr io.Writer, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: sleipnir %s\n\nflags:\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- rl export ----

func rlExport(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl export", stderr, "rl export [flags] RUN_DIR...")
	format := fs.String("format", "steps", "steps | tokens | groups | sft | dpo | kto | atif | canonical")
	out := fs.String("o", "", "output file (default: stdout)")
	statsPath := fs.String("stats", "", "also write the export statistics as JSON to this file")
	method := fs.String("advantage", "", "grpo | rloo | broadcast | anchor | none (default: grpo for steps, tokens and groups; none for the rest)")
	groupBy := fs.String("group-by", adv.GroupByTaskPolicy, "advantage baseline group: task | task+policy | group")
	roles := fs.String("roles", "", "comma-separated roles to keep (worker, manager, reviewer, compactor, mailman); empty keeps all")
	minReward := fs.Float64("min-reward", 0, "sft, dpo and kto: smallest episode reward that counts as a positive example")
	topK := fs.Int("top-k", 0, "sft: keep the K best episodes per task (0 keeps all that qualify)")
	keepFlagged := fs.Bool("keep-flagged", false, "export episodes with hard flags too (infra errors, truncation, reward hacking, ...)")
	keepFlat := fs.Bool("keep-flat", false, "keep groups whose rewards are all equal (they carry no learning signal)")
	keepWeak := fs.Bool("keep-weak", false, "keep episodes with no verifier verdict in the training formats")
	teacher := fs.String("teacher", "", "comma-separated non-policy models whose completions may be trained on (sft, dpo, kto); others are skipped")
	licenses := fs.String("licenses", "", "comma-separated SPDX ids an episode's repository may carry (empty allows any)")
	inline := fs.Bool("inline", false, "canonical: embed the full prompt in every step instead of a segment table")
	table := fs.String("table", "", "canonical: write the segment table to this file instead of the head of the output")
	reasoning := fs.String("reasoning", "drop", "reasoning in completions: drop | field | keep")
	maxPrompt := fs.Int("max-prompt-tokens", 0, "drop steps whose prompt is longer (0: no limit)")
	maxSamples := fs.Int("max-samples", 0, "stop after this many records (0: no limit)")
	split := fs.String("split", "", "assign whole repositories to splits, e.g. train:0.9,val:0.1")
	seed := fs.Int64("seed", 0, "salt of the split assignment")
	pack := fs.Bool("pack", false, "tokens and sft: pack an agent's segment into one sequence when its token traces chain")
	systemRole := fs.String("system-role", "", "wire role of the system prompt (default: system)")
	noRedact := fs.Bool("no-redact", false, "do not redact secrets and personal data (only for data that never leaves your machine)")
	salt := fs.String("redact-salt", "", "salt of the redaction tokens; equal secrets get equal tokens per salt")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fs.Usage()
		return errors.New("rl export: pass one or more run directories (the --out of rl rollout)")
	}
	f := export.Format(*format)
	if !validFormat(f) {
		return fmt.Errorf("rl export: unknown format %q (want %s)", *format, formatNames())
	}
	if err := oneOf("reasoning", *reasoning, "drop", "field", "keep"); err != nil {
		return fmt.Errorf("rl export: %w", err)
	}

	samples, err := findSamples(paths)
	if err != nil {
		return fmt.Errorf("rl export: %w", err)
	}
	cache := newRunCache(4)
	srcs := make([]export.Source, len(samples))
	for i, s := range samples {
		srcs[i] = export.Source{Episode: s.Episode, Prompts: prompts{dir: s.Dir, cache: cache}}
	}

	o := export.DefaultOptions(f)
	o.Roles = splitList(*roles)
	o.MinReward, o.TopK = *minReward, *topK
	o.DropFlagged = !*keepFlagged
	o.KeepFlat, o.KeepWeak = *keepFlat, *keepWeak
	o.TeacherOK = splitList(*teacher)
	o.Licenses = splitList(*licenses)
	o.Inline = *inline
	o.Reasoning = *reasoning
	o.MaxPromptTokens, o.MaxSamples = *maxPrompt, *maxSamples
	o.Split, o.Seed = *split, *seed
	o.PackSegments = *pack
	o.Wire.SystemRole = *systemRole

	m := strings.ToLower(strings.TrimSpace(*method))
	if m == "" {
		m = adv.MethodNone
		if f == export.FormatSteps || f == export.FormatTokens || f == export.FormatGroups {
			m = adv.MethodGRPO
		}
	}
	if m != adv.MethodNone {
		spec, err := adv.SpecFor(m)
		if err != nil {
			return fmt.Errorf("rl export: %w", err)
		}
		spec.GroupBy = *groupBy
		o.Advantage = adv.GRPOFunc(spec)
	}
	if !*noRedact {
		rc := redact.Config{Salt: *salt}
		if err := rc.Validate(); err != nil {
			return fmt.Errorf("rl export: %w", err)
		}
		o.Redactor = redact.New(rc)
	}

	w, closeOut, err := openOutput(*out, stdout)
	if err != nil {
		return fmt.Errorf("rl export: %w", err)
	}
	var tableClose func(bool) error
	if *table != "" {
		var tw io.Writer
		tw, tableClose, err = openOutput(*table, nil)
		if err != nil {
			closeOut(false)
			return fmt.Errorf("rl export: %w", err)
		}
		o.Table = tw
	}
	st, err := export.Export(w, srcs, o)
	if tableClose != nil {
		if cerr := tableClose(err == nil); err == nil {
			err = cerr
		}
	}
	if cerr := closeOut(err == nil); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("rl export: %w", err)
	}
	printExportStats(stderr, st, *out)
	if *statsPath != "" {
		if err := writeJSONFile(*statsPath, st); err != nil {
			return fmt.Errorf("rl export: writing statistics: %w", err)
		}
	}
	return nil
}

func validFormat(f export.Format) bool {
	for _, x := range export.Formats() {
		if x == f {
			return true
		}
	}
	return false
}

func formatNames() string {
	var n []string
	for _, f := range export.Formats() {
		n = append(n, string(f))
	}
	return strings.Join(n, ", ")
}

// openOutput opens path for writing (mode 0600: training data is derived from
// private code) or returns def. The returned close function removes a partial
// file when the export failed.
func openOutput(path string, def io.Writer) (io.Writer, func(ok bool) error, error) {
	if path == "" || path == "-" {
		if def == nil {
			return nil, nil, errors.New("an output file is required here")
		}
		return def, func(bool) error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return f, func(ok bool) error {
		err := f.Close()
		if !ok {
			os.Remove(path)
		}
		return err
	}, nil
}

func printExportStats(w io.Writer, st export.Stats, out string) {
	dest := out
	if dest == "" || dest == "-" {
		dest = "stdout"
	}
	fmt.Fprintf(w, "exported %d %s records from %d of %d episodes to %s\n", st.Records, st.Format, st.Kept, st.Episodes, dest)
	if st.PromptTokens+st.ResponseTokens > 0 {
		fmt.Fprintf(w, "  tokens: %d prompt, %d response, %d trained\n", st.PromptTokens, st.ResponseTokens, st.TrainedTokens)
	}
	if len(st.ByRole) > 0 {
		fmt.Fprintf(w, "  by role: %s\n", kvLine(st.ByRole))
	}
	if len(st.BySplit) > 0 {
		fmt.Fprintf(w, "  by split: %s\n", kvLine(st.BySplit))
	}
	if len(st.Drops) > 0 {
		fmt.Fprintf(w, "  dropped: %s\n", kvLine(st.Drops))
	}
	if st.Packed > 0 || len(st.PackFailed) > 0 {
		fmt.Fprintf(w, "  packed segments: %d (%d steps); not packed: %s\n", st.Packed, st.PackedSteps, kvLine(st.PackFailed))
	}
	if len(st.Redactions) > 0 {
		fmt.Fprintf(w, "  redacted: %s\n", kvLine(st.Redactions))
	}
	if len(st.TeacherSkipped) > 0 {
		fmt.Fprintf(w, "  teacher steps skipped (not in --teacher): %s\n", kvLine(st.TeacherSkipped))
	}
	for _, x := range st.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", x)
	}
}

func kvLine(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

// ---- rl expand ----

func rlExpand(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl expand", stderr, "rl expand [flags] EXPORT.jsonl")
	table := fs.String("table", "", "the segment table written by rl export --table (default: the head of EXPORT.jsonl)")
	out := fs.String("o", "", "output file (default: stdout)")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) != 1 {
		fs.Usage()
		return errors.New("rl expand: pass exactly one canonical export")
	}
	in, err := os.Open(paths[0])
	if err != nil {
		return fmt.Errorf("rl expand: %w", err)
	}
	defer in.Close()
	var tr io.Reader
	if *table != "" {
		tf, err := os.Open(*table)
		if err != nil {
			return fmt.Errorf("rl expand: %w", err)
		}
		defer tf.Close()
		tr = tf
	}
	w, closeOut, err := openOutput(*out, stdout)
	if err != nil {
		return fmt.Errorf("rl expand: %w", err)
	}
	n, err := export.Expand(w, in, tr)
	if cerr := closeOut(err == nil); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("rl expand: %w", err)
	}
	fmt.Fprintf(stderr, "expanded %d episodes\n", n)
	return nil
}

// ---- rl verify ----

// rlVerify replays every recorded prompt from the log and compares it with the
// hash of what was sent on the wire. A mismatch means the log cannot reproduce
// what the policy saw, so its data must not be trained on.
func rlVerify(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl verify", stderr, "rl verify RUN_DIR...")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fs.Usage()
		return errors.New("rl verify: pass one or more run directories")
	}
	samples, err := findSamples(paths)
	if err != nil {
		return fmt.Errorf("rl verify: %w", err)
	}
	var bad, unreadable int
	for _, s := range samples {
		run, err := traj.Open(s.Dir)
		if err != nil {
			unreadable++
			fmt.Fprintf(stdout, "%s: cannot open the log: %v\n", s.Dir, err)
			continue
		}
		for _, m := range run.Verify() {
			bad++
			fmt.Fprintf(stdout, "%s: %s: %s: %s\n", s.Dir, m.Req, m.Kind, m.Detail)
		}
	}
	fmt.Fprintf(stdout, "verified %d rollouts: %d mismatches, %d unreadable\n", len(samples), bad, unreadable)
	if bad > 0 || unreadable > 0 {
		return errors.New("rl verify: some rollouts do not replay; do not train on them")
	}
	return nil
}

// ---- rl reward ----

// rlReward re-scores rollouts under different weights, caps or target prices
// without running anything again. Scoring is a pure function of the recorded
// episode, so this is how reward shaping is iterated on.
func rlReward(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl reward", stderr, "rl reward [flags] RUN_DIR...")
	rewards := fs.String("rewards", "", "rewards.json with weights, caps, detectors (default: the documented defaults)")
	target := fs.String("target-price", "", "price and cache model episodes are repriced under: a preset or a model id (see -list-targets)")
	tasksFile := fs.String("tasks", "", "tasks file to score against (default: each rollout's task.json, whose hidden files are redacted)")
	dry := fs.Bool("dry-run", false, "print the new rewards without rewriting episode.json")
	list := fs.Bool("list-targets", false, "list the target-price presets and exit")
	probes := fs.Bool("probes", true, "run compaction fidelity probes")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if *list {
		for _, t := range reward.Targets() {
			fmt.Fprintln(stdout, t)
		}
		return nil
	}
	if len(paths) == 0 {
		fs.Usage()
		return errors.New("rl reward: pass one or more run directories")
	}
	cfg := reward.DefaultConfig()
	if *rewards != "" {
		if cfg, err = reward.LoadConfig(*rewards); err != nil {
			return fmt.Errorf("rl reward: %w", err)
		}
	}
	if *target != "" {
		if _, ok := reward.LookupTarget(*target); !ok {
			return fmt.Errorf("rl reward: unknown target price %q (see --list-targets)", *target)
		}
		cfg.TargetName = *target
		cfg.Target = cost.Model{}
	}
	cfg.Probes = cfg.Probes && *probes
	var byID map[string]rl.Task
	if *tasksFile != "" {
		ts, err := env.LoadTasks(*tasksFile)
		if err != nil {
			return fmt.Errorf("rl reward: %w", err)
		}
		byID = map[string]rl.Task{}
		for _, t := range ts {
			byID[t.ID] = t
		}
	}
	samples, err := findSamples(paths)
	if err != nil {
		return fmt.Errorf("rl reward: %w", err)
	}
	cache := newRunCache(4)
	var before, after float64
	var n, hacks int
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EPISODE\tBEFORE\tAFTER\tFLAGS")
	for _, s := range samples {
		ep := s.Episode
		if ep.Has(rl.FlagInfraError) {
			continue
		}
		task := taskFor(s, byID)
		blobs, err := blobsOf(s.Dir)
		if err != nil {
			return fmt.Errorf("rl reward: %s: %w", s.Dir, err)
		}
		c := cfg
		c.Prompts = promptText(cache, s.Dir)
		was := ep.Reward.Total
		if err := reward.Score(ep, task, c, reward.DiffsFromBlobs(blobs.Get)); err != nil {
			return fmt.Errorf("rl reward: %s: %w", ep.ID, err)
		}
		before, after, n = before+was, after+ep.Reward.Total, n+1
		if hasHack(ep) {
			hacks++
		}
		fmt.Fprintf(tw, "%s\t%.3f\t%.3f\t%s\n", ep.ID, was, ep.Reward.Total, strings.Join(ep.Flags, ","))
		if !*dry {
			if err := writeJSONFile(filepath.Join(s.Dir, "episode.json"), ep); err != nil {
				return fmt.Errorf("rl reward: %s: %w", ep.ID, err)
			}
		}
	}
	tw.Flush()
	if n > 0 {
		fmt.Fprintf(stdout, "%d episodes rescored: mean reward %.3f -> %.3f, %d with hack flags%s\n", n, before/float64(n), after/float64(n), hacks, map[bool]string{true: " (dry run: nothing written)", false: ""}[*dry])
	}
	return nil
}

func hasHack(ep *rl.Episode) bool {
	for _, f := range ep.Flags {
		if strings.HasPrefix(f, "hack:") {
			return true
		}
	}
	return false
}

// taskFor picks the task a rollout is scored against: the caller's tasks file
// (which still has the hidden files the hardcoding detector compares against),
// else the task.json the runner stored.
func taskFor(s sample, byID map[string]rl.Task) *rl.Task {
	if t, ok := byID[s.Episode.TaskID]; ok {
		return &t
	}
	if t, err := readTask(s.Dir); err == nil {
		return t
	}
	return &rl.Task{ID: s.Episode.TaskID}
}

// ---- rl show ----

func rlShow(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl show", stderr, "rl show [flags] RUN_DIR [TASK/SAMPLE]")
	asJSON := fs.Bool("json", false, "print the summary (or the episode) as JSON")
	paths, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 || len(paths) > 2 {
		fs.Usage()
		return errors.New("rl show: pass a run directory, and optionally task/sample")
	}
	if len(paths) == 2 {
		return showEpisode(filepath.Join(paths[0], filepath.FromSlash(paths[1])), *asJSON, stdout)
	}
	b, err := os.ReadFile(filepath.Join(paths[0], "summary.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return showEpisode(paths[0], *asJSON, stdout) // a sample directory
		}
		return fmt.Errorf("rl show: %w", err)
	}
	if *asJSON {
		_, err := stdout.Write(b)
		return err
	}
	var sum env.Summary
	if err := json.Unmarshal(b, &sum); err != nil {
		return fmt.Errorf("rl show: summary.json: %w", err)
	}
	printSummary(stdout, &sum)
	return nil
}

func printSummary(w io.Writer, s *env.Summary) {
	state := "finished"
	switch {
	case s.Partial:
		state = "running (partial)"
	case s.Interrupted:
		state = "interrupted"
	}
	fmt.Fprintf(w, "run %s: %s; %d tasks x %d samples\n", s.RunID, state, s.Tasks, s.Group)
	fmt.Fprintf(w, "  rollouts %d: %d completed (%d resumed), %d infra errors, %d cancelled, %d pending\n", s.Rollouts, s.Completed, s.Resumed, s.Infra, s.Cancelled, s.Pending)
	fmt.Fprintf(w, "  pass rate %.1f%%   mean score %.3f   mean reward %.3f   hack rate %.1f%%   budget rate %.1f%%\n", 100*s.PassRate, s.MeanScore, s.MeanReward, 100*s.HackRate, 100*s.BudgetRate)
	fmt.Fprintf(w, "  mean cost $%.4f   mean ITE %.0f   mean requests %.1f   mean steps %.1f   mean wall %.1fs\n", s.MeanCostUSD, s.MeanITE, s.MeanRequests, s.MeanSteps, s.MeanWallMs/1000)
	if len(s.PerTask) > 0 {
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TASK\tSAMPLES\tPASSED\tPASS%\tREWARD\tCOST")
		for _, t := range s.PerTask {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%.0f\t%.3f\t$%.4f\n", t.ID, t.Samples, t.Passed, 100*t.PassRate, t.MeanReward, t.MeanCostUSD)
		}
		tw.Flush()
	}
	for _, e := range s.InfraErrors {
		fmt.Fprintf(w, "infra: %s/%d after %d attempts: %s\n", e.Task, e.Sample, e.Attempts, e.Message)
	}
	for _, x := range s.Warnings {
		fmt.Fprintf(w, "warning: %s\n", x)
	}
}

func showEpisode(dir string, asJSON bool, w io.Writer) error {
	ep, err := readEpisode(dir)
	if err != nil {
		return fmt.Errorf("rl show: %w", err)
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", " ")
		return enc.Encode(ep)
	}
	fmt.Fprintf(w, "episode %s (group %s)\n", ep.ID, ep.Group)
	fmt.Fprintf(w, "  policy %s   harness %s   renderer %s\n", ep.Policy.Model, ep.Harness.Version, ep.Harness.Renderer)
	if v := ep.Outcome.Verifier; v != nil {
		fmt.Fprintf(w, "  verifier: pass=%v score=%.2f (%d ms)   claimed: %s\n", v.Pass, v.Score, v.Ms, ep.Outcome.Claimed)
	} else {
		fmt.Fprintf(w, "  no verifier verdict   claimed: %s\n", ep.Outcome.Claimed)
	}
	fmt.Fprintf(w, "  reward %.3f %s\n", ep.Reward.Total, componentLine(ep.Reward.Components))
	fmt.Fprintf(w, "  cost $%.4f (repriced %.0f ITE under %q), %d requests, %.1fs\n", ep.Cost.USD, ep.Cost.ITE, ep.Cost.Target, ep.Cost.Requests, float64(ep.Cost.WallMs)/1000)
	if len(ep.Flags) > 0 {
		fmt.Fprintf(w, "  flags: %s\n", strings.Join(ep.Flags, " "))
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tROLE\tSTATUS\tSTEPS\tSEGMENTS\tTRAINABLE\tREWARD\tIN\tCACHED\tOUT")
	for _, a := range ep.Agents {
		var in, cached, out, train int
		for _, s := range a.Steps {
			in += s.Usage.InputTokens
			cached += s.Usage.CacheReadTokens
			out += s.Usage.OutputTokens
			if s.Trainable {
				train++
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%.3f\t%d\t%d\t%d\n", a.ID, a.Role, a.Status, len(a.Steps), len(a.Segments), train, a.Reward.Total, in, cached, out)
	}
	tw.Flush()
	if len(ep.Signals) > 0 {
		fmt.Fprintf(w, "\nsignals: %s\n", floatLine(ep.Signals))
	}
	for _, n := range ep.Reward.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	return nil
}

func componentLine(m map[string]float64) string {
	if len(m) == 0 {
		return ""
	}
	return "(" + floatLine(m) + ")"
}

func floatLine(m map[string]float64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%.3g", k, m[k])
	}
	return strings.Join(parts, " ")
}
