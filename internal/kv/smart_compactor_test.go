package kv

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// docThread is a docs writer's thread: it read the API spec it is documenting (a big result), read and then edited a source file,
// ran the tests twice, and is now writing the page.
func docThread() *Thread {
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("Document the HTTP API in docs/api.md")}})
	call := func(id, tool, input, result string) {
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text("step"), core.ToolUse(id, tool, json.RawMessage(input))}})
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{core.ToolResult(id, false, core.Text(result))}})
	}
	call("c1", "read", `{"path":"internal/api/spec.md"}`, strings.Repeat("GET /api/v1/projects/{p}/events returns ...\n", 400)) // t2-t3, big
	call("c2", "read", `{"path":"internal/api/api.go"}`, strings.Repeat("func handler() {}\n", 300))                            // t4-t5, edited later
	call("c3", "bash", `{"command":"go test ./..."}`, strings.Repeat("ok  pkg\n", 400))                                         // t6-t7, passing log
	call("c4", "edit", `{"path":"internal/api/api.go","old_string":"a","new_string":"b"}`, "Edited")                            // t8-t9
	call("c5", "read", `{"path":"docs/api.md"}`, "# HTTP API\n")                                                                // t10-t11
	call("c6", "write", `{"path":"docs/api.md","content":"x"}`, "Wrote")                                                        // t12-t13
	call("c7", "bash", `{"command":"go vet ./..."}`, "ok")                                                                      // t14-t15
	return th
}

// The compactor is given a budget, how much keeping from each unit keeps, the references of the big results it can keep or mask,
// and which reads are stale (the file was edited or read again later).
func TestTheCompactorBriefCarriesTheBudgetAndWhatEachUnitHolds(t *testing.T) {
	s := stackFor(docThread())
	pol := DefaultApplyPolicy()
	pol.KeepTarget = 3_000
	brief := Instruction(s, est(), pol)
	for _, want := range []string{
		"Budget: the conversation is",
		"keep about 3.0k of it verbatim",
		"keeps ",
		"[t3.0 ", // the spec read's result, big enough to name
		"read(internal/api/api.go, edited later)",   // superseded by the edit
		"read(docs/api.md, edited later)",           // written after it was read
		`"keep":[]`,                                 // the reply shape names keep
		"material the current output is built from", // the priorities
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "read(internal/api/spec.md, ") {
		t.Errorf("the spec, never touched again, was marked stale:\n%s", brief)
	}
	pol.KeepTarget = 0
	if strings.Contains(Instruction(s, est(), pol), "Budget:") {
		t.Error("a budget line without a budget")
	}
}

// A result the compactor names in keep stays verbatim however old, where the harness masks old bulky results by itself; one named
// in mask as well is masked.
func TestAKeptResultIsNotMaskedByAge(t *testing.T) {
	th := docThread()
	pol := DefaultApplyPolicy()
	pol.AutoMaskAfterUnits = 2 // the spec read (t2-t3) is old enough to be masked
	pol.MaskMinTokens = 200
	spec := MaskRef{Turn: 3, Index: 0}
	verbatim := func(p *Patch) bool {
		s := stackFor(th)
		res, err := Apply(s, p, est(), pol)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range res.Replacement {
			if tr.ID != 3 {
				continue
			}
			for _, b := range tr.Blocks {
				if b.Kind == core.BlockToolResult {
					return !isMasked(b)
				}
			}
		}
		t.Fatal("t3 was not retained")
		return false
	}
	base := Patch{KeepFrom: 2, Spine: []SpineEntry{{From: 1, To: 1, Line: "the task"}}}
	if verbatim(&base) {
		t.Fatal("setup: the old spec read was not masked by age")
	}
	kept := base
	kept.Keep = []MaskRef{spec}
	if !verbatim(&kept) {
		t.Fatal("a result named in keep was masked by age")
	}
	both := kept
	both.Mask = []MaskRef{spec}
	if verbatim(&both) {
		t.Fatal("a result named in mask as well was kept")
	}
}

// keep is read like mask: refs, a bad one costing itself and a warning.
func TestParsePatchReadsKeep(t *testing.T) {
	p, err := ParsePatch(`{"keep_from":"t4","spine":[{"turns":"t1-t3","line":"x"}],"keep":["t3.0","t5#1","nope"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(p.Keep) != fmt.Sprint([]MaskRef{{Turn: 3, Index: 0}, {Turn: 5, Index: 1}}) {
		t.Fatalf("keep %v", p.Keep)
	}
	if !strings.Contains(strings.Join(p.Warnings, "|"), `keep[2]: bad ref "nope"`) {
		t.Fatalf("warnings %v", p.Warnings)
	}
}
