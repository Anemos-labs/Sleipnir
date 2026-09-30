package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/mcp/mcptest"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// TestMain turns the test binary into an MCP server when a stdio test starts it
// as a child: the binary re-executes itself, so stdio tests exercise real
// processes, pipes, signals and process groups without a fixture binary.
func TestMain(m *testing.M) {
	if mcptest.IsHelper() {
		mcptest.HelperMain()
		return
	}
	os.Exit(m.Run())
}

// helperCfg is a trusted stdio entry that runs the reference server in the given
// mode as a child process of this test binary.
func helperCfg(mode string, env map[string]string) ServerConfig {
	// GORACE: a -race binary sleeps a second at exit by default; the helper
	// children need not.
	e := map[string]string{mcptest.EnvHelper: "1", mcptest.EnvMode: mode, "GORACE": "atexit_sleep_ms=0"}
	for k, v := range env {
		e[k] = v
	}
	return ServerConfig{Type: TypeStdio, Command: os.Args[0], Args: []string{"-test.run=^$"}, Env: e, Trust: true, Scope: ScopeUser}
}

// quickOpts are manager options tuned for tests: short timeouts and backoff, a
// base environment that still lets the test binary run.
func quickOpts(servers map[string]ServerConfig) Options {
	return Options{
		Servers:         servers,
		BaseEnv:         SafeBaseEnv(os.Environ()),
		ConnectTimeout:  10 * time.Second,
		CallTimeout:     10 * time.Second,
		ShutdownGrace:   500 * time.Millisecond,
		Backoff:         Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond, MaxFailures: 4},
		RefreshInterval: 10 * time.Millisecond,
		StableAfter:     time.Hour,
		ReconnectWait:   3 * time.Second,
	}
}

// httpServer serves a reference server over streamable HTTP and returns its URL.
func httpServer(t *testing.T, s *mcptest.Server, o mcptest.HTTPOptions) (*httptest.Server, *mcptest.HTTP) {
	t.Helper()
	h := s.HTTP(o)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, h
}

func httpCfg(url string) ServerConfig {
	return ServerConfig{Type: TypeHTTP, URL: url, Trust: true, Scope: ScopeUser, AllowPrivate: true}
}

// permFunc adapts a function to perm.Requester.
type permFunc func(perm.Request) perm.Decision

func (f permFunc) Check(_ context.Context, r perm.Request) perm.Decision { return f(r) }

// recorder is a perm.Requester that records what it was asked and allows or
// denies as told.
type recorder struct {
	mu    sync.Mutex
	reqs  []perm.Request
	allow bool
	why   string
}

func newRecorder(allow bool) *recorder { return &recorder{allow: allow, why: "test policy"} }

func (r *recorder) Check(_ context.Context, req perm.Request) perm.Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return perm.Decision{Allow: r.allow, Reason: r.why}
}

func (r *recorder) requests() []perm.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]perm.Request(nil), r.reqs...)
}

// memBlobs is an in-memory content-addressed store with the method set of the
// harness blob store, so the tests need nothing from the events package.
type memBlobs struct {
	mu sync.Mutex
	m  map[core.Hash][]byte
}

func (b *memBlobs) Put(data []byte) (core.Hash, error) {
	h := core.HashBytes(data)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[core.Hash][]byte{}
	}
	b.m[h] = append([]byte(nil), data...)
	return h, nil
}

func (b *memBlobs) Get(h core.Hash) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.m[h]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return append([]byte(nil), data...), nil
}

func (b *memBlobs) Has(h core.Hash) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.m[h]
	return ok
}

func (b *memBlobs) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m)
}

// testEnv is a tools.Env with a blob store and the given permission checker.
func testEnv(p perm.Requester) (*tools.Env, *memBlobs) {
	blobs := &memBlobs{}
	env := (&tools.Env{Agent: "a1", Role: "worker", Perm: p, Blobs: blobs}).Defaults()
	return env, blobs
}

func mustJSON(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func raw(m map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range m {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}

func startManager(t *testing.T, o Options) *Manager {
	t.Helper()
	m := NewManager(o)
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return m
}

func runTool(t *testing.T, tl tools.Tool, env *tools.Env, input string) *tools.Result {
	t.Helper()
	res, err := tl.Run(context.Background(), &tools.Call{ID: "c1", Name: tl.Spec().Name, Input: json.RawMessage(input), Env: env})
	if err != nil {
		t.Fatalf("Run returned a Go error (must be model-visible instead): %v", err)
	}
	if res == nil {
		t.Fatal("Run returned nil result")
	}
	return res
}

func findTool(t *testing.T, ts []tools.Tool, suffix string) tools.Tool {
	t.Helper()
	for _, tl := range ts {
		if strings.HasSuffix(tl.Spec().Name, suffix) {
			return tl
		}
	}
	t.Fatalf("no tool with suffix %q in %v", suffix, toolNames(ts))
	return nil
}

func toolNames(ts []tools.Tool) []string {
	var out []string
	for _, tl := range ts {
		out = append(out, tl.Spec().Name)
	}
	return out
}

var _ = http.StatusOK

func newRef() *mcptest.Server { return mcptest.New() }

func mcpHTTPOpts() mcptest.HTTPOptions { return mcptest.HTTPOptions{} }
