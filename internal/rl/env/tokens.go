package env

import "github.com/anemos-labs/sleipnir/internal/core"

// Episode signals the env package reads that package rl does not name (internal/rl/traj produces them).
const (
	sigRequestErrors  = "request_errors"
	sigRequestRetries = "request_retries"
)

// Tokens is what a rollout was billed for, by kind.
type Tokens struct {
	Input      int `json:"input"`               // uncached input, paid at the full price
	CacheRead  int `json:"cache_read"`          // served from the provider's cache
	CacheWrite int `json:"cache_write"`         // written to it, both TTLs
	Output     int `json:"output"`              // everything the model generated
	Reasoning  int `json:"reasoning,omitempty"` // the reasoning share of Output, when the provider says
}

func tokensOf(u core.Usage) Tokens {
	return Tokens{Input: u.InputTokens, CacheRead: u.CacheReadTokens, CacheWrite: u.CacheWriteTokens(), Output: u.OutputTokens, Reasoning: u.ReasoningTokens}
}

// Prompt is every input token the provider processed: the cached prefix, what was written to the cache, and the rest.
func (t Tokens) Prompt() int { return t.Input + t.CacheRead + t.CacheWrite }

// HitRatio is the share of input tokens that came from the cache, weighted by tokens, not by requests: a long prompt that
// hit counts for what it saved. Zero when nothing was sent.
func (t Tokens) HitRatio() float64 {
	if p := t.Prompt(); p > 0 {
		return float64(t.CacheRead) / float64(p)
	}
	return 0
}

func (t Tokens) plus(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.CacheRead + o.CacheRead, t.CacheWrite + o.CacheWrite, t.Output + o.Output, t.Reasoning + o.Reasoning}
}
