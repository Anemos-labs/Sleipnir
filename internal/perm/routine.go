package perm

import (
	"path"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// A routine question is one that a delegate (Config.Delegate: in a team, the manager) may settle before a person is asked. It is a
// question the mode puts (no Ask rule, no built-in protection, nothing the engine or the tool marks high risk), about an action that
// stays inside the workspace: every file it touches is resolved, inside a workspace root (an agent's own tree, the session's
// $TMPDIR), and it reaches no network but this machine. What the engine cannot see through (a substitution, a variable that steers
// what runs, a path only known when the command runs) is never routine, and neither are the programs whose effects are not files of
// the workspace: package installs and downloads, signals to other processes, services and schedulers, containers. A program is judged
// by its base name wherever it is run from (.venv/bin/pip, /usr/bin/pip3). The one install that is routine is the one the tests preset
// allows, into a virtual environment of the workspace from the package index (venv.go); the question then says it reaches the index
// (Request.PackageIndex). Everything else goes to the person, as without a delegate.

// unroutinePrograms are programs whose effects reach beyond the files of the workspace whatever their arguments.
var unroutinePrograms = map[string]bool{
	"kill": true, "pkill": true, "killall": true, "systemctl": true, "service": true, "launchctl": true, "crontab": true, "at": true,
	"docker": true, "podman": true, "kubectl": true, "helm": true, "apt": true, "apt-get": true, "dpkg": true, "brew": true,
	"dnf": true, "yum": true, "pacman": true, "apk": true, "snap": true, "npx": true, "pnpx": true, "bunx": true, "pipx": true,
	"sudo": true, "doas": true, "su": true, "chown": true, "mount": true, "umount": true,
}

// installSubcommands are, per program, the subcommands that install, remove or download packages.
var installSubcommands = map[string][]string{
	"npm": {"install", "i", "ci", "add", "update", "upgrade", "exec", "publish"}, "pnpm": {"install", "i", "add", "update", "dlx", "publish"},
	"yarn": {"install", "add", "upgrade", "dlx", "publish"}, "bun": {"install", "i", "add", "update", "x"},
	"pip": {"install", "download", "uninstall"}, "pip3": {"install", "download", "uninstall"}, "uv": {"pip", "add", "sync", "tool", "run"}, "poetry": {"install", "add", "update"},
	"go": {"get", "install"}, "cargo": {"install", "add", "update", "fetch", "publish"}, "gem": {"install", "update"},
	"bundle": {"install", "update"}, "composer": {"install", "require", "update"}, "dotnet": {"add", "restore", "tool"},
	"mvn": {"install", "deploy", "dependency:get"}, "python": {"-m"}, "python3": {"-m"},
}

// programBase is the name a command word runs a program of: its last path element (.venv/bin/pip is pip), or "" for a word the
// shell builds when it runs.
func programBase(prog string) string {
	if prog == "" || dynamicProgram(prog) {
		return ""
	}
	return path.Base(prog)
}

// installs reports whether a command installs, removes or downloads packages: a subcommand of installSubcommands, and for python -m the
// module pip. prog is the program's base name (programBase); python3.12 and pip3.12 are python and pip3.
func installs(prog string, args []string) bool {
	if shellparse.InterpreterName(prog) == "python" {
		prog = "python"
	} else if pipName(prog) {
		prog = "pip3"
	}
	subs, ok := installSubcommands[prog]
	if !ok || len(args) == 0 {
		return false
	}
	for i, arg := range args {
		if prog == "python" || prog == "python3" {
			if arg == "-m" && i+1 < len(args) && (args[i+1] == "pip" || args[i+1] == "ensurepip") {
				return true
			}
			continue
		}
		for _, s := range subs {
			if arg == s {
				return true
			}
		}
	}
	return false
}

// noteRoutine records whether one unit keeps the request routine (see the head of this file); the evaluator's request is routine
// only while every unit is.
func (ev *evaluator) noteRoutine(u *unit) {
	if ev.unroutine {
		return
	}
	ev.unroutine = !ev.unitRoutine(u)
}

// unitRoutine is noteRoutine's judgement of one unit.
func (ev *evaluator) unitRoutine(u *unit) bool {
	if u.dyn != "" || u.envBad != "" || u.high != nil {
		return false
	}
	if u.tool {
		r := u.req
		if u.network || len(u.accesses) == 0 && (r.Writes || strings.HasPrefix(normTool(r.Tool), "mcp")) {
			return false
		}
	} else if s := u.simple; s != nil && s.Program != "" {
		// The program's base name decides, whatever directory it is run from: .venv/bin/pip install is an install, /usr/bin/pip3 too.
		base := programBase(s.Program)
		if s.Elevated() || unroutinePrograms[base] || u.venv != nil && !u.venv.ok() || installs(base, s.Args) && !u.venv.ok() {
			return false
		}
		if u.network && !((u.name == "curl" || u.name == "wget") && loopbackHTTP(u.name, s.Args)) {
			return false
		}
	}
	for _, a := range u.allAccesses() {
		if a.dynamic || a.invalid != "" || a.real == "" || !ev.rs.inWorkspace(a.real) {
			return false
		}
	}
	return true
}
