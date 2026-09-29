package core

// Usage is normalised token accounting for one model request.
//
// Providers disagree on what "input tokens" means (Anthropic reports the
// uncached remainder; OpenAI reports the total and a cached subset). Adapters
// convert to this shape so cost math and cache analytics never care.
type Usage struct {
	// InputTokens is the uncached input processed at full price.
	InputTokens int `json:"input_tokens"`
	// CacheReadTokens were served from cache.
	CacheReadTokens int `json:"cache_read_tokens"`
	// CacheWrite5mTokens / CacheWrite1hTokens were written to cache this
	// request. Providers without a write premium report zero here.
	CacheWrite5mTokens int `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int `json:"cache_write_1h_tokens"`
	OutputTokens       int `json:"output_tokens"`
	// ReasoningTokens is the reasoning share of OutputTokens when reported.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// CacheWriteTokens is the total written to cache.
func (u Usage) CacheWriteTokens() int { return u.CacheWrite5mTokens + u.CacheWrite1hTokens }

// TotalInput is the full prompt size regardless of how it was billed.
func (u Usage) TotalInput() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens()
}

// HitRatio is the fraction of the prompt served from cache, in [0,1].
func (u Usage) HitRatio() float64 {
	t := u.TotalInput()
	if t == 0 {
		return 0
	}
	return float64(u.CacheReadTokens) / float64(t)
}

// Add returns the field-wise sum.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:        u.InputTokens + o.InputTokens,
		CacheReadTokens:    u.CacheReadTokens + o.CacheReadTokens,
		CacheWrite5mTokens: u.CacheWrite5mTokens + o.CacheWrite5mTokens,
		CacheWrite1hTokens: u.CacheWrite1hTokens + o.CacheWrite1hTokens,
		OutputTokens:       u.OutputTokens + o.OutputTokens,
		ReasoningTokens:    u.ReasoningTokens + o.ReasoningTokens,
	}
}
