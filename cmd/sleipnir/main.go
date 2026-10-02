// Command sleipnir is a coding-agent harness built around a multi-layer
// prompt-cache engine that many agents share.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/probe"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Set at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	// First, before anything reads a key or starts a command: see docs/SECURITY.md. MoveKeys takes the
	// provider keys out of the environment, so no command the harness starts inherits them; every reader
	// goes through harden.Secret (harden.TestReadersOfCredentialsUseSecret).
	harden.Process(harden.MoveKeys("HF_TOKEN")) // a key that does not end in API_KEY is named
	// Keys the person stored with `sleipnir login`: held in memory beside the ones moved out of the environment.
	if err := config.LoadStoredKeys(userHome()); err != nil {
		fmt.Fprintln(os.Stderr, "sleipnir:", err)
	}
	cmd, args := defaultToChat(os.Args[1:], term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())))
	if cmd == "" {
		usage(os.Stderr)
		os.Exit(2)
	}
	// Ctrl-C and SIGTERM cancel the process's context, and with it whatever a command is doing:
	// that is what Ctrl-C means for a command that does one thing. A command that does many
	// (chat: a turn at a time) handles Ctrl-C itself, and its context is cancelled by SIGTERM
	// only: a signal goes to every channel that asked for it, so registering here as well would
	// end the whole session when a turn was cancelled.
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if ownsInterrupt[cmd] {
		sigs = []os.Signal{syscall.SIGTERM}
	}
	ctx, caught, stop := interruptContext(sigs...)
	defer stop()

	var err error
	switch cmd {
	case "doctor":
		err = cmdDoctor(ctx, args)
	case "models":
		err = cmdModels(ctx, args)
	case "mock":
		err = cmdMock(ctx, args)
	case "version", "--version", "-v":
		fmt.Printf("sleipnir %s (%s)\n", version, commit)
	case "help", "--help", "-h":
		usage(os.Stdout) // asked for: it is the output, not a complaint
	default:
		if handler, ok := extraCommands[cmd]; ok {
			err = handler(ctx, args)
			break
		}
		fmt.Fprintf(os.Stderr, "sleipnir: unknown command %q (sleipnir -h lists the commands)\n", cmd)
		os.Exit(2)
	}
	if sig := caught(); sig != nil && errors.Is(err, context.Canceled) {
		// the command ended because it was told to: say so (not what the library says about a context), and end with the status a shell
		// gives a process that a signal stopped, so that a script can tell an interrupted run from one that failed
		fmt.Fprintln(os.Stderr, "sleipnir: interrupted")
		os.Exit(interruptStatus(sig))
	}
	if code := reportError(os.Stderr, err); code != 0 {
		os.Exit(code)
	}
}

// defaultToChat is the command and arguments for a command line: what was typed, except that on a terminal `sleipnir` alone, or
// followed by flags (`sleipnir --model heimdall/x`), opens the chat in the current directory. Where there is no terminal (a script, a
// pipe) the bare command stays a usage error, and "" says so.
func defaultToChat(argv []string, tty bool) (cmd string, args []string) {
	if len(argv) == 0 {
		if tty {
			return "chat", nil
		}
		return "", nil
	}
	switch argv[0] {
	case "-h", "--help", "-v", "--version":
	default:
		if strings.HasPrefix(argv[0], "-") && tty {
			return "chat", argv
		}
	}
	return argv[0], argv[1:]
}

// interruptContext is the context of a command that does one thing: Ctrl-C and SIGTERM cancel it. caught says which signal did, if
// one did. After the first signal the system has the signals back, so that a second Ctrl-C ends the process at once, as it does any
// other (a command that is slow to stop does not hold the terminal). stop gives the signals back without waiting for one.
func interruptContext(sigs ...os.Signal) (ctx context.Context, caught func() os.Signal, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	var (
		mu   sync.Mutex
		got  os.Signal
		done = make(chan struct{})
		once sync.Once
	)
	go func() {
		select {
		case s := <-ch:
			mu.Lock()
			got = s
			mu.Unlock()
			cancel()
			signal.Stop(ch)
			if s == os.Interrupt && term.IsTerminal(int(os.Stderr.Fd())) {
				fmt.Fprintln(os.Stderr, "\nsleipnir: interrupting (Ctrl-C again to quit at once)")
			}
		case <-done:
		}
	}()
	caught = func() os.Signal {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
	stop = func() {
		once.Do(func() {
			signal.Stop(ch)
			close(done)
			cancel()
		})
	}
	return ctx, caught, stop
}

// interruptStatus is the exit status of a process that a signal ended, by the shell's convention: 128 plus the signal's number (130
// for Ctrl-C, 143 for SIGTERM).
func interruptStatus(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 130
}

// reportError prints what a command returned and gives the process exit code: 0 for
// success, and for -h too (the flag package printed the usage: help is not a
// failure), 1 for an error, or the status an exitError names (exitcode.go). The
// message is printed with terminal control characters made harmless.
func reportError(w io.Writer, err error) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	fmt.Fprintln(w, wrapBlockFor(w, "sleipnir: "+tools.SanitizeForTerminal(err.Error())))
	if h := authHint(err, "`sleipnir login`"); h != "" {
		fmt.Fprintln(w, wrapBlockFor(w, h))
	}
	var ee *exitError
	if errors.As(err, &ee) && ee.code > 0 {
		return ee.code
	}
	return 1
}

// authHint says what to do about a key the provider refused: enter it again (how names the way to, where the person is: `sleipnir login` on
// the command line, /login in the chat), and check whether an environment variable is the one in use (it wins over the stored key, so a stale
// export is the usual reason a fresh login seems to change nothing). Empty for any other error, and for a ChatGPT sign-in that has ended: that
// error says so itself.
func authHint(err error, how string) string {
	if pe, ok := provider.AsError(err); ok && pe.Kind == provider.ErrAuth && !chatgptauth.IsSignInError(err) {
		return "The provider refused the key. Enter it again with " + how + "; if a key variable is set in your environment it wins over the stored one (`env | grep API_KEY`)."
	}
	return ""
}

// extraCommands lets other files in this package register subcommands.
var extraCommands = map[string]func(context.Context, []string) error{}

// ownsInterrupt names the commands that handle Ctrl-C themselves (chat.go registers chat); the
// process's context is not cancelled by SIGINT for them.
var ownsInterrupt = map[string]bool{}

func usage(w io.Writer) {
	fmt.Fprint(w, `sleipnir - a team of eight coding agents that share one prompt cache

Start:
  cd your-project && sleipnir
      The first run asks which provider to use (Heimdall is the recommended
      one), takes its key and opens the chat: a manager and seven workers.
      In the chat, ctrl+g shows the agents, ctrl+t the stats, /help the rest.
  sleipnir run "fix the failing test"
      One goal, no chat (sleipnir swarm 8 "..." runs the whole team).

Usage:
  sleipnir <command> [flags]

Every day:
  chat      interactive session: what "sleipnir" alone opens on a terminal
  run       run a goal (one agent; --swarm N for a manager with workers)
  swarm     shorthand for run --swarm: sleipnir swarm <agents> "<goal>"
  login     store a key, or sign in with your ChatGPT plan; logout removes it
  models    list models and prices from a marketplace catalogue
  sessions  list recorded sessions (sessions prune: delete the old ones)

Set up a project:
  init      write a starter config and AGENTS.md for this project
  config    show the effective configuration and where each value came from
  trust     a project's own instructions and settings: show, remember your yes
  mcp       tool servers (Model Context Protocol): list, approve, revoke, test
  schedule  goals to run on a schedule (cron): add, list, rm
  daemon    run the scheduled goals that are due (--once for a cron job)

Look at a session, or measure:
  watch     a session as it is written: the swarm cockpit, caches, mail, board
  replay    play a recorded session back; --record writes an SVG, --final text
  inspect   the cache inspector: a dashboard of a session, live or recorded
  friction  rank what slowed recorded sessions down: refusals, errors, retries
  doctor    probe an endpoint: streaming, tools, prefix cache, warm-up
  demo      a scripted team on a mock endpoint: the shared cache and the bill
  sim       simulate cache policies: what layering buys, and where it stops
  recon     print the project survey that seeds the shared prompt layer
  rl        the RL environment: rollout, eval, report, export (rl help)
  mock      the built-in mock provider, for demos and tests
  version   print version

`)
	fmt.Fprint(w, providerParagraph())
	fmt.Fprintln(w, wrapWords("A bare model id goes to the default provider: the only one configured, else the first hosted one whose key variable "+
		"(HEIMDALL_API_KEY, OPENROUTER_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, ...) is set. A key comes from its environment variable, else from "+
		"~/.sleipnir/auth.json (sleipnir login stores it there, mode 0600). Every command takes -h.", usageWidth))
}

// usageWidth is the widest line of the usage text: most terminal windows open at 80 columns, and a longer line is broken in the middle of a word.
const usageWidth = 78

// providerParagraph says which providers are built in, for the usage text: from the table itself, so that it cannot fall behind it.
func providerParagraph() string {
	var hosted, local, plan []string
	for _, n := range session.ProviderNames(nil) {
		p, _ := session.LookupProvider(nil, n)
		switch {
		case p.Auth == config.AuthChatGPTPlan:
			plan = append(plan, n)
		case p.APIKeyEnv == "":
			local = append(local, n)
		case n == "heimdall":
			hosted = append([]string{"heimdall (recommended)"}, hosted...)
		default:
			hosted = append(hosted, n)
		}
	}
	text := "A model is written provider/model. Built in: " + strings.Join(hosted, ", ") +
		"; your ChatGPT plan (" + strings.Join(plan, ", ") + ": sleipnir login chatgpt, no key); and the local servers " + strings.Join(local, ", ") +
		" (no key); or a provider from your config."
	return wrapWords(text, usageWidth) + "\n\n"
}

// wrapWords breaks text into lines of at most width characters, at spaces.
func wrapWords(text string, width int) string {
	var b strings.Builder
	line := 0
	for _, w := range strings.Fields(text) {
		switch {
		case line == 0:
		case line+1+len(w) > width:
			b.WriteByte('\n')
			line = 0
		default:
			b.WriteByte(' ')
			line++
		}
		b.WriteString(w)
		line += len(w)
	}
	return b.String()
}

// wrapBlock breaks text at spaces so that no line is wider than width. A paragraph written by hand in lines of about the width, one of
// which is a little too long, is flowed again as a whole (a lone word is not left on a line of its own); text that fits is left exactly as
// it was. A line that goes on is indented under where its own text began: after its indentation, or after a name and the padding behind
// it ("  taskgen    make tasks ..."), so that a table keeps its columns. A word wider than width stays whole.
func wrapBlock(text string, width int) string {
	if width < 20 {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && continuesParagraph(lines[j-1], lines[j], width) {
			j++
		}
		run, wide := lines[i:j], false
		for _, l := range run {
			wide = wide || utf8.RuneCountInString(l) > width
		}
		switch {
		case !wide:
			out = append(out, run...)
		case len(run) == 1:
			out = append(out, wrapLine(run[0], width))
		default:
			lead := run[0][:len(run[0])-len(strings.TrimLeft(run[0], " "))]
			out = append(out, wrapLine(lead+strings.Join(strings.Fields(strings.Join(run, " ")), " "), width))
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// continuesParagraph says whether next goes on from prev: both are running text with the same indentation, and prev is long enough to
// have been broken by hand at the margin (a list of short lines is not one paragraph).
func continuesParagraph(prev, next string, width int) bool {
	if utf8.RuneCountInString(prev) < width*3/5 || strings.TrimSpace(next) == "" {
		return false
	}
	indent := func(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }
	return indent(prev) == indent(next) && isProse(prev) && isProse(next)
}

var listItem = regexp.MustCompile(`^([-*] |\d+[.)] )`)

// isProse is false for a line with columns in it ("name   text"), an item of a list and a usage line.
func isProse(line string) bool {
	body := strings.TrimLeft(line, " ")
	return !strings.Contains(body, "  ") && !listItem.MatchString(body) && !strings.HasPrefix(body, "usage: ")
}

func wrapLine(line string, width int) string {
	if utf8.RuneCountInString(line) <= width {
		return line
	}
	body := strings.TrimLeft(line, " ")
	hang := len(line) - len(body)           // under the line's own first word
	if strings.HasPrefix(body, "usage: ") { // a usage line goes on under the command, not under "usage:"
		hang += len("usage: ")
	} else if item := listItem.FindString(body); item != "" { // the item of a list, under its text
		hang += len(item)
	} else if i := strings.Index(body, "  "); i > 0 && i <= 24 {
		if rest := strings.TrimLeft(body[i:], " "); rest != "" { // a name ("add [--yes]"), its padding, and the text it describes
			hang = len(line) - len(rest)
		}
	}
	if hang > width/2 {
		hang = len(line) - len(body)
	}
	parts := strings.Split(wrapWords(line[hang:], width-hang), "\n")
	parts[0] = line[:hang] + parts[0]
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.Repeat(" ", hang) + parts[i]
	}
	return strings.Join(parts, "\n")
}

// wrapBlockFor is text with its lines fitted to the terminal out is (wrapBlock), and as it was when out is not one: a pipe, a file and
// the generator of docs/CLI.md get the text the program wrote.
func wrapBlockFor(out io.Writer, text string) string {
	if w := termWidth(out); w > 20 {
		return wrapBlock(text, w-1)
	}
	return text
}

// printHelp writes help text to w, its lines fitted to the terminal when w is one.
func printHelp(w io.Writer, text string) { fmt.Fprint(w, wrapBlockFor(w, text)) }

func addProviderFlags(fs *flag.FlagSet) *providerFlags {
	pf := &providerFlags{}
	fs.StringVar(&pf.provider, "provider", "", "heimdall | openrouter | openai | custom (default: auto-detect)")
	fs.StringVar(&pf.baseURL, "base-url", "", "API base URL (overrides the provider default)")
	fs.StringVar(&pf.keyEnv, "api-key-env", "", "environment variable holding the API key")
	return pf
}

func cmdDoctor(ctx context.Context, args []string) error {
	fs := newFlagSet("doctor", flag.ExitOnError)
	pf := addProviderFlags(fs)
	model := fs.String("model", "", "model to probe: provider/model, or a bare id for the default provider (required)")
	deep := fs.Bool("deep", false, "also measure cache granularity, minimum prefix and warm-up needs (more requests)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	noKey := fs.Bool("no-affinity", false, "do not send a conversation/cache key")
	capture := fs.Bool("capture", false, "also check token-id capture (self-hosted policy servers; RL data)")
	trust := fs.Bool("trust-project", false, "apply provider settings from the project's config (they are ignored by default)")
	fs.Parse(args)
	if *model == "" {
		return fmt.Errorf("doctor: --model is required, as in `sleipnir doctor --model heimdall/MODEL` (`sleipnir models` lists them; the key comes from `sleipnir login` or the provider's environment variable)")
	}
	rec := &headerRecorder{}
	var client provider.Provider
	probeModel, where := *model, ""
	if pf.baseURL == "" && pf.provider == "" {
		// Same resolution as `run`: configured providers, then the built-ins.
		cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: !*trust})
		if err != nil {
			return err
		}
		ref, err := session.ResolveModel(cfg, *model)
		if err != nil {
			return err
		}
		c, _, err := session.BuildProvider(cfg, ref, session.ProviderOptions{CaptureTokens: *capture, OnHeaders: rec.set})
		if err != nil {
			return err
		}
		client, probeModel = c, ref.Model
		base, keyEnv, _ := session.ProviderInfo(cfg, ref.Provider)
		where = fmt.Sprintf("%s (%s, key from %s)", ref.Provider, base, envLabel(providerSpec{keyEnv: keyEnv}))
	} else {
		// Explicit endpoint flags: a one-off custom provider.
		spec, key, err := pf.resolve()
		if err != nil {
			return err
		}
		if key == "" && spec.keyEnv != "" {
			return fmt.Errorf("doctor: %s is not set", spec.keyEnv)
		}
		c := newClient(spec, key, rec)
		if *capture {
			prof := c.Profile()
			prof.CaptureTokens = true
			c.SetProfile(prof)
		}
		client = c
		where = fmt.Sprintf("%s (key from %s)", spec.baseURL, envLabel(spec))
	}
	fmt.Fprintf(os.Stderr, "probing %s at %s\n", probeModel, where)
	rep, err := probe.Run(ctx, probe.Config{
		Provider: client, Model: probeModel, Deep: *deep, Headers: rec.get, CacheKey: !*noKey, Capture: *capture,
		Log: func(s string) { fmt.Fprintln(os.Stderr, s) },
	})
	if err != nil && rep == nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if eerr := enc.Encode(rep); eerr != nil {
			return eerr
		}
	} else {
		fmt.Println()
		fmt.Print(rep.Text())
	}
	if err != nil {
		return err
	}
	// docs/CLI.md: doctor exits 1 if the probe fails. The report says what was found either way.
	if ferr := rep.Failure(); ferr != nil {
		return fmt.Errorf("doctor: %w", ferr)
	}
	return nil
}

func envLabel(s providerSpec) string {
	if s.keyEnv == "" {
		return "none"
	}
	return "$" + s.keyEnv
}

func cmdMock(ctx context.Context, args []string) error {
	fs := newFlagSet("mock", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8089", "listen address")
	engines := fs.Int("engines", 2, "number of independent engines (each with its own prefix cache)")
	fs.Parse(args)
	srv := mock.New(mock.Config{Engines: *engines, Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, nil)
	hs := &http.Server{Addr: *addr, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "mock provider on http://%s (%d engines); use --provider custom --base-url http://%s\n", *addr, *engines, *addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
