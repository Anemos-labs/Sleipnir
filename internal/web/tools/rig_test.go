package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// testHost is the Host of requests sent straight to the handler.
const testHost = "127.0.0.1:6969"

// fakeHost is a seam.Host with fixed tabs and projects that records the frames published.
type fakeHost struct {
	mu       sync.Mutex
	tabs     []wire.TabSummary
	projects []wire.Project
	frames   []wire.Frame
}

// fakeTab is a tab of the fake host: only its summary is real.
type fakeTab struct {
	seam.Tab
	sum wire.TabSummary
}

// Summary describes the tab.
func (t fakeTab) Summary() wire.TabSummary { return t.sum }

// Tabs lists the tabs.
func (h *fakeHost) Tabs() []wire.TabSummary {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wire.TabSummary{}, h.tabs...)
}

// Tab finds a tab.
func (h *fakeHost) Tab(id string) (seam.Tab, bool) {
	for _, t := range h.Tabs() {
		if t.ID == id {
			return fakeTab{sum: t}, true
		}
	}
	return nil, false
}

// Active is none.
func (h *fakeHost) Active() string { return "" }

// Projects lists the projects.
func (h *fakeHost) Projects(context.Context) []wire.Project {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wire.Project{}, h.projects...)
}

// Questions lists none.
func (h *fakeHost) Questions() []wire.OpenQuestion { return nil }

// Publish records a frame.
func (h *fakeHost) Publish(f wire.Frame) {
	h.mu.Lock()
	h.frames = append(h.frames, f)
	h.mu.Unlock()
}

// framesOf returns the frames of a type published so far.
func (h *fakeHost) framesOf(typ string) []wire.Frame {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []wire.Frame
	for _, f := range h.frames {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

// rig is a server with the tools routes over a state directory of its own.
type rig struct {
	t       *testing.T
	srv     *web.Server
	host    *fakeHost
	home    string
	state   string
	project string
	o       Options
	mu      sync.Mutex
	logs    strings.Builder
}

// newRig makes a server whose state directory is a temporary one and whose runs start the test binary as `sleipnir`.
func newRig(t *testing.T, mut func(*Options), env ...string) *rig {
	t.Helper()
	home := t.TempDir()
	rg := &rig{t: t, home: home, state: filepath.Join(home, "state"), project: filepath.Join(home, "proj"), host: &fakeHost{}}
	if err := os.MkdirAll(rg.project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEIPNIR_HOME", rg.state)
	t.Setenv("HOME", home)
	rg.host.projects = []wire.Project{{Dir: rg.project, Root: rg.project, Name: "proj"}}
	rg.o = Options{
		Home: home, Version: "0.1.0", Commit: "abc", Self: os.Args[0], Cwd: rg.project,
		Env: func() []string { return append(append(os.Environ(), "TOOLS_FAKE=1"), env...) },
	}
	if mut != nil {
		mut(&rg.o)
	}
	srv, err := web.New(web.Config{Addr: testHost, UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
		Logf: func(f string, a ...any) {
			rg.mu.Lock()
			defer rg.mu.Unlock()
			rg.logs.WriteString(fmt.Sprintf(f, a...) + "\n")
		},
		Routes: func(s *web.Server) { Register(s, rg.host, rg.o) }})
	if err != nil {
		t.Fatal(err)
	}
	rg.srv = srv
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		Shutdown(ctx, srv)
		srv.Hub().Close()
	})
	return rg
}

// log returns what the server logged.
func (rg *rig) log() string {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.logs.String()
}

// req is a request of a test: by default from the page (bearer token, the server's Origin, the custom header, JSON).
type req struct {
	method, path string
	body         any
	raw          string
	header       map[string]string
	noAuth       bool
}

// do sends a request to the server's handler.
func (rg *rig) do(q req) *httptest.ResponseRecorder {
	rg.t.Helper()
	var r *http.Request
	switch {
	case q.raw != "":
		r = httptest.NewRequest(q.method, q.path, strings.NewReader(q.raw))
		r.Header.Set("Content-Type", "application/json")
	case q.body != nil:
		b, _ := json.Marshal(q.body)
		r = httptest.NewRequest(q.method, q.path, strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
	default:
		r = httptest.NewRequest(q.method, q.path, nil)
	}
	r.Host = testHost
	if q.method != http.MethodGet {
		r.Header.Set("Origin", "http://"+testHost)
		r.Header.Set(web.RequestHeader, web.RequestHeaderValue)
	}
	if !q.noAuth {
		r.Header.Set("Authorization", "Bearer "+rg.srv.Token())
	}
	for k, v := range q.header {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	rg.srv.Handler().ServeHTTP(w, r)
	return w
}

// confirm obtains a confirmation id for a scope.
func (rg *rig) confirm(scope string) string {
	rg.t.Helper()
	w := rg.do(req{method: "POST", path: "/api/confirm", body: map[string]string{"scope": scope}})
	if w.Code != 200 {
		rg.t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var c wire.ConfirmID
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	return c.ID
}

// errCode is the code of an error response.
func errCode(w *httptest.ResponseRecorder) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	return e.Code
}

// decode decodes a response body.
func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body.String(), err)
	}
	return v
}

// waitRun waits for a run of the server's runner to end and returns its frames.
func (rg *rig) waitRun(id string) []wire.RunFrame {
	rg.t.Helper()
	runs := runner.For(rg.srv, nil, runner.Options{})
	done := runs.Done(id)
	if done == nil {
		rg.t.Fatalf("no run %s", id)
	}
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		rg.t.Fatalf("run %s did not end", id)
	}
	var out []wire.RunFrame
	for _, f := range rg.host.framesOf("run") {
		if rf, ok := f.Data.(wire.RunFrame); ok && rf.ID == id {
			out = append(out, rf)
		}
	}
	return out
}

// session writes a recorded session: a log with a first prompt and a cost, last written age ago.
func (rg *rig) session(id string, age time.Duration, first string) string {
	rg.t.Helper()
	dir := filepath.Join(rg.state, "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		rg.t.Fatal(err)
	}
	when := time.Now().Add(-age)
	var b strings.Builder
	for i, e := range []map[string]any{
		{"type": "session.start", "data": map[string]any{"model": "m-1", "cwd": rg.project, "root": rg.project}},
		{"type": "user.input", "agent": "main", "data": map[string]any{"text": first}},
		{"type": "agent.snapshot", "agent": "main", "data": map[string]any{}},
		{"type": "session.end", "data": map[string]any{"cost_usd": 0.25, "reason": "exit"}},
	} {
		e["seq"], e["ts"] = i+1, when.Add(time.Duration(i)*time.Second)
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	log := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(log, []byte(b.String()), 0o644); err != nil {
		rg.t.Fatal(err)
	}
	for _, p := range []string{log, dir} {
		if err := os.Chtimes(p, when, when); err != nil {
			rg.t.Fatal(err)
		}
	}
	return dir
}
