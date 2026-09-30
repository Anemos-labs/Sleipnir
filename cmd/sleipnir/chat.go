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
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/session"
)

func init() { extraCommands["chat"] = cmdChat }

// cmdChat is the interactive loop: type a goal, watch the agent work, steer with
// slash commands. Ctrl-C cancels the running turn, not the session.
func cmdChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	model := fs.String("model", "", "model: provider/model or a bare id for the default provider")
	cwd := fs.String("cwd", "", "working directory")
	mode := fs.String("mode", "", "permissions: default | accept-edits | plan | bypass")
	swarmN := fs.Int("swarm", 0, "chat with a manager that can spawn up to N workers")
	trust := fs.Bool("trust-project", false, "load project instruction files and project-level config")
	verbose := fs.Bool("verbose", false, "print notices and tool errors")
	budget := fs.Float64("budget-usd", 0, "stop when spend reaches this many US dollars")
	if err := fs.Parse(args); err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)
	o := session.Options{
		Cwd: *cwd, Model: *model, Mode: perm.Mode(*mode), Swarm: *swarmN > 0, MaxAgents: *swarmN + 1,
		TrustProject: *trust, BudgetUSD: *budget,
	}
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
	defer s.Close()

	fmt.Fprintf(os.Stderr, "sleipnir %s · %s · %s · session %s\nType a goal, or /help. Ctrl-C cancels the current turn; /exit quits.\n",
		version, s.Model.ID, modeName(s), s.ID)
	for {
		fmt.Fprint(os.Stderr, "\n› ")
		line, err := readInput(ctx, in)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr)
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "/"):
			quit, send := slash(ctx, s, line)
			if quit {
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
		fmt.Fprintln(os.Stderr, "stopped: budget exhausted")
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
	}
}

const chatHelp = `/help              this text (and your custom commands and skills)
/cost              tokens, cost and cache hit ratio so far
/context           layer sizes of the current prompt (what is pinned, what is thread)
/agents            swarm board: agents and tasks
/mode <m>          default | accept-edits | plan | bypass   (/plan = plan mode)
/rewind            list checkpoints;  /rewind <id> restores files to before that turn
/diff <id>         show what changed since a checkpoint
/recon             show the project map pinned in the shared layer
/skills            list the skills the model can load
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
	case "/cost":
		printCost(s)
	case "/context":
		printContext(s)
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
			fmt.Fprintln(os.Stderr, s.Shared.Text())
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
}

func firstText(s string, n int) string {
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
		fmt.Fprintf(os.Stdout, "%s (%s)\n%s\n", d.Path, d.Status, d.Unified)
	}
}
