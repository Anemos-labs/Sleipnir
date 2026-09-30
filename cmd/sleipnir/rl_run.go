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
	"time"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/env"
	"github.com/reee344/sleipnir/internal/rl/harness"
	"github.com/reee344/sleipnir/internal/rl/reward"
	"github.com/reee344/sleipnir/internal/session"
)

func init() {
	rlCommands["rollout"] = rlRollout
	rlCommands["eval"] = rlEval
	rlCommands["serve"] = rlServe
}

// ---- shared flags ----

// policyFlags name the model being trained or evaluated and how it is sampled.
type policyFlags struct {
	model, baseURL, keyEnv, sampling string
	temperature, topP                float64
	maxTokens                        int
	capture                          bool
	swarm                            int
	roleModels                       kvFlags
	seed                             int64
	ctxTokens                        int
}

func (p *policyFlags) register(fs *flag.FlagSet) {
	p.roleModels = kvFlags{}
	fs.StringVar(&p.model, "model", "", "the policy: provider/model or a bare id for the default provider (default: config models.default)")
	fs.StringVar(&p.baseURL, "base-url", "", "policy endpoint base URL (default: the provider's)")
	fs.StringVar(&p.keyEnv, "api-key-env", "", "name of the environment variable holding the policy's API key (default: the provider's)")
	fs.StringVar(&p.sampling, "sampling", "", `sampling parameters as a JSON object, e.g. {"temperature":1,"top_p":0.95,"max_tokens":4096}`)
	fs.Float64Var(&p.temperature, "temperature", 0, "sampling temperature (overrides --sampling)")
	fs.Float64Var(&p.topP, "top-p", 0, "nucleus sampling (overrides --sampling)")
	fs.IntVar(&p.maxTokens, "max-tokens", 0, "completion limit per request (overrides --sampling)")
	fs.BoolVar(&p.capture, "capture", false, "ask the endpoint for token ids and logprobs (needed for the tokens export; self-hosted vLLM/SGLang-style servers)")
	fs.IntVar(&p.swarm, "swarm", 0, "run a manager with up to N workers instead of a single agent (default: what the task's team says)")
	fs.Var(p.roleModels, "role-model", "role=model override, repeatable (e.g. compactor=heimdall/deepseek/deepseek-v4-flash)")
	fs.Int64Var(&p.seed, "seed", 0, "run seed; each rollout's sampling seed derives from it")
	fs.IntVar(&p.ctxTokens, "context-tokens", 0, "the policy's context window when a task does not set one")
}

// resolve turns the flags into the policy spec and a harness for it.
func (p *policyFlags) resolve(fs *flag.FlagSet) (env.PolicySpec, *harness.Harness, error) {
	wd, _ := os.Getwd()
	root, _ := config.FindRoot(wd)
	home, _ := os.UserHomeDir()
	cfg, _, err := config.Load(config.LoadOpts{Cwd: wd, Root: root, Home: home, UntrustedProject: true})
	if err != nil {
		return env.PolicySpec{}, nil, fmt.Errorf("config: %w", err)
	}
	var ref session.ModelRef
	var prov config.Provider
	if p.baseURL != "" && p.model != "" && !knownProviderPrefix(cfg, p.model) {
		// A self-hosted policy server needs no provider entry: the endpoint and the
		// model id are all there is to say.
		ref = session.ModelRef{Provider: "policy", Model: p.model}
		prov = config.Provider{Dialect: config.DialectOpenAIChat, BaseURL: p.baseURL, APIKeyEnv: p.keyEnv}
	} else {
		var err error
		if ref, err = session.ResolveModel(cfg, p.model); err != nil {
			return env.PolicySpec{}, nil, err
		}
		var ok bool
		if prov, ok = session.LookupProvider(cfg, ref.Provider); !ok {
			return env.PolicySpec{}, nil, fmt.Errorf("unknown provider %q", ref.Provider)
		}
	}
	if d := prov.EffectiveDialect(); d != config.DialectOpenAIChat {
		return env.PolicySpec{}, nil, fmt.Errorf("provider %q speaks %s: RL policies are served through chat completions (vLLM, SGLang, or a gateway's chat route)", ref.Provider, d)
	}
	base, keyEnv := prov.BaseURL, prov.APIKeyEnv
	if b, k, ok := session.ProviderInfo(cfg, ref.Provider); ok {
		base, keyEnv = b, k
	}
	if p.baseURL != "" {
		base = p.baseURL
	}
	if p.keyEnv != "" {
		keyEnv = p.keyEnv
	}
	sampling := map[string]any{}
	if p.sampling != "" {
		if err := json.Unmarshal([]byte(p.sampling), &sampling); err != nil {
			return env.PolicySpec{}, nil, fmt.Errorf("--sampling: %w", err)
		}
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "temperature":
			sampling["temperature"] = p.temperature
		case "top-p":
			sampling["top_p"] = p.topP
		case "max-tokens":
			sampling["max_tokens"] = p.maxTokens
		}
	})
	spec := env.PolicySpec{Model: ref.Model, BaseURL: base, APIKeyEnv: keyEnv}
	if len(sampling) > 0 {
		if spec.Sampling, err = json.Marshal(sampling); err != nil {
			return env.PolicySpec{}, nil, err
		}
	}
	h := &harness.Harness{PolicyOptions: prov.Options, PolicyHeaders: prov.Headers, ContextTokens: p.ctxTokens}
	return spec, h, nil
}

// knownProviderPrefix reports whether ref starts with the name of a configured
// or built-in provider ("local/my-policy").
func knownProviderPrefix(cfg *config.Config, ref string) bool {
	i := strings.IndexByte(ref, '/')
	if i <= 0 {
		return false
	}
	_, ok := session.LookupProvider(cfg, ref[:i])
	return ok
}

// rigFlags configure the environment: where workspaces live, how rollouts are
// verified and scored, and how much runs at once.
type rigFlags struct {
	workDir, mode, blobs, rewards, target string
	concurrency, verifyRepeats            int
	verifyPolicy                          string
	infraRetries                          int
	maxWall                               time.Duration
	noNetIsolation, requireNetIsolation   bool
	keepFailed                            bool
	passEnv                               string
	setEnv                                kvFlags
}

func (r *rigFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&r.workDir, "work-dir", "", "where snapshots, workspaces and verification checkouts live (default: <state>/rl-work)")
	fs.StringVar(&r.mode, "workspace-mode", env.ModeExport, "export: a fresh single-commit tree without history | clone: a local clone with history")
	fs.StringVar(&r.blobs, "blobs", "", "blob store holding the tasks' hidden verifier files (default: blobs/ next to the tasks file)")
	fs.StringVar(&r.rewards, "rewards", "", "rewards.json with weights, caps and detectors (default: the documented defaults)")
	fs.StringVar(&r.target, "target-price", "", "price and cache model rollouts are repriced under (see: sleipnir rl reward -list-targets)")
	fs.IntVar(&r.concurrency, "concurrency", 1, "rollouts in flight")
	fs.IntVar(&r.verifyRepeats, "verify-repeats", 0, "run the verifier this many times to catch flaky tests")
	fs.StringVar(&r.verifyPolicy, "verify-policy", "", "with repeats: all | any | majority")
	fs.IntVar(&r.infraRetries, "infra-retries", 0, "retries of a rollout that failed for infrastructure reasons (default 2; -1 disables)")
	fs.DurationVar(&r.maxWall, "max-wall", 0, "wall-clock cap of a rollout whose task sets none (default 1h)")
	fs.BoolVar(&r.noNetIsolation, "no-net-isolation", false, "do not isolate the network of tasks that do not need it")
	fs.BoolVar(&r.requireNetIsolation, "require-net-isolation", false, "refuse to run where network isolation is unavailable")
	fs.BoolVar(&r.keepFailed, "keep-failed", false, "keep the workspaces of failed rollouts for debugging")
	fs.StringVar(&r.passEnv, "pass-env", "", "comma-separated environment variables (or globs) handed to the agent's and verifier's commands although they are not on the toolchain allowlist")
	r.setEnv = kvFlags{}
	fs.Var(r.setEnv, "set-env", "NAME=value forced into the agent's and verifier's environment, repeatable (e.g. GOCACHE=/shared/cache: faster, less isolated)")
}

// rig is a configured Runner and what closes it.
type rig struct {
	Runner *env.Runner
	Pipe   *harness.Pipeline
	Reward reward.Config
	closeF func()
}

func (r *rig) Close() { r.closeF() }

// workspaces builds the workspace manager the flags describe.
func (rf *rigFlags) workspaces(stderr io.Writer) (*env.Workspaces, error) {
	work := rf.workDir
	if work == "" {
		home, _ := os.UserHomeDir()
		work = filepath.Join(stateDir(home), "rl-work")
	}
	wd, _ := os.Getwd()
	ws, err := env.NewWorkspaces(env.WorkspaceOptions{
		Root: work, Mode: rf.mode, RepoBase: wd,
		DisableNetIsolation: rf.noNetIsolation, RequireNetIsolation: rf.requireNetIsolation,
		PassEnv: splitList(rf.passEnv), SetEnv: map[string]string(rf.setEnv),
		Logf: func(f string, a ...any) { fmt.Fprintf(stderr, "env: "+f+"\n", a...) },
	})
	if err != nil {
		return nil, err
	}
	for _, w := range ws.Warnings() {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	return ws, nil
}

func (rf *rigFlags) build(h env.Harness, out, tasksFile string, stderr io.Writer) (*rig, error) {
	cfg := reward.DefaultConfig()
	var err error
	if rf.rewards != "" {
		if cfg, err = reward.LoadConfig(rf.rewards); err != nil {
			return nil, err
		}
	}
	if rf.target != "" {
		if _, ok := reward.LookupTarget(rf.target); !ok {
			return nil, fmt.Errorf("unknown target price %q (see: sleipnir rl reward -list-targets)", rf.target)
		}
		cfg.TargetName, cfg.Target = rf.target, cost.Model{}
	}
	ws, err := rf.workspaces(stderr)
	if err != nil {
		return nil, err
	}
	blobsDir := rf.blobs
	if blobsDir == "" && tasksFile != "" {
		if d := filepath.Join(filepath.Dir(tasksFile), "blobs"); dirExists(d) {
			blobsDir = d
		}
	}
	var hidden events.Blobs
	if blobsDir != "" {
		if hidden, err = events.NewDirBlobs(blobsDir); err != nil {
			ws.Close()
			return nil, err
		}
	}
	pipe := &harness.Pipeline{Harness: rl.HarnessRef{Version: version, Commit: commit}, Reward: cfg}
	rn := &env.Runner{
		Harness: h, Extract: pipe.Extract, Score: pipe.Score, Workspaces: ws, Out: out,
		Concurrency: rf.concurrency, HiddenBlobs: hidden,
		VerifyRepeats: rf.verifyRepeats, VerifyPassPolicy: rf.verifyPolicy,
		InfraRetries: rf.infraRetries, MaxWall: rf.maxWall,
		Logf: func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) },
	}
	return &rig{Runner: rn, Pipe: pipe, Reward: cfg, closeF: ws.Close}, nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func stateDir(home string) string {
	if v := os.Getenv("SLEIPNIR_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".sleipnir")
}

// taskSelection are the flags that choose which tasks run.
type taskSelection struct {
	file      string
	tags, ids string
	n         int
	seed      int64
}

func (t *taskSelection) register(fs *flag.FlagSet) {
	fs.StringVar(&t.file, "tasks", "", "tasks file (JSON lines)")
	fs.StringVar(&t.tags, "tag", "", "comma-separated tags a task must carry (prefix a tag with ! to exclude it)")
	fs.StringVar(&t.ids, "id", "", "comma-separated task ids (path.Match wildcards allowed)")
	fs.IntVar(&t.n, "n", 0, "run a deterministic subset of this many tasks")
	fs.Int64Var(&t.seed, "task-seed", 0, "seed of the -n subset")
}

func (t *taskSelection) load() ([]rl.Task, error) {
	if t.file == "" {
		return nil, errors.New("--tasks is required")
	}
	tasks, err := env.LoadTasks(t.file)
	if err != nil {
		return nil, err
	}
	tasks = env.Filter(tasks, splitList(t.tags), splitList(t.ids), t.n, t.seed)
	if len(tasks) == 0 {
		return nil, errors.New("no tasks selected")
	}
	return tasks, nil
}

func progressPrinter(w io.Writer) func(env.Progress) {
	return func(p env.Progress) {
		switch p.Type {
		case "rollout.done":
			line := fmt.Sprintf("[%d/%d] %s/%d %s", p.Done, p.Total, p.Task, p.Sample, p.Status)
			if p.Pass != nil {
				line += fmt.Sprintf(" pass=%v", *p.Pass)
			}
			if p.Score != nil {
				line += fmt.Sprintf(" reward=%.3f", *p.Score)
			}
			if p.Error != "" {
				line += ": " + p.Error
			}
			fmt.Fprintln(w, line)
		case "rollout.retry":
			fmt.Fprintf(w, "%s/%d: retrying (attempt %d): %s\n", p.Task, p.Sample, p.Attempt, p.Error)
		}
	}
}

// ---- rl rollout ----

func rlRollout(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl rollout", stderr, "rl rollout --tasks tasks.jsonl --model MODEL --group G --out RUN_DIR [flags]")
	var pf policyFlags
	var rf rigFlags
	var ts taskSelection
	pf.register(fs)
	rf.register(fs)
	ts.register(fs)
	group := fs.Int("group", 4, "samples per task (the GRPO group size)")
	out := fs.String("out", "", "run directory (default: runs/<timestamp>); rerunning into it resumes")
	force := fs.Bool("force", false, "rerun rollouts that already have an episode")
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if *group < 1 {
		return errors.New("rl rollout: --group must be at least 1")
	}
	tasks, err := ts.load()
	if err != nil {
		return fmt.Errorf("rl rollout: %w", err)
	}
	if err := env.ValidateTasks(tasks); err != nil {
		return fmt.Errorf("rl rollout: %w", err)
	}
	pol, h, err := pf.resolve(fs)
	if err != nil {
		return fmt.Errorf("rl rollout: %w", err)
	}
	if *out == "" {
		*out = filepath.Join("runs", time.Now().UTC().Format("20060102-150405"))
	}
	rg, err := rf.build(h, *out, ts.file, stderr)
	if err != nil {
		return fmt.Errorf("rl rollout: %w", err)
	}
	defer rg.Close()
	rg.Runner.Progress = progressPrinter(stderr)
	fmt.Fprintf(stderr, "rolling out %d tasks x %d samples with %s (%s) into %s\n", len(tasks), *group, pol.Model, pol.BaseURL, *out)
	sum, err := rg.Runner.Rollout(ctx, tasks, *group, env.RolloutOpts{
		RunID: filepath.Base(*out), Policy: pol, Capture: pf.capture, Swarm: pf.swarm > 0, Agents: pf.swarm,
		RoleModels: pf.roleModels, TargetPrice: rf.target, Seed: pf.seed, Force: *force, KeepFailed: rf.keepFailed,
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
		return fmt.Errorf("rl rollout: %w", err)
	}
	fmt.Fprintf(stderr, "next: sleipnir rl export %s --format steps -o data.jsonl\n", *out)
	return nil
}

// ---- rl eval ----

func rlEval(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl eval", stderr, "rl eval --tasks holdout.jsonl --model MODEL [flags]")
	var pf policyFlags
	var rf rigFlags
	var ts taskSelection
	pf.register(fs)
	rf.register(fs)
	ts.register(fs)
	samples := fs.Int("samples", 1, "samples per task (pass^k needs k or more)")
	out := fs.String("out", "", "run directory (default: runs/eval-<timestamp>)")
	exclude := fs.String("exclude", "", "training task list (ids, one per line, or task JSONL): refuse to evaluate anything in it")
	baseline := fs.String("baseline", "", "an earlier report.json to compare against")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	tasks, err := ts.load()
	if err != nil {
		return fmt.Errorf("rl eval: %w", err)
	}
	pol, h, err := pf.resolve(fs)
	if err != nil {
		return fmt.Errorf("rl eval: %w", err)
	}
	if *out == "" {
		*out = filepath.Join("runs", "eval-"+time.Now().UTC().Format("20060102-150405"))
	}
	rg, err := rf.build(h, *out, ts.file, stderr)
	if err != nil {
		return fmt.Errorf("rl eval: %w", err)
	}
	defer rg.Close()
	rg.Runner.Progress = progressPrinter(stderr)
	rep, err := env.Eval(ctx, rg.Runner, tasks, env.EvalOptions{
		Samples: *samples, ExcludeFile: *exclude,
		Rollout: env.RolloutOpts{RunID: filepath.Base(*out), Policy: pol, Capture: pf.capture, Swarm: pf.swarm > 0, Agents: pf.swarm,
			RoleModels: pf.roleModels, TargetPrice: rf.target, Seed: pf.seed},
	})
	var ce *env.ContaminationError
	if errors.As(err, &ce) {
		return fmt.Errorf("rl eval: %d held-out tasks also appear in the training list (%s ...): evaluating on training data measures nothing", len(ce.IDs), strings.Join(ce.IDs[:min(3, len(ce.IDs))], ", "))
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", " ")
		_ = enc.Encode(rep)
	} else {
		printEvalReport(stdout, rep)
	}
	if *baseline != "" && err == nil {
		b, rerr := os.ReadFile(*baseline)
		if rerr != nil {
			return fmt.Errorf("rl eval: %w", rerr)
		}
		var prev env.Report
		if rerr := json.Unmarshal(b, &prev); rerr != nil {
			return fmt.Errorf("rl eval: %s: %w", *baseline, rerr)
		}
		printComparison(stdout, env.Compare(prev, rep))
	}
	if err != nil {
		return fmt.Errorf("rl eval: %w", err)
	}
	return nil
}

func printEvalReport(w io.Writer, r env.Report) {
	fmt.Fprintf(w, "eval %s of %s: %d tasks x %d samples; %d rollouts completed, %d infra failures\n", r.RunID, r.Model, r.Tasks, r.Samples, r.Completed, r.Infra)
	fmt.Fprintf(w, "  pass@1 %.1f%%   mean score %.3f   mean reward %.3f   hack rate %.1f%%   budget rate %.1f%%\n", 100*r.PassAt1, r.MeanScore, r.MeanReward, 100*r.HackRate, 100*r.BudgetRate)
	for _, k := range sortedKeys(r.PassHat) {
		fmt.Fprintf(w, "  pass^%d %.1f%%   pass@%d %.1f%%\n", k, 100*r.PassHat[k], k, 100*r.PassAt[k])
	}
	fmt.Fprintf(w, "  cost: median $%.4f (p90 $%.4f)   ITE median %.0f   requests median %.0f   steps median %.0f   wall median %.0fs\n",
		r.USD.Median, r.USD.P90, r.ITE.Median, r.Requests.Median, r.Steps.Median, r.WallMs.Median/1000)
	if len(r.ByTag) > 0 {
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TAG\tTASKS\tSAMPLES\tPASS@1\tMEAN USD\tHACK")
		tags := make([]string, 0, len(r.ByTag))
		for t := range r.ByTag {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		for _, t := range tags {
			s := r.ByTag[t]
			fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f%%\t$%.4f\t%.1f%%\n", t, s.Tasks, s.Samples, 100*s.PassAt1, s.MeanUSD, 100*s.Hack)
		}
		tw.Flush()
	}
	if len(r.ByRole) > 0 {
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ROLE\tEPISODES\tMEAN STEPS\tMEAN REWARD\tMEAN OUT TOKENS")
		roles := make([]string, 0, len(r.ByRole))
		for k := range r.ByRole {
			roles = append(roles, k)
		}
		sort.Strings(roles)
		for _, k := range roles {
			v := r.ByRole[k]
			fmt.Fprintf(tw, "%s\t%d\t%.1f\t%.3f\t%.0f\n", k, v.Episodes, v.MeanSteps, v.MeanReward, v.MeanOutputTokens)
		}
		tw.Flush()
	}
	if len(r.Dropped) > 0 {
		fmt.Fprintf(w, "dropped (no completed rollout): %s\n", strings.Join(r.Dropped, ", "))
	}
}

func printComparison(w io.Writer, c env.Comparison) {
	fmt.Fprintf(w, "\ncompared with the baseline %s (%d paired tasks, %.0f%% bootstrap intervals over tasks):\n", c.A, c.Paired, 100*c.Confidence)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "METRIC\tBASELINE\tNOW\tDELTA\tINTERVAL\tP\t")
	for _, m := range c.Metrics {
		sig := ""
		if m.Significant {
			sig = "*"
		}
		fmt.Fprintf(tw, "%s\t%.4g\t%.4g\t%+.4g\t[%+.3g, %+.3g]\t%.3f\t%s\n", m.Name, m.A, m.B, m.Delta, m.Low, m.High, m.P, sig)
	}
	tw.Flush()
	if len(c.OnlyA)+len(c.OnlyB) > 0 {
		fmt.Fprintf(w, "not compared: %d tasks only in the baseline, %d only in this run\n", len(c.OnlyA), len(c.OnlyB))
	}
}

func sortedKeys(m map[int]float64) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}

// ---- rl serve ----

func rlServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("rl serve", stderr, "rl serve --runs DIR [flags]")
	var rf rigFlags
	rf.register(fs)
	addr := fs.String("addr", "127.0.0.1:8090", "listen address")
	tokenEnv := fs.String("token-env", "", "name of the environment variable holding the bearer token clients must present (required off loopback)")
	runs := fs.String("runs", "runs", "directory holding one subdirectory per run")
	tasksFile := fs.String("tasks", "", "task registry addressed by {\"task\": \"<id>\"} requests")
	repoRoots := fs.String("repo-root", "", "comma-separated directories: inline tasks may only use repositories under them")
	rewardsDir := fs.String("rewards-dir", "", "directory of rewards files a request may name")
	maxRuns := fs.Int("max-runs", 0, "concurrent rollout requests (default 2)")
	maxGroup := fs.Int("max-group", 0, "largest group size of one request (default 64)")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	var tasks []rl.Task
	if *tasksFile != "" {
		var err error
		if tasks, err = env.LoadTasks(*tasksFile); err != nil {
			return fmt.Errorf("rl serve: %w", err)
		}
	}
	token := ""
	if *tokenEnv != "" {
		if token = os.Getenv(*tokenEnv); token == "" {
			return fmt.Errorf("rl serve: $%s is empty", *tokenEnv)
		}
	}
	// The policy comes with every request; the harness builds its client from it.
	rg, err := rf.build(&harness.Harness{}, *runs, *tasksFile, stderr)
	if err != nil {
		return fmt.Errorf("rl serve: %w", err)
	}
	defer rg.Close()
	fmt.Fprintf(stderr, "rollout server on %s (runs in %s)\n", *addr, *runs)
	err = env.Serve(ctx, *addr, rg.Runner, env.ServeOptions{
		Root: *runs, Token: token, Tasks: tasks, RepoRoots: splitList(*repoRoots), RewardsDir: *rewardsDir,
		MaxRuns: *maxRuns, MaxGroup: *maxGroup, Concurrency: rf.concurrency,
		NewScorer: func(path string) (env.Scorer, error) {
			cfg, err := reward.LoadConfig(path)
			if err != nil {
				return nil, err
			}
			return (&harness.Pipeline{Harness: rg.Pipe.Harness, Reward: cfg}).Score, nil
		},
		Logf: func(f string, a ...any) { fmt.Fprintf(stderr, f+"\n", a...) },
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
