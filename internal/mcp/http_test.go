package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/mcp/mcptest"
)

// dialHTTP connects a client to url as a user-scoped, trusted, private-allowed
// streamable HTTP entry (test servers live on 127.0.0.1).
func dialHTTP(t *testing.T, url string, mod func(*ServerConfig), copts ClientOptions) (*Client, error) {
	t.Helper()
	cfg := httpCfg(url)
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, "web", cfg, DialOptions{Client: copts})
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

func mustDial(t *testing.T, url string, mod func(*ServerConfig), copts ClientOptions) *Client {
	t.Helper()
	c, err := dialHTTP(t, url, mod, copts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c
}

func callText(t *testing.T, c *Client, tool, args string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := c.CallTool(ctx, tool, json.RawMessage(args), CallOptions{})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	var sb strings.Builder
	for _, c := range r.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHTTPStreamableFlow(t *testing.T) {
	for _, mode := range []struct {
		name string
		opts mcptest.HTTPOptions
	}{
		{"sse responses with sessions", mcptest.HTTPOptions{}},
		{"json responses with sessions", mcptest.HTTPOptions{JSONOnly: true}},
		{"sse responses without sessions", mcptest.HTTPOptions{NoSessions: true}},
		{"json responses without sessions", mcptest.HTTPOptions{JSONOnly: true, NoSessions: true}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			ts, h := httpServer(t, mcptest.New(), mode.opts)
			c := mustDial(t, ts.URL+"/mcp", func(c *ServerConfig) {
				c.Headers = map[string]string{"X-Team": "core", "Authorization": "Bearer " + placeholder}
			}, ClientOptions{})
			if got := callText(t, c, "echo", `{"message":"over http"}`); got != "over http" {
				t.Fatalf("echo = %q", got)
			}
			tools, _, err := c.ListTools(context.Background())
			if err != nil || len(tools) < 10 {
				t.Fatalf("%d tools, %v", len(tools), err)
			}
			waitFor(t, "the GET stream to open", func() bool {
				for _, r := range h.Requests() {
					if r.Method == "GET" {
						return true
					}
				}
				return false
			})
			c.Close()

			reqs := h.Requests()
			var posts []mcptest.Recorded
			var sawDelete, sawGet bool
			for _, r := range reqs {
				switch r.Method {
				case "POST":
					posts = append(posts, r)
				case "DELETE":
					sawDelete = true
				case "GET":
					sawGet = true
					if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
						t.Errorf("GET Accept = %q", r.Header.Get("Accept"))
					}
				}
			}
			if len(posts) < 4 {
				t.Fatalf("only %d POSTs", len(posts))
			}
			first := posts[0]
			if !strings.Contains(first.Body, `"initialize"`) {
				t.Fatalf("first POST = %s", first.Body)
			}
			if first.Header.Get("Mcp-Session-Id") != "" || first.Header.Get("MCP-Protocol-Version") != "" {
				t.Error("initialize must carry neither a session nor a protocol version header")
			}
			for i, p := range posts {
				if ct := p.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("POST %d Content-Type = %q", i, ct)
				}
				if a := p.Header.Get("Accept"); !strings.Contains(a, "application/json") || !strings.Contains(a, "text/event-stream") {
					t.Errorf("POST %d Accept = %q: the spec requires both", i, a)
				}
				if p.Header.Get("X-Team") != "core" || p.Header.Get("Authorization") != "Bearer "+placeholder {
					t.Errorf("POST %d lost the configured headers: %v", i, p.Header)
				}
				if p.Header.Get("User-Agent") != "sleipnir-mcp" {
					t.Errorf("User-Agent = %q", p.Header.Get("User-Agent"))
				}
				if i == 0 {
					continue
				}
				if v := p.Header.Get("MCP-Protocol-Version"); v != LatestProtocolVersion {
					t.Errorf("POST %d MCP-Protocol-Version = %q, want %q", i, v, LatestProtocolVersion)
				}
				if !mode.opts.NoSessions && p.Header.Get("Mcp-Session-Id") == "" {
					t.Errorf("POST %d has no session id", i)
				}
			}
			if !sawGet {
				t.Error("no GET stream was opened")
			}
			if !mode.opts.NoSessions && !sawDelete {
				t.Error("closing must end the session with DELETE")
			}
			if !strings.Contains(posts[1].Body, "notifications/initialized") {
				t.Errorf("second POST should be the initialized notification, got %s", posts[1].Body)
			}
		})
	}
}

func TestHTTPProtocolVersionHeaderFollowsTheNegotiatedVersion(t *testing.T) {
	for _, ver := range []string{"2025-03-26", "2025-11-25", "2024-11-05"} {
		t.Run(ver, func(t *testing.T) {
			s := mcptest.New()
			s.ProtocolVersion = ver
			ts, h := httpServer(t, s, mcptest.HTTPOptions{JSONOnly: true})
			c := mustDial(t, ts.URL, nil, ClientOptions{})
			callText(t, c, "echo", `{"message":"x"}`)
			var last mcptest.Recorded
			for _, r := range h.Requests() {
				if r.Method == "POST" {
					last = r
				}
			}
			if got := last.Header.Get("MCP-Protocol-Version"); got != ver {
				t.Errorf("header = %q, want the negotiated %q", got, ver)
			}
			if inits := s.Initializes(); len(inits) != 1 || inits[0] != LatestProtocolVersion {
				t.Errorf("client offered %v, want exactly %s", inits, LatestProtocolVersion)
			}
		})
	}
}

func TestHTTPRejectsAnUnsupportedVersion(t *testing.T) {
	s := mcptest.New()
	s.ProtocolVersion = "2023-01-01"
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{})
	_, err := dialHTTP(t, ts.URL, nil, ClientOptions{})
	if !errors.Is(err, ErrProtocolVersion) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPProgressAndServerRequestsOverSSE(t *testing.T) {
	ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	var mu sync.Mutex
	var steps []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := c.CallTool(ctx, "progress", json.RawMessage(`{"steps":3}`), CallOptions{OnProgress: func(p Progress) {
		mu.Lock()
		steps = append(steps, p.Message)
		mu.Unlock()
	}})
	if err != nil || r.Content[0].Text != "progressed" {
		t.Fatalf("%v %v", r, err)
	}
	waitFor(t, "progress callbacks", func() bool { mu.Lock(); defer mu.Unlock(); return len(steps) == 3 })

	// The server asks for sampling on the POST's own stream: refused, and the
	// refusal is POSTed back so the tool can finish.
	got := callText(t, c, "sampling", `{}`)
	if !strings.Contains(got, "client error") || !strings.Contains(got, "-32601") || !strings.Contains(got, "sampling") {
		t.Errorf("sampling must be refused with an error: %q", got)
	}
	if got := callText(t, c, "ping_client", `{}`); !strings.Contains(got, "client result: {}") {
		t.Errorf("ping = %q", got)
	}
}

func TestHTTPListChangedArrivesOnTheGETStream(t *testing.T) {
	s := mcptest.New()
	ts, h := httpServer(t, s, mcptest.HTTPOptions{})
	got := make(chan ListKind, 8)
	mustDial(t, ts.URL, nil, ClientOptions{OnListChanged: func(k ListKind) { got <- k }})
	waitFor(t, "the GET stream", func() bool {
		for _, r := range h.Requests() {
			if r.Method == "GET" {
				return true
			}
		}
		return false
	})
	// The GET handler registers its stream after the request arrives; give it a
	// moment, then announce until the client hears it.
	deadline := time.After(5 * time.Second)
	for {
		s.NotifyToolsChanged()
		select {
		case k := <-got:
			if k != ListTools {
				t.Errorf("kind = %s", k)
			}
			return
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("tools/list_changed never arrived over the GET stream")
		}
	}
}

func TestHTTPServerWithoutAStreamStillWorks(t *testing.T) {
	ts, h := httpServer(t, mcptest.New(), mcptest.HTTPOptions{NoStream: true, JSONOnly: true})
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	if got := callText(t, c, "echo", `{"message":"no stream"}`); got != "no stream" {
		t.Fatal(got)
	}
	time.Sleep(100 * time.Millisecond)
	gets := 0
	for _, r := range h.Requests() {
		if r.Method == "GET" {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("%d GET attempts: a 405 must end the attempt for good", gets)
	}
	if c.Err() != nil {
		t.Errorf("%v", c.Err())
	}
}

func TestHTTPSessionExpiry(t *testing.T) {
	ts, h := httpServer(t, mcptest.New(), mcptest.HTTPOptions{JSONOnly: true})
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	callText(t, c, "echo", `{"message":"x"}`)
	h.ExpireSessions()
	_, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"message":"y"}`), CallOptions{})
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the client must end so the manager can start a new session")
	}
	if !errors.Is(c.Err(), ErrSessionExpired) || !errors.Is(c.Err(), ErrClosed) {
		t.Errorf("Err() = %v", c.Err())
	}
}

// statusServer answers initialize properly and everything after with a status.
func statusServer(t *testing.T, code int, body string, ctype string) *httptest.Server {
	t.Helper()
	ref := mcptest.New()
	inner := ref.HTTP(mcptest.HTTPOptions{JSONOnly: true})
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"tools/call"`) || (code >= 400 && !strings.Contains(string(b), "initialize") && r.Method == "POST" && calls.Add(1) > 100) {
			if ctype != "" {
				w.Header().Set("Content-Type", ctype)
			}
			w.WriteHeader(code)
			io.WriteString(w, body)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestHTTPErrorStatusesAreShortAndLeakNothing(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{401, "authentication required"},
		{403, "access denied"},
		{404, "session expired"}, // with a session established, 404 means the server forgot it (spec)
		{429, "rate limited"},
		{500, "HTTP 500"},
		{502, "HTTP 502"},
		{503, "HTTP 503"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.code), func(t *testing.T) {
			body := "<html>token " + placeholder + " internal path /srv/secret/app.py</html>"
			ts := statusServer(t, tt.code, body, "text/html")
			c := mustDial(t, ts.URL+"/mcp?key="+placeholder, func(c *ServerConfig) {
				c.Headers = map[string]string{"Authorization": "Bearer " + placeholder}
			}, ClientOptions{})
			_, err := c.CallTool(context.Background(), "echo", nil, CallOptions{})
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q lacks %q", err, tt.want)
			}
			for _, leak := range []string{placeholder, "/srv/secret", "<html>", "127.0.0.1"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error leaks %q: %v", leak, err)
				}
			}
		})
	}
}

func TestHTTPInitializeFailureNamesTheServerNotTheCredentials(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized: bad token "+placeholder, http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)
	_, err := dialHTTP(t, ts.URL+"/mcp?key="+placeholder, func(c *ServerConfig) {
		c.Headers = map[string]string{"Authorization": "Bearer " + placeholder}
	}, ClientOptions{})
	if err == nil || !strings.Contains(err.Error(), `server "web"`) || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), placeholder) {
		t.Errorf("credential in error: %v", err)
	}
}

func TestHTTPJSONRPCErrorInAnErrorStatusIsRelayed(t *testing.T) {
	ref := mcptest.New().HTTP(mcptest.HTTPOptions{JSONOnly: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"tools/call"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Bad Request: tool arguments invalid"}}`)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		ref.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	_, err := c.CallTool(context.Background(), "echo", nil, CallOptions{})
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Code != -32600 || !strings.Contains(rpc.Message, "arguments invalid") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPAcceptedButUnansweredRequestIsAnError(t *testing.T) {
	ts := statusServer(t, http.StatusAccepted, "", "")
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	_, err := c.CallTool(context.Background(), "echo", nil, CallOptions{})
	if err == nil || !strings.Contains(err.Error(), "202") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPUnexpectedContentType(t *testing.T) {
	ts := statusServer(t, 200, "<html>"+placeholder+"</html>", "text/html; charset=utf-8")
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	_, err := c.CallTool(context.Background(), "echo", nil, CallOptions{})
	if err == nil || !strings.Contains(err.Error(), "content type") || strings.Contains(err.Error(), placeholder) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPOversizedResponses(t *testing.T) {
	for _, mode := range []mcptest.HTTPOptions{{JSONOnly: true}, {}} {
		name := "sse"
		if mode.JSONOnly {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			ts, _ := httpServer(t, mcptest.New(), mode)
			cfg := httpCfg(ts.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, err := Dial(ctx, "web", cfg, DialOptions{MaxMessageBytes: 64 << 10})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			start := time.Now()
			_, err = c.CallTool(ctx, "big", json.RawMessage(`{"bytes":500000}`), CallOptions{})
			if !errors.Is(err, ErrMessageTooLarge) {
				t.Fatalf("err = %v, want ErrMessageTooLarge", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Error("slow to fail")
			}
			// The client is fine afterwards.
			if got := callText(t, c, "echo", `{"message":"after"}`); got != "after" {
				t.Errorf("after = %q", got)
			}
		})
	}
}

func TestHTTPStreamEndingEarlyFailsTheCallPromptly(t *testing.T) {
	ref := mcptest.New().HTTP(mcptest.HTTPOptions{JSONOnly: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"tools/call"`) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			io.WriteString(w, ": nothing to see\n\n")
			return // stream closes without an answer
		}
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		ref.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	start := time.Now()
	_, err := c.CallTool(context.Background(), "echo", nil, CallOptions{Timeout: 30 * time.Second})
	var rpc *RPCError
	if !errors.As(err, &rpc) || !strings.Contains(rpc.Message, "closed the response stream") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("must not wait for the timeout")
	}
}

func TestHTTPRedirects(t *testing.T) {
	t.Run("same origin is followed with body and headers", func(t *testing.T) {
		ref := mcptest.New().HTTP(mcptest.HTTPOptions{JSONOnly: true})
		var sawKey atomic.Bool
		mux := http.NewServeMux()
		mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/mcp", http.StatusTemporaryRedirect)
		})
		mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") == placeholder {
				sawKey.Store(true)
			}
			ref.ServeHTTP(w, r)
		})
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		c := mustDial(t, ts.URL+"/old", func(c *ServerConfig) { c.Headers = map[string]string{"X-Api-Key": placeholder} }, ClientOptions{})
		if got := callText(t, c, "echo", `{"message":"redirected"}`); got != "redirected" {
			t.Fatal(got)
		}
		if !sawKey.Load() {
			t.Error("same-origin redirect should keep the headers")
		}
	})
	t.Run("cross origin is refused and nothing is sent there", func(t *testing.T) {
		var elsewhere atomic.Int32
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			elsewhere.Add(1)
			t.Errorf("request reached another origin with headers %v", r.Header)
		}))
		t.Cleanup(other.Close)
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/steal?token="+placeholder, http.StatusTemporaryRedirect)
		}))
		t.Cleanup(origin.Close)
		_, err := dialHTTP(t, origin.URL+"/mcp", func(c *ServerConfig) {
			c.Headers = map[string]string{"X-Api-Key": placeholder, "Authorization": "Bearer " + placeholder}
		}, ClientOptions{})
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("err = %v, want a refusal to follow the redirect", err)
		}
		if strings.Contains(err.Error(), placeholder) || strings.Contains(err.Error(), "/steal") {
			t.Errorf("error leaks the redirect target: %v", err)
		}
		if elsewhere.Load() != 0 {
			t.Errorf("%d requests reached the other origin", elsewhere.Load())
		}
	})
	t.Run("too many redirects", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, r.URL.Path+"x", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(ts.Close)
		if _, err := dialHTTP(t, ts.URL+"/a", nil, ClientOptions{}); err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestHTTPGuardRefusesPrivateAddressesByDefault(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the guard must stop the connection before it reaches the server")
	}))
	t.Cleanup(ts.Close)
	cfg := ServerConfig{Type: TypeHTTP, URL: ts.URL + "/mcp?key=" + placeholder, Trust: true, Scope: ScopeUser}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, "web", cfg, DialOptions{})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if !strings.Contains(err.Error(), "loopback") || strings.Contains(err.Error(), placeholder) {
		t.Errorf("err = %v", err)
	}
}

func TestHTTPCleartextNeedsAPrivateAllowance(t *testing.T) {
	cfg := ServerConfig{Type: TypeHTTP, URL: "http://mcp.example.com/mcp", Trust: true, Scope: ScopeUser}
	_, err := Dial(context.Background(), "web", cfg, DialOptions{})
	if err == nil || !strings.Contains(err.Error(), "plain http://") {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewHTTPTransport(HTTPOptions{URL: "http://localhost:1/mcp", Net: NetOptions{AllowPrivate: true}}); err != nil {
		t.Errorf("a local server over http must be allowed: %v", err)
	}
}

func TestHTTPDeadServerEndsTheConnectionAfterRepeatedFailures(t *testing.T) {
	ts := httptest.NewServer(mcptest.New().HTTP(mcptest.HTTPOptions{JSONOnly: true, NoStream: true}))
	url := ts.URL + "/mcp?key=" + placeholder
	c := mustDial(t, url, nil, ClientOptions{})
	callText(t, c, "echo", `{"message":"x"}`)
	ts.Close()
	var errs []error
	for i := 0; i < maxConsecutiveFailures; i++ {
		_, err := c.CallTool(context.Background(), "echo", nil, CallOptions{})
		errs = append(errs, err)
	}
	for _, err := range errs {
		if err == nil {
			t.Fatal("a call to a dead server succeeded")
		}
		if strings.Contains(err.Error(), placeholder) || strings.Contains(err.Error(), "/mcp") {
			t.Errorf("transport error leaks the URL: %v", err)
		}
	}
	select {
	case <-c.Done():
		if !strings.Contains(c.Err().Error(), "unreachable") {
			t.Errorf("Err() = %v", c.Err())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("repeated transport failures must end the connection so it can be re-established")
	}
}

func TestHTTPTimeoutAbortsTheRequestAndCancels(t *testing.T) {
	ref := mcptest.New().HTTP(mcptest.HTTPOptions{JSONOnly: true})
	aborted := make(chan struct{})
	var cancelSeen atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(b), `"tools/call"`):
			<-r.Context().Done() // hangs until the client gives up
			close(aborted)
			return
		case strings.Contains(string(b), "notifications/cancelled"):
			cancelSeen.Store(true)
		}
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		ref.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	c := mustDial(t, ts.URL, nil, ClientOptions{})
	_, err := c.CallTool(context.Background(), "hang", nil, CallOptions{Timeout: 150 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("the HTTP request was not aborted when the call gave up")
	}
	waitFor(t, "notifications/cancelled", cancelSeen.Load)
}

func TestHTTPSecretsInHeadersNeverReachErrorsOrLogs(t *testing.T) {
	var logs []string
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A careless server echoing the credential it was sent.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"bad credential %s"}}`, r.Header.Get("Authorization"))
	}))
	t.Cleanup(ts.Close)
	cfg := httpCfg(ts.URL)
	cfg.Headers = map[string]string{"Authorization": "Bearer " + placeholder}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, "web", cfg, DialOptions{Client: ClientOptions{Logf: func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	}}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), placeholder) {
		t.Errorf("the server echoed the credential and it reached the error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, placeholder) {
			t.Errorf("credential in log: %s", l)
		}
	}
}

func TestHTTPRejectsReservedHeadersUpFront(t *testing.T) {
	for _, h := range []string{"Host", "Content-Type", "Mcp-Session-Id", "MCP-Protocol-Version", "Accept", "Content-Length", "Transfer-Encoding", "Connection"} {
		_, err := NewHTTPTransport(HTTPOptions{URL: "https://h.example/mcp", Headers: map[string]string{h: "x"}})
		if err == nil || !strings.Contains(err.Error(), "managed by the transport") {
			t.Errorf("%s: err = %v", h, err)
		}
	}
}

// Close must not wait for anything that has already finished: a client that has
// streamed responses closes promptly (a stuck WaitGroup once made every HTTP
// Close cost its 3 second safety timeout).
func TestHTTPCloseIsPrompt(t *testing.T) {
	for name, mode := range map[string]mcptest.HTTPOptions{"sse": {}, "json": {JSONOnly: true}} {
		t.Run(name, func(t *testing.T) {
			ts, _ := httpServer(t, mcptest.New(), mode)
			c := mustDial(t, ts.URL, nil, ClientOptions{})
			callText(t, c, "echo", `{"message":"x"}`)
			callText(t, c, "progress", `{"steps":2}`)
			if _, _, err := c.ListTools(context.Background()); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			c.Close()
			if d := time.Since(start); d > time.Second {
				t.Errorf("Close took %v", d)
			}
		})
	}
}
