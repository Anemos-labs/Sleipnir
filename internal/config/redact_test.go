package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/mcp"
)

// Canaries: values that must never come out of Redact, in the shapes credentials take.
var redactCanaries = []string{
	"sk-canaryHeaderValue0123456789abcdef", // a provider header (Authorization-like)
	"canary-plain-header-value",            // a header value of no particular shape: every header value is withheld
	"ghp_canaryOptionToken0123456789abcdefABCD",
	"canary-hook-header",
	"canaryhookpass",
	"xoxb-canary-slack-0123456789",
	"canary-mcp-env-value",
	"canary-mcp-header",
	"canary-url-user",
	"canary-url-pass",
	"canary-query-value",
	"sk-ant-canaryArgKey0123456789abcdefgh",
	"canaryFlagValueAfterToken",
	"canary-unknown-top-level",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYW5hcnkifQ.c2lnbmF0dXJlY2FuYXJ5",
}

func redactFixture(t *testing.T) *Config {
	t.Helper()
	doc := `{
	  "providers": {"acme": {"base_url": "https://canary-url-user:canary-url-pass@api.acme.test/v1?token=canary-query-value",
	    "api_key_env": "ACME_API_KEY", "headers": {"Authorization": "sk-canaryHeaderValue0123456789abcdef", "X-Other": "canary-plain-header-value"},
	    "options": {"token": "ghp_canaryOptionToken0123456789abcdefABCD", "session_header": true}}},
	  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [
	    {"type": "http", "url": "https://hooks.test/x?key=canary-query-value", "headers": {"Authorization": "canary-hook-header"}},
	    {"type": "command", "command": "curl -u admin:canaryhookpass https://x.test -H 'Authorization: Bearer xoxb-canary-slack-0123456789'"}]}]},
	  "mcp": {"tracker": {"command": "tracker-mcp", "args": ["--key", "sk-ant-canaryArgKey0123456789abcdefgh", "--token", "canaryFlagValueAfterToken", "--project", "SHOP"],
	    "env": {"TRACKER_TOKEN": "canary-mcp-env-value"}},
	    "remote": {"url": "https://canary-url-user:canary-url-pass@mcp.test/p/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYW5hcnkifQ.c2lnbmF0dXJlY2FuYXJ5?q=canary-query-value",
	    "headers": {"X-Api": "canary-mcp-header"}}},
	  "models": {"default": "acme/model-1"},
	  "mystery": {"value": "canary-unknown-top-level"}
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func TestRedactWithholdsEveryCanary(t *testing.T) {
	out := Redact(redactFixture(t))
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, c := range redactCanaries {
		if strings.Contains(text, c) {
			t.Errorf("canary %q is in the redacted configuration: %s", c, text)
		}
	}
	// What is not a secret stays readable.
	for _, keep := range []string{"ACME_API_KEY", "api.acme.test", "acme/model-1", "tracker-mcp", "SHOP", "--project", "X-Other", "TRACKER_TOKEN", "Bash"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q was withheld but is not a secret: %s", keep, text)
		}
	}
	prov := out["providers"].(map[string]any)["acme"].(map[string]any)
	if prov["headers"].(map[string]any)["X-Other"] != Hidden || prov["options"].(map[string]any)["session_header"] != true {
		t.Errorf("provider entry: %v", prov)
	}
	if out["mystery"] != Hidden {
		t.Errorf("an unknown top-level key is withheld whole: %v", out["mystery"])
	}
}

func TestRedactAtShowsALayerValueByTheSameRules(t *testing.T) {
	v := RedactAt([]string{"providers", "acme", "headers"}, map[string]any{"Authorization": "canary-plain-header-value"})
	if m := v.(map[string]any); m["Authorization"] != Hidden {
		t.Errorf("a header value at depth: %v", v)
	}
	if v := RedactAt([]string{"mcp", "x"}, map[string]any{"command": 12}); v != invalidMCP {
		t.Errorf("an MCP entry that does not parse: %v", v)
	}
	if v := RedactAt([]string{"cache", "shared_ttl"}, "5m"); v != "5m" {
		t.Errorf("an ordinary value: %v", v)
	}
}

func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://api.example.com/v1", "https://api.example.com/v1"},
		{"https://u:p@api.example.com/v1?key=abc&b=2#frag", "https://" + Hidden + "@api.example.com/v1?key=" + Hidden + "&b=" + Hidden + "#" + Hidden},
		{"http://127.0.0.1:8000/v1", "http://127.0.0.1:8000/v1"},
		{"${BASE}/v1", "${BASE}/v1"},
	} {
		if got := RedactURL(tc.in); got != tc.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactArgs(t *testing.T) {
	in := []string{"--project", "SHOP", "--token", "abc", "--api-key=sk-proj-0123456789abcdefghij", "-v", "ghp_0123456789abcdefghijABCDEFGHIJ0123", "--password", "-x"}
	got := RedactArgs(in)
	want := []string{"--project", "SHOP", "--token", Hidden, Hidden, "-v", Hidden, "--password", "-x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("RedactArgs:\n got %q\nwant %q", got, want)
	}
}

func TestMCPEntryKeepsNamesAndWithholdsValues(t *testing.T) {
	c := mcp.ServerConfig{Command: "srv", Args: []string{"--x"}, Env: map[string]string{"K": "v-canary"}, URL: "", Disabled: true}
	e := MCPEntry(c)
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "v-canary") || !strings.Contains(string(b), `"K"`) || e["disabled"] != true {
		t.Errorf("entry: %s", b)
	}
}
