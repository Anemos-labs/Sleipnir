package main

import (
	"bufio"
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
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// init registers single-agent runs, swarm runs, and repository reconnaissance.
func init() {
	extraCommands["run"] = cmdRun
	extraCommands["swarm"] = cmdSwarm
	extraCommands["recon"] = cmdRecon
}

// cmdSwarm is `run --swarm N`: the first argument is the number of workers; the manager comes on top.
func cmdSwarm(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help" {
		return runCommand(ctx, "swarm", []string{"-h"}) // the flags of run, under swarm's own heading
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return fmt.Errorf("swarm: the first argument is the number of workers, the manager not counted (8 or more: one of each role), got %q; for example: sleipnir swarm 8 \"add a login page\" --verify \"go test ./...\"", args[0])
	}
	return runCommand(ctx, "swarm", append([]string{"--swarm", args[0]}, args[1:]...))
}

type kvFlags map[string]string

// String formats the accumulated key/value overrides as a Go map for flag display.
func (k kvFlags) String() string { return fmt.Sprint(map[string]string(k)) }

// Set stores a nonempty role=model pair in an initialized map, replacing any prior value for the
// role.
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
func cmdRun(ctx context.Context, args []string) error { return runCommand(ctx, "run", args) }

// runCommand is cmdRun under the name the person typed (run, or swarm for the
// shorthand): the name shows in the usage.
func runCommand(ctx context.Context, name string, args []string) error {
	fs := newFlagSet(name, flag.ExitOnError)
	model := fs.String("model", "", "model: provider/model or a bare id for the default provider (default: config models.default)")
	cwd := fs.String("cwd", "", "working directory (default: current)")
	mode := fs.String("mode", "", "permissions: default | accept-edits | plan | bypass | yolo (default: config, then default)")
	autoFix := fs.Bool("auto-fix", false, "repair CI and PR review findings for this objective")
	autoMerge := fs.Bool("auto-merge", false, "merge this objective PR after checks and repository requirements pass")
	swarmN := fs.Int("swarm", 0, "run a manager and N workers instead of a single agent (config swarm.max_workers is the ceiling)")
	maxSteps := fs.Int("max-steps", 0, "step cap of a single agent's run, which also keeps team roles at theirs (unset: windows of 200 steps that go on while the agent keeps editing)")
	budget := fs.Float64("budget-usd", 0, "stop when spend reaches this many US dollars")
	verify := fs.String("verify", "", "swarm: command the harness runs before a worker's task may leave 'doing' (with --isolation worktree, also on every merge). {dirs} in it stands for the directories the task may touch (./... without a scope), so that each task is verified on its own work: 'go test {dirs}'")
	isolation, commit := isolationFlags(fs)
	mailman := mailmanFlag(fs)
	asJSON := fs.Bool("json", false, "stream events as JSON lines on stdout")
	quiet := fs.Bool("quiet", false, "print only the final answer")
	verbose := fs.Bool("verbose", false, "print notices and tool errors")
	noRecon := fs.Bool("no-recon", false, "do not survey the project into the shared prompt layer")
	trust := fs.Bool("trust-project", false, trustProjectHelp)
	dir := fs.String("session-dir", "", "directory for this run's recording (events.jsonl, blobs/, checkpoints/); default <state>/sessions/<id>, <state> being $SLEIPNIR_HOME or ~/.sleipnir")
	ctxWin := fs.Int("context-window", 0, "override the model's context window (small values force frequent compaction)")
	capture := fs.Bool("capture", false, "ask the endpoint for token ids and logprobs (self-hosted policy servers; RL data)")
	noWeb := fs.Bool("no-web", false, "disable the web tools")
	noMCP := fs.Bool("no-mcp", false, "start no MCP tool servers")
	mcpConfigs := mcpConfigFlags(fs)
	noMemory := fs.Bool("no-memory", false, noMemoryHelp)
	resume := resumeFlags(fs)
	roleFlag := &roleModelFlag{}
	fs.Var(roleFlag, "role-model", roleModelHelp)
	fs.Var(&teamFlag{roles: roleFlag, cwd: cwd}, "team", teamHelp)
	autoDocs := autoDocsFlag(fs)
	managerApproves := managerApprovesFlag(fs)
	allow := allowFlags(fs)
	askTimeout := fs.Duration("ask-timeout", 0, "refuse a question to the person that nobody answers within this time, and tell the worker (default: wait for the person); for a run that is left alone")
	fs.Usage = func() {
		if name == "swarm" {
			printHelp(os.Stderr, "usage: sleipnir swarm <workers> [flags] <prompt | ->\n\nRuns one goal with a team: a manager and up to <workers> workers (the same as run --swarm <workers>).\nThe prompt may be '-' to read stdin.\n\nflags:\n")
		} else {
			printHelp(os.Stderr, "usage: sleipnir run [flags] <prompt | ->\n\nRuns one goal through the harness. The prompt may be '-' to read stdin.\n\nflags:\n")
		}
		printFlags(fs)
	}
	words, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	spec, err := resume()
	if err != nil {
		return err
	}
	prompt, note, err := readGoal(words, os.Stdin)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if note != "" {
		fmt.Fprintln(os.Stderr, "sleipnir:", note)
	}
	if prompt == "" {
		eg := `sleipnir run "fix the failing test"`
		if name == "swarm" {
			eg = `sleipnir swarm 8 "fix the failing test"`
		}
		return usageError(fs, name+`: a prompt is required, as in `+eg)
	}

	if *askTimeout < 0 {
		return fmt.Errorf("%s: --ask-timeout must not be negative", name)
	}
	if *noMCP && len(*mcpConfigs) > 0 {
		return usageError(fs, name+": --mcp-config names servers that --no-mcp would not start; give one of them")
	}
	mcpServers, err := mcpConfigs.servers()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	roleModels, teamSlots, err := roleFlag.split()
	if err != nil {
		return usageError(fs, name+": "+err.Error())
	}
	swarmGiven := name == "swarm"
	fs.Visit(func(f *flag.Flag) { swarmGiven = swarmGiven || f.Name == "swarm" })
	if *swarmN, err = teamWorkers(teamSlots, *swarmN, swarmGiven); err != nil {
		return usageError(fs, name+": "+err.Error())
	}
	if err := checkTeamSize(*swarmN); err != nil {
		return usageError(fs, name+": "+err.Error())
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	team, workers := teamOf(*swarmN)
	o := session.Options{AutoFix: *autoFix, AutoMerge: *autoMerge,
		AskTimeout: *askTimeout,
		Cwd:        *cwd, Model: *model, Mode: perm.Mode(*mode), Swarm: team, Workers: workers,
		MaxSteps: *maxSteps, FixedSteps: *maxSteps > 0, BudgetUSD: *budget, Verify: *verify, NoRecon: *noRecon, TrustProject: *trust,
		Dir: *dir, ContextWindow: *ctxWin, CaptureTokens: *capture, NoWeb: *noWeb, RoleModels: roleModels,
		Resume: spec, NoMCP: *noMCP, MCPServers: mcpServers, NoMemory: *noMemory, Isolation: *isolation, Commit: *commit, Mailman: mailman(), Allow: expandAllow(*allow),
		AutoDocs: autoDocs(), ManagerApproves: managerApproves(), Team: teamSlots, DefaultTeam: true,
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
		fmt.Fprintln(os.Stderr, wrapFor(os.Stderr, fmt.Sprintf("sleipnir %s · %s%s · session %s", version, s.Model.ID, budgetLabel(s), s.ID)))
		if !interactive {
			if n := noOneToAskNote(perm.Mode(*mode), *allow); n != "" {
				fmt.Fprintln(os.Stderr, wrapFor(os.Stderr, n))
			}
		}
	}
	start := time.Now()
	res, err := s.Run(ctx, prompt)
	s.SetEndReason(endReasonOf(ctx, err))
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
			if res.Unfinished != "" {
				out["unfinished"] = res.Unfinished
			}
			if res.Plan {
				// a subscription plan's session: cost_usd is what was billed (nothing per token), list_usd its usage at the API list price
				out["list_usd"], out["plan"] = res.ListUSD, true
			}
			if ref := s.RefusedWithNoOneToAsk(); len(ref) > 0 {
				out["refused_no_one_to_ask"] = ref
			}
			if integ != nil {
				out["integration"] = integ
			}
			_ = json.NewEncoder(os.Stdout).Encode(out)
		case !*quiet:
			fmt.Fprintln(os.Stderr, "\n"+runSummary(os.Stderr, time.Since(start), res))
			if s.Ckpt != nil {
				fmt.Fprintln(os.Stderr, changedLine(s.Ckpt.List()))
			}
			if s.Swarm != nil {
				if l := verifyLine(*verify, s.Swarm.VerifyRuns); l != "" {
					fmt.Fprintln(os.Stderr, l)
				}
			}
			printRefusals(os.Stderr, s.RefusedWithNoOneToAsk(), s.Cwd())
		}
	}
	if !*asJSON {
		printIntegration(os.Stderr, integ, *quiet)
	}
	if *quiet && res != nil {
		// --quiet prints only the final answer: the sink shows nothing, so the answer is written here
		if ans := strings.TrimRight(res.Text, "\n"); strings.TrimSpace(ans) != "" {
			fmt.Fprintln(os.Stdout, ans)
		}
	}
	if err != nil && errors.Is(err, agent.ErrBudget) {
		return budgetStopped(s, res)
	}
	if err == nil && res != nil && res.Unfinished != "" {
		return unfinishedError(res.Unfinished)
	}
	return err
}

// unfinishedError is the error of a swarm that stopped with work left undone: status 3 (docs/CLI.md), so that a script can tell a team
// that did what it was asked from one that stopped. What was done is in place; the message says what was not.
func unfinishedError(left string) error {
	return &exitError{code: exitUnfinished, err: fmt.Errorf("the team stopped with unfinished work (%s); what was done is in place, see the session's board", left)}
}

// maxPromptInput bounds what is read from standard input as part of a goal.
const maxPromptInput = 8 << 20

// stdinGrace is how long a goal given in words waits for piped data to begin arriving. A pipe that nobody writes to and nobody
// closes (a parent process that spawned us with an open stdin and forgot it) would otherwise hold the run for ever; a command that
// takes longer than this to print its first byte is told so, and the run goes on without it.
var stdinGrace = 3 * time.Second

// readGoal is the goal of a run: the words on the command line, what is piped in, or both. A lone "-" stands for the piped input
// (as the last word it means "the input goes with these words"). With words and input both, the input comes first in a block of
// its own and the words last: the instruction is read after the material it is about. Input that is not data (a terminal, /dev/null,
// a socket) is not input; with no words at all anything that is not a terminal is read, as it always was. note is something the
// person should be told (input that never came).
func readGoal(words []string, stdin *os.File) (goal, note string, err error) {
	explicit := false // a "-" asked for the input: it is waited for
	if n := len(words); n > 1 && words[n-1] == "-" {
		words, explicit = words[:n-1], true
	}
	prompt := strings.TrimSpace(strings.Join(words, " "))
	if prompt == "-" {
		prompt, explicit = "", true
	}
	if prompt != "" && !explicit && !stdinHasData(stdin) {
		return prompt, "", nil
	}
	if prompt == "" && !explicit && term.IsTerminal(int(stdin.Fd())) {
		return "", "", nil
	}
	grace := stdinGrace
	if prompt == "" || explicit {
		grace = 0 // the input is all there is to go on: wait for it
	}
	b, arrived, err := readPipedInput(stdin, maxPromptInput, grace)
	if err != nil {
		return "", "", err
	}
	if !arrived {
		return prompt, fmt.Sprintf("no data came on standard input within %s; going on with the prompt alone (redirect < /dev/null to skip this wait, or pipe from a command that prints sooner, or say - to wait for it)", grace), nil
	}
	input := strings.TrimSpace(string(b))
	switch {
	case prompt == "":
		return input, "", nil
	case input == "":
		return prompt, "", nil
	}
	return "<stdin>\n" + input + "\n</stdin>\n\n" + prompt, "", nil
}

// readPipedInput reads r to its end, at most limit bytes (more is an error). With a grace period it gives up, and says so (arrived is false),
// if the first byte has not come by then; once data has begun it waits for the rest as long as it takes.
func readPipedInput(r io.Reader, limit int, grace time.Duration) (b []byte, arrived bool, err error) {
	br := bufio.NewReader(io.LimitReader(r, int64(limit)+1))
	if grace > 0 {
		first := make(chan error, 1)
		go func() { _, e := br.Peek(1); first <- e }()
		select {
		case e := <-first:
			if e != nil && e != io.EOF {
				return nil, true, e
			}
		case <-time.After(grace):
			return nil, false, nil // the goroutine stays with the pipe until the process ends
		}
	}
	b, err = io.ReadAll(br)
	if err != nil {
		return nil, true, err
	}
	if len(b) > limit {
		return nil, true, fmt.Errorf("standard input is larger than %d MiB: name the file in the prompt instead", limit>>20)
	}
	return b, true, nil
}

// stdinHasData reports whether f is something a person piped data into: a pipe or a file, not a terminal, /dev/null or a socket.
func stdinHasData(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && (fi.Mode()&os.ModeNamedPipe != 0 || fi.Mode().IsRegular())
}

// budgetLabel is " · budget $N" for a session that has one: a cap that will stop a run
// should be visible when the run starts (a swarm's is on by default).
func budgetLabel(s *session.Session) string {
	if b := s.Budget(); b > 0 {
		return " · budget " + cost.Dollars(b)
	}
	return ""
}

// budgetStopped is the error of a run that hit its budget: what was spent, the cap, and
// where it comes from (the flag, and for a swarm the configuration and its default).
func budgetStopped(s *session.Session, res *session.Result) error {
	spent := ""
	if res != nil {
		spent = " (" + cost.Dollars(res.CostUSD) + " spent)"
	}
	hint := "raise it with --budget-usd"
	if s.Swarm != nil {
		hint = "raise it with --budget-usd or swarm.budget_usd (0 removes the cap)"
	}
	return fmt.Errorf("stopped: the budget of %s is exhausted%s; %s", cost.Dollars(s.Budget()), spent, hint)
}

// endReasonOf is the SessionEnd reason of a run that returned err: how it ended, for
// hooks that care whether a run was finished, interrupted, out of budget or broken.
func endReasonOf(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return session.EndCompleted
	case errors.Is(err, agent.ErrBudget):
		return session.EndBudget
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return session.EndInterrupted
	}
	return session.EndError
}

// errString returns an empty string for nil and the error message otherwise.
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

// String distinguishes an unset or nil tri-state flag from explicitly true or false values.
func (t *triBool) String() string {
	if t == nil || !t.set {
		return ""
	}
	return strconv.FormatBool(t.val)
}

// Set parses a boolean and marks the flag explicitly set only when parsing succeeds.
func (t *triBool) Set(v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	t.set, t.val = true, b
	return nil
}

// IsBoolFlag permits the flag parser to accept the flag without an explicit value.
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

// autoDocsFlag registers --auto-docs, a three-state flag: unset leaves swarm.auto_docs (default true) in charge.
func autoDocsFlag(fs *flag.FlagSet) func() *bool {
	var t triBool
	fs.Var(&t, "auto-docs", "swarm: the team's docs worker keeps README, docs/ and AGENTS.md current after every accepted task without being asked (default: config swarm.auto_docs, true; --auto-docs=false makes docs a role the manager assigns)")
	return func() *bool {
		if !t.set {
			return nil
		}
		v := t.val
		return &v
	}
}

// managerApprovesFlag registers --manager-approves, a three-state flag: unset leaves permissions.manager_approves (default true) in
// charge.
func managerApprovesFlag(fs *flag.FlagSet) func() *bool {
	var t triBool
	fs.Var(&t, "manager-approves", "swarm: a worker's routine permission question (a command inside its own tree that no rule covers, nothing protected, no network but this machine, or an install into a virtual environment of the workspace from the package index) goes to the manager before you; unanswered in a minute it comes to you (default: config permissions.manager_approves, true; --manager-approves=false asks you directly)")
	return func() *bool {
		if !t.set {
			return nil
		}
		v := t.val
		return &v
	}
}

// noMemoryHelp is the help of --no-memory.
const noMemoryHelp = "start without the global memory: the notes in ~/.sleipnir/MEMORY.md and the lesson memory are neither shown to the agents nor saved"

// roleModelHelp is the help of --role-model.
const roleModelHelp = "role=model, repeatable: the model a role runs on (manager=heimdallm/x, mailman=heimdallm/small, compactor=…, judge=…, embedder=… turns the lesson memory on, reranker=… orders what it finds). Give a worker role more than once, or alone, and the values are the team instead: one worker slot each, e.g. --role-model backend --role-model backend=heimdallm/x --role-model tester … (at least 8; the default team is one worker of each role)"

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
	fs := newFlagSet("recon", flag.ExitOnError)
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

// spentText is a cost as the line chat, `run` and the session lists write it: "$0.0123", or for a session on a subscription plan (a
// ChatGPT sign-in) the bill plus what the plan covered at the API list price, "≈$0.0123 plan" (state.PlanHelp says what that is).
func spentText(billed, list float64, plan bool) string {
	if plan {
		return fmt.Sprintf("≈$%.4f plan", billed+list)
	}
	return fmt.Sprintf("$%.4f", billed)
}

// runSummary is the line a run ends on: how long, how many steps, what it cost, and where the session is kept. On a terminal too narrow for all
// of it on one line, the directory (the long part) is on a line of its own, under the rest, shortened with ~ where it can be.
func runSummary(out io.Writer, took time.Duration, res *session.Result) string {
	line := fmt.Sprintf("── %s · %d steps · %s · cache hit %.0f%% · %d compactions", took.Round(time.Second), res.Steps, spentText(res.CostUSD, res.ListUSD, res.Plan), res.Usage.HitRatio()*100, res.Compactions)
	if w := termWidth(out); w > 0 && utf8.RuneCountInString(line)+3+utf8.RuneCountInString(res.Dir) >= w {
		return line + "\n   " + tildePath(res.Dir)
	}
	return line + " · " + res.Dir
}

// verifyLine says, after a swarm run that had a --verify command, that the harness ran it and what came of it: a person otherwise cannot tell
// the gate from the workers' own test runs. It is empty without a command, and says so when the command never ran.
func verifyLine(cmd string, runs func() (ran, failed int)) string {
	if cmd == "" {
		return ""
	}
	ran, failed := runs()
	switch {
	case ran == 0:
		return "   verify `" + cmd + "`: the gate never ran (no task reached done)"
	case failed == 0:
		return fmt.Sprintf("   verify `%s`: the gate ran %d times and passed every time", cmd, ran)
	}
	return fmt.Sprintf("   verify `%s`: the gate ran %d times; %d failed and the work was sent back", cmd, ran, failed)
}

// changedLine is the line after a run's summary that says which files it changed (the checkpoints of the session know), or that it changed none:
// a run that was refused its edits ends with a diagnosis, and a person reading the summary alone could not tell.
func changedLine(list []checkpoint.Info) string {
	seen := map[string]bool{}
	var files []string
	for _, cp := range list {
		for _, f := range cp.Files {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	switch n := len(files); {
	case n == 0:
		return "   no file was changed"
	case n <= 5:
		return "   changed: " + tools.SanitizeForTerminal(strings.Join(files, ", "))
	default:
		return fmt.Sprintf("   changed: %s and %d more", tools.SanitizeForTerminal(strings.Join(files[:5], ", ")), n-5)
	}
}
