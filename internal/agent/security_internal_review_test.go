package agent

// Security review repro for docs/reviews/security-robustness.md (gated: SLEIPNIR_REVIEW=1,
// asserts the SECURE behaviour and fails while the finding is open).

import (
	"os"
	"testing"
	"time"
)

// S26c: backoff honours the server's Retry-After without a ceiling, so one hostile or buggy 429
// parks the agent (and, via swarm.Governor, every other agent) for as long as the header says.
func TestSecReview_S26c_BackoffHonoursUnboundedRetryAfter(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1")
	}
	if d := backoff(0, 95*365*24*time.Hour); d > 10*time.Minute {
		t.Errorf("S26c: backoff(0, Retry-After=95y) = %v", d)
	}
}
