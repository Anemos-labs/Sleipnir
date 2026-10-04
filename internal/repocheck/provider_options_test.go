package repocheck

import (
	"regexp"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// The provider-option reference must cover the validated option inventory and
// must not retain renamed or removed keys. Builder tests check the other side
// of this contract: accepted keys must have a consumer.
func TestProviderOptionReferenceMatchesConfiguration(t *testing.T) {
	doc := read(t, "docs/CONFIGURATION.md")
	_, section, found := strings.Cut(doc, "## 7. Provider options\n")
	if !found {
		t.Fatal("provider option reference section is missing")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	want := map[string]bool{}
	for _, dialect := range []string{config.DialectOpenAIChat, config.DialectAnthropic} {
		for _, name := range config.ProviderOptionNames(dialect) {
			want[name] = true
		}
	}
	seen := map[string]bool{}
	for _, row := range regexp.MustCompile("(?m)^\\| `([a-z][a-z0-9_]+)` \\|").FindAllStringSubmatch(section, -1) {
		name := row[1]
		seen[name] = true
		if !want[name] {
			t.Errorf("docs/CONFIGURATION.md documents unsupported option %q", name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("docs/CONFIGURATION.md is missing supported provider option %q", name)
		}
	}
}
