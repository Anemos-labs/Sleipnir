package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// placeholder is a stand-in credential. Tests never use credential-shaped
// literals; anything that must look like a secret is this plain string.
const placeholder = "test-token-not-a-secret"

func rawMap(t *testing.T, doc string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("bad test document: %v", err)
	}
	return m
}

func issuesOf(is []Issue, fatal bool) []string {
	var out []string
	for _, i := range is {
		if i.Fatal == fatal {
			out = append(out, i.Error())
		}
	}
	return out
}

func TestParseShapes(t *testing.T) {
	want := map[string]ServerConfig{
		"github": {Type: TypeStdio, Command: "gh-mcp", Args: []string{"--stdio", "-v"}, Env: map[string]string{"A": "1", "PORT": "8080"}, Cwd: "sub"},
		"docs":   {Type: TypeHTTP, URL: "https://docs.example.com/mcp", Headers: map[string]string{"X-Team": "core"}},
		"legacy": {Type: TypeSSE, URL: "https://old.example.com/sse"},
	}
	sleipnir := `{
	  "github": {"command": "gh-mcp", "args": ["--stdio", "-v"], "env": {"A": "1", "PORT": 8080}, "cwd": "sub"},
	  "docs": {"url": "https://docs.example.com/mcp", "headers": {"X-Team": "core"}},
	  "legacy": {"type": "sse", "url": "https://old.example.com/sse"}
	}`
	claude := `{"mcpServers": ` + sleipnir + `}`
	for name, doc := range map[string]string{"sleipnir": sleipnir, "claude code": claude} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(rawMap(t, doc))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d servers, want %d: %v", len(got), len(want), got)
			}
			for n, w := range want {
				g := got[n]
				g.Scope = ""
				if !reflect.DeepEqual(g, w) {
					t.Errorf("%s:\n got %#v\nwant %#v", n, g, w)
				}
			}
		})
	}
}

func TestParseErrorsNameServerAndField(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string // substrings of the joined error
	}{
		{"not an object", `{"x": 5}`, []string{`server "x"`, "must be an object"}},
		{"nothing to run", `{"x": {}}`, []string{`server "x"`, `needs "command"`}},
		{"both transports", `{"x": {"command": "a", "url": "https://h"}}`, []string{`server "x"`, `both "command" and "url"`}},
		{"unknown field with hint", `{"x": {"command": "a", "deny_tool": ["*"]}}`, []string{`server "x"`, `field "deny_tool"`, `did you mean "deny_tools"`}},
		{"unknown field no hint", `{"x": {"command": "a", "zzzzzzzz": 1}}`, []string{`field "zzzzzzzz"`, "known fields"}},
		{"args type", `{"x": {"command": "a", "args": "-v"}}`, []string{`server "x"`, `field "args"`, "array of strings"}},
		{"env value type", `{"x": {"command": "a", "env": {"K": {"nested": 1}}}}`, []string{`field "env.K"`, "must be a string"}},
		{"header value type", `{"x": {"url": "https://h", "headers": {"Authorization": ["` + placeholder + `"]}}}`, []string{`field "headers.Authorization"`, "must be a string"}},
		{"bad type", `{"x": {"type": "grpc", "command": "a"}}`, []string{`field "type"`, `"stdio", "http" or "sse"`}},
		{"negative timeout", `{"x": {"command": "a", "timeout": -3}}`, []string{`field "timeout"`}},
		{"timeout word", `{"x": {"command": "a", "timeout": "soon"}}`, []string{`field "timeout"`, "duration"}},
		{"max output", `{"x": {"command": "a", "max_output_chars": -1}}`, []string{`field "max_output_chars"`}},
		{"stdio with url", `{"x": {"type": "stdio", "command": "a", "url": "https://h"}}`, []string{`field "url"`, "not valid for a stdio"}},
		{"http with command", `{"x": {"type": "http", "url": "https://h", "command": "a"}}`, []string{`field "command"`, "not valid for a http"}},
		{"http with args", `{"x": {"type": "http", "url": "https://h", "args": ["a"]}}`, []string{`field "args"`}},
		{"empty command", `{"x": {"type": "stdio", "command": "  "}}`, []string{`field "command"`, "required"}},
		{"glob list type", `{"x": {"command": "a", "allow_tools": "*"}}`, []string{`field "allow_tools"`, "array"}},
		{"empty glob", `{"x": {"command": "a", "deny_tools": [""]}}`, []string{`field "deny_tools[0]"`}},
		{"same option twice", `{"x": {"command": "a", "allow_tools": ["a"], "allowTools": ["b"]}}`, []string{"sets the same option"}},
		{"enabled contradicts", `{"x": {"command": "a", "disabled": true, "enabled": true}}`, []string{`field "enabled"`, "contradicts"}},
		{"trust type", `{"x": {"command": "a", "trust": "yes"}}`, []string{`field "trust"`, "true or false"}},
		{"empty name", `{"": {"command": "a"}}`, []string{"server names must be non-empty"}},
		{"control name", "{\"a\\u200bb\": {\"command\": \"a\"}}", []string{"invisible characters"}},
		{"mcpServers not object", `{"mcpServers": []}`, []string{`field "mcpServers"`, "must be an object"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(rawMap(t, tt.doc))
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if strings.Contains(err.Error(), placeholder) {
				t.Errorf("error leaks a credential: %v", err)
			}
		})
	}
}

func TestParseKeepsValidServersNextToBrokenOnes(t *testing.T) {
	got, err := Parse(rawMap(t, `{"good": {"command": "a"}, "bad": {"command": "a", "url": "https://h"}, "alsogood": {"url": "https://h"}}`))
	if err == nil {
		t.Fatal("expected an error for the broken entry")
	}
	if _, ok := got["good"]; !ok {
		t.Error("good server missing")
	}
	if _, ok := got["alsogood"]; !ok {
		t.Error("alsogood server missing")
	}
	if _, ok := got["bad"]; ok {
		t.Error("broken server must not be returned")
	}
	var is Issue
	if !errors.As(err, &is) || is.Server != "bad" || !is.Fatal {
		t.Errorf("errors.As(Issue) = %+v", is)
	}
}

func TestParseAliasesAndForeignFields(t *testing.T) {
	doc := `{"x": {
	  "command": "a", "workingDirectory": "w", "allowTools": ["a*"], "excludeTools": ["b*"],
	  "maxOutputChars": 100, "startupTimeout": "45s", "allowPrivate": false, "enabled": false,
	  "alwaysAllow": ["tool"], "description": "hi", "oauth": {"x": 1}
	}}`
	got, is := ParseWith(rawMap(t, doc), ParseOptions{Scope: ScopeUser})
	x, ok := got["x"]
	if !ok {
		t.Fatalf("not parsed: %v", is)
	}
	if x.Cwd != "w" || !reflect.DeepEqual(x.AllowTools, []string{"a*"}) || !reflect.DeepEqual(x.DenyTools, []string{"b*"}) ||
		x.MaxOutputChars != 100 || x.StartupTimeout != 45*time.Second || !x.Disabled {
		t.Errorf("aliases not applied: %+v", x)
	}
	warns := strings.Join(issuesOf(is, false), "\n")
	for _, w := range []string{"alwaysAllow", "description", "oauth"} {
		if !strings.Contains(warns, w) {
			t.Errorf("no warning for ignored field %s in:\n%s", w, warns)
		}
	}
	if !strings.Contains(warns, "permission rules") {
		t.Errorf("auto-approve style options must explain that prompts are not skipped:\n%s", warns)
	}
	if len(issuesOf(is, true)) != 0 {
		t.Errorf("unexpected fatal issues: %v", issuesOf(is, true))
	}
}

func TestParseClaudeShapeWarnsAboutSiblings(t *testing.T) {
	got, is := ParseWith(rawMap(t, `{"mcpServers": {"a": {"command": "x"}}, "inputs": [], "b": {"command": "y"}}`), ParseOptions{})
	if len(got) != 1 || got["a"].Command != "x" {
		t.Fatalf("got %v", got)
	}
	w := strings.Join(issuesOf(is, false), "\n")
	if !strings.Contains(w, `"inputs"`) || !strings.Contains(w, `"b"`) {
		t.Errorf("siblings not reported:\n%s", w)
	}
}

func TestTrustAndPrivilegesFollowScope(t *testing.T) {
	entry := `{"x": {"command": "a", "trust": %s, "allow_private": true}}`
	tests := []struct {
		name        string
		scope       Scope
		trust       string
		wantTrust   bool
		wantPrivate bool
		wantWarn    []string
	}{
		{"user default", ScopeUser, "null", true, true, nil},
		{"user explicit false", ScopeUser, "false", false, true, nil},
		{"user explicit true", ScopeUser, "true", true, true, nil},
		{"project cannot self-trust", ScopeProject, "true", false, false, []string{"trust", "allow_private"}},
		{"unknown scope is untrusted", "", "true", false, false, []string{"trust", "allow_private"}},
		{"project without claims", ScopeProject, "null", false, false, []string{"allow_private"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, is := ParseWith(rawMap(t, fmt.Sprintf(entry, tt.trust)), ParseOptions{Scope: tt.scope})
			x := got["x"]
			if x.Trust != tt.wantTrust || x.AllowPrivate != tt.wantPrivate || x.Scope != tt.scope {
				t.Errorf("trust=%v private=%v scope=%q, want %v %v %q", x.Trust, x.AllowPrivate, x.Scope, tt.wantTrust, tt.wantPrivate, tt.scope)
			}
			w := strings.Join(issuesOf(is, false), "\n")
			for _, s := range tt.wantWarn {
				if !strings.Contains(w, `"`+s+`"`) {
					t.Errorf("no warning about %s:\n%s", s, w)
				}
			}
		})
	}
	// Plain Parse is the untrusted path.
	got, _ := Parse(rawMap(t, `{"x": {"command": "a", "trust": true}}`))
	if got["x"].Trust {
		t.Error("Parse must not honour trust from an unlabelled file")
	}
}

func TestParseDurations(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		bad  bool
	}{
		{`30`, 30 * time.Second, false},
		{`1.5`, 1500 * time.Millisecond, false},
		{`"90s"`, 90 * time.Second, false},
		{`"1500ms"`, 1500 * time.Millisecond, false},
		{`"2m"`, 2 * time.Minute, false},
		{`"45"`, 45 * time.Second, false},
		{`0`, 0, false},
		{`-1`, 0, true},
		{`"-5s"`, 0, true},
		{`100000`, 0, true},
		{`"abc"`, 0, true},
		{`true`, 0, true},
		{`1e400`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, is := ParseWith(rawMap(t, `{"x": {"command": "a", "timeout": `+tt.in+`}}`), ParseOptions{})
			if tt.bad {
				if len(issuesOf(is, true)) == 0 {
					t.Fatalf("no error, got %v", got["x"].Timeout)
				}
				return
			}
			if got["x"].Timeout != tt.want {
				t.Errorf("timeout = %v, want %v (issues %v)", got["x"].Timeout, tt.want, is)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"x": {"command": "a", "args": [`)
	for i := 0; i <= maxArgs; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"a"`)
	}
	sb.WriteString(`]}}`)
	if _, err := Parse(rawMap(t, sb.String())); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too many args accepted: %v", err)
	}

	long := strings.Repeat("a", maxCommandLen+1)
	if _, err := Parse(rawMap(t, `{"x": {"command": "`+long+`"}}`)); err == nil {
		t.Error("oversized command accepted")
	}

	many := map[string]json.RawMessage{}
	for i := 0; i < maxServers+5; i++ {
		many[fmt.Sprintf("s%03d", i)] = json.RawMessage(`{"command": "a"}`)
	}
	got, err := Parse(many)
	if err == nil || len(got) != maxServers {
		t.Errorf("server cap: got %d servers, err=%v", len(got), err)
	}
}

func TestParseNullEntryIsIgnored(t *testing.T) {
	got, err := Parse(rawMap(t, `{"a": null, "b": {"command": "x"}}`))
	if err != nil || len(got) != 1 {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := Parse(nil); err != nil || len(got) != 0 {
		t.Errorf("nil input: %v %v", got, err)
	}
}

func TestEffectiveTypeInference(t *testing.T) {
	tests := []struct {
		cfg  ServerConfig
		want string
	}{
		{ServerConfig{Command: "a"}, TypeStdio},
		{ServerConfig{URL: "https://h"}, TypeHTTP},
		{ServerConfig{Type: "Streamable-HTTP", URL: "https://h"}, TypeHTTP},
		{ServerConfig{Type: "SSE", URL: "https://h"}, TypeSSE},
		{ServerConfig{Command: "a", URL: "https://h"}, ""},
		{ServerConfig{}, ""},
	}
	for _, tt := range tests {
		if got := tt.cfg.EffectiveType(); got != tt.want {
			t.Errorf("%+v: %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := func(c ServerConfig) ServerConfig { return c }
	tests := []struct {
		name string
		cfg  ServerConfig
		want string // "" = valid; else substring
	}{
		{"stdio ok", ok(ServerConfig{Command: "a", Env: map[string]string{"A": "1"}}), ""},
		{"http ok", ServerConfig{URL: "https://h.example/mcp?x=1", Headers: map[string]string{"Authorization": "Bearer " + placeholder}}, ""},
		{"nul in command", ServerConfig{Command: "a\x00b"}, "command"},
		{"nul in arg", ServerConfig{Command: "a", Args: []string{"x\x00"}}, "args[0]"},
		{"env name with =", ServerConfig{Command: "a", Env: map[string]string{"A=B": "1"}}, "env."},
		{"env empty name", ServerConfig{Command: "a", Env: map[string]string{"": "1"}}, "env."},
		{"ftp url", ServerConfig{URL: "ftp://h/x"}, "http:// or https://"},
		{"no host", ServerConfig{URL: "https:///x"}, "no host"},
		{"userinfo", ServerConfig{URL: "https://user:" + placeholder + "@h/x"}, "must not contain credentials"},
		{"header crlf", ServerConfig{URL: "https://h", Headers: map[string]string{"X-A": "a\r\nInjected: 1"}}, "control character"},
		{"header bad name", ServerConfig{URL: "https://h", Headers: map[string]string{"X A": "1"}}, "invalid header name"},
		{"reserved host", ServerConfig{URL: "https://h", Headers: map[string]string{"Host": "evil"}}, "managed by the transport"},
		{"reserved session", ServerConfig{URL: "https://h", Headers: map[string]string{"mcp-session-id": "x"}}, "managed by the transport"},
		{"reserved content type", ServerConfig{URL: "https://h", Headers: map[string]string{"Content-Type": "text/plain"}}, "managed by the transport"},
		{"no type", ServerConfig{}, "type"},
		{"negative timeout", ServerConfig{Command: "a", Timeout: -1}, "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("error %v, want substring %q", err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), placeholder) {
				t.Errorf("validation error leaks a value: %v", err)
			}
		})
	}
}

func TestSecretsStayOutOfTextForms(t *testing.T) {
	c := ServerConfig{
		Type: TypeHTTP, URL: "https://user:" + placeholder + "@h.example/secret-path-" + placeholder + "?key=" + placeholder,
		Headers: map[string]string{"Authorization": "Bearer " + placeholder},
		Env:     map[string]string{"TOKEN": placeholder},
		Scope:   ScopeUser, Trust: true,
	}
	forms := map[string]string{
		"String":     c.String(),
		"%v":         fmt.Sprintf("%v", c),
		"%+v":        fmt.Sprintf("%+v", c),
		"%#v":        fmt.Sprintf("%#v", c),
		"%v pointer": fmt.Sprintf("%v", &c),
		"Redacted":   fmt.Sprintf("%v %v %v", c.Redacted().Env, c.Redacted().Headers, c.Redacted().URL),
	}
	for name, s := range forms {
		if strings.Contains(s, placeholder) {
			t.Errorf("%s leaks a value: %s", name, s)
		}
	}
	if !strings.Contains(c.String(), "Authorization") || !strings.Contains(c.String(), "h.example") {
		t.Errorf("String should still say what it is: %s", c.String())
	}
	if c.Redacted().Headers["Authorization"] != "***" || c.Redacted().Env["TOKEN"] != "***" {
		t.Errorf("Redacted did not mask values: %+v", c.Redacted())
	}
	// The original is untouched.
	if c.Headers["Authorization"] != "Bearer "+placeholder {
		t.Error("Redacted mutated its receiver")
	}
}

func TestFingerprint(t *testing.T) {
	base := ServerConfig{Command: "a", Args: []string{"x"}, Env: map[string]string{"A": "1", "B": "2"}}
	same := ServerConfig{Command: "a", Args: []string{"x"}, Env: map[string]string{"B": "2", "A": "1"}, Trust: true, Scope: ScopeUser, Timeout: time.Minute, AllowTools: []string{"a"}}
	if base.Fingerprint() != same.Fingerprint() {
		t.Error("fingerprint must ignore map order and settings that do not decide what runs")
	}
	for name, c := range map[string]ServerConfig{
		"command": {Command: "b", Args: []string{"x"}, Env: base.Env},
		"args":    {Command: "a", Args: []string{"y"}, Env: base.Env},
		"env":     {Command: "a", Args: []string{"x"}, Env: map[string]string{"A": "1"}},
		"url":     {URL: "https://h"},
		"private": {Command: "a", Args: []string{"x"}, Env: base.Env, AllowPrivate: true},
	} {
		if c.Fingerprint() == base.Fingerprint() {
			t.Errorf("%s change did not change the fingerprint", name)
		}
	}
}

func TestMarshalJSONRoundTrip(t *testing.T) {
	in := ServerConfig{
		Type: TypeStdio, Command: "a", Args: []string{"x"}, Timeout: 90 * time.Second, StartupTimeout: 1500 * time.Millisecond,
		AllowTools: []string{"a*"}, MaxOutputChars: 10, Scope: ScopeUser, Trust: true,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"timeout":"1m30s"`) {
		t.Errorf("duration not rendered as a string: %s", b)
	}
	got, is := ParseWith(map[string]json.RawMessage{"x": b}, ParseOptions{Scope: ScopeUser})
	if len(issuesOf(is, true)) != 0 {
		t.Fatalf("round trip failed: %v", is)
	}
	if !reflect.DeepEqual(got["x"], in) {
		t.Errorf("round trip:\n got %#v\nwant %#v", got["x"], in)
	}
}

func TestExpandConfig(t *testing.T) {
	env := map[string]string{"TOKEN": placeholder, "HOST": "example.com", "EMPTY": ""}
	c := ServerConfig{
		Type: TypeHTTP, URL: "https://${HOST}/mcp", Command: "",
		Headers: map[string]string{"Authorization": "Bearer ${TOKEN}", "X-Region": "${REGION:-eu}", "X-Empty": "${EMPTY:-fallback}"},
	}
	got, err := c.Expand(env)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.com/mcp" || got.Headers["Authorization"] != "Bearer "+placeholder ||
		got.Headers["X-Region"] != "eu" || got.Headers["X-Empty"] != "fallback" {
		t.Errorf("expanded: %+v", got)
	}
	if c.URL != "https://${HOST}/mcp" {
		t.Error("Expand mutated its receiver")
	}

	_, err = c.Expand(map[string]string{"HOST": "h"})
	if err == nil || !strings.Contains(err.Error(), "headers.Authorization") || !strings.Contains(err.Error(), "TOKEN") {
		t.Errorf("missing variable error should name field and variable: %v", err)
	}
	var unset *UnsetVarError
	if !errors.As(err, &unset) || unset.Name != "TOKEN" {
		t.Errorf("errors.As(UnsetVarError) = %v", err)
	}

	// Never from the process environment.
	t.Setenv("MCP_TEST_PROCESS_ONLY", "from-the-process")
	_, err = ServerConfig{Command: "${MCP_TEST_PROCESS_ONLY}"}.Expand(nil)
	if err == nil {
		t.Error("nil env map must not fall back to the process environment")
	}
	if strings.Contains(fmt.Sprint(err), "from-the-process") {
		t.Errorf("error leaks the process value: %v", err)
	}
}

func TestEnvRefs(t *testing.T) {
	c := ServerConfig{
		Command: "${BIN}", Args: []string{"--k=${KEY}", "${A:-${B}}", "literal $NOT"},
		Env: map[string]string{"X": "${KEY}", "Y": "$${ESCAPED}"}, Cwd: "${HOME_DIR:-/tmp}",
		URL: "", Headers: map[string]string{"Authorization": "${TOKEN}"},
	}
	got := c.EnvRefs()
	want := []string{"A", "B", "BIN", "HOME_DIR", "KEY", "TOKEN"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnvRefs = %v, want %v", got, want)
	}
}
