package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// chatFlagSet is the flags of `sleipnir chat` registered on a flag set: one definition for the command itself (cmdChat), for the
// in-process restarts of `sleipnir web` (restartArgs, then parseChatFlags) and for its New session dialog (argsFor, then
// parseChatFlags), so that the three accept the same words with the same defaults and messages.
type chatFlagSet struct {
	model, cwd, mode, verify *string
	swarmN                   *int
	trust, verbose, noMCP    *bool
	budget                   *float64
	isolation                *string
	commit                   *bool
	mailman                  func() *bool
	roleModels               kvFlags
	resume                   func() (string, error)
	allow                    *allowFlag
	plain, noAnim            *bool
}

// registerChatFlags defines every flag of `sleipnir chat` on fs, with the descriptions the help prints.
func registerChatFlags(fs *flag.FlagSet) *chatFlagSet {
	f := &chatFlagSet{}
	f.model = fs.String("model", "", "model: provider/model or a bare id for the default provider")
	f.cwd = fs.String("cwd", "", "working directory")
	f.mode = fs.String("mode", "", "permissions: default | accept-edits | plan | bypass | yolo")
	f.swarmN = fs.Int("swarm", 0, "chat as a team of a manager and N workers (config swarm.max_workers is the ceiling); on a terminal the default is "+strconv.Itoa(defaultWorkers)+" workers, --swarm 0 is a single agent")
	f.trust = fs.Bool("trust-project", false, trustProjectHelp)
	f.verbose = fs.Bool("verbose", false, "print notices and tool errors")
	f.budget = fs.Float64("budget-usd", 0, "stop when spend reaches this many US dollars")
	f.noMCP = fs.Bool("no-mcp", false, "start no MCP tool servers")
	f.verify = fs.String("verify", "", "swarm: command the harness runs before a worker's task may leave 'doing' (with --isolation worktree, also on every merge). {dirs} in it stands for the directories the task may touch (./... without a scope), so that each task is verified on its own work: 'go test {dirs}'")
	f.isolation, f.commit = isolationFlags(fs)
	f.mailman = mailmanFlag(fs)
	f.roleModels = kvFlags{}
	fs.Var(f.roleModels, "role-model", "role=model override, repeatable (e.g. manager=heimdall/x, mailman=heimdall/small)")
	f.resume = resumeFlags(fs)
	f.allow = allowFlags(fs)
	f.plain = fs.Bool("plain", false, "plain lines, as when the input or the output is not a terminal: no colour, no status line, no redrawing, approvals typed as y, a or n")
	f.noAnim = fs.Bool("no-anim", false, "no animation: the spinner stands still, and nothing sweeps, folds or flashes (also SLEIPNIR_ANIM=0, REDUCE_MOTION=1 and NO_COLOR)")
	return f
}

// chatFlags is a parsed `sleipnir chat` command line.
type chatFlags struct {
	model, cwd, mode, verify, isolation string
	// workers is --swarm N; swarmGiven says whether the line had --swarm (without it the caller's default applies).
	workers    int
	swarmGiven bool
	trust      bool
	verbose    bool
	noMCP      bool
	budget     float64
	commit     bool
	mailman    *bool
	roleModels map[string]string
	resume     string
	allow      []string // as typed: the name tests is not expanded
	plain      bool
	noAnim     bool
}

// values reads what was parsed into a chatFlags. fs is the set the flags were registered on.
func (f *chatFlagSet) values(fs *flag.FlagSet) (chatFlags, error) {
	spec, err := f.resume()
	if err != nil {
		return chatFlags{}, err
	}
	out := chatFlags{
		model: *f.model, cwd: *f.cwd, mode: *f.mode, verify: *f.verify, isolation: *f.isolation,
		workers: *f.swarmN, trust: *f.trust, verbose: *f.verbose, noMCP: *f.noMCP, budget: *f.budget, commit: *f.commit,
		mailman: f.mailman(), resume: spec, allow: append([]string(nil), (*f.allow)...), plain: *f.plain, noAnim: *f.noAnim,
	}
	if len(f.roleModels) > 0 {
		out.roleModels = make(map[string]string, len(f.roleModels))
		for k, v := range f.roleModels {
			out.roleModels[k] = v
		}
	}
	fs.Visit(func(fl *flag.Flag) { out.swarmGiven = out.swarmGiven || fl.Name == "swarm" })
	return out, nil
}

// parseChatFlags parses chat arguments with the same defaults, validation and messages as `sleipnir chat` (ContinueOnError: an
// error is returned, nothing is printed and nothing exits). It also refuses what `sleipnir chat` would refuse only when the session
// starts (an unknown mode or isolation, a negative budget or team), and positional arguments, which the chat does not take.
func parseChatFlags(args []string) (chatFlags, error) {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	set := registerChatFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return chatFlags{}, errors.New("-h asks for help; it is not a session setting")
		}
		return chatFlags{}, err
	}
	if fs.NArg() > 0 {
		return chatFlags{}, fmt.Errorf("only flags are taken here, got %q", fs.Arg(0))
	}
	f, err := set.values(fs)
	if err != nil {
		return chatFlags{}, err
	}
	if f.mode != "" {
		if err := oneOf("mode", f.mode, "default", "accept-edits", "plan", "bypass", "yolo"); err != nil {
			return chatFlags{}, err
		}
	}
	if f.isolation != "" {
		if err := oneOf("isolation", f.isolation, "none", "worktree"); err != nil {
			return chatFlags{}, err
		}
	}
	if f.budget < 0 || f.budget != f.budget {
		return chatFlags{}, errors.New("--budget-usd must not be negative")
	}
	if f.workers < 0 {
		return chatFlags{}, errors.New("--swarm must not be negative")
	}
	return f, nil
}

// options turns parsed flags into session options for an interactive session (chatOptions), the way cmdChat does: --swarm N is a
// manager and N workers, 0 a single agent, and the named sets of --allow are expanded.
func (f chatFlags) options() session.Options {
	team, workers := teamOf(f.workers)
	var rm map[string]string
	if len(f.roleModels) > 0 {
		rm = make(map[string]string, len(f.roleModels))
		for k, v := range f.roleModels {
			rm[k] = v
		}
	}
	return chatOptions(session.Options{
		Cwd: f.cwd, Model: f.model, Mode: perm.Mode(f.mode), Swarm: team, Workers: workers,
		TrustProject: f.trust, BudgetUSD: f.budget, Resume: f.resume, NoMCP: f.noMCP,
		Verify: f.verify, Isolation: f.isolation, Commit: f.commit, Mailman: f.mailman, RoleModels: rm,
		Allow: expandAllow(f.allow),
	})
}

// newSessionRequest is the body of POST /api/sessions: the New session dialog's fields (wire.NewSessionRequest), the session it
// resumes with those settings (Resume), and the page's idempotency key.
type newSessionRequest struct {
	wire.NewSessionRequest
	Resume   string `json:"resume,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// argsFor renders the New session dialog's request as chat arguments, in the order the dialog's command line shows them: --cwd,
// --model, --mode, --swarm, --isolation, --verify, --commit, --mailman, --budget-usd, --allow, --trust-project, --no-mcp, then
// --role-model in the order of the roles' names. The name, the first goal and the effort are not chat flags: the host applies them
// once the session has started.
func argsFor(req wire.NewSessionRequest) []string {
	var a []string
	if req.Cwd != "" {
		a = append(a, "--cwd", req.Cwd)
	}
	if req.Model != "" {
		a = append(a, "--model", req.Model)
	}
	if req.Mode != "" {
		a = append(a, "--mode", req.Mode)
	}
	if req.Swarm != nil {
		a = append(a, "--swarm", strconv.Itoa(*req.Swarm))
	}
	if req.Isolation != "" {
		a = append(a, "--isolation", req.Isolation)
	}
	if req.Verify != "" {
		a = append(a, "--verify", req.Verify)
	}
	if req.Commit {
		a = append(a, "--commit")
	}
	if req.Mailman {
		a = append(a, "--mailman")
	}
	if req.Budget != nil && *req.Budget != 0 {
		a = append(a, "--budget-usd", strconv.FormatFloat(*req.Budget, 'f', -1, 64))
	}
	for _, r := range req.Rules {
		if strings.TrimSpace(r) != "" {
			a = append(a, "--allow", r)
		}
	}
	if req.TrustProject {
		a = append(a, "--trust-project")
	}
	if req.NoMcp {
		a = append(a, "--no-mcp")
	}
	roles := make([]string, 0, len(req.RoleModels))
	for role := range req.RoleModels {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		a = append(a, "--role-model", role+"="+req.RoleModels[role])
	}
	return a
}

// commandLine is a chat argument list as a person would type it after `sleipnir chat`: words with a space, a quote or nothing in them
// are quoted.
func commandLine(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"{}$*?;&|<>()") {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
			continue
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}
