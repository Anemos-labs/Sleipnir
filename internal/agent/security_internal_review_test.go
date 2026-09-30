package agent

import (
	"testing"
	"time"
)

// S26c (fixed): backoff used to honour the server's Retry-After without a ceiling, so one
// hostile or buggy 429 parked the agent (and, via swarm.Governor, every other agent) for as
// long as the header said. docs/reviews/security-robustness.md.
func TestSec_S26c_BackoffBoundsRetryAfter(t *testing.T) {
	if d := backoff(0, 95*365*24*time.Hour); d > MaxRetryAfter {
		t.Errorf("backoff(0, Retry-After=95y) = %v, want at most %v", d, MaxRetryAfter)
	}
	if d := backoff(0, 3*time.Second); d < 3*time.Second {
		t.Errorf("a modest Retry-After must still be honoured: %v", d)
	}
}
