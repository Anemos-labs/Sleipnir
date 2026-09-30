package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
)

func hookConfig(t *testing.T, hooks map[string]string) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Hooks = map[string]json.RawMessage{}
	for ev, cmd := range hooks {
		b, _ := json.Marshal([]any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}})
		cfg.Hooks[ev] = b
	}
	return cfg
}

func TestPromptHooksAddContextAndCanBlock(t *testing.T) {
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "ended")
	var seen []string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		seen = append(seen, c.LastUser())
		return mock.Reply{Text: "ok"}
	})
	o := opts(t, repo, client, model)
	o.Config = hookConfig(t, map[string]string{
		"SessionStart":     "cat >/dev/null; echo 'SESSION-CONTEXT: the build is green'",
		"UserPromptSubmit": "if grep -q forbidden; then echo 'no forbidden words here' >&2; exit 2; fi; echo 'PROMPT-CONTEXT: use tabs'",
		"SessionEnd":       "cat >/dev/null; touch " + marker,
	})
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "fix the parser"); err != nil {
		t.Fatal(err)
	}
	got := seen[len(seen)-1]
	for _, want := range []string{"fix the parser", "SESSION-CONTEXT: the build is green", "PROMPT-CONTEXT: use tabs"} {
		if !strings.Contains(got, want) {
			t.Errorf("the model did not receive %q:\n%s", want, got)
		}
	}
	// A hook context is text of the message: the cached layers must not carry it.
	if strings.Contains(s.Shared.Text(), "PROMPT-CONTEXT") || strings.Contains(s.Shared.Text(), "SESSION-CONTEXT") {
		t.Error("hook output leaked into the shared layer")
	}

	// The second prompt is blocked before it reaches the model.
	before := len(seen)
	_, err = s.Run(context.Background(), "please do the forbidden thing")
	if !errors.Is(err, session.ErrPromptBlocked) || !strings.Contains(err.Error(), "no forbidden words") {
		t.Fatalf("a hook that exits 2 must block the prompt with its reason, got %v", err)
	}
	if len(seen) != before {
		t.Error("a blocked prompt reached the model")
	}
	// SessionStart ran once, at the first prompt only.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the SessionEnd hook did not run: %v", err)
	}
}

func TestHooksReceiveTheClaudeCodePayloadAndNoKeys(t *testing.T) {
	repo := newRepo(t)
	dump := filepath.Join(t.TempDir(), "payload.json")
	envDump := filepath.Join(t.TempDir(), "env.txt")
	t.Setenv("SLEIPNIR_TEST_PROVIDER_KEY", "canary-key-value")
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Config = hookConfig(t, map[string]string{"UserPromptSubmit": "cat > " + dump + "; env > " + envDump})
	o.Config.Providers = map[string]config.Provider{"p": {BaseURL: "http://x", APIKeyEnv: "SLEIPNIR_TEST_PROVIDER_KEY"}}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "hello hooks"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload is not JSON: %s", b)
	}
	if p["hook_event_name"] != "UserPromptSubmit" || p["prompt"] != "hello hooks" || p["session_id"] != s.ID {
		t.Errorf("payload: %v", p)
	}
	env, _ := os.ReadFile(envDump)
	if strings.Contains(string(env), "canary-key-value") {
		t.Error("a hook saw the provider's API key")
	}
}

func TestUntrustedProjectHooksDoNotRun(t *testing.T) {
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "pwned")
	hooks := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"touch ` + marker + `"}]}]}}`
	if err := os.MkdirAll(filepath.Join(repo, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sleipnir", "config.json"), []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.TrustProject = false // the repository's config is loaded, but its hooks are not honoured
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a hook from an untrusted repository ran")
	}
}

func TestBadHookConfigurationDisablesHooksNotTheSession(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	cfg := config.Defaults()
	cfg.Hooks = map[string]json.RawMessage{"PreToolUse": json.RawMessage(`"not a list"`)}
	o.Config = cfg
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatalf("a malformed hooks block must not stop the session: %v", err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
}
