package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// wantHeaders are the headers every response carries.
var wantHeaders = map[string]string{
	"X-Content-Type-Options":       "nosniff",
	"X-Frame-Options":              "DENY",
	"Referrer-Policy":              "no-referrer",
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Resource-Policy": "same-origin",
}

// checkEnvelopeHeaders fails the test unless the response carries the full set of security headers.
func checkEnvelopeHeaders(t testing.TB, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'",
		"img-src 'self' data:", "connect-src 'self'", "frame-ancestors 'none'", "form-action 'none'", "base-uri 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("%s (%d): CSP %q lacks %q", label, rec.Code, csp, want)
		}
	}
	// 'unsafe-inline' is allowed for style attributes and nothing else: not for scripts, not for style elements, never eval.
	if strings.Contains(strings.ReplaceAll(csp, "style-src-attr 'unsafe-inline'", ""), "unsafe") || strings.Contains(csp, "*") || strings.Contains(csp, "http") {
		t.Errorf("%s: CSP is not strict: %q", label, csp)
	}
	for k, v := range wantHeaders {
		if got := h.Get(k); got != v {
			t.Errorf("%s (%d): %s = %q, want %q", label, rec.Code, k, got, v)
		}
	}
	if !strings.Contains(h.Get("Permissions-Policy"), "camera=()") || !strings.Contains(h.Get("Permissions-Policy"), "microphone=()") {
		t.Errorf("%s: Permissions-Policy = %q", label, h.Get("Permissions-Policy"))
	}
	if cc := h.Get("Cache-Control"); cc != "no-store" && cc != "no-cache" {
		t.Errorf("%s (%d): Cache-Control = %q", label, rec.Code, cc)
	}
	for _, bad := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Methods", "Server"} {
		if v := h.Get(bad); v != "" {
			t.Errorf("%s: %s = %q: the API is same-origin only", label, bad, v)
		}
	}
	if h.Get("X-Request-Id") == "" {
		t.Errorf("%s: no X-Request-Id", label)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	tok := rg.srv.Token()
	small := strings.Repeat("a", 100)
	cases := []struct {
		name string
		r    req
		code int
	}{
		{"page", req{target: "/", header: map[string]string{"Cookie": cookie}}, 200},
		{"asset", req{target: "/app.css", header: map[string]string{"Cookie": cookie}}, 200},
		{"asset 304", req{target: "/app.css", header: map[string]string{"Cookie": cookie, "If-None-Match": "*"}}, 304},
		{"spa fallback", req{target: "/sessions/abc", header: map[string]string{"Cookie": cookie}}, 200},
		{"api", req{target: "/api/ping", header: map[string]string{"Cookie": cookie}}, 200},
		{"healthz", req{target: "/healthz"}, 200},
		{"unauthenticated api", req{target: "/api/ping"}, 401},
		{"unauthenticated page", req{target: "/", header: map[string]string{"Accept": "text/html"}}, 401},
		{"bad bearer", req{target: "/api/ping", header: map[string]string{"Authorization": "Bearer nope"}}, 401},
		{"token redirect", req{target: "/?token=" + tok}, 303},
		{"token on api", req{target: "/api/ping?token=" + tok}, 401},
		{"bad host", req{target: "/", host: "evil.example"}, 403},
		{"bad host healthz", req{target: "/healthz", host: "evil.example:6969"}, 403},
		{"cross-site fetch", req{target: "/api/ping", header: map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "cross-site"}}, 403},
		{"cross-origin post", req{method: "POST", target: "/api/echo/1", header: map[string]string{"Cookie": cookie, "Origin": "http://evil.example"}}, 403},
		{"not found api", req{target: "/api/nothing", header: map[string]string{"Cookie": cookie}}, 404},
		{"not found asset", req{target: "/nothing.js", header: map[string]string{"Cookie": cookie}}, 404},
		{"unclean path", req{target: "/js/../app.css", header: map[string]string{"Cookie": cookie}}, 404},
		{"method not allowed", req{method: "POST", target: "/api/ping", header: browser(cookie), body: "{}"}, 405},
		{"missing csrf header", req{method: "POST", target: "/api/echo/1", header: map[string]string{"Cookie": cookie, "Origin": testOrigin}}, 403},
		{"wrong content type", req{method: "POST", target: "/api/echo/1", header: func() map[string]string {
			h := browser(cookie)
			h["Content-Type"] = "text/plain"
			return h
		}(), body: `{"name":"x"}`}, 415},
		{"body too large", req{method: "POST", target: "/api/small", header: browser(cookie), body: `{"a":"` + small + `"}`}, 413},
		{"bad json", req{method: "POST", target: "/api/echo/1", header: browser(cookie), body: `{`}, 400},
		{"confirm required", req{method: "POST", target: "/api/danger", header: browser(cookie), body: `{}`}, 428},
		{"panic", req{target: "/api/boom", header: map[string]string{"Cookie": cookie}}, 500},
	}
	for _, c := range cases {
		rec := rg.do(c.r)
		if rec.Code != c.code {
			t.Errorf("%s = %d %s, want %d", c.name, rec.Code, strings.TrimSpace(rec.Body.String()), c.code)
		}
		checkEnvelopeHeaders(t, c.name, rec)
	}
}

func TestEveryErrorBodyIsUniformJSON(t *testing.T) {
	rg := newRig(t, nil)
	for _, r := range []req{
		{target: "/api/ping"}, {target: "/api/missing", header: rg.bearer()}, {method: "POST", target: "/api/ping", header: rg.bearer()},
		{target: "/", host: "evil.example"}, {target: "/js/../x", header: rg.bearer()},
	} {
		rec := rg.do(r)
		if rec.Code < 400 {
			t.Errorf("%v = %d", r, rec.Code)
			continue
		}
		var e errorBody
		mustJSON(t, rec, &e)
		if e.Error == "" || e.Code == "" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: error body %q (%s)", r.method, r.target, rec.Body.String(), rec.Header().Get("Content-Type"))
		}
	}
}

func TestMethodsAndRouting(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	// Unknown paths and wrong methods are answered after authentication: a stranger learns which routes exist from nothing.
	for _, r := range []req{{target: "/api/nothing"}, {method: "POST", target: "/api/ping"}, {method: "DELETE", target: "/healthz"}, {method: "TRACE", target: "/"}} {
		if rec := rg.do(r); rec.Code != 401 && rec.Code != 403 {
			t.Errorf("%s %s without a credential = %d", r.method, r.target, rec.Code)
		}
	}
	b := rg.bearer()
	bpost := func(method, target string) *httptest.ResponseRecorder {
		h := map[string]string{RequestHeader: RequestHeaderValue, "Content-Type": "application/json"}
		for k, v := range b {
			h[k] = v
		}
		return rg.do(req{method: method, target: target, header: h})
	}
	for _, c := range []struct {
		method, target string
		code           int
		allow          string
	}{
		{"POST", "/api/ping", 405, "GET, HEAD"},
		{"PUT", "/api/ping", 405, "GET, HEAD"},
		{"DELETE", "/api/ping", 405, "GET, HEAD"},
		{"PATCH", "/api/ping", 405, "GET, HEAD"},
		{"OPTIONS", "/api/ping", 405, "GET, HEAD"},
		{"TRACE", "/api/ping", 405, "GET, HEAD"},
		{"PROPFIND", "/api/ping", 405, "GET, HEAD"},
		{"GET", "/api/danger", 405, "POST"},
		{"PUT", "/api/small", 405, "POST"},
		{"POST", "/api/echo/1", 200, ""},
		{"PUT", "/api/echo/1", 200, ""},
		{"DELETE", "/api/echo/1", 200, ""},
		{"PATCH", "/api/echo/1", 405, ""},
		{"POST", "/api/unknown", 404, ""}, // the catch-alls are GET routes, but they are not what the path "has"
		{"POST", "/nothing", 404, ""},
	} {
		rec := bpost(c.method, c.target)
		if c.method == "POST" && c.target == "/api/echo/1" {
			rec = rg.do(req{method: "POST", target: c.target, header: mergeHeaders(b, map[string]string{RequestHeader: RequestHeaderValue, "Content-Type": "application/json"}), body: `{"name":"n"}`})
		}
		if rec.Code != c.code {
			t.Errorf("%s %s = %d %s, want %d", c.method, c.target, rec.Code, strings.TrimSpace(rec.Body.String()), c.code)
		}
		if c.allow != "" && !strings.Contains(rec.Header().Get("Allow"), strings.Split(c.allow, ", ")[0]) {
			t.Errorf("%s %s: Allow = %q, want %q", c.method, c.target, rec.Header().Get("Allow"), c.allow)
		}
		if c.code == 405 && errCode(rec) != "method_not_allowed" {
			t.Errorf("%s %s: code %q", c.method, c.target, errCode(rec))
		}
	}
	// HEAD is a GET.
	if rec := rg.do(req{method: "HEAD", target: "/api/ping", header: b}); rec.Code != 200 {
		t.Errorf("HEAD = %d", rec.Code)
	}
	// Unknown API paths are JSON 404s; path values reach the handler.
	if rec := rg.do(req{target: "/api/nothing/at/all", header: map[string]string{"Cookie": cookie}}); rec.Code != 404 || errCode(rec) != "not_found" {
		t.Errorf("unknown api = %d %s", rec.Code, rec.Body.String())
	}
	var echo struct{ ID, Q string }
	mustJSON(t, rg.do(req{target: "/api/echo/abc%20d?q=%3Cb%3E", header: map[string]string{"Cookie": cookie}}), &echo)
	if echo.ID != "abc d" || echo.Q != "<b>" {
		t.Errorf("path value = %+v", echo)
	}
	// A GET body is ignored.
	if rec := rg.do(req{target: "/api/ping", header: b, body: `{"x":1}`}); rec.Code != 200 {
		t.Errorf("GET with a body = %d", rec.Code)
	}
}

// mergeHeaders returns a copy of a with b added.
func mergeHeaders(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestHandlePanicsOnProgrammingErrors(t *testing.T) {
	rg := newRig(t, nil)
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, p := range []string{"/no-method", "FETCH /x", "GET x", "GET example.com/x", "", "POST", "get /x", "CONNECT /x", "OPTIONS /x", "GET /api/ping"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Handle(%q) did not panic", p)
				}
			}()
			rg.srv.Handle(p, h, RouteOpts{})
		}()
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a GET route that needs a confirmation was accepted")
			}
		}()
		rg.srv.Handle("GET /api/x", h, RouteOpts{NeedsConfirm: true})
	}()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a nil handler was accepted")
			}
		}()
		rg.srv.Handle("GET /api/y", nil, RouteOpts{})
	}()
	// A pattern with a method and a path is fine, also after the server is built.
	rg.srv.Handle("GET /api/late", h, RouteOpts{})
	if rec := rg.get("/api/late"); rec.Code != 200 {
		t.Errorf("late route = %d", rec.Code)
	}
}

func TestHostHeaderBlocksDNSRebinding(t *testing.T) {
	rg := newRig(t, nil)
	b := rg.bearer()
	for _, host := range []string{"127.0.0.1:6969", "localhost:6969", "LOCALHOST:6969", "Localhost:6969", "[::1]:6969"} {
		for _, p := range []string{"/api/ping", "/healthz", "/"} {
			if rec := rg.do(req{target: p, host: host, header: b}); rec.Code != 200 {
				t.Errorf("Host %q on %s = %d, want 200", host, p, rec.Code)
			}
		}
	}
	bad := []string{
		"evil.com", "evil.com:6969", "127.0.0.1.evil.com:6969", "localhost.evil.com:6969", "192.168.1.5:6969", "[::2]:6969", "0.0.0.0:6969",
		"127.0.0.1@evil.com:6969", "evil.com#127.0.0.1:6969", "127.0.0.1:6970", "localhost:1", "127.0.0.1", "localhost", "[::1]", "127.0.0.2:6969",
		"127.0.0.1:6969.evil.com", "127.0.0.1:0", "127.0.0.1: 6969", " 127.0.0.1:6969", "127.0.0.1:6969 ", "localhost:6969,evil.com",
	}
	for _, host := range bad {
		for _, method := range []string{"GET", "POST"} {
			for _, p := range []string{"/api/ping", "/healthz", "/", "/app.css"} {
				h := mergeHeaders(b, map[string]string{RequestHeader: RequestHeaderValue, "Origin": "http://" + host, "Content-Type": "application/json"})
				rec := rg.do(req{method: method, target: p, host: host, header: h})
				if rec.Code != 403 || errCode(rec) != "bad_host" {
					t.Errorf("%s %s with Host %q = %d %s, want 403 bad_host", method, p, host, rec.Code, errCode(rec))
				}
			}
		}
	}
	// The empty Host header of an HTTP/1.0 request is refused as well.
	hr := httptest.NewRequest("GET", "/healthz", nil)
	hr.Host = ""
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, hr)
	if rec.Code != 403 {
		t.Errorf("empty Host = %d", rec.Code)
	}
}

func TestHostOnOtherPortsAndAddresses(t *testing.T) {
	rg := newRig(t, func(c *Config) { c.Addr = "127.0.0.1:80" })
	if rec := rg.do(req{target: "/healthz", host: "127.0.0.1"}); rec.Code != 200 {
		t.Errorf("a server on port 80 answers a Host without a port: %d", rec.Code)
	}
	if rec := rg.do(req{target: "/healthz", host: "127.0.0.1:6969"}); rec.Code != 403 {
		t.Errorf("wrong port = %d", rec.Code)
	}
	rg = newRig(t, func(c *Config) { c.Addr = "127.5.5.5:7000" })
	for host, want := range map[string]int{"127.5.5.5:7000": 200, "localhost:7000": 200, "127.0.0.1:7000": 200, "127.5.5.6:7000": 403, "127.5.5.5:6969": 403} {
		if rec := rg.do(req{target: "/healthz", host: host}); rec.Code != want {
			t.Errorf("Host %q = %d, want %d", host, rec.Code, want)
		}
	}
	// Remote access has no host allowlist: the token is the protection.
	rg = newRig(t, func(c *Config) { c.Addr = "0.0.0.0:6969"; c.AllowNonLoopback = true })
	if rec := rg.do(req{target: "/api/ping", host: "myhost.example:6969", header: rg.bearer()}); rec.Code != 200 {
		t.Errorf("remote Host = %d", rec.Code)
	}
	if rec := rg.do(req{target: "/api/ping", host: "myhost.example:6969"}); rec.Code != 401 {
		t.Errorf("remote Host without a token = %d", rec.Code)
	}
}

// The matrix of what a request that changes state must look like. "browser" requests carry the session cookie, "script" requests the bearer token.
func TestCrossSiteRequestForgeryMatrix(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	json := "application/json"
	type c struct {
		name string
		h    map[string]string
		code int
		want string
	}
	base := func(extra map[string]string, drop ...string) map[string]string {
		h := map[string]string{"Cookie": cookie, "Origin": testOrigin, RequestHeader: RequestHeaderValue, "Content-Type": json}
		for k, v := range extra {
			h[k] = v
		}
		for _, d := range drop {
			delete(h, d)
		}
		return h
	}
	cases := []c{
		{"the page itself", base(nil), 200, ""},
		{"same-origin fetch metadata", base(map[string]string{"Sec-Fetch-Site": "same-origin"}), 200, ""},
		{"charset is fine", base(map[string]string{"Content-Type": "application/json; charset=utf-8"}), 200, ""},
		{"upper-case type is fine", base(map[string]string{"Content-Type": "Application/JSON"}), 200, ""},
		{"same origin by 127.0.0.1 spelled in capitals", base(map[string]string{"Origin": "HTTP://127.0.0.1:6969"}), 200, ""},
		{"fetch metadata without Origin (a same-origin browser)", base(map[string]string{"Sec-Fetch-Site": "same-origin"}, "Origin"), 200, ""},

		{"a simple cross-origin POST (text/plain)", base(map[string]string{"Origin": "http://evil.example", "Content-Type": "text/plain", "Sec-Fetch-Site": "cross-site"}, RequestHeader), 403, "forbidden_site"},
		{"cross-origin with every header right", base(map[string]string{"Origin": "http://evil.example"}), 403, "forbidden_origin"},
		{"cross-origin form post", base(map[string]string{"Origin": "http://evil.example", "Content-Type": "application/x-www-form-urlencoded"}, RequestHeader), 403, "forbidden_origin"},
		{"the same host on another port", base(map[string]string{"Origin": "http://127.0.0.1:3000"}), 403, "forbidden_origin"},
		{"another loopback name, same port", base(map[string]string{"Origin": "http://localhost:6969"}), 403, "forbidden_origin"},
		{"the port is part of the origin (no port)", base(map[string]string{"Origin": "http://127.0.0.1"}), 403, "forbidden_origin"},
		{"https origin for an http server", base(map[string]string{"Origin": "https://127.0.0.1:6969"}), 403, "forbidden_origin"},
		{"a prefix of the origin", base(map[string]string{"Origin": "http://127.0.0.1:69"}), 403, "forbidden_origin"},
		{"the origin with a suffix", base(map[string]string{"Origin": "http://127.0.0.1:6969.evil.example"}), 403, "forbidden_origin"},
		{"the origin with a path", base(map[string]string{"Origin": "http://127.0.0.1:6969/"}), 403, "forbidden_origin"},
		{"the null origin (sandboxed frame, redirect)", base(map[string]string{"Origin": "null"}), 403, "forbidden_origin"},
		{"an empty Origin", base(map[string]string{"Origin": " "}), 403, "forbidden_origin"},
		{"same-site (another port of this host)", base(map[string]string{"Sec-Fetch-Site": "same-site"}), 403, "forbidden_site"},
		{"cross-site fetch metadata with our origin", base(map[string]string{"Sec-Fetch-Site": "cross-site"}), 403, "forbidden_site"},
		{"no Origin and no fetch metadata with a cookie", base(nil, "Origin"), 403, "forbidden_origin"},
		{"no custom header", base(nil, RequestHeader), 403, "csrf"},
		{"custom header with another value", base(map[string]string{RequestHeader: "yes"}), 403, "csrf"},
		{"no content type", base(nil, "Content-Type"), 415, "unsupported_media_type"},
		{"text/plain", base(map[string]string{"Content-Type": "text/plain"}), 415, "unsupported_media_type"},
		{"form encoding", base(map[string]string{"Content-Type": "application/x-www-form-urlencoded"}), 415, "unsupported_media_type"},
		{"multipart", base(map[string]string{"Content-Type": "multipart/form-data; boundary=x"}), 415, "unsupported_media_type"},
		{"json-looking but not json", base(map[string]string{"Content-Type": "application/jsonp"}), 415, "unsupported_media_type"},
		{"json with a foreign parameter", base(map[string]string{"Content-Type": "application/json; boundary=x"}), 415, "unsupported_media_type"},
		{"json in latin-1", base(map[string]string{"Content-Type": "application/json; charset=iso-8859-1"}), 415, "unsupported_media_type"},
		{"a vendor json type", base(map[string]string{"Content-Type": "application/vnd.api+json"}), 415, "unsupported_media_type"},
	}
	for _, tc := range cases {
		rec := rg.do(req{method: "POST", target: "/api/echo/7", header: tc.h, body: `{"name":"n"}`})
		if rec.Code != tc.code || (tc.want != "" && errCode(rec) != tc.want) {
			t.Errorf("%s: %d %s, want %d %s", tc.name, rec.Code, errCode(rec), tc.code, tc.want)
		}
	}
	// The same matrix for the other unsafe methods.
	for _, method := range []string{"PUT", "DELETE"} {
		if rec := rg.do(req{method: method, target: "/api/echo/7", header: base(nil)}); rec.Code != 200 {
			t.Errorf("%s from the page = %d %s", method, rec.Code, rec.Body.String())
		}
		for name, h := range map[string]map[string]string{
			"cross-origin": base(map[string]string{"Origin": "http://evil.example"}), "no header": base(nil, RequestHeader), "no origin": base(nil, "Origin"),
		} {
			if rec := rg.do(req{method: method, target: "/api/echo/7", header: h}); rec.Code != 403 {
				t.Errorf("%s %s = %d, want 403", method, name, rec.Code)
			}
		}
	}
	// A preflight is never answered with permission, from anywhere.
	for _, origin := range []string{"http://evil.example", "http://127.0.0.1:3000", "null"} {
		rec := rg.do(req{method: "OPTIONS", target: "/api/echo/7", header: map[string]string{
			"Origin": origin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "x-sleipnir-web,content-type", "Cookie": cookie,
		}})
		if rec.Code != 403 || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Methods") != "" {
			t.Errorf("preflight from %s = %d, headers %v", origin, rec.Code, rec.Header())
		}
	}
	// Two Origin headers are an attack, not a request.
	hr := httptest.NewRequest("POST", "/api/echo/7", strings.NewReader(`{"name":"n"}`))
	hr.Host = testHost
	hr.Header["Origin"] = []string{testOrigin, "http://evil.example"}
	for k, v := range base(nil, "Origin") {
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, hr)
	if rec.Code != 403 {
		t.Errorf("two Origin headers = %d", rec.Code)
	}

	// A script has no Origin and no fetch metadata: it must use the bearer token, and the other rules still apply.
	script := func(extra map[string]string, drop ...string) map[string]string {
		h := mergeHeaders(rg.bearer(), map[string]string{RequestHeader: RequestHeaderValue, "Content-Type": json})
		for k, v := range extra {
			h[k] = v
		}
		for _, d := range drop {
			delete(h, d)
		}
		return h
	}
	for name, tc := range map[string]struct {
		h    map[string]string
		code int
	}{
		"bearer script":              {script(nil), 200},
		"script lacking the header":  {script(nil, RequestHeader), 403},
		"script with text/plain":     {script(map[string]string{"Content-Type": "text/plain"}), 415},
		"script claiming an origin":  {script(map[string]string{"Origin": "http://evil.example"}), 403},
		"script claiming cross-site": {script(map[string]string{"Sec-Fetch-Site": "cross-site"}), 403},
	} {
		if rec := rg.do(req{method: "POST", target: "/api/echo/7", header: tc.h, body: `{"name":"n"}`}); rec.Code != tc.code {
			t.Errorf("%s = %d, want %d", name, rec.Code, tc.code)
		}
	}
}

func TestFetchMetadataOnReads(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	get := func(target string, h map[string]string) int {
		return rg.do(req{target: target, header: mergeHeaders(map[string]string{"Cookie": cookie}, h)}).Code
	}
	for _, site := range []string{"cross-site", "same-site"} {
		// Another site, or another port of this host (same-site), cannot make the browser read the API with the cookie.
		if c := get("/api/ping", map[string]string{"Sec-Fetch-Site": site, "Sec-Fetch-Mode": "cors"}); c != 403 {
			t.Errorf("%s fetch of the API = %d", site, c)
		}
		if c := get("/api/ping", map[string]string{"Sec-Fetch-Site": site, "Sec-Fetch-Mode": "navigate"}); c != 403 {
			t.Errorf("%s navigation to the API = %d", site, c)
		}
		if c := get("/app.css", map[string]string{"Sec-Fetch-Site": site, "Sec-Fetch-Mode": "no-cors"}); c != 403 {
			t.Errorf("%s subresource request = %d", site, c)
		}
		// A link from another page to this one is a navigation and may arrive.
		if c := get("/", map[string]string{"Sec-Fetch-Site": site, "Sec-Fetch-Mode": "navigate"}); c != 200 {
			t.Errorf("%s navigation to the page = %d", site, c)
		}
	}
	for _, site := range []string{"same-origin", "none", ""} {
		if c := get("/api/ping", map[string]string{"Sec-Fetch-Site": site}); c != 200 {
			t.Errorf("%q fetch of the API = %d", site, c)
		}
	}
	// Event streams are reads too.
	if c := get("/api/events/session-1", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "cors"}); c != 403 {
		t.Errorf("same-site event stream = %d", c)
	}
}

func TestBodyLimitAndStrictDecoding(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	post := func(target, body string, mut func(*http.Request)) *httptest.ResponseRecorder {
		hr := httptest.NewRequest("POST", target, strings.NewReader(body))
		hr.Host = testHost
		for k, v := range browser(cookie) {
			hr.Header.Set(k, v)
		}
		if mut != nil {
			mut(hr)
		}
		rec := httptest.NewRecorder()
		rg.h.ServeHTTP(rec, hr)
		return rec
	}
	// The cap of a route (32 bytes here) applies by declared length, and by count when the length is not declared.
	over := `{"a":"` + strings.Repeat("x", 40) + `"}`
	if rec := post("/api/small", over, nil); rec.Code != 413 || errCode(rec) != "body_too_large" {
		t.Errorf("declared length over the cap = %d %s", rec.Code, errCode(rec))
	}
	if rec := post("/api/small", over, func(r *http.Request) { r.ContentLength = -1; r.TransferEncoding = []string{"chunked"} }); rec.Code != 413 {
		t.Errorf("chunked body over the cap = %d", rec.Code)
	}
	if rec := post("/api/small", over, func(r *http.Request) { r.ContentLength = 5 }); rec.Code != 400 && rec.Code != 413 {
		t.Errorf("a body longer than its declared length = %d", rec.Code)
	}
	if rec := post("/api/small", `{"a":"ok"}`, nil); rec.Code != 200 {
		t.Errorf("a body within the cap = %d %s", rec.Code, rec.Body.String())
	}
	// The default cap is 64 KiB.
	big := `{"name":"` + strings.Repeat("x", int(DefaultMaxBody)) + `"}`
	if rec := post("/api/echo/1", big, nil); rec.Code != 413 {
		t.Errorf("default cap = %d", rec.Code)
	}
	just := `{"name":"` + strings.Repeat("x", int(DefaultMaxBody)-12) + `"}`
	if rec := post("/api/echo/1", just, nil); rec.Code != 200 {
		t.Errorf("a body just under the default cap = %d", rec.Code)
	}

	// The decoder is strict.
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"unknown field":         {`{"name":"x","admin":true}`, 400},
		"wrong type":            {`{"name":5}`, 400},
		"null body":             {`null`, 200}, // a null decodes into nothing: handlers validate what they need
		"trailing value":        {`{"name":"x"}{"name":"y"}`, 400},
		"trailing garbage":      {`{"name":"x"} x`, 400},
		"trailing whitespace":   {"{\"name\":\"x\"}\n \t", 200},
		"empty":                 {``, 400},
		"truncated":             {`{"name":"x`, 400},
		"not an object":         {`[1]`, 400},
		"duplicate key":         {`{"name":"a","name":"b"}`, 200},
		"case-insensitive keys": {`{"NAME":"x"}`, 200},
		"deep nesting":          {strings.Repeat("[", 20000), 400},
		"invalid utf-8":         {"{\"name\":\"\xff\"}", 200}, // replaced by U+FFFD by encoding/json
		"nul in a string":       {"{\"name\":\"a\\u0000b\"}", 200},
	} {
		rec := post("/api/echo/1", tc.body, nil)
		if rec.Code != tc.code {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), tc.code)
		}
		if rec.Code == 400 && errCode(rec) != "bad_json" {
			t.Errorf("%s: code %q", name, errCode(rec))
		}
		if rec.Code >= 400 && strings.Contains(rec.Body.String(), "admin") && name != "unknown field" {
			t.Errorf("%s: the error echoes the body: %s", name, rec.Body.String())
		}
	}
	// No-body routes refuse a body, however small.
	rec := rg.do(req{method: "DELETE", target: "/api/echo/1", header: browser(cookie), body: `{}`})
	if rec.Code != 400 {
		t.Errorf("DELETE with a body on a NoBody route = %d", rec.Code)
	}
}

func TestPanicIsAGeneric500AndTheServerKeepsServing(t *testing.T) {
	rg := newRig(t, nil)
	rec := rg.get("/api/boom")
	if rec.Code != 500 || errCode(rec) != "internal" {
		t.Fatalf("panic = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "kaboom") || strings.Contains(rec.Body.String(), rg.srv.Token()) {
		t.Errorf("the response says what panicked: %s", rec.Body.String())
	}
	logged := rg.log.text()
	if !strings.Contains(logged, "panic in GET GET /api/boom") || !strings.Contains(logged, "kaboom") {
		t.Errorf("the panic is not logged: %q", logged)
	}
	if strings.Contains(logged, rg.srv.Token()) {
		t.Errorf("the token is in the log: %s", logged)
	}
	if rec := rg.get("/api/ping"); rec.Code != 200 {
		t.Errorf("after a panic = %d", rec.Code)
	}
	// A slot is not lost to a panic.
	rg = newRig(t, func(c *Config) { c.MaxInFlight = 1 })
	for i := 0; i < 3; i++ {
		if rec := rg.get("/api/boom"); rec.Code != 500 {
			t.Fatalf("panic %d = %d", i, rec.Code)
		}
	}
}

func TestPanicAfterTheResponseStartedAbortsTheConnection(t *testing.T) {
	rg := newRig(t, nil)
	rg.srv.HandleFunc("GET /api/half", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		panic("after the first write")
	}, RouteOpts{})
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler: the client must see a broken response, not a complete one", r)
		}
	}()
	rg.get("/api/half")
}

func TestRequestsInFlightAreCapped(t *testing.T) {
	rg := newRig(t, func(c *Config) { c.MaxInFlight = 2 })
	done := make(chan int, 3)
	for i := 0; i < 2; i++ {
		go func() { done <- rg.get("/api/slow").Code }()
	}
	for i := 0; i < 2; i++ {
		<-rg.entered
	}
	rec := rg.get("/api/ping")
	if rec.Code != 503 || errCode(rec) != "busy" || rec.Header().Get("Retry-After") == "" {
		t.Errorf("over the cap = %d %s", rec.Code, rec.Body.String())
	}
	checkEnvelopeHeaders(t, "busy", rec)
	close(rg.release)
	for i := 0; i < 2; i++ {
		if c := <-done; c != 200 {
			t.Errorf("held request = %d", c)
		}
	}
	if rec := rg.get("/api/ping"); rec.Code != 200 {
		t.Errorf("after the others finished = %d", rec.Code)
	}
}

func TestLogsNeverHoldCredentialsOrBodies(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	tok := rg.srv.Token()
	cookieVal := strings.TrimPrefix(cookie, CookieName+"=")
	id := rg.confirm(cookie, "danger")
	rg.post(cookie, "/api/danger", `{}`, map[string]string{ConfirmHeader: id})
	// Handlers that log what they should not are masked.
	rec := rg.do(req{target: "/api/log", header: map[string]string{"Cookie": cookie}})
	rid := rec.Header().Get("X-Request-Id")
	rg.do(req{method: "POST", target: "/api/echo/1", header: browser(cookie), body: `{"name":"super-secret-body-value"}`})
	rg.do(req{target: "/api/boom", header: map[string]string{"Cookie": cookie}})
	rg.do(req{target: "/?token=" + tok})
	rg.do(req{target: "/api/ping", header: map[string]string{"Authorization": "Bearer " + tok}})
	text := rg.log.text()
	for _, secret := range []string{tok, cookieVal, id, "super-secret-body-value"} {
		if strings.Contains(text, secret) {
			t.Errorf("the log holds %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "GET GET /api/log ["+rid+"]") || !strings.Contains(text, "[redacted]") {
		t.Errorf("the request-scoped line is not tagged or masked:\n%s", text)
	}
	if strings.Contains(text, "\nforged line") {
		t.Errorf("a newline in a message forges a log line:\n%s", text)
	}
	// Shapes are masked even when the server cannot know the value.
	rg.srv.Logf("Authorization: Bearer abcdefghijklmnopqrstuvwxyz and token=abc123456789 and %s=cookievalue12345 and X-Confirm: abcdefghijkl", CookieName)
	last := rg.log.lines[len(rg.log.lines)-1]
	for _, leaked := range []string{"abcdefghijklmnopqrstuvwxyz", "abc123456789", "cookievalue12345", "abcdefghijkl"} {
		if strings.Contains(last, leaked) {
			t.Errorf("shape not masked: %q in %q", leaked, last)
		}
	}
}

func TestHeldProviderKeysAreMaskedInLogs(t *testing.T) {
	rg := newRig(t, nil)
	heldKeyForTest(t, "SLEIPNIR_WEB_TEST_API_KEY", "sk-test-0123456789abcdefghijklmnop")
	rg.srv.Logf("the provider said sk-test-0123456789abcdefghijklmnop")
	if strings.Contains(rg.log.text(), "sk-test-0123456789") {
		t.Errorf("a held key reached the log: %s", rg.log.text())
	}
}

func TestUncleanPathsAreNotRouted(t *testing.T) {
	rg := newRig(t, nil)
	b := rg.bearer()
	for _, p := range []string{
		"//app.css", "/js/../app.css", "/./app.css", "/js//app.js", "/%2e%2e/app.css", "/js/%2e%2e/app.css", "/api/../healthz", "/api//ping", "/api/./ping",
		"/..%2fapp.css", "/js\\app.js", "/%5Capp.css", "/api/ping/..", "//evil.example/x",
	} {
		rec := rg.do(req{target: p, header: b})
		if rec.Code != 404 && rec.Code != 400 {
			t.Errorf("GET %s = %d, want 404: an unclean path must not be routed or redirected", p, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("GET %s redirects to %q", p, loc)
		}
	}
	if rec := rg.do(req{target: "/js/app.js", header: b}); rec.Code != 200 {
		t.Errorf("a clean path = %d", rec.Code)
	}
}

func TestServerStreamingResponsesKeepTheirHeaders(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	hr := httptest.NewRequest("GET", "/api/events/session-1", nil).WithContext(ctx)
	hr.Host = testHost
	hr.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, hr)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	checkEnvelopeHeaders(t, "stream", rec)
	if !strings.HasPrefix(rec.Body.String(), "retry: ") {
		t.Errorf("stream starts with %q", rec.Body.String())
	}
	if u := rg.do(req{target: "/api/events/session-1"}); u.Code != 401 {
		t.Errorf("an unauthenticated stream = %d", u.Code)
	}
	if u := rg.get("/api/events/forbidden"); u.Code != 404 {
		t.Errorf("a stream the route refuses = %d", u.Code)
	}
	_ = url.QueryEscape
}

func TestMethodFallbacksOnTheAPIAreNotRoutesOfAPath(t *testing.T) {
	rg := newRig(t, nil)
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		rg.srv.HandleFunc(m+" /api/", func(w http.ResponseWriter, r *http.Request) {
			Error(w, http.StatusNotImplemented, "not_implemented", "no")
		}, RouteOpts{})
	}
	// A path that only the fallbacks match has no methods of its own: it is unknown, not "wrong method".
	if rec := rg.get("/api/nothing"); rec.Code != 404 || errCode(rec) != "not_found" {
		t.Errorf("GET of an unknown path = %d %s", rec.Code, rec.Body.String())
	}
	h := mergeHeaders(rg.bearer(), map[string]string{RequestHeader: "1"})
	if rec := rg.do(req{method: "POST", target: "/api/nothing", header: h}); rec.Code != 501 {
		t.Errorf("POST to an unknown path reaches the fallback: %d", rec.Code)
	}
	// A path with a real route still says which methods it has.
	if rec := rg.do(req{method: "POST", target: "/api/ping", header: h}); rec.Code != 501 && rec.Code != 405 {
		t.Errorf("POST /api/ping = %d", rec.Code)
	}
	if rec := rg.get("/api/danger"); rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "POST") {
		t.Errorf("GET of a POST route = %d allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}
