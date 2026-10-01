package inspect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

const loopbackHost = "127.0.0.1:8787"

// do sends a request straight to the handler with a loopback Host header.
func do(t testing.TB, h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = loopbackHost
	for k, v := range hdr {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getJSON(t testing.TB, h http.Handler, target string, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, h, http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("GET %s content type = %q", target, ct)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: %v\n%s", target, err, rec.Body.String())
		}
	}
	return rec
}

func newTestServer(t testing.TB, dir string, mut func(*Config)) *Server {
	t.Helper()
	cfg := Config{Root: dir, Addr: loopbackHost, LoadWait: 5 * time.Second}
	if mut != nil {
		mut(&cfg)
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestEveryEndpointServesJSON(t *testing.T) {
	dir, res := synthDir(t, synthCfg{Workers: 3, Steps: 30, Anomalies: true})
	srv := newTestServer(t, dir, nil)
	h := srv.Handler()

	var sum Summary
	rec := getJSON(t, h, "/api/summary", &sum)
	if sum.Totals.Requests != res.Requests || sum.Session.Name != "session" || sum.State != StateEnded {
		t.Errorf("summary = %d requests, %s, %s", sum.Totals.Requests, sum.Session.Name, sum.State)
	}
	for _, hd := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"} {
		if rec.Header().Get(hd) != "" {
			t.Errorf("%s is set: the API is same-origin only", hd)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("API responses must not be cached: %q", rec.Header().Get("Cache-Control"))
	}

	var agents []AgentView
	getJSON(t, h, "/api/agents", &agents)
	if len(agents) != 4 {
		t.Errorf("agents = %d", len(agents))
	}
	var detail AgentDetail
	getJSON(t, h, "/api/agent/be-1", &detail)
	if detail.Agent.ID != "be-1" || len(detail.Tools) == 0 || len(detail.Compactions) == 0 {
		t.Errorf("agent detail = %+v", detail.Agent)
	}
	if rec := do(t, h, "GET", "/api/agent/nobody", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent = %d", rec.Code)
	}

	var page RequestPage
	getJSON(t, h, "/api/requests?agent=be-1&kind=main&limit=5", &page)
	if len(page.Requests) != 5 || !page.More || page.Requests[0].Agent != "be-1" || page.Requests[0].Kind != "main" {
		t.Errorf("requests = %d more=%v", len(page.Requests), page.More)
	}
	var all RequestPage
	getJSON(t, h, "/api/requests?tail=1&limit=20000", &all)
	if len(all.Requests) != res.Requests {
		t.Errorf("tail = %d requests, want %d", len(all.Requests), res.Requests)
	}
	var delta RequestPage
	getJSON(t, h, fmt.Sprintf("/api/requests?since=%d", all.Rev), &delta)
	if len(delta.Requests) != 0 {
		t.Errorf("nothing changed but %d rows came back", len(delta.Requests))
	}

	var comps CompactionReport
	getJSON(t, h, "/api/compactions", &comps)
	if len(comps.Compactions) != res.Commits {
		t.Errorf("compactions = %d, want %d", len(comps.Compactions), res.Commits)
	}
	var an AnomalyReport
	getJSON(t, h, "/api/anomalies", &an)
	if an.Totals.Drift != 1 || an.Totals.LowHit != 1 || len(an.Anomalies) != 2 {
		t.Errorf("anomalies = %+v", an.Totals)
	}
	var sw SwarmReport
	getJSON(t, h, "/api/swarm", &sw)
	if len(sw.Tasks) != 3 || sw.Totals.Spawns != 4 {
		t.Errorf("swarm = %d tasks %d spawns", len(sw.Tasks), sw.Totals.Spawns)
	}

	rid := all.Requests[len(all.Requests)/2].ID
	var lay LayerReport
	getJSON(t, h, "/api/layers?text=1&req="+url.QueryEscape(rid), &lay)
	if lay.Req.ID != rid || len(lay.Layers) != 7 {
		t.Errorf("layers = %+v", lay.Req.ID)
	}
	if rec := do(t, h, "GET", "/api/layers?req=nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown request = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/layers", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("layers without req = %d", rec.Code)
	}

	var ev EventPage
	getJSON(t, h, "/api/events?limit=10", &ev)
	if len(ev.Events) != 10 || ev.LastSeq != res.LastSeq || ev.Events[9].Seq != res.LastSeq {
		t.Errorf("events = %d last %d", len(ev.Events), ev.LastSeq)
	}
	getJSON(t, h, "/api/events?since=0&limit=3", &ev)
	if len(ev.Events) != 3 || ev.Events[0].Seq != 1 || !ev.More {
		t.Errorf("events from the start = %+v", ev.Events)
	}

	var list SessionList
	getJSON(t, h, "/api/sessions", &list)
	if list.Mode != "single" || len(list.Sessions) != 1 || list.Current != "." {
		t.Errorf("sessions = %+v", list)
	}
	var health map[string]any
	getJSON(t, h, "/healthz", &health)
	if health["ok"] != true || len(health) != 1 {
		t.Errorf("healthz = %v: it must say nothing else", health)
	}
}

func TestMethodsOtherThanGetAndHeadAreRefused(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 8})
	h := newTestServer(t, dir, nil).Handler()
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS", "TRACE", "CONNECT", "PROPFIND"} {
		for _, path := range []string{"/", "/api/summary", "/api/events", "/healthz", "/nope"} {
			rec := do(t, h, method, path, nil)
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s = %d allow %q, want 405 with Allow: GET, HEAD", method, path, rec.Code, rec.Header().Get("Allow"))
			}
		}
	}
	// A body on GET is ignored, not interpreted.
	req := httptest.NewRequest("GET", "/api/summary", strings.NewReader(`{"session":"../../x"}`))
	req.Host = loopbackHost
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET with a body = %d", rec.Code)
	}
	head := do(t, h, "HEAD", "/api/summary", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 && head.Header().Get("Content-Type") == "" {
		t.Errorf("HEAD = %d", head.Code)
	}
	if rec := do(t, h, "GET", "/api/nothing", nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("unknown API path = %d %s", rec.Code, rec.Body.String())
	}
}

func TestHostHeaderBlocksDNSRebinding(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 8})
	h := newTestServer(t, dir, nil).Handler()
	ok := []string{"127.0.0.1:8787", "127.0.0.1", "localhost:8787", "LOCALHOST", "Localhost:1", "[::1]:8787", "[::1]"}
	for _, host := range ok {
		if rec := do(t, h, "GET", "/api/summary", map[string]string{"Host": host}); rec.Code != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, rec.Code)
		}
	}
	bad := []string{"evil.com", "evil.com:8787", "127.0.0.1.evil.com", "localhost.evil.com:8787", "192.168.1.5:8787", "[::2]:8787", "0.0.0.0:8787", "", "127.0.0.1@evil.com", "evil.com#127.0.0.1"}
	for _, host := range bad {
		for _, path := range []string{"/api/summary", "/", "/healthz", "/app.css"} {
			if rec := do(t, h, "GET", path, map[string]string{"Host": host}); rec.Code != http.StatusForbidden {
				t.Errorf("Host %q on %s = %d, want 403: a rebound name must not read the dashboard", host, path, rec.Code)
			}
		}
	}
}

func TestTokenIsRequiredAndComparedSafely(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 8})
	const tok = "s3cret-token"
	srv := newTestServer(t, dir, func(c *Config) { c.Token = tok })
	h := srv.Handler()

	for _, p := range []string{"/", "/api/summary", "/api/sessions", "/app.css", "/js/app.js", "/api/events", "/nope"} {
		rec := do(t, h, "GET", p, nil)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" || strings.Contains(rec.Body.String(), tok) {
			t.Errorf("no token on %s = %d", p, rec.Code)
		}
	}
	if rec := do(t, h, "GET", "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("healthz must stay open for probes: %d", rec.Code)
	}
	bearer := func(v string) map[string]string { return map[string]string{"Authorization": v} }
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"right token", bearer("Bearer " + tok), 200},
		{"scheme is case-insensitive", bearer("bearer " + tok), 200},
		{"surrounding space", bearer("Bearer  " + tok + " "), 200},
		{"wrong token", bearer("Bearer wrong"), 401},
		{"prefix of the token", bearer("Bearer " + tok[:len(tok)-1]), 401},
		{"token plus suffix", bearer("Bearer " + tok + "x"), 401},
		{"empty token", bearer("Bearer "), 401},
		{"basic auth", bearer("Basic " + tok), 401},
		{"no scheme", bearer(tok), 401},
		{"huge token", bearer("Bearer " + strings.Repeat("a", 60000)), 401},
		{"wrong cookie", map[string]string{"Cookie": cookieName + "=" + tok}, 401}, // the cookie holds a digest, never the raw token
		{"empty cookie", map[string]string{"Cookie": cookieName + "="}, 401},
	}
	for _, tc := range cases {
		if rec := do(t, h, "GET", "/api/summary", tc.hdr); rec.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	// The token check compares fixed-size digests; lengths never short-circuit it.
	for _, s := range []string{"", "a", tok[:1], tok[:len(tok)-1], tok + "\x00", strings.ToUpper(tok)} {
		if srv.tokenEqual(s) {
			t.Errorf("tokenEqual(%q) = true", s)
		}
	}
	if !srv.tokenEqual(tok) {
		t.Error("tokenEqual rejected the token")
	}

	// A browser is handed the token once, in the URL; it becomes a cookie and the URL is cleaned.
	rec := do(t, h, "GET", "/?x=1&token="+tok+"&y=2", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("token in the URL = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "token") || strings.Contains(loc, tok) || !strings.Contains(loc, "x=1") || !strings.Contains(loc, "y=2") {
		t.Errorf("redirect to %q: the token must be stripped and other parameters kept", loc)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Fatalf("cookies = %+v", cookies)
	}
	if strings.Contains(cookies[0].Value, tok) {
		t.Error("the cookie must not contain the raw token")
	}
	if rec := do(t, h, "GET", "/api/summary", map[string]string{"Cookie": cookieName + "=" + cookies[0].Value}); rec.Code != http.StatusOK {
		t.Errorf("cookie = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/", map[string]string{"Cookie": cookieName + "=" + cookies[0].Value}); rec.Code != http.StatusOK {
		t.Errorf("page with cookie = %d", rec.Code)
	}
	// On the API a token parameter authorises the call without a redirect, for scripts.
	if rec := do(t, h, "GET", "/api/summary?token="+tok, nil); rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 0 {
		t.Errorf("API token parameter = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/?token=nope", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token parameter = %d", rec.Code)
	}
}

// The token URL is turned into a redirect. Whatever path the request line carries,
// the Location must stay on this origin: browsers read "//host" and "/\host" as
// another site.
func TestTokenRedirectNeverLeavesThisOrigin(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 0, MgrSteps: 2, Steps: 2})
	const tok = "s3cret-token"
	srv := newTestServer(t, dir, func(c *Config) { c.Token = tok })
	h := srv.Handler()
	sameOrigin := func(p, loc string) {
		t.Helper()
		u, err := url.Parse(loc)
		if err != nil || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") || strings.ContainsAny(loc, "\\") {
			t.Errorf("%s redirected to %q", p, loc)
		}
	}
	for _, p := range []string{"//evil.example/x", "///evil.example", "/\\evil.example", "/%5Cevil.example", "/%2F%2Fevil.example", "/../../evil.example", "/index.html", "/app.css", "/nope", "/"} {
		rec := do(t, h, "GET", p+"?keep=1&token="+tok, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s: %d, want a redirect", p, rec.Code)
			continue
		}
		loc := rec.Header().Get("Location")
		sameOrigin(p, loc)
		if strings.Contains(loc, tok) || !strings.Contains(loc, "keep=1") {
			t.Errorf("%s redirected to %q: the token goes, other parameters stay", p, loc)
		}
	}
	// Paths under /api/ take the token without a redirect; the mux may still clean an
	// untidy path, and that redirect stays on this origin too.
	for _, p := range []string{"/api/../evil", "/api//evil.example", "/api/./x"} {
		rec := do(t, h, "GET", p+"?token="+tok, nil)
		if loc := rec.Header().Get("Location"); loc != "" {
			sameOrigin(p, loc)
		}
	}
}

func TestNonLoopbackNeedsAToken(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 6})
	for _, addr := range []string{"0.0.0.0:8787", ":8787", "[::]:8787", "192.168.1.10:8787", "example.com:80", "10.0.0.1:0"} {
		if _, err := NewServer(Config{Root: dir, Addr: addr}); err == nil || !strings.Contains(err.Error(), "--token") {
			t.Errorf("NewServer(%q) without a token: %v, want a refusal that names --token", addr, err)
		}
		if _, err := Listen(addr, ""); err == nil {
			t.Errorf("Listen(%q) without a token succeeded", addr)
		}
		if _, err := NewServer(Config{Root: dir, Addr: addr, Token: "t"}); err != nil {
			t.Errorf("NewServer(%q) with a token: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0", "127.5.5.5:0"} {
		ln, err := Listen(addr, "")
		if err != nil {
			if strings.Contains(addr, "::1") {
				t.Logf("no IPv6 loopback here: %v", err)
				continue
			}
			if strings.Contains(err.Error(), "can't assign requested address") {
				// macOS only has 127.0.0.1 on its loopback interface; the rest of 127/8 needs an alias.
				t.Logf("%s is not configured on this machine: %v", addr, err)
				continue
			}
			t.Errorf("Listen(%q): %v", addr, err)
			continue
		}
		ln.Close()
	}
	if ln, err := Listen("0.0.0.0:0", "tok"); err != nil {
		t.Errorf("a wildcard listener with a token: %v", err)
	} else {
		ln.Close()
	}
	if _, err := Listen("no-port", ""); err == nil {
		t.Error("an address without a port was accepted")
	}
	// Serve is a second line of defence: a non-loopback socket without a token is refused.
	srv := newTestServer(t, dir, nil)
	wild, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip("cannot listen on the wildcard address")
	}
	defer wild.Close()
	if err := srv.Serve(context.Background(), wild); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("Serve on a wildcard socket without a token: %v", err)
	}
	// With a token a public address accepts any Host (the token is the protection).
	pub := newTestServer(t, dir, func(c *Config) { c.Addr = "0.0.0.0:8787"; c.Token = "t" })
	if rec := do(t, pub.Handler(), "GET", "/api/summary", map[string]string{"Host": "myhost.example:8787", "Authorization": "Bearer t"}); rec.Code != http.StatusOK {
		t.Errorf("token-protected public address = %d", rec.Code)
	}
}

func TestParametersCannotReachTheFilesystem(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 8})
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(t, dir, nil).Handler()
	type tc struct {
		path string
		want []int
	}
	hostile := []tc{
		{"/api/summary?session=../secret.txt", []int{404}},
		{"/api/summary?session=..%2f..%2fetc%2fpasswd", []int{404}},
		{"/api/summary?session=%2e%2e", []int{404}},
		{"/api/summary?session=%00", []int{400}},
		{"/api/summary?session=" + strings.Repeat("a", 600), []int{400}},
		{"/api/summary?session=/etc/passwd", []int{404}},
		{"/api/summary?session=session", []int{404}}, // the directory's own name is not an id either
		{"/api/agent/..%2f..%2fsecret.txt", []int{404}},
		{"/api/agent/%00", []int{404, 400}},
		{"/api/agent/", []int{404}},
		{"/api/layers?req=../../secret.txt", []int{404}},
		{"/api/layers?req=" + strings.Repeat("b", 600), []int{400}},
		{"/api/layers?req=%00", []int{400}},
		{"/api/requests?agent=../../secret.txt", []int{200}}, // an unknown agent is an empty page
		{"/api/requests?since=abc", []int{400}},
		{"/api/requests?since=-1", []int{400}},
		{"/api/requests?limit=99999999999999999999", []int{400}},
		{"/api/requests?kind=../../x", []int{400}},
		{"/api/events?since=1e9", []int{400}},
		{"/api/events?agent=" + strings.Repeat("c", 600), []int{400}},
		{"/api/events?type=%00", []int{400}},
		// a redirect to the cleaned path is fine (the mux sends 301 before Go 1.26, 307 from it); what matters is what the
		// body holds, which is checked for every row below
		{"/../../secret.txt", []int{301, 307, 308, 404}},
		{"/%2e%2e/%2e%2e/secret.txt", []int{301, 307, 308, 404}},
		{"/js/../../../secret.txt", []int{301, 307, 308, 404}},
		{"/js/..%2f..%2f..%2fsecret.txt", []int{404}},
		{"//secret.txt", []int{301, 307, 308, 404}},
		{"/..;/secret.txt", []int{404}},
		{"/js/", []int{404}},
		{"/index.html/../../secret.txt", []int{301, 307, 308, 404}},
		{"/.git/config", []int{404}},
		{"/go.mod", []int{404}},
	}
	for _, c := range hostile {
		rec := do(t, h, "GET", c.path, nil)
		okStatus := false
		for _, w := range c.want {
			okStatus = okStatus || rec.Code == w
		}
		if !okStatus {
			t.Errorf("GET %s = %d, want one of %v", c.path, rec.Code, c.want)
		}
		if strings.Contains(rec.Body.String(), "TOP SECRET") || strings.Contains(rec.Body.String(), "root:") {
			t.Errorf("GET %s leaked file contents: %.100s", c.path, rec.Body.String())
		}
	}
	// Numeric parameters are clamped, not trusted.
	var page RequestPage
	getJSON(t, h, "/api/requests?limit=999999999", &page)
	if len(page.Requests) > 20000 {
		t.Errorf("limit not clamped: %d rows", len(page.Requests))
	}
	var ev EventPage
	getJSON(t, h, "/api/events?since=0&limit=999999", &ev)
	if len(ev.Events) > 2000 {
		t.Errorf("event limit not clamped: %d", len(ev.Events))
	}
}

func TestLogStringsAreInertInEveryResponse(t *testing.T) {
	b := newEvb(t)
	xss := []string{`<img src=x onerror=alert(1)>`, `</script><script>alert(2)</script>`, `"><svg onload=alert(3)>`, `'; DROP TABLE x;--`, `&lt;b&gt;`}
	b.emit("", "session.start", m{"model": xss[0], "provider": xss[1], "root": "/x/" + xss[2], "version": xss[3]})
	b.emit("", "user.input", m{"text": xss[2]})
	agent := xss[0]
	b.emit("swarm", "agent.spawn", m{"id": agent, "role": xss[1], "task": xss[2], "by": xss[3], "model": xss[4]})
	b.req(agent, xss[3]+".1", []m{sec("shared", 100, xss[4])}, m{"model": xss[0], "role": xss[1], "cache_key": xss[2]})
	b.resp(agent, xss[3]+".1", 10, 0, 100, 5, m{"model": xss[0]})
	b.emit(agent, "tool.call", m{"id": "c1", "name": xss[1], "input": m{"command": xss[2], "path": xss[0]}})
	b.emit(agent, "tool.result", m{"id": "c1", "name": xss[1], "chars": 1, "ms": 1})
	b.emit(agent, "mail.send", m{"id": "m1", "from": agent, "to": xss[2], "kind": xss[3], "text": xss[0]})
	b.emit(agent, "cache.anomaly", m{"kind": xss[0], "diverged": xss[1], "req": xss[3] + ".1"})
	b.emit(agent, "compact.plan", m{"decision": "start", "mode": "fork", "reason": xss[2]})
	b.emit(agent, "agent.end", m{"id": agent, "state": xss[0], "evidence": xss[1]})
	b.emit("swarm", "governor", m{"note": xss[0]})
	b.emit(agent, "lease", m{"path": xss[2], "action": xss[1]})
	b.emit(agent, "custom.type."+xss[0], m{"x": xss[1]})
	h := newTestServer(t, b.dir, nil).Handler()
	paths := []string{"/api/summary", "/api/agents", "/api/agent/" + url.PathEscape(agent), "/api/requests?tail=1", "/api/compactions", "/api/anomalies",
		"/api/swarm", "/api/layers?text=1&req=" + url.QueryEscape(xss[3]+".1"), "/api/events?since=0&limit=100", "/api/sessions"}
	for _, p := range paths {
		rec := do(t, h, "GET", p, nil)
		if rec.Code != 200 {
			t.Errorf("GET %s = %d %s", p, rec.Code, rec.Body.String())
			continue
		}
		body := rec.Body.String()
		if strings.ContainsAny(body, "<>") {
			t.Errorf("GET %s contains raw angle brackets: an untrusted string could be sniffed as markup: %.200s", p, body)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("GET %s headers %v", p, rec.Header())
		}
	}
	// The strings are present, escaped, not dropped.
	body := do(t, h, "GET", "/api/agents", nil).Body.String()
	if !strings.Contains(body, `\u003cimg src=x onerror=alert(1)\u003e`) {
		t.Errorf("hostile agent id was not carried through as escaped data: %.300s", body)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 6})
	srv := newTestServer(t, dir, func(c *Config) { c.Token = "tok" })
	h := srv.Handler()
	reqs := []struct {
		method, path string
		hdr          map[string]string
	}{
		{"GET", "/", map[string]string{"Authorization": "Bearer tok"}}, {"GET", "/app.css", map[string]string{"Authorization": "Bearer tok"}},
		{"GET", "/api/summary", map[string]string{"Authorization": "Bearer tok"}}, {"GET", "/api/nothing", map[string]string{"Authorization": "Bearer tok"}},
		{"GET", "/", nil}, {"POST", "/", nil}, {"GET", "/healthz", nil}, {"GET", "/", map[string]string{"Host": "evil.com"}}, {"GET", "/?token=tok", nil},
	}
	for _, r := range reqs {
		rec := do(t, h, r.method, r.path, r.hdr)
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'none'", "connect-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s %s (%d): CSP %q lacks %q", r.method, r.path, rec.Code, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "*") || strings.Contains(csp, "http") {
			t.Errorf("CSP is not strict: %q", csp)
		}
		for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cross-Origin-Resource-Policy": "same-origin"} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s: %s = %q, want %q", r.method, r.path, k, got, v)
			}
		}
	}
}

func TestStaticAssetsAreServedFromTheBinary(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 6})
	h := newTestServer(t, dir, nil).Handler()
	want := map[string]string{"/": "text/html; charset=utf-8", "/index.html": "text/html; charset=utf-8", "/app.css": "text/css; charset=utf-8", "/js/app.js": "text/javascript; charset=utf-8",
		"/js/lib.js": "text/javascript; charset=utf-8", "/favicon.svg": "image/svg+xml"}
	for p, ct := range want {
		rec := do(t, h, "GET", p, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != ct || rec.Body.Len() == 0 {
			t.Errorf("GET %s = %d %q", p, rec.Code, rec.Header().Get("Content-Type"))
		}
		etag := rec.Header().Get("ETag")
		if etag == "" || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s: etag %q cache-control %q", p, etag, rec.Header().Get("Cache-Control"))
		}
		if again := do(t, h, "GET", p, map[string]string{"If-None-Match": etag}); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
			t.Errorf("GET %s with its ETag = %d", p, again.Code)
		}
	}
	if rec := do(t, h, "GET", "/", nil); !strings.Contains(rec.Body.String(), `<script type="module" src="/js/app.js">`) {
		t.Error("index.html does not load the app")
	}
	for _, p := range []string{"/nothing.js", "/js/nothing.js", "/web/index.html", "/index.htm"} {
		if rec := do(t, h, "GET", p, nil); rec.Code != 404 {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
}

func TestLiveTailingThroughTheServer(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	srv := newTestServer(t, dir, nil)
	h := srv.Handler()
	emit := func(i int) {
		id := fmt.Sprintf("a.%d", i)
		log.Emit("a", "model.request", m{"req": id, "agent": "a", "kind": "main", "model": "claude-sonnet-5-5", "sections": []m{sec("shared", 1000, h64("s"))}, "thread_from": 1, "thread_to": i})
		log.Emit("a", "model.response", m{"req": id, "model": "claude-sonnet-5-5", "usage": map[string]int{"input_tokens": 10, "cache_read_tokens": 990, "output_tokens": 1}})
		log.Flush()
	}
	emit(1)
	var s1 Summary
	getJSON(t, h, "/api/summary", &s1)
	if s1.Totals.Requests != 1 || s1.State != StateLive {
		t.Fatalf("first summary = %+v state %s", s1.Totals, s1.State)
	}
	var p1 RequestPage
	getJSON(t, h, "/api/requests?tail=1", &p1)

	emit(2)
	emit(3)
	srv.Tick() // what the server's loop does every second
	var s2 Summary
	getJSON(t, h, "/api/summary", &s2)
	if s2.Totals.Requests != 3 || s2.Rev <= s1.Rev {
		t.Fatalf("after two more: %d requests rev %d -> %d", s2.Totals.Requests, s1.Rev, s2.Rev)
	}
	var delta RequestPage
	getJSON(t, h, fmt.Sprintf("/api/requests?since=%d", p1.Rev), &delta)
	if len(delta.Requests) != 2 || delta.Requests[0].ID != "a.2" || delta.Requests[1].ID != "a.3" {
		t.Errorf("delta = %+v: only the new requests since the last poll", delta.Requests)
	}
	var ev EventPage
	getJSON(t, h, fmt.Sprintf("/api/events?since=%d", s1.Log.LastSeq), &ev)
	if len(ev.Events) != 4 || ev.LastSeq != s2.Log.LastSeq {
		t.Errorf("raw tail = %d events, last %d", len(ev.Events), ev.LastSeq)
	}
	var s3 Summary
	getJSON(t, h, "/api/summary", &s3)
	if s3.Rev != s2.Rev {
		t.Error("the revision moved with nothing new")
	}
}

func TestMultiSessionServer(t *testing.T) {
	root := sessionTree(t)
	srv := newTestServer(t, root, nil)
	h := srv.Handler()
	srv.reg.indexPending(context.Background())

	var list SessionList
	getJSON(t, h, "/api/sessions", &list)
	if list.Mode != "multi" || len(list.Sessions) != 4 {
		t.Fatalf("sessions = %+v", list)
	}
	for _, s := range list.Sessions {
		if s.Digest == nil || s.Digest.Requests == 0 {
			t.Errorf("session %s has no digest", s.ID)
		}
	}
	if rec := do(t, h, "GET", "/api/summary", nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "several sessions") {
		t.Errorf("no session parameter in a multi-session directory = %d %s", rec.Code, rec.Body.String())
	}
	var sum Summary
	getJSON(t, h, "/api/summary?session="+url.QueryEscape("r001/taskA/1"), &sum)
	if sum.Session.ID != "r001/taskA/1" || sum.Totals.Requests == 0 {
		t.Errorf("summary = %s", sum.Session.ID)
	}
	var page RequestPage
	getJSON(t, h, "/api/requests?tail=1&session="+url.QueryEscape("loose"), &page)
	if len(page.Requests) == 0 {
		t.Error("no requests for the loose session")
	}
	for _, bad := range []string{"../loose", "r001/../loose", "r001", "/etc", "r001/taskA/0/blobs/x", "linkdir", "linklog", "deep/a/b/c/d/e", ".git/z"} {
		if rec := do(t, h, "GET", "/api/summary?session="+url.QueryEscape(bad), nil); rec.Code != http.StatusNotFound {
			t.Errorf("session %q = %d, want 404", bad, rec.Code)
		}
	}
	// Two sessions are independent models.
	var a, b Summary
	getJSON(t, h, "/api/summary?session=r001/taskA/0", &a)
	getJSON(t, h, "/api/summary?session=r001/taskB/0", &b)
	if a.Session.ID == b.Session.ID || a.Totals.CacheRead == b.Totals.CacheRead {
		t.Errorf("sessions mixed up: %s %s", a.Session.ID, b.Session.ID)
	}
}

func TestRequestsWaitForALoadingSessionThenAnswer(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 6})
	srv := newTestServer(t, dir, func(c *Config) { c.LoadWait = 20 * time.Millisecond })
	h := srv.Handler()
	e := srv.reg.get("")
	// Hold the entry in the loading state.
	e.mu.Lock()
	e.loading, e.done = true, make(chan struct{})
	done := e.done
	e.mu.Unlock()
	e.read.Store(300)
	e.total.Store(1000)
	rec := do(t, h, "GET", "/api/summary", nil)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusAccepted || body["loading"] != true || body["read"] != float64(300) || body["total"] != float64(1000) {
		t.Fatalf("loading answer = %d %v", rec.Code, body)
	}
	// It finishes: the same request now answers.
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.sess, e.loading = s, false
	e.mu.Unlock()
	close(done)
	if rec := do(t, h, "GET", "/api/summary", nil); rec.Code != http.StatusOK {
		t.Errorf("after loading = %d", rec.Code)
	}
}

func TestUnreadableLogAnswers500WithoutLeakingThePath(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 6})
	srv := newTestServer(t, dir, nil)
	h := srv.Handler()
	if err := os.Remove(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, "GET", "/api/summary", nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), dir) || strings.Contains(rec.Body.String(), "events.jsonl") {
		t.Errorf("unreadable log = %d %s", rec.Code, rec.Body.String())
	}
}

func TestServeOverARealSocketAndShutDown(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 6})
	srv := newTestServer(t, dir, func(c *Config) { c.Interval = 20 * time.Millisecond })
	ln, err := Listen("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("healthz = %d %s", res.StatusCode, body)
	}
	// The browser-visible page.
	res, err = client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(page), "<title>") {
		t.Errorf("page = %d", res.StatusCode)
	}
	// A rebound name is refused over the wire as well.
	req, _ := http.NewRequest("GET", base+"/api/summary", nil)
	req.Host = "evil.example"
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("rebound Host over the wire = %d", res.StatusCode)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("Serve returned %v after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
	if _, err := client.Get(base + "/healthz"); err == nil {
		t.Error("still serving after shutdown")
	}
}
