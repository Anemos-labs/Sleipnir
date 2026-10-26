package agent

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// The test-weakening guard.
//
// The guard notices a failing test command followed by successful edits to test
// files only. It adds one advisory note per failure episode to the results turn,
// leaving the prompt prefix unchanged. A test can be wrong: the note asks for the
// contradicted requirement or an implementation fix rather than forbidding edits.

var (
	// testCmdRe matches one simple command, its program's base name and its words, that runs tests: a test runner (optionally
	// through npx, bundle exec, uv run and the like, or python -m), a build tool's test subcommand after its flags (mvn and gradle
	// after their other tasks too: mvn -q clean test), npm run test:unit, or Node's built-in runner (node --test).
	testCmdRe = regexp.MustCompile(`^(?:(?:npx|pnpx|bunx|bundle exec|uv run|poetry run|pipenv run|hatch run)\s+)?(?:` +
		`(?:pytest|py\.test|jest|vitest|mocha|rspec|phpunit|ctest|tox|nox)\b|` +
		`python[0-9.]*(?:\s+-\S+)*\s+-m\s+(?:pytest|unittest)\b|` +
		`(?:go|cargo|dotnet|swift|deno|bun|mix|stack|cabal|dune|bazel|npm|yarn|pnpm)(?:\s+[-+]\S+)*\s+(?:run\s+)?tests?\b|` +
		`(?:mvnw?|gradlew?|make|rake|sbt)(?:\s+\S+)*?\s+tests?\b|` +
		`node(?:\s+-\S+)*\s+--test\b)`)
	testPathRe = regexp.MustCompile(`(?i)(_test\.go|_test\.py|\.(test|spec)\.[a-z]+|_spec\.rb|Test\.java)$|(^|/)test_[^/]*\.py$|(^|/)(tests?|__tests__)/`)
	patchFile  = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)
)

type testGuard struct {
	failed bool // a test command failed and the code has not been edited since
	nudged bool // already told, for this failure

	edited   bool // code was changed in this run
	ranSince bool // and a test command has run since the last such change

	hint string // the project's own test command (Config.VerifyHint), which counts as a test run however it is spelled
}

// reset clears the test guard's accumulated verification state and sets the project's test command.
func (g *testGuard) reset(hint string) { *g = testGuard{hint: strings.TrimSpace(hint)} }

// isTestCommand reports whether a shell command runs tests: one of its simple commands is a known runner, or starts with the words
// of the project's own test command. Text that only mentions a runner (grep 'go test' Makefile, echo "npm test") is an argument of
// another program and does not count. A line the parser cannot read is not counted either.
func (g *testGuard) isTestCommand(cmd string) bool {
	an := shellparse.Parse(cmd)
	if !an.Parsed {
		return false
	}
	var hint []string
	if g.hint != "" {
		hint, _ = shellparse.Fields(g.hint)
	}
	for _, c := range an.Commands {
		words := append([]string{c.Program}, c.Args...)
		if hasWordPrefix(words, hint) {
			return true
		}
		words[0] = path.Base(c.Program)
		if testCmdRe.MatchString(strings.Join(words, " ")) {
			return true
		}
	}
	return false
}

// hasWordPrefix reports whether words begins with every word of prefix (an empty prefix is no match).
func hasWordPrefix(words, prefix []string) bool {
	if len(prefix) == 0 || len(words) < len(prefix) {
		return false
	}
	for i, w := range prefix {
		if words[i] != w {
			return false
		}
	}
	return true
}

// editedPaths are the files a write, edit or patch call names.
func editedPaths(call core.Block) []string {
	switch call.ToolName {
	case "write", "edit", "patch", "apply_patch":
	default:
		return nil
	}
	var in struct {
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	if json.Unmarshal(call.Input, &in) != nil {
		return nil
	}
	var out []string
	if in.Path != "" {
		out = append(out, in.Path)
	}
	for _, m := range patchFile.FindAllStringSubmatch(in.Patch, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// docPathRe are the files whose edit does not call for running the tests.
var docPathRe = regexp.MustCompile(`(?i)(\.(md|markdown|txt|rst|adoc)|(^|/)(docs?|license|changelog)[^/]*)$`)

// unverified reports whether a code edit succeeded in this run without a later
// test command executing. It does not establish that tests passed or covered the edit.
func (g *testGuard) unverified() bool { return g.edited && !g.ranSince }

// observe uses aligned tool results to track executed tests and successful edits.
// Refused, invalid, failed, or missing tool results leave the state unchanged;
// an executed command with a nonzero exit still counts as a failing test run.
func (g *testGuard) observe(calls, results []core.Block, exitFailed []bool) string {
	var note string
	for i, call := range calls {
		if i >= len(results) || results[i].IsError {
			continue
		}
		if call.ToolName == "bash" {
			var in struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(call.Input, &in) == nil && g.isTestCommand(in.Command) {
				g.ranSince = true
				failed := i < len(exitFailed) && exitFailed[i]
				if failed != g.failed {
					g.nudged = false
				}
				g.failed = failed
			}
			continue
		}
		paths := editedPaths(call)
		for _, p := range paths {
			if !docPathRe.MatchString(p) {
				g.edited, g.ranSince = true, false
			}
		}
		if len(paths) == 0 || !g.failed {
			continue
		}
		onlyTests := true
		for _, p := range paths {
			if !testPathRe.MatchString(p) {
				onlyTests = false
			}
		}
		switch {
		case !onlyTests:
			g.failed, g.nudged = false, false // an edit outside tests starts a new failure episode
			note = ""                         // this batch did not change only test files
		case !g.nudged:
			g.nudged = true
			note = "[harness] The last test run failed and you changed only test files. Change a test only when it contradicts the task: " +
				"if so, say in your next message which requirement it gets wrong. Otherwise fix the code under test, then run the tests again."
		}
	}
	return note
}
