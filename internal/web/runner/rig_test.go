package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// testHost is the Host of requests sent straight to the handler.
const testHost = "127.0.0.1:6969"

// fakeHost is a seam.Host with fixed tabs that records the frames published.
type fakeHost struct {
	mu     sync.Mutex
	tabs   []wire.TabSummary
	frames []wire.Frame
	notify chan struct{}
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

// Active is the first tab.
func (h *fakeHost) Active() string { return "" }

// Projects lists none.
func (h *fakeHost) Projects(context.Context) []wire.Project { return nil }

// Questions lists none.
func (h *fakeHost) Questions() []wire.OpenQuestion { return nil }

// Publish records a frame.
func (h *fakeHost) Publish(f wire.Frame) {
	h.mu.Lock()
	h.frames = append(h.frames, f)
	h.mu.Unlock()
	select {
	case h.notify <- struct{}{}:
	default:
	}
}

// runFrames returns the run frames of a run published so far, and whether one of them had the critical flag.
func (h *fakeHost) runFrames(id string) (out []wire.RunFrame, critical bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range h.frames {
		if rf, ok := f.Data.(wire.RunFrame); ok && f.Type == "run" && rf.ID == id {
			out = append(out, rf)
			critical = critical || f.Critical
		}
	}
	return out, critical
}

// rig is a server with the runner's routes.
type rig struct {
	t    *testing.T
	srv  *web.Server
	host *fakeHost
	runs *Runs
	logs *bytes.Buffer
	mu   sync.Mutex
}

// newRig makes a server whose runner starts the test binary as `sleipnir` (RUNNER_FAKE=1).
func newRig(t *testing.T, o Options, extraEnv ...string) *rig {
	t.Helper()
	rg := &rig{t: t, host: &fakeHost{notify: make(chan struct{}, 1), tabs: []wire.TabSummary{{ID: "shop", SID: "20261009-221530-a91c3e", Cwd: t.TempDir()}}}, logs: &bytes.Buffer{}}
	if o.Self == "" {
		o.Self = os.Args[0]
	}
	if o.Env == nil {
		o.Env = func() []string { return append(append(os.Environ(), "RUNNER_FAKE=1"), extraEnv...) }
	}
	if o.Cwd == "" {
		o.Cwd = t.TempDir()
	}
	srv, err := web.New(web.Config{Addr: testHost, UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
		Logf: func(f string, a ...any) {
			rg.mu.Lock()
			defer rg.mu.Unlock()
			rg.logs.WriteString(strings.TrimSpace(fmt.Sprintf(f, a...)) + "\n")
		},
		Routes: func(s *web.Server) { Register(s, rg.host, o) }})
	if err != nil {
		t.Fatal(err)
	}
	rg.srv = srv
	rg.runs = For(srv, rg.host, o)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		Shutdown(ctx, srv)
		srv.Hub().Close()
	})
	return rg
}

// log returns what the server logged.
func (rg *rig) log() string { rg.mu.Lock(); defer rg.mu.Unlock(); return rg.logs.String() }

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
	var body *strings.Reader
	switch {
	case q.raw != "":
		body = strings.NewReader(q.raw)
	case q.body != nil:
		b, _ := json.Marshal(q.body)
		body = strings.NewReader(string(b))
	}
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(q.method, q.path, body)
		r.Header.Set("Content-Type", "application/json")
	} else {
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

// start starts a run and returns its id.
func (rg *rig) start(body any, hdr map[string]string) string {
	rg.t.Helper()
	w := rg.do(req{method: "POST", path: "/api/runs", body: body, header: hdr})
	if w.Code != http.StatusAccepted {
		rg.t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var s wire.RunStarted
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	return s.ID
}

// wait waits for a run to end and returns its result frame.
func (rg *rig) wait(id string, within time.Duration) wire.RunFrame {
	rg.t.Helper()
	done := rg.runs.Done(id)
	if done == nil {
		rg.t.Fatalf("no run %s", id)
	}
	select {
	case <-done:
	case <-time.After(within):
		rg.t.Fatalf("run %s did not end within %s", id, within)
	}
	frames, _ := rg.host.runFrames(id)
	for _, f := range frames {
		if f.Result != nil {
			return f
		}
	}
	rg.t.Fatalf("run %s ended without a result frame", id)
	return wire.RunFrame{}
}

// lines are the output lines of a run's frames.
func (rg *rig) lines(id string) []wire.RunLine {
	frames, _ := rg.host.runFrames(id)
	var out []wire.RunLine
	for _, f := range frames {
		out = append(out, f.Lines...)
	}
	return out
}

// waitLine waits for a line of a run that contains s and returns it.
func (rg *rig) waitLine(id, s string, within time.Duration) string {
	rg.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, l := range rg.lines(id) {
			if strings.Contains(l.T, s) {
				return l.T
			}
		}
		select {
		case <-rg.host.notify:
		case <-time.After(20 * time.Millisecond):
		}
	}
	rg.t.Fatalf("no line with %q in run %s: %+v", s, id, rg.lines(id))
	return ""
}
