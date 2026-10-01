package reward

import (
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// Targets lists the preset target names accepted by Config.TargetName (any id
// of the built-in cost table is accepted as well).
func Targets() []string {
	names := []string{"anthropic-sonnet", "anthropic-opus", "anthropic-haiku", "openai", "marketplace", "no-cache"}
	sort.Strings(names)
	return names
}

// LookupTarget resolves a target name to a price and cache model.
//
// The presets are the deployment families the training doc names: an
// Anthropic-like provider (explicit breakpoints, write premium, 5 minute TTL),
// an OpenAI-like one (automatic caching, no premium, 128-token granularity), a
// marketplace-like one (automatic LRU cache, cheap reads, no premium) and a
// provider without any cache. Prices are relative anyway: only the ratios in
// cost.Price.Weights matter to the ITE that rewards use.
func LookupTarget(name string) (cost.Model, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	defaults := cost.Defaults()
	switch key {
	case "anthropic-sonnet", "anthropic":
		return defaults.Lookup("claude-sonnet-5-5")
	case "anthropic-opus":
		return defaults.Lookup("claude-opus-5-5")
	case "anthropic-haiku":
		return defaults.Lookup("claude-haiku-4-5")
	case "openai", "openai-like":
		return cost.Model{
			ID: "openai-like", Provider: "openai", ContextTokens: 1_000_000, MaxOutput: 32_000,
			// Cached input at a quarter of the price, no write premium (writes cost the
			// plain input price), output four times the input price.
			Price: cost.Price{InputPerM: 2, OutputPerM: 8, CacheReadPerM: 0.5, CacheWrite5mPerM: 2, CacheWrite1hPerM: 2},
			Cache: cost.OpenAICacheModel(),
		}, true
	case "marketplace", "marketplace-like":
		return cost.Model{
			ID: "marketplace-like", Provider: "marketplace", ContextTokens: 1_000_000, MaxOutput: 32_000,
			Price: cost.Price{InputPerM: 1, OutputPerM: 2, CacheReadPerM: 0.25, CacheWrite5mPerM: 1, CacheWrite1hPerM: 1},
			Cache: cost.CacheModel{
				Auto: true, MinPrefixTokens: 64, TTLs: []time.Duration{time.Hour},
				ReadRefreshesTTL: true, ReadableAfter: cost.ReadableAtFirstByte, KeyRouting: true,
			},
		}, true
	case "no-cache", "none":
		return cost.Model{
			ID: "no-cache", Provider: "none", ContextTokens: 1_000_000, MaxOutput: 32_000,
			Price: cost.Price{InputPerM: 1, OutputPerM: 4, CacheReadPerM: 1, CacheWrite5mPerM: 1, CacheWrite1hPerM: 1},
		}, true
	}
	return defaults.Lookup(name)
}

// writeWeights returns the relative write prices of a model. An automatic cache
// with no stated write price has no premium (cost.Price.Weights would apply the
// Anthropic-style 1.25x default, which is wrong for OpenAI-like engines).
func writeWeights(m cost.Model) (w5, w1 float64) {
	w := m.Price.Weights()
	w5, w1 = w.Write5m, w.Write1h
	if m.Cache.Auto && !m.Cache.Explicit {
		if m.Price.CacheWrite5mPerM == 0 {
			w5 = 1
		}
		if m.Price.CacheWrite1hPerM == 0 {
			w1 = w5
		}
	}
	return w5, w1
}
