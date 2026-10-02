package shell

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Working-directory persistence.
//
// Every command is a fresh shell, yet models expect `cd sub` to stick, as it
// does in a terminal. The Manager therefore remembers, per agent, where the
// last foreground command ended and starts the next one there. The final
// directory is captured by an EXIT trap that writes `pwd -P` to a private temp
// file: nothing is added to the command's output, and it runs on every normal
// exit path (end of script, `exit N`, `set -e` failures).
//
// A command that installs its own EXIT trap or `exec`s replaces ours; the file
// then stays empty and the previous directory is kept, which is the safe way
// to fail.
//
// The directory is never allowed to wander outside the project: a persisted
// directory outside Env.Root (and outside the agent's own Cwd, which in
// isolated mode is a worktree that may live elsewhere) is discarded and the
// agent is put back in Env.Cwd. This is a guard rail against getting lost, not
// a sandbox: the command itself can still touch anything permissions allow.

// cwdState is the remembered directory of one agent. base records the Env.Cwd
// it was derived from, so a change of worktree invalidates it.
type cwdState struct {
	base string
	dir  string
}

// wrapScript prefixes command with the trap that records the final directory.
// The prefix stays on the command's first line so error messages keep their
// line numbers. `>|` overrides a `set -C` the command may have enabled.
func wrapScript(command, cwdFile string) string {
	return "__sleipnir_cwd=" + shQuote(cwdFile) + `; trap 'pwd -P >|"$__sleipnir_cwd" 2>/dev/null' EXIT; ` + command
}

// shQuote single-quotes s for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// baseDir resolves the agent's configured working directory and verifies it
// exists (the run guard).
func baseDir(env *tools.Env) (string, error) {
	base := env.Cwd
	if base == "" {
		base = env.Root
	}
	if base == "" {
		return "", errNoCwd
	}
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	fi, err := os.Stat(base)
	if err != nil || !fi.IsDir() {
		return "", &missingDirError{dir: base}
	}
	return base, nil
}

type missingDirError struct{ dir string }

func (e *missingDirError) Error() string {
	return "working directory " + e.dir + " does not exist or is not a directory"
}

type noCwdError struct{}

func (noCwdError) Error() string { return "no working directory is configured for this agent" }

var errNoCwd error = noCwdError{}

// startDir returns where the agent's next foreground command should start.
func (m *Manager) startDir(env *tools.Env, base string) string {
	m.mu.Lock()
	st, ok := m.cwds[env.Agent]
	m.mu.Unlock()
	if !ok {
		return base
	}
	if st.base == base && isDir(st.dir) && insideBounds(st.dir, env) {
		return st.dir
	}
	m.mu.Lock()
	delete(m.cwds, env.Agent)
	m.mu.Unlock()
	return base
}

// recordCwd reads the directory the command ended in and updates the agent's
// remembered directory. It returns the directory the agent is now in and, when
// the agent was put back because it left the project, a note for the model.
func (m *Manager) recordCwd(env *tools.Env, base, start, cwdFile string) (dir, note string) {
	data, err := os.ReadFile(cwdFile)
	final := strings.TrimRight(string(data), "\r\n")
	if err != nil || final == "" || !filepath.IsAbs(final) || !isDir(final) {
		return start, "" // trap did not run (exec, own trap, killed): keep going from where we were
	}
	if !insideBounds(final, env) {
		m.mu.Lock()
		delete(m.cwds, env.Agent)
		m.mu.Unlock()
		return base, "[working directory reset to " + printable(base) + ": " + printable(final) + " is outside the project root]"
	}
	m.mu.Lock()
	if final == base {
		delete(m.cwds, env.Agent)
	} else {
		m.cwds[env.Agent] = cwdState{base: base, dir: final}
	}
	m.mu.Unlock()
	return final, ""
}

// printable makes a path safe to embed in a one-line note: a directory name may
// hold newlines, and an outside-the-project path is by definition not one the
// project controls, so it must not be able to forge lines in a tool result.
func printable(p string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xA0) {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(p, "?"))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// insideBounds reports whether dir is within the project root or within the
// agent's own working directory, after resolving symlinks on both sides (a
// symlink inside the project that points out of it must not smuggle the agent
// out). With no Root configured there is nothing to enforce.
func insideBounds(dir string, env *tools.Env) bool {
	if env.Root == "" {
		return true
	}
	real := resolve(dir)
	for _, bound := range []string{env.Root, env.Cwd} {
		if bound != "" && within(real, resolve(bound)) {
			return true
		}
	}
	return false
}

func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// within reports whether path is dir or lies below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// leadingCd matches a command that starts by changing to one directory: cd DIR && ..., cd DIR; ..., or cd DIR alone.
var leadingCd = regexp.MustCompile(`^\s*cd\s+(?:"([^"\n$` + "`" + `]+)"|'([^'\n]+)'|([^\s;&|<>"'$` + "`" + `\\]+))(?:\s+\d?>>?\s*\S+)*\s*(?:&&|;|\n|$)`)

// missingLeadingCd is the answer to a command that starts with a cd to an absolute directory that does not exist, "" for any other
// command. A weak model invents the directory (/Users/someone/project) although it starts in the project and its prompt says so; asking
// the person to approve a read of a place that is not there would be a question with no answer worth giving, so it is refused at once,
// saying where the command runs.
func missingLeadingCd(command, dir, root string) string {
	m := leadingCd.FindStringSubmatch(command)
	if m == nil {
		return ""
	}
	target := m[1] + m[2] + m[3]
	if !filepath.IsAbs(target) {
		// "cd myproject" from inside myproject: the survey names the project and a weak model takes the name for a directory to enter.
		name := strings.TrimSuffix(filepath.ToSlash(target), "/")
		if root != "" && name != "" && !strings.ContainsAny(name, "/.") && name == filepath.Base(root) {
			if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
				return "you are already in the project (" + printable(name) + "): commands run in " + printable(dir) + ". Leave the cd out and use paths relative to it"
			}
		}
		return ""
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		return ""
	}
	return "the directory " + printable(target) + " does not exist. Commands already run in " + printable(dir) + ": leave the cd out and use paths relative to it"
}
