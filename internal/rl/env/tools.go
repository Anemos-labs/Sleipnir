package env

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// KnownTools are the names rl.Task.Requires may list: each is an executable a
// task's setup or verifier runs, looked up on the PATH its commands get. A
// name outside the list is a validation error, so a typo cannot mark a task
// as needing something no machine has.
var KnownTools = []string{
	"bash", "cargo", "cc", "clang", "deno", "dotnet", "g++", "gcc", "go", "gradle", "java", "javac", "make", "mvn",
	"node", "npm", "npx", "perl", "php", "python3", "ruby", "rustc", "sqlite3", "tsc",
}

// MissingTools lists, in the order the task gives them, the tools of t.Requires
// that are not on the PATH the task's commands run with (the scrubbed one, with
// --set-env and --pass-env applied). A task that needs a missing tool cannot be
// judged here: `rl tasks check` and rollouts skip it instead of reporting an
// unsound task or an infrastructure failure.
func (m *Workspaces) MissingTools(t rl.Task) []string {
	if len(t.Requires) == 0 {
		return nil
	}
	env := EnvMap(m.commandEnv(m.root, m.root, t.Network, ""))
	var missing []string
	for _, name := range t.Requires {
		if !onPath(name, env["PATH"], env["PATHEXT"], runtime.GOOS) {
			missing = append(missing, name)
		}
	}
	return missing
}

// onPath reports whether an executable called name is in one of the
// directories of pathList. On Windows (goos) a name without an extension is
// tried with each extension of pathExt, and a file is executable by existing:
// the app execution aliases that stand for python3.exe and others are reparse
// points that only Lstat can see.
func onPath(name, pathList, pathExt, goos string) bool {
	names := []string{name}
	if goos == "windows" {
		if pathExt == "" {
			pathExt = ".COM;.EXE;.BAT;.CMD"
		}
		exts := strings.Split(pathExt, ";")
		if !slices.ContainsFunc(exts, func(e string) bool { return e != "" && strings.EqualFold(e, filepath.Ext(name)) }) {
			names = names[:0]
			for _, e := range exts {
				if e != "" {
					// Both spellings, for a case-sensitive file system holding Windows tools.
					names = append(names, name+e, name+strings.ToLower(e))
				}
			}
		}
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			continue
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			if goos == "windows" {
				if fi, err := os.Lstat(p); err == nil && !fi.IsDir() {
					return true
				}
				continue
			}
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
				return true
			}
		}
	}
	return false
}
