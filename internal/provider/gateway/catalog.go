// Package gateway understands OpenAI-compatible marketplaces such as Heimdall
// and OpenRouter: their public model catalogue, prices, and capabilities.
//
// Prices arrive as exact decimal strings per token. They are converted to the
// per-million-token figures the cost package uses. Marketplace engines cache
// prefixes automatically and charge no write premium, so a cache write costs
// the same as ordinary input.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/cost"
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
// coding agent can use).
func (e Entry) IsChat() bool {
	return strings.HasSuffix(e.Modality, "->text")
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

// Parse converts a /models response body into entries.
func Parse(body []byte) ([]Entry, error) {
	var env struct {
		Data []rawModel `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("gateway: bad catalogue: %w", err)
	}
	out := make([]Entry, 0, len(env.Data))
	for _, m := range env.Data {
		in := perMillion(m.Pricing.Prompt)
		outp := perMillion(m.Pricing.Completion)
		read := perMillion(m.Pricing.InputCacheRead)
		if m.Pricing.InputCacheRead == "" {
			read = in // no published cache price: cached tokens bill as input
		}
		cm := cost.OpenAICacheModel()
		maxOut := 0
		if m.TopProvider.MaxCompletionTokens != nil {
			maxOut = *m.TopProvider.MaxCompletionTokens
		}
		out = append(out, Entry{
			Model: cost.Model{
				ID: m.ID, Provider: "gateway", ContextTokens: m.ContextLength, MaxOutput: maxOut,
				Price: cost.Price{
					InputPerM: in, OutputPerM: outp, CacheReadPerM: read,
					CacheWrite5mPerM: in, CacheWrite1hPerM: in, // no write premium
				},
				Cache: cm,
			},
			Modality:  m.Architecture.Modality,
			Supported: m.SupportedParameters,
			Endpoints: m.EndpointCount,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model.ID < out[j].Model.ID })
	return out, nil
}

func perMillion(perToken string) float64 {
	if perToken == "" {
		return 0
	}
	f, err := strconv.ParseFloat(perToken, 64)
	if err != nil {
		return 0
	}
	return f * 1e6
}

// Fetch downloads and parses the public catalogue at baseURL + "/models".
func Fetch(ctx context.Context, hc *http.Client, baseURL string) ([]Entry, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway: catalogue returned http %d", resp.StatusCode)
	}
	return Parse(b)
}

// Table builds a cost table from entries, layered over base (so known vendors'
// own price entries survive).
func Table(base *cost.Table, entries []Entry) *cost.Table {
	if base == nil {
		base = cost.NewTable()
	}
	for _, e := range entries {
		base.Put(e.Model)
	}
	return base
}
