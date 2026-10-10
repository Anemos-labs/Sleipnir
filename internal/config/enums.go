package config

import (
	"slices"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// Dialects lists the provider dialects Validate accepts (providers.<name>.dialect).
func Dialects() []string { return slices.Clone(dialects) }

// PermModes lists the permission modes Validate accepts (permissions.mode), in the order the engine documents them.
func PermModes() []perm.Mode { return slices.Clone(permModes) }

// CacheTTLs lists the lifetimes cache.shared_ttl accepts.
func CacheTTLs() []string { return slices.Clone(cacheTTLs) }

// Isolations lists the values swarm.isolation accepts.
func Isolations() []string { return slices.Clone(isolations) }

// TestsPreset is the name that stands for the build and test commands of most projects wherever an allow rule is taken (--allow,
// /allow, the web's rule form): perm.TestsAllow.
const TestsPreset = "tests"

// TestsPresetSummary says in one line what the tests preset allows.
const TestsPresetSummary = "go, cargo, npm, pnpm, yarn, pytest, unittest, mvn, gradle, dotnet and make: test, build, check, lint and vet, and go mod init and tidy, never install or run"

// ExpandAllow turns the names of sets of rules into the rules (TestsPreset into perm.TestsAllow, surrounding spaces ignored);
// anything else is a rule as it is written. The result is a new slice.
func ExpandAllow(in []string) []string {
	var out []string
	for _, r := range in {
		if strings.TrimSpace(r) == TestsPreset {
			out = append(out, perm.TestsAllow...)
			continue
		}
		out = append(out, r)
	}
	return out
}
