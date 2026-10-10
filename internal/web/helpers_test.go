package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// testHost is the Host header of requests sent straight to the handler: the address the test servers are configured with.
const testHost = "127.0.0.1:6969"

// testOrigin is the Origin a page served by the test servers sends.
const testOrigin = "http://" + testHost

// testUI is the UI of the test servers: the contents do not matter to the envelope, only the shape of the files.
var testUI = fstest.MapFS{
	"index.html":               {Data: []byte("<!doctype html><title>t</title><script src=\"/js/app.js\"></script>")},
	"js/app.js":                {Data: []byte("console.log(1)")},
	"app.css":                  {Data: []byte("body{}")},
	"fonts/f.woff2":            {Data: []byte("wOF2....")},
	"img/a.png":                {Data: []byte("\x89PNG")},
	"favicon.ico":              {Data: []byte("\x00\x00\x01\x00")},
	"data.json":                {Data: []byte(`{"a":1}`)},
	"icon.svg":                 {Data: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>")},
	".hidden":                  {Data: []byte("secret")},
	".git/config":              {Data: []byte("[core]")},
	"js/.env":                  {Data: []byte("KEY=secret")},
	"unknown.bin":              {Data: []byte("bin")},
	"notes.md":                 {Data: []byte("# notes")},
	"nested/deeper/page.txt":   {Data: []byte("text")},
	"fonts/OFL-barlow.txt":     {Data: []byte("licence")},
	"nested/deeper/module.mjs": {Data: []byte("export {}")},
}

// clock is a controllable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

// Now returns the current fake time.
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the fake time forward.
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// logs collects the lines a server logs.
type logs struct {
	mu    sync.Mutex
	lines []string
}

// add records a line.
func (l *logs) add(f string, a ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}

// text returns everything logged so far.
func (l *logs) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// rig is a server under test with its clock and log.
type rig struct {
	t     testing.TB
	srv   *Server
	h     http.Handler
	clock *clock
	log   *logs
	// block is closed by tests that hold requests in /api/slow
	entered chan struct{}
	release chan struct{}
}

// newRig creates a server with a fake clock, a collecting log, the test UI and the test routes.
func newRig(t testing.TB, mut func(*Config)) *rig {
	t.Helper()
	rg := &rig{t: t, clock: &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}, log: &logs{},
		entered: make(chan struct{}, 64), release: make(chan struct{})}
	cfg := Config{Addr: testHost, UI: testUI, Logf: rg.log.add, Now: rg.clock.Now, Routes: rg.routes}
	if mut != nil {
		mut(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rg.srv, rg.h = srv, srv.Handler()
	t.Cleanup(srv.Hub().Close)
	return rg
}

// routes registers what the tests need beyond the built-in routes.
func (rg *rig) routes(s *Server) {
	s.HandleFunc("GET /api/echo/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, 200, map[string]string{"id": r.PathValue("id"), "q": r.URL.Query().Get("q")})
	}, RouteOpts{})
	s.HandleFunc("POST /api/echo/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if !DecodeJSON(w, r, &body) {
			return
		}
		_ = WriteJSON(w, 200, map[string]string{"id": r.PathValue("id"), "name": body.Name})
	}, RouteOpts{})
	s.HandleFunc("PUT /api/echo/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, 200, map[string]bool{"put": true})
	}, RouteOpts{})
	s.HandleFunc("DELETE /api/echo/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, 200, map[string]bool{"deleted": true})
	}, RouteOpts{NoBody: true})
	s.HandleFunc("POST /api/small", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			A string `json:"a"`
		}
		if !DecodeJSON(w, r, &body) {
			return
		}
		_ = WriteJSON(w, 200, body)
	}, RouteOpts{MaxBody: 32})
	s.HandleFunc("POST /api/danger", func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, 200, map[string]bool{"done": true})
	}, RouteOpts{NeedsConfirm: true, ConfirmScope: "danger"})
	s.HandleFunc("POST /api/default-scope", func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, 200, map[string]bool{"done": true})
	}, RouteOpts{NeedsConfirm: true})
	s.HandleFunc("POST /api/cond", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		if !DecodeJSON(w, r, &body) {
			return
		}
		if body.Mode == "yolo" && !s.RequireConfirm(w, r, "mode:yolo") {
			return
		}
		_ = WriteJSON(w, 200, body)
	}, RouteOpts{})
	s.HandleFunc("GET /api/boom", func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom with " + s.Token())
	}, RouteOpts{})
	s.HandleFunc("GET /api/log", func(w http.ResponseWriter, r *http.Request) {
		Logf(r, "token=%s and %s\nforged line", s.Token(), "Authorization: Bearer "+s.Token())
		_ = WriteJSON(w, 200, map[string]string{"rid": RequestID(r)})
	}, RouteOpts{})
	s.HandleFunc("GET /api/slow", func(w http.ResponseWriter, r *http.Request) {
		rg.entered <- struct{}{}
		select {
		case <-rg.release:
		case <-r.Context().Done():
		}
		_ = WriteJSON(w, 200, map[string]bool{"slow": true})
	}, RouteOpts{})
	s.Handle("GET /api/events/{topic}", s.Hub().ServeSSE(func(r *http.Request) (string, bool) {
		t := r.PathValue("topic")
		return t, t != "forbidden"
	}), RouteOpts{})
}

// req is a request to build.
type req struct {
	method, target string
	host           string
	header         map[string]string
	body           string
	tls            bool
}

// do sends a request straight to the handler and returns the recorded response.
func (rg *rig) do(r req) *httptest.ResponseRecorder {
	rg.t.Helper()
	if r.method == "" {
		r.method = "GET"
	}
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, r.target, body)
	hr.Host = testHost
	if r.host != "" {
		hr.Host = r.host
	}
	for k, v := range r.header {
		if v == "" {
			hr.Header.Del(k)
			continue
		}
		hr.Header.Set(k, v)
	}
	if r.tls {
		hr.TLS = tlsState()
	}
	// A request that unexpectedly starts a stream must fail the test, not hang it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, hr.WithContext(ctx))
	return rec
}

// get sends an authenticated GET with the bearer token.
func (rg *rig) get(target string) *httptest.ResponseRecorder {
	rg.t.Helper()
	return rg.do(req{target: target, header: rg.bearer()})
}

// bearer is the header of a non-browser client.
func (rg *rig) bearer() map[string]string {
	return map[string]string{"Authorization": "Bearer " + rg.srv.Token()}
}

// login exchanges the token the way a browser does and returns the cookie header value.
func (rg *rig) login() string {
	rg.t.Helper()
	rec := rg.do(req{target: "/?token=" + rg.srv.Token()})
	if rec.Code != http.StatusSeeOther {
		rg.t.Fatalf("token exchange = %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return CookieName + "=" + c.Value
		}
	}
	rg.t.Fatal("no session cookie")
	return ""
}

// browser is the headers of a same-origin fetch from the page, carrying the cookie.
func browser(cookie string) map[string]string {
	return map[string]string{
		"Cookie": cookie, "Origin": testOrigin, "Sec-Fetch-Site": "same-origin",
		RequestHeader: RequestHeaderValue, "Content-Type": "application/json",
	}
}

// post sends a JSON POST as the page would.
func (rg *rig) post(cookie, target, body string, extra map[string]string) *httptest.ResponseRecorder {
	rg.t.Helper()
	h := browser(cookie)
	for k, v := range extra {
		h[k] = v
	}
	return rg.do(req{method: "POST", target: target, header: h, body: body})
}

// confirm obtains a confirmation id for a scope as the page would.
func (rg *rig) confirm(cookie, scope string) string {
	rg.t.Helper()
	rec := rg.post(cookie, "/api/confirm", fmt.Sprintf(`{"scope":%q}`, scope), nil)
	if rec.Code != 200 {
		rg.t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	mustJSON(rg.t, rec, &out)
	return out.ID
}

// tlsState is a request that arrived over TLS.
func tlsState() *tls.ConnectionState { return &tls.ConnectionState{} }

// mustJSON decodes a recorded JSON response.
func mustJSON(t testing.TB, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
}

// errCode returns the code of an error body, or "" if the body is not one.
func errCode(rec *httptest.ResponseRecorder) string {
	var e errorBody
	if json.Unmarshal(rec.Body.Bytes(), &e) != nil {
		return ""
	}
	return e.Code
}

// heldKeyForTest makes the process hold a provider key for the duration of the test, as a stored login does.
func heldKeyForTest(t testing.TB, name, value string) {
	t.Helper()
	harden.Provide(name, value)
	t.Cleanup(func() { harden.Provide(name, "") })
}

// uesc is the JSON unicode escape of a code point given in hex digits, as encoding/json writes it.
func uesc(hex string) string { return "\\" + "u" + hex }
