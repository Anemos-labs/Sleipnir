// Package cost holds the economics the cache planner reasons with: per-model
// prices and the caching rules of each provider family.
//
// Everything the planner decides (is compaction worth a rewrite? is a
// keep-alive cheaper than a cold miss? should the shared layer use a 1-hour
// TTL?) is expressed in input-token equivalents, so the same logic works across
// providers whose absolute prices and cache rules differ.
package cost

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// ReadableAfter says when a freshly written cache entry becomes readable by
// other requests.
type ReadableAfter int

const (
	// ReadableImmediately: entries are usable as soon as the request is accepted.
	ReadableImmediately ReadableAfter = iota
	// ReadableAtFirstByte: after the first response byte streams back. Parallel
	// requests started before that all pay the uncached price, which is why
	// fan-out must be gated on a primer request.
	ReadableAtFirstByte
	// ReadableAtComplete: only after the whole response finishes.
	ReadableAtComplete
)

// CacheModel is a provider's caching rules.
type CacheModel struct {
	// Explicit: the caller places breakpoints. Auto: the provider caches
	// prefixes on its own. Some providers do both.
	Explicit bool
	Auto     bool

	MaxBreakpoints  int
	LookbackBlocks  int
	MinPrefixTokens int
	// Granularity is the token increment of automatic caching (0: block edges).
	Granularity int

	// TTLs lists supported lifetimes; the first is the default.
	TTLs []time.Duration
	// Reads refresh the TTL at no charge (true for Anthropic and OpenAI).
	ReadRefreshesTTL bool

	ReadableAfter ReadableAfter
	// KeyRouting: the provider honours an explicit routing key (prompt_cache_key).
	KeyRouting bool
}

// DefaultTTL is the provider's default entry lifetime (0 if none is modelled).
func (c CacheModel) DefaultTTL() time.Duration {
	if len(c.TTLs) == 0 {
		return 0
	}
	return c.TTLs[0]
}

// Price is dollars per million tokens.
type Price struct {
	InputPerM        float64 `json:"input_per_m"`
	OutputPerM       float64 `json:"output_per_m"`
	CacheReadPerM    float64 `json:"cache_read_per_m"`
	CacheWrite5mPerM float64 `json:"cache_write_5m_per_m"`
	CacheWrite1hPerM float64 `json:"cache_write_1h_per_m"`
}

// USD prices a usage record. The result is never NaN or negative: a price or a
// token count that cannot be right (see Price.Validate) prices the record at
// +Inf, so a budget check ("spend >= limit") trips instead of silently never
// firing. Prices that passed Validate and counts that are not negative give the
// ordinary figure.
func (p Price) USD(u core.Usage) float64 {
	usd := (float64(u.InputTokens)*p.InputPerM +
		float64(u.CacheReadTokens)*p.CacheReadPerM +
		float64(u.CacheWrite5mTokens)*p.CacheWrite5mPerM +
		float64(u.CacheWrite1hTokens)*p.CacheWrite1hPerM +
		float64(u.OutputTokens)*p.OutputPerM) / 1e6
	if math.IsNaN(usd) || usd < 0 {
		return math.Inf(1)
	}
	return usd
}

// Weights are prices relative to the plain input price. They let planners work
// in input-token equivalents (ITE): a cache read of 10k tokens costs
// 10k*Read ITE, a rewrite costs 10k*Write5m ITE, and so on.
type Weights struct {
	Read, Write5m, Write1h, Output float64
}

// Weights derives relative prices. Missing cache prices fall back to the
// common 0.1x read / 1.25x write structure.
func (p Price) Weights() Weights {
	in := p.InputPerM
	if !finite(in) || in <= 0 || p.Validate() != nil {
		// No usable input price (or a price table that cannot be trusted): the
		// common structure, never a ratio of garbage.
		return Weights{Read: 0.1, Write5m: 1.25, Write1h: 2, Output: 5}
	}
	w := Weights{
		Read:    p.CacheReadPerM / in,
		Write5m: p.CacheWrite5mPerM / in,
		Write1h: p.CacheWrite1hPerM / in,
		Output:  p.OutputPerM / in,
	}
	if p.CacheReadPerM == 0 {
		w.Read = 0.1
	}
	if p.CacheWrite5mPerM == 0 {
		w.Write5m = 1.25
	}
	if p.CacheWrite1hPerM == 0 {
		w.Write1h = 2
	}
	return w
}

// Model describes one model as seen by the harness.
type Model struct {
	ID            string
	Provider      string // "anthropic", "openai", ...
	ContextTokens int
	MaxOutput     int
	Price         Price
	Cache         CacheModel
	// Enforces preserved-thinking checks: editing history before a thinking
	// block invalidates the block (400 or dropped, depending on account).
	PreservedThinking bool
}

var (
	dateSuffix = regexp.MustCompile(`-\d{8}$`)
	verSuffix  = regexp.MustCompile(`@\d{8}$`)
)

// Normalize maps gateway-style ids ("anthropic/claude-opus-5-5",
// "claude-haiku-4-5-20251001") to the short canonical id: author prefix, date
// suffix and dots removed, lower-cased.
func Normalize(id string) string {
	id = normalizeFull(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// normalizeFull keeps the author prefix, so two vendors' identically named
// models do not collide in a marketplace catalogue.
func normalizeFull(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	id = dateSuffix.ReplaceAllString(id, "")
	id = verSuffix.ReplaceAllString(id, "")
	return strings.ReplaceAll(id, ".", "-")
}

// Table is a set of models with lookup by (normalised) id.
type Table struct{ m map[string]Model }

// NewTable builds a table from models. A model that fails Validate is left out
// (use Put to learn why).
func NewTable(models ...Model) *Table {
	t := &Table{m: map[string]Model{}}
	for _, m := range models {
		_ = t.Put(m)
	}
	return t
}

// Lookup finds a model by id, tolerating provider prefixes and date suffixes.
// An exact (author-qualified) match wins over the short form.
func (t *Table) Lookup(id string) (Model, bool) {
	if m, ok := t.m[normalizeFull(id)]; ok {
		return m, true
	}
	m, ok := t.m[Normalize(id)]
	return m, ok
}

// Put adds or replaces a model. It is stored under its full id and, when free,
// under the short id so "claude-opus-5-5" still finds "anthropic/claude-opus-5-5".
//
// A model that fails Validate (a NaN or negative price, an absurd window, an
// unusable id) is refused and the table is left as it was: lookups then fall
// through to a conservative Fallback instead of to numbers that would make a
// budget unreachable.
func (t *Table) Put(m Model) error {
	if err := m.Validate(); err != nil {
		return fmt.Errorf("cost: model %q refused: %w", displayID(m.ID), err)
	}
	full, short := normalizeFull(m.ID), Normalize(m.ID)
	t.m[full] = m
	if _, taken := t.m[short]; !taken || short == full {
		t.m[short] = m
	}
	return nil
}

// displayID is a model id fit for an error message.
func displayID(id string) string {
	id = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return '?'
		}
		return r
	}, id)
	if len(id) > 64 {
		id = strings.ToValidUTF8(id[:64], "?") + "..."
	}
	return id
}

// All returns every distinct model.
func (t *Table) All() []Model {
	seen := map[string]bool{}
	out := make([]Model, 0, len(t.m))
	for _, m := range t.m {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}

// Fallback returns a conservative model for ids the table does not know: 0.1x
// reads, 1.25x writes, no minimum knowledge. Costs are reported as estimates.
func Fallback(id string) Model {
	return Model{
		ID: id, Provider: "unknown", ContextTokens: 200_000, MaxOutput: 16_000,
		Price: Price{InputPerM: 3, OutputPerM: 15, CacheReadPerM: 0.3, CacheWrite5mPerM: 3.75, CacheWrite1hPerM: 6},
		Cache: anthropicCache(1024),
	}
}
