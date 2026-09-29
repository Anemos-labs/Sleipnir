package core

import (
	"math"
	"sync"
)

// Estimator predicts token counts without a network call. The cache planner
// needs sizes constantly (is this prefix above the provider's minimum? does
// compaction pay for itself?), and an exact tokenizer per model is neither
// available nor worth a round trip.
type Estimator interface {
	// Tokens estimates the token count of s.
	Tokens(s string) int
	// Observe feeds back ground truth: a text of the given byte length that the
	// provider counted as tokens tokens. Implementations may ignore it.
	Observe(bytes, tokens int)
}

// BytesEstimator estimates tokens as bytes / ratio and self-calibrates from
// provider usage reports with an exponential moving average.
//
// Usage reports are a free tokenizer: with cache breakpoints at known
// positions, cache_read/cache_write counts reveal the exact token size of each
// layer, which Observe folds into the ratio.
type BytesEstimator struct {
	mu    sync.Mutex
	ratio float64 // bytes per token
	alpha float64
	n     int
}

// NewBytesEstimator starts from a conventional 3.6 bytes/token for mixed
// code and prose.
func NewBytesEstimator() *BytesEstimator {
	return &BytesEstimator{ratio: 3.6, alpha: 0.2}
}

// WithRatio sets the starting ratio.
func (e *BytesEstimator) WithRatio(r float64) *BytesEstimator {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r > 0.5 {
		e.ratio = r
	}
	return e
}

// Tokens implements Estimator.
func (e *BytesEstimator) Tokens(s string) int {
	if s == "" {
		return 0
	}
	e.mu.Lock()
	r := e.ratio
	e.mu.Unlock()
	return int(math.Ceil(float64(len(s)) / r))
}

// Observe implements Estimator. Tiny samples are ignored; they are dominated
// by fixed per-message overhead and would skew the ratio.
func (e *BytesEstimator) Observe(bytes, tokens int) {
	if bytes < 2048 || tokens <= 0 {
		return
	}
	sample := float64(bytes) / float64(tokens)
	if sample < 1.0 || sample > 8.0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	if e.n == 1 {
		e.ratio = sample
		return
	}
	e.ratio = (1-e.alpha)*e.ratio + e.alpha*sample
}

// Ratio reports the current bytes-per-token estimate.
func (e *BytesEstimator) Ratio() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ratio
}
