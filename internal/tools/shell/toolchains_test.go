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

// On Windows the advice names the interpreter that is installed and the environment's Scripts directory, and the lookup searches the
// session's PATH with PATHEXT (the command shell's rules), not the harness process's PATH.
func TestToolchainContextOnWindows(t *testing.T) {
	text := toolchainText("windows", func(name string) bool { return name == "python" || name == "git" })
	if !strings.Contains(text, `python -m venv .venv, then .venv\Scripts\pip install`) || !strings.Contains(text, `.venv\Scripts\python -m <tool>`) {
		t.Errorf("Windows advice: %q", text)
	}
	if unix := toolchainText("linux", func(name string) bool { return name == "python3" }); !strings.Contains(unix, "python3 -m venv .venv, then .venv/bin/pip install") {
		t.Errorf("Unix advice: %q", unix)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node.exe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "make.CMD"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	pathList := `C:\nowhere;` + dir
	for name, want := range map[string]bool{"node": true, "make": true, "go": false} {
		if got := onPath("windows", name, pathList, ""); got != want {
			t.Errorf("onPath(windows, %s) = %v, want %v", name, got, want)
		}
	}
	if onPath("windows", "node", pathList, ".BAT") {
		t.Error("PATHEXT without .EXE still found node.exe")
	}
}

// A relative PATH entry names a different directory for each agent's working directory, so the session-wide list does not count
// what it would find (here: an executable in the harness's own working directory).
func TestToolchainContextSkipsRelativePathEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by the Windows table below")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pytest"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if onPath("linux", "pytest", ".", "") || onPath("linux", "pytest", "bin:.", "") {
		t.Error("a relative PATH entry was resolved against the process's working directory")
	}
	if !onPath("linux", "pytest", dir, "") {
		t.Error("the absolute entry was not searched")
	}
	for dir, want := range map[string]bool{`C:\tools`: true, `\\srv\share`: true, `\tools`: true, `tools`: false, `.\bin`: false, `C:tools`: false} {
		if got := isAbsFor("windows", dir); got != want {
			t.Errorf("isAbsFor(windows, %q) = %v, want %v", dir, got, want)
		}
	}
}
