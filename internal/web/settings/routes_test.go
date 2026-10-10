package settings

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// route is one route of this package as the security matrix sends it.
type route struct {
	method, path string
	body         any  // a valid body for a route that reads one
	noBody       bool // the route refuses any body
}

// settingsRoutes are every route Register adds (KeyRoutes is false).
func settingsRoutes(repo string) []route {
	return []route{
		{method: "GET", path: "/api/models"},
		{method: "POST", path: "/api/models/fav", body: wire.FavRequest{Ref: "acme/m", On: true}},
		{method: "GET", path: "/api/sessions/t1/permissions"},
		{method: "GET", path: "/api/sessions/t1/trust"},
		{method: "GET", path: "/api/trust/challenge?dir=" + repo},
		{method: "POST", path: "/api/trust", body: trustRequest{Dir: repo}},
		{method: "GET", path: "/api/sessions/t1/mcp"},
		{method: "POST", path: "/api/sessions/t1/mcp/x/approve", noBody: true},
		{method: "POST", path: "/api/sessions/t1/mcp/x/revoke", noBody: true},
		{method: "POST", path: "/api/sessions/t1/mcp/x/test", noBody: true},
		{method: "POST", path: "/api/sessions/t1/mcp/x/reconnect", noBody: true},
		{method: "GET", path: "/api/sessions/t1/skills"},
		{method: "GET", path: "/api/providers"},
		{method: "POST", path: "/api/providers/recheck", noBody: true},
		{method: "POST", path: "/api/providers/heimdall/signout", noBody: true},
		{method: "GET", path: "/api/sessions/t1/config"},
	}
}

// Every route is behind the envelope: a credential, the custom header and a JSON body on writes, the page's own origin, the body
// caps; a route that takes no body refuses one; GET routes are read-only (another method is refused).
func TestEveryRouteIsBehindTheEnvelope(t *testing.T) {
	e := newEnv(t, nil)
	big := `{"ref":"` + strings.Repeat("a", 70<<10) + `"}`
	for _, rt := range settingsRoutes(e.repo) {
		name := rt.method + " " + rt.path
		t.Run(name, func(t *testing.T) {
			if rec := e.do(rt.method, rt.path, rt.body, map[string]string{"Authorization": ""}); rec.Code != http.StatusUnauthorized {
				t.Errorf("no credential: %d %s", rec.Code, rec.Body)
			}
			if rt.method == "GET" {
				if rec := e.do("DELETE", rt.path, nil, nil); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
					t.Errorf("another method: %d", rec.Code)
				}
				return
			}
			if rec := e.do(rt.method, rt.path, rt.body, map[string]string{"X-Sleipnir-Web": ""}); rec.Code != http.StatusForbidden || errCode(rec) != "csrf" {
				t.Errorf("without the custom header: %d %s", rec.Code, errCode(rec))
			}
			if rec := e.do(rt.method, rt.path, rt.body, map[string]string{"Origin": "http://evil.test", "Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
				t.Errorf("from another site: %d %s", rec.Code, errCode(rec))
			}
			if rec := e.do(rt.method, rt.path, rt.body, map[string]string{"Origin": "http://127.0.0.1:7000"}); rec.Code != http.StatusForbidden {
				t.Errorf("from another local port: %d %s", rec.Code, errCode(rec))
			}
			if rt.noBody {
				if rec := e.do(rt.method, rt.path, `{}`, nil); rec.Code != http.StatusBadRequest {
					t.Errorf("a body on a route that takes none: %d", rec.Code)
				}
				return
			}
			if rec := e.do(rt.method, rt.path, `{}`, map[string]string{"Content-Type": "text/plain"}); rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("a body that is not JSON: %d", rec.Code)
			}
			if rec := e.do(rt.method, rt.path, big, nil); rec.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("a body over the cap: %d", rec.Code)
			}
			if rec := e.do(rt.method, rt.path, `{"unknown":1}`, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("an unknown field: %d", rec.Code)
			}
		})
	}
}

func TestTabIdsAreCheckedBeforeAnyLookup(t *testing.T) {
	e := newEnv(t, nil)
	if rec := e.do("GET", "/api/providers", nil, map[string]string{"Authorization": "Bearer not-the-token"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong token: %d", rec.Code)
	}
	for _, p := range []string{"permissions", "trust", "mcp", "skills", "config"} {
		if rec := e.do("GET", "/api/sessions/BAD_ID/"+p, nil, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s with a malformed id: %d", p, rec.Code)
		}
		if rec := e.do("GET", "/api/sessions/nope/"+p, nil, nil); rec.Code != http.StatusNotFound || errCode(rec) != "no_session" {
			t.Errorf("%s of a tab that is not there: %d %s", p, rec.Code, errCode(rec))
		}
	}
	for _, p := range []string{"approve", "revoke", "test", "reconnect"} {
		if rec := e.do("POST", "/api/sessions/t1/mcp/bad%20name/"+p, nil, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("mcp %s with a malformed name: %d", p, rec.Code)
		}
	}
	if rec := e.do("POST", "/api/providers/bad%20name/signout", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("signout with a malformed name: %d", rec.Code)
	}
}

// The key route is not served (decision D-01): the catch-all answers it.
func TestTheKeyRouteIsNotServed(t *testing.T) {
	e := newEnv(t, nil)
	if KeyRoutes {
		t.Fatal("KeyRoutes is on: decision D-01 keeps sign-in in the terminal")
	}
	rec := e.do("POST", "/api/providers/openai/key", wire.KeySaveRequest{Key: "sk-whatever-0123456789abcdef"}, map[string]string{"X-Confirm": e.confirm("key:openai")})
	if rec.Code != http.StatusNotFound {
		t.Errorf("the key route answered %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".sleipnir", "auth.json")); !os.IsNotExist(err) {
		t.Error("a key was stored through a route that is off")
	}
}

// Texts this package keeps a copy of stay those of their source.
func TestCopiedTextsMatchTheirSources(t *testing.T) {
	for file, want := range map[string][]string{
		filepath.Join("..", "..", "swarm", "swarm.go"):            {managerWritesText},
		filepath.Join("..", "..", "tui", "app", "chat_dialog.go"): {"Yes, use them this time", "Yes, and remember them until they change", "No, leave them out"},
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s no longer says %q", file, w)
			}
		}
	}
}
