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
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

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
	harden.Process(harden.MoveKeys())
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
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
		fmt.Fprintf(os.Stderr, "sleipnir: unknown command %q\n\n", cmd)
		usage(os.Stderr)
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
	fmt.Fprintln(w, "sleipnir:", tools.SanitizeForTerminal(err.Error()))
	var ee *exitError
	if errors.As(err, &ee) && ee.code > 0 {
		return ee.code
	}
	return 1
}

// extraCommands lets other files in this package register subcommands.
var extraCommands = map[string]func(context.Context, []string) error{}

// ownsInterrupt names the commands that handle Ctrl-C themselves (chat.go registers chat); the
// process's context is not cancelled by SIGINT for them.
var ownsInterrupt = map[string]bool{}

func usage(w io.Writer) {
	fmt.Fprint(w, `sleipnir - a coding-agent harness with a shared multi-layer prompt cache

Usage:
  sleipnir <command> [flags]

Commands:
  init      write a starter .sleipnir/config.json and AGENTS.md for this project
  config    show the effective configuration and where each value came from
  sessions  list recorded sessions (sessions prune: delete the old ones)
  chat      interactive session (slash commands, Ctrl-C cancels a turn)
  run       run a goal through the harness (single agent; --swarm N for a manager with workers)
  swarm     shorthand for run --swarm: sleipnir swarm <workers> "<goal>"
  recon     print the deterministic project survey that seeds the shared prompt layer
  mcp       tool servers (Model Context Protocol): list, approve, revoke, test
  trust     the project's own instructions and settings: what they are, and remember your yes until they change
  inspect   the cache inspector: a live or after-the-fact dashboard of a recorded session (layers, hit ratio, swarm, cost)
  watch     the terminal's view of a session as it is written: the swarm cockpit, each agent's cache, mail, board
  replay    play a recorded session back on those screens; --record writes an animated SVG, --final a text screen
  rl        the RL environment: taskgen, rollout, eval, serve, reward, report, compare, export, verify (see: sleipnir rl help)
  friction  rank what slowed recorded sessions down: refusals, failed tool calls, stuck runs, retries, repeated reads
  doctor    probe an endpoint: streaming, tools, prefix-cache behaviour, warm-up needs
  models    list models and prices from a marketplace catalogue
  demo      a scripted team of 10+ agents on a mock endpoint: see the shared cache and the bill, no key needed
  mock      run the built-in mock provider (deterministic, cache-faithful) for demos and tests
  sim       simulate cache policies: what layering buys and where it stops paying
  version   print version

A model is written provider/model. Built in: heimdall (recommended), openrouter, openai, anthropic,
together, fireworks, groq, cerebras, deepinfra, and the local servers ollama, lmstudio, llamacpp and
vllm (no key); or a provider from your config. A bare model id goes to the default provider: the only
one configured, else the first hosted one whose key variable (HEIMDALL_API_KEY, OPENROUTER_API_KEY,
OPENAI_API_KEY, ANTHROPIC_API_KEY, ...) is set. Keys are read from the environment only, never from a file. Every command takes -h.
`)
}

func addProviderFlags(fs *flag.FlagSet) *providerFlags {
	pf := &providerFlags{}
	fs.StringVar(&pf.provider, "provider", "", "heimdall | openrouter | openai | custom (default: auto-detect)")
	fs.StringVar(&pf.baseURL, "base-url", "", "API base URL (overrides the provider default)")
	fs.StringVar(&pf.keyEnv, "api-key-env", "", "environment variable holding the API key")
	return pf
}

func cmdDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	pf := addProviderFlags(fs)
	model := fs.String("model", "", "model to probe: provider/model, or a bare id for the default provider (required)")
	deep := fs.Bool("deep", false, "also measure cache granularity, minimum prefix and warm-up needs (more requests)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	noKey := fs.Bool("no-affinity", false, "do not send a conversation/cache key")
	capture := fs.Bool("capture", false, "also check token-id capture (self-hosted policy servers; RL data)")
	trust := fs.Bool("trust-project", false, "apply provider settings from the project's config (they are ignored by default)")
	fs.Parse(args)
	if *model == "" {
		return fmt.Errorf("doctor: --model is required (see `sleipnir models`)")
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
	fs := flag.NewFlagSet("mock", flag.ExitOnError)
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
