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
