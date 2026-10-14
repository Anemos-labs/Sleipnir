package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// probedCommands are the commands ToolchainContext looks for, in the order it lists them: version control and build drivers, then
// one group per language ecosystem, then containers and databases. Each is a command agents commonly need to build, test or run a
// project, so whether it is installed decides how an agent verifies its work.
var probedCommands = []string{
	"git", "gh", "make", "cmake",
	"go", "cargo", "rustc",
	"node", "npm", "pnpm", "yarn", "bun", "deno",
	"python3", "python", "pip3", "pytest", "uv",
	"ruby", "bundle",
	"java", "mvn", "gradle", "dotnet",
	"gcc", "g++", "clang",
	"docker", "sqlite3",
}

// ToolchainContext says which of the commonly needed commands are on the PATH that commands run with, and which are not, so an
// agent knows before it writes a test whether it can run it. It only looks the names up (nothing is run), lists them in a fixed
// order and names no directory, so the text is the same for every agent of a session and stable across the session's requests.
func (m *Manager) ToolchainContext() string {
	pathList := os.Getenv("PATH")
	if m.opts.BaseEnv != nil {
		pathList = envValue(m.opts.BaseEnv, "PATH")
	}
	var found, missing []string
	for _, name := range probedCommands {
		if onPath(name, pathList) {
			found = append(found, name)
		} else {
			missing = append(missing, name)
		}
	}
	var b strings.Builder
	b.WriteString("Commands on PATH: ")
	b.WriteString(listOrNone(found))
	b.WriteString(".\nNot on PATH: ")
	b.WriteString(listOrNone(missing))
	b.WriteString(".\nA missing tool the project needs is set up inside the project, never system-wide: for Python, python3 -m venv .venv, then .venv/bin/pip install the tool and run it with .venv/bin/python -m.")
	return b.String()
}

// listOrNone joins names with commas, or says "none".
func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// envValue is the value of key in env (KEY=value entries; the last one wins, as in exec), "" when absent. Keys compare
// case-insensitively on Windows.
func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		k, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == key || (runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			v = val
		}
	}
	return v
}

// onPath reports whether name is an executable file in one of the directories of pathList. On Windows it defers to exec.LookPath,
// which applies PATHEXT; elsewhere it checks each directory for a regular file with an execute bit.
func onPath(name, pathList string) bool {
	if runtime.GOOS == "windows" {
		_, err := exec.LookPath(name)
		return err == nil
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, name))
		if err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return true
		}
	}
	return false
}
