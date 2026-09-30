package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/mcp/mcptest"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// The requests an MCP tool builds must mean something to the real permission
// engine: names match rules, the network flag makes a remote call a question,
// plan mode refuses.
func TestAdapterRequestsWorkWithTheRealPermissionEngine(t *testing.T) {
	s := mcptest.New()
	ts, _ := httpServer(t, s, mcptest.HTTPOptions{JSONOnly: true})
	other := mcptest.New()
	ts2, _ := httpServer(t, other, mcptest.HTTPOptions{JSONOnly: true})
	m := startManager(t, quickOpts(map[string]ServerConfig{"srv": httpCfg(ts.URL), "other": httpCfg(ts2.URL)}))
	tls := m.Tools()
	root := t.TempDir()

	run := func(t *testing.T, cfg perm.Config, tool string) *tools.Result {
		t.Helper()
		cfg.Root, cfg.Home = root, t.TempDir()
		e, err := perm.NewEngine(cfg)
		if err != nil {
			t.Fatal(err)
		}
		env := (&tools.Env{Agent: "a1", Role: "worker", Perm: e}).Defaults()
		return runTool(t, findTool(t, tls, tool), env, `{"message":"hi"}`)
	}

	t.Run("default mode without a prompter refuses", func(t *testing.T) {
		res := run(t, perm.Config{}, "mcp__srv__echo")
		if !res.IsError || !strings.Contains(res.Text, "permission denied") || !strings.Contains(res.Text, "approval") {
			t.Errorf("%+v", res)
		}
	})
	t.Run("a rule naming the tool allows it", func(t *testing.T) {
		if res := run(t, perm.Config{Allow: []string{"mcp__srv__echo"}}, "mcp__srv__echo"); res.IsError || res.Text != "hi" {
			t.Errorf("%+v", res)
		}
	})
	t.Run("a server wildcard allows that server only", func(t *testing.T) {
		cfg := perm.Config{Allow: []string{"mcp__srv__*"}}
		if res := run(t, cfg, "mcp__srv__echo"); res.IsError {
			t.Errorf("own server: %+v", res)
		}
		if res := run(t, cfg, "mcp__other__echo"); !res.IsError {
			t.Errorf("another server's tool must still ask: %+v", res)
		}
	})
	t.Run("deny beats allow", func(t *testing.T) {
		res := run(t, perm.Config{Allow: []string{"mcp__srv__*"}, Deny: []string{"mcp__srv__echo"}}, "mcp__srv__echo")
		if !res.IsError || !strings.Contains(res.Text, "permission denied") {
			t.Errorf("%+v", res)
		}
	})
	t.Run("plan mode refuses", func(t *testing.T) {
		res := run(t, perm.Config{Mode: perm.ModePlan}, "mcp__srv__echo")
		if !res.IsError || !strings.Contains(res.Text, "permission denied") {
			t.Errorf("%+v", res)
		}
	})
	t.Run("a prompter can allow", func(t *testing.T) {
		var asked atomic.Int32
		var seen perm.Request
		res := run(t, perm.Config{Prompter: func(_ context.Context, r perm.Request) perm.Decision {
			asked.Add(1)
			seen = r
			return perm.Decision{Allow: true}
		}}, "mcp__srv__echo")
		if res.IsError || asked.Load() != 1 {
			t.Errorf("%+v asked=%d", res, asked.Load())
		}
		if seen.Tool != "mcp__srv__echo" || !seen.Network || seen.Summary == "" {
			t.Errorf("the prompter should get a descriptive request: %+v", seen)
		}
	})
}

func TestGoroutinesAreReleasedAfterClose(t *testing.T) {
	skipNotUnix(t)
	mcpGoroutines := func() []string {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		var out []string
		for _, g := range strings.Split(string(buf), "\n\n") {
			// Our own goroutines: anything inside the package that is not the test
			// running this check.
			if strings.Contains(g, "sleipnir/internal/mcp.") && !strings.Contains(g, "testing.tRunner") &&
				!strings.Contains(g, "mcp.TestGoroutines") {
				out = append(out, g)
			}
		}
		return out
	}

	before := len(mcpGoroutines())
	stream, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
	legacy := httptest.NewServer(mcptest.New().SSE(mcptest.SSEOptions{}))
	m := NewManager(quickOpts(map[string]ServerConfig{
		"stream": httpCfg(stream.URL), "legacy": sseCfg(legacy.URL + "/sse"), "local": helperCfg("", nil),
	}))
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	env, _ := testEnv(newRecorder(true))
	for _, tl := range m.Tools() {
		if strings.HasSuffix(tl.Spec().Name, "__echo") || strings.HasSuffix(tl.Spec().Name, "__progress") {
			runTool(t, tl, env, `{"message":"x"}`)
		}
	}
	// A cancelled call and a timed-out one, which start goroutines of their own.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	for _, tl := range m.Tools() {
		if strings.HasSuffix(tl.Spec().Name, "__stream__slow") || strings.HasSuffix(tl.Spec().Name, "__local__slow") {
			_, _ = tl.Run(ctx, &tools.Call{Input: json.RawMessage(`{"ms":5000}`), Env: env})
		}
	}
	m.Close()
	legacy.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		left := mcpGoroutines()
		if len(left) <= before {
			return
		}
		if time.Now().After(deadline) {
			all := make([]byte, 4<<20)
			all = all[:runtime.Stack(all, true)]
			t.Fatalf("%d goroutines of the package survive Close (%d before):\n%s\n=== ALL ===\n%s", len(left), before, strings.Join(left, "\n\n"), all)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDialHookReplacesTheBuiltInGuard(t *testing.T) {
	ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{JSONOnly: true, NoStream: true})
	var calls atomic.Int32
	var lastAddr atomic.Value
	hook := func(ctx context.Context, network, address string) (net.Conn, error) {
		lastAddr.Store(address) // before the count: a reader that sees a call sees its address
		calls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	cfg := httpCfg(ts.URL)
	ctxt, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The hook decides: here it allows loopback although AllowPrivate is not set on the
	// built-in guard (which would refuse it); plain http still needs the flag.
	cfg.AllowPrivate = false
	if _, err := Dial(ctxt, "web", cfg, DialOptions{Net: NetOptions{Dial: hook}}); err == nil || !strings.Contains(err.Error(), "plain http://") {
		t.Fatalf("cleartext http needs allow_private even with a hook: %v", err)
	}
	c, err := Dial(ctxt, "web", cfg, DialOptions{Net: NetOptions{Dial: hook, AllowPrivate: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	callText(t, c, "echo", `{"message":"x"}`)
	if calls.Load() == 0 || !strings.HasPrefix(lastAddr.Load().(string), "127.0.0.1:") {
		t.Errorf("the hook was not used: %d calls", calls.Load())
	}

	// And a hook that says no is final.
	deny := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("policy: no") }
	_, err = Dial(ctxt, "web", cfg, DialOptions{Net: NetOptions{Dial: deny, AllowPrivate: true}})
	if err == nil || !strings.Contains(err.Error(), "policy: no") {
		t.Errorf("err = %v", err)
	}
}

func TestProxyIsExplicitAndStillVetted(t *testing.T) {
	var viaProxy atomic.Int32
	target, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{JSONOnly: true, NoStream: true})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaProxy.Add(1)
		// Forward like a proxy would: the request line carries the absolute URL.
		out, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
		out.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	popts := NetOptions{Proxy: func(*http.Request) (*url.URL, error) { return proxyURL, nil }}
	ctxt, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Without allow_private the loopback target is refused before the proxy sees it.
	cfg := httpCfg(target.URL)
	cfg.AllowPrivate = false
	if _, err := Dial(ctxt, "web", cfg, DialOptions{Net: popts}); err == nil {
		t.Fatal("expected a refusal")
	}
	if viaProxy.Load() != 0 {
		t.Error("a request for a private target reached the proxy")
	}
	// With it, requests go through the proxy.
	popts.AllowPrivate = true
	c, err := Dial(ctxt, "web", cfg, DialOptions{Net: popts})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	callText(t, c, "echo", `{"message":"via proxy"}`)
	if viaProxy.Load() == 0 {
		t.Error("the configured proxy was not used")
	}
	// The process environment is never consulted.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	c2, err := Dial(ctxt, "web", httpCfg(target.URL), DialOptions{})
	if err != nil {
		t.Fatalf("HTTP_PROXY in the environment must be ignored: %v", err)
	}
	c2.Close()
}
