package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMCPConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "servers.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Both shapes the configuration reads are accepted, and a later file's entry replaces an earlier one of the same name.
func TestMCPConfigFlagReadsBothShapesInOrder(t *testing.T) {
	first := writeMCPConfig(t, `{"mcpServers": {"studio": {"command": "first"}, "search": {"command": "search-mcp"}}}`)
	second := writeMCPConfig(t, `{"studio": {"command": "second", "args": ["--port", "1"]}}`)
	flag := mcpConfigFlag{first, second}
	servers, err := flag.servers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers: %v", servers)
	}
	if !strings.Contains(string(servers["studio"]), `"second"`) {
		t.Errorf("the later file did not win: %s", servers["studio"])
	}
	if !strings.Contains(string(servers["search"]), `"search-mcp"`) {
		t.Errorf("the first file's other server was lost: %s", servers["search"])
	}
}

// A run whose caller meant to give it a server refuses to start without it: an unreadable file, a file that is not a JSON
// object and an entry the configuration would refuse are errors that name the file.
func TestMCPConfigFlagRefusesWhatWouldStartWithoutTheServer(t *testing.T) {
	for name, body := range map[string]string{
		"not an object": `["studio"]`,
		"not json":      `{"mcpServers": `,
		"no command":    `{"mcpServers": {"studio": {"args": ["x"]}}}`,
		"bad servers":   `{"mcpServers": ["studio"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeMCPConfig(t, body)
			if _, err := (mcpConfigFlag{path}).servers(); err == nil || !strings.Contains(err.Error(), path) {
				t.Errorf("want an error naming %s, got %v", path, err)
			}
		})
	}
	if _, err := (mcpConfigFlag{filepath.Join(t.TempDir(), "absent.json")}).servers(); err == nil {
		t.Error("a missing file was accepted")
	}
}

// No flag is no servers, not an empty set that would read as "the command line named none".
func TestMCPConfigFlagWithoutFilesNamesNothing(t *testing.T) {
	servers, err := mcpConfigFlag(nil).servers()
	if err != nil || servers != nil {
		t.Errorf("servers %v, err %v", servers, err)
	}
	if err := (&mcpConfigFlag{}).Set("  "); err == nil {
		t.Error("a blank path was accepted")
	}
}
