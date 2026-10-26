package agent

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// The test-weakening guard.
//
// The guard notices a failing test command followed by successful edits to test
// files only. It adds one advisory note per failure episode to the results turn,
// leaving the prompt prefix unchanged. A test can be wrong: the note asks for the
// contradicted requirement or an implementation fix rather than forbidding edits.

var (
	// testCmdRe matches a command that runs tests: a test runner by name, a build tool's test subcommand after any flags
	// (mvn -q test, ./gradlew --offline test, npm run test:unit), or Node's built-in runner (node --test).
	testCmdRe = regexp.MustCompile(`\b(pytest|py\.test|unittest|jest|vitest|mocha|rspec|phpunit|ctest|tox|nox)\b|` +
		`\b(go|cargo|dotnet|swift|deno|bun|mix|stack|cabal|dune|bazel|mvnw?|gradlew?|npm|yarn|pnpm|make|rake)(\s+[-+][\w.:=/+-]+)*\s+(run\s+)?tests?\b|` +
		`\bnode(\s+-[\w.:=/+-]+)*\s+--test\b`)
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

// isTestCommand reports whether a shell command runs tests: a known runner, or the project's own test command.
func (g *testGuard) isTestCommand(cmd string) bool {
	return testCmdRe.MatchString(cmd) || (g.hint != "" && strings.Contains(cmd, g.hint))
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
