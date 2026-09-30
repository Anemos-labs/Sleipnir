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
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/harden"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/gateway"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/probe"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/tools"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
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
	if code := reportError(os.Stderr, err); code != 0 {
		os.Exit(code)
	}
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

func usage(w io.Writer) {
	fmt.Fprint(w, `sleipnir - a coding-agent harness with a shared multi-layer prompt cache

Usage:
  sleipnir <command> [flags]

Commands:
  init      write a starter .sleipnir/config.json and AGENTS.md for this project
  config    show the effective configuration and where each value came from
  sessions  list recorded sessions
  chat      interactive session (slash commands, Ctrl-C cancels a turn)
  run       run a goal through the harness (single agent; --swarm N for a manager with workers)
  swarm     shorthand for run --swarm: sleipnir swarm <workers> "<goal>"
  recon     print the deterministic project survey that seeds the shared prompt layer
  mcp       tool servers (Model Context Protocol): list, approve, revoke, test
  inspect   the cache inspector: a live or after-the-fact dashboard of a recorded session (layers, hit ratio, swarm, cost)
  rl        the RL environment: taskgen, rollout, eval, serve, reward, export, verify (see: sleipnir rl help)
  doctor    probe an endpoint: streaming, tools, prefix-cache behaviour, warm-up needs
  models    list models and prices from a marketplace catalogue
  demo      a scripted team of 10+ agents on a mock endpoint: see the shared cache and the bill, no key needed
  mock      run the built-in mock provider (deterministic, cache-faithful) for demos and tests
  sim       simulate cache policies: what layering buys and where it stops paying
  version   print version

A model is written provider/model (built in: heimdall, openrouter, openai; or a provider from your
config). A bare model id goes to the default provider: the only one configured, else the first of
those three whose key variable (HEIMDALL_API_KEY, OPENROUTER_API_KEY, OPENAI_API_KEY) is set. Keys
are read from the environment only, never from a file. Every command takes -h.
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
		return enc.Encode(rep)
	}
	fmt.Println()
	fmt.Print(rep.Text())
	return err
}

func envLabel(s providerSpec) string {
	if s.keyEnv == "" {
		return "none"
	}
	return "$" + s.keyEnv
}

func cmdModels(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	pf := addProviderFlags(fs)
	all := fs.Bool("all", false, "include non-chat models")
	filter := fs.String("filter", "", "only ids containing this text")
	fs.Parse(args)
	baseURL := ""
	if pf.baseURL != "" {
		baseURL = pf.baseURL
	} else {
		cfg, _, cerr := config.Load(config.LoadOpts{UntrustedProject: true})
		if cerr != nil {
			return cerr
		}
		name := strings.ToLower(pf.provider)
		if name == "" {
			if d, derr := session.DefaultProvider(cfg); derr == nil {
				name = d
			} else {
				name = "heimdall" // the catalogue is public
			}
		}
		b, _, ok := session.ProviderInfo(cfg, name)
		if !ok || b == "" {
			return fmt.Errorf("models: unknown provider %q", name)
		}
		baseURL = b
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entries, err := gateway.Fetch(cctx, &http.Client{Timeout: 30 * time.Second}, baseURL)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Model.ID < entries[j].Model.ID })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tCONTEXT\t$/M IN\t$/M CACHED\t$/M OUT\tTOOLS\tREASONING")
	for _, e := range entries {
		if !*all && !e.IsChat() {
			continue
		}
		if *filter != "" && !strings.Contains(e.Model.ID, *filter) {
			continue
		}
		p := e.Model.Price
		fmt.Fprintf(tw, "%s\t%s\t%.4f\t%.4f\t%.4f\t%v\t%v\n", e.Model.ID, human(e.Model.ContextTokens), p.InputPerM, p.CacheReadPerM, p.OutputPerM, e.SupportsTools(), e.SupportsReasoning())
	}
	return tw.Flush()
}

func human(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
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
