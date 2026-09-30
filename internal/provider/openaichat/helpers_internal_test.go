package openaichat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/provider"
)

// scripted serves every request with script. gone receives a value when a handler
// notices that the client went away (the request context ended).
type scripted struct {
	*httptest.Server
	gone chan struct{}
	hits atomic.Int32
}

func newScripted(t *testing.T, script func(w http.ResponseWriter, fl http.Flusher, r *http.Request)) *scripted {
	t.Helper()
	s := &scripted{gone: make(chan struct{}, 16)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		// net/http only watches for a client disconnect once the request body has been read.
		io.Copy(io.Discard, r.Body)
		fl, _ := w.(http.Flusher)
		script(w, fl, r)
		select {
		case <-r.Context().Done():
			s.gone <- struct{}{}
		default:
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func sse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
}

// frame is one SSE data frame carrying a content delta.
func frame(content string) string {
	return `data: {"id":"g","choices":[{"index":0,"delta":{"content":"` + content + `"}}]}` + "\n\n"
}

func rawFrame(json string) string { return "data: " + json + "\n\n" }

func finish(reason string) string {
	return `data: {"id":"g","choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}` + "\n\ndata: [DONE]\n\n"
}

// call runs one streaming request against url with cfg.
func call(t *testing.T, url string, cfg Config, on func(provider.Event)) (*provider.Response, error) {
	t.Helper()
	cfg.Name, cfg.BaseURL = "t", url
	return New(cfg).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hello")}, on)
}

func wantProviderError(t *testing.T, err error, kind provider.ErrKind, retryable bool) *provider.Error {
	t.Helper()
	pe, ok := provider.AsError(err)
	if !ok {
		t.Fatalf("want a *provider.Error, got %T: %v", err, err)
	}
	if pe.Kind != kind || pe.Retryable() != retryable {
		t.Fatalf("kind = %v retryable = %v, want %v %v (%v)", pe.Kind, pe.Retryable(), kind, retryable, pe)
	}
	return pe
}

func waitGone(t *testing.T, s *scripted) {
	t.Helper()
	select {
	case <-s.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not closed: the server never saw the client go away")
	}
}

// stubTransport answers every request with status and body, recording what it saw.
type stubTransport struct {
	status int
	body   string
	seen   []*http.Request
}

func (s *stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.seen = append(s.seen, r)
	h := http.Header{"Content-Type": []string{"application/json"}}
	return &http.Response{
		StatusCode: s.status, Header: h, Request: r,
		Body: io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

const okBody = `{"id":"g","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

// refuseAll is a client that fails the test if any request is sent.
func refuseAll(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a request was sent to %s", r.URL)
		return nil, fmt.Errorf("blocked")
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
