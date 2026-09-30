package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/anthropic"
)

const testKey = "test-key-not-a-secret"

type captured struct {
	method, path string
	header       http.Header
	body         []byte
}

// capture serves a canned 200 SSE reply and records every request.
type capture struct {
	mu   sync.Mutex
	reqs []captured
	ts   *httptest.Server
}

func newCapture(t *testing.T, fixture string) *capture {
	t.Helper()
	data, err := readFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	c := &capture{}
	c.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs = append(c.reqs, captured{r.Method, r.URL.Path, r.Header.Clone(), b})
		c.mu.Unlock()
		if strings.HasSuffix(fixture, ".json") {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.Write(data)
	}))
	t.Cleanup(c.ts.Close)
	return c
}

func (c *capture) last(t *testing.T) captured {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		t.Fatal("no request reached the server")
	}
	return c.reqs[len(c.reqs)-1]
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func readFixture(name string) ([]byte, error) {
	if strings.HasSuffix(name, ".json") {
		return readFile("testdata/json/" + name)
	}
	return readFile("testdata/sse/" + name)
}

func TestClientRequestWiring(t *testing.T) {
	cp := newCapture(t, "text.sse")
	c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL + "/", APIKey: testKey})
	p := hello("claude-opus-5-5")
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil); err != nil {
		t.Fatal(err)
	}
	r := cp.last(t)
	if r.method != http.MethodPost || r.path != "/v1/messages" {
		t.Errorf("%s %s", r.method, r.path)
	}
	h := r.header
	if h.Get("X-Api-Key") != testKey || h.Get("Authorization") != "" {
		t.Errorf("auth headers: %v", h)
	}
	if h.Get("Anthropic-Version") != "2023-06-01" || h.Get("Content-Type") != "application/json" || h.Get("Accept") != "text/event-stream" {
		t.Errorf("headers: %v", h)
	}
	if h.Get("Anthropic-Beta") != "" {
		t.Errorf("no beta needed: %q", h.Get("Anthropic-Beta"))
	}
	want, err := anthropic.Build(p, anthropic.Options{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.body, want) {
		t.Errorf("the wire body is exactly Build's:\n%s\n%s", r.body, want)
	}
	if !strings.Contains(c.String(), "anthropic(") || strings.Contains(c.String(), testKey) {
		t.Errorf("String() = %q", c.String())
	}
}

func TestClientAuthStyles(t *testing.T) {
	cases := []struct {
		name, style, key  string
		wantKey, wantAuth string
	}{
		{"default", "", testKey, testKey, ""},
		{"x-api-key", "x-api-key", testKey, testKey, ""},
		{"bearer", "bearer", testKey, "", "Bearer " + testKey},
		{"bearer is case-insensitive", "Bearer", testKey, "", "Bearer " + testKey},
		{"no key", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newCapture(t, "text.sse")
			c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL, APIKey: tc.key, AuthStyle: tc.style})
			if _, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil); err != nil {
				t.Fatal(err)
			}
			h := cp.last(t).header
			if h.Get("X-Api-Key") != tc.wantKey || h.Get("Authorization") != tc.wantAuth {
				t.Errorf("x-api-key=%q authorization=%q", h.Get("X-Api-Key"), h.Get("Authorization"))
			}
		})
	}
}

func TestClientBetaHeaders(t *testing.T) {
	hotPrompt := func() *core.Prompt { return hotLoopPrompt() }
	cases := []struct {
		name string
		cfg  anthropic.Config
		req  provider.Request
		want string
	}{
		{"none", anthropic.Config{}, provider.Request{Prompt: hello("m")}, ""},
		{"configured", anthropic.Config{Betas: []string{"context-management-2025-06-27"}}, provider.Request{Prompt: hello("m")}, "context-management-2025-06-27"},
		{"per request", anthropic.Config{}, provider.Request{Prompt: hello("m"), Betas: []string{"extra-beta"}}, "extra-beta"},
		{"required by the body", anthropic.Config{}, provider.Request{Prompt: hotPrompt()}, anthropic.BetaTurnScopedSystem},
		{"binding", anthropic.Config{}, provider.Request{Prompt: hello("claude-opus-5-5"), BindingMode: "drop_block"}, anthropic.BetaThinkingBinding},
		{"all merged, deduplicated, in order",
			anthropic.Config{Betas: []string{"a-beta", " b-beta "}, Headers: map[string]string{"Anthropic-Beta": "c-beta, a-beta"}},
			provider.Request{Prompt: hotPrompt(), Betas: []string{"b-beta", "d-beta"}},
			"a-beta,b-beta,d-beta," + anthropic.BetaTurnScopedSystem + ",c-beta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := newCapture(t, "text.sse")
			tc.cfg.BaseURL = cp.ts.URL
			req := tc.req
			if req.Prompt != nil && req.Prompt.Model == "claude-opus-5-5" && req.BindingMode != "" {
				req.Prompt.Params.Thinking = "adaptive"
			}
			if _, err := anthropic.New(tc.cfg).Do(context.Background(), &req, nil); err != nil {
				t.Fatal(err)
			}
			if got := cp.last(t).header.Get("Anthropic-Beta"); got != tc.want {
				t.Errorf("anthropic-beta = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("a custom header never replaces the computed betas", func(t *testing.T) {
		cp := newCapture(t, "text.sse")
		c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL, Headers: map[string]string{"anthropic-beta": "only-this"}})
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: hotLoopPrompt()}, nil); err != nil {
			t.Fatal(err)
		}
		got := cp.last(t).header.Get("Anthropic-Beta")
		if !strings.Contains(got, anthropic.BetaTurnScopedSystem) || !strings.Contains(got, "only-this") {
			t.Errorf("anthropic-beta = %q", got)
		}
	})
	t.Run("other custom headers and the session key", func(t *testing.T) {
		cp := newCapture(t, "text.sse")
		c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL, Headers: map[string]string{"X-Team": "sleipnir"}, SessionHeader: "X-Session-Id"})
		p := hello("m")
		p.CacheKey = "sl:session:shard-3"
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil); err != nil {
			t.Fatal(err)
		}
		h := cp.last(t).header
		if h.Get("X-Team") != "sleipnir" || h.Get("X-Session-Id") != "sl:session:shard-3" {
			t.Errorf("headers = %v", h)
		}
		// Without a key nothing is sent, and by default nothing is sent at all.
		p.CacheKey = ""
		c.Do(context.Background(), &provider.Request{Prompt: p}, nil)
		if cp.last(t).header.Get("X-Session-Id") != "" {
			t.Error("empty cache key must not produce a header")
		}
		c2 := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL})
		p.CacheKey = "k"
		c2.Do(context.Background(), &provider.Request{Prompt: p}, nil)
		if cp.last(t).header.Get("X-Session-Id") != "" {
			t.Error("the session header is opt-in")
		}
	})
}

// roundTripFunc lets a test see the URL a client would call without a network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientBaseURLForms(t *testing.T) {
	data, _ := readFixture("text.sse")
	for _, tc := range []struct{ base, want string }{
		{"", "https://api.anthropic.com/v1/messages"},
		{"https://gw.example.com", "https://gw.example.com/v1/messages"},
		{"https://gw.example.com/", "https://gw.example.com/v1/messages"},
		{"https://gw.example.com/v1", "https://gw.example.com/v1/messages"},
		{"https://gw.example.com/anthropic/v1/", "https://gw.example.com/anthropic/v1/messages"},
		{"https://gw.example.com/prefix", "https://gw.example.com/prefix/v1/messages"},
	} {
		var got string
		hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r.URL.String()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})}
		c := anthropic.New(anthropic.Config{BaseURL: tc.base, HTTPClient: hc})
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("base %q -> %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestClientProfile(t *testing.T) {
	c := anthropic.New(anthropic.Config{Name: "anth", BaseURL: "https://example.test", Model: "claude-opus-5-5"})
	p := c.Profile()
	if p.Name != "anth" || p.Dialect != "anthropic" || p.BaseURL != "https://example.test" {
		t.Errorf("identity: %+v", p)
	}
	cm := p.Cache
	if !cm.Explicit || cm.Auto || cm.MaxBreakpoints != 4 || cm.LookbackBlocks != 20 || cm.MinPrefixTokens != 512 ||
		len(cm.TTLs) != 2 || cm.TTLs[0] != 5*time.Minute || cm.TTLs[1] != time.Hour || !cm.ReadRefreshesTTL || cm.ReadableAfter != cost.ReadableAtFirstByte || cm.KeyRouting {
		t.Errorf("cache model: %+v", cm)
	}
	if !p.ReplayThinking || !p.TurnScopedSystem || !p.BindingControls || !p.PrewarmZeroTokens || !p.StreamUsage || p.CaptureTokens || p.ChatCacheControl {
		t.Errorf("capabilities: %+v", p)
	}
	caps := p.KVCaps()
	if caps.Dialect != "anthropic" || caps.MaxBreakpoints != 4 || caps.LookbackBlocks != 20 || caps.MinPrefixTokens != 512 || !caps.ReplayThinking || caps.CacheKeys {
		t.Errorf("caps: %+v", caps)
	}
	// kv picks the hot-tail mechanism from this flag: with it the board view is a
	// turn-scoped system message, without it a preserved-thinking route must persist it.
	if !caps.TurnScopedSystem {
		t.Errorf("caps must advertise turn-scoped system messages: %+v", caps)
	}
	if got := kv.ResolveHot(kv.HotInline, caps, true); got != kv.HotTurnScoped {
		t.Errorf("hot mode on this route = %v, want turn-scoped", got)
	}
	for model, want := range map[string]int{"claude-haiku-4-5": 4096, "claude-opus-4-6": 4096, "claude-opus-4-7": 2048, "claude-sonnet-5": 1024, "claude-fable-5-1": 512, "": 1024, "who-knows": 1024} {
		got := anthropic.New(anthropic.Config{Model: model}).Profile().Cache.MinPrefixTokens
		if got != want {
			t.Errorf("min prefix for %q = %d, want %d", model, got, want)
		}
	}
	// Turn-scoped system messages exist only on some families; on the others the
	// profile says so, and kv delivers the board view another way.
	for model, want := range map[string]bool{
		"claude-opus-5-5": true, "claude-fable-5-1": true, "claude-sonnet-5-5": true, "claude-opus-5": true, "claude-opus-4-8": true,
		"anthropic/claude-sonnet-5-5": true, "claude-sonnet-5": false, "claude-opus-4-7": false, "claude-sonnet-4-6": false, "claude-haiku-4-5": false,
		"": true, "who-knows": true,
	} {
		if got := anthropic.New(anthropic.Config{Model: model}).Profile().TurnScopedSystem; got != want {
			t.Errorf("TurnScopedSystem for %q = %v, want %v", model, got, want)
		}
	}
	if kv.ResolveHot(kv.HotInline, anthropic.New(anthropic.Config{Model: "claude-sonnet-5"}).Profile().KVCaps(), false) != kv.HotInline {
		t.Error("a model without mid-conversation system messages gets the inline board view")
	}
	// A custom model resolver reaches the profile too.
	custom := anthropic.Config{Model: "claude-sonnet-5", Options: anthropic.Options{Models: func(string) anthropic.ModelInfo { return anthropic.ModelInfo{Known: true, MidSystem: true} }}}
	if !anthropic.New(custom).Profile().TurnScopedSystem {
		t.Error("Options.Models must decide the default profile")
	}
	// A profile override wins, and SetProfile swaps it.
	o := anthropic.DefaultProfile("gw", "https://gw", "")
	o.ReplayThinking = false
	if got := anthropic.New(anthropic.Config{Profile: &o}).Profile(); got.ReplayThinking || got.Name != "gw" {
		t.Errorf("override: %+v", got)
	}
	c.SetProfile(o)
	if c.Profile().Name != "gw" {
		t.Error("SetProfile did not take")
	}
}

func TestClientProfileFlagsShapeTheRequest(t *testing.T) {
	send := func(t *testing.T, prof func(*provider.Profile), req *provider.Request) (captured, []anthropic.Warning) {
		t.Helper()
		cp := newCapture(t, "text.sse")
		p := anthropic.DefaultProfile("x", cp.ts.URL, "")
		if prof != nil {
			prof(&p)
		}
		var ws []anthropic.Warning
		c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL, Profile: &p, OnWarnings: func(_ *provider.Request, w []anthropic.Warning) { ws = append(ws, w...) }})
		if _, err := c.Do(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
		return cp.last(t), ws
	}
	t.Run("no turn-scoped system messages: folded, no beta", func(t *testing.T) {
		r, _ := send(t, func(p *provider.Profile) { p.TurnScopedSystem = false }, &provider.Request{Prompt: hotLoopPrompt()})
		if strings.Contains(string(r.body), "clear_at") || r.header.Get("Anthropic-Beta") != "" || strings.Contains(string(r.body), `"role":"system"`) {
			t.Errorf("header %q body %s", r.header.Get("Anthropic-Beta"), r.body)
		}
	})
	t.Run("no thinking replay", func(t *testing.T) {
		r, ws := send(t, func(p *provider.Profile) { p.ReplayThinking = false }, &provider.Request{Prompt: hotLoopPrompt()})
		if strings.Contains(string(r.body), "signature") {
			t.Errorf("thinking must be dropped: %s", r.body)
		}
		found := false
		for _, w := range ws {
			found = found || w.Code == "thinking_dropped"
		}
		if !found {
			t.Errorf("warnings = %v", ws)
		}
	})
	t.Run("no zero-token warm-up", func(t *testing.T) {
		r, _ := send(t, func(p *provider.Profile) { p.PrewarmZeroTokens = false }, &provider.Request{Prompt: hello("m"), Warm: true})
		if !strings.Contains(string(r.body), `"max_tokens":1`) || strings.Contains(string(r.body), `"stream"`) {
			t.Errorf("%s", r.body)
		}
	})
	t.Run("no binding controls: the request runs without them", func(t *testing.T) {
		p := hello("claude-opus-5-5")
		p.Params.Thinking = "adaptive"
		r, ws := send(t, func(p *provider.Profile) { p.BindingControls = false }, &provider.Request{Prompt: p, BindingMode: "drop_block"})
		if strings.Contains(string(r.body), "block_binding") || r.header.Get("Anthropic-Beta") != "" {
			t.Errorf("header %q body %s", r.header.Get("Anthropic-Beta"), r.body)
		}
		if len(ws) == 0 || ws[0].Code != "binding_unsupported" {
			t.Errorf("warnings = %v", ws)
		}
	})
}

func TestClientWarm(t *testing.T) {
	cp := newCapture(t, "warm_empty.json")
	c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL})
	rec := &recorder{}
	p := hello("claude-opus-5-5")
	p.Params.MaxTokens = 4096
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: p, Warm: true}, rec.on)
	if err != nil {
		t.Fatal(err)
	}
	r := cp.last(t)
	if !strings.Contains(string(r.body), `"max_tokens":0`) || strings.Contains(string(r.body), `"stream"`) || r.header.Get("Accept") != "application/json" {
		t.Errorf("warm request: %s %v", r.body, r.header)
	}
	if resp.Usage.CacheWrite1hTokens != 2048 || rec.count(provider.EvStart) != 1 {
		t.Errorf("%+v %v", resp.Usage, rec.kinds())
	}
	// NoStream is a regular non-streaming call (keep-alives).
	if _, err := c.Do(context.Background(), &provider.Request{Prompt: p, NoStream: true}, nil); err != nil {
		t.Fatal(err)
	}
	r = cp.last(t)
	if !strings.Contains(string(r.body), `"max_tokens":4096`) || strings.Contains(string(r.body), `"stream"`) {
		t.Errorf("%s", r.body)
	}
}

func TestClientHooks(t *testing.T) {
	t.Run("OnHeaders sees successes and failures", func(t *testing.T) {
		var mu sync.Mutex
		var seen []http.Header
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Anthropic-Ratelimit-Requests-Remaining", "41")
			if r.Header.Get("X-Fail") != "" {
				w.WriteHeader(500)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			data, _ := readFixture("text.sse")
			w.Write(data)
		}))
		defer ts.Close()
		c := anthropic.New(anthropic.Config{BaseURL: ts.URL, OnHeaders: func(h http.Header) { mu.Lock(); seen = append(seen, h); mu.Unlock() }})
		c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		c2 := anthropic.New(anthropic.Config{BaseURL: ts.URL, Headers: map[string]string{"X-Fail": "1"}, OnHeaders: func(h http.Header) { mu.Lock(); seen = append(seen, h); mu.Unlock() }})
		c2.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
		if len(seen) != 2 || seen[0].Get("Anthropic-Ratelimit-Requests-Remaining") != "41" || seen[1].Get("Anthropic-Ratelimit-Requests-Remaining") != "41" {
			t.Errorf("seen %v", seen)
		}
	})
	t.Run("OnWarnings reports what Build changed", func(t *testing.T) {
		cp := newCapture(t, "text.sse")
		var got []anthropic.Warning
		var label string
		c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL, OnWarnings: func(r *provider.Request, w []anthropic.Warning) { label = r.Label; got = w }})
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: hotLoopPrompt(), Label: "be-1/turn 3"}, nil); err != nil {
			t.Fatal(err)
		}
		if label != "be-1/turn 3" || len(got) == 0 || got[0].Code != "marker_moved" || !strings.Contains(got[0].String(), "marker_moved:") {
			t.Errorf("%q %v", label, got)
		}
	})
}

func TestClientRejectsBadInput(t *testing.T) {
	cp := newCapture(t, "text.sse")
	c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL})
	for name, req := range map[string]*provider.Request{
		"nil request":   nil,
		"nil prompt":    {},
		"no model":      {Prompt: &core.Prompt{Messages: []core.Message{user(core.Text("x"))}}},
		"no messages":   {Prompt: &core.Prompt{Model: "m"}},
		"forced choice": {Prompt: &core.Prompt{Model: "m", Messages: []core.Message{user(core.Text("x"))}, Params: core.Params{ToolChoice: "any"}}},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := c.Do(context.Background(), req, nil)
			if resp != nil {
				t.Fatalf("resp = %+v", resp)
			}
			wantKind(t, err, provider.ErrBadRequest, false)
		})
	}
	if cp.count() != 0 {
		t.Errorf("%d requests reached the server; invalid prompts must be caught before sending", cp.count())
	}
}

func TestClientErrorsNeverEchoTheKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer ts.Close()
	c := anthropic.New(anthropic.Config{BaseURL: ts.URL, APIKey: testKey})
	_, err := c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
	if err == nil || strings.Contains(err.Error(), testKey) || strings.Contains(c.String(), testKey) {
		t.Fatalf("%v", err)
	}
	// A dead server: the transport error must not include headers either.
	ts.Close()
	_, err = c.Do(context.Background(), &provider.Request{Prompt: hello("m")}, nil)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("%v", err)
	}
}

func TestClientIsSafeForConcurrentUse(t *testing.T) {
	cp := newCapture(t, "thinking_signature.sse")
	c := anthropic.New(anthropic.Config{BaseURL: cp.ts.URL, APIKey: testKey})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%6 == 0 {
				p := c.Profile()
				p.Name = "swapped"
				c.SetProfile(p)
			}
			resp, err := c.Do(context.Background(), &provider.Request{Prompt: hotLoopPrompt()}, func(provider.Event) {})
			if err != nil || resp.Turn.PlainText() == "" {
				t.Errorf("%v", err)
			}
		}(i)
	}
	wg.Wait()
	if cp.count() != 24 {
		t.Errorf("requests = %d", cp.count())
	}
}

func TestRequestBodyIsValidJSONForEveryFixturePrompt(t *testing.T) {
	// A cheap guard: the shared prompts used across the suite all produce valid bodies.
	for name, p := range map[string]*core.Prompt{"layered": layeredPrompt(), "loop": toolLoopPrompt(true), "hot": hotLoopPrompt()} {
		for _, stream := range []bool{true, false} {
			b, err := anthropic.Build(p, anthropic.Options{}, stream)
			if err != nil || !json.Valid(b) {
				t.Errorf("%s stream=%v: %v", name, stream, err)
			}
		}
	}
}
