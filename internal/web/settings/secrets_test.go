package settings

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/harden"
)

// canaryShapes are values in the shapes credentials take (the token formats harden.LooksSecret and package redact know, and plain
// values that are secret only by where they sit); each test plants every shape in every place.
var canaryShapes = []string{
	"sk-canary0shape0openai0123456789abcdef",
	"sk-ant-canary1shape1anthropic0123456789",
	"ghp_canary2shape2github0123456789abcdefAB",
	"xoxb-canary3shape3-slack0123456789",
	"AKIACANARY4SHAPE4AWS",
	"AIzaCanary5Shape5Google0123456789abcdefgh",
	"eyJhbGciOiJIUzI1NiJ9.eyJjYW5hcnkiOiI2In0.Y2FuYXJ5NnNpZ25hdHVyZQ",
	"canary7plainvalue",
}

// TestNoSecretInSettingsDTOs plants canaries in provider headers and options, hook headers and command lines, MCP environment,
// headers, arguments and URLs (user information and query), auth.json, the ChatGPT sign-in and the environment, then reads every
// page and runs the actions: no canary may appear in any answer or in the server's log.
func TestNoSecretInSettingsDTOs(t *testing.T) {
	c := canaryShapes
	envKey := "sk-canaryEnvKey0123456789abcdefghij"
	storedKey := "sk-canaryStoredKey0123456789abcdefgh"
	e := newEnv(t, func(home, repo string) {
		user := fmt.Sprintf(`{
		  "providers": {"acme": {"base_url": "https://u:%[1]s@api.acme.test/v1?k=%[2]s", "api_key_env": "ACME_API_KEY",
		     "headers": {"Authorization": %[3]q, "X-Plain": %[4]q}, "options": {"token": %[5]q}}},
		  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [
		     {"type": "http", "url": "https://hooks.test/x?key=%[6]s", "headers": {"Authorization": %[7]q}},
		     {"type": "command", "command": "notify --token %[8]s && curl -H 'Authorization: Bearer %[1]s' https://x.test"}]}]},
		  "mcp": {"tracker": {"command": "tracker-mcp", "args": ["--key", %[2]q, "--project", "SHOP", "--token", %[8]q],
		     "env": {"TRACKER_TOKEN": %[3]q, "PLAIN": %[4]q}},
		     "remote": {"url": "https://u:%[5]s@mcp.test/hook/%[6]s?q=%[7]s", "headers": {"X-Api": %[8]q}}}
		}`, c[0], c[1], c[2], c[7], c[3], c[4], c[5], c[7])
		write(t, filepath.Join(home, ".sleipnir", "config.json"), user)
		write(t, filepath.Join(repo, ".mcp.json"), fmt.Sprintf(`{"mcpServers": {"proj": {"command": "proj-mcp", "args": [%q], "env": {"K": %q}}}}`, c[6], c[7]))
		write(t, filepath.Join(home, ".sleipnir", "auth.json"), fmt.Sprintf(`{"OPENROUTER_API_KEY": %q}`, storedKey))
		conn := `{"version":1,"host_id":"urn:uuid:x","client_id":"c","access_token":"` + c[6] + `","refresh_token":"` + c[0] +
			`","id_token":"` + c[6] + `","email":"ada@example.com","expires_at_ms":` + strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `}`
		write(t, filepath.Join(home, ".sleipnir", "chatgpt.json"), conn)
	})
	t.Setenv("HEIMDALL_API_KEY", envKey)
	t.Setenv("ACME_API_KEY", c[1])
	if err := config.LoadStoredKeys(e.home); err != nil { // what main does at start
		t.Fatal(err)
	}
	t.Cleanup(func() { harden.Provide("OPENROUTER_API_KEY", "") })
	all := append(append([]string(nil), c...), envKey, storedKey)
	check := func(what string, body []byte) {
		t.Helper()
		for _, canary := range all {
			if strings.Contains(string(body), canary) {
				t.Errorf("%s: canary %q is in the answer: %s", what, canary, body)
			}
		}
	}
	for _, path := range []string{
		"/api/models", "/api/sessions/t1/permissions", "/api/sessions/t1/trust", "/api/sessions/t1/mcp", "/api/sessions/t1/skills",
		"/api/providers", "/api/sessions/t1/config",
	} {
		rec := e.do("GET", path, nil, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
		check(path, rec.Body.Bytes())
	}
	// the trusted view reads the project's entries too
	write(t, filepath.Join(e.repo, "AGENTS.md"), "rules\n")
	for _, rt := range []struct{ method, path string }{
		{"POST", "/api/providers/recheck"},
		{"POST", "/api/sessions/t1/mcp/tracker/test"},
		{"POST", "/api/sessions/t1/mcp/proj/approve"},
		{"POST", "/api/providers/openrouter/signout"},
		{"POST", "/api/providers/heimdall/signout"},
		{"GET", "/api/trust/challenge?dir=" + e.repo},
	} {
		rec := e.do(rt.method, rt.path, nil, nil)
		check(rt.method+" "+rt.path, rec.Body.Bytes())
		check(rt.method+" "+rt.path+" (headers)", []byte(fmt.Sprint(rec.Header())))
	}
	check("the server's log", []byte(e.logText()))

	// the chat plan shows who is signed in, never a token
	var pv struct {
		Providers []struct {
			ID, Key, Who string
			SignedIn     bool
		}
	}
	e.ok("GET", "/api/providers", nil, nil, &pv)
	for _, p := range pv.Providers {
		if p.ID == "chatgpt" && (!p.SignedIn || p.Who != "ada@example.com" || p.Key != keySignedIn) {
			t.Errorf("chatgpt row: %+v", p)
		}
	}
}
