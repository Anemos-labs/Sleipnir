package agent

import (
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// What a wait cures is an endpoint that answered that it is down or overloaded. A failure that came with no status may be an
// endpoint that does not exist, and a request the endpoint refused is wrong however long it waits.
func TestOnlyAnEndpointThatSaidItIsDownOrOverloadedIsWaitedFor(t *testing.T) {
	for _, c := range []struct {
		name string
		pe   provider.Error
		want bool
	}{
		{"overloaded", provider.Error{Kind: provider.ErrOverloaded, Status: 529}, true},
		{"database unavailable", provider.Error{Kind: provider.ErrServer, Status: 503}, true},
		{"internal error", provider.Error{Kind: provider.ErrServer, Status: 500}, true},
		{"bad gateway", provider.Error{Kind: provider.ErrServer, Status: 502}, true},
		{"gateway timeout", provider.Error{Kind: provider.ErrServer, Status: 504}, true},
		{"rate limit", provider.Error{Kind: provider.ErrRateLimit, Status: 429}, true},
		{"a refused connection", provider.Error{Kind: provider.ErrNetwork}, false},
		{"a dns failure", provider.Error{Kind: provider.ErrNetwork, Status: 0}, false},
		{"nothing for a minute", provider.Error{Kind: provider.ErrTimeout}, false},
		{"request timeout", provider.Error{Kind: provider.ErrServer, Status: 408}, false},
		{"bad request", provider.Error{Kind: provider.ErrServer, Status: 400}, false},
		{"not found", provider.Error{Kind: provider.ErrServer, Status: 404}, false},
	} {
		if got := outage(&c.pe); got != c.want {
			t.Errorf("%s (status %d): outage = %v, want %v", c.name, c.pe.Status, got, c.want)
		}
	}
}

// A request that is retried for minutes goes far past the sixth attempt: the shift that grows the wait must not overflow into a
// negative one, which would turn the longest wait into none and the retries into a storm.
func TestBackoffNeverOverflowsHoweverManyTimesARequestIsRetried(t *testing.T) {
	old := RetryBase
	RetryBase = time.Second
	defer func() { RetryBase = old }()
	for _, attempt := range []int{0, 5, 6, 20, 40, 63, 64, 100, 10_000} {
		d := backoff(attempt, 0)
		if d <= 0 || d > 75*time.Second { // the ceiling is 60 s, and the jitter adds up to a quarter
			t.Errorf("backoff(%d) = %v, want a wait between nothing and a minute and a quarter", attempt, d)
		}
	}
}

// The wait before the next attempt of a request that fails for an outage: the first six attempts wait as they always did, the ones
// patience adds stop growing at half a minute, what the endpoint asked for is a floor of every wait, and the patience is a
// ceiling of all of them together.
func TestRetryDelay(t *testing.T) {
	old := RetryBase
	RetryBase = time.Second
	defer func() { RetryBase = old }()
	down := &provider.Error{Kind: provider.ErrServer, Status: 503}
	slow := &provider.Error{Kind: provider.ErrRateLimit, Status: 429, RetryAfter: 45 * time.Second}
	gone := &provider.Error{Kind: provider.ErrNetwork}
	const patience = 5 * time.Minute

	// the first six attempts, with or without patience: the same as ever, and then nothing
	for attempt := 0; attempt < maxAttempts-1; attempt++ {
		for _, p := range []time.Duration{0, patience} {
			d, again := retryDelay(attempt, down, 0, p)
			if !again || d <= 0 || d > 21*time.Second { // 16 s with a quarter of jitter is the longest
				t.Errorf("attempt %d, patience %v: delay %v again %v", attempt, p, d, again)
			}
		}
	}
	if _, again := retryDelay(maxAttempts-1, down, 0, 0); again {
		t.Error("the sixth failure with no patience must be the last")
	}
	// an outage past the sixth attempt: more, and never more than half a minute apart (the first of them, whose wait would have been
	// 32 s, is between 24 and 30 with the jitter; every later one is the half minute)
	for attempt := maxAttempts - 1; attempt < maxAttempts+30; attempt++ {
		d, again := retryDelay(attempt, down, time.Minute, patience)
		if !again || d > maxOutageDelay || d < 24*time.Second || (attempt > maxAttempts-1 && d != maxOutageDelay) {
			t.Errorf("attempt %d: delay %v again %v, want at most %v and another attempt", attempt, d, again, maxOutageDelay)
		}
	}
	// what the endpoint asked for is a floor, even past the half minute
	if d, again := retryDelay(maxAttempts, slow, time.Minute, patience); !again || d < 45*time.Second || d > MaxRetryAfter {
		t.Errorf("Retry-After of 45s: delay %v again %v", d, again)
	}
	// the patience is a ceiling of the waits together: the last wait is cut, and nothing follows it
	if d, again := retryDelay(maxAttempts, down, patience-4*time.Second, patience); !again || d != 4*time.Second {
		t.Errorf("4s of patience left: delay %v again %v, want 4s and another attempt", d, again)
	}
	if _, again := retryDelay(maxAttempts, down, patience, patience); again {
		t.Error("an agent whose patience is used up must not try again")
	}
	// a failure that is not an outage gets the six attempts and nothing more, whatever the patience
	if _, again := retryDelay(maxAttempts-1, gone, 0, patience); again {
		t.Error("a failure with no status was waited for")
	}
	if d, again := retryDelay(0, gone, 0, patience); !again || d <= 0 {
		t.Errorf("the first attempts of a failure with no status are retried as ever: %v %v", d, again)
	}
}
