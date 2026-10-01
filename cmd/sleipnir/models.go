package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// modelRow is one listed model: Ref is what --model takes.
type modelRow struct {
	Ref string
	gateway.Entry
}

// modelFilter is the search of `sleipnir models`: every word must appear in the reference, and each flag narrows further.
type modelFilter struct {
	Words        []string
	Tools        bool
	Reasoning    bool
	MaxOut       float64 // $/M output tokens; 0 means no limit
	MinContext   int
	OnlyFavorite bool
	All          bool // include models that do not produce text
}

func (f modelFilter) keep(r modelRow, fav map[string]bool) bool {
	if !f.All && !r.IsChat() {
		return false
	}
	if f.Tools && !r.SupportsTools() || f.Reasoning && !r.SupportsReasoning() || f.OnlyFavorite && !fav[r.Ref] {
		return false
	}
	if f.MaxOut > 0 && r.Model.Price.OutputPerM > f.MaxOut || r.Model.ContextTokens < f.MinContext {
		return false
	}
	ref := strings.ToLower(r.Ref)
	for _, w := range f.Words {
		if !strings.Contains(ref, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

// parseTokens reads "128k", "1m" or "32768".
func parseTokens(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	switch {
	case strings.HasSuffix(t, "k"):
		mult, t = 1e3, t[:len(t)-1]
	case strings.HasSuffix(t, "m"):
		mult, t = 1e6, t[:len(t)-1]
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("want a token count such as 128k, got %q", s)
	}
	return int(v * mult), nil
}

// usableProviders are the providers `models` asks when none is named: those whose key is set, and Heimdall, whose
// catalogue is public. Local servers and providers without a catalogue route are asked only by name.
func usableProviders(cfg *config.Config) []string {
	var out []string
	for _, n := range session.ProviderNames(cfg) {
		p, ok := session.LookupProvider(cfg, n)
		if !ok || p.EffectiveDialect() != config.DialectOpenAIChat {
			continue
		}
		if n == "heimdall" || p.APIKeyEnv != "" && harden.Secret(p.APIKeyEnv) != "" {
			out = append(out, n)
		}
	}
	return out
}

func cmdModels(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "fav" {
		return cmdFavorites(os.Stdout, args[1:])
	}
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: sleipnir models [words...] [flags]
       sleipnir models fav [add|rm] provider/model...

Lists the models of every provider whose key is set (and Heimdall), as references that --model takes. Each word narrows the
search (all must appear, any case). Favorites, marked *, come first.

`)
		fs.PrintDefaults()
	}
	pf := addProviderFlags(fs)
	var f modelFilter
	fs.BoolVar(&f.All, "all", false, "include non-chat models")
	filter := fs.String("filter", "", "only ids containing this text (same as a word)")
	fs.BoolVar(&f.Tools, "tools", false, "only models that accept tools")
	fs.BoolVar(&f.Reasoning, "reasoning", false, "only models with configurable reasoning")
	fs.Float64Var(&f.MaxOut, "max-price", 0, "only models with an output price of at most this many dollars per million tokens")
	minCtx := fs.String("min-context", "", "only models with at least this context window (128k, 1m)")
	fs.BoolVar(&f.OnlyFavorite, "fav", false, "only favorites")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f.Words = fs.Args()
	if *filter != "" {
		f.Words = append(f.Words, *filter)
	}
	if *minCtx != "" {
		n, err := parseTokens(*minCtx)
		if err != nil {
			return fmt.Errorf("models: --min-context: %w", err)
		}
		f.MinContext = n
	}
	cfg, _, cerr := config.Load(config.LoadOpts{UntrustedProject: true})
	if cerr != nil {
		return cerr
	}

	// Which catalogues: the endpoint given, the provider named, or every usable provider.
	type source struct{ name, base string }
	var sources []source
	switch {
	case pf.baseURL != "":
		sources = []source{{"", pf.baseURL}}
	case pf.provider != "" && !strings.EqualFold(pf.provider, "all"):
		name := strings.ToLower(pf.provider)
		b, _, ok := session.ProviderInfo(cfg, name)
		if !ok || b == "" {
			return fmt.Errorf("models: unknown provider %q", name)
		}
		sources = []source{{name, b}}
	default:
		for _, n := range usableProviders(cfg) {
			b, _, _ := session.ProviderInfo(cfg, n)
			sources = append(sources, source{n, b})
		}
	}

	rows := make([][]modelRow, len(sources))
	errs := make([]error, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			es, err := gateway.Fetch(cctx, &http.Client{Timeout: 30 * time.Second}, src.base)
			errs[i] = err
			for _, e := range es {
				ref := e.Model.ID
				if src.name != "" {
					ref = src.name + "/" + ref
				}
				rows[i] = append(rows[i], modelRow{Ref: ref, Entry: e})
			}
		}()
	}
	wg.Wait()
	var all []modelRow
	failed := 0
	for i, src := range sources {
		if errs[i] != nil {
			failed++
			if len(sources) == 1 {
				return errs[i]
			}
			fmt.Fprintf(os.Stderr, "models: %s: %v\n", src.name, errs[i])
			continue
		}
		all = append(all, rows[i]...)
	}
	if len(sources) > 0 && failed == len(sources) {
		return fmt.Errorf("models: no catalogue could be fetched")
	}
	if len(sources) == 0 {
		return fmt.Errorf("models: no provider to ask: set HEIMDALL_API_KEY (or another provider's key), or pass --provider")
	}
	if err := printModels(os.Stdout, all, f, favoriteSet(cfg)); err != nil {
		return err
	}
	if !slices.ContainsFunc(all, func(r modelRow) bool { return f.keep(r, favoriteSet(cfg)) }) {
		fmt.Fprintf(os.Stderr, "models: nothing matches (%d models listed by %d provider(s); try fewer words or filters)\n", len(all), len(sources))
	}
	return nil
}

func favoriteSet(cfg *config.Config) map[string]bool {
	m := map[string]bool{}
	for _, r := range cfg.Models.Favorites {
		m[r] = true
	}
	return m
}

// printModels writes the table: favorites first, then by reference.
func printModels(w io.Writer, all []modelRow, f modelFilter, fav map[string]bool) error {
	var rows []modelRow
	for _, r := range all {
		if f.keep(r, fav) {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if fi, fj := fav[rows[i].Ref], fav[rows[j].Ref]; fi != fj {
			return fi
		}
		return rows[i].Ref < rows[j].Ref
	})
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tCONTEXT\t$/M IN\t$/M CACHED\t$/M OUT\tTOOLS\tREASONING")
	for _, r := range rows {
		name := r.Ref
		if fav[r.Ref] {
			name = "* " + name
		}
		p := r.Model.Price
		fmt.Fprintf(tw, "%s\t%s\t%.4f\t%.4f\t%.4f\t%v\t%v\n", name, human(r.Model.ContextTokens), p.InputPerM, p.CacheReadPerM, p.OutputPerM, r.SupportsTools(), r.SupportsReasoning())
	}
	return tw.Flush()
}

// cmdFavorites lists, adds and removes favorites in the user's own configuration.
func cmdFavorites(w io.Writer, args []string) error {
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true})
	if err != nil {
		return err
	}
	favs := slices.Clone(cfg.Models.Favorites)
	if len(args) == 0 || args[0] == "list" {
		for _, r := range favs {
			fmt.Fprintln(w, r)
		}
		return nil
	}
	op, refs := args[0], args[1:]
	if (op != "add" && op != "rm") || len(refs) == 0 {
		return fmt.Errorf("models fav: want `list`, `add provider/model...` or `rm provider/model...`")
	}
	for _, r := range refs {
		if _, _, ok := config.SplitModelRef(r); !ok {
			return fmt.Errorf("models fav: %q is not a provider/model reference (see `sleipnir models`)", r)
		}
		if op == "add" && !slices.Contains(favs, r) {
			favs = append(favs, r)
		}
		if op == "rm" {
			favs = slices.DeleteFunc(favs, func(x string) bool { return x == r })
		}
	}
	home, _ := os.UserHomeDir()
	if err := config.Save(config.UserConfigPath(home), map[string]any{"models": map[string]any{"favorites": favs}}); err != nil {
		return err
	}
	fmt.Fprintf(w, "%d favorite(s)\n", len(favs))
	return nil
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
