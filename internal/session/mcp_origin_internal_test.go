package session

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// A repository's MCP entry is found by the loader's spelling of its path, whatever punctuation its name has, and an entry whose
// origin is unknown is not taken for the user's.
func TestMCPProvenanceUsesTheLoadersPath(t *testing.T) {
	rep := &config.Report{
		Layers:  []config.LayerInfo{{Kind: "user", Source: "u"}, {Kind: "project", Source: "p"}},
		Origins: map[string]string{config.Path("mcp", "a.b"): "p", config.Path("mcp", "mine"): "u", "mcp.a": "u"},
	}
	s := &Session{cfgRep: rep}
	for name, want := range map[string]bool{"a.b": true, "mine": false, "unknown": true} {
		if got := s.mcpFromRepository(name); got != want {
			t.Errorf("%q: from the repository = %v, want %v", name, got, want)
		}
	}
	if (&Session{}).mcpFromRepository("x") {
		t.Error("a configuration handed over whole is the caller's")
	}
}
