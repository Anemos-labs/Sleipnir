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
