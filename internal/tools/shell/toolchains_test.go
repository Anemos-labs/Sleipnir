package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The toolchain list names what is installed on the commands' PATH and what is not, in a fixed order, without a directory: an agent
// that reads "pytest" under "Not on PATH" sets up a project environment before writing tests it could not run.
func TestToolchainContextListsWhatIsInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH lookup on Windows goes through exec.LookPath and PATHEXT")
	}
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{"pytest": 0o755, "go": 0o755, "cargo": 0o644} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "node"), 0o755); err != nil { // a directory is not a command
		t.Fatal(err)
	}
	m := NewManager(Options{BaseEnv: []string{"HOME=/nowhere", "PATH=" + dir}})
	defer m.Shutdown()
	text := m.ToolchainContext()
	if !strings.HasPrefix(text, "Commands on PATH: go, pytest.\n") {
		t.Errorf("installed commands, in probe order: %q", text)
	}
	missing := text[strings.Index(text, "Not on PATH: "):]
	for _, want := range []string{"cargo", "node", "python3", "git"} {
		if !strings.Contains(missing, want) {
			t.Errorf("%s (not executable, a directory or absent) is not listed as missing: %q", want, text)
		}
	}
	if strings.Contains(text, dir) {
		t.Errorf("the text names a directory: %q", text)
	}
	if !strings.Contains(text, "python3 -m venv .venv") {
		t.Errorf("no advice for a missing tool: %q", text)
	}
	if again := m.ToolchainContext(); again != text {
		t.Errorf("not deterministic: %q then %q", text, again)
	}
	empty := NewManager(Options{BaseEnv: []string{"PATH="}})
	defer empty.Shutdown()
	if got := empty.ToolchainContext(); !strings.HasPrefix(got, "Commands on PATH: none.\n") {
		t.Errorf("an empty PATH: %q", got)
	}
}
