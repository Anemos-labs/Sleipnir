package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/anemos-labs/sleipnir/internal/catalog"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/mcp/mcptest"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/testutil"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The test binary also serves as an MCP server (mcptest), for the sessions that start one; otherwise it runs the tests and checks that
// no goroutine is left behind.
func TestMain(m *testing.M) {
	if mcptest.IsHelper() {
		mcptest.HelperMain()
		return
	}
	os.Exit(testutil.CheckLeaks(m))
}

// testHost is the Host header of the test server's requests.
const testHost = "127.0.0.1:6969"

// fakeHost is a seam.Host with a fixed set of tabs.
type fakeHost struct {
	mu       sync.Mutex
	tabs     map[string]*fakeTab
	order    []string
	active   string
	projects []wire.Project
}

// Tabs lists the tabs in order.
func (h *fakeHost) Tabs() []wire.TabSummary {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []wire.TabSummary
	for _, id := range h.order {
		out = append(out, h.tabs[id].Summary())
	}
	return out
}

// Tab finds a tab.
func (h *fakeHost) Tab(id string) (seam.Tab, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tabs[id]
	if !ok {
		return nil, false
	}
	return t, true
}

// Active is the first tab.
func (h *fakeHost) Active() string { return h.active }

// Projects lists the configured projects.
func (h *fakeHost) Projects(context.Context) []wire.Project { return h.projects }

// Questions lists none.
func (h *fakeHost) Questions() []wire.OpenQuestion { return nil }

// Publish drops frames.
func (h *fakeHost) Publish(wire.Frame) {}

// fakeTab is a seam.Tab of which the settings routes use Summary, Rules and Access; the other methods are not implemented (a call
// panics, which fails the test).
type fakeTab struct {
	seam.Tab
	id, cwd string
	sess    *session.Session
	rules   []wire.Rule
}

// Summary describes the tab.
func (t *fakeTab) Summary() wire.TabSummary { return wire.TabSummary{ID: t.id, Cwd: t.cwd, Name: t.id} }

// Rules returns the canned rules.
func (t *fakeTab) Rules(context.Context) ([]wire.Rule, error) { return t.rules, nil }

// Access gives the tab's session.
func (t *fakeTab) Access() seam.SessionAccess { return fakeAccess{t: t} }

// fakeAccess is the tab's seam.SessionAccess (only Session is used).
type fakeAccess struct {
	seam.SessionAccess
	t *fakeTab
}

// Session returns the tab's session.
func (a fakeAccess) Session() *session.Session { return a.t.sess }

// testEnv is a server with the settings routes on a fake host, a private home and state directory, and what the server logged.
type testEnv struct {
	t    *testing.T
	home string
	repo string
	srv  *web.Server
	svc  *service
	host *fakeHost
	tab  *fakeTab

	logMu sync.Mutex
	logs  []string
}

// newEnv builds the environment: a git repository as the tab's project, HOME and SLEIPNIR_HOME in temporary directories, no
// provider key from the machine. setup may write files before the server starts.
func newEnv(t *testing.T, setup func(home, repo string)) *testEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLEIPNIR_HOME", "")
	t.Setenv("SLEIPNIR_MODEL", "")
	cfg, _, _ := config.Load(config.LoadOpts{Home: home, UntrustedProject: true, Environ: func() []string { return nil }})
	for _, kp := range catalog.KeyProviders(cfg) {
		t.Setenv(kp.Env, "")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	t.Chdir(repo)
	if setup != nil {
		setup(home, repo)
	}
	e := &testEnv{t: t, home: home, repo: repo}
	e.host = &fakeHost{tabs: map[string]*fakeTab{}, active: "t1", projects: []wire.Project{{Dir: repo}}}
	e.tab = &fakeTab{id: "t1", cwd: repo}
	e.host.tabs["t1"], e.host.order = e.tab, []string{"t1"}
	srv, err := web.New(web.Config{Addr: testHost, UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, Logf: e.logf})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	e.svc = newService(srv, e.host, Options{Home: home, Version: "test"})
	e.svc.fetch = func(context.Context, catalog.Options) catalog.Result { return catalog.Result{} }
	e.svc.register()
	return e
}

// logf records a server log line.
func (e *testEnv) logf(format string, args ...any) {
	e.logMu.Lock()
	e.logs = append(e.logs, fmt.Sprintf(format, args...))
	e.logMu.Unlock()
}

// logText is everything the server logged.
func (e *testEnv) logText() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return strings.Join(e.logs, "\n")
}

// request builds an authenticated request as the page sends it; hdr overrides headers (an empty value removes one).
func (e *testEnv) request(method, path string, body any, hdr map[string]string) *http.Request {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "http://"+testHost+path, rd)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+e.srv.Token())
	if method != http.MethodGet {
		req.Header.Set(web.RequestHeader, web.RequestHeaderValue)
		if rd != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	return req
}

// do sends a request and returns the recorder.
func (e *testEnv) do(method, path string, body any, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, e.request(method, path, body, hdr))
	return rec
}

// ok sends a request that must answer 200 and decodes the answer into v (nil: no decoding).
func (e *testEnv) ok(method, path string, body any, hdr map[string]string, v any) []byte {
	e.t.Helper()
	rec := e.do(method, path, body, hdr)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
	}
	if v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, rec.Body.String())
		}
	}
	return rec.Body.Bytes()
}

// confirm obtains a confirmation id for scope.
func (e *testEnv) confirm(scope string) string {
	e.t.Helper()
	var c struct{ ID string }
	e.ok(http.MethodPost, "/api/confirm", map[string]string{"scope": scope}, nil, &c)
	if c.ID == "" {
		e.t.Fatal("no confirmation id")
	}
	return c.ID
}

// errCode decodes the code of an error answer.
func errCode(rec *httptest.ResponseRecorder) string {
	var b struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b.Code
}

// write writes a file, making its directory.
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// refServer is an MCP entry that runs the reference server as a child of this test binary.
func refServer(extra string) map[string]any {
	args := []string{"-test.run=^$"}
	if extra != "" {
		args = append(args, "-test.v="+extra)
	}
	return map[string]any{"command": os.Args[0], "args": args, "env": map[string]string{mcptest.EnvHelper: "1", "GORACE": "atexit_sleep_ms=0"}}
}

// writeJSON writes v as JSON to path.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(b))
}

// noModel is the provider of a session that never asks a model.
type noModel struct{}

// Profile names the stand-in provider.
func (noModel) Profile() provider.Profile {
	return provider.Profile{Name: "none", Dialect: "openai-chat"}
}

// Do refuses: the session never sends a request.
func (noModel) Do(context.Context, *provider.Request, func(provider.Event)) (*provider.Response, error) {
	return nil, errors.New("no model in this session")
}

// startSession starts a real harness session in the tab's project and gives it to the tab.
func (e *testEnv) startSession(o session.Options) *session.Session {
	e.t.Helper()
	m := cost.Model{ID: "stand-in", ContextTokens: 100_000, MaxOutput: 4096}
	o.Cwd, o.Root, o.Home, o.Dir = e.repo, e.repo, e.home, e.t.TempDir()
	o.Provider, o.ModelInfo, o.Model = noModel{}, &m, m.ID
	o.Offline, o.NoRecon, o.NoWeb = true, true, true
	if o.Mode == "" {
		o.Mode = perm.ModeDefault
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { s.Close() })
	e.tab.sess = s
	return s
}
