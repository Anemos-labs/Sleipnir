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
