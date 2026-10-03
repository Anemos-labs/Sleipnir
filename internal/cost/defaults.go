package cost

import "time"

// anthropicCache constructs the explicit-cache policy with four breakpoints, twenty-block
// lookback, and five-minute or one-hour TTLs.
func anthropicCache(minPrefix int) CacheModel {
	return CacheModel{
		Explicit:         true,
		MaxBreakpoints:   4,
		LookbackBlocks:   20,
		MinPrefixTokens:  minPrefix,
		TTLs:             []time.Duration{5 * time.Minute, time.Hour},
		ReadRefreshesTTL: true,
		ReadableAfter:    ReadableAtFirstByte,
	}
}

// openaiCache models OpenAI-style automatic prefix caching: no markers, no
// write premium, a 1024-token floor and 128-token increments. Retention is
// short in memory; the planner treats it as ~5 minutes unless the request
// asks for extended retention.
func openaiCache() CacheModel {
	return CacheModel{
		Auto:             true,
		MinPrefixTokens:  1024,
		Granularity:      128,
		TTLs:             []time.Duration{5 * time.Minute},
		ReadRefreshesTTL: true,
		ReadableAfter:    ReadableAtFirstByte,
		KeyRouting:       true,
	}
}

// claude constructs catalog pricing and cache policy, deriving write prices from input price and
// preserving the supplied thinking capability.
func claude(id string, ctx, maxOut int, in, out, read float64, minPrefix int, pt bool) Model {
	return Model{
		ID: id, Provider: "anthropic", ContextTokens: ctx, MaxOutput: maxOut,
		Price: Price{
			InputPerM: in, OutputPerM: out, CacheReadPerM: read,
			CacheWrite5mPerM: in * 1.25, CacheWrite1hPerM: in * 2,
		},
		Cache:             anthropicCache(minPrefix),
		PreservedThinking: pt,
	}
}

// Defaults is the built-in price table. Prices are per the providers' public
// list at the time of writing and can be overridden by configuration; treat
// reported dollars as estimates unless the gateway returns its own cost.
func Defaults() *Table {
	return NewTable(
		claude("claude-fable-5-1", 1_000_000, 128_000, 10, 50, 0.25, 512, true),
		claude("claude-mythos-5-1", 1_000_000, 128_000, 10, 50, 0.25, 512, false),
		claude("claude-fable-5", 1_000_000, 128_000, 10, 50, 1.00, 512, false),
		claude("claude-opus-5-5", 1_000_000, 128_000, 4, 20, 0.20, 512, true),
		claude("claude-opus-5", 1_000_000, 128_000, 5, 25, 0.50, 512, false),
		claude("claude-opus-4-8", 1_000_000, 128_000, 5, 25, 0.50, 1024, false),
		claude("claude-opus-4-7", 1_000_000, 128_000, 5, 25, 0.50, 2048, false),
		claude("claude-opus-4-6", 1_000_000, 128_000, 5, 25, 0.50, 4096, false),
		claude("claude-sonnet-5-5", 1_000_000, 128_000, 2, 10, 0.20, 512, true),
		claude("claude-sonnet-5", 1_000_000, 128_000, 2, 10, 0.20, 1024, false),
		claude("claude-sonnet-4-6", 1_000_000, 128_000, 3, 15, 0.30, 1024, false),
		claude("claude-haiku-4-5", 200_000, 64_000, 1, 5, 0.10, 4096, false),
		// Deterministic test model served by the built-in mock provider.
		Model{
			ID: "mock-1", Provider: "mock", ContextTokens: 1_000_000, MaxOutput: 64_000,
			Price: Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 0.4, CacheWrite5mPerM: 5, CacheWrite1hPerM: 8},
			Cache: anthropicCache(512),
		},
	)
}

// OpenAICacheModel exposes the automatic-caching model for adapters and tests.
func OpenAICacheModel() CacheModel { return openaiCache() }

// AnthropicCacheModel exposes the explicit-breakpoint model for adapters and tests.
func AnthropicCacheModel(minPrefix int) CacheModel { return anthropicCache(minPrefix) }
