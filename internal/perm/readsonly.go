package perm

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// ReadsOnly reports whether a shell command line, judged by its text alone, only reads and prints: it parses with
// confidence, every command it runs (in lists, pipes, loops, substitutions and the strings given to sh -c) is an
// invocation on default mode's read-only allowlist (cat, sed -n, grep, find without -exec, git log, cd, echo, ...), no
// assignment steers what a program runs or loads (PATH, LD_PRELOAD, GIT_*), and every redirection reads a file,
// duplicates a descriptor or discards output into /dev/null.
//
// It knows nothing of the workspace, the variables or the directory the line runs in, so a read outside the project is
// still a read. It is not a permission: what may run is decided by an Engine. It tells inspection from work, as a team's
// stall sweep does: `cd web && echo "== a.ts ==" && sed -n '1,80p' src/a.ts | head -40 2>/dev/null` only reads, and
// `npx tsc --noEmit`, `go test ./...` or `sed -i s/a/b/ x` may do more.
func ReadsOnly(cmd string) bool {
	if strings.TrimSpace(cmd) == "" {
		return false
	}
	an := shellparse.Parse(cmd)
	if !an.Parsed || len(an.Commands) == 0 {
		return false
	}
	for _, s := range an.Commands {
		for _, r := range s.Redirects {
			if redirectWrites(r) {
				return false
			}
		}
		for _, env := range s.Env {
			if envDangerous(env) {
				return false
			}
		}
		if s.Program == "" {
			continue // an assignment, or a redirection of a compound command (judged above)
		}
		if d, ok := declaration(s); ok && !dynamicProgram(s.Program) {
			for _, a := range d.assigns {
				if envDangerous(a) {
					return false
				}
			}
			continue // export a=1, readonly a=1: no file changes
		}
		name := shellparse.ProgramName(s.Program)
		if name == "" || dynamicProgram(s.Program) || !analyseSafe(name, s.Args).ok {
			return false
		}
	}
	return true
}

// redirectWrites reports whether a redirection may write a file: output to anything but /dev/null. Input, here-documents
// and descriptor duplication (2>&1, >&-) do not.
func redirectWrites(r shellparse.Redirect) bool {
	switch strings.TrimLeft(r.Op, "0123456789") {
	case ">", ">>", ">|", "&>", "&>>", "<>":
		return r.Target != "/dev/null"
	case ">&":
		return !allDigits(r.Target) && r.Target != "-" && r.Target != "/dev/null"
	}
	return false
}
