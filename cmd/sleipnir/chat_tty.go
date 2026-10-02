package main

// `sleipnir chat` on a terminal: the inline program of docs/UX.md (internal/tui/app, RunChat). This file is the part of it that
// knows about sessions. The program itself knows nothing of them: it draws, reads keys and keeps the order of things, and asks a
// ChatHost to run a turn or a slash command. What the session says while it works reaches it through the sink and the prompter of
// a ChatLink, which only forward.
//
// The session is made while the program already runs, in a goroutine of this file's: making it can take a while (the catalogue, the
// survey, tool servers), what a person types meanwhile is kept, Ctrl-C ends it, and a tool server that asks for approval is asked
// on the screen like any other question.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/app"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
)

// chatTTY is what the program needs from the flags.
type chatTTY struct {
	resume  func() (string, error)
	options func(resume string) session.Options
	cwd     string
	verbose bool
	noAnim  bool
}

// chatOnTerminal runs the chat program and, when it ends, the session's end: its reason, the integration of an isolated run, the
// close. Everything it prints is printed once the terminal is back as it was.
func chatOnTerminal(ctx context.Context, f chatTTY) error {
	spec, err := f.resume()
	if err != nil {
		return err
	}
	o := f.options(spec)
	link := app.NewChatLink()
	sink := link.Sink()
	o.Sink = sink
	o.Prompter = link.Prompter()
	mainAgent := "main"
	if o.Swarm {
		mainAgent = "mgr"
		o.NewSink = func(string) agent.Sink { return sink }
	}
	session.Version = version

	type made struct {
		s   *session.Session
		err error
	}
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	attach := make(chan app.ChatAttach, 1)
	startDone := make(chan made, 1)
	cwd := f.cwd
	go func() {
		s, err := session.New(startCtx, o)
		if err == nil && startCtx.Err() != nil {
			// Ctrl-C (or the end of the process) came while the start was finishing: the person asked for no session, and gets none
			s.SetEndReason(session.EndInterrupted)
			s.Close()
			s, err = nil, startCtx.Err()
		}
		startDone <- made{s, err}
		if err != nil {
			attach <- app.ChatAttach{Err: err}
			return
		}
		log, _ := s.Log.Subscribe(4096)
		var models func() []input.Choice
		if s.Swarm == nil { // /model is refused in a swarm
			models = modelChoices(startCtx)
		}
		attach <- app.ChatAttach{Host: &sessionHost{s: s}, Info: chatInfo(s, cwd), Events: log, Commands: chatSlashCommands(s), Root: cwd, Models: models}
	}()

	var hist *input.History
	if home, herr := os.UserHomeDir(); herr == nil {
		hist, _ = input.OpenHistory(filepath.Join(stateDir(home), "history.jsonl")) // never nil; one that cannot be read or written still serves this session
	}
	var restartTo []string
	end, err := app.RunChatTTY(ctx, link, attach, app.ChatTTYOptions{RestartTo: &restartTo, NoAnim: f.noAnim, Verbose: f.verbose, MainAgent: mainAgent, History: hist, CancelStart: cancelStart})

	// the program is over and the terminal is ours again; the session is whatever the start came to
	cancelled := startCtx.Err() != nil // Ctrl-C while the session was being made (the program cancels the start), or SIGTERM
	cancelStart()
	m := <-startDone
	if m.err != nil {
		switch {
		case ctx.Err() != nil:
			return ctx.Err() // SIGTERM: main says "interrupted" and ends with 143
		case cancelled:
			return &exitError{code: 130, err: errors.New("interrupted")}
		}
		return m.err
	}
	s := m.s
	defer func() {
		// An isolated session ends by applying what passed verification to the checkout (a no-op, and nil, for any other session);
		// the person is told what happened.
		printIntegration(os.Stderr, finishRun(ctx, s), false)
		s.Close()
		if end == app.ChatRestart && restartTo != nil {
			runAgain(restartTo) // a command asked for the chat again with other flags (/restart, /swarm): it ends this process
		}
	}()
	switch end {
	case app.ChatRestart, app.ChatQuit:
		s.SetEndReason(session.EndExit)
	case app.ChatInterrupted:
		s.SetEndReason(session.EndInterrupted)
	default:
		s.SetEndReason(session.EndError)
	}
	if err != nil {
		return err
	}
	return nil
}

// chatInfo is what the banner says about a session.
func chatInfo(s *session.Session, cwd string) app.ChatInfo {
	info := app.ChatInfo{Version: version, Model: s.Model.ID, Cwd: tildePath(cwd), SessionID: s.ID, Swarm: s.Swarm != nil,
		Budget: strings.TrimPrefix(budgetLabel(s), " · ")}
	if s.Resumed() && s.Agent != nil {
		info.Resumed = fmt.Sprintf("resumed: %d turns restored; the first request writes the cached prefix again, once", len(s.Agent.Stack().Thread.Turns))
	}
	return info
}

// tildePath shows a path under the home directory as ~/....
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// sessionHost is a session as the chat program sees it.
type sessionHost struct{ s *session.Session }

// Turn runs a goal. What the model says, the tools it calls and what they answer reach the screen through the sink; what comes back
// is how it ended, and the program draws the record of it.
func (h *sessionHost) Turn(ctx context.Context, goal string) app.TurnResult {
	res, err := h.s.Run(ctx, goal)
	out := app.TurnResult{Err: err}
	if res != nil {
		out.Steps, out.CostUSD, out.HitRatio = res.Steps, res.CostUSD, res.Usage.HitRatio()
	}
	if errors.Is(err, agent.ErrBudget) {
		out.Message = tools.SanitizeForTerminal(budgetStopped(h.s, res).Error())
	}
	if hint := authHint(err); hint != "" {
		out.Message = tools.SanitizeForTerminal(err.Error()) + "\n" + hint
	}
	return out
}

// Command runs a slash command. Both of its streams are the one the program is given, so that they come back in the order they were
// written and nothing reaches the terminal but through the program.
func (h *sessionHost) Command(ctx context.Context, line string, out io.Writer) app.CommandResult {
	if res, ok := h.programCommand(line, out); ok {
		return res
	}
	before := h.s.Model.ID
	quit, send := slashTo(ctx, h.s, line, out, out)
	res := app.CommandResult{Quit: quit, Send: send}
	if h.s.Model.ID != before {
		res.Model = h.s.Model.ID
	}
	return res
}

// programCommand runs the slash commands that act on the terminal program, not the session: /verbose and /anim change what is shown, and
// /restart and /swarm start the chat again with other flags (the ones that decide the shape of a session: a swarm, its isolation, its
// verifier, tool servers, trust), carrying the conversation over when it can be.
func (h *sessionHost) programCommand(line string, out io.Writer) (app.CommandResult, bool) {
	f := strings.Fields(line)
	onOff := func(name string) (string, bool) {
		if len(f) < 2 {
			return "on", true
		}
		switch strings.ToLower(f[1]) {
		case "on", "off":
			return strings.ToLower(f[1]), true
		}
		fmt.Fprintf(out, "usage: %s [on|off]\n", name)
		return "", false
	}
	switch f[0] {
	case "/verbose":
		if v, ok := onOff("/verbose"); ok {
			fmt.Fprintf(out, "verbose: %s (notices and tool errors %s)\n", v, map[string]string{"on": "are shown", "off": "show only warnings"}[v])
			return app.CommandResult{Verbose: v}, true
		}
		return app.CommandResult{}, true
	case "/anim":
		if v, ok := onOff("/anim"); ok {
			fmt.Fprintf(out, "animation: %s\n", v)
			return app.CommandResult{Anim: v}, true
		}
		return app.CommandResult{}, true
	case "/roles":
		if len(f) == 1 {
			fmt.Fprintln(out, "who runs on which model (change one with /roles role=provider/model; the chat restarts with it):")
			for _, r := range h.s.RoleModels() {
				fmt.Fprintf(out, "  %-10s %-44s %s\n", r.Role, r.Model, r.From)
			}
			return app.CommandResult{}, true
		}
		var typed []string
		for _, kv := range f[1:] {
			role, ref, ok := strings.Cut(kv, "=")
			if !ok || role == "" || ref == "" {
				fmt.Fprintln(out, "usage: /roles [role=provider/model ...]: show the table, or change roles (e.g. /roles manager=anthropic/claude-opus-5-5 compactor=ollama/qwen3:8b)")
				return app.CommandResult{}, true
			}
			if h.s.Swarm == nil && role != session.CompactorRole {
				fmt.Fprintf(out, "roles: a single agent runs on one model (/model); a team has roles: /swarm 8 first. Only the compactor can differ here: /roles compactor=%s\n", ref)
				return app.CommandResult{}, true
			}
			typed = append(typed, "--role-model", role+"="+ref)
		}
		args, err := restartArgs(h.s, typed, false)
		if err != nil {
			fmt.Fprintln(out, "roles:", err)
			return app.CommandResult{}, true
		}
		fmt.Fprintf(out, "restarting: sleipnir chat %s\n", strings.Join(args, " "))
		return app.CommandResult{Restart: args}, true
	case "/new", "/resume":
		var typed []string
		switch {
		case f[0] == "/resume" && len(f) > 1:
			typed = []string{"--resume", f[1]}
		case f[0] == "/resume":
			typed = []string{"--continue"}
		}
		args, err := restartArgs(h.s, typed, f[0] == "/new")
		if err != nil {
			fmt.Fprintln(out, f[0]+":", err)
			return app.CommandResult{}, true
		}
		fmt.Fprintf(out, "restarting: sleipnir chat %s\n", strings.Join(args, " "))
		return app.CommandResult{Restart: args}, true
	case "/restart", "/swarm":
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), f[0]))
		if f[0] == "/swarm" {
			if len(f) < 2 {
				fmt.Fprintln(out, "usage: /swarm <agents> [flags]: start again as a team of up to that many agents, the manager included, e.g. /swarm 8 --verify \"go test {dirs}\" --isolation worktree")
				return app.CommandResult{}, true
			}
			rest = "--swarm " + rest
		}
		args, err := restartArgs(h.s, splitArgs(rest), false)
		if err != nil {
			fmt.Fprintln(out, f[0]+":", err)
			return app.CommandResult{}, true
		}
		fmt.Fprintf(out, "restarting: sleipnir chat %s\n", strings.Join(args, " "))
		return app.CommandResult{Restart: args}, true
	}
	return app.CommandResult{}, false
}

// Mode is the permission mode in force.
func (h *sessionHost) Mode() string { return string(h.s.Perm.Mode()) }

// SetMode sets the mode and reports the one in force.
func (h *sessionHost) SetMode(mode string) string {
	h.s.Perm.SetMode(perm.Mode(mode))
	return string(h.s.Perm.Mode())
}

// chatCommand is a slash command of the chat's own, for the list the prompt completes from. The text of /help is chatHelp, and a
// test keeps the two together.
type chatCommand struct{ name, args, desc string }

var chatCommands = []chatCommand{
	{"help", "", "this text (and your custom commands and skills)"},
	{"status", "", "model, mode, session, budget and cost at a glance"},
	{"permissions", "", "the mode and the rules in force, among them what you allowed this session"},
	{"trust", "", "this project's own instructions and settings: what they are, and whether you trusted them"},
	{"cost", "", "tokens, cost and cache hit ratio so far"},
	{"context", "", "layer sizes of the current prompt"},
	{"compact", "[focus]", "fold the older thread now"},
	{"agents", "", "swarm board: agents and tasks"},
	{"model", "[provider/model]", "show the model, or move this conversation to another one (the prompt cache starts over)"},
	{"budget", "[usd|off]", "show or set the dollar budget for the turns from now on"},
	{"allow", "<rule>", "allow for the rest of this session what would otherwise ask: tests, Bash(go test:*), Edit(src/**)"},
	{"verbose", "[on|off]", "show or hide notices and tool errors"},
	{"anim", "[on|off]", "turn the motion on or off"},
	{"roles", "[role=model]", "which model each role runs on; change one (the chat restarts with it)"},
	{"new", "", "start the conversation again, empty (the same model and mode)"},
	{"resume", "[id]", "continue an earlier session: the newest of this project, or the id /sessions shows"},
	{"restart", "[flags]", "start the chat again with other flags: --no-mcp, --trust-project, --cwd DIR, ... (the conversation comes along when it can)"},
	{"swarm", "<n> [flags]", "start again as a team of n agents, the manager included: /swarm 8 --verify \"go test {dirs}\" --isolation worktree"},
	{"sessions", "", "the newest sessions; resume one with sleipnir --resume <id>"},
	{"cwd", "", "the directory this session works in"},
	{"mode", "<m>", "default | accept-edits | plan | bypass"},
	{"plan", "", "plan mode: read-only"},
	{"rewind", "[id]", "list checkpoints, or restore files to before a turn"},
	{"diff", "<id>", "show what changed since a checkpoint"},
	{"recon", "", "show the project map pinned in the shared layer"},
	{"skills", "", "list the skills the model can load"},
	{"mcp", "", "MCP tool servers: state and tools"},
	{"exit", "", "quit (also Ctrl-D, or Ctrl-C twice at the prompt)"},
}

// chatSlashCommands is every slash command the session answers: the chat's own, then the custom commands, the skills a person may
// invoke and the prompts of the tool servers.
func chatSlashCommands(s *session.Session) []input.Command {
	var out []input.Command
	for _, c := range chatCommands {
		out = append(out, input.Command{Name: c.name, Args: c.args, Description: c.desc})
	}
	have := map[string]bool{}
	for _, c := range out {
		have[c.Name] = true
	}
	add := func(c input.Command) {
		if !have[c.Name] {
			have[c.Name] = true
			out = append(out, c)
		}
	}
	if reg := s.Commands(); reg != nil {
		for _, c := range reg.List() {
			add(input.Command{Name: c.Name, Args: c.ArgumentHint, Description: firstText(c.Description, 70)})
		}
	}
	if s.Skills != nil {
		for _, k := range s.Skills.Skills() {
			add(input.Command{Name: k.Name, Description: firstText(k.Summary(), 70)})
		}
	}
	prompts := s.MCPPrompts()
	sort.Slice(prompts, func(i, j int) bool { return prompts[i].Command < prompts[j].Command })
	for _, p := range prompts {
		add(input.Command{Name: strings.TrimPrefix(p.Command, "/"), Description: firstText(p.Description, 70)})
	}
	return out
}
