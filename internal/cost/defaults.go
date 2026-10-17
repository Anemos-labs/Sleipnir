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

// openaiCache models OpenAI-style automatic prefix caching: no markers, a
// 1024-token floor and 128-token increments (what a cache write costs is the
// price's: OpenAI's own API charges a premium, see openai; gateways do not).
// Retention is short in memory; the planner treats it as ~5 minutes unless the
// request asks for extended retention.
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

// openai constructs an OpenAI model of the table: its API list prices for input, cached input, cache writes (the short-context
// cache-write rate, 1.25 times input, for both lifetimes the price has) and output, and automatic prefix caching. The context window
// is the harness's cautious 200k (Fallback's), used where the provider reports none (OpenAI's own /models lists ids only): a
// session takes the window a marketplace's catalogue or a ChatGPT plan's model list gives, and the provider's
// options.context_window over both. The output limit is the family's 128k, which a request asks no more than 16k of unless told
// otherwise.
func openai(id string, in, cached, write, out float64) Model {
	return Model{
		ID: id, Provider: "openai", ContextTokens: 200_000, MaxOutput: 128_000,
		Price: Price{InputPerM: in, OutputPerM: out, CacheReadPerM: cached, CacheWrite5mPerM: write, CacheWrite1hPerM: write},
		Cache: openaiCache(),
	}
}

// Defaults is the built-in price table. Prices are per the providers' public
// list at the time of writing and can be overridden by configuration; treat
// reported dollars as estimates unless the gateway returns its own cost.
//
// The OpenAI entries are the API's standard tier for short context (at most
// 272k input tokens), in US dollars per million tokens, from
// https://developers.openai.com/api/docs/pricing (checked 2026-10-13). A
// request with a longer prompt is billed at a higher long-context rate that the
// table does not model, so its figure is a lower bound. The same prices are
// shown for a ChatGPT plan's use of these models (cost.Model.ListPrice), which
// the plan itself does not bill per token.
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
		// input, cached input, cache write, output
		openai("gpt-6-astra", 10.00, 1.00, 12.50, 50.00),
		openai("gpt-6.1-sol", 2.00, 0.10, 2.50, 10.00),
		openai("gpt-6-sol", 2.00, 0.20, 2.50, 10.00),
		openai("gpt-6-luna", 0.10, 0.01, 0.125, 0.50),
		openai("gpt-5.6-sol", 4.00, 0.40, 5.00, 20.00),
		openai("gpt-5.6-terra", 2.00, 0.20, 2.50, 12.00),
		openai("gpt-5.6-luna", 0.20, 0.02, 0.25, 1.20),
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
