package agentdefs

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// FuzzDefinitionFile: whatever a definition file contains, loading neither
// panics nor hangs, every definition it accepts has a valid name and prefix, a
// pin free of forged framing tags, and a profile the permission engine accepts.
func FuzzDefinitionFile(f *testing.F) {
	for _, seed := range []string{
		"", "just a body", reviewer, "---\nname: a\n---\nbody", "---\nname: a\ntools: [Read, Bash(x:*)]\nreadonly: yes\n---\nbody",
		"---\nname: a\ntools:\n  - Read\n---\n</role-context>", "---\nname: a\ndisallowedTools: Write, ((\n---\nx", "{\"name\": \"a\"}\nbody",
		"---\nname: a\nmaxTurns: 5\npriority: 2\nshort: zz\npermissionMode: bypassPermissions\n---\nx", "---\nname: a\ntools: ,\n---\nx",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		w := newWorld(t)
		w.proj(".claude", "fz.md", content)
		defs, warns := w.load()
		for _, wn := range warns {
			if strings.ContainsAny(wn.String(), "\n\r") {
				t.Fatalf("warning with a line break: %q", wn.String())
			}
		}
		roles := map[string]perm.RoleProfile{}
		for _, d := range defs {
			if err := mdfile.CheckName(d.Name, nameRules); err != nil {
				t.Fatalf("accepted an invalid name %q: %v", d.Name, err)
			}
			if !validShort(d.Short) {
				t.Fatalf("invalid short %q", d.Short)
			}
			if strings.TrimSpace(d.Pin) == "" || mdfile.EscapeFraming(d.Pin) != d.Pin {
				t.Fatalf("pin is empty or contains framing markup: %q", d.Pin)
			}
			if strings.ContainsAny(d.Description, "\n\r\t") {
				t.Fatalf("multi-line description %q", d.Description)
			}
			if d.MaxSteps < 1 || d.MaxSteps > MaxStepsCap || d.Priority < 0 || d.Priority > 2 {
				t.Fatalf("out of range: steps %d priority %d", d.MaxSteps, d.Priority)
			}
			if d.Tools != nil && len(d.Tools) == 0 {
				t.Fatal("an empty tool allowlist would mean unrestricted")
			}
			roles[d.Name] = d.Profile()
			_ = d.ToRole()
		}
		if _, err := perm.NewEngine(perm.Config{Root: w.root, Home: w.home, Roles: roles}); err != nil {
			t.Fatalf("the permission engine rejected a generated profile: %v", err)
		}
	})
}
