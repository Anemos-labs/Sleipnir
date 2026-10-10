package web

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTokenIsRandomAndLongEnough(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		rg := newRig(t, nil)
		tok := rg.srv.Token()
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(raw) < 16 {
			t.Fatalf("token %q: %d bytes (%v), want at least 128 bits of base64url", tok, len(raw), err)
		}
		if len(raw) != 32 {
			t.Errorf("token has %d bytes of randomness, want 32", len(raw))
		}
		if seen[tok] {
			t.Fatalf("token %q was generated twice", tok)
		}
		seen[tok] = true
		if strings.ContainsAny(tok, "+/=") || url.QueryEscape(tok) != tok {
			t.Errorf("token %q needs escaping in a URL", tok)
		}
	}
}

func TestTheTokenCannotBeChosenFromOutside(t *testing.T) {
	// There is no Config field, flag or environment variable for it: Config is the whole surface.
	t.Setenv("SLEIPNIR_WEB_TOKEN", "from-the-environment")
	t.Setenv("SLEIPNIR_TOKEN", "from-the-environment")
	rg := newRig(t, nil)
	if rg.srv.Token() == "from-the-environment" {
		t.Fatal("the token came from the environment")
	}
	if rec := rg.do(req{target: "/api/ping", header: map[string]string{"Authorization": "Bearer from-the-environment"}}); rec.Code != 401 {
		t.Errorf("an environment token was accepted: %d", rec.Code)
	}
}

func TestTokenMatrix(t *testing.T) {
	rg := newRig(t, nil)
	tok := rg.srv.Token()
	bearer := func(v string) map[string]string { return map[string]string{"Authorization": v} }
	cases := []struct {
		name string
		h    map[string]string
		want int
	}{
		{"no credential", nil, 401},
		{"right token", bearer("Bearer " + tok), 200},
		{"scheme is case-insensitive", bearer("bearer " + tok), 200},
		{"surrounding space", bearer("Bearer  " + tok + " "), 200},
		{"wrong token", bearer("Bearer wrong"), 401},
		{"prefix of the token", bearer("Bearer " + tok[:len(tok)-1]), 401},
		{"token plus suffix", bearer("Bearer " + tok + "x"), 401},
		{"upper-cased token", bearer("Bearer " + strings.ToUpper(tok)), 401},
		{"empty token", bearer("Bearer "), 401},
		{"basic auth", bearer("Basic " + tok), 401},
		{"no scheme", bearer(tok), 401},
		{"huge token", bearer("Bearer " + strings.Repeat("a", 60000)), 401},
		{"token as cookie value", map[string]string{"Cookie": CookieName + "=" + tok}, 401},
		{"empty cookie", map[string]string{"Cookie": CookieName + "="}, 401},
		{"made-up cookie", map[string]string{"Cookie": CookieName + "=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, 401},
		{"another cookie name", map[string]string{"Cookie": "other=" + tok}, 401},
		{"token in a header of its own", map[string]string{"X-Token": tok, "X-Sleipnir-Token": tok}, 401},
		{"token in the query of an API call", nil, 401},
	}
	for _, c := range cases {
		target := "/api/ping"
		if strings.Contains(c.name, "query") {
			target += "?token=" + tok
		}
		rec := rg.do(req{target: target, header: c.h})
		if rec.Code != c.want {
			t.Errorf("%s = %d, want %d", c.name, rec.Code, c.want)
		}
		if rec.Code == 401 {
			if errCode(rec) != "unauthenticated" || rec.Header().Get("WWW-Authenticate") == "" || strings.Contains(rec.Body.String(), tok) {
				t.Errorf("%s: 401 body %q headers %v", c.name, rec.Body.String(), rec.Header())
			}
		}
	}
	// Everything but /healthz needs a credential, the page and its assets included.
	for _, p := range []string{"/", "/index.html", "/app.css", "/js/app.js", "/api/ping", "/api/echo/1", "/api/events/x", "/nope", "/api/nope", "/some/spa/route"} {
		if rec := rg.do(req{target: p}); rec.Code != 401 {
			t.Errorf("GET %s without a credential = %d", p, rec.Code)
		}
	}
	if rec := rg.do(req{target: "/healthz"}); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Errorf("healthz = %d %s: it must say nothing else", rec.Code, rec.Body.String())
	}
	if rec := rg.do(req{method: "POST", target: "/healthz", header: browser("")}); rec.Code == 200 {
		t.Errorf("healthz answers POST: %d", rec.Code)
	}
}

func TestBrowserSignInPage(t *testing.T) {
	rg := newRig(t, nil)
	rec := rg.do(req{target: "/", header: map[string]string{"Accept": "text/html,application/xhtml+xml"}})
	if rec.Code != 401 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("a browser without a session = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	if strings.Contains(body, rg.srv.Token()) || strings.Contains(strings.ToLower(body), "<script") || strings.Contains(strings.ToLower(body), "<style") ||
		strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Errorf("the sign-in page must be inert and say nothing: %s", body)
	}
	// An API call from the page gets JSON even if it accepts HTML.
	rec = rg.do(req{target: "/api/ping", header: map[string]string{"Accept": "text/html", RequestHeader: RequestHeaderValue}})
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("API 401 content type = %s", rec.Header().Get("Content-Type"))
	}
}

func TestTheURLTokenBecomesACookieAndLeavesTheAddressBar(t *testing.T) {
	rg := newRig(t, nil)
	tok := rg.srv.Token()
	rec := rg.do(req{target: "/?x=1&token=" + tok + "&y=%3Cb%3E"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("token in the URL = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil || u.Host != "" || u.Scheme != "" || u.Path != "/" || strings.Contains(loc, "token") || strings.Contains(loc, tok) {
		t.Errorf("redirect to %q: the token must go and the target must be the root of this origin", loc)
	}
	if q := u.Query(); q.Get("x") != "1" || q.Get("y") != "<b>" || len(q) != 2 {
		t.Errorf("other parameters are kept as data: %v", q)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies = %+v", cs)
	}
	c := cs[0]
	if c.Name != CookieName || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure || c.Domain != "" {
		t.Errorf("cookie = %+v", c)
	}
	if c.MaxAge != 24*3600 || c.Expires.IsZero() {
		t.Errorf("cookie lifetime: max-age %d expires %v", c.MaxAge, c.Expires)
	}
	if strings.Contains(c.Value, tok) || strings.Contains(tok, c.Value) || len(c.Value) < 40 {
		t.Errorf("the cookie value %q must be an id unrelated to the token", c.Value)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("the redirect is cacheable: %q", rec.Header().Get("Cache-Control"))
	}
	// Each exchange is a session of its own.
	rec2 := rg.do(req{target: "/?token=" + tok})
	if c2 := rec2.Result().Cookies()[0]; c2.Value == c.Value {
		t.Error("two exchanges produced the same session")
	}
	// The cookie opens the page and the API; the bearer header is not needed any more.
	cookie := CookieName + "=" + c.Value
	for _, p := range []string{"/", "/api/ping", "/app.css"} {
		if r := rg.do(req{target: p, header: map[string]string{"Cookie": cookie}}); r.Code != 200 {
			t.Errorf("GET %s with the cookie = %d", p, r.Code)
		}
	}
	// Over TLS the cookie is Secure.
	rec = rg.do(req{target: "/?token=" + tok, tls: true})
	if c := rec.Result().Cookies()[0]; !c.Secure {
		t.Error("the cookie is not Secure over TLS")
	}
	// A wrong token is a 401 and no cookie; the HEAD form does not start a session either.
	for _, target := range []string{"/?token=wrong", "/?token=", "/?token=" + tok + "x", "/?token=%00"} {
		rec := rg.do(req{target: target})
		if rec.Code != 401 || len(rec.Result().Cookies()) != 0 {
			t.Errorf("GET %s = %d with %d cookies", target, rec.Code, len(rec.Result().Cookies()))
		}
	}
	// A stale token in the URL does not hide a good cookie (a bookmark from an earlier run).
	if r := rg.do(req{target: "/?token=stale", header: map[string]string{"Cookie": cookie}}); r.Code != 200 {
		t.Errorf("stale token with a good cookie = %d", r.Code)
	}
}

func TestTokenInTheURLIsAcceptedOnTheRootOnly(t *testing.T) {
	rg := newRig(t, nil)
	tok := rg.srv.Token()
	for _, p := range []string{
		"/api/ping", "/api/echo/1", "/api/events/x", "/api/auth/logout", "/index.html", "/app.css", "/js/app.js", "/some/route", "/healthz-not", "/api/",
		"//",
	} {
		rec := rg.do(req{target: p + "?token=" + tok})
		if rec.Code == 200 || rec.Code == 303 || len(rec.Result().Cookies()) != 0 {
			t.Errorf("GET %s?token=... = %d with cookies %v: the token is accepted on / only", p, rec.Code, rec.Result().Cookies())
		}
	}
	// The token as a form field or in a POST body is not a credential at all.
	rec := rg.do(req{method: "POST", target: "/api/echo/1?token=" + tok, header: map[string]string{RequestHeader: RequestHeaderValue, "Content-Type": "application/json"}, body: `{"token":"` + tok + `"}`})
	if rec.Code != 401 {
		t.Errorf("POST with the token in the URL and body = %d", rec.Code)
	}
}

// The token URL becomes a redirect. Whatever the request line carries, the Location stays on this origin: browsers read "//host" and "/\host" as another site.
func TestTokenRedirectNeverLeavesThisOrigin(t *testing.T) {
	rg := newRig(t, nil)
	tok := rg.srv.Token()
	check := func(target string) {
		t.Helper()
		rec := rg.do(req{target: target})
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther {
			if loc != "" {
				t.Errorf("%s: %d redirects to %q", target, rec.Code, loc)
			}
			return
		}
		u, err := url.Parse(loc)
		if err != nil || u.Host != "" || u.Scheme != "" || u.Opaque != "" || u.Path != "/" || !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") || strings.ContainsAny(loc, "\\ \r\n") {
			t.Errorf("%s redirected to %q", target, loc)
		}
		if strings.Contains(loc, tok) {
			t.Errorf("%s redirected to %q: the token stays in the address bar", target, loc)
		}
	}
	for _, q := range []string{
		"?token=" + tok + "&next=//evil.example", "?token=" + tok + "&redirect=https://evil.example/", "?next=//evil.example&token=" + tok,
		"?token=" + tok + "&a=%0d%0aSet-Cookie:x=y", "?token=" + tok + "&url=/\\evil.example", "?token=" + tok + "&token=" + tok, "?token=" + tok + "&" + strings.Repeat("a=b&", 3000),
	} {
		check("/" + q)
	}
	// Path tricks never reach the exchange: only the exact path "/" does.
	for _, p := range []string{"//evil.example/x", "///evil.example", "/\\evil.example", "/%5Cevil.example", "/%2F%2Fevil.example", "/../../evil.example", "/index.html", "/app.css", "/nope"} {
		check(p + "?keep=1&token=" + tok)
	}
}

func TestSessionsExpireAndAreBounded(t *testing.T) {
	rg := newRig(t, func(c *Config) { c.SessionTTL = time.Hour })
	cookie := rg.login()
	get := func(c string) int {
		return rg.do(req{target: "/api/ping", header: map[string]string{"Cookie": c}}).Code
	}
	if get(cookie) != 200 {
		t.Fatal("fresh session refused")
	}
	rg.clock.Advance(59 * time.Minute)
	if get(cookie) != 200 {
		t.Error("session ended early")
	}
	rg.clock.Advance(2 * time.Minute)
	if get(cookie) != 401 {
		t.Error("session did not expire (the lifetime is absolute, not refreshed by use)")
	}
	if get(cookie) != 401 {
		t.Error("expired session came back")
	}

	// There are at most 32 sessions; the oldest ends first.
	rg = newRig(t, nil)
	var cookies []string
	for i := 0; i < maxSessions+3; i++ {
		rg.clock.Advance(time.Second)
		cookies = append(cookies, rg.login())
	}
	for i, c := range cookies {
		want := 200
		if i < 3 {
			want = 401
		}
		if got := get2(rg, c); got != want {
			t.Errorf("session %d = %d, want %d", i, got, want)
		}
	}
}

// get2 is a GET of the API with a cookie.
func get2(rg *rig, cookie string) int {
	return rg.do(req{target: "/api/ping", header: map[string]string{"Cookie": cookie}}).Code
}

func TestCookieTossingCannotLockTheSessionOut(t *testing.T) {
	// Cookies are not isolated by port: another page on this host can send a cookie of the same name.
	rg := newRig(t, nil)
	cookie := rg.login()
	rec := rg.do(req{target: "/api/ping", header: map[string]string{"Cookie": CookieName + "=junk; " + cookie + "; " + CookieName + "=junk2"}})
	if rec.Code != 200 {
		t.Errorf("a junk cookie in front of the good one = %d", rec.Code)
	}
}

func TestFailedTokensAreThrottled(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	tok := rg.srv.Token()
	for i := 0; i < defaultAuthFailures; i++ {
		if rec := rg.do(req{target: "/api/ping", header: map[string]string{"Authorization": "Bearer wrong"}}); rec.Code != 401 {
			t.Fatalf("failure %d = %d", i, rec.Code)
		}
	}
	// The window is full: attempts are no longer evaluated, not even a right token, so it cannot be guessed at line rate.
	for _, r := range []req{
		{target: "/api/ping", header: map[string]string{"Authorization": "Bearer " + tok}},
		{target: "/api/ping", header: map[string]string{"Authorization": "Bearer wrong"}},
		{target: "/?token=" + tok},
	} {
		rec := rg.do(r)
		if rec.Code != 429 || errCode(rec) != "rate_limited" || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%v while throttled = %d %s", r, rec.Code, rec.Body.String())
		}
		checkEnvelopeHeaders(t, "throttled", rec)
	}
	// A session that already exists is a different credential and is not slowed down by an attacker's failures.
	if get2(rg, cookie) != 200 {
		t.Error("an existing session was throttled")
	}
	if get2(rg, CookieName+"=wrong") != 401 {
		t.Error("a bad cookie is a plain 401")
	}
	rg.clock.Advance(defaultAuthWindow + time.Second)
	if rec := rg.do(req{target: "/api/ping", header: map[string]string{"Authorization": "Bearer " + tok}}); rec.Code != 200 {
		t.Errorf("after the window = %d", rec.Code)
	}
	// Stale cookies (every restart leaves some) never count as failed attempts.
	rg = newRig(t, nil)
	for i := 0; i < 5*defaultAuthFailures; i++ {
		get2(rg, CookieName+"=stale")
	}
	if rec := rg.do(req{target: "/api/ping", header: rg.bearer()}); rec.Code != 200 {
		t.Errorf("stale cookies throttled the token: %d", rec.Code)
	}
	// Failures from a wrong ?token= count too.
	rg = newRig(t, nil)
	for i := 0; i < defaultAuthFailures; i++ {
		rg.do(req{target: "/?token=wrong"})
	}
	if rec := rg.do(req{target: "/?token=" + rg.srv.Token()}); rec.Code != 429 {
		t.Errorf("exchange after %d wrong tokens = %d", defaultAuthFailures, rec.Code)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	rg := newRig(t, nil)
	a, b := rg.login(), rg.login()
	rec := rg.post(a, "/api/auth/logout", "", nil)
	if rec.Code != 200 {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != CookieName || cs[0].MaxAge >= 0 || cs[0].Value != "" {
		t.Errorf("logout must clear the cookie: %+v", cs)
	}
	if get2(rg, a) != 401 {
		t.Error("the session still works after logout")
	}
	if get2(rg, b) != 200 {
		t.Error("logout ended another session")
	}
	// It is a POST like any other: no cross-origin logout.
	if rec := rg.do(req{method: "POST", target: "/api/auth/logout", header: map[string]string{"Cookie": b, "Origin": "http://evil.example", RequestHeader: "1"}}); rec.Code != 403 {
		t.Errorf("cross-origin logout = %d", rec.Code)
	}
	if rec := rg.do(req{method: "GET", target: "/api/auth/logout", header: map[string]string{"Cookie": b}}); rec.Code == 200 {
		t.Errorf("logout by GET = %d", rec.Code)
	}
	if get2(rg, b) != 200 {
		t.Error("a refused logout ended the session")
	}
}

func TestRotateEndsEveryOtherSessionAndTheOldToken(t *testing.T) {
	rg := newRig(t, nil)
	old := rg.srv.Token()
	a, b := rg.login(), rg.login()
	rec := rg.post(a, "/api/auth/rotate", "", nil)
	if rec.Code != 200 {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), rg.srv.Token()) || strings.Contains(rec.Body.String(), old) {
		t.Error("the response discloses a token")
	}
	if rg.srv.Token() == old {
		t.Error("the token did not change")
	}
	var fresh string
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			fresh = CookieName + "=" + c.Value
		}
	}
	if fresh == "" || get2(rg, fresh) != 200 {
		t.Error("the caller must stay signed in through a new cookie")
	}
	if get2(rg, a) != 401 || get2(rg, b) != 401 {
		t.Error("an old session survived the rotation")
	}
	if rec := rg.do(req{target: "/api/ping", header: map[string]string{"Authorization": "Bearer " + old}}); rec.Code != 401 {
		t.Errorf("the old token = %d", rec.Code)
	}
	if rec := rg.do(req{target: "/?token=" + old}); rec.Code != 401 {
		t.Errorf("the old token in the URL = %d", rec.Code)
	}
	// A bearer caller would be locked out by it, so it cannot ask.
	h := mergeHeaders(rg.bearer(), map[string]string{RequestHeader: "1"})
	if rec := rg.do(req{method: "POST", target: "/api/auth/rotate", header: h}); rec.Code != 403 {
		t.Errorf("rotate by bearer = %d", rec.Code)
	}
}

func TestConfirmationFlow(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	danger := func(c string, id string) int {
		extra := map[string]string{}
		if id != "" {
			extra[ConfirmHeader] = id
		}
		return rg.post(c, "/api/danger", `{}`, extra).Code
	}
	// Without an id the route answers 428 and says which scope to ask for.
	rec := rg.post(cookie, "/api/danger", `{}`, nil)
	if rec.Code != 428 || errCode(rec) != "confirm_required" || rec.Header().Get("X-Confirm-Scope") != "danger" {
		t.Errorf("no confirmation = %d %s scope %q", rec.Code, errCode(rec), rec.Header().Get("X-Confirm-Scope"))
	}
	id := rg.confirm(cookie, "danger")
	if len(id) < 24 {
		t.Errorf("confirmation id %q is short", id)
	}
	if danger(cookie, id) != 200 {
		t.Error("a good confirmation was refused")
	}
	// Single use.
	rec = rg.post(cookie, "/api/danger", `{}`, map[string]string{ConfirmHeader: id})
	if rec.Code != 403 || errCode(rec) != "confirm_invalid" {
		t.Errorf("reused confirmation = %d %s", rec.Code, errCode(rec))
	}
	// Scope: an id for one thing does not authorise another, and the attempt spends it.
	other := rg.confirm(cookie, "something-else")
	if danger(cookie, other) != 403 {
		t.Error("an id for another scope was accepted")
	}
	if rec := rg.post(cookie, "/api/cond", `{"mode":"yolo"}`, map[string]string{ConfirmHeader: other}); rec.Code != 403 {
		t.Errorf("a spent id was accepted again: %d", rec.Code)
	}
	// Session binding: another session or the bearer token cannot use it.
	other2 := rg.login()
	id = rg.confirm(cookie, "danger")
	if danger(other2, id) != 403 {
		t.Error("a confirmation was used by a different session")
	}
	id = rg.confirm(cookie, "danger")
	rec = rg.do(req{method: "POST", target: "/api/danger", header: mergeHeaders(rg.bearer(), map[string]string{RequestHeader: "1", "Content-Type": "application/json", ConfirmHeader: id}), body: `{}`})
	if rec.Code != 403 {
		t.Errorf("a confirmation was used by the bearer token: %d", rec.Code)
	}
	// Expiry.
	id = rg.confirm(cookie, "danger")
	rg.clock.Advance(59 * time.Second)
	idLate := rg.confirm(cookie, "danger")
	rg.clock.Advance(2 * time.Second)
	if danger(cookie, id) != 403 {
		t.Error("an expired confirmation was accepted")
	}
	if danger(cookie, idLate) != 200 {
		t.Error("a confirmation within its minute was refused")
	}
	// Garbage.
	for _, bad := range []string{"x", strings.Repeat("a", 5000), "AAAAAAAAAAAAAAAAAAAAAAAAAAAA", "../.."} {
		if danger(cookie, bad) != 403 {
			t.Errorf("garbage confirmation %q was accepted", bad)
		}
	}
	// The default scope of a route is its pattern.
	id = rg.confirm(cookie, "POST /api/default-scope")
	if rg.post(cookie, "/api/default-scope", `{}`, map[string]string{ConfirmHeader: id}).Code != 200 {
		t.Error("the default scope is not the pattern")
	}
	// A handler can ask for a confirmation only for some inputs.
	if rec := rg.post(cookie, "/api/cond", `{"mode":"default"}`, nil); rec.Code != 200 {
		t.Errorf("conditional route without the condition = %d", rec.Code)
	}
	if rec := rg.post(cookie, "/api/cond", `{"mode":"yolo"}`, nil); rec.Code != 428 {
		t.Errorf("conditional route with the condition = %d", rec.Code)
	}
	id = rg.confirm(cookie, "mode:yolo")
	if rec := rg.post(cookie, "/api/cond", `{"mode":"yolo"}`, map[string]string{ConfirmHeader: id}); rec.Code != 200 {
		t.Errorf("conditional route with a confirmation = %d", rec.Code)
	}
	// Logout takes the session's confirmations with it.
	id = rg.confirm(other2, "danger")
	rg.post(other2, "/api/auth/logout", "", nil)
	third := rg.login()
	if danger(third, id) != 403 {
		t.Error("a confirmation survived its session")
	}
}

func TestConfirmationEndpointValidatesAndBounds(t *testing.T) {
	rg := newRig(t, nil)
	cookie := rg.login()
	for name, body := range map[string]string{
		"missing scope": `{}`, "empty scope": `{"scope":""}`, "long scope": `{"scope":"` + strings.Repeat("a", 121) + `"}`, "control char": `{"scope":"a\nb"}`,
		"non-ascii": `{"scope":"café"}`, "extra field": `{"scope":"a","ttl":9999}`, "not json": `scope=a`,
	} {
		if rec := rg.post(cookie, "/api/confirm", body, nil); rec.Code != 400 {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
	if rec := rg.do(req{method: "POST", target: "/api/confirm", header: map[string]string{RequestHeader: "1", "Content-Type": "application/json"}, body: `{"scope":"a"}`}); rec.Code != 401 {
		t.Errorf("unauthenticated confirm = %d", rec.Code)
	}
	var out struct {
		ID        string `json:"id"`
		Scope     string `json:"scope"`
		ExpiresIn int    `json:"expires_in"`
	}
	mustJSON(t, rg.post(cookie, "/api/confirm", `{"scope":"trust add"}`, nil), &out)
	if out.Scope != "trust add" || out.ExpiresIn != 60 || out.ID == "" {
		t.Errorf("confirm response = %+v", out)
	}
	// Outstanding ids are bounded per session.
	n := 0
	for i := 0; i < 3*maxConfirmsPerPrincipal; i++ {
		if rg.post(cookie, "/api/confirm", `{"scope":"a"}`, nil).Code == 200 {
			n++
		}
	}
	if n >= 3*maxConfirmsPerPrincipal || n > maxConfirmsPerPrincipal {
		t.Errorf("%d confirmations were issued to one session", n)
	}
	// IssueConfirm refuses a request that is not authenticated or a bad scope.
	if id := rg.srv.IssueConfirm(newBareRequest(), "x"); id != "" {
		t.Errorf("IssueConfirm for a request outside the envelope = %q", id)
	}
}

// newBareRequest is a request that did not come through the envelope.
func newBareRequest() *http.Request {
	r, _ := http.NewRequest("GET", "/", nil)
	return r
}
