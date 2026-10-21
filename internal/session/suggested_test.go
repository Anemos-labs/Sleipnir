package session_test

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A provider's suggested_model names the model a picker starts on: by its id or the start of it, so that a new version is still found;
// a configuration that tunes the built-in provider keeps its suggestion, and one of its own replaces it.
func TestSuggestedModel(t *testing.T) {
	refs := []string{"heimdallm/acme/a-1", "heimdallm/meta-models/muse-spark-open-816b-a42b", "openrouter/meta-models/muse-spark-x"}
	if got := session.SuggestedModel(config.Defaults(), refs); got != "heimdallm/meta-models/muse-spark-open-816b-a42b" {
		t.Errorf("built-in suggestion: %q", got)
	}
	if got := session.SuggestedModel(config.Defaults(), refs[:1]); got != "" {
		t.Errorf("no listed model is suggested: %q", got)
	}
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"heimdallm": {AllowHosts: []string{"gw.example"}}}
	if got := session.SuggestedModel(cfg, refs); got != "heimdallm/meta-models/muse-spark-open-816b-a42b" {
		t.Errorf("an entry that tunes the built-in keeps its suggestion: %q", got)
	}
	cfg.Providers = map[string]config.Provider{"heimdallm": {SuggestedModel: "acme/"}}
	if got := session.SuggestedModel(cfg, refs); got != "heimdallm/acme/a-1" {
		t.Errorf("the configuration's suggestion: %q", got)
	}
}
