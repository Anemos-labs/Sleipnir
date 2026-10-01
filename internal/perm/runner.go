package perm

import (
	"strings"

	"github.com/reee344/sleipnir/internal/shellparse"
)

// runnerPrefixes are the commands a person doing test-driven work runs over and over with different arguments (go test ./a, go test
// ./b): "don't ask again" for one of them remembers the prefix and not the exact line. Each is a program and, where the program runs
// anything it is told to, the subcommand: a rule for "go" alone would let go run a program of the model's choosing. Never in this
// table: shells, interpreters, rm, sudo, curl or anything unknown. Flags that run a program of the model's choosing (go test -exec)
// are still asked about by the engine, and a test run executes whatever tests the repository has anyway.
var runnerPrefixes = [][]string{
	{"go", "test"}, {"go", "build"}, {"go", "vet"},
	{"npm", "test"}, {"npm", "run"},
	{"pytest"},
	{"cargo", "test"}, {"cargo", "build"}, {"cargo", "check"},
	{"make"},
	{"git", "add"}, {"git", "commit"}, {"git", "status"},
}

// RunnerPrefix is the prefix of argv that "don't ask again" remembers, when argv starts with one of the runner commands.
func RunnerPrefix(argv []string) (prefix string, ok bool) {
	for _, p := range runnerPrefixes {
		if len(argv) >= len(p) && strings.Join(argv[:len(p)], "\x00") == strings.Join(p, "\x00") {
			return shellparse.Join(p), true
		}
	}
	return "", false
}

// RememberedAs is what "don't ask again" would remember for a shell command line, in words for the question: "go test" when the
// line is one of the runner commands (the prefix is remembered), "" when it is the exact line.
func RememberedAs(command string) string {
	an := shellparse.Parse(command)
	if !an.Parsed || len(an.Commands) != 1 {
		return ""
	}
	s := an.Commands[0]
	if len(s.Env) > 0 || len(s.Wrappers) > 0 || s.Program == "" {
		return ""
	}
	p, _ := RunnerPrefix(append([]string{s.Program}, s.Args...))
	return p
}
