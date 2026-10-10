package web

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// codeOf returns the launch code in a URL made by LaunchURL.
func codeOf(t testing.TB, u string) string {
	t.Helper()
	p, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return p.Query().Get("token")
}

func TestLaunchURLNamesTheBoundPortAndCarriesACodeNotTheToken(t *testing.T) {
	rg := newRig(t, nil)
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:41234")
	u := rg.srv.LaunchURL(addr)
	code := codeOf(t, u)
	if !strings.HasPrefix(u, "http://127.0.0.1:41234/?token=") || code == "" {
		t.Fatalf("LaunchURL = %q", u)
	}
	if strings.Contains(u, rg.srv.Token()) || code == rg.srv.Token() {
		t.Errorf("the launch URL carries the run token: %s", u)
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) < 16 {
		t.Errorf("code %q: %d bytes (%v), want at least 128 bits of base64url", code, len(raw), err)
	}
	if again := codeOf(t, rg.srv.LaunchURL(addr)); again == code {
		t.Error("two launch URLs share a code")
	}
	// The URL of the run token (the first stdout line) is unchanged.
	if !strings.HasSuffix(rg.srv.URL(addr), "?token="+rg.srv.Token()) {
		t.Errorf("URL = %s", rg.srv.URL(addr))
	}
}

func TestALaunchCodeOpensASessionOnceAndOnlyOnThePageURL(t *testing.T) {
	rg := newRig(t, nil)
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:6969")
	code := codeOf(t, rg.srv.LaunchURL(addr))

	rec := rg.do(req{target: "/?x=1&token=" + code})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?x=1" {
		t.Fatalf("first use = %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != CookieName || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteStrictMode || strings.Contains(cs[0].Value, code) {
		t.Fatalf("cookies = %+v", cs)
	}
	cookie := CookieName + "=" + cs[0].Value
	if get2(rg, cookie) != 200 {
		t.Error("the session made from a launch code does not work")
	}
	// Spent: the second use is refused, and the session it made is not affected.
	if rec := rg.do(req{target: "/?token=" + code}); rec.Code != 401 || len(rec.Result().Cookies()) != 0 {
		t.Errorf("second use = %d", rec.Code)
	}
	if get2(rg, cookie) != 200 {
		t.Error("a refused reuse ended the session")
	}
	// The run token keeps working in the URL.
	if rec := rg.do(req{target: "/?token=" + rg.srv.Token()}); rec.Code != http.StatusSeeOther {
		t.Errorf("run token = %d", rec.Code)
	}
}

func TestALaunchCodeIsNoBearerCredentialAndWorksNowhereElse(t *testing.T) {
	rg := newRig(t, nil)
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:6969")
	code := codeOf(t, rg.srv.LaunchURL(addr))
	for name, r := range map[string]req{
		"bearer":               {target: "/api/ping", header: map[string]string{"Authorization": "Bearer " + code}},
		"api query":            {target: "/api/ping?token=" + code},
		"asset query":          {target: "/app.css?token=" + code},
		"index query":          {target: "/index.html?token=" + code},
		"cookie":               {target: "/api/ping", header: map[string]string{"Cookie": CookieName + "=" + code}},
		"rotate as a script":   {method: "POST", target: "/api/auth/rotate", header: map[string]string{"Authorization": "Bearer " + code, RequestHeader: "1"}},
		"confirm as a bearer":  {method: "POST", target: "/api/confirm", header: map[string]string{"Authorization": "Bearer " + code, RequestHeader: "1", "Content-Type": "application/json"}, body: `{"scope":"x"}`},
		"as the confirm value": {method: "POST", target: "/api/danger", header: map[string]string{"Authorization": "Bearer " + code, RequestHeader: "1", "Content-Type": "application/json", ConfirmHeader: code}, body: `{}`},
	} {
		if rec := rg.do(r); rec.Code != 401 && rec.Code != 403 {
			t.Errorf("%s = %d: a launch code is for the page URL only", name, rec.Code)
		}
	}
	// None of those spent it: it still opens the page once.
	if rec := rg.do(req{target: "/?token=" + code}); rec.Code != http.StatusSeeOther {
		t.Errorf("after the refused uses = %d", rec.Code)
	}
}

func TestALaunchCodeExpiresAfterThirtySeconds(t *testing.T) {
	rg := newRig(t, nil)
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:6969")
	fresh := codeOf(t, rg.srv.LaunchURL(addr))
	stale := codeOf(t, rg.srv.LaunchURL(addr))
	rg.clock.Advance(29 * time.Second)
	if rec := rg.do(req{target: "/?token=" + fresh}); rec.Code != http.StatusSeeOther {
		t.Errorf("at 29 s = %d", rec.Code)
	}
	rg.clock.Advance(2 * time.Second)
	if rec := rg.do(req{target: "/?token=" + stale}); rec.Code != 401 {
		t.Errorf("at 31 s = %d", rec.Code)
	}
	// An expired code is gone, not merely refused.
	rg.clock.Advance(-time.Hour)
	if rec := rg.do(req{target: "/?token=" + stale}); rec.Code != 401 {
		t.Errorf("an expired code came back: %d", rec.Code)
	}
}

func TestLaunchCodesAreBoundedAndRotationEndsThem(t *testing.T) {
	rg := newRig(t, nil)
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:6969")
	var codes []string
	for i := 0; i < maxLaunch+2; i++ {
		rg.clock.Advance(time.Millisecond)
		codes = append(codes, codeOf(t, rg.srv.LaunchURL(addr)))
	}
	rg.auth().mu.Lock()
	n := len(rg.auth().launch)
	rg.auth().mu.Unlock()
	if n != maxLaunch {
		t.Errorf("%d codes outstanding, want %d", n, maxLaunch)
	}
	if rec := rg.do(req{target: "/?token=" + codes[0]}); rec.Code != 401 {
		t.Errorf("the oldest code, forgotten = %d", rec.Code)
	}
	if rec := rg.do(req{target: "/?token=" + codes[len(codes)-1]}); rec.Code != http.StatusSeeOther {
		t.Errorf("the newest code = %d", rec.Code)
	}
	// Rotating the token ends the codes with it.
	cookie := rg.login()
	pending := codeOf(t, rg.srv.LaunchURL(addr))
	if rec := rg.post(cookie, "/api/auth/rotate", "", nil); rec.Code != 200 {
		t.Fatalf("rotate = %d", rec.Code)
	}
	if rec := rg.do(req{target: "/?token=" + pending}); rec.Code != 401 {
		t.Errorf("a launch code survived the rotation: %d", rec.Code)
	}
}

func TestWrongLaunchCodesCountTowardsTheThrottle(t *testing.T) {
	rg := newRig(t, nil)
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:6969")
	code := codeOf(t, rg.srv.LaunchURL(addr))
	rg.do(req{target: "/?token=" + code}) // spent
	for i := 0; i < defaultAuthFailures; i++ {
		rg.do(req{target: "/?token=" + code}) // each reuse is a failed attempt
	}
	if rec := rg.do(req{target: "/?token=" + codeOf(t, rg.srv.LaunchURL(addr))}); rec.Code != 429 {
		t.Errorf("after %d failures a good code is not evaluated: %d", defaultAuthFailures, rec.Code)
	}
}

func (rg *rig) auth() *authority { return rg.srv.auth }

func TestErrorDetailAddsStructuredData(t *testing.T) {
	type challenge struct {
		Dir     string `json:"dir"`
		Confirm string `json:"confirm"`
	}
	rec := httptest.NewRecorder()
	ErrorDetail(rec, 409, "trust_required", "trust this project first", challenge{Dir: "/p/<x>", Confirm: "c1"})
	if rec.Code != 409 || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("response = %d %v", rec.Code, rec.Header())
	}
	body := rec.Body.String()
	if strings.ContainsAny(body, "<>&") {
		t.Errorf("the detail is not escaped: %s", body)
	}
	var got struct {
		Error  string
		Code   string
		Detail challenge
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Error != "trust this project first" || got.Code != "trust_required" || got.Detail.Dir != "/p/<x>" || got.Detail.Confirm != "c1" {
		t.Errorf("body = %s (%v)", body, err)
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("Content-Length %s for %d bytes", rec.Header().Get("Content-Length"), rec.Body.Len())
	}

	// A map works as well, and is what retryAfterMs is sent as.
	rec = httptest.NewRecorder()
	ErrorDetail(rec, 409, "too_soon", "wait a moment", map[string]int{"retryAfterMs": 350})
	if !strings.Contains(rec.Body.String(), `"detail":{"retryAfterMs":350}`) {
		t.Errorf("body = %s", rec.Body.String())
	}

	// The same body as the error type the route packages return.
	e := wire.Error{Msg: "wait a moment", Code: "too_soon", Detail: map[string]int{"retryAfterMs": 350}}
	want, _ := json.Marshal(e)
	if strings.TrimSpace(rec.Body.String()) != string(want) {
		t.Errorf("ErrorDetail = %s, wire.Error = %s", rec.Body.String(), want)
	}
}

func TestErrorDetailWithoutOrWithBadDetailIsThePlainError(t *testing.T) {
	plain := httptest.NewRecorder()
	Error(plain, 400, "bad_request", "no")
	for name, d := range map[string]any{"nil": nil, "unencodable": make(chan int), "func": func() {}} {
		rec := httptest.NewRecorder()
		ErrorDetail(rec, 400, "bad_request", "no", d)
		if rec.Code != 400 || rec.Body.String() != plain.Body.String() {
			t.Errorf("%s: %d %q, want %q", name, rec.Code, rec.Body.String(), plain.Body.String())
		}
	}
	// The message is bounded as in Error.
	rec := httptest.NewRecorder()
	ErrorDetail(rec, 400, "x", strings.Repeat("a", 2000), map[string]int{"n": 1})
	var e struct{ Error string }
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if len(e.Error) > maxErrorMessage+3 {
		t.Errorf("message of %d bytes", len(e.Error))
	}
}

func TestErrorDetailThroughTheEnvelopeKeepsTheHeaders(t *testing.T) {
	rg := newRig(t, nil)
	rg.srv.HandleFunc("GET /api/challenge", func(w http.ResponseWriter, r *http.Request) {
		ErrorDetail(w, 409, "trust_required", "trust this project first", map[string]string{"confirm": rg.srv.IssueConfirm(r, "trust:abc")})
	}, RouteOpts{})
	rec := rg.get("/api/challenge")
	if rec.Code != 409 || errCode(rec) != "trust_required" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	checkEnvelopeHeaders(t, "detail", rec)
}
