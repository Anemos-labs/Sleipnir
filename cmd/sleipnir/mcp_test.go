package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/mcp"
	"github.com/anemos-labs/sleipnir/internal/mcp/mcptest"
)

func TestMCPPromptArgs(t *testing.T) {
	p := mcp.PromptEntry{Arguments: []mcp.PromptArgument{{Name: "file"}, {Name: "focus"}}}
	for _, c := range []struct {
		line string
		want map[string]string
	}{
		{"", map[string]string{}},
		{"main.go", map[string]string{"file": "main.go"}},
		{"main.go the error paths and tests", map[string]string{"file": "main.go", "focus": "the error paths and tests"}},
		{"focus=speed main.go", map[string]string{"focus": "speed", "file": "main.go"}},
		{"file=a.go focus=b", map[string]string{"file": "a.go", "focus": "b"}},
		{"a b c d", map[string]string{"file": "a", "focus": "b c d"}},
	} {
		if got := mcpPromptArgs(p, c.line); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %v, want %v", c.line, got, c.want)
		}
	}
}

// mcpProject makes a home with one trusted server and a repository with one of
// its own.
func mcpProject(t *testing.T) (home, repo string) {
	t.Helper()
	home, repo = t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	t.Setenv("HOME", home)
	t.Setenv("SLEIPNIR_HOME", filepath.Join(home, ".sleipnir"))
	entry := func(extraArg string) map[string]any {
		return map[string]any{"command": os.Args[0], "args": []string{"-test.run=^$", extraArg},
			"env": map[string]string{mcptest.EnvHelper: "1", "GORACE": "atexit_sleep_ms=0"}}
	}
	write := func(path string, v any) {
		b, _ := json.Marshal(v)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".sleipnir", "config.json"), map[string]any{"mcp": map[string]any{"mine": entry("-test.v=false")}})
	write(filepath.Join(repo, ".sleipnir", "config.json"), map[string]any{"mcp": map[string]any{"theirs": entry("-test.short=false")}})
	return home, repo
}

func TestMCPListApproveRevoke(t *testing.T) {
	home, repo := mcpProject(t)
	root, _ := filepath.EvalSymlinks(repo)

	var out bytes.Buffer
	if err := mcpList(&out, home, root, true); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "mine") || !strings.Contains(got, "your config") || !strings.Contains(got, "theirs") || !strings.Contains(got, "needs approval") {
		t.Fatalf("list:\n%s", got)
	}
	// Your own entry is shown without its arguments; a project's entry is shown in full, because you are about to judge it.
	if strings.Contains(got, `"-test.v=false"`) || !strings.Contains(got, `"-test.short=false"`) {
		t.Errorf("list shows arguments for the wrong entries:\n%s", got)
	}
	// Without --trust-project the repository's entries are not even read.
	out.Reset()
	if err := mcpList(&out, home, root, false); err != nil || strings.Contains(out.String(), "theirs") {
		t.Errorf("an untrusted project's entry was listed: %v\n%s", err, out.String())
	}

	// Approving your own entry is refused, an unknown one too; declining leaves it unapproved.
	var msg bytes.Buffer
	if err := mcpApproval(strings.NewReader("n\n"), &msg, home, root, true, "theirs", false); err == nil {
		t.Error("declined approval must be an error")
	}
	if err := mcpApproval(nil, &msg, home, root, true, "mine", true); err == nil || !strings.Contains(err.Error(), "own entry") {
		t.Errorf("approving a user entry: %v", err)
	}
	if err := mcpApproval(nil, &msg, home, root, true, "nope", true); err == nil {
		t.Error("approving an unknown entry must fail")
	}
	if err := mcpApproval(strings.NewReader("y\n"), &msg, home, root, true, "theirs", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.String(), `"-test.short=false"`) {
		t.Errorf("the approval prompt did not show what would run:\n%s", msg.String())
	}
	out.Reset()
	_ = mcpList(&out, home, root, true)
	if !strings.Contains(out.String(), "approved") {
		t.Errorf("after approval:\n%s", out.String())
	}
	if err := mcpApproval(nil, &msg, home, root, false, "theirs", false); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_ = mcpList(&out, home, root, true)
	if strings.Contains(out.String(), "approved") {
		t.Errorf("after revoking:\n%s", out.String())
	}
}

func TestMCPTestStartsTheServersAndListsTheirTools(t *testing.T) {
	home, repo := mcpProject(t)
	_ = home
	var out, errw bytes.Buffer
	// Only the user's own server: the project's needs approval and the test has no terminal to ask on.
	if err := mcpTest(context.Background(), &out, &errw, repo, nil, false); err != nil {
		t.Fatalf("%v\n%s\n%s", err, out.String(), errw.String())
	}
	got := out.String()
	for _, want := range []string{"mine: ready", "mcp__mine__echo", "tools in the frozen list"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "theirs") {
		t.Errorf("an untrusted project's server appeared:\n%s", got)
	}
}

// `config --json` output is pasted into bug reports: an MCP entry's credentials must not be in it.
func TestConfigJSONRedactsMCPCredentials(t *testing.T) {
	cfg := &config.Config{MCP: map[string]json.RawMessage{
		"remote": json.RawMessage(`{"url":"https://mcp.example.com/v1/tok_abcdefgh12345678","headers":{"Authorization":"Bearer sk_secret_value_123456"}}`),
		"local":  json.RawMessage(`{"command":"srv","args":["--token","tok_abcdefgh12345678"],"env":{"API_KEY":"key_value_abcdefgh"}}`),
		"broken": json.RawMessage(`{"command":42}`),
	}}
	b, err := json.Marshal(redactedConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, leak := range []string{"tok_abcdefgh", "sk_secret_value", "key_value_abcdefgh", "Bearer"} {
		if strings.Contains(got, leak) {
			t.Errorf("the redacted config still contains %q:\n%s", leak, got)
		}
	}
	for _, want := range []string{"https://mcp.example.com/...", "Authorization", "API_KEY", "2 arguments", "not a valid entry"} {
		if !strings.Contains(got, want) {
			t.Errorf("the redacted config lacks %q:\n%s", want, got)
		}
	}
}
