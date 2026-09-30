package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// harness runs web_fetch the way the agent loop does, with a fake clock so
// cache expiry needs no sleeping.
type harness struct {
	t     *testing.T
	f     *fetcher
	tool  *fetchTool
	blobs *events.MemBlobs

	mu    sync.Mutex
	clock time.Time
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	f := newFetcher(cfg)
	h := &harness{t: t, f: f, tool: &fetchTool{f: f}, blobs: events.NewMemBlobs(), clock: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	t.Cleanup(func() {
		if tr, ok := f.client.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	})
	return h
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.mu.Unlock()
}

func (h *harness) env(agent string) *tools.Env {
	return &tools.Env{Agent: agent, Blobs: h.blobs, Now: h.now}
}

func (h *harness) run(env *tools.Env, input any) *tools.Result {
	h.t.Helper()
	res, err := h.tryRun(env, input)
	if err != nil {
		h.t.Fatalf("web_fetch: harness error: %v", err)
	}
	return res
}

func (h *harness) tryRun(env *tools.Env, input any) (*tools.Result, error) {
	var raw json.RawMessage
	switch v := input.(type) {
	case json.RawMessage:
		raw = v
	case string:
		raw = json.RawMessage(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return h.tool.Run(context.Background(), &tools.Call{ID: "c1", Name: "web_fetch", Input: raw, Env: env})
}

// get fetches url with default paging.
func (h *harness) get(url string, extra ...map[string]any) *tools.Result {
	h.t.Helper()
	in := map[string]any{"url": url}
	for _, e := range extra {
		for k, v := range e {
			in[k] = v
		}
	}
	return h.run(h.env("agent"), in)
}

// server starts a test server whose handler is counted. Server errors (for
// example a client hanging up mid-body) are not test output.
func newServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, hits
}

func serve(t *testing.T, ctype, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	return newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		io.WriteString(w, body)
	})
}

// splitPage separates a web_fetch result into its header line, the page body
// and the continuation trailer.
func splitPage(t *testing.T, text string) (header, body, trailer string) {
	t.Helper()
	if !strings.HasPrefix(text, "[fetched ") {
		t.Fatalf("result does not start with a header line: %.120q", text)
	}
	nl := strings.IndexByte(text, '\n')
	header, rest := text[:nl], text[nl+1:]
	if strings.HasPrefix(rest, "Title: ") {
		rest = rest[strings.IndexByte(rest, '\n')+1:]
	}
	rest = strings.TrimPrefix(rest, "\n")
	if i := strings.LastIndex(rest, "\n\n[showing characters "); i >= 0 {
		return header, rest[:i], rest[i+2:]
	}
	return header, rest, ""
}

// staticLookup makes names resolve without DNS.
func staticLookup(m map[string]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		if v, ok := m[host]; ok {
			return []netip.Addr{netip.MustParseAddr(v)}, nil
		}
		return nil, fmt.Errorf("no such host %s", host)
	}
}

// recordingPerm is a Requester that records requests and answers fixedly.
type recordingPerm struct {
	mu    sync.Mutex
	reqs  []perm.Request
	allow bool
	why   string
}

func (r *recordingPerm) Check(_ context.Context, req perm.Request) perm.Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return perm.Decision{Allow: r.allow, Reason: r.why}
}

func (r *recordingPerm) seen() []perm.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]perm.Request(nil), r.reqs...)
}

// tcpPort returns the port of an httptest server.
func tcpPort(srv *httptest.Server) string {
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return port
}
