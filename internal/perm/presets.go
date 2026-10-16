package perm

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// TestsAllow is the preset of allow rules for the commands that build, check, format and test a project (`--allow tests`, the answer
// "allow builds and tests for this session" of a question about such a command, and accept-edits mode). They run the project's own
// code, as running its tests is meant to. Two of them reach beyond the project's files, and only so far: an HTTP request to this
// machine (Bash(curl:loopback), the smoke test of a server the project's own build started), and a Python virtual environment of the
// workspace (Bash(python:venv)): creating one, and installing packages into it from the package index, never into the system's or the
// user's Python (venv.go). Every other install, download, or interpreter given a program of its own still asks. The checkers (ruff
// check, mypy, pyright, tsc --noEmit, eslint, py_compile) are allowed when every file they name is inside the workspace
// (":workspace"), without the options that make them write elsewhere or install (workspaceRefused).
var TestsAllow = []string{
	"Bash(go test:*)", "Bash(go build:*)", "Bash(go vet:*)", "Bash(gofmt:*)", "Bash(go fmt:*)", "Bash(go mod init:*)", "Bash(go mod tidy:*)",
	"Bash(cargo test:*)", "Bash(cargo build:*)", "Bash(cargo check:*)", "Bash(cargo clippy:*)", "Bash(cargo fmt:*)",
	"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)", "Bash(npm run format:*)",
	"Bash(pnpm test:*)", "Bash(yarn test:*)",
	"Bash(node --test:*)", "Bash(node --check:*)",
	"Bash(pytest:*)", "Bash(python -m pytest:*)", "Bash(python3 -m pytest:*)", "Bash(python -m unittest:*)", "Bash(python3 -m unittest:*)",
	"Bash(python -m py_compile:workspace)", "Bash(python3 -m py_compile:workspace)",
	"Bash(python -m compileall:workspace)", "Bash(python3 -m compileall:workspace)",
	"Bash(python:venv)",
	"Bash(ruff check:workspace)", "Bash(ruff format --check:workspace)", "Bash(ruff format --diff:workspace)",
	"Bash(mypy:workspace)", "Bash(pyright:workspace)", "Bash(tsc --noEmit:workspace)", "Bash(eslint:workspace)",
	"Bash(mvn test:*)", "Bash(mvn -q test:*)", "Bash(gradle test:*)", "Bash(./gradlew test:*)",
	"Bash(dotnet test:*)", "Bash(dotnet build:*)",
	"Bash(make test:*)", "Bash(make check:*)", "Bash(make build:*)", "Bash(make lint:*)", "Bash(make fmt:*)", "Bash(make vet:*)", "Bash(ctest:*)",
	"Bash(curl:loopback)", "Bash(wget:loopback)",
}

// workspaceRefused are, for the prefixes of the preset's ":workspace" rules, the options that take a command off the rule: they
// rewrite the project's files (--fix, eslint's suppressions file), write files of their own (--createstub, --init, --generateTrace, a
// report: eslint -o, mypy --junit-xml, tsc's build information), or install packages (mypy --install-types runs pip in the Python that runs mypy). A
// one-letter option counts attached and in a cluster as well (refusedOption).
var workspaceRefused = map[string][]string{
	"ruff check": {"--fix", "--unsafe-fixes", "--add-noqa", "--fix-only", "-o", "--output-file"},
	"mypy": {"--install-types", "--junit-xml", "--html-report", "--txt-report", "--xml-report", "--xslt-html-report",
		"--xslt-txt-report", "--linecount-report", "--linecoverage-report", "--lineprecision-report", "--any-exprs-report",
		"--cobertura-xml-report"},
	"pyright":      {"--createstub", "--writebaseline"},
	"tsc --noEmit": {"-b", "--build", "--init", "--generateTrace", "--generateCpuProfile", "--incremental", "--tsBuildInfoFile"},
	"eslint":       {"--fix", "--init", "--fix-type", "-o", "--output-file", "--suppress-all", "--suppress-rule", "--prune-suppressions"},
}

// refusedOption returns the first of names that args give: as the argument itself or with "=value", and, for a one-letter option
// ("-o"), attached to its value ("-oreport.json") or in a cluster of one-letter options ("-qo report.json"), as the checkers' option
// parsers take it. "" when none is given. A cluster that only holds the letter in another option's attached value is refused too:
// the rule then does not apply, and the command is judged without it.
func refusedOption(args []string, names []string) string {
	for _, n := range names {
		if hasLong(args, n) {
			return n
		}
		if len(n) != 2 || n[0] != '-' || n[1] == '-' {
			continue
		}
		for _, a := range args {
			if len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], n[1]) >= 0 {
				return n
			}
		}
	}
	return ""
}

// runnerRefused are, for a build or test runner, the options that make it run a program of the caller's choosing instead of the
// project's own build and tests: go test -exec PROG runs the test binary through PROG, cargo --config can set a runner or a compiler
// wrapper, npm --script-shell runs the scripts with another shell, node --require loads any module first (and --test-reporter a module that is not a built-in reporter), make SHELL=PROG runs every
// recipe with PROG. A prefix rule names the runner (Bash(go test:*), the tests preset's and the read-only roles' rules, or the person's
// own), so a command with one of these options is not the command the rule allowed: it is judged as if no such rule existed.
var runnerRefused = map[string][]string{
	"go":     {"-exec", "--exec", "-toolexec", "--toolexec", "-vettool", "--vettool"},
	"cargo":  {"--config", "-Z"},
	"npm":    {"--script-shell", "--node-options"},
	"pnpm":   {"--script-shell", "--node-options"},
	"yarn":   {"--script-shell", "--node-options"},
	"node":   {"--require", "-r", "--import", "--loader", "--experimental-loader", "--env-file"},
	"make":   {"SHELL", ".SHELLFLAGS", "MAKESHELL", "-f", "--file", "--makefile", "--eval", "-E"},
	"gradle": {"-I", "--init-script"}, "gradlew": {"-I", "--init-script"},
}

// runsAnother reports whether a runner's arguments carry an option of runnerRefused, with its value apart, after "=", or (for a
// one-letter option) attached ("-fother.mk", "-Zflag", or "-sf other.mk" for make's clusters); or, for go, link flags that name the
// external linker or archiver to run (-ldflags=-extld=PROG; -ldflags "-X main.version=1" is a build's own). prog is the program as
// written; its base name selects the runner (/usr/bin/go is go, ./gradlew is gradlew).
func runsAnother(prog string, args []string) bool {
	prog = shellparse.ProgramName(prog)
	refused := runnerRefused[prog]
	if hasLong(args, refused...) {
		return true
	}
	if prog == "make" {
		// a command-line variable takes every assignment operator of make: SHELL:=PROG, SHELL::=, SHELL?=, SHELL+=, SHELL!=
		for _, a := range args {
			for _, n := range refused {
				if !strings.HasPrefix(n, "-") && strings.HasPrefix(a, n) && makeAssigns(a[len(n):]) {
					return true
				}
			}
		}
	}
	for _, a := range args {
		if len(a) < 3 || a[0] != '-' || a[1] == '-' {
			continue
		}
		for _, o := range refused {
			if len(o) != 2 || o[0] != '-' {
				continue
			}
			// attached: -fother.mk; make also takes clusters of one-letter options (-sf other.mk, -kE '...')
			if a[1] == o[1] || prog == "make" && strings.IndexByte(a[1:], o[1]) >= 0 {
				return true
			}
		}
	}
	if prog == "node" {
		return customReporter(args)
	}
	if prog != "go" {
		return false
	}
	for i, a := range args {
		name, val, eq := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "ldflags" {
			continue
		}
		if !eq && i+1 < len(args) {
			val = args[i+1]
		}
		if strings.Contains(val, "extld") || strings.Contains(val, "extar") {
			return true
		}
	}
	return false
}

// makeAssigns reports whether rest (what follows a variable's name in a make argument) is one of make's assignment operators.
func makeAssigns(rest string) bool {
	for _, op := range []string{"=", ":=", "::=", ":::=", "?=", "+=", "!="} {
		if strings.HasPrefix(rest, op) {
			return true
		}
	}
	return false
}

// nodeReporters are the test reporters built into node; any other --test-reporter value is a module node loads and runs.
var nodeReporters = map[string]bool{"spec": true, "tap": true, "dot": true, "junit": true, "lcov": true}

// customReporter reports whether node's arguments name a test reporter that is not built in (--test-reporter=./mine.js).
func customReporter(args []string) bool {
	for i, a := range args {
		val, ok := strings.CutPrefix(a, "--test-reporter=")
		if !ok && a == "--test-reporter" && i+1 < len(args) {
			val, ok = args[i+1], true
		}
		if ok && !nodeReporters[val] {
			return true
		}
	}
	return false
}

// workspaceFileLists are the prefixes of the preset's ":workspace" rules whose program compiles the files it is given and, given none,
// a default set of its own: python -m compileall with no file compiles every directory of sys.path (the interpreter's own included),
// and both read a list of files from standard input or a file (-, -i) that the engine cannot see. compiledFiles holds them to naming
// their files.
var workspaceFileLists = map[string]bool{
	"python -m py_compile": true, "python3 -m py_compile": true, "python -m compileall": true, "python3 -m compileall": true,
}

// compileValueLetters are the short options of compileall that take a value (-d DESTDIR, -s, -p, -x, -r, -j, -o, -e): the rest of
// their cluster ("-j4"), or the next argument when they end it ("-qj 4"), is that value, not a file to compile.
var compileValueLetters = map[byte]bool{'d': true, 's': true, 'p': true, 'x': true, 'r': true, 'j': true, 'o': true, 'e': true}

// compiledFiles is why the arguments of python -m py_compile or compileall (after the module) do not name the files it compiles: it
// names none, so compileall would compile every directory of sys.path, or it reads its list from standard input or a file ("-", -i),
// whose paths are not on the command line. "" when they name them. Short options are read as argparse reads them, one letter at a
// time in a cluster ("-qi list" is -q -i list).
func compiledFiles(args []string) string {
	files := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-":
			return "it reads the files to compile from standard input"
		case a == "--":
			for _, b := range args[i+1:] {
				if b == "-" {
					return "it reads the files to compile from standard input"
				}
				files++
			}
			i = len(args)
		case a == "--invalidation-mode":
			i++
		case strings.HasPrefix(a, "--invalidation-mode="), a == "--hardlink-dupes", a == "--quiet":
		case strings.HasPrefix(a, "--"):
			// argparse takes a prefix of a long option as the option ("--inv" is --invalidation-mode), so a long option that is not
			// spelled out may be one that takes the next argument: refuse it rather than count that argument as a file
			return "it gives an option (" + a + ") that is not one of compileall's spelled out in full"
		case strings.HasPrefix(a, "-"):
		cluster:
			for k := 1; k < len(a); k++ {
				switch c := a[k]; {
				case c == 'i':
					return "it reads the files to compile from a list (-i)"
				case compileValueLetters[c]:
					if k == len(a)-1 {
						i++ // the value is the next argument
					}
					break cluster // the value is the rest of the cluster
				}
			}
		default:
			files++
		}
	}
	if files == 0 {
		return "it names no file to compile (compileall would compile every directory of sys.path)"
	}
	return ""
}

// PresetTests is the name of that set in a Decision.
const PresetTests = "tests"

// addPreset gives the session the rules of a named preset. An unknown name adds nothing.
func (e *Engine) addPreset(name string) {
	if name != PresetTests {
		return
	}
	for _, s := range TestsAllow {
		if r, err := ParseRule(Allow, s); err == nil {
			e.addRule(ScopeSession, r, OriginTests)
		}
	}
}
