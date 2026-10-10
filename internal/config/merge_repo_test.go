package config

import (
	"reflect"
	"strings"
	"testing"
)

// A repository's configuration adds to the user's deny and ask rules and hooks; it cannot remove them by nulling, emptying or
// replacing the section that holds them.
func TestRepositoryCannotUnsetTheSectionsThatHoldDenyAskAndHooks(t *testing.T) {
	user := `{"permissions":{"deny":["Read(~/.ssh/**)"],"ask":["Bash(git push:*)"],"roles":{"tester":{"deny":["Bash(rm:*)"]}}},
		"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh"}]}]}}`
	for _, tc := range []struct{ name, project string }{
		{"permissions null", `{"permissions":null}`},
		{"roles null", `{"permissions":{"roles":null}}`},
		{"role null", `{"permissions":{"roles":{"tester":null}}}`},
		{"hooks null", `{"hooks":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, err := Load(layerFixture(t, user, tc.project, ""))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if !reflect.DeepEqual(cfg.Permissions.Deny, []string{"Read(~/.ssh/**)"}) {
				t.Errorf("deny %q", cfg.Permissions.Deny)
			}
			if !reflect.DeepEqual(cfg.Permissions.Ask, []string{"Bash(git push:*)"}) {
				t.Errorf("ask %q", cfg.Permissions.Ask)
			}
			if got := cfg.Permissions.Roles["tester"].Deny; !reflect.DeepEqual(got, []string{"Bash(rm:*)"}) {
				t.Errorf("role deny %q", got)
			}
			if !strings.Contains(string(cfg.Hooks["PreToolUse"]), "guard.sh") {
				t.Errorf("hooks %s", cfg.Hooks["PreToolUse"])
			}
		})
	}
}

// A scalar or a list in place of one of those sections is a load error that names the file and the field, not a silent replacement.
func TestRepositoryReplacingAProtectedSectionIsALoadError(t *testing.T) {
	user := `{"permissions":{"deny":["Read(~/.ssh/**)"]}}`
	for _, project := range []string{`{"permissions":"none"}`, `{"permissions":[]}`, `{"permissions":{"roles":{"tester":false}}}`, `{"hooks":0}`} {
		if _, _, err := Load(layerFixture(t, user, project, "")); err == nil || !strings.Contains(err.Error(), "expected an object") {
			t.Errorf("%s: err %v", project, err)
		}
	}
}
