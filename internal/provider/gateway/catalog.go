// Package gateway understands OpenAI-compatible marketplaces such as Heimdall
// and OpenRouter: their public model catalogue, prices, and capabilities.
//
// Prices arrive as exact decimal strings per token. They are converted to the
// per-million-token figures the cost package uses. Marketplace engines cache
// prefixes automatically and charge no write premium, so a cache write costs
// the same as ordinary input.
//
// The catalogue is data from a third party, and every dollar figure and window
// size the harness plans with comes from it. Parse therefore keeps only entries
// whose numbers survive cost.Model.Validate (no NaN, infinite, negative or absurd
// price or window) and whose id is printable: an entry that fails is left out, and
// the model is then priced with cost.Fallback's conservative estimate instead of
// with numbers that could make a budget unreachable.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

const (
	// MaxCatalogueBytes bounds the body of a catalogue response.
	MaxCatalogueBytes = 32 << 20
	// MaxCatalogueEntries bounds the number of models in a catalogue. Real ones
	// list hundreds; more than this is a flood, and the catalogue is refused.
	MaxCatalogueEntries = 50_000
	// maxSupported bounds the capability list of one entry.
	maxSupported = 256
	// maxCapabilityBytes bounds one capability name and the modality string.
	maxCapabilityBytes = 64
)

// Entry is one catalogue model with the fields the harness cares about.
type Entry struct {
	Model cost.Model
	// Modality is the catalogue's "input->output" description.
	Modality string
	// Supported lists supported request parameters ("tools", "reasoning_effort").
	Supported []string
	// Endpoints is how many providers serve the model. With a single endpoint,
	// session affinity cannot change where a request lands.
	Endpoints int
}

// SupportsTools reports whether the model accepts tool definitions.
func (e Entry) SupportsTools() bool { return has(e.Supported, "tools") }

// SupportsReasoning reports whether reasoning effort is configurable.
func (e Entry) SupportsReasoning() bool {
	return has(e.Supported, "reasoning_effort") || has(e.Supported, "reasoning")
}

// IsChat reports whether the model produces text from text (the only kind a
// coding agent can use). A catalogue that does not say what its models take and give
// (OpenAI's own, Ollama's, vLLM's and LM Studio's list nothing but ids) leaves the
// question open, and open counts as yes: the alternative is a listing of nothing.
func (e Entry) IsChat() bool {
	return e.Modality == "" || strings.HasSuffix(e.Modality, "->text")
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type rawModel struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	Architecture  struct {
		Modality string `json:"modality"`
	} `json:"architecture"`
	Pricing struct {
		Prompt         string `json:"prompt"`
		Completion     string `json:"completion"`
		InputCacheRead string `json:"input_cache_read"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
	EndpointCount       int      `json:"endpoint_count"`
}

// Rejection says why an entry of a catalogue was left out.
type Rejection struct {
	// ID is the entry's id, made printable and bounded (it is not trusted).
	ID     string
	Reason string
}

// Parse converts a /models response body into entries. Entries that fail
// validation (see the package comment) are left out; ParseDetailed says which and
// why.
func Parse(body []byte) ([]Entry, error) {
	entries, _, err := ParseDetailed(body)
	return entries, err
}

// ParseDetailed is Parse plus the list of entries that were left out.
func ParseDetailed(body []byte) (entries []Entry, rejected []Rejection, err error) {
	var env struct {
		Data []rawModel `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, nil, fmt.Errorf("gateway: bad catalogue: %s", provider.SanitizeText(err.Error(), 200))
	}
	if len(env.Data) > MaxCatalogueEntries {
		return nil, nil, fmt.Errorf("gateway: the catalogue lists %d models (limit %d); refusing it", len(env.Data), MaxCatalogueEntries)
	}
	out := make([]Entry, 0, len(env.Data))
	for _, m := range env.Data {
		e, err := entryOf(m)
		if err != nil {
			rejected = append(rejected, Rejection{ID: provider.SanitizeText(m.ID, 80), Reason: err.Error()})
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model.ID < out[j].Model.ID })
	return out, rejected, nil
}

// entryOf builds and validates one entry.
func entryOf(m rawModel) (Entry, error) {
	in, err := perMillion(m.Pricing.Prompt)
	if err != nil {
		return Entry{}, fmt.Errorf("input price: %w", err)
	}
	outp, err := perMillion(m.Pricing.Completion)
	if err != nil {
		return Entry{}, fmt.Errorf("output price: %w", err)
	}
	read, err := perMillion(m.Pricing.InputCacheRead)
	if err != nil {
		return Entry{}, fmt.Errorf("cache read price: %w", err)
	}
	if m.Pricing.InputCacheRead == "" {
		read = in // no published cache price: cached tokens bill as input
	}
	maxOut := 0
	if m.TopProvider.MaxCompletionTokens != nil {
		maxOut = *m.TopProvider.MaxCompletionTokens
	}
	model := cost.Model{
		ID: m.ID, Provider: "gateway", ContextTokens: m.ContextLength, MaxOutput: maxOut,
		Price: cost.Price{
			InputPerM: in, OutputPerM: outp, CacheReadPerM: read,
			CacheWrite5mPerM: in, CacheWrite1hPerM: in, // no write premium
		},
		Cache: cost.OpenAICacheModel(),
	}
	if err := model.Validate(); err != nil {
		return Entry{}, err
	}
	return Entry{
		Model:     model,
		Modality:  capability(m.Architecture.Modality),
		Supported: capabilities(m.SupportedParameters),
		Endpoints: max(m.EndpointCount, 0),
	}, nil
}

// capability makes one capability word printable and bounded.
func capability(s string) string { return provider.SanitizeText(s, maxCapabilityBytes) }

func capabilities(in []string) []string {
	if len(in) > maxSupported {
		in = in[:maxSupported]
	}
	var out []string
	for _, s := range in {
		if c := capability(s); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// perMillion converts a per-token decimal string to dollars per million tokens.
// An empty string is a price that was not published (0, which callers may
// replace); anything that is not a finite number is an error, never a price.
func perMillion(perToken string) (float64, error) {
	perToken = strings.TrimSpace(perToken)
	if perToken == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(perToken, 64)
	switch {
	case err != nil:
		return 0, errors.New("not a number")
	case math.IsNaN(f) || math.IsInf(f, 0):
		return 0, errors.New("not a finite number")
	}
	v := f * 1e6
	if math.IsInf(v, 0) {
		return 0, errors.New("out of range")
	}
	return v, nil
}

// Fetch downloads and parses the public catalogue at baseURL + "/models". It
// follows redirects only within the origin of baseURL, and reads at most
// MaxCatalogueBytes.
func Fetch(ctx context.Context, hc *http.Client, baseURL string) ([]Entry, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	hc = provider.HardenClient(hc)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		if re, ok := provider.AsRedirect(err); ok {
			return nil, fmt.Errorf("gateway: %w", re)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway: catalogue returned http %d", resp.StatusCode)
	}
	b, err := provider.ReadCapped(resp.Body, MaxCatalogueBytes)
	if err != nil {
		return nil, fmt.Errorf("gateway: catalogue: %s", provider.SanitizeText(err.Error(), 200))
	}
	return Parse(b)
}

// Vet returns the entries of es that pass the checks Parse applies to a fresh
// download. Use it on a catalogue that comes from anywhere else (a cached copy on
// disk), so that an edited file cannot smuggle in numbers the network could not.
func Vet(es []Entry) []Entry {
	out := make([]Entry, 0, len(es))
	for _, e := range es {
		if e.Model.Validate() != nil || len(e.Supported) > maxSupported {
			continue
		}
		e.Modality = capability(e.Modality)
		e.Supported = capabilities(e.Supported)
		e.Endpoints = max(e.Endpoints, 0)
		out = append(out, e)
	}
	return out
}

// Table builds a cost table from entries, layered over base (so known vendors'
// own price entries survive). An entry the cost table refuses is skipped.
func Table(base *cost.Table, entries []Entry) *cost.Table {
	if base == nil {
		base = cost.NewTable()
	}
	for _, e := range entries {
		_ = base.Put(e.Model)
	}
	return base
}
