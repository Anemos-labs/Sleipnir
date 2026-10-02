package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// "server; retrying in 877ms" told a person nothing while a swarm waited out a provider that was
// down: the notice names the failure, its status and what the endpoint said.
func TestRetryNoticeSaysWhatFailed(t *testing.T) {
	pe := &provider.Error{Kind: provider.ErrServer, Status: 502,
		Message: "The provider returned an error (error code: 1033) [provider Consensus Protocol]"}
	got := retryNotice(pe, 877*time.Millisecond+400*time.Microsecond)
	want := "server (http 502): The provider returned an error (error code: 1033) [provider Consensus Protocol]; retrying in 877ms"
	if got != want {
		t.Fatalf("retryNotice = %q, want %q", got, want)
	}
	if got, want := retryNotice(&provider.Error{Kind: provider.ErrRateLimit}, 2*time.Second), "rate_limit; retrying in 2s"; got != want {
		t.Fatalf("a failure with no status and no message: %q, want %q", got, want)
	}
}

// What the endpoint said is untrusted text on a person's terminal: it is bounded and free of
// escape sequences and control characters.
func TestRetryNoticeBoundsWhatTheEndpointSaid(t *testing.T) {
	pe := &provider.Error{Kind: provider.ErrServer, Status: 500,
		Message: "\x1b[2J\x1b]0;owned\x07boom\r\n" + strings.Repeat("x", 5000)}
	got := retryNotice(pe, time.Second)
	if strings.ContainsAny(got, "\x1b\x07\r\n") {
		t.Fatalf("control characters reached the notice: %q", got)
	}
	if len(got) > 300 {
		t.Fatalf("the notice is %d bytes long", len(got))
	}
	if !strings.HasSuffix(got, "; retrying in 1s") || !strings.Contains(got, "boom") {
		t.Fatalf("unexpected notice: %q", got)
	}
}

func TestTokenCountIsReadableAtEverySize(t *testing.T) {
	for in, want := range map[int]string{0: "0", 669: "669", 999: "999", 1000: "1.0k", 2400: "2.4k", 9999: "10.0k", 10_000: "10k", 31_200: "31k"} {
		if got := tokenCount(in); got != want {
			t.Errorf("tokenCount(%d) = %q, want %q", in, got, want)
		}
	}
}

// While an outage is waited out, a retry is announced once in each half minute and not every time: seven lines of the same 503 in a minute
// filled a person's screen, and the status line says how long the model has not answered.
func TestRetriesInALongOutageAreAnnouncedOnceInHalfAMinute(t *testing.T) {
	announced := -1
	var said []int
	for attempt, waited := 0, time.Duration(0); attempt < 14; attempt++ {
		waited += min(time.Duration(1<<attempt)*time.Second, 10*time.Second)
		if say, half := sayRetry(attempt, waited, announced); say {
			said = append(said, attempt)
			announced = half
		}
	}
	// attempts 0 to 4 are the usual ones; 5 is the first the outage adds; then one for each half minute of waiting
	if len(said) > 9 || said[0] != 0 || said[4] != 4 || said[5] != 5 {
		t.Errorf("announced the retries after attempts %v", said)
	}
	if len(said) >= 14 {
		t.Errorf("every retry was announced: %v", said)
	}
}
