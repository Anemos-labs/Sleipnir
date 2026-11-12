package session_test

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// opencode-go is the OpenCode Go plan: its own endpoint, the OpenCode key, and the session header it requires.
func TestOpenCodeGoIsBuiltIn(t *testing.T) {
	p, ok := session.LookupProvider(config.Defaults(), "opencode-go")
	if !ok {
		t.Fatal("opencode-go is not a built-in provider")
	}
	if p.BaseURL != "https://opencode.ai/zen/go/v1" || p.APIKeyEnv != "OPENCODE_API_KEY" || p.Dialect != config.DialectOpenAIChat {
		t.Fatalf("opencode-go = %+v", p)
	}
	if p.Options["session_header_name"] != "x-opencode-session" {
		t.Fatalf("opencode-go options = %v, want session_header_name x-opencode-session", p.Options)
	}
}
