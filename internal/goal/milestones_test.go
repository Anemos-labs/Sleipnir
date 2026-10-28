package goal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A milestones section is a heading or a "Milestones:" line and the list under it; indented lines are a milestone's details, and the
// section ends at the next heading or at an unindented line that is not a list item.
func TestParseMilestonesReadsTheSection(t *testing.T) {
	text := `Build a CSV viewer.

## Milestones
1. M1: Parse files up to 1 GB
   - streaming, constant memory
   - go test ./csv passes
2. [ ] **M2** - Render a virtualised table
- [x] Release v0.1.0 with binaries

Notes after the list are not milestones.
- neither is this item

Milestones:
* Website deployed
`
	got := ParseMilestones(text, 1)
	want := []Milestone{
		{ID: "M1", Text: "Parse files up to 1 GB — streaming, constant memory; go test ./csv passes"},
		{ID: "M2", Text: "Render a virtualised table"},
		{ID: "M3", Text: "Release v0.1.0 with binaries"},
		{ID: "M4", Text: "Website deployed"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d milestones %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("milestone %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if ms := ParseMilestones("make the tests pass\n- one\n- two", 1); len(ms) != 0 {
		t.Errorf("a list with no milestones heading: %+v", ms)
	}
	if ms := ParseMilestones("# Plan\n## Milestones\n- a\n## Stack\n- Go", 5); len(ms) != 1 || ms[0].ID != "M5" {
		t.Errorf("a heading ends the section, numbering starts at first: %+v", ms)
	}
	var long strings.Builder
	long.WriteString("Milestones:\n")
	for range MaxMilestones + 5 {
		long.WriteString("- x\n")
	}
	if ms := ParseMilestones(long.String(), 1); len(ms) != MaxMilestones {
		t.Errorf("%d milestones kept, want %d", len(ms), MaxMilestones)
	}
}

// Milestones written as headings: nested under a milestones heading, or each naming itself; the lines and deeper headings under one are
// its details, and a heading at the section's level or above ends them.
func TestParseMilestonesReadsHeadings(t *testing.T) {
	nested := `# Tallyho spec
## Milestones
### Core API
REST endpoints for invoices.
- go test ./api passes
#### Acceptance
OpenAPI served
### Web UI
Renders PDFs.
## Stack
### Go
`
	want := []string{"Core API — REST endpoints for invoices.; go test ./api passes; Acceptance; OpenAPI served", "Web UI — Renders PDFs."}
	got := ParseMilestones(nested, 1)
	if len(got) != len(want) {
		t.Fatalf("nested: %+v", got)
	}
	for i := range want {
		if got[i].Text != want[i] || got[i].ID != fmt.Sprintf("M%d", i+1) {
			t.Errorf("nested %d: %+v, want %q", i, got[i], want[i])
		}
	}
	named := "## Overview\nA tool.\n## Milestone 1: Parser\n- streams\n## M2 - Release\nv0.1.0 tagged\n## License\nMIT"
	got = ParseMilestones(named, 1)
	if len(got) != 2 || got[0].Text != "Parser — streams" || got[1].Text != "Release — v0.1.0 tagged" {
		t.Errorf("named: %+v", got)
	}
}

func TestSpecRefNamesAFileOnALineOfItsOwn(t *testing.T) {
	for in, want := range map[string]string{
		"Build it.\nSpec: docs/SPEC.md\n":        "docs/SPEC.md",
		"spec:`GOAL.md`":                         "GOAL.md",
		"Spec: TBD":                              "",
		"Spec: the app must be fast and small.":  "",
		"build it per the spec: SPEC.md":         "SPEC.md", // typed on one line
		"the spec: SPEC.md is wrong, rewrite it": "",
	} {
		if got := SpecRef(in); got != want {
			t.Errorf("SpecRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// Prepare reads the spec once, keeps its text and takes its milestones after the objective's; a spec outside the project, missing or too
// large is refused.
func TestPrepareKeepsTheSpecAsSet(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := "# Spec\n\n## Milestones\n- API serves /v1/items\n- Docker image builds\n"
	if err := os.WriteFile(filepath.Join(root, "docs", "SPEC.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Prepare("Ship the service.\nMilestones:\n- README written\n\nSpec: docs/SPEC.md", root, root)
	if err != nil {
		t.Fatal(err)
	}
	if g.Spec != "docs/SPEC.md" || !strings.Contains(g.SpecText, "API serves") {
		t.Errorf("spec %q text %q", g.Spec, g.SpecText)
	}
	if len(g.Milestones) != 3 || g.Milestones[0].Text != "README written" || g.Milestones[2].ID != "M3" || g.Milestones[2].Text != "Docker image builds" {
		t.Errorf("milestones %+v", g.Milestones)
	}
	if s := Start(g); !strings.Contains(s, "3 milestones") || !strings.Contains(s, "Docker image builds") || Plain(s) != g.Objective {
		t.Errorf("Start: %q (plain %q)", s, Plain(s))
	}

	outside := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare("x\nSpec: "+outside, root, root); !errors.Is(err, ErrSpecOutside) {
		t.Errorf("a spec outside the project: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.md")); err == nil {
		if _, err := Prepare("x\nSpec: link.md", root, root); !errors.Is(err, ErrSpecOutside) {
			t.Errorf("a link out of the project: %v", err)
		}
	}
	if _, err := Prepare("x\nSpec: ../SPEC.md", root, root); err == nil {
		t.Error("a missing spec was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "big.md"), []byte(strings.Repeat("x", MaxSpecBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare("x\nSpec: big.md", root, root); !errors.Is(err, ErrSpecTooBig) {
		t.Errorf("a spec too large: %v", err)
	}
	if g, err := Prepare("no spec here", root, root); err != nil || g.Spec != "" || len(g.Milestones) != 0 {
		t.Errorf("a plain goal: %+v %v", g, err)
	}
}

// "done" stands only when the same verdict confirms every milestone with evidence; rulings update the milestones either way.
func TestReconcileHoldsDoneToEveryMilestone(t *testing.T) {
	g := New("x")
	g.Milestones = ParseMilestones("Milestones:\n- a\n- b\n- c", 1)

	v := g.Reconcile(Verdict{Kind: Continue, Reason: "b missing", Milestones: []MilestoneVerdict{{ID: "M1", Met: true, Evidence: "record 4"}, {ID: "m3", Met: true}}})
	if v.Kind != Continue || !g.Milestones[0].Met || g.Milestones[0].Evidence != "record 4" || g.Milestones[2].Met {
		t.Errorf("continue: %+v %+v", v, g.Milestones)
	}
	if g.MetCount() != 1 {
		t.Errorf("met %d", g.MetCount())
	}
	if c := Continuation(g, nil, v); !strings.Contains(c, "Milestones met: 1 of 3") || !strings.Contains(c, "open milestone M2: b") || strings.Contains(c, "open milestone M1") {
		t.Errorf("continuation: %q", c)
	}
	if p := JudgePrompt(g, nil, "", nil); !strings.Contains(p, "- M1 [met last time: record 4] a") || !strings.Contains(p, "- M2 [open] b") || !strings.Contains(p, `"milestones"`) {
		t.Errorf("judge prompt: %q", p)
	}

	// done, with M1 left out of the verdict: not done, and M1 leads what is left
	v = g.Reconcile(Verdict{Kind: Done, Reason: "all good", Milestones: []MilestoneVerdict{{ID: "M2", Met: true, Evidence: "r9"}, {ID: "M3", Met: true, Evidence: "r10"}}})
	if v.Kind != Continue || !strings.Contains(v.Reason, "M1") || len(v.Left) == 0 || !strings.HasPrefix(v.Left[0], "M1: a") {
		t.Errorf("done without M1: %+v", v)
	}
	// done, every milestone confirmed with evidence: done
	all := []MilestoneVerdict{{ID: "M1", Met: true, Evidence: "r4"}, {ID: "M2", Met: true, Evidence: "r9"}, {ID: "M3", Met: true, Evidence: "r10"}}
	if v = g.Reconcile(Verdict{Kind: Done, Reason: "all good", Milestones: all}); v.Kind != Done || g.MetCount() != 3 {
		t.Errorf("done with all: %+v %+v", v, g.Milestones)
	}
	// met without evidence is not met, and a ruling can take a milestone back
	all[1] = MilestoneVerdict{ID: "M2", Met: true, Evidence: " "}
	if v = g.Reconcile(Verdict{Kind: Done, Milestones: all}); v.Kind != Continue || g.Milestones[1].Met {
		t.Errorf("met without evidence: %+v %+v", v, g.Milestones)
	}
	// a goal without milestones is untouched
	plain := New("y")
	if v := plain.Reconcile(Verdict{Kind: Done, Reason: "ok"}); v.Kind != Done {
		t.Errorf("no milestones: %+v", v)
	}
}

func TestParseVerdictReadsMilestoneRulings(t *testing.T) {
	v, ok := ParseVerdict(`{"verdict":"continue","reason":"r","left":[],"milestones":[{"id":"M1","met":true,"evidence":"record 3"},{"id":"M2","met":false}]}`)
	if !ok || len(v.Milestones) != 2 || !v.Milestones[0].Met || v.Milestones[0].Evidence != "record 3" || v.Milestones[1].Met {
		t.Errorf("%+v %v", v, ok)
	}
}

// Goals without milestones send the judge and the agent exactly what they did before milestones existed.
func TestNoMilestonesNoChange(t *testing.T) {
	g := New("make the tests pass")
	if s := Start(g); s != "make the tests pass\n\n[standing goal: before you begin, write what it requires as steps with the plan tool, and keep them current. The harness checks the goal against evidence after each turn and sends you on with what is missing until it is met.]" {
		t.Errorf("Start changed: %q", s)
	}
	if p := JudgePrompt(g, nil, "", nil); strings.Contains(p, "MILESTONES") || strings.Contains(p, "SPEC") {
		t.Errorf("judge prompt: %q", p)
	}
}

// Prepare keeps the project's rubric as it was when the goal was set; the agent's first message and the judge's request carry it, a project
// without one sets goals as before, and a rubric that cannot be read stops the goal from being set below the project's bar.
func TestPrepareKeepsTheProjectRubric(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "cmd")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if g, err := Prepare("Fix the parser.", sub, root); err != nil || g.Rubric != "" || g.RubricText != "" {
		t.Fatalf("no rubric: %+v %v", g, err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(RubricFile))
	if err := os.WriteFile(path, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g, err := Prepare("Fix the parser.", sub, root); err != nil || g.Rubric != "" {
		t.Fatalf("an empty rubric: %+v %v", g, err)
	}
	rubric := "- Every exported function has a test.\n- The CHANGELOG names each user-visible change.\n"
	if err := os.WriteFile(path, []byte(rubric), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Prepare("Fix the parser.", sub, root)
	if err != nil {
		t.Fatal(err)
	}
	if g.Rubric != RubricFile || !strings.Contains(g.RubricText, "CHANGELOG names") {
		t.Fatalf("rubric %q text %q", g.Rubric, g.RubricText)
	}
	if err := os.WriteFile(path, []byte("- anything goes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := Start(g); !strings.Contains(s, "project rubric "+RubricFile) || !strings.Contains(s, "exported function has a test") || Plain(s) != g.Objective {
		t.Errorf("Start: %q (plain %q)", s, Plain(s))
	}
	if p := JudgePrompt(g, nil, "fixed", nil); !strings.Contains(p, "PROJECT RUBRIC") || !strings.Contains(p, "never removes or relaxes") || !strings.Contains(p, "exported function has a test") {
		t.Errorf("judge prompt: %q", p)
	}
	if p := JudgePrompt(New("Fix the parser."), nil, "fixed", nil); strings.Contains(p, "RUBRIC") {
		t.Errorf("a goal without a rubric: %q", p)
	}

	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxSpecBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare("Fix the parser.", sub, root); !errors.Is(err, ErrSpecTooBig) || !strings.Contains(err.Error(), "rubric") {
		t.Errorf("a rubric too large: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "rubric.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err == nil {
		if _, err := Prepare("Fix the parser.", sub, root); !errors.Is(err, ErrSpecOutside) {
			t.Errorf("a rubric linked out of the project: %v", err)
		}
	}
}
