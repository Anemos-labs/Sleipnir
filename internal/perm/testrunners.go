package perm

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// Test runners given paths to find tests in.
//
// pytest ., python -m pytest tests, python -m unittest discover -s tests and node --test tests read the tests below their operands;
// they do not write there. Judged as an unknown command, an operand is a tree that may be written, so "." covered the protected
// configuration directory (Edit(./.sleipnir/**)) and a test run in bypass mode stopped for the person. A runner is recognised only
// with the options listed here (none of them names a file to write); any other option makes it an unknown command again.

// runnerFlags describes a test runner's options: those without a value, those whose value is not a file (a pattern, a number, a
// name), and those whose value is a directory or file it reads.
type runnerFlags struct {
	bare    map[string]bool
	valued  map[string]bool
	pathArg map[string]bool
	// letters are single-letter flags that may be clustered (-xq)
	letters string
}

var (
	pytestFlags = runnerFlags{
		bare: set("-q", "-qq", "-v", "-vv", "-vvv", "-x", "-s", "-l", "-ra", "-rA", "-rf", "-rs", "-rx", "-rE", "--lf", "--ff", "--nf", "--sw",
			"--co", "--collect-only", "--no-header", "--no-summary", "--disable-warnings", "--strict-markers", "--strict-config", "--exitfirst",
			"--last-failed", "--failed-first", "--showlocals", "--quiet", "--verbose", "--capture=no", "--import-mode=importlib"),
		valued:  set("-k", "-m", "-W", "-n", "-p", "--tb", "--maxfail", "--durations", "--color", "--log-level", "--import-mode", "--capture"),
		letters: "qvxsl",
	}
	unittestFlags = runnerFlags{
		bare:    set("discover", "-v", "-q", "-f", "-b", "-c", "--verbose", "--quiet", "--failfast", "--buffer", "--catch", "--locals"),
		valued:  set("-k", "-p", "--pattern"),
		pathArg: set("-s", "-t", "--start-directory", "--top-level-directory"),
		letters: "vqfbc",
	}
	nodeTestFlags = runnerFlags{
		bare: set("--test", "--test-only", "--test-force-exit", "--experimental-test-coverage", "--experimental-test-module-mocks",
			"--experimental-strip-types", "--no-warnings"),
		valued: set("--test-reporter", "--test-name-pattern", "--test-skip-pattern", "--test-concurrency", "--test-timeout", "--test-shard",
			"--test-isolation"),
	}
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// testRunnerUses returns what a test runner invocation reads (its operands and directory options, as trees), and whether the command is
// one: pytest or py.test, python -m pytest or unittest, node --test.
func testRunnerUses(name string, args []string) ([]pathUse, bool) {
	interp := shellparse.InterpreterName(name)
	switch {
	case name == "pytest" || name == "py.test":
		return runnerOperands(args, pytestFlags, true)
	case interp == "python" && len(args) >= 2 && args[0] == "-m" && args[1] == "pytest":
		return runnerOperands(args[2:], pytestFlags, true)
	case interp == "python" && len(args) >= 2 && args[0] == "-m" && args[1] == "unittest":
		return runnerOperands(args[2:], unittestFlags, false)
	case interp == "node" && hasWord(args, "--test"):
		return runnerOperands(args, nodeTestFlags, false)
	}
	return nil, false
}

func hasWord(args []string, w string) bool {
	for _, a := range args {
		if a == w {
			return true
		}
	}
	return false
}

// runnerOperands reads args with the runner's options: every operand (a pytest node id's file part) and directory option is a tree
// read. It reports false at the first option it does not know.
func runnerOperands(args []string, f runnerFlags, nodeIDs bool) ([]pathUse, bool) {
	var uses []pathUse
	read := func(p string) {
		if nodeIDs {
			p, _, _ = strings.Cut(p, "::")
		}
		if p != "" {
			uses = append(uses, pathUse{raw: p, tree: true})
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			for _, r := range args[i+1:] {
				read(r)
			}
			return uses, true
		case !strings.HasPrefix(a, "-") || a == "-":
			if f.bare[a] { // a subcommand word (unittest discover)
				continue
			}
			read(a)
		case f.bare[a]:
		case f.valued[a] || f.pathArg[a]:
			if i+1 >= len(args) {
				return nil, false
			}
			i++
			if f.pathArg[a] {
				read(args[i])
			}
		default:
			k, v, eq := strings.Cut(a, "=")
			switch {
			case eq && f.valued[k]:
			case eq && f.pathArg[k]:
				read(v)
			case !strings.HasPrefix(a, "--") && f.letters != "" && strings.Trim(a[1:], f.letters) == "":
			default:
				return nil, false // an option this does not know: the command may write what it names
			}
		}
	}
	return uses, true
}
