package core

import (
	"time"
)

// Message is one entry of the logical prompt sent to a provider.
type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
	// Turn is the thread turn this message renders, or 0 for synthetic
	// messages (the pinned-context preamble).
	Turn TurnID `json:"turn,omitempty"`
}

// BlockRef addresses one block of a Prompt in wire order.
type BlockRef struct {
	// Sys selects the system blocks; otherwise Msg/Blk index Messages.
	Sys bool `json:"sys,omitempty"`
	Msg int  `json:"msg"`
	Blk int  `json:"blk"`
}

// Breakpoint asks the provider to cache the prompt prefix ending at After
// (inclusive). Providers with automatic caching ignore breakpoints.
type Breakpoint struct {
	After BlockRef      `json:"after"`
	TTL   time.Duration `json:"ttl,omitempty"` // 0: provider default
	Label string        `json:"label,omitempty"`
}

// Params are per-request generation settings. Providers fold what they
// support and ignore the rest. Thinking and effort are part of the cached
// prefix on some providers, so callers keep them constant per route.
type Params struct {
	MaxTokens  int      `json:"max_tokens,omitempty"`
	Thinking   string   `json:"thinking,omitempty"`    // "", "adaptive", "off"
	Effort     string   `json:"effort,omitempty"`      // "", low|medium|high|xhigh|max
	ToolChoice string   `json:"tool_choice,omitempty"` // "", "auto", "none"
	Stop       []string `json:"stop,omitempty"`
	// Temperature is ignored by models that reject sampling parameters.
	Temperature *float64 `json:"temperature,omitempty"`
}

// Prompt is the provider-neutral rendering of an agent's context for one
// request. kv produces it; provider adapters turn it into wire JSON without
// re-ordering or re-serialising its contents.
type Prompt struct {
	Model       string       `json:"model"`
	Tools       []ToolSpec   `json:"tools,omitempty"`
	System      []Block      `json:"system,omitempty"`
	Messages    []Message    `json:"messages"`
	Breakpoints []Breakpoint `json:"breakpoints,omitempty"`
	// CacheKey groups requests that share a prefix for providers that route by
	// key (OpenAI prompt_cache_key). Empty when unused.
	CacheKey string `json:"cache_key,omitempty"`
	Params   Params `json:"params"`
}

// WalkBlocks visits every block in wire order: tools (as synthetic entries),
// system blocks, then message blocks. It is the single definition of "prefix
// order" used by breakpoint planning, drift detection and size accounting.
func (p *Prompt) WalkBlocks(fn func(ref BlockRef, tool *ToolSpec, b *Block)) {
	for i := range p.Tools {
		fn(BlockRef{Sys: true, Msg: -1, Blk: i}, &p.Tools[i], nil)
	}
	for i := range p.System {
		fn(BlockRef{Sys: true, Msg: 0, Blk: i}, nil, &p.System[i])
	}
	for mi := range p.Messages {
		for bi := range p.Messages[mi].Blocks {
			fn(BlockRef{Msg: mi, Blk: bi}, nil, &p.Messages[mi].Blocks[bi])
		}
	}
}
