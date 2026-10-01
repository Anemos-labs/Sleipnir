package session_test

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func anthropicCfg(opts map[string]any) *config.Config {
	return &config.Config{Providers: map[string]config.Provider{
		"claude": {Dialect: config.DialectAnthropic, BaseURL: "https://gateway.example/api/v1", APIKeyEnv: "SLEIPNIR_TEST_ANTHROPIC_KEY", Options: opts},
	}}
}

func TestBuildProviderAnthropicDialect(t *testing.T) {
	t.Setenv("SLEIPNIR_TEST_ANTHROPIC_KEY", "test-key-value")
	p, m, err := session.BuildProvider(anthropicCfg(nil), session.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, session.ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prof := p.Profile()
	if prof.Dialect != "anthropic" {
		t.Fatalf("dialect %q", prof.Dialect)
	}
	if prof.Cache.MaxBreakpoints != 4 || !prof.ReplayThinking || !prof.TurnScopedSystem {
		t.Errorf("the first-party profile should have explicit breakpoints, thinking replay and turn-scoped system messages: %+v", prof)
	}
	if m.ID == "" || m.ContextTokens == 0 {
		t.Errorf("model description: %+v", m)
	}
}

func TestBuildProviderAnthropicGatewayOptionsNarrowTheProfile(t *testing.T) {
	t.Setenv("SLEIPNIR_TEST_ANTHROPIC_KEY", "test-key-value")
	p, _, err := session.BuildProvider(anthropicCfg(map[string]any{
		"auth_style": "bearer", "cache_control": false, "no_turn_scoped_system": true, "no_thinking_replay": true,
	}), session.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, session.ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prof := p.Profile()
	if prof.Cache.MaxBreakpoints != 0 {
		t.Errorf("a gateway that drops cache_control has no breakpoints to plan: %d", prof.Cache.MaxBreakpoints)
	}
	if prof.TurnScopedSystem || prof.ReplayThinking {
		t.Errorf("declared gateway limits were not applied: %+v", prof)
	}
}

func TestBuildProviderAnthropicNeedsItsKeyAndCannotCaptureTokens(t *testing.T) {
	t.Setenv("SLEIPNIR_TEST_ANTHROPIC_KEY", "")
	if _, _, err := session.BuildProvider(anthropicCfg(nil), session.ModelRef{Provider: "claude", Model: "m"}, session.ProviderOptions{}); err == nil || !strings.Contains(err.Error(), "SLEIPNIR_TEST_ANTHROPIC_KEY") {
		t.Fatalf("a missing key must name the variable: %v", err)
	}
	t.Setenv("SLEIPNIR_TEST_ANTHROPIC_KEY", "test-key-value")
	if _, _, err := session.BuildProvider(anthropicCfg(nil), session.ModelRef{Provider: "claude", Model: "m"}, session.ProviderOptions{CaptureTokens: true}); err == nil || !strings.Contains(err.Error(), "token ids") {
		t.Fatalf("RL capture is impossible on the Messages API and must say so: %v", err)
	}
}

func TestBuildProviderResponsesDialectIsNotBuiltYet(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.Provider{"r": {Dialect: config.DialectOpenAIResponses, BaseURL: "https://x/v1"}}}
	_, _, err := session.BuildProvider(cfg, session.ModelRef{Provider: "r", Model: "m"}, session.ProviderOptions{})
	if err == nil || !strings.Contains(err.Error(), "not built yet") {
		t.Fatalf("got %v", err)
	}
}
