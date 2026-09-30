package inspect

import (
	"fmt"
	"sort"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
)

// priceInfo is one model's price and cache rules as the inspector applies them.
type priceInfo struct {
	model     string
	canonical string
	source    string // "table" | "fallback"
	price     cost.Price
	ttl       time.Duration
	explicit  bool
	requests  int
}

// priceFor resolves a model id (gateway-style ids and date suffixes are
// tolerated by cost.Table) and remembers the answer. An id the table does not
// know gets cost.Fallback's conservative prices and is reported as such.
func (s *Session) priceFor(model string) *priceInfo {
	if p, ok := s.prices[model]; ok {
		return p
	}
	m, ok := s.opts.Prices.Lookup(model)
	src := "table"
	if !ok {
		m = cost.Fallback(model)
		src = "fallback"
	}
	ttl := m.Cache.DefaultTTL()
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	p := &priceInfo{
		model: model, canonical: cost.Normalize(model), source: src, price: m.Price, ttl: ttl,
		explicit: m.Cache.Explicit,
	}
	if len(s.prices) < 64 {
		s.prices[model] = p
	}
	return p
}

// writePerM is what a cache write costs: the modelled premium on providers
// with explicit caching, the plain input price on automatic ones (no premium).
func (p *priceInfo) writePerM(u core.Usage) float64 {
	if !p.explicit {
		return p.price.InputPerM
	}
	_ = u
	return p.price.CacheWrite5mPerM
}

// bill prices a usage record from the table, split by what was paid for.
func (p *priceInfo) bill(u core.Usage) Money {
	m := Money{
		Uncached: float64(u.InputTokens) * p.price.InputPerM / 1e6,
		Read:     float64(u.CacheReadTokens) * p.price.CacheReadPerM / 1e6,
		Write: (float64(u.CacheWrite5mTokens)*p.price.CacheWrite5mPerM +
			float64(u.CacheWrite1hTokens)*p.price.CacheWrite1hPerM) / 1e6,
		Output: float64(u.OutputTokens) * p.price.OutputPerM / 1e6,
	}
	m.Total = m.Uncached + m.Read + m.Write + m.Output
	return m
}

// noCache prices the same request with no caching at all: every prompt token at
// the plain input price, output unchanged.
func (p *priceInfo) noCache(u core.Usage) Money {
	m := Money{
		Uncached: float64(u.TotalInput()) * p.price.InputPerM / 1e6,
		Output:   float64(u.OutputTokens) * p.price.OutputPerM / 1e6,
	}
	m.Total = m.Uncached + m.Output
	return m
}

func (m *Money) add(o Money) {
	m.Uncached += o.Uncached
	m.Read += o.Read
	m.Write += o.Write
	m.Output += o.Output
	m.Total += o.Total
}

// costAgg accumulates the bill and both counterfactuals over the whole log.
type costAgg struct {
	reported  float64
	reportedN int
	gatewayN  int
	actual    Money
	noCache   Money
	naive     Money
	compUSD   float64
}

func newCostAgg() costAgg { return costAgg{} }

// cacheAgg accumulates cache statistics over the whole log.
type cacheAgg struct {
	mainRead, mainPrompt     int64
	sideRead, sidePrompt     int64
	steadyRead, steadyPrompt int64
	steadyN                  int
	rebaseRead, rebasePrompt int64
	rebaseN                  int
	cold, first, warmFirst   int
	ctxSum, naiveCtxSum      int64
	ctxN                     int
	ctxMax                   int
	expected, actual         int64
	anyRead                  bool
	lastAnyT                 time.Time
	lastAnyG0                int
}

// naiveBill estimates what a plain harness would have paid for this main
// request: one growing history per agent, no compaction and no layering, with
// the usual "cache up to the end of the previous turn" behaviour.
//
// The context it would have sent is the actual prompt plus every token
// compaction has folded away so far, minus the hot tail (a plain harness has
// none). If the agent's previous request was within the cache lifetime, the
// previous context is read from cache and only the growth is written;
// otherwise the whole context is written. An agent's first request is charged a
// full write except for the constitution and tools, which every harness shares
// across agents. Output tokens are the ones actually generated.
func (s *Session) naiveBill(a *agent, r *req, p *priceInfo) (int64, Money) {
	n := int64(r.prompt) + r.foldedAtReq
	if r.hotTok > 0 {
		n -= int64(r.hotTok)
	}
	if n < 1 {
		n = 1
	}
	var readTok int64
	switch {
	case a.naiveAny && r.t.Sub(a.naiveT) < p.ttl:
		readTok = min(a.naivePrev, n)
	case !a.naiveAny && s.cache.lastAnyG0 > 0 && !s.cache.lastAnyT.IsZero() && r.t.Sub(s.cache.lastAnyT) < p.ttl:
		readTok = min(int64(s.cache.lastAnyG0), n)
	}
	writeTok := n - readTok
	m := Money{
		Read:   float64(readTok) * p.price.CacheReadPerM / 1e6,
		Write:  float64(writeTok) * p.writePerM(r.usage) / 1e6,
		Output: float64(r.usage.OutputTokens) * p.price.OutputPerM / 1e6,
	}
	m.Total = m.Read + m.Write + m.Output
	a.naivePrev, a.naiveT, a.naiveAny = n, r.t, true
	return n, m
}

// assumptionsLocked lists, in plain words, how every non-recorded number was made.
func (s *Session) assumptionsLocked() []string {
	out := []string{
		"Actual: recorded token usage priced with the table below, so the three bills use the same prices. The gateway-reported total (cost_usd) is shown separately and can differ if the gateway's prices differ.",
		"No cache: the same requests with every prompt token (uncached + cache reads + cache writes) billed at the plain input price; output tokens unchanged; compactor calls included.",
		"Naive whole-history (ESTIMATE): one growing history per agent, nothing folded and no layers. Each request's context is the actual prompt plus every token compaction removed so far, minus the hot tail.",
		"Naive caching model: if the agent's previous request was inside the cache lifetime its previous context is read from cache and only the growth is written; after a longer gap, or on an agent's first request, the context is written in full (constitution and tools are assumed shared across agents).",
		"Naive leaves out what a plain harness would also pay for and Sleipnir avoids (re-orienting every agent, briefings, summary calls) and what Sleipnir pays for coordination (hot tail, compactor calls), so it is a conservative baseline for the layered design, not a measurement.",
		"Context sizes are provider-reported prompt tokens; the naive context ignores the context window (a plain harness would have to compact or fail before it grew that large).",
	}
	return out
}

// pricesUsedLocked reports the prices applied, most used first.
func (s *Session) pricesUsedLocked() []PriceUsed {
	out := make([]PriceUsed, 0, len(s.prices))
	for _, p := range s.prices {
		out = append(out, PriceUsed{
			Model: p.model, Canonical: p.canonical, Source: p.source,
			InputPerM: p.price.InputPerM, OutputPerM: p.price.OutputPerM, ReadPerM: p.price.CacheReadPerM,
			Write5mPerM: p.price.CacheWrite5mPerM, Write1hPerM: p.price.CacheWrite1hPerM,
			TTLSeconds: int(p.ttl / time.Second), Explicit: p.explicit, Requests: p.requests,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func pct(saved, base float64) float64 {
	if base <= 0 {
		return 0
	}
	return saved / base
}

func ratio(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func fmtK(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}
