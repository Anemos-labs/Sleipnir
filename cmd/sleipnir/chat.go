package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/checkpoint"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/mcp"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/tools"
)

func init() { extraCommands["chat"] = cmdChat }

// cmdChat is the interactive loop: type a goal, watch the agent work, steer with
// slash commands. Ctrl-C cancels the running turn, not the session.
func cmdChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	model := fs.String("model", "", "model: provider/model or a bare id for the default provider")
	cwd := fs.String("cwd", "", "working directory")
	mode := fs.String("mode", "", "permissions: default | accept-edits | plan | bypass")
	swarmN := fs.Int("swarm", 0, "chat with a manager that can spawn up to N workers (config swarm.max_agents is the ceiling)")
	trust := fs.Bool("trust-project", false, trustProjectHelp)
	verbose := fs.Bool("verbose", false, "print notices and tool errors")
	budget := fs.Float64("budget-usd", 0, "stop when spend reaches this many US dollars")
	noMCP := fs.Bool("no-mcp", false, "start no MCP tool servers")
	verify := fs.String("verify", "", "swarm: command the harness runs before a worker's task may leave 'doing' (with --isolation worktree, also on every merge)")
	isolation, commit := isolationFlags(fs)
	mailman := mailmanFlag(fs)
	roleModels := kvFlags{}
	fs.Var(roleModels, "role-model", "role=model override, repeatable (e.g. manager=heimdall/x, mailman=heimdall/small)")
	resume := resumeFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	spec, err := resume()
	if err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)
	o := chatOptions(session.Options{
		Cwd: *cwd, Model: *model, Mode: perm.Mode(*mode), Swarm: *swarmN > 0, MaxAgents: *swarmN + 1,
		TrustProject: *trust, BudgetUSD: *budget, Resume: spec, NoMCP: *noMCP,
		Verify: *verify, Isolation: *isolation, Commit: *commit, Mailman: mailman(), RoleModels: roleModels,
	})
	if term.IsTerminal(int(os.Stdin.Fd())) {
		o.Prompter = session.TerminalPrompter(in, os.Stderr)
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
	s, err := session.New(ctx, o)
	if err != nil {
		return err
	}
	defer func() {
		// An isolated session ends by applying what passed verification to the checkout
		// (a no-op, and nil, for any other session); the person is told what happened.
		printIntegration(os.Stderr, finishRun(ctx, s), false)
		s.Close()
	}()

	fmt.Fprintf(os.Stderr, "sleipnir %s · %s%s · %s · session %s\n", version, s.Model.ID, budgetLabel(s), modeName(s), s.ID)
	if s.Resumed() {
		fmt.Fprintf(os.Stderr, "resumed: %d turns restored; the first request writes the cached prefix again, once\n", len(s.Agent.Stack().Thread.Turns))
	}
	fmt.Fprintln(os.Stderr, "Type a goal, or /help. Ctrl-C cancels the current turn; /exit quits.")
	for {
		fmt.Fprint(os.Stderr, "\n› ")
		line, err := readInput(ctx, in)
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				s.SetEndReason(session.EndExit)
				fmt.Fprintln(os.Stderr)
				return nil
			case errors.Is(err, context.Canceled):
				s.SetEndReason(session.EndInterrupted)
				fmt.Fprintln(os.Stderr)
				return nil
			}
			s.SetEndReason(session.EndError)
			return err
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "/"):
			quit, send := slash(ctx, s, line)
			if quit {
				s.SetEndReason(session.EndExit)
				return nil
			}
			if send == "" {
				continue
			}
			line = send // a custom command or skill expanded into a prompt
		}
		runTurn(ctx, s, line)
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

// readInput reads one logical line; a trailing backslash continues it.
func readInput(ctx context.Context, in *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		type res struct {
			s   string
			err error
		}
		ch := make(chan res, 1)
		go func() { s, err := in.ReadString('\n'); ch <- res{s, err} }()
		var r res
		select {
		case r = <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if r.err != nil && r.s == "" {
			return "", r.err
		}
		ln := strings.TrimRight(r.s, "\r\n")
		if strings.HasSuffix(ln, "\\") {
			sb.WriteString(strings.TrimSuffix(ln, "\\"))
			sb.WriteString("\n")
			fmt.Fprint(os.Stderr, "… ")
			continue
		}
		sb.WriteString(ln)
		return sb.String(), nil
	}
}

// runTurn runs one goal with Ctrl-C bound to that turn only.
func runTurn(parent context.Context, s *session.Session, goal string) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	res, err := s.Run(ctx, goal)
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

const chatHelp = `/help              this text (and your custom commands and skills)
/cost              tokens, cost and cache hit ratio so far
/context           layer sizes of the current prompt (what is pinned, what is thread)
/compact [focus]   fold the older thread now (optionally: what to keep in view); a declared, priced rebase
/agents            swarm board: agents and tasks
/mode <m>          default | accept-edits | plan | bypass   (/plan = plan mode)
/rewind            list checkpoints;  /rewind <id> restores files to before that turn
/diff <id>         show what changed since a checkpoint
/recon             show the project map pinned in the shared layer
/skills            list the skills the model can load
/mcp               MCP tool servers: state and tools (/mcp reconnect NAME); their prompts run as /mcp__server__prompt
/exit              quit (also Ctrl-D)`

// slash handles a slash command. It reports whether to quit, and the prompt to
// send when the command was a custom one (or a skill) that expanded into text.
func slash(ctx context.Context, s *session.Session, line string) (quit bool, send string) {
	f := strings.Fields(line)
	switch f[0] {
	case "/exit", "/quit":
		return true, ""
	case "/help", "/?":
		fmt.Fprintln(os.Stderr, chatHelp)
		printCustom(s, os.Stderr)
	case "/skills":
		printSkills(s, os.Stderr)
	case "/mcp":
		if len(f) == 3 && f[1] == "reconnect" {
			if err := s.MCPReconnect(f[2]); err != nil {
				fmt.Fprintln(os.Stderr, "mcp:", err)
			} else {
				fmt.Fprintln(os.Stderr, "reconnecting", f[2])
			}
			break
		}
		printMCP(s, os.Stderr)
	case "/cost":
		printCost(s)
	case "/context":
		printContext(s)
	case "/compact":
		compactNow(ctx, s, strings.TrimSpace(strings.TrimPrefix(line, f[0])), os.Stderr)
	case "/agents":
		printAgents(s)
	case "/plan":
		s.Perm.SetMode(perm.ModePlan)
		fmt.Fprintln(os.Stderr, "plan mode: read-only")
	case "/mode":
		if len(f) < 2 {
			fmt.Fprintln(os.Stderr, "mode:", s.Perm.Mode())
			break
		}
		switch m := perm.Mode(f[1]); m {
		case perm.ModeDefault, perm.ModeAcceptEdits, perm.ModePlan, perm.ModeBypass:
			s.Perm.SetMode(m)
			fmt.Fprintln(os.Stderr, "mode:", m)
		default:
			fmt.Fprintln(os.Stderr, "unknown mode; use default, accept-edits, plan or bypass")
		}
	case "/rewind":
		rewind(s, f[1:])
	case "/diff":
		showDiff(s, f[1:])
	case "/recon":
		if s.Shared != nil {
			fmt.Fprintln(os.Stderr, tools.SanitizeForTerminal(s.Shared.Text()))
		}
	default:
		args := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
		prompt, notices, ok, err := expandSlash(ctx, s, strings.TrimPrefix(f[0], "/"), args)
		for _, n := range notices {
			fmt.Fprintln(os.Stderr, "note:", n)
		}
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "%s: %v\n", f[0], err)
		case !ok:
			fmt.Fprintf(os.Stderr, "unknown command %s; try /help\n", f[0])
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

func printCost(s *session.Session) {
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
	fmt.Fprintf(os.Stderr, "input %d (uncached) + %d cached-read + %d cache-write · output %d · hit %.0f%% · $%.4f\n",
		u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens(), u.OutputTokens, u.HitRatio()*100, usd)
}

// compactNow folds the thread on request, with Ctrl-C bound to it, and says what
// happened. focus tells the compactor what the user cares about.
func compactNow(parent context.Context, s *session.Session, focus string, w io.Writer) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
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

func printContext(s *session.Session) {
	var a *agent.Agent
	if s.Swarm != nil {
		a = s.Swarm.Manager()
	} else {
		a = s.Agent
	}
	if a == nil {
		fmt.Fprintln(os.Stderr, "no agent yet; send a goal first")
		return
	}
	est := core.NewBytesEstimator()
	st := a.Stack()
	row := func(name string, tokens int) { fmt.Fprintf(os.Stderr, "  %-16s %7d tokens\n", name, tokens) }
	row("constitution", st.Const.Tokens(est))
	row("shared pin", st.Shared.Tokens(est))
	row("role pin", st.RoleL.Tokens(est))
	row("notes", st.Notes.Tokens(est))
	row("spine", st.Spine.Tokens(est))
	row("thread (verbatim)", st.Thread.Tokens(est))
}

func printAgents(s *session.Session) {
	if s.Swarm == nil {
		fmt.Fprintln(os.Stderr, "single agent session (start with --swarm N for a team)")
		return
	}
	snap := s.Swarm.Board.Snapshot()
	sort.Slice(snap.Agents, func(i, j int) bool { return snap.Agents[i].ID < snap.Agents[j].ID })
	for _, a := range snap.Agents {
		fmt.Fprintf(os.Stderr, "  %-8s %-10s %-8s %-4s %s\n", a.ID, a.Role, a.State, a.Task, firstText(a.Line, 80))
	}
	for _, t := range snap.Tasks {
		fmt.Fprintf(os.Stderr, "  %-4s %-8s %-8s %s\n", t.ID, t.Status, t.Owner, firstText(t.Title, 80))
	}
	if s.Swarm.MailmanEnabled() {
		st := s.Swarm.MailmanStats()
		fmt.Fprintf(os.Stderr, "  mailman: %d worker messages taken, %d digests covering %d, %d delivered directly, %d waiting\n",
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

func rewind(s *session.Session, args []string) {
	if len(args) == 0 {
		list := s.Ckpt.List()
		if len(list) == 0 {
			fmt.Fprintln(os.Stderr, "no checkpoints yet")
		}
		for _, c := range list {
			fmt.Fprintf(os.Stderr, "  %s  %s  %d files  %s\n", c.ID, c.Time.Format("15:04:05"), len(c.Files), c.Label)
		}
		return
	}
	rep, err := s.Ckpt.Restore(args[0], checkpoint.RestoreOpts{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "rewind:", err)
		return
	}
	fmt.Fprintln(os.Stderr, rep.Summary())
}

func showDiff(s *session.Session, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: /diff <checkpoint id>")
		return
	}
	diffs, err := s.Ckpt.Diff(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "diff:", err)
		return
	}
	for _, d := range diffs {
		fmt.Fprintf(os.Stdout, "%s (%s)\n%s\n", tools.SanitizeForTerminal(d.Path), d.Status, tools.SanitizeForTerminal(d.Unified))
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
