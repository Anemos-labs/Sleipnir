package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/mcp/mcptest"
)

func sseCfg(url string) ServerConfig {
	c := httpCfg(url)
	c.Type = TypeSSE
	return c
}

func dialSSE(t *testing.T, url string, mod func(*ServerConfig), copts ClientOptions) (*Client, error) {
	t.Helper()
	cfg := sseCfg(url)
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, "legacy", cfg, DialOptions{Client: copts})
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

func TestLegacySSEFlow(t *testing.T) {
	s := mcptest.New()
	s.ProtocolVersion = "2024-11-05" // what servers of that era answer
	h := s.SSE(mcptest.SSEOptions{})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	got := make(chan ListKind, 4)
	c, err := dialSSE(t, ts.URL+"/sse", func(c *ServerConfig) {
		c.Headers = map[string]string{"X-Team": "core"}
	}, ClientOptions{OnListChanged: func(k ListKind) { got <- k }})
	if err != nil {
		t.Fatal(err)
	}
	if v := c.Initialized().ProtocolVersion; v != "2024-11-05" {
		t.Errorf("protocol = %s", v)
	}
	if out := callText(t, c, "echo", `{"message":"legacy"}`); out != "legacy" {
		t.Fatalf("echo = %q", out)
	}
	tools, _, err := c.ListTools(context.Background())
	if err != nil || len(tools) < 10 {
		t.Fatalf("%d tools, %v", len(tools), err)
	}
	// Server-initiated messages ride the same stream.
	if out := callText(t, c, "sampling", `{}`); !strings.Contains(out, "-32601") {
		t.Errorf("sampling must be refused over SSE too: %q", out)
	}
	s.NotifyToolsChanged()
	select {
	case k := <-got:
		if k != ListTools {
			t.Errorf("kind = %s", k)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("list_changed never arrived")
	}
	posted := h.PostedTo()
	if len(posted) < 4 {
		t.Fatalf("posts = %v", posted)
	}
	for _, p := range posted {
		if !strings.HasPrefix(p, "/messages?sessionId=") {
			t.Errorf("POSTed to %q, want the announced endpoint", p)
		}
	}
}

func TestLegacySSECrashEndsTheConnection(t *testing.T) {
	s := mcptest.New()
	ts := httptest.NewServer(s.SSE(mcptest.SSEOptions{}))
	t.Cleanup(ts.Close)
	c, err := dialSSE(t, ts.URL+"/sse", nil, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CallTool(context.Background(), "crash", nil, CallOptions{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("the call must fail when the stream dies")
	}
	select {
	case <-c.Done():
		if !errors.Is(c.Err(), ErrClosed) {
			t.Errorf("Err() = %v", c.Err())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream loss must end the connection so the manager reconnects")
	}
}

func TestLegacySSERefusesAnEndpointOnAnotherOrigin(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		t.Errorf("a request reached the foreign endpoint: %v", r.Header)
	}))
	t.Cleanup(other.Close)
	for name, endpoint := range map[string]string{
		"absolute other host": other.URL + "/steal?token=" + placeholder,
		"scheme change":       "https://127.0.0.1/steal",
		"protocol relative":   "//evil.example/steal",
	} {
		t.Run(name, func(t *testing.T) {
			s := mcptest.New()
			ts := httptest.NewServer(s.SSE(mcptest.SSEOptions{Endpoint: endpoint}))
			t.Cleanup(ts.Close)
			_, err := dialSSE(t, ts.URL+"/sse", func(c *ServerConfig) {
				c.Headers = map[string]string{"Authorization": "Bearer " + placeholder}
			}, ClientOptions{})
			if err == nil || !strings.Contains(err.Error(), "another origin") {
				t.Fatalf("err = %v, want a refusal", err)
			}
			if strings.Contains(err.Error(), placeholder) || strings.Contains(err.Error(), "steal") {
				t.Errorf("error leaks the endpoint: %v", err)
			}
		})
	}
	if elsewhere.Load() != 0 {
		t.Errorf("%d requests reached the foreign origin", elsewhere.Load())
	}
}

func TestLegacySSEAcceptsARelativeAndASameOriginAbsoluteEndpoint(t *testing.T) {
	ref := mcptest.New()
	sse := ref.SSE(mcptest.SSEOptions{})
	var ts *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) { sse.ServeHTTP(w, r) })
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) { sse.ServeHTTP(w, r) })
	// An absolute, same-origin endpoint announced by a server.
	abs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, "event: endpoint\ndata: %s/messages?sessionId=zzz\n\n", ts.URL)
		fl.Flush()
		<-r.Context().Done()
	})
	mux.Handle("/abs/sse", abs)
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	c, err := dialSSE(t, ts.URL+"/sse", nil, ClientOptions{})
	if err != nil {
		t.Fatalf("relative endpoint: %v", err)
	}
	callText(t, c, "echo", `{"message":"x"}`)

	// Absolute same-origin: accepted (the initialize then fails only because the
	// fake session id is unknown, which proves the POST went where it was told).
	_, err = dialSSE(t, ts.URL+"/abs/sse", nil, ClientOptions{})
	if err == nil || strings.Contains(err.Error(), "another origin") {
		t.Fatalf("absolute same-origin endpoint must be accepted, err = %v", err)
	}
}

func TestLegacySSEOnlyTheFirstEndpointCounts(t *testing.T) {
	var posts atomic.Int32
	var ts *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: endpoint\ndata: /first\n\n")
		fl.Flush()
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "event: endpoint\ndata: /second\n\n") // a mid-session re-point is ignored
		fl.Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/first", func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/second", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the second endpoint must never be used")
	})
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	tr, err := NewSSETransport(HTTPOptions{URL: ts.URL + "/sse", Net: NetOptions{AllowPrivate: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	if err := tr.Start(Handler{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := tr.Send(ctx, []byte(`{"jsonrpc":"2.0","method":"a"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := tr.Send(ctx, []byte(`{"jsonrpc":"2.0","method":"b"}`)); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 {
		t.Errorf("posts to the first endpoint = %d, want 2", posts.Load())
	}
}

func TestLegacySSENoEndpointMeansTheHandshakeTimesOut(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)
	cfg := sseCfg(ts.URL)
	cfg.StartupTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err := Dial(context.Background(), "legacy", cfg, DialOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("startup timeout ignored")
	}
}

func TestLegacySSEBadStreams(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"not found", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "HTTP 404"},
		{"wrong content type", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>"+placeholder+"</html>")
		}, "content type"},
		{"redirect elsewhere", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://other.example/sse?token="+placeholder, http.StatusFound)
		}, "redirect"},
		{"stream closes at once", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
		}, "closed the event stream"},
		{"unauthorised", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, "authentication required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(tt.handler)
			t.Cleanup(ts.Close)
			_, err := dialSSE(t, ts.URL, nil, ClientOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), placeholder) {
				t.Errorf("leak: %v", err)
			}
		})
	}
}

func TestLegacySSEPostFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprint(w, "event: endpoint\ndata: /post\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		http.Error(w, "boom "+placeholder, http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	_, err := dialSSE(t, ts.URL+"/sse", nil, ClientOptions{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || strings.Contains(err.Error(), placeholder) {
		t.Fatalf("err = %v", err)
	}
}
