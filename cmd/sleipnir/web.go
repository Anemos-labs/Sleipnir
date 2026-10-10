package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/web"
)

// init registers the browser interface command with the CLI dispatcher.
func init() { extraCommands["web"] = cmdWeb }

// webDefaults are the chat flags given to `sleipnir web`. They are the defaults of the sessions
// that are started from the page; the page may change them per session. The session host
// (web_host.go) reads them through webHost.
type webDefaults struct {
	// Cwd is the absolute working directory sessions start in.
	Cwd string
	// Model is --model: provider/model, or "" for the configured default.
	Model string
	// Mode is --mode, validated; "" leaves the configuration in charge, as in the chat.
	Mode string
	// Workers is the team size of a new session: a manager and this many workers, 0 for a single
	// agent. Without --swarm it is the chat's default on a terminal, 8 workers, kept under
	// config swarm.max_workers.
	Workers int
	// WorkersGiven reports whether --swarm was on the command line.
	WorkersGiven bool
	// TrustProject is --trust-project: apply the project's security-sensitive configuration.
	TrustProject bool
	// BudgetUSD is --budget-usd; 0 leaves the configuration in charge.
	BudgetUSD float64
	// NoMCP is --no-mcp.
	NoMCP bool
	// Verify is --verify.
	Verify string
	// Isolation is --isolation, "none", "worktree" or "" (the configuration decides).
	Isolation string
	// Commit is --commit.
	Commit bool
	// Mailman is --mailman: nil when the flag was not given.
	Mailman *bool
	// RoleModels is the --role-model overrides.
	RoleModels map[string]string
	// Allow is the --allow rules with the named sets (tests) expanded.
	Allow []string
	// Resume is --resume or --continue as session.Options.Resume takes it: "", an id, a directory
	// or "latest".
	Resume string
}

// webHost attaches the session host to the web server. The code that implements sessions sets it
// (in an init function of web_host.go); when it is nil the server serves the page and the
// built-in routes only. It is called once, before the server is created, with the context of the
// command (cancelled on Ctrl-C), the defaults and a log function that masks credentials. It
// returns the function that registers the host's routes (web.Config.Routes) and a function that
// closes the host; the command calls the latter after the server has stopped.
var webHost func(ctx context.Context, d webDefaults, logf func(format string, args ...any)) (routes func(*web.Server), closeHost func(), err error)

// cmdWeb serves the browser interface: sessions, workspace and settings over HTTP on loopback.
func cmdWeb(ctx context.Context, args []string) error {
	fs := newFlagSet("web", flag.ExitOnError)
	addr := fs.String("addr", web.DefaultAddr, "listen address; only a loopback address is accepted (127.0.0.1:0 picks a free port)")
	open := fs.Bool("open", false, "open the page in the default browser")
	cwd := fs.String("cwd", "", "working directory of the sessions started in the page (default: the current directory)")
	model := fs.String("model", "", "default model of new sessions: provider/model or a bare id for the default provider")
	mode := fs.String("mode", "", "default permissions of new sessions: default | accept-edits | plan | bypass | yolo")
	swarmN := fs.Int("swarm", 0, "new sessions are a team of a manager and N workers (config swarm.max_workers is the ceiling); the default is "+strconv.Itoa(defaultWorkers)+" workers, --swarm 0 is a single agent")
	trust := fs.Bool("trust-project", false, trustProjectHelp)
	budget := fs.Float64("budget-usd", 0, "default spending limit of new sessions, in US dollars")
	noMCP := fs.Bool("no-mcp", false, "start no MCP tool servers in new sessions")
	verify := fs.String("verify", "", "swarm: command the harness runs before a worker's task may leave 'doing' (see sleipnir chat -h); {dirs} stands for the directories the task may touch")
	isolation, commit := isolationFlags(fs)
	mailman := mailmanFlag(fs)
	roleModels := kvFlags{}
	fs.Var(roleModels, "role-model", "role=model override for new sessions, repeatable (e.g. manager=heimdall/x, mailman=heimdall/small)")
	resume := resumeFlags(fs)
	allow := allowFlags(fs)
	fs.Usage = func() {
		if !helpAsked(os.Args) { // a flag that does not exist: the one line that says so, not forty lines of flags
			fmt.Fprintln(os.Stderr, "(sleipnir web -h lists the flags)")
			return
		}
		printHelp(os.Stderr, `usage: sleipnir web [flags]

Serves the interface in a browser, on this machine: chat sessions with a
manager and workers, approvals, the workspace and the settings. The first line
printed on standard output is the address to open. It carries a token that is
valid for this run only; the page exchanges it for a session cookie and
removes it from the address bar.

The server binds to loopback and refuses any other address. It runs the same
sessions, with the same permissions, as the terminal chat, with your rights: a
process or a person who can read that address can act as you. To use it from
another machine, forward the port with SSH.

The chat flags are the defaults of the sessions started in the page.

flags:
`)
		printFlags(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, fmt.Sprintf("web: takes no arguments, got %q", fs.Arg(0)))
	}
	if *mode != "" {
		if err := oneOf("mode", *mode, "default", "accept-edits", "plan", "bypass", "yolo"); err != nil {
			return usageError(fs, "web: "+err.Error())
		}
	}
	if *isolation != "" {
		if err := oneOf("isolation", *isolation, "none", "worktree"); err != nil {
			return usageError(fs, "web: "+err.Error())
		}
	}
	if *budget < 0 {
		return usageError(fs, "web: --budget-usd must not be negative")
	}
	if *swarmN < 0 {
		return usageError(fs, "web: --swarm must not be negative")
	}
	resumeSpec, err := resume()
	if err != nil {
		return usageError(fs, "web: "+err.Error())
	}
	dir, err := webCwd(*cwd)
	if err != nil {
		return err
	}
	d := webDefaults{
		Cwd: dir, Model: *model, Mode: *mode, Workers: defaultTeam(), TrustProject: *trust, BudgetUSD: *budget,
		NoMCP: *noMCP, Verify: *verify, Isolation: *isolation, Commit: *commit, Mailman: mailman(),
		RoleModels: roleModels, Allow: expandAllow(*allow), Resume: resumeSpec,
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "swarm" {
			d.Workers, d.WorkersGiven = *swarmN, true
		}
	})

	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "sleipnir web: "+f+"\n", a...) }
	cfg := web.Config{Addr: *addr, Logf: logf}
	if webHost != nil {
		routes, closeHost, err := webHost(ctx, d, logf)
		if err != nil {
			return err
		}
		if closeHost != nil {
			defer closeHost()
		}
		cfg.Routes = routes
	}
	srv, err := web.New(cfg)
	if err != nil {
		return err
	}
	ln, err := web.Listen(*addr, false)
	if err != nil {
		return err
	}
	u := srv.URL(ln.Addr())
	fmt.Println(u) // the first line of standard output, and the only place the token is printed
	fmt.Fprintf(os.Stderr, "sleipnir web: sessions started in the page work in %s\n", dir)
	fmt.Fprintln(os.Stderr, wrapFor(os.Stderr, "sleipnir web: the address above carries a token for this run only; the page keeps a session cookie and removes the token from the address bar"))
	if *open {
		if err := openBrowser(u); err != nil {
			fmt.Fprintf(os.Stderr, "sleipnir web: could not open a browser (%v); open the address above\n", err)
		}
	}
	fmt.Fprintln(os.Stderr, "sleipnir web: Ctrl-C to stop")
	return srv.Serve(ctx, ln)
}

// webCwd is the absolute working directory for sessions: --cwd, else the current directory. It
// must exist.
func webCwd(arg string) (string, error) {
	dir := arg
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("web: %w", err)
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("web: %w", err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("web: --cwd %s is not a directory", abs)
	}
	return abs, nil
}
