package perm

import "testing"

// A cd that is a link of an && chain has run whenever a later link runs, so those links are judged in the directory it went to only:
// "cd build && cmake .." reads the project, not the home directory above a project that lives in it. Wherever the chain may stop
// before the cd, or the cd may not have run, the start directory still counts.
func TestACdInAnAndChainMovesTheLaterLinks(t *testing.T) {
	f := newFixture(t)
	runCases(t, f, []tc{
		{name: "cmake from a build directory", mode: ModeBypass, req: bash("mkdir -p build && cd build && cmake .."), want: "allow"},
		{name: "a search of the project from a subdirectory", mode: ModeBypass, req: bash("cd src && grep -r package .."), want: "allow"},
		{name: "two cds in a chain", mode: ModeBypass, req: bash("cd sub && cd deep && grep -r x ../.."), want: "allow"},
		{name: "a pipeline link after the cd", mode: ModeBypass, req: bash("cd src && grep -r package .. | head -3"), want: "allow"},
		{name: "a substitution of a later link runs after the cd", mode: ModeBypass, req: bash("cd src && echo $(grep -r package ..)"), want: "allow"},

		{name: "a semicolon runs the next command even when cd fails", mode: ModeBypass, req: bash("cd src; grep -r x .."), want: "deny"},
		{name: "past the chain the start directory counts again", mode: ModeBypass, req: bash("cd src && true; grep -r x .."), want: "deny"},
		{name: "|| runs when cd fails", mode: ModeBypass, req: bash("cd src || grep -r x .."), want: "deny"},
		{name: "a cd in a subshell moves nothing", mode: ModeBypass, req: bash("(cd src) && grep -r x .."), want: "deny"},
		{name: "the cd's own substitution runs before it", mode: ModeBypass, req: bash("cd $(grep -r x ..) && true"), want: "deny"},
		{name: "credentials stay protected from the new directory", mode: ModeBypass, req: bash("cd src && cat ../../.ssh/id_rsa"), want: "deny"},
		{name: "a cd to home then a search of it", mode: ModeBypass, req: bash("cd src && cd .. && cd .. && grep -r x ."), want: "deny"},
		{name: "a cd to the parent then a search", mode: ModeBypass, req: bash("cd .. && grep -r x ."), want: "deny"},
		// bash looks a bare operand up in $CDPATH first: CDPATH=.. makes "cd proj" from ~/proj stay in ~/proj
		{name: "a CDPATH the line sets", mode: ModeBypass, req: bash("CDPATH=..; cd proj && grep -r x .."), want: "deny"},
		{name: "an exported CDPATH the line sets", mode: ModeBypass, req: bash("export CDPATH=.. && cd proj && grep -r x .."), want: "deny"},
		{name: "a CDPATH cd after a narrowing one", mode: ModeBypass, req: bash("cd ./src && CDPATH=../.. cd proj && cat ../.ssh/id_rsa"), want: "deny"},
	})
}

// A CDPATH in the harness's environment reaches the commands' shell: a bare operand may go anywhere it lists, so the start
// directory still counts; an operand that starts with "." or "/" is not looked up.
func TestACdChainUnderCDPATH(t *testing.T) {
	t.Setenv("CDPATH", "..")
	f := newFixture(t)
	runCases(t, f, []tc{
		{name: "a bare operand", mode: ModeBypass, req: bash("cd src && grep -r x .."), want: "deny"},
		{name: "a dot operand", mode: ModeBypass, req: bash("cd ./src && grep -r package .."), want: "allow"},
		{name: "an absolute operand", mode: ModeBypass, req: bash("cd {root}/src && grep -r package .."), want: "allow"},
	})
}
