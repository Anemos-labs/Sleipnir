package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// FuzzParse: whatever a configuration file contains, parsing neither panics nor
// returns a server that fails its own structural rules, and no error mentions a
// value it was given.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`{}`,
		`{"a":{"command":"x"}}`,
		`{"mcpServers":{"a":{"url":"https://h","headers":{"A":"` + placeholder + `"}}}}`,
		`{"a":{"command":"x","args":[1]}}`,
		`{"a":{"command":"x","env":{"K":null,"L":true,"M":1.5}}}`,
		`{"a":{"type":"sse","url":"https://h","timeout":"1s","allowTools":["*"]}}`,
		`{"a":5,"b":null,"c":[]}`,
		`{"":{}}`,
		`{"a":{"command":"x","timeout":1e308}}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(doc), &raw) != nil {
			return
		}
		for _, scope := range []Scope{"", ScopeUser, ScopeProject} {
			servers, issues := ParseWith(raw, ParseOptions{Scope: scope})
			for name, s := range servers {
				if s.EffectiveType() == "" || badServerName(name) != "" {
					t.Fatalf("returned an unusable server %q: %+v", name, s)
				}
				if scope != ScopeUser && (s.Trust || s.AllowPrivate) {
					t.Fatalf("privileges granted from an untrusted scope: %+v", s)
				}
				_ = s.String()
				_ = s.Fingerprint()
				_ = s.EnvRefs()
				_ = s.Redacted()
			}
			for _, is := range issues {
				if strings.Contains(is.Error(), placeholder) {
					t.Fatalf("issue leaks a value: %v", is)
				}
			}
		}
	})
}

// nopTransport swallows everything the client sends.
type nopTransport struct {
	mu sync.Mutex
	h  Handler
}

func (n *nopTransport) Start(h Handler) error { n.h = h; return nil }
func (n *nopTransport) Send(ctx context.Context, msg []byte) error {
	return nil
}
func (n *nopTransport) Close() error { return nil }

// FuzzClientHandle feeds arbitrary bytes to a live client as if a server had sent
// them. It must not panic, hang, or leave the client unable to fail cleanly.
func FuzzClientHandle(f *testing.F) {
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"x"}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":1,"progress":1}}`,
		`{"jsonrpc":"2.0","id":"a","method":"sampling/createMessage","params":{}}`,
		`[{"id":1,"result":1},{"method":"x"}]`,
		`{"id":1,"error":null,"result":null}`,
		`{"method":"notifications/message","params":{"data":{"a":[1,2,3]}}}`,
		`{"jsonrpc":"2.0","id":9223372036854775807,"result":{}}`,
		`[[[[[[[[`,
		``,
		`null`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, msg []byte) {
		c := NewClient(&nopTransport{}, ClientOptions{OnLog: func(LogMessage) {}, OnListChanged: func(ListKind) {}})
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			// A pending call, so responses have something to hit.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, _ = c.request(ctx, "ping", nil, callOpts{})
		}()
		c.handle(msg)
		c.handle(append([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), msg...))
		<-done
		_ = c.Close()
	})
}

func FuzzDecoders(f *testing.F) {
	for _, s := range []string{
		`{"name":"t","inputSchema":{"type":"object"}}`,
		`{"content":[{"type":"image","data":"AAAA"}]}`,
		`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}}}`,
		`{"messages":[{"role":"user","content":{"type":"text","text":"x"}}]}`,
		`{"contents":[{"uri":"x","blob":"!!"}]}`,
		`{"tools":[1,2],"nextCursor":5}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		raw := json.RawMessage(b)
		_ = decodeTool(raw)
		_ = decodePrompt(raw)
		_ = decodeContent(raw)
		_, _ = decodeCallToolResult(raw)
		_, _ = decodeInitialize(raw)
		_, _ = decodeReadResource(raw)
		_, _ = decodeGetPrompt(raw)
		_, _, _ = pageResult(raw, "tools")
		_, _ = decodeResource(raw)
		_, _ = decodeResourceTemplate(raw)
		_, _, _ = normalizeSchema(raw, 1024)
		_ = renderResult(&CallToolResult{Content: []Content{decodeContent(raw)}}, nil, false, nil)
	})
}
