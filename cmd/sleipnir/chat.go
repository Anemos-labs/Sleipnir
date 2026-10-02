package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/mcp"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/tui/app"
)

func init() {
	extraCommands["chat"] = cmdChat
	ownsInterrupt["chat"] = true
}

// quitHint is what a first Ctrl-C at the prompt says: the same line in the chat on a terminal and in this one.
const quitHint = app.QuitHint

// cmdChat is the interactive loop: type a goal, watch the agent work, steer with
// slash commands.
//
// Ctrl-C cancels what is running (a turn, and any question the turn is asking; a slash
// command; the start of the session) and nothing else. At the prompt a first Ctrl-C prints
// quitHint, and a second one within quitWindow, with nothing typed in between, quits. The
// handling is in one place, interrupts (chat_input.go), and main leaves SIGINT to it
// (ownsInterrupt): the process's context, which ends the session, is cancelled by SIGTERM only.
//
// The input is owned by one reader, stdinLines (chat_input.go): the prompt reads goals and an
// approval question reads its answer from it, and a line typed while a turn runs waits for the
// prompt instead of answering a question that comes later.
// defaultAgents is the size of the team of the chat on a terminal when --swarm is not given: eight agents in all, the manager included (the
// horse has eight legs). --swarm N is always a number of agents in all.
const defaultAgents = 8

// defaultTeam is the size of that team, kept under the ceiling the person's own settings put on a session (swarm.max_agents).
func defaultTeam() int {
	n := defaultAgents
	if cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true}); err == nil {
		if c := cfg.Swarm.MaxAgents; c > 0 && n > c {
			n = c
		}
	}
	return n
}

func cmdChat(ctx context.Context, args []string) error {
	fs := newFlagSet("chat", flag.ExitOnError)
	model := fs.String("model", "", "model: provider/model or a bare id for the default provider")
	cwd := fs.String("cwd", "", "working directory")
	mode := fs.String("mode", "", "permissions: default | accept-edits | plan | bypass")
	swarmN := fs.Int("swarm", 0, "chat as a team of N agents in all, the manager included (config swarm.max_agents is the ceiling); on a terminal the default is "+strconv.Itoa(defaultAgents)+", --swarm 0 (or 1) is a single agent")
	trust := fs.Bool("trust-project", false, trustProjectHelp)
	verbose := fs.Bool("verbose", false, "print notices and tool errors")
	budget := fs.Float64("budget-usd", 0, "stop when spend reaches this many US dollars")
	noMCP := fs.Bool("no-mcp", false, "start no MCP tool servers")
	verify := fs.String("verify", "", "swarm: command the harness runs before a worker's task may leave 'doing' (with --isolation worktree, also on every merge). {dirs} in it stands for the directories the task may touch (./... without a scope), so that each task is verified on its own work: 'go test {dirs}'")
	isolation, commit := isolationFlags(fs)
	mailman := mailmanFlag(fs)
	roleModels := kvFlags{}
	fs.Var(roleModels, "role-model", "role=model override, repeatable (e.g. manager=heimdall/x, mailman=heimdall/small)")
	resume := resumeFlags(fs)
	allow := allowFlags(fs)
	plain := fs.Bool("plain", false, "plain lines, as when the input or the output is not a terminal: no colour, no status line, no redrawing, approvals typed as y, a or n")
	noAnim := fs.Bool("no-anim", false, "no animation: the spinner stands still, and nothing sweeps, folds or flashes (also SLEIPNIR_ANIM=0, REDUCE_MOTION=1 and NO_COLOR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Nothing says which model to use: ask the provider what it has, once (pick.go).
	asking := bufio.NewReader(os.Stdin)
	if err := ensureModel(ctx, model, asking, os.Stderr, func() (string, error) { return readSecret(ctx, asking) }, term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))); err != nil {
		return err
	}
	sessionOptions := func(spec string) session.Options {
		return chatOptions(session.Options{
			Cwd: *cwd, Model: *model, Mode: perm.Mode(*mode), Swarm: *swarmN > 1, MaxAgents: *swarmN,
			TrustProject: *trust, BudgetUSD: *budget, Resume: spec, NoMCP: *noMCP,
			Verify: *verify, Isolation: *isolation, Commit: *commit, Mailman: mailman(), RoleModels: roleModels,
			Allow: expandAllow(*allow),
		})
	}
	if !*plain && app.CanDrawChat(os.Stdin, os.Stdout, os.Getenv) {
		given := false
		fs.Visit(func(f *flag.Flag) { given = given || f.Name == "swarm" })
		if !given {
			*swarmN = defaultTeam()
		}
	}
	// On a terminal that can be drawn on the chat is a program (internal/tui/app): a live region with a status line, the prompt
	// stack and the input, markdown, diffs, a dialog for approvals. Anything else, a pipe, a file, TERM=dumb or --plain, gets the
	// line chat below, byte for byte what it has always printed.
	if !*plain && app.CanDrawChat(os.Stdin, os.Stdout, os.Getenv) {
		dir := *cwd
		if dir == "" {
			dir, _ = os.Getwd()
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		return chatOnTerminal(ctx, chatTTY{resume: resume, options: sessionOptions, cwd: dir, verbose: *verbose, noAnim: *noAnim})
	}
	// From here on Ctrl-C is this command's: registered before anything slow starts, so that
	// it never meets the default action, which would end the process.
	intr := newInterrupts()
	stopWatching := intr.watch()
	defer stopWatching()
	spec, err := resume()
	if err != nil {
		return err
	}
	isTerminal := term.IsTerminal(int(os.Stdin.Fd()))
	lines := newStdinLines(os.Stdin, !isTerminal)
	o := sessionOptions(spec)
	if isTerminal {
		o.Prompter = session.LinePrompter(lines.Answer, os.Stderr)
	}
	mainAgent := "main"
	if o.Swarm {
		mainAgent = "mgr"
	}
	sink := session.NewTextSink(os.Stdout, os.Stderr, mainAgent, *verbose)
	o.Sink = sink
	if o.Swarm {
		o.NewSink = func(string) agent.Sink { return sink }
	}
	session.Version = version
	// Starting can take a while (catalogue, recon, tool servers), and Ctrl-C ends it. The
	// context is not cancelled when New returns: what New starts may keep it.
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	intr.begin(cancelStart)
	s, err := session.New(startCtx, o)
	intr.end()
	if err != nil {
		return err
	}
	defer func() {
		// An isolated session ends by applying what passed verification to the checkout
		// (a no-op, and nil, for any other session); the person is told what happened.
		printIntegration(os.Stderr, finishRun(ctx, s), false)
		s.Close()
	}()

	fmt.Fprintln(os.Stderr, wrapFor(os.Stderr, fmt.Sprintf("sleipnir %s · %s%s · %s · session %s", version, s.Model.ID, budgetLabel(s), modeName(s), s.ID)))
	if a := s.Main(); s.Resumed() && a != nil {
		fmt.Fprintln(os.Stderr, resumedLine(s, a))
	}
	fmt.Fprintln(os.Stderr, "Type a goal, or /help. Ctrl-C cancels the current turn (twice at the prompt quits); /exit or Ctrl-D quits.")
	for {
		if ctx.Err() != nil { // SIGTERM, in the middle of a turn or not
			s.SetEndReason(session.EndInterrupted)
			fmt.Fprintln(os.Stderr)
			return nil
		}
		fmt.Fprint(os.Stderr, "\n› ")
		line, err := readInput(ctx, lines, intr.idle)
		switch {
		case err == nil:
			intr.disarm() // whatever was typed, a Ctrl-C after it is a first one again
		case errors.Is(err, errInterrupted):
			if intr.pressed() {
				s.SetEndReason(session.EndInterrupted)
				fmt.Fprintln(os.Stderr)
				return nil
			}
			fmt.Fprintln(os.Stderr, "\n"+quitHint)
			continue
		case errors.Is(err, io.EOF):
			s.SetEndReason(session.EndExit)
			fmt.Fprintln(os.Stderr)
			return nil
		case errors.Is(err, context.Canceled):
			s.SetEndReason(session.EndInterrupted)
			fmt.Fprintln(os.Stderr)
			return nil
		default:
			s.SetEndReason(session.EndError)
			return err
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "/"):
			var quit bool
			var send string
			intr.run(ctx, func(c context.Context) { quit, send = slash(c, s, line) })
			if quit {
				s.SetEndReason(session.EndExit)
				return nil
			}
			if send == "" {
				continue
			}
			line = send // a custom command or skill expanded into a prompt
		}
		runTurn(ctx, intr, s, line)
	}
}

// chatOptions marks a session as interactive: a person is at the keyboard across
// turns. In a swarm that means the manager is not held to its board (workers
// legitimately outlive a turn) and finished work wakes it instead; run and swarm
// are batch runs and do not set it.
func chatOptions(o session.Options) session.Options {
	o.Interactive = true
	return o
}

func modeName(s *session.Session) string {
	m := string(s.Perm.Mode())
	if s.Swarm != nil {
		m += " · swarm"
	}
	return m
}

// runTurn runs one goal with Ctrl-C bound to that turn only (interrupts.run): it cancels the
// turn, and with it a question the turn is asking, and nothing else. parent is the process's
// context, which SIGTERM cancels.
func runTurn(parent context.Context, intr *interrupts, s *session.Session, goal string) {
	start := time.Now()
	var res *session.Result
	var err error
	intr.run(parent, func(ctx context.Context) { res, err = s.Run(ctx, goal) })
	fmt.Fprintln(os.Stdout)
	if res != nil {
		fmt.Fprintf(os.Stderr, "── %s · %d steps · $%.4f · cache hit %.0f%%\n",
			time.Since(start).Round(time.Second), res.Steps, res.CostUSD, res.Usage.HitRatio()*100)
	}
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "(cancelled)")
	case errors.Is(err, agent.ErrBudget):
		fmt.Fprintln(os.Stderr, tools.SanitizeForTerminal(budgetStopped(s, res).Error()))
	default:
		fmt.Fprintln(os.Stderr, "error:", tools.SanitizeForTerminal(err.Error()))
	}
}

const chatHelp = `conversation
/goal TEXT         work until it is met, judged on evidence (/goal: status)
/new               start again, empty (same model and mode)
/clear             the same as /new
/resume [id]       pick an earlier session from a menu, and continue it
/sessions          the newest sessions
/compact [focus]   fold the older thread now; focus says what to keep in view
/rewind [id]       list checkpoints, or restore files to before a turn
/diff [id]         what changed in the newest checkpoint, or in <id>
/exit              quit (Ctrl-D, or Ctrl-C twice at the prompt)

model and cost
/model [ref]       pick a model from a menu; a team starts again on it
/fav [ref]         star a model, or unstar it; starred ones lead in /model
/login [provider]  add a key, or sign in with ChatGPT (the chat comes back)
/budget [usd|off]  the dollar budget for the turns from now on
/cost              tokens, cost and cache hit ratio so far
/stats             the stats page (ctrl+t): cost, cache, savings, layers
/context           what each layer of the prompt weighs
/status            model, mode, session, budget and cost at a glance

permissions
/mode <m>          default | accept-edits | plan | bypass   (/plan = plan)
/allow <rule>      allow, this session, what would ask: tests, Bash(go test:*)
/permissions       the mode and the rules in force
/trust             this project's own instructions and settings, and your yes

a team, and the program
/roles [role=m]    which model each role runs on; change one (restarts)
/swarm <n> [flags] start again as a team of n agents (the manager included)
/restart [flags]   start again with other flags: --no-mcp, --cwd DIR, ...
/agents            the team's agents and tasks (ctrl+g)
/steer TEXT        tell the running turn something, without stopping it
/verbose [on|off]  notices and tool errors
/anim [on|off]     motion
/cwd               the directory this session works in

what the model knows
/recon             the project map in the shared layer
/skills            the skills the model can load
/mcp               tool servers: state and tools (/mcp reconnect NAME)
/help              this text, and your custom commands and skills`

// slash handles a slash command. It reports whether to quit, and the prompt to
// send when the command was a custom one (or a skill) that expanded into text.
// It writes to the process's standard streams, as the line chat always has.
func slash(ctx context.Context, s *session.Session, line string) (quit bool, send string) {
	return slashTo(ctx, s, line, os.Stdout, os.Stderr)
}

// slashTo is slash with the streams given: what a command says goes to stderr (a diff, which is output, goes to stdout). The chat on a
// terminal gives both the same buffer, because nothing but its program may write to the terminal.
func slashTo(ctx context.Context, s *session.Session, line string, stdout, stderr io.Writer) (quit bool, send string) {
	f := strings.Fields(line)
	switch f[0] {
	case "/exit", "/quit":
		return true, ""
	case "/help", "/?":
		fmt.Fprintln(stderr, chatHelp)
		printCustom(s, stderr)
	case "/skills":
		printSkills(s, stderr)
	case "/mcp":
		if len(f) == 3 && f[1] == "reconnect" {
			if err := s.MCPReconnect(f[2]); err != nil {
				fmt.Fprintln(stderr, "mcp:", err)
			} else {
				fmt.Fprintln(stderr, "reconnecting", f[2])
			}
			break
		}
		printMCP(s, stderr)
	case "/status":
		printStatus(stderr, s)
	case "/permissions":
		printPermissions(stderr, s)
	case "/trust":
		home, _ := os.UserHomeDir()
		dir, _ := os.Getwd()
		if err := trustShow(stderr, trust.OpenLedger(session.TrustLedgerPath(home)), home, dir); err != nil {
			fmt.Fprintln(stderr, "trust:", err)
		}
	case "/cost":
		printCost(stderr, s)
	case "/stats": // in the terminal program the page is drawn by the program itself (internal/tui/app, page); this is the line chat's
		printCost(stderr, s)
		printContext(stderr, s)
	case "/context":
		printContext(stderr, s)
	case "/compact":
		compactNow(ctx, s, strings.TrimSpace(strings.TrimPrefix(line, f[0])), stderr)
	case "/agents":
		printAgents(stderr, s)
	case "/steer":
		text := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
		if text == "" {
			fmt.Fprintln(stderr, "usage: /steer TEXT: tell the running turn something without stopping it (use the other file, skip the tests); it is read with the agent's next step")
			break
		}
		if a := s.Main(); a != nil {
			a.Steer(text)
			fmt.Fprintln(stderr, "steering sent: the agent reads it with its next step")
		}
	case "/plan":
		s.Perm.SetMode(perm.ModePlan)
		fmt.Fprintln(stderr, "plan mode: read-only")
	case "/model":
		if len(f) < 2 {
			fmt.Fprintf(stderr, "model: %s (change it with /model provider/model; list them with `sleipnir models`)\n", s.Model.ID)
			break
		}
		if ref, err := s.SwitchModel(ctx, f[1]); err != nil {
			fmt.Fprintln(stderr, tools.SanitizeForTerminal(err.Error()))
		} else {
			fmt.Fprintf(stderr, "model: %s (the conversation carries over; the prompt cache starts over)\n", ref)
		}
	case "/fav":
		ref := s.ModelRef()
		if len(f) > 1 {
			ref = f[1]
		}
		if len(f) > 2 || ref == "" {
			fmt.Fprintln(stderr, "usage: /fav [provider/model]: star the model, or unstar it; this session's model when none is named")
			break
		}
		fmt.Fprintln(stderr, favoriteLine(nil, s.Home(), ref))
	case "/budget":
		if len(f) < 2 {
			if b := s.Budget(); b > 0 {
				fmt.Fprintf(stderr, "budget: $%.2f, spent $%.4f (change it with /budget <dollars>, /budget off removes it)\n", b, s.Cost())
			} else {
				fmt.Fprintf(stderr, "budget: none, spent $%.4f (set one with /budget <dollars>)\n", s.Cost())
			}
			break
		}
		usd, err := parseBudget(f[1])
		if err == nil {
			err = s.SetBudget(usd)
		}
		switch {
		case err != nil:
			fmt.Fprintln(stderr, "budget:", err)
		case usd == 0:
			fmt.Fprintln(stderr, "budget: removed")
		default:
			fmt.Fprintf(stderr, "budget: $%.2f for the turns from now on (spent so far $%.4f)\n", usd, s.Cost())
		}
	case "/allow":
		rules := expandAllow(f[1:])
		if len(rules) == 0 {
			fmt.Fprintln(stderr, "usage: /allow tests | /allow 'Bash(go test:*)' | /allow 'Edit(src/**)' ...: allow for the rest of this session what would otherwise ask; tests is the build and test commands of most projects ("+testsPresetSummary+")")
			break
		}
		var done []string
		for _, r := range rules {
			if err := s.AllowForSession(r); err != nil {
				fmt.Fprintf(stderr, "allow %s: %v\n", tools.SanitizeForTerminal(r), err)
			} else {
				done = append(done, tools.SanitizeForTerminal(r))
			}
		}
		switch {
		case slices.Contains(f[1:], testsPreset) && len(done) >= len(testsAllow): // the "tests" preset: its rules are one idea, and not a Go one
			fmt.Fprintf(stderr, "allowed for this session: the build and test commands (%s; %d rules)\n", testsPresetSummary, len(done))
		case len(done) > 4:
			fmt.Fprintf(stderr, "allowed for this session: %d rules, among them %s, %s\n", len(done), done[0], done[1])
		case len(done) > 0:
			fmt.Fprintf(stderr, "allowed for this session: %s\n", strings.Join(done, ", "))
		}
	case "/cwd":
		fmt.Fprintf(stderr, "cwd: %s (a session works in one directory: start sleipnir there, or /restart --cwd DIR)\n", tildePath(s.Cwd()))
	case "/sessions":
		if err := printSessions(stdout, stderr, "", 10); err != nil {
			fmt.Fprintln(stderr, "sessions:", err)
		}
	case "/mode":
		if len(f) < 2 {
			fmt.Fprintln(stderr, "mode:", s.Perm.Mode())
			break
		}
		switch m := perm.Mode(f[1]); m {
		case perm.ModeDefault, perm.ModeAcceptEdits, perm.ModePlan, perm.ModeBypass:
			s.Perm.SetMode(m)
			fmt.Fprintln(stderr, "mode:", m)
		default:
			fmt.Fprintln(stderr, "unknown mode; use default, accept-edits, plan or bypass")
		}
	case "/rewind":
		rewind(stderr, s, f[1:])
	case "/diff":
		showDiff(stdout, stderr, s, f[1:])
	case "/recon":
		if s.Shared != nil {
			fmt.Fprintln(stderr, tools.SanitizeForTerminal(s.Shared.Text()))
		}
	default:
		args := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
		prompt, notices, ok, err := expandSlash(ctx, s, strings.TrimPrefix(f[0], "/"), args)
		for _, n := range notices {
			fmt.Fprintln(stderr, "note:", n)
		}
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "%s: %v\n", f[0], err)
		case !ok:
			fmt.Fprintf(stderr, "unknown command %s; %stry /help\n", f[0], didYouMean(f[0]))
		default:
			return false, prompt
		}
	}
	return false, ""
}

// expandSlash turns a custom command or a user-invocable skill into the prompt
// to send. ok is false when nothing of that name exists.
func expandSlash(ctx context.Context, s *session.Session, name, args string) (prompt string, notices []string, ok bool, err error) {
	if strings.HasPrefix(name, "mcp__") {
		for _, p := range s.MCPPrompts() {
			if p.Command == "/"+name {
				text, err := s.MCPPrompt(ctx, p.Command, mcpPromptArgs(p, args))
				if err != nil {
					return "", nil, true, err
				}
				return text, nil, true, nil
			}
		}
	}
	if reg := s.Commands(); reg != nil {
		if _, found := reg.Get(name); found {
			exp, err := reg.Expand(ctx, name, args)
			if err != nil {
				return "", nil, true, err
			}
			return exp.Prompt, exp.Notices, true, nil
		}
	}
	if s.Skills != nil {
		if _, found := s.Skills.Get(name); found {
			l, err := s.Skills.LoadForUser(name, args)
			if err != nil {
				return "", nil, true, err
			}
			return l.Text(), nil, true, nil
		}
	}
	return "", nil, false, nil
}

// printCustom lists the user's own commands and skills under /help.
func printCustom(s *session.Session, w io.Writer) {
	if reg := s.Commands(); reg != nil {
		cmds := reg.List()
		if len(cmds) > 0 {
			fmt.Fprintln(w, "\ncustom commands:")
		}
		for _, c := range cmds {
			fmt.Fprintf(w, "/%-17s %s\n", c.Name, firstText(c.Description, 90))
		}
	}
	if s.Skills != nil && s.Skills.Len() > 0 {
		fmt.Fprintln(w, "\nskills (also loadable by the model; /skills lists them):")
	}
}

func printSkills(s *session.Session, w io.Writer) {
	if s.Skills == nil || s.Skills.Len() == 0 {
		fmt.Fprintln(w, "no skills (put SKILL.md files under .sleipnir/skills/<name>/ or ~/.sleipnir/skills/<name>/)")
		return
	}
	for _, k := range s.Skills.Skills() {
		who := ""
		if k.DisableModelInvocation {
			who = " (you only)"
		}
		fmt.Fprintf(w, "  %-20s %s%s\n", k.Name, firstText(k.Summary(), 90), who)
	}
}

// printStatus is /status: what a person looks for when unsure what this session is doing.
func printStatus(w io.Writer, s *session.Session) {
	kind := "single agent"
	if s.Swarm != nil {
		kind = "swarm"
	}
	fmt.Fprintf(w, "model    %s (%s)\nmode     %s\nsession  %s (%s)\n", s.Model.ID, kind, s.Perm.Mode(), s.ID, tildePath(s.Dir))
	if b := s.Budget(); b > 0 {
		fmt.Fprintf(w, "budget   $%.2f\n", b)
	}
	printCost(w, s)
}

// printPermissions is /permissions: the mode and every rule in force, so that "what did I allow?" has an answer. Rules that came from
// "don't ask again" are in the allow list with the rest.
func printPermissions(w io.Writer, s *session.Session) {
	fmt.Fprintln(w, "mode:", s.Perm.Mode())
	for _, a := range []perm.Action{perm.Deny, perm.Ask, perm.Allow} {
		rules := s.Perm.Rules(a)
		if len(rules) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s (%d):\n", a, len(rules))
		for i, r := range rules {
			if i == 40 {
				fmt.Fprintf(w, "  ... %d more\n", len(rules)-i)
				break
			}
			fmt.Fprintln(w, "  "+tools.SanitizeForTerminal(r))
		}
	}
}

// costText is a cost to four places, and "<$0.0001" for a cost that is not nothing and would show as $0.0000.
func costText(usd float64) string {
	if usd > 0 && usd < 0.00005 {
		return "<$0.0001"
	}
	return fmt.Sprintf("$%.4f", usd)
}

func printCost(w io.Writer, s *session.Session) {
	var u core.Usage
	var usd float64
	switch {
	case s.Swarm != nil:
		usd = s.Swarm.TotalCost()
		if m := s.Swarm.Manager(); m != nil {
			u, _ = m.Usage()
		}
	case s.Agent != nil:
		u, usd = s.Agent.Usage()
	}
	fmt.Fprintf(w, "input %d (uncached) + %d cached-read + %d cache-write · output %d · hit %.0f%% · %s\n",
		u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens(), u.OutputTokens, u.HitRatio()*100, costText(usd))
}

// compactNow folds the thread on request and says what happened. focus tells the compactor
// what the user cares about. ctx is one that Ctrl-C cancels (the slash command runs under
// interrupts.run).
func compactNow(ctx context.Context, s *session.Session, focus string, w io.Writer) {
	rep, err := s.Compact(ctx, focus)
	switch {
	case err != nil:
		fmt.Fprintln(w, "compact:", err)
	case rep.Mode == "none":
		fmt.Fprintln(w, "nothing to compact yet")
	default:
		fmt.Fprintf(w, "compacted (%s): %d turns folded, %d → %d tokens; the next request writes the cached prefix again, once\n",
			rep.Mode, rep.FoldedTurns, rep.TokensBefore, rep.TokensAfter)
	}
}

func printContext(w io.Writer, s *session.Session) {
	var a *agent.Agent
	if s.Swarm != nil {
		a = s.Swarm.Manager()
	} else {
		a = s.Agent
	}
	if a == nil {
		fmt.Fprintln(w, "no agent yet; send a goal first")
		return
	}
	est := core.NewBytesEstimator()
	st := a.Stack()
	row := func(name string, tokens int) { fmt.Fprintf(w, "  %-16s %7d tokens\n", name, tokens) }
	row("constitution", st.Const.Tokens(est))
	row("shared pin", st.Shared.Tokens(est))
	row("role pin", st.RoleL.Tokens(est))
	row("notes", st.Notes.Tokens(est))
	row("spine", st.Spine.Tokens(est))
	row("thread (verbatim)", st.Thread.Tokens(est))
}

func printAgents(w io.Writer, s *session.Session) {
	if s.Swarm == nil {
		fmt.Fprintln(w, "single agent session (start with --swarm N for a team)")
		return
	}
	snap := s.Swarm.Board.Snapshot()
	sort.Slice(snap.Agents, func(i, j int) bool { return snap.Agents[i].ID < snap.Agents[j].ID })
	for _, a := range snap.Agents {
		fmt.Fprintf(w, "  %-8s %-10s %-8s %-4s %s\n", a.ID, a.Role, a.State, a.Task, firstText(a.Line, 80))
	}
	for _, t := range snap.Tasks {
		fmt.Fprintf(w, "  %-4s %-8s %-8s %s\n", t.ID, t.Status, t.Owner, firstText(t.Title, 80))
	}
	if s.Swarm.MailmanEnabled() {
		st := s.Swarm.MailmanStats()
		fmt.Fprintf(w, "  mailman: %d worker messages taken, %d digests covering %d, %d delivered directly, %d waiting\n",
			st.Parcels, st.Digests, st.Digested, st.Direct, st.Pending)
	}
}

// firstText is s cut to n bytes, and cleaned for a terminal: it is used for
// text somebody else wrote (a skill's description, a worker's status line, a
// task title).
func firstText(s string, n int) string {
	s = tools.SanitizeForTerminal(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func rewind(w io.Writer, s *session.Session, args []string) {
	if len(args) == 0 {
		n := 0
		for _, c := range s.Ckpt.List() {
			if len(c.Files) == 0 { // a checkpoint that touched nothing has nothing to put back
				continue
			}
			fmt.Fprintf(w, "  %s  %s  %d files  %s\n", c.ID, c.Time.Format("15:04:05"), len(c.Files), c.Label)
			n++
		}
		if n == 0 {
			fmt.Fprintln(w, "no checkpoints yet (one is kept for each turn that changes a file)")
		} else {
			fmt.Fprintln(w, "/rewind ID puts the files back as they were; /diff ID shows what changed")
		}
		return
	}
	rep, err := s.Ckpt.Restore(args[0], checkpoint.RestoreOpts{})
	if err != nil {
		fmt.Fprintln(w, "rewind:", err)
		return
	}
	fmt.Fprintln(w, rep.Summary())
}

func showDiff(stdout, stderr io.Writer, s *session.Session, args []string) {
	id := ""
	if len(args) > 0 {
		id = args[0]
	} else {
		// no id: the newest checkpoint that changed a file (a trial's person typed /diff after an edit and was told to look up an id first)
		list := s.Ckpt.List()
		for i := len(list) - 1; i >= 0 && id == ""; i-- {
			if len(list[i].Files) > 0 {
				id = list[i].ID
			}
		}
		if id == "" {
			fmt.Fprintln(stderr, "usage: /diff <checkpoint id>   (no checkpoint has changed a file yet)")
			return
		}
		fmt.Fprintf(stderr, "checkpoint %s, the newest that changed a file\n", id)
	}
	diffs, err := s.Ckpt.Diff(id)
	if err != nil {
		fmt.Fprintln(stderr, "diff:", err)
		return
	}
	for _, d := range diffs {
		fmt.Fprintf(stdout, "%s (%s)\n%s\n", tools.SanitizeForTerminal(d.Path), d.Status, tools.SanitizeForTerminal(d.Unified))
	}
}

// mcpPromptArgs maps what the user typed after a server prompt's command onto its
// declared arguments: name=value pairs, or bare words filling the arguments in
// order (the last one takes the rest of the line).
func mcpPromptArgs(p mcp.PromptEntry, line string) map[string]string {
	out := map[string]string{}
	line = strings.TrimSpace(line)
	if line == "" {
		return out
	}
	var bare []string
	for _, w := range strings.Fields(line) {
		if k, v, ok := strings.Cut(w, "="); ok && k != "" {
			out[k] = v
			continue
		}
		bare = append(bare, w)
	}
	var free []string
	for _, a := range p.Arguments {
		if _, set := out[a.Name]; !set {
			free = append(free, a.Name)
		}
	}
	for i, w := range bare {
		if i >= len(free) {
			break
		}
		if i == len(free)-1 {
			out[free[i]] = strings.Join(bare[i:], " ")
			break
		}
		out[free[i]] = w
	}
	return out
}

// parseBudget reads a dollar amount for /budget: "5", "$5", "0.50"; "off", "none" and "0" remove the budget.
func parseBudget(s string) (float64, error) {
	t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if t == "off" || t == "none" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("want dollars such as 5 or 0.50, or off; got %q", s)
	}
	return v, nil
}

// didYouMean is "did you mean /model? " for a slash command that is a typo of one the chat has (a swapped or a missing letter), else "".
func didYouMean(typed string) string {
	name := strings.TrimPrefix(typed, "/")
	for _, c := range chatCommands {
		if len(name) >= 3 && editDistance(name, c.name) <= 2 && name != c.name {
			return "did you mean /" + c.name + "? "
		}
	}
	return ""
}

// editDistance is the number of single-letter changes, and swaps of two neighbours, between two words.
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}
