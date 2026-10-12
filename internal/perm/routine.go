package perm

import "strings"

// A routine question is one that a delegate (Config.Delegate: in a team, the manager) may settle before a person is asked. It is a
// question the mode puts (no Ask rule, no built-in protection, nothing the engine or the tool marks high risk), about an action that
// stays inside the workspace: every file it touches is resolved, inside a workspace root (an agent's own tree, the session's
// $TMPDIR), and it reaches no network but this machine. What the engine cannot see through (a substitution, a variable that steers
// what runs, a path only known when the command runs) is never routine, and neither are the programs whose effects are not files of
// the workspace: package installs and downloads, signals to other processes, services and schedulers, containers. Everything else
// goes to the person, as without a delegate.

// unroutinePrograms are programs whose effects reach beyond the files of the workspace whatever their arguments.
var unroutinePrograms = map[string]bool{
	"kill": true, "pkill": true, "killall": true, "systemctl": true, "service": true, "launchctl": true, "crontab": true, "at": true,
	"docker": true, "podman": true, "kubectl": true, "helm": true, "apt": true, "apt-get": true, "dpkg": true, "brew": true,
	"dnf": true, "yum": true, "pacman": true, "apk": true, "snap": true, "npx": true, "pnpx": true, "bunx": true, "pipx": true,
	"sudo": true, "doas": true, "su": true, "chown": true, "mount": true, "umount": true,
}

// installSubcommands are, per program, the subcommands that install or download packages.
var installSubcommands = map[string][]string{
	"npm": {"install", "i", "ci", "add", "update", "upgrade", "exec", "publish"}, "pnpm": {"install", "i", "add", "update", "dlx", "publish"},
	"yarn": {"install", "add", "upgrade", "dlx", "publish"}, "bun": {"install", "i", "add", "update", "x"},
	"pip": {"install", "download"}, "pip3": {"install", "download"}, "uv": {"pip", "add", "sync", "tool", "run"}, "poetry": {"install", "add", "update"},
	"go": {"get", "install"}, "cargo": {"install", "add", "update", "fetch", "publish"}, "gem": {"install", "update"},
	"bundle": {"install", "update"}, "composer": {"install", "require", "update"}, "dotnet": {"add", "restore", "tool"},
	"mvn": {"install", "deploy", "dependency:get"}, "python": {"-m"}, "python3": {"-m"},
}

// installs reports whether a command installs or downloads packages: a subcommand of installSubcommands, and for python -m the
// module pip.
func installs(prog string, args []string) bool {
	subs, ok := installSubcommands[prog]
	if !ok || len(args) == 0 {
		return false
	}
	if prog == "python" || prog == "python3" {
		return len(args) > 1 && args[0] == "-m" && (args[1] == "pip" || args[1] == "ensurepip")
	}
	for _, s := range subs {
		if args[0] == s {
			return true
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
		if s.Elevated() || unroutinePrograms[u.name] || installs(u.name, s.Args) {
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
