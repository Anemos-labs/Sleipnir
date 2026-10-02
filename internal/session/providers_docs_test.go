package session_test

import (
	"os"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// docs/PROVIDERS.md says, for every provider that is built in, what its key variable is and what has been verified: a provider added to the
// table and not to the page would be one the page does not know.
func TestEveryBuiltInProviderIsOnTheProvidersPage(t *testing.T) {
	b, err := os.ReadFile("../../docs/PROVIDERS.md")
	if err != nil {
		t.Skipf("not a checkout of the repository: %v", err)
	}
	page := string(b)
	for _, name := range session.ProviderNames(nil) {
		if !strings.Contains(page, "`"+name+"`") {
			t.Errorf("the provider %q is built in and docs/PROVIDERS.md does not name it", name)
		}
		if _, env, ok := session.ProviderInfo(nil, name); ok && env != "" && !strings.Contains(page, "`"+env+"`") {
			t.Errorf("docs/PROVIDERS.md does not give %s, the key variable of %q", env, name)
		}
	}
}
