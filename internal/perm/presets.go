package perm

import "strings"

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
// rewrite the project's files (--fix), write files of their own (--createstub, --init, --generateTrace), or install packages
// (mypy --install-types runs pip in the Python that runs mypy).
var workspaceRefused = map[string][]string{
	"ruff check":   {"--fix", "--unsafe-fixes", "--add-noqa", "--fix-only"},
	"mypy":         {"--install-types"},
	"pyright":      {"--createstub", "--writebaseline"},
	"tsc --noEmit": {"-b", "--build", "--init", "--generateTrace", "--generateCpuProfile"},
	"eslint":       {"--fix", "--init", "--fix-type"},
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
