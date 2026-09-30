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

	"github.com/reee344/sleipnir/internal/provider/gateway"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/probe"
)

// Set at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
)

func main() {
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
	model := fs.String("model", "", "model id to probe (required)")
	deep := fs.Bool("deep", false, "also measure cache granularity, minimum prefix and warm-up needs (more requests)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	noKey := fs.Bool("no-affinity", false, "do not send a conversation/cache key")
	fs.Parse(args)
	if *model == "" {
		return fmt.Errorf("doctor: --model is required (see `sleipnir models`)")
	}
	spec, key, err := pf.resolve()
	if err != nil {
		return err
	}
	if key == "" && spec.keyEnv != "" {
		return fmt.Errorf("doctor: %s is not set", spec.keyEnv)
	}
	rec := &headerRecorder{}
	client := newClient(spec, key, rec)
	fmt.Fprintf(os.Stderr, "probing %s at %s (key from %s)\n", *model, spec.baseURL, envLabel(spec))
	rep, err := probe.Run(ctx, probe.Config{
		Provider: client, Model: *model, Deep: *deep, Headers: rec.get, CacheKey: !*noKey,
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
	spec, _, err := pf.resolve()
	if err != nil && pf.baseURL == "" && pf.provider == "" {
		spec = builtinProviders["heimdall"]
	} else if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entries, err := gateway.Fetch(cctx, &http.Client{Timeout: 30 * time.Second}, spec.baseURL)
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
