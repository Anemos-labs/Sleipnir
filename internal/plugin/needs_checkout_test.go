//go:build unix

package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A wildcard default location takes the program of an unpacked release, never one inside a repository checkout that the
// wildcard happens to match, at the matched directory or above the program.
func TestDefaultPathSkipsCheckouts(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"tool-1/bin/tool", "tool-2/bin/tool", "tool-3/bin/tool"} {
		f := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []string{"tool-2/.git", "tool-3/bin/.git"} {
		if err := os.Mkdir(filepath.Join(dir, g), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	n := Need{ID: "tool", Commands: []string{"tool"}, Paths: map[string][]string{runtime.GOOS: {filepath.Join(dir, "tool-*", "bin", "tool")}}}
	if got, want := defaultPath(runtime.GOOS, n), filepath.Join(dir, "tool-1", "bin", "tool"); got != want {
		t.Errorf("defaultPath = %q, want the release %q", got, want)
	}
}
