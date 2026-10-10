package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/catalog"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
)

// modelRow is one listed model: Ref is what --model takes (catalog.Entry).
type modelRow = catalog.Entry

// modelFilter is the search of `sleipnir models`: every word must appear in the reference, and each flag narrows further. It has
// the fields of catalog.Filter, which does the filtering.
type modelFilter struct {
	Words        []string
	Tools        bool
	Reasoning    bool
	MaxOut       float64 // $/M output tokens; 0 means no limit
	MinContext   int
	OnlyFavorite bool
	All          bool // include models that do not produce text
}

// keep applies chat capability, tool, reasoning, favorite, price, context, and case-insensitive
// search-word filters to a model row (catalog.Filter.Keep).
func (f modelFilter) keep(r modelRow, fav map[string]bool) bool {
	return catalog.Filter(f).Keep(r, fav)
}

// parseTokens reads "128k", "1m" or "32768" (catalog.ParseTokens).
func parseTokens(s string) (int, error) { return catalog.ParseTokens(s) }

// usableProviders are the providers `models` asks when none is named (catalog.UsableProviders).
func usableProviders(cfg *config.Config, withPublic bool) []string {
	return catalog.UsableProviders(cfg, withPublic)
}

// localSources are the local servers that answer with a list of models right now (catalog.LocalSources).
func localSources(ctx context.Context, cfg *config.Config) []modelSource {
	return fromCatalog(catalog.LocalSources(ctx, cfg))
}

// cmdModels lists filtered models from selected catalogs using each provider's
// authentication mode, or edits favorites in the user's configuration.
func cmdModels(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "fav" {
		return cmdFavorites(os.Stdout, args[1:])
	}
	fs := newFlagSet("models", flag.ExitOnError)
	fs.Usage = func() {
		printHelp(fs.Output(), `usage: sleipnir models [words...] [flags]
       sleipnir models fav [add|rm] provider/model...

Lists the models of every provider whose key is set (and Heimdall), as references that --model takes. Each word narrows the
search (all must appear, any case). Favorites, marked *, come first.

`)
		printFlags(fs)
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
	var sources []modelSource
	switch {
	case pf.baseURL != "":
		sources = []modelSource{{name: "", base: pf.baseURL}}
	case pf.provider != "" && !strings.EqualFold(pf.provider, "all"):
		name := strings.ToLower(pf.provider)
		b, _, ok := session.ProviderInfo(cfg, name)
		if !ok || b == "" {
			return fmt.Errorf("models: unknown provider %q", name)
		}
		p, _ := session.LookupProvider(cfg, name)
		sources = []modelSource{{name: name, base: b, plan: p.Auth == config.AuthChatGPTPlan}}
	default:
		sources = append(usableSources(cfg, true), localSources(ctx, cfg)...)
	}

	all, errs := fetchModels(ctx, sources)
	failed := 0
	for i, src := range sources {
		if errs[i] != nil {
			failed++
			if len(sources) == 1 {
				return errs[i]
			}
			fmt.Fprintf(os.Stderr, "models: %s: %v\n", src.name, errs[i])
		}
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

// modelSource is one catalogue to ask: the provider's name ("" for a bare endpoint) and its base URL. A ChatGPT plan's list needs the
// sign-in's token and is shaped differently, so it is asked another way (catalog.PlanModels).
type modelSource struct {
	name, base string
	plan       bool
}

// fromCatalog converts catalogue sources into the command's.
func fromCatalog(in []catalog.Source) []modelSource {
	var out []modelSource
	for _, s := range in {
		out = append(out, modelSource{name: s.Name, base: s.Base, plan: s.Plan})
	}
	return out
}

// toCatalog converts the command's sources into the catalogue's.
func toCatalog(in []modelSource) []catalog.Source {
	out := make([]catalog.Source, len(in))
	for i, s := range in {
		out[i] = catalog.Source{Name: s.name, Base: s.base, Plan: s.plan}
	}
	return out
}

// usableSources resolves catalog endpoints and authentication modes for providers eligible for
// model listing (catalog.UsableSources).
func usableSources(cfg *config.Config, withPublic bool) []modelSource {
	return fromCatalog(catalog.UsableSources(cfg, withPublic))
}

// fetchModels asks the catalogues at once (catalog.FetchEntries, the ChatGPT sign-in of the user's home). The rows are those of the
// catalogues that answered; errs says, per source, why one did not.
func fetchModels(ctx context.Context, sources []modelSource) (all []modelRow, errs []error) {
	return catalog.FetchEntries(ctx, userHome(), toCatalog(sources))
}

// planProvider marks catalog entries authenticated through ChatGPT. The catalog
// supplies no per-token prices, so tables label their price cells "plan".
const planProvider = catalog.PlanProvider

// priceOut is the output price of a row as the tables show it: a plan's models say "plan".
func priceOut(r modelRow) string {
	if r.Model.Provider == planProvider {
		return "plan"
	}
	return fmt.Sprintf("$%.3g/M out", r.Model.Price.OutputPerM)
}

// modelMenu is what `/model ` completes from: the chat models of every provider with a key, favorites first, then by reference. /fav stars
// and unstars one, in the user's own configuration and in the menu.
type modelMenu struct {
	home string
	mu   sync.Mutex
	rows []modelRow
	fav  map[string]bool
	list []input.Choice
}

// modelChoices starts fetching the catalogues of every usable provider in the background and returns the menu they make. It answers from
// memory, and with nothing until the first catalogue has come.
func modelChoices(ctx context.Context, home string) *modelMenu {
	cfg, _, err := config.Load(config.LoadOpts{Home: home, UntrustedProject: true})
	if err != nil {
		return nil
	}
	m := &modelMenu{home: home, fav: favoriteSet(cfg)}
	go func() {
		rows, _ := fetchModels(ctx, append(usableSources(cfg, false), localSources(ctx, cfg)...))
		m.mu.Lock()
		defer m.mu.Unlock()
		m.rows = rows
		m.rebuild()
	}()
	return m
}

// rebuild orders the models and describes them; the caller holds the lock.
func (m *modelMenu) rebuild() {
	rows := slices.Clone(m.rows)
	sort.SliceStable(rows, func(i, j int) bool {
		if fi, fj := m.fav[rows[i].Ref], m.fav[rows[j].Ref]; fi != fj {
			return fi
		}
		return rows[i].Ref < rows[j].Ref
	})
	var out []input.Choice
	for _, r := range rows {
		if !r.IsChat() {
			continue
		}
		d := fmt.Sprintf("%s ctx, %s", human(r.Model.ContextTokens), priceOut(r))
		if r.SupportsReasoning() {
			d += ", reasoning"
		}
		if m.fav[r.Ref] {
			d = "* " + d
		}
		out = append(out, input.Choice{Text: r.Ref, Detail: d})
	}
	m.list = out
}

// Choices is the list `/model ` completes from.
func (m *modelMenu) Choices() []input.Choice {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list
}

// Toggle stars the model that ref names, or unstars it when it was starred, and reports which. The choice is kept in the user's
// configuration (models.favorites, which `sleipnir models` lists first too) and the menu is ordered again at once.
func (m *modelMenu) Toggle(ref string) (bool, error) {
	starred, err := toggleFavorite(m.home, ref)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if starred {
		m.fav[ref] = true
	} else {
		delete(m.fav, ref)
	}
	m.rebuild()
	return starred, nil
}

// toggleFavorite adds ref to the favorites of the user's configuration under home, or removes it when it is there
// (catalog.ToggleFavorite).
func toggleFavorite(home, ref string) (bool, error) { return catalog.ToggleFavorite(home, ref) }

// saveFavorites writes the favorites into the user's configuration under home (catalog.SaveFavorites).
func saveFavorites(home string, favs []string) error { return catalog.SaveFavorites(home, favs) }

// favoriteSet builds a membership map from configured model favorites (catalog.Favorites).
func favoriteSet(cfg *config.Config) map[string]bool { return catalog.Favorites(cfg) }

// printModels writes the table: favorites first, then by reference. On a terminal too narrow for it the columns that tell least go first
// (REASONING, then the cached price, then the input price) and, as a last resort, a reference is cut short.
func printModels(w io.Writer, all []modelRow, f modelFilter, fav map[string]bool) error {
	return printModelsWidth(w, all, f, fav, termWidth(w))
}

// printModelsWidth is printModels for a terminal width (0: not a terminal, the whole table).
func printModelsWidth(w io.Writer, all []modelRow, f modelFilter, fav map[string]bool, width int) error {
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
	cells := [][]string{{"MODEL", "CONTEXT", "$/M IN", "$/M CACHED", "$/M OUT", "TOOLS", "REASONING"}}
	for _, r := range rows {
		name := r.Ref
		if fav[r.Ref] {
			name = "* " + name
		}
		p := r.Model.Price
		in, cached, out := fmt.Sprintf("%.4f", p.InputPerM), fmt.Sprintf("%.4f", p.CacheReadPerM), fmt.Sprintf("%.4f", p.OutputPerM)
		if r.Model.Provider == planProvider {
			in, cached, out = "plan", "plan", "plan"
		}
		cells = append(cells, []string{name, human(r.Model.ContextTokens), in, cached, out,
			fmt.Sprint(r.SupportsTools()), fmt.Sprint(r.SupportsReasoning())})
	}
	cols := []int{0, 1, 2, 3, 4, 5, 6}
	if width > 0 {
		widest := make([]int, len(cols))
		for _, row := range cells {
			for c, v := range row {
				widest[c] = max(widest[c], utf8.RuneCountInString(v))
			}
		}
		total := func() int { // the table's width: its columns and the two spaces between them
			n := -2
			for _, c := range cols {
				n += widest[c] + 2
			}
			return n
		}
		for _, drop := range []int{6, 3, 2} {
			if total() < width {
				break
			}
			cols = slices.DeleteFunc(cols, func(c int) bool { return c == drop })
		}
		if over := total() - (width - 1); over > 0 {
			keep := max(widest[0]-over, 16)
			for _, row := range cells[1:] {
				row[0] = fit(row[0], keep)
			}
		}
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, row := range cells {
		for i, c := range cols {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, row[c])
		}
		fmt.Fprintln(tw)
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
	if err := saveFavorites(home, favs); err != nil {
		return err
	}
	fmt.Fprintf(w, "%d favorite(s)\n", len(favs))
	return nil
}

// human abbreviates positive context sizes and displays a question mark for missing or nonpositive
// sizes.
func human(n int) string {
	switch {
	case n <= 0:
		return "?" // a catalogue that lists ids only (a local server) does not say
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}
