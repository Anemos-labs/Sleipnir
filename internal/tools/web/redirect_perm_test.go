package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// hopPerm is a perm.Requester that remembers what it was asked and decides with
// another one.
type hopPerm struct {
	mu   sync.Mutex
	reqs []perm.Request
	next perm.Requester
}

// Check implements perm.Requester.
func (r *hopPerm) Check(ctx context.Context, q perm.Request) perm.Decision {
	r.mu.Lock()
	r.reqs = append(r.reqs, q)
	r.mu.Unlock()
	return r.next.Check(ctx, q)
}

// urls lists the URL of every request it was asked about.
func (r *hopPerm) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, q := range r.reqs {
		var in struct{ URL string }
		_ = json.Unmarshal(q.Input, &in)
		out = append(out, in.URL)
	}
	return out
}

// A redirect to another host is a fetch of that host: the permission engine judges it
// as it would judge a first fetch of it, before it is followed.
func TestCrossHostRedirectAsksTheEngine(t *testing.T) {
	target, targetHits := serve(t, "text/plain", "target content")
	var to atomic.Value
	origin, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			http.Redirect(w, r, "/landing", http.StatusFound)
			return
		}
		if r.URL.Path == "/landing" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("same host"))
			return
		}
		http.Redirect(w, r, to.Load().(string), http.StatusFound)
	})
	h := newHarness(t, Config{AllowHosts: []string{"*.test"}})
	h.f.guard.lookup = staticLookup(map[string]string{"a.test": "127.0.0.1", "www.a.test": "127.0.0.1", "b.test": "127.0.0.1"})
	start := "http://a.test:" + tcpPort(origin) + "/start"
	engine := func(allow ...string) *perm.Engine {
		e, err := perm.NewEngine(perm.Config{Root: t.TempDir(), Home: t.TempDir(), Allow: allow})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	run := func(p perm.Requester, url string) (string, bool) {
		env := h.env("agent")
		env.Perm = p
		res := h.run(env, map[string]any{"url": url})
		return res.Text, res.IsError
	}

	// Only a.test is allowed and nobody can be asked: the hop to b.test is refused, and
	// b.test is never contacted.
	to.Store("http://b.test:" + tcpPort(target) + "/landing")
	before := targetHits.Load()
	rec := &hopPerm{next: engine("WebFetch(domain:a.test)")}
	text, isErr := run(rec, start)
	if !isErr || !strings.Contains(text, "b.test") || !strings.Contains(text, "redirect") || targetHits.Load() != before {
		t.Fatalf("redirect to an unapproved host: err=%v %q (target hits %d)", isErr, text, targetHits.Load()-before)
	}
	if u := rec.urls(); len(u) != 2 || u[1] != "http://b.test:"+tcpPort(target)+"/landing" {
		t.Fatalf("the engine was asked about %v", u)
	}

	// Both hosts allowed: followed.
	text, isErr = run(engine("WebFetch(domain:a.test)", "WebFetch(domain:b.test)"), start+"?x=1")
	if isErr || !strings.Contains(text, "target content") {
		t.Fatalf("redirect to an approved host: %v %q", isErr, text)
	}

	// A hop that stays on the site is never a question: it is checked for refusals only.
	// The rule allows the start URL alone, so a question about the hop would be refused.
	for _, same := range []string{"/landing", "http://www.a.test:" + tcpPort(origin) + "/landing", "http://A.TEST.:" + tcpPort(origin) + "/landing"} {
		to.Store(same)
		rec := &hopPerm{next: engine("WebFetch(http://a.test:" + tcpPort(origin) + "/start*)")}
		text, isErr := run(rec, start+"?same="+same)
		if isErr || !strings.Contains(text, "same host") || len(rec.urls()) != 2 {
			t.Fatalf("redirect to %s: %v %q, asked %v", same, isErr, text, rec.urls())
		}
	}
}

// siteFixture is an origin on a.test whose /start redirects where the test says, a
// second server for a port change, and a runner with a permission engine of given rules.
type siteFixture struct {
	t                  *testing.T
	h                  *harness
	origin, other      string // ports
	originHits, others *atomic.Int32
	to                 atomic.Value
	page               string
}

func newSiteFixture(t *testing.T) *siteFixture {
	sf := &siteFixture{t: t, page: "same site"}
	o, oh := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, sf.to.Load().(string), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(sf.page))
	})
	x, xh := serve(t, "text/plain", "other port")
	sf.origin, sf.originHits, sf.other, sf.others = tcpPort(o), oh, tcpPort(x), xh
	sf.h = newHarness(t, Config{AllowHosts: []string{"*.test"}})
	sf.h.f.guard.lookup = staticLookup(map[string]string{"a.test": "127.0.0.1", "www.a.test": "127.0.0.1"})
	return sf
}

// fetch runs web_fetch for url under an engine with the rules ("Allow X" / "Deny X").
func (sf *siteFixture) fetch(url string, extra map[string]any, rules ...string) *tools.Result {
	cfg := perm.Config{Root: sf.t.TempDir(), Home: sf.t.TempDir()}
	for _, r := range rules {
		action, pattern, _ := strings.Cut(r, " ")
		if action == "Deny" {
			cfg.Deny = append(cfg.Deny, "WebFetch("+pattern+")")
		} else {
			cfg.Allow = append(cfg.Allow, "WebFetch("+pattern+")")
		}
	}
	e, err := perm.NewEngine(cfg)
	if err != nil {
		sf.t.Fatal(err)
	}
	env := sf.h.env("agent")
	env.Perm = e
	in := map[string]any{"url": url}
	for k, v := range extra {
		in[k] = v
	}
	return sf.h.run(env, in)
}

// A redirect that stays on the site still meets a rule that refuses where it leads, and
// one that changes the port or goes down from https to http leaves the site: it is
// judged as a first fetch of its URL.
func TestRedirectsOnTheSiteHonourDenyRules(t *testing.T) {
	sf := newSiteFixture(t)
	start := "http://a.test:" + sf.origin + "/start"

	// www.a.test is denied although a.test is allowed
	sf.to.Store("http://www.a.test:" + sf.origin + "/landing")
	res := sf.fetch(start, nil, "Allow domain:a.test", "Deny domain:www.a.test")
	if !res.IsError || !strings.Contains(res.Text, "www.a.test") || !strings.Contains(res.Text, "not allowed") {
		t.Fatalf("a redirect to a denied www host was followed: %q", res.Text)
	}
	// a URL rule that denies a path on the same host
	sf.to.Store("/admin/x")
	res = sf.fetch(start+"?p", nil, "Allow domain:a.test", "Deny http://a.test:"+sf.origin+"/admin/*")
	if !res.IsError || !strings.Contains(res.Text, "/admin/x") {
		t.Fatalf("a redirect to a denied path was followed: %q", res.Text)
	}

	// another port: allowed by a URL rule for the first port only, so it is a question,
	// and nobody can be asked; the other server is never contacted
	sf.to.Store("http://a.test:" + sf.other + "/landing")
	before := sf.others.Load()
	res = sf.fetch(start+"?port", nil, "Allow http://a.test:"+sf.origin+"/*")
	if !res.IsError || sf.others.Load() != before {
		t.Fatalf("a redirect to another port was followed: %q", res.Text)
	}
	// the refusal is that nobody can be asked: fetching the URL itself would meet it too
	if strings.Contains(res.Text, "fetch that URL itself") {
		t.Fatalf("the refusal suggests a fetch that would be refused as well: %q", res.Text)
	}

	// the same redirect with a rule for both ports is followed
	res = sf.fetch(start+"?both", nil, "Allow http://a.test:"+sf.origin+"/*", "Allow http://a.test:"+sf.other+"/*")
	if res.IsError || !strings.Contains(res.Text, "other port") {
		t.Fatalf("an allowed redirect to another port: %q", res.Text)
	}
}

// What leaves the site and what stays on it.
func TestSameSite(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"https://a.test/x", "https://a.test/y", true},
		{"https://a.test/x", "https://www.a.test/y", true},
		{"https://www.a.test/x", "https://a.test./y", true},
		{"http://a.test/x", "https://a.test/y", true},
		{"http://a.test:8080/x", "https://a.test:8080/y", true},
		{"https://a.test:443/x", "https://a.test/y", true},
		{"https://a.test/x", "http://a.test/y", false},
		{"https://a.test/x", "https://a.test:8443/y", false},
		{"http://a.test:8080/x", "http://a.test:9090/y", false},
		{"https://a.test/x", "https://b.test/y", false},
		{"https://a.test/x", "https://api.a.test/y", false},
	} {
		a, _ := url.Parse(c.a)
		b, _ := url.Parse(c.b)
		if got := sameSite(a, b); got != c.same {
			t.Errorf("sameSite(%s, %s) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}

// A page reached through a redirect that stays on the site is cached like any other: a
// model paging through it fetches it once.
func TestAPageReachedThroughWwwIsCached(t *testing.T) {
	sf := newSiteFixture(t)
	sf.page = strings.Repeat("A paragraph of the page, long enough to page through.\n\n", 1200)
	sf.to.Store("http://www.a.test:" + sf.origin + "/page")
	start := "http://a.test:" + sf.origin + "/start"
	var cached []bool
	for _, off := range []int{0, 20000, 40000} {
		res := sf.fetch(start, map[string]any{"offset": off}, "Allow domain:a.test")
		if res.IsError {
			t.Fatalf("offset %d: %q", off, res.Text)
		}
		cached = append(cached, res.Meta["cached"].(bool))
	}
	if hits := sf.originHits.Load(); hits != 2 || cached[0] || !cached[1] || !cached[2] {
		t.Fatalf("origin hits %d (want 2: the start and the page, once), cached %v (want false true true)", hits, cached)
	}
	// a caller whose rules deny the www host is refused the cached page as well
	res := sf.fetch(start, map[string]any{"offset": 20000}, "Allow domain:a.test", "Deny domain:www.a.test")
	if !res.IsError || !strings.Contains(res.Text, "www.a.test") {
		t.Fatalf("a cached page reached through a denied redirect was served: %q", res.Text)
	}
}

// A refused redirect suggests fetching its URL directly only when that fetch could be
// approved: not when the refusal is that nobody can be asked.
func TestRefusedRedirectMessage(t *testing.T) {
	from, _ := url.Parse("https://a.test/x")
	to, _ := url.Parse("https://b.test/y")
	noOne := (&redirectRefusedError{from: from, to: to, reason: "approval required: network access to b.test" + perm.NoOneToAsk}).Error()
	if strings.Contains(noOne, "fetch that URL itself") || !strings.Contains(noOne, "b.test") {
		t.Errorf("with nobody to ask: %q", noOne)
	}
	denied := (&redirectRefusedError{from: from, to: to, reason: "denied by rule WebFetch(domain:b.test)"}).Error()
	if !strings.Contains(denied, "fetch that URL itself") {
		t.Errorf("a refusal by rule: %q", denied)
	}
}
