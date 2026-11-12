package openaichat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
)

// serveFrames answers every request with the given SSE frames, or with body as a plain JSON response when
// frames is nil, and records the request headers.
func serveFrames(t *testing.T, frames []string, body string, opts openaichat.Options, headers map[string]string) (*openaichat.Client, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		if frames == nil {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			io.WriteString(w, "data: "+f+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(ts.Close)
	c := openaichat.New(openaichat.Config{Name: "x", BaseURL: ts.URL, APIKey: "k", Options: opts, Headers: headers})
	return c, func() http.Header { mu.Lock(); defer mu.Unlock(); return got }
}

func toolCalls(t *testing.T, c *openaichat.Client, noStream bool) []core.Block {
	t.Helper()
	r, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("", user("go")), NoStream: noStream}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stop != core.StopToolUse {
		t.Fatalf("stop = %v, want tool use", r.Stop)
	}
	return r.Turn.ToolCalls()
}

func wantCalls(t *testing.T, calls []core.Block, want ...[3]string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("%d tool calls, want %d: %+v", len(calls), len(want), calls)
	}
	for i, w := range want {
		c := calls[i]
		if c.ToolID != w[0] || c.ToolName != w[1] || string(c.Input) != w[2] || c.Invalid != "" {
			t.Fatalf("call %d = id %q name %q input %s invalid %q, want %v", i, c.ToolID, c.ToolName, c.Input, c.Invalid, w)
		}
	}
}

// An endpoint that sends each call whole in a delta of its own, without an index, gets one call per id: keyed by
// the missing index (0), every call of the turn was appended into one, `{"path":"a"}{"command":"ls"}`, not JSON.
func TestStreamedToolCallsWithoutIndexAreKeptApart(t *testing.T) {
	c, _ := serveFrames(t, []string{
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\": \"a\"}"}}]}}]}`,
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"id":"c2","type":"function","function":{"name":"bash","arguments":"{\"command\": \"ls\"}"}}]}}]}`,
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}, "", openaichat.Options{}, nil)
	wantCalls(t, toolCalls(t, c, false), [3]string{"c1", "read", `{"path": "a"}`}, [3]string{"c2", "bash", `{"command": "ls"}`})
}

// One call streamed in pieces without an index, its id only on the first piece (or repeated on each), stays one call.
func TestStreamedToolCallWithoutIndexContinuesAcrossDeltas(t *testing.T) {
	for name, second := range map[string]string{
		"id once":     `{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"\"a\"}"}}]}}]}`,
		"id repeated": `{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"id":"c1","function":{"arguments":"\"a\"}"}}]}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := serveFrames(t, []string{
				`{"id":"r","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\": "}}]}}]}`,
				second,
				`{"id":"r","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			}, "", openaichat.Options{}, nil)
			wantCalls(t, toolCalls(t, c, false), [3]string{"c1", "read", `{"path": "a"}`})
		})
	}
}

// Deltas that carry an index are keyed by it, interleaved or not, as before.
func TestStreamedToolCallsWithIndexInterleave(t *testing.T) {
	c, _ := serveFrames(t, []string{
		`{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\""}}]}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"bash","arguments":"{\"command\""}}]}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":": \"a\"}"}}]}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":": \"ls\"}"}}]}}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}, "", openaichat.Options{}, nil)
	wantCalls(t, toolCalls(t, c, false), [3]string{"c1", "read", `{"path": "a"}`}, [3]string{"c2", "bash", `{"command": "ls"}`})
}

// A plain response lists its calls without indexes: each is its own call.
func TestNonStreamedToolCallsAreKeptApart(t *testing.T) {
	c, _ := serveFrames(t, nil, `{"id":"r","model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,`+
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\": \"a\"}"}},`+
		`{"id":"c2","type":"function","function":{"name":"bash","arguments":"{\"command\": \"ls\"}"}}]}}],`+
		`"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`, openaichat.Options{}, nil)
	wantCalls(t, toolCalls(t, c, true), [3]string{"c1", "read", `{"path": "a"}`}, [3]string{"c2", "bash", `{"command": "ls"}`})
}

var textFrames = []string{
	`{"id":"r","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
}

func send(t *testing.T, c *openaichat.Client) {
	t.Helper()
	p := prompt("", user("go"))
	p.CacheKey = "sl:abcd1234:0123456789ab:0"
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil); err != nil {
		t.Fatal(err)
	}
}

// session_header_name sends the routing key under the endpoint's own header (OpenCode Go: x-opencode-session)
// and not as X-Session-Id; session_header alone keeps X-Session-Id.
func TestSessionHeaderName(t *testing.T) {
	c, hdr := serveFrames(t, textFrames, "", openaichat.Options{SessionHeaderName: "x-opencode-session"}, nil)
	send(t, c)
	if got := hdr().Get("x-opencode-session"); got != "sl:abcd1234:0123456789ab:0" {
		t.Fatalf("x-opencode-session = %q", got)
	}
	if got := hdr().Get("X-Session-Id"); got != "" {
		t.Fatalf("X-Session-Id = %q, want none", got)
	}

	c, hdr = serveFrames(t, textFrames, "", openaichat.Options{SessionHeader: true}, nil)
	send(t, c)
	if got := hdr().Get("X-Session-Id"); got != "sl:abcd1234:0123456789ab:0" {
		t.Fatalf("X-Session-Id = %q", got)
	}

	c, hdr = serveFrames(t, textFrames, "", openaichat.Options{}, nil)
	send(t, c)
	if hdr().Get("X-Session-Id") != "" || hdr().Get("x-opencode-session") != "" {
		t.Fatalf("a routing key was sent without being asked for: %v", hdr())
	}
}

// Requests name the client instead of Go's HTTP-library default; a configured User-Agent still wins.
func TestUserAgent(t *testing.T) {
	c, hdr := serveFrames(t, textFrames, "", openaichat.Options{}, nil)
	send(t, c)
	if got := hdr().Get("User-Agent"); got != "sleipnir" {
		t.Fatalf("User-Agent = %q", got)
	}
	c, hdr = serveFrames(t, textFrames, "", openaichat.Options{}, map[string]string{"User-Agent": "acme/2"})
	send(t, c)
	if got := hdr().Get("User-Agent"); got != "acme/2" {
		t.Fatalf("User-Agent = %q, want the configured one", got)
	}
}
