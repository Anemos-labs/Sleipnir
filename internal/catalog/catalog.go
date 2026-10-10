// Package catalog is the model catalogue of the providers a person can use, and their favourites: which providers are asked (those
// with a key, Heimdall's public list, the local servers that answer, a signed-in ChatGPT plan), the rows they list, the filter of
// `sleipnir models`, and the favourites kept in the user's configuration (models.favorites). `sleipnir models`, the chat's /model menu
// and the Models page of `sleipnir web` read it.
//
// It also holds the provider-status service of the Providers page: which providers take a key, whether a key is there and where it
// comes from (never the key), and the non-interactive halves of `sleipnir login` and `logout` (store, forget, check a key).
//
// Rows are data from the network: their text is sanitized by package gateway before it gets here; a price of zero for input, output
// and cached tokens means the catalogue did not say (Row reports it as unknown, never as free).
package catalog

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
)

// PlanProvider marks catalogue entries reached through a ChatGPT plan's sign-in. The plan publishes no per-token prices, so tables say
// "plan" where a price would be.
const PlanProvider = "chatgpt-plan"

// Entry is one listed model: Ref is what --model takes (provider/model), the rest is the catalogue's entry.
type Entry struct {
	Ref string
	gateway.Entry
}

// Plan reports whether the entry is a ChatGPT plan's model.
func (e Entry) Plan() bool { return e.Model.Provider == PlanProvider }

// Filter is the search of `sleipnir models`: every word must appear in the reference (any case), and each set field narrows further.
type Filter struct {
	Words        []string
	Tools        bool
	Reasoning    bool
	MaxOut       float64 // $/M output tokens; 0 means no limit
	MinContext   int
	OnlyFavorite bool
	All          bool // include models that do not produce text
}

// Keep applies the filter to one entry: chat capability (unless All), tools, reasoning, favourite, output price (a plan's model has
// none and is left out by any limit), context window, and the words.
func (f Filter) Keep(r Entry, fav map[string]bool) bool {
	if !f.All && !r.IsChat() {
		return false
	}
	if f.Tools && !r.SupportsTools() || f.Reasoning && !r.SupportsReasoning() || f.OnlyFavorite && !fav[r.Ref] {
		return false
	}
	if f.MaxOut > 0 && (r.Model.Provider == PlanProvider || r.Model.Price.OutputPerM > f.MaxOut) || r.Model.ContextTokens < f.MinContext {
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

// ParseTokens reads a token count: "128k", "1m" or "32768".
func ParseTokens(s string) (int, error) {
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

// Row is one model of the catalogue as the Models page shows it. In, Out and Cached are US dollars per million tokens; nil means the
// price is unknown (a plan's model, or a catalogue that lists ids and no prices).
type Row struct {
	Ref       string   `json:"ref"`
	Provider  string   `json:"provider"`
	Context   int      `json:"context"`
	In        *float64 `json:"in"`
	Out       *float64 `json:"out"`
	Cached    *float64 `json:"cached"`
	Tools     bool     `json:"tools"`
	Reasoning bool     `json:"reasoning"`
	Plan      bool     `json:"plan"`
	// Chat says the model produces text (only such models are listed unless the filter's All is set).
	Chat bool `json:"chat"`
}

// RowOf turns an entry into a row. The provider is the reference's first part. Prices are unknown for a plan's model and for an
// entry whose three prices are all zero: catalogues that list ids only say nothing about price, and a missing price shown as $0 would
// be a claim the catalogue did not make.
func RowOf(e Entry) Row {
	prov, _, _ := strings.Cut(e.Ref, "/")
	r := Row{
		Ref: e.Ref, Provider: prov, Context: e.Model.ContextTokens, Tools: e.SupportsTools(), Reasoning: e.SupportsReasoning(),
		Plan: e.Plan(), Chat: e.IsChat(),
	}
	p := e.Model.Price
	if !r.Plan && (p.InputPerM != 0 || p.OutputPerM != 0 || p.CacheReadPerM != 0) {
		in, out, cached := p.InputPerM, p.OutputPerM, p.CacheReadPerM
		r.In, r.Out, r.Cached = &in, &out, &cached
	}
	return r
}

// PriceKnown reports whether the row has prices.
func (r Row) PriceKnown() bool { return r.In != nil }
