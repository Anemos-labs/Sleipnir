package perm

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// The reason of a question about a command is read by the person who answers it and, when nobody can, by the model that is refused.
// It says what the command does in plain words, why the mode asks, and what would let it through.

// unlisted is what analyseSafe says of a program it has no entry for.
const unlisted = "not on the read-only allowlist"

// languages names the language of each interpreter shellparse knows.
var languages = map[string]string{
	"python": "Python", "node": "JavaScript", "perl": "Perl", "ruby": "Ruby", "php": "PHP", "lua": "Lua", "Rscript": "R",
	"deno": "JavaScript", "bun": "JavaScript", "tclsh": "Tcl",
}

// unlistedReason is the reason a command that is not on the read-only list asks: what it does, and what allows it in the view's mode.
func (ev *evaluator) unlistedReason(u *unit) string {
	clause := "it asks before running a program that is not read-only"
	switch {
	case ev.v.mode == ModeDefault && ev.testsCover(u):
		clause = "accept-edits mode or the tests preset (--allow tests) allows it"
	case ev.v.mode == ModeAcceptEdits:
		clause = "it allows edits and the build and test commands, not other programs"
	}
	return ev.modeReason(u.label + ": " + ev.does(u) + "; " + clause)
}

// does says in a few words what a command that is not on the read-only list does.
func (ev *evaluator) does(u *unit) string {
	s := u.simple
	base := programBase(s.Program)
	switch {
	case u.venv != nil && u.venv.create:
		return "creates the Python virtual environment " + orDefault(u.venv.dir, ".venv") + becauseNot(u.venv.why)
	case u.venv != nil && u.venv.install:
		return "installs Python packages into the virtual environment " + orDefault(u.venv.dir, "it names") + becauseNot(u.venv.why)
	case u.venv != nil:
		why := u.venv.why
		if why == "" && !ev.accessesInWorkspace(u) {
			why = "it names a file outside the workspace"
		}
		return "runs the tests or checks with the virtual environment " + orDefault(u.venv.dir, "it names") + becauseNot(why)
	case installs(base, s.Args):
		return "installs, removes or downloads packages, outside a virtual environment of the workspace"
	case ev.testsNear(u) != "":
		return "checks the project, but " + ev.testsNear(u)
	case ev.testsCover(u):
		return "builds, checks or tests the project"
	case u.why != "" && u.why != unlisted:
		return u.why
	}
	if lang := languages[shellparse.InterpreterName(base)]; lang != "" {
		return interpreterDoes(lang, base, s.Args)
	}
	switch {
	case base == "sh" || base == "bash" || base == "dash" || base == "zsh" || base == "ksh":
		if hasShort(s.Args, "c") {
			return "runs shell commands given on the command line"
		}
		return "runs a shell script"
	case u.name == "":
		return "runs the program " + s.Program
	}
	return "runs " + base + ", which is not on the read-only list"
}

// becauseNot appends why the tests preset does not allow a command, when it does not.
func becauseNot(why string) string {
	if why == "" {
		return ""
	}
	return "; the tests preset does not allow it, since " + why
}

// orDefault is s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// interpreterDoes says what an interpreter is given to run: a program on its command line (-c, -e), a module (python -m), its
// standard input ("-", a here-document, or nothing), or a script.
func interpreterDoes(lang, base string, args []string) string {
	for i, a := range args {
		switch {
		case a == "-c" || a == "-e" || a == "-E" || a == "-p" || a == "-r" || a == "--eval" || a == "--print":
			return "runs a " + lang + " program given on the command line"
		case a == "-m" && i+1 < len(args):
			return "runs the " + lang + " module " + args[i+1]
		case a == "-":
			return "runs a " + lang + " program read from its standard input"
		case !strings.HasPrefix(a, "-"):
			return "runs the " + lang + " script " + a
		}
	}
	if shellparse.ReadsCodeFromStdin(base, args) {
		return "runs a " + lang + " program read from its standard input"
	}
	return "runs " + base
}

// testsCover reports whether a rule of the tests preset allows the command of u (its exact rule need not be one of them: a venv
// install, a checker whose files are all inside the workspace).
func (ev *evaluator) testsCover(u *unit) bool {
	if u.simple == nil || u.envBad != "" || u.dyn != "" {
		return false
	}
	for _, r := range ev.e.tests {
		switch {
		case r.venv:
			if u.venv.ok() && ev.accessesInWorkspace(u) {
				return true
			}
		case r.matchCommand(*u.simple, false) && r.coversRunner(*u.simple) && (!r.workspace || ev.withinWorkspace(u, r)):
			return true
		}
	}
	return false
}

// testsNear is why a ":workspace" rule of the tests preset whose prefix the command has does not allow it: an option it refuses, or a
// file outside the workspace. "" when no such rule is near.
func (ev *evaluator) testsNear(u *unit) string {
	if u.simple == nil || u.envBad != "" || u.dyn != "" {
		return ""
	}
	for _, r := range ev.e.tests {
		if !r.workspace || !r.matchCommand(*u.simple, false) || ev.withinWorkspace(u, r) {
			continue
		}
		if f := refusedOption(u.simple.Args, r.refused); f != "" {
			return "with " + f + " it does more than check (the tests preset leaves it out)"
		}
		return "it names a file outside the workspace (the tests preset allows it inside the workspace only)"
	}
	return ""
}
