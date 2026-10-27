package env

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// A server without a token takes requests from this machine's programs, not from web pages: a request that names an origin is
// refused, and so is a body that is not sent as JSON (a page can send a form or text across origins without asking).
func TestATokenlessServerRefusesBrowserRequests(t *testing.T) {
	f := newServerFixture(t, func(o *ServeOptions) { o.Token = "" })
	post := func(h map[string]string) int {
		req, _ := http.NewRequest("POST", f.ts.URL+"/v1/rollouts", strings.NewReader(`{"task":{}}`))
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(map[string]string{"Content-Type": "text/plain"}); c != http.StatusUnsupportedMediaType {
		t.Errorf("text body: %d", c)
	}
	if c := post(map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}); c != http.StatusForbidden {
		t.Errorf("a page's request: %d", c)
	}
	if c := post(map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); c != http.StatusForbidden {
		t.Errorf("a cross-site fetch: %d", c)
	}
	if c := post(map[string]string{"Content-Type": "application/json; charset=utf-8"}); c == http.StatusForbidden || c == http.StatusUnsupportedMediaType || c == http.StatusUnauthorized {
		t.Errorf("a program's JSON request: %d", c)
	}
}

// A relative repo.path is judged where the workspaces read it (their base), and the task goes on with the resolved path.
func TestRepoPathIsReadAgainstTheWorkspaceBase(t *testing.T) {
	w := &Workspaces{opts: WorkspaceOptions{RepoBase: "/srv/repos"}}
	if got := w.RepoPath("a/../b"); got != filepath.Clean("/srv/repos/b") {
		t.Fatalf("%q", got)
	}
	if got := w.RepoPath("/abs/x"); got != "/abs/x" {
		t.Fatalf("%q", got)
	}
}

// A page whose DNS name is rebound to this machine reaches a tokenless server as its own origin, and an old browser sends no Fetch
// Metadata on a GET: the Host header, which names the page's host, is what gives it away. Loopback names still pass.
func TestATokenlessServerRefusesAForeignHostHeader(t *testing.T) {
	f := newServerFixture(t, func(o *ServeOptions) { o.Token = "" })
	get := func(host string) int {
		req, _ := http.NewRequest("GET", f.ts.URL+"/v1/runs/nothing-here", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, h := range []string{"evil.example", "evil.example:8080", "127.0.0.1.nip.io", "localhost.evil.example"} {
		if c := get(h); c != http.StatusForbidden {
			t.Errorf("Host %q: %d", h, c)
		}
	}
	for _, h := range []string{"localhost", "LOCALHOST:9", "127.0.0.1:1234", "[::1]:80", "127.0.0.2"} {
		if c := get(h); c == http.StatusForbidden {
			t.Errorf("Host %q refused", h)
		}
	}
	if !loopbackHostHeader("localhost.") || loopbackHostHeader("") || loopbackHostHeader("0.0.0.0") {
		t.Error("loopbackHostHeader")
	}
}
