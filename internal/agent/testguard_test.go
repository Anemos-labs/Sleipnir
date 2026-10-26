package agent

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func tcall(name, input string) core.Block { return core.Block{ToolName: name, Input: []byte(input)} }

func TestTestGuardNotesAnEditOfOnlyTestsAfterAFailingRun(t *testing.T) {
	var g testGuard
	observe := func(calls []core.Block, failed []bool) string {
		return g.observe(calls, make([]core.Block, len(calls)), failed)
	}
	run := func(failed bool) string {
		return observe([]core.Block{tcall("bash", `{"command":"cd x && go test ./..."}`)}, []bool{failed})
	}
	edit := func(path string) string { return observe([]core.Block{tcall("edit", `{"path":"`+path+`"}`)}, nil) }

	if n := edit("cache_test.go"); n != "" {
		t.Errorf("no failing run yet: %q", n)
	}
	run(true)
	n := edit("cache_test.go")
	if !strings.Contains(n, "[harness]") || !strings.Contains(n, "only test files") {
		t.Fatalf("a test edited after a failing run is noted: %q", n)
	}
	if again := edit("cache_test.go"); again != "" {
		t.Errorf("once per failing run: %q", again)
	}
	run(true) // still failing: the same episode
	if again := edit("tests/helper.py"); again != "" {
		t.Errorf("the same failure is not noted twice: %q", again)
	}
	run(false)
	run(true)
	if n := edit("src/x.test.ts"); n == "" {
		t.Error("a new failure is a new episode")
	}
	run(true)
	if n := observe([]core.Block{tcall("edit", `{"path":"cache.go"}`), tcall("edit", `{"path":"cache_test.go"}`)}, nil); n != "" {
		t.Errorf("the code was edited in the same batch: %q", n)
	}
	run(true)
	if n := observe([]core.Block{tcall("edit", `{"path":"cache_test.go"}`), tcall("edit", `{"path":"cache.go"}`)}, nil); n != "" {
		t.Errorf("a later successful code edit in the batch was ignored: %q", n)
	}
	run(true)
	if n := observe([]core.Block{tcall("apply_patch", `{"patch":"*** Begin Patch\n*** Update File: lib/a.go\n@@\n-a\n+b\n*** Update File: lib/a_test.go\n@@\n-a\n+b\n*** End Patch"}`)}, nil); n != "" {
		t.Errorf("a patch that changes code and tests is a code change: %q", n)
	}
	// Other commands and tools leave the state alone.
	run(true)
	observe([]core.Block{tcall("bash", `{"command":"ls"}`), tcall("read", `{"path":"a_test.go"}`)}, []bool{false, false})
	if n := edit("a_test.go"); n == "" {
		t.Error("ls and read do not end the failing episode")
	}
}

func TestTestGuardRefusalsDoNotClearFailingTestEvidence(t *testing.T) {
	var g testGuard
	g.observe([]core.Block{tcall("bash", `{"command":"go test ./..."}`)}, []core.Block{{}}, []bool{true})
	before := g
	for _, call := range []core.Block{
		tcall("edit", `{"path":"a.go"}`),
		tcall("edit", `{"path":"a_test.go"}`),
		tcall("bash", `{"command":"go test ./..."}`),
	} {
		for _, results := range [][]core.Block{nil, {{IsError: true}}} {
			if note := g.observe([]core.Block{call}, results, nil); note != "" || g != before {
				t.Fatalf("unsuccessful %s changed failure evidence: %+v; note=%q", call.ToolName, g, note)
			}
		}
	}
	if note := g.observe([]core.Block{tcall("edit", `{"path":"a_test.go"}`)}, []core.Block{{}}, nil); !strings.Contains(note, "only test files") {
		t.Fatalf("successful test edit lost the failure warning: %q", note)
	}
}

func TestTestGuardRecognisesTestCommands(t *testing.T) {
	for _, cmd := range []string{
		"go test ./...", "cd x && go test -race ./...", "python3 -m unittest discover -s tests -v", "python -m pytest -q",
		"node --test test/*.test.js", "node --experimental-vm-modules --test", "npm test", "npm run test:unit", "npm -s test", "pnpm -r test",
		"yarn test", "bun test", "deno test -A", "npx vitest run", "npx jest", "cargo test --offline --quiet", "cargo +nightly test",
		"mvn -q test", "./mvnw test", "./gradlew --offline test", "gradle test", "dotnet test", "swift test", "make test", "make tests",
		"ctest --output-on-failure", "mix test", "bundle exec rspec", "tox -e py312", "mvn clean test", "./gradlew clean build test",
		".venv/bin/python -m pytest -x", "timeout 120 go test ./...", "CI=1 npm test", "go vet ./... && go test ./... 2>&1 | tail -5",
	} {
		if !(&testGuard{}).isTestCommand(cmd) {
			t.Errorf("%q runs tests", cmd)
		}
	}
	for _, cmd := range []string{"ls test", "cat test/a.test.js", "npm install", "go build ./...", "git log -- test", "node script.js", "echo test", "make build",
		"rg -n 'node --test' package.json", `grep "go test" Makefile`, `echo "run npm test next"`, "go run ./cmd/x test"} {
		if (&testGuard{}).isTestCommand(cmd) {
			t.Errorf("%q does not run tests", cmd)
		}
	}
	g := testGuard{}
	g.reset("  ./scripts/check.sh --fast ")
	if !g.isTestCommand("./scripts/check.sh --fast 2>&1 | tail") {
		t.Error("the project's own test command counts as a test run")
	}
	if g.isTestCommand("echo './scripts/check.sh --fast'") || g.isTestCommand("cat ./scripts/check.sh") {
		t.Error("a command that only names the project's test command does not run it")
	}
	g.reset("mvn test")
	if !g.isTestCommand("mvn clean test") {
		t.Error("mvn clean test runs the tests the survey's mvn test names")
	}
}

func TestTestGuardCountsTheRunnerAProjectScriptWraps(t *testing.T) {
	// A Node project's test script is "node --test test/*.test.js"; the survey names it "npm run test". Running the script's
	// own command verifies the change as well as the npm wrapper does, and must not draw the "run the tests" nudge.
	var g testGuard
	g.reset("npm run test")
	g.observe([]core.Block{tcall("write", `{"path":"src/slugify.js"}`)}, []core.Block{{}}, nil)
	if !g.unverified() {
		t.Fatal("a code edit with no test run since is unverified")
	}
	g.observe([]core.Block{tcall("bash", `{"command":"node --test test/*.test.js 2>&1 | tail -20"}`)}, []core.Block{{}}, []bool{false})
	if g.unverified() {
		t.Error("node --test ran the tests after the edit")
	}
}
