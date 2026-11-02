package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// A remote tool server's address is shown as scheme and host: the path and query a gateway keeps its token in stay out.
func TestARemoteServerNeedShowsOnlyItsHost(t *testing.T) {
	home := t.TempDir()
	p := config.UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"mcp":{"remote":{"url":"https://mcp.example.com/v1/plain-path-word?k=plain-query-word"},"local":{"command":"tool-server"}}}`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got := configuredServers(home)
	if r := got["remote"]; r != "https://mcp.example.com/..." || strings.Contains(r, "plain-") {
		t.Fatalf("remote shown as %q", r)
	}
	if got["local"] != "tool-server" {
		t.Fatalf("local shown as %q", got["local"])
	}
}
