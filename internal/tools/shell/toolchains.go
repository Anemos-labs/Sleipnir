package shell

import (
	"os"
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
// agent knows before it writes a test whether it can run it, and how to set up a missing Python tool on this machine. It only looks
// the names up (nothing is run), lists them in a fixed order and names no directory, so the text is the same for every agent of a
// session and stable across the session's requests.
func (m *Manager) ToolchainContext() string {
	pathList, pathExt := os.Getenv("PATH"), os.Getenv("PATHEXT")
	if m.opts.BaseEnv != nil {
		pathList, pathExt = envValue(m.opts.BaseEnv, "PATH"), envValue(m.opts.BaseEnv, "PATHEXT")
	}
	return toolchainText(runtime.GOOS, func(name string) bool { return onPath(runtime.GOOS, name, pathList, pathExt) })
}

// toolchainText renders the toolchain list for an operating system, given which commands are installed.
func toolchainText(goos string, installed func(string) bool) string {
	var found, missing []string
	have := map[string]bool{}
	for _, name := range probedCommands {
		if installed(name) {
			found = append(found, name)
			have[name] = true
		} else {
			missing = append(missing, name)
		}
	}
	py, bin := "python3", ".venv/bin/"
	if !have["python3"] && have["python"] {
		py = "python"
	}
	if goos == "windows" {
		bin = `.venv\Scripts\`
	}
	var b strings.Builder
	b.WriteString("Commands on PATH: ")
	b.WriteString(listOrNone(found))
	b.WriteString(".\nNot on PATH: ")
	b.WriteString(listOrNone(missing))
	b.WriteString(".\nA missing tool the project needs is set up inside the project, never system-wide. For Python on this machine: " +
		py + " -m venv .venv, then " + bin + "pip install the tool (or -r requirements.txt), and run it with " + bin + "python -m <tool>.")
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

// onPath reports whether name is a command in one of the absolute directories of pathList on goos. On Windows a command is a file
// whose name is name plus one of the extensions of pathExt (PATHEXT; ".COM;.EXE;.BAT;.CMD" when empty), as the shell would find it;
// elsewhere it is a regular file with an execute bit. A relative entry (".", "node_modules/.bin") is skipped: it names a different
// directory for every agent's working directory, and the list is one text shared by the whole session.
func onPath(goos, name, pathList, pathExt string) bool {
	sep := string(os.PathListSeparator)
	if goos == "windows" {
		sep = ";"
		if pathExt == "" {
			pathExt = ".COM;.EXE;.BAT;.CMD"
		}
	}
	for _, dir := range strings.Split(pathList, sep) {
		if dir == "" || !isAbsFor(goos, dir) {
			continue
		}
		if goos != "windows" {
			fi, err := os.Stat(filepath.Join(dir, name))
			if err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
				return true
			}
			continue
		}
		for _, ext := range strings.Split(pathExt, ";") {
			if ext == "" {
				continue
			}
			if fi, err := os.Stat(filepath.Join(dir, name+strings.ToLower(ext))); err == nil && fi.Mode().IsRegular() {
				return true
			}
			if fi, err := os.Stat(filepath.Join(dir, name+ext)); err == nil && fi.Mode().IsRegular() {
				return true
			}
		}
	}
	return false
}

// isAbsFor reports whether dir names the same directory whatever the working directory and drive: rooted at "/" elsewhere; on
// Windows a drive letter followed by a separator, or a UNC path (two separators). A single leading separator is the current drive's
// root, which differs between agents on different drives.
func isAbsFor(goos, dir string) bool {
	if goos != "windows" {
		return strings.HasPrefix(dir, "/")
	}
	sep := func(c byte) bool { return c == '\\' || c == '/' }
	if len(dir) >= 2 && sep(dir[0]) && sep(dir[1]) {
		return true
	}
	return len(dir) >= 3 && dir[1] == ':' && sep(dir[2])
}
