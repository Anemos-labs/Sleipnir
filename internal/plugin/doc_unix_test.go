//go:build unix

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A plugin's documents are read inside its directory, only as regular files, and only up to a bound: a link out of the plugin,
// a pipe and an oversized file are refused rather than read.
func TestPluginDocumentsAreBoundedRegularFilesInside(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "plugin.json")
	if err := os.WriteFile(outside, []byte(`{"id":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, ManifestFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil {
		t.Error("a manifest linked from outside the plugin was read")
	}

	dir = t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ManifestFile), 0o644); err != nil {
		t.Skip(err)
	}
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a pipe: %v", err)
	}

	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"id":"x","description":"`+strings.Repeat("a", maxDocBytes)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("an oversized manifest: %v", err)
	}
	if _, err := MCPServers(Manifest{Dir: dir}, nil); err != nil {
		t.Errorf("no MCP file: %v", err)
	}
}
