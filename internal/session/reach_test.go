package session_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// An interactive session whose endpoint cannot be reached says so as it starts, instead of reading as ready until its first
// request has spent its retries; one whose endpoint answers says nothing.
func TestAnInteractiveSessionSaysWhenItsEndpointCannotBeReached(t *testing.T) {
	start := func(base string) *noticeSink {
		t.Helper()
		client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
		if base != "" {
			client = openaichat.New(openaichat.Config{Name: "gone", BaseURL: base})
		}
		o := opts(t, newRepo(t), client, model)
		o.Interactive = true
		sink := &noticeSink{}
		o.Sink = sink
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return sink
	}
	gone := httptest.NewServer(nil)
	base := gone.URL + "/v1"
	gone.Close()

	sink := start(base)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(sink.all(), "cannot reach") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := sink.all(); !strings.Contains(got, "cannot reach "+strings.TrimPrefix(gone.URL, "http://")) || !strings.Contains(got, "connection refused") {
		t.Fatalf("an unreachable endpoint was not named: %q", got)
	}

	sink = start("")
	time.Sleep(200 * time.Millisecond)
	if got := sink.all(); strings.Contains(got, "cannot reach") {
		t.Fatalf("an endpoint that answers was called unreachable: %q", got)
	}
}
