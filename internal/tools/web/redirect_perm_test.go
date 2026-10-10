package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
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

	// A hop that does not change the host in a way that matters asks nothing more.
	for _, same := range []string{"/landing", "http://www.a.test:" + tcpPort(origin) + "/landing", "http://A.TEST.:" + tcpPort(origin) + "/landing"} {
		to.Store(same)
		rec := &hopPerm{next: engine("WebFetch(domain:a.test)")}
		text, isErr := run(rec, start+"?same="+same)
		if isErr || !strings.Contains(text, "same host") || len(rec.urls()) != 1 {
			t.Fatalf("redirect to %s: %v %q, asked %v", same, isErr, text, rec.urls())
		}
	}
}
