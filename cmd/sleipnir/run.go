package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
)

func init() {
	extraCommands["run"] = cmdRun
	extraCommands["swarm"] = func(ctx context.Context, args []string) error {
		return cmdRun(ctx, append([]string{"--swarm"}, args...))
	}
	extraCommands["recon"] = cmdRecon
}

type kvFlags map[string]string

func (k kvFlags) String() string { return fmt.Sprint(map[string]string(k)) }
func (k kvFlags) Set(v string) error {
	name, val, ok := strings.Cut(v, "=")
	if !ok || name == "" || val == "" {
		return fmt.Errorf("want role=model, got %q", v)
	}
	k[name] = val
	return nil
}

// cmdRun runs one goal through the harness: a single agent, or with --swarm a
// manager and workers.
func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	model := fs.String("model", "", "model: provider/model or a bare id for the default provider (default: config models.default)")
	cwd := fs.String("cwd", "", "working directory (default: current)")
	mode := fs.String("mode", "", "permissions: default | accept-edits | plan | bypass (default: config, then default)")
	swarmN := fs.Int("swarm", 0, "run a manager with up to N workers instead of a single agent")
	maxSteps := fs.Int("max-steps", 0, "step limit for a single agent (default 200)")
	budget := fs.Float64("budget-usd", 0, "stop when spend reaches this many US dollars")
	verify := fs.String("verify", "", "swarm: command the harness runs before a worker's task may leave 'doing' (with --isolation worktree, also on every merge)")
	isolation, commit := isolationFlags(fs)
	mailman := mailmanFlag(fs)
	asJSON := fs.Bool("json", false, "stream events as JSON lines on stdout")
	quiet := fs.Bool("quiet", false, "print only the final answer")
	verbose := fs.Bool("verbose", false, "print notices and tool errors")
	noRecon := fs.Bool("no-recon", false, "do not survey the project into the shared prompt layer")
	trust := fs.Bool("trust-project", false, "load project instruction files and project-level config (unsafe for untrusted repositories)")
	dir := fs.String("session-dir", "", "where to write events.jsonl and blobs/ (default ~/.sleipnir/sessions/<id>)")
	ctxWin := fs.Int("context-window", 0, "override the model's context window (small values force frequent compaction)")
	capture := fs.Bool("capture", false, "ask the endpoint for token ids and logprobs (self-hosted policy servers; RL data)")
	noWeb := fs.Bool("no-web", false, "disable the web tools")
	noMCP := fs.Bool("no-mcp", false, "start no MCP tool servers")
	resume := resumeFlags(fs)
	roleModels := kvFlags{}
	fs.Var(roleModels, "role-model", "role=model override, repeatable (e.g. manager=heimdall/x)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: sleipnir run [flags] <prompt | ->\n\nRuns one goal through the harness. The prompt may be '-' to read stdin.\n\nflags:\n")
		fs.PrintDefaults()
	}
	words, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	spec, err := resume()
	if err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(words, " "))
	if prompt == "-" || (prompt == "" && !term.IsTerminal(int(os.Stdin.Fd()))) {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if err != nil {
			return err
		}
		prompt = strings.TrimSpace(string(b))
	}
	if prompt == "" {
		fs.Usage()
		return errors.New("run: a prompt is required")
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	o := session.Options{
		Cwd: *cwd, Model: *model, Mode: perm.Mode(*mode), Swarm: *swarmN > 0, MaxAgents: *swarmN + 1,
		MaxSteps: *maxSteps, BudgetUSD: *budget, Verify: *verify, NoRecon: *noRecon, TrustProject: *trust,
		Dir: *dir, ContextWindow: *ctxWin, CaptureTokens: *capture, NoWeb: *noWeb, RoleModels: roleModels,
		Resume: spec, NoMCP: *noMCP, Isolation: *isolation, Commit: *commit, Mailman: mailman(),
	}
	if interactive {
		o.Prompter = session.TerminalPrompter(os.Stdin, os.Stderr)
	}
	var sink agent.Sink
	mainAgent := "main"
	if o.Swarm {
		mainAgent = "mgr"
	}
	switch {
	case *asJSON:
		sink = session.NewJSONSink(os.Stdout)
	case *quiet:
		sink = agent.NopSink{}
	default:
		sink = session.NewTextSink(os.Stdout, os.Stderr, mainAgent, *verbose)
	}
	o.Sink = sink
	if o.Swarm {
		o.NewSink = func(string) agent.Sink { return sink }
	}
	session.Version = version

	s, err := session.New(ctx, o)
	if err != nil {
		return err
	}
	defer s.Close()
	if !*quiet && !*asJSON {
		fmt.Fprintf(os.Stderr, "sleipnir %s · %s · session %s\n", version, s.Model.ID, s.ID)
	}
	start := time.Now()
	res, err := s.Run(ctx, prompt)
	// An isolated run ends here: the verified result goes into the checkout and the
	// trees are removed (a no-op, and nil, for a shared-tree run). Ctrl-C does not
	// skip it: work that passed verification is not thrown away.
	integ := finishRun(ctx, s)
	if res != nil {
		switch {
		case *asJSON:
			out := map[string]any{
				"type": "result", "text": res.Text, "steps": res.Steps, "cost_usd": res.CostUSD, "usage": res.Usage,
				"hit_ratio": res.Usage.HitRatio(), "compactions": res.Compactions, "stop": res.Stop, "session": res.SessionID,
				"dir": res.Dir, "elapsed_ms": time.Since(start).Milliseconds(), "error": errString(err),
			}
			if integ != nil {
				out["integration"] = integ
			}
			_ = json.NewEncoder(os.Stdout).Encode(out)
		case !*quiet:
			fmt.Fprintf(os.Stderr, "\n── %s · %d steps · $%.4f · cache hit %.0f%% · %d compactions · %s\n",
				time.Since(start).Round(time.Second), res.Steps, res.CostUSD, res.Usage.HitRatio()*100, res.Compactions, res.Dir)
		}
	}
	if !*asJSON {
		printIntegration(os.Stderr, integ, *quiet)
	}
	if err != nil && errors.Is(err, agent.ErrBudget) {
		return fmt.Errorf("stopped: budget exhausted")
	}
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isolationFlags registers --isolation and --commit (run, swarm and chat share them).
func isolationFlags(fs *flag.FlagSet) (isolation *string, commit *bool) {
	isolation = fs.String("isolation", "", "swarm: none | worktree (default: config swarm.isolation). worktree gives every writer a git worktree of its own; finished work is merged and verified through a queue and applied to your checkout at the end")
	commit = fs.Bool("commit", false, "swarm with --isolation worktree: commit the verified result onto your branch instead of leaving uncommitted edits (needs a clean checkout on a branch)")
	return isolation, commit
}

// triBool is a boolean flag that also knows whether it was given: --mailman turns the
// mailman on, --mailman=false turns it off for one run whatever the configuration says,
// and leaving it out leaves the configuration in charge.
type triBool struct{ set, val bool }

func (t *triBool) String() string {
	if t == nil || !t.set {
		return ""
	}
	return strconv.FormatBool(t.val)
}

func (t *triBool) Set(v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	t.set, t.val = true, b
	return nil
}

func (t *triBool) IsBoolFlag() bool { return true }

// mailmanFlag registers --mailman (run, swarm and chat share it) and returns what to put
// in session.Options.Mailman: nil when the flag was not given.
func mailmanFlag(fs *flag.FlagSet) func() *bool {
	var t triBool
	fs.Var(&t, "mailman", "swarm: route worker mail through a mailman agent that digests bursts (default: config swarm.mailman; --mailman=false turns it off for this run). Its model: --role-model mailman=<model>")
	return func() *bool {
		if !t.set {
			return nil
		}
		v := t.val
		return &v
	}
}

// finishRun ends an isolated run (see session.Session.Finish) even when the run was
// cancelled: what passed verification is applied, not thrown away. It returns nil for
// a session that is not an isolated swarm.
func finishRun(ctx context.Context, s *session.Session) *swarm.IntegrationReport {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	return s.Finish(fctx)
}

// printIntegration says what became of an isolated run's result. What reached the
// checkout is quiet-able; a result that did not is always said, with the one command
// that gets it.
func printIntegration(w io.Writer, rep *swarm.IntegrationReport, quiet bool) {
	if rep == nil || (quiet && rep.Applied && len(rep.Kept) == 0) {
		return
	}
	fmt.Fprintf(w, "integration: %s\n", rep.Message)
}

// cmdRecon prints the deterministic project survey that seeds the shared prompt
// layer, so its size and content can be inspected before a run.
func cmdRecon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recon", flag.ExitOnError)
	budget := fs.Int("budget", 5000, "token budget for the survey")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := "."
	if fs.NArg() > 0 {
		root = fs.Arg(0)
	}
	r, err := session.BuildRecon(ctx, session.ReconOptions{Root: root, BudgetTokens: *budget})
	if err != nil {
		return err
	}
	for _, s := range r.Segments {
		fmt.Printf("## %s\n%s\n", s.Key, s.Text)
	}
	fmt.Fprintf(os.Stderr, "[%d tokens, %d files considered]\n", r.Tokens, r.Files)
	return nil
}
