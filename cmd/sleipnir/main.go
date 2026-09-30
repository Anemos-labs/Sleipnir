// Command sleipnir is a coding-agent harness built around a multi-layer
// prompt-cache engine that many agents share.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/gateway"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/probe"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/tools/shell"
)

// Set at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	// Make this process non-dumpable so the commands it runs for agents cannot read
	// its environment (provider API keys) through /proc/<ppid>/environ.
	_ = shell.HardenProcess()
	if len(os.Args) < 2 {
		usage()
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
		usage()
	default:
		if handler, ok := extraCommands[cmd]; ok {
			err = handler(ctx, args)
			break
		}
		fmt.Fprintf(os.Stderr, "sleipnir: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sleipnir:", err)
		os.Exit(1)
	}
}

// extraCommands lets other files in this package register subcommands.
var extraCommands = map[string]func(context.Context, []string) error{}

func usage() {
	fmt.Fprint(os.Stderr, `sleipnir - a coding-agent harness with a shared multi-layer prompt cache

Usage:
  sleipnir <command> [flags]

Commands:
  init      write a starter .sleipnir/config.json and AGENTS.md for this project
  config    show the effective configuration and where each value came from
  sessions  list recorded sessions
  chat      interactive session (slash commands, Ctrl-C cancels a turn)
  run       run a goal through the harness (single agent; --swarm N for a manager with workers)
  swarm     shorthand for run --swarm
  recon     print the deterministic project survey that seeds the shared prompt layer
  doctor    probe an endpoint: streaming, tools, prefix-cache behaviour, warm-up needs
  models    list models and prices from a marketplace catalogue
  mock      run the built-in mock provider (deterministic, cache-faithful) for demos and tests
  sim       simulate cache policies: what layering buys and where it stops paying
  version   print version

Providers are selected with --provider (heimdall, openrouter, openai, custom) or
auto-detected from HEIMDALL_API_KEY / OPENROUTER_API_KEY / OPENAI_API_KEY.
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
