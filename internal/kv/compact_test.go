package kv

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
)

// buildThread makes: user task, then n exchanges (assistant tool_use + user
// tool_result of resultChars), then returns the thread.
func buildThread(n, resultChars int) *Thread {
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("Implement pagination for /users and keep the API backwards compatible")}})
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("call_%d", i)
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{
			{Kind: core.BlockThinking, Text: "hmm", Wire: json.RawMessage(`{"type":"thinking"}`), WireFormat: "anthropic"},
			core.Text(fmt.Sprintf("step %d", i)),
			core.ToolUse(id, "bash", json.RawMessage(fmt.Sprintf(`{"command":"go test ./pkg%d/..."}`, i))),
		}})
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{
			core.ToolResult(id, false, core.Text(strings.Repeat("ok line of output\n", resultChars/18))),
		}})
	}
	return th
}

func stackFor(th *Thread) *Stack {
	s := &Stack{Agent: "be-1", Role: "backend", Model: "m"}
	s.Thread = th.Snapshot()
	return s
}

func TestParsePatchToleratesProseAndFences(t *testing.T) {
	reply := "Sure! Here is the patch:\n```json\n" + `{
	  "keep_from": "t9",
	  "spine": [
	    {"turns": "t1-t4", "line": "Explored repo {braces} in strings are fine"},
	    {"turns": "t5", "line": "Ran tests"},
	    {"turns": "garbage", "line": "x"}
	  ],
	  "mask": ["t6.0", "nonsense"],
	  "notes": [{"op":"ADD","key":"Decisions","text":"use cursor pagination"},{"op":"explode","key":"x"}],
	  "promote": [{"scope":"shared","key":"conventions","text":"tests: make test"},{"scope":"galaxy","key":"k","text":"t"}]
	}` + "\n```\nHope that helps."
	p, err := ParsePatch(reply)
	if err != nil {
		t.Fatal(err)
	}
	if p.KeepFrom != 9 || len(p.Spine) != 2 || len(p.Mask) != 1 || len(p.Notes) != 1 || len(p.Promote) != 1 {
		t.Fatalf("parsed = %+v", p)
	}
	if p.Spine[0].To != 4 || p.Notes[0].Op != "add" {
		t.Fatalf("normalisation failed: %+v %+v", p.Spine[0], p.Notes[0])
	}
	if len(p.Warnings) != 4 {
		t.Fatalf("expected 4 warnings (bad range, bad mask, bad op, bad promote), got %v", p.Warnings)
	}
	for _, bad := range []string{"", "no json here", `{"spine":[]}`, `{"keep_from":"t3"`} {
		if _, err := ParsePatch(bad); err == nil {
			t.Fatalf("ParsePatch(%q) should fail", bad)
		}
	}
}

func TestApplyFoldsOldUnitsIntoSpineAndKeepsRecent(t *testing.T) {
	th := buildThread(8, 8000) // turns: 1 user + 16 = 17
	s := stackFor(th)
	// Units: [t1] [t2,t3] [t4,t5] ... ; keep from t12 (unit 6 of exchanges).
	p := &Patch{
		KeepFrom: 12,
		Spine: []SpineEntry{
			{From: 1, To: 5, Line: "Read the users handler and the router; pagination is missing"},
			{From: 6, To: 9, Line: "Added limit/offset params, tests still failing on ordering"},
		},
		Mask: []MaskRef{{Turn: 13, Index: 0}},
	}
	e := core.NewBytesEstimator().WithRatio(4)
	res, err := Apply(s, p, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.KeepFrom != 12 {
		t.Fatalf("keep_from = %d", res.KeepFrom)
	}
	txt := res.Spine.Text()
	for _, want := range []string{"t1-t5 · Read the users handler", "t6-t9 · Added limit/offset"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("spine missing %q:\n%s", want, txt)
		}
	}
	// Units t10-t11 were not covered by the patch: a mechanical line, never a silent drop.
	if !strings.Contains(txt, "t10-t11 · tool work: bash×1") || res.Mechanical != 1 {
		t.Fatalf("uncovered units need a mechanical line:\n%s (mechanical=%d)", txt, res.Mechanical)
	}
	if res.Replacement[0].ID != 12 {
		t.Fatalf("first retained turn = t%d", res.Replacement[0].ID)
	}
	if err := Validate(res.Replacement); err != nil {
		t.Fatalf("retained turns must stay structurally valid: %v", err)
	}
	for _, tr := range res.Replacement {
		for _, b := range tr.Blocks {
			if b.Kind == core.BlockThinking {
				t.Fatal("thinking blocks must be stripped from retained turns at a rebase")
			}
		}
	}
	if res.MaskedResults == 0 {
		t.Fatal("explicit mask ignored")
	}
	if res.RemovedTokens <= res.SpineAdded*5 {
		t.Fatalf("compaction should shrink dramatically: removed %d tokens, spine %d", res.RemovedTokens, res.SpineAdded)
	}
	// The human's original instruction survives verbatim in the notes.
	seg, ok := res.Notes.Segment("instructions")
	if !ok || !strings.Contains(seg.Text, "keep the API backwards compatible") {
		t.Fatalf("user instructions must be preserved verbatim in notes: %+v", res.Notes)
	}
}

func TestApplyProtectsNewestUnitsAndInFlightExchange(t *testing.T) {
	th := buildThread(3, 200)
	// An assistant tool call still awaiting its result: the request in flight.
	th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{
		{Kind: core.BlockThinking, Wire: json.RawMessage(`{"type":"thinking"}`), WireFormat: "anthropic"},
		core.ToolUse("call_pending", "bash", json.RawMessage(`{"command":"sleep 1"}`)),
	}})
	s := stackFor(th)
	e := core.NewBytesEstimator().WithRatio(4)
	res, err := Apply(s, &Patch{KeepFrom: 999}, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	last := res.Replacement[len(res.Replacement)-1]
	if len(last.ToolCalls()) != 1 || last.ToolCalls()[0].ToolID != "call_pending" {
		t.Fatal("in-flight exchange must survive")
	}
	// A rebase voids every thinking binding, the in-flight assistant turn's
	// included: removing all thinking is the one edit a provider always accepts.
	for _, b := range last.Blocks {
		if b.Kind == core.BlockThinking {
			t.Fatal("thinking must be stripped from every retained turn, the in-flight one too")
		}
	}
	if len(Units(res.Replacement)) < 2 {
		t.Fatalf("at least MinKeepUnits units must remain, got %d", len(Units(res.Replacement)))
	}
	if _, err := Apply(stackFor(buildThread(0, 0)), &Patch{KeepFrom: 1}, e, DefaultApplyPolicy()); err == nil {
		t.Fatal("a single-turn thread cannot be compacted")
	}
}

func TestAutoMaskHidesOldBulkyResultsOnly(t *testing.T) {
	th := buildThread(10, 12000)
	s := stackFor(th)
	e := core.NewBytesEstimator().WithRatio(4)
	pol := DefaultApplyPolicy()
	pol.AutoMaskAfterUnits = 3
	// keep_from t4 retains almost everything, so masking is what does the work.
	res, err := Apply(s, &Patch{KeepFrom: 4}, e, pol)
	if err != nil {
		t.Fatal(err)
	}
	var masked, verbatim int
	for _, tr := range res.Replacement {
		for _, b := range tr.Blocks {
			if b.Kind == core.BlockToolResult {
				if isMasked(b) {
					masked++
					if !strings.Contains(b.Result[0].Text, "bash(go test") || !strings.Contains(b.Result[0].Text, "recall t") {
						t.Fatalf("mask placeholder must say what was hidden and how to recall it: %q", b.Result[0].Text)
					}
				} else {
					verbatim++
				}
			}
		}
	}
	if masked == 0 || verbatim < 3 {
		t.Fatalf("expected old results masked and the newest %d kept verbatim, got masked=%d verbatim=%d", 3, masked, verbatim)
	}
	if res.RetainedTokens > 20000 {
		t.Fatalf("masking should collapse retained size, got %d tokens", res.RetainedTokens)
	}
}

func TestNotesOps(t *testing.T) {
	th := buildThread(4, 100)
	s := stackFor(th)
	s.Notes = NewLayer("notes:be-1", KindNotes, 1, []Segment{
		{Key: "facts", Text: "- db is postgres\n- api in Go", Vol: VolSlow},
	})
	e := core.NewBytesEstimator().WithRatio(4)
	p := &Patch{KeepFrom: 5, Notes: []NoteOp{
		{Op: "add", Key: "decisions", Text: "cursor pagination"},
		{Op: "add", Key: "decisions", Text: "cursor pagination"}, // duplicate is idempotent
		{Op: "replace", Key: "facts", Match: "api in Go", Text: "- api in Go 1.24"},
		{Op: "remove", Key: "facts", Match: "postgres"},
		{Op: "replace", Key: "facts", Match: "no such line", Text: "- appended"},
		{Op: "add", Key: "working-set", Text: "users.go:handler"},
	}}
	res, err := Apply(s, p, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := res.Notes.Segment("facts")
	if strings.Contains(facts.Text, "postgres") || !strings.Contains(facts.Text, "Go 1.24") || !strings.Contains(facts.Text, "- appended") {
		t.Fatalf("facts = %q", facts.Text)
	}
	dec, _ := res.Notes.Segment("decisions")
	if strings.Count(dec.Text, "cursor pagination") != 1 {
		t.Fatalf("add must be idempotent: %q", dec.Text)
	}
	// Volatility ordering: slow sections precede the fast working-set.
	txt := res.Notes.Text()
	if strings.Index(txt, "## facts") > strings.Index(txt, "## working-set") {
		t.Fatalf("stable sections must precede volatile ones:\n%s", txt)
	}
	if res.Notes.Version != 2 {
		t.Fatalf("notes version = %d", res.Notes.Version)
	}
}

func TestNotesOverBudgetFlag(t *testing.T) {
	th := buildThread(4, 100)
	s := stackFor(th)
	e := core.NewBytesEstimator().WithRatio(4)
	pol := DefaultApplyPolicy()
	pol.MaxSectionTokens = 50
	res, err := Apply(s, &Patch{KeepFrom: 5, Notes: []NoteOp{{Op: "add", Key: "facts", Text: strings.Repeat("very long fact ", 100)}}}, e, pol)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NotesOverBudget {
		t.Fatal("oversized section must request consolidation")
	}
}

func TestCommitThenRenderIsADeclaredRebase(t *testing.T) {
	th := buildThread(8, 6000)
	s := stackFor(th)
	s.Const = NewLayer("const", KindConst, 1, []Segment{{Text: strings.Repeat("You are Sleipnir. ", 300)}})
	e := core.NewBytesEstimator().WithRatio(4)
	caps := Caps{Dialect: "openai-chat"}
	g := &Guard{}

	render := func() *Rendered {
		s.Thread = th.Snapshot()
		return Render(s, RenderOpts{Caps: caps, Policy: DefaultPolicy(), Est: e})
	}
	// Steady state: appends never register as drift.
	g.Observe(render(), s.Thread.Epoch, e)
	addExchange(th, 100)
	c := g.Observe(render(), th.Snapshot().Epoch, e)
	if c.Drift || c.SharedBlocks == 0 {
		t.Fatalf("append-only growth flagged as drift: %+v", c)
	}

	// Commit a compaction.
	snap := th.Snapshot()
	s.Thread = snap
	res, err := Apply(s, &Patch{KeepFrom: 14}, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := th.Commit(snap.Epoch, res.Replacement, len(snap.Turns)); err != nil {
		t.Fatal(err)
	}
	s.Spine = res.Spine
	// Same epoch after a rebase would be a bug and must be flagged...
	bad := g.Observe(render(), snap.Epoch, e)
	if !bad.Drift {
		t.Fatalf("history rewrite without a declared epoch must be flagged: %+v", bad)
	}
	// ...while the new epoch legitimises it, and later appends are clean again.
	g2 := &Guard{}
	g2.Observe(render(), th.Snapshot().Epoch, e)
	addExchange(th, 101)
	if c := g2.Observe(render(), th.Snapshot().Epoch, e); c.Drift {
		t.Fatalf("post-rebase growth flagged: %+v", c)
	}
}

func TestGuardNamesTheLayerThatDrifted(t *testing.T) {
	s := newStack(t)
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("go")}})
	s.Thread = th.Snapshot()
	e := est()
	g := &Guard{}
	opts := RenderOpts{Caps: caps(), Policy: DefaultPolicy(), Est: e}
	g.Observe(Render(s, opts), 0, e)
	// Someone "just tweaks" the role pin between requests without declaring an epoch.
	s.RoleL = NewLayer("role:backend", KindRole, 1, []Segment{{Key: "conv", Text: big("rule", 2200) + " oops", Vol: VolEpoch}})
	c := g.Observe(Render(s, opts), 0, e)
	if !c.Drift || c.Diverged != "role" {
		t.Fatalf("expected drift in role layer, got %+v", c)
	}
}

func TestPlannerEconomics(t *testing.T) {
	pl := DefaultPlanner()
	// Anthropic-like: read 0.1, write 1.25.
	anth := State{PrefixTokens: 12_000, ThreadTokens: 40_000, PromptTokens: 52_000, ContextWindow: 1_000_000, Warm: true, W: cost.Weights{Read: 0.1, Write5m: 1.25}, Write: 1.25}
	out := Outcome{SpineAdded: 800, RetainedTokens: 6_000}
	if d := pl.ShouldCommit(anth, out); !d.Yes {
		t.Fatalf("big shrink over a 25-turn horizon should pay back: %+v", d)
	}
	// A 40k->6.8k shrink is so large it pays back inside two turns; the agent has
	// to be on its very last request before a warm rewrite stops being worth it.
	short := anth
	short.Remaining = 2
	if d := pl.ShouldCommit(short, out); !d.Yes {
		t.Fatalf("40k->6.8k pays back within 2 turns: %+v", d)
	}
	short.Remaining = 1
	if d := pl.ShouldCommit(short, out); d.Yes {
		t.Fatalf("one remaining turn cannot amortise a warm rewrite: %+v", d)
	}
	// Cold cache: always commit, the rewrite is free.
	cold := short
	cold.Warm = false
	if d := pl.ShouldCommit(cold, out); !d.Yes || d.NetITE <= 0 {
		t.Fatalf("cold commit must be free and positive: %+v", d)
	}
	// Hard limit overrides economics.
	hard := short
	hard.ThreadTokens = 70_000
	if d := pl.ShouldCommit(hard, Outcome{SpineAdded: 800, RetainedTokens: 6_000}); !d.Yes {
		t.Fatalf("hard limit must force commit: %+v", d)
	}
	// A patch that does not shrink anything never commits.
	if d := pl.ShouldCommit(anth, Outcome{SpineAdded: 30_000, RetainedTokens: 20_000}); d.Yes {
		t.Fatalf("non-shrinking patch: %+v", d)
	}
	// No-write-premium providers (marketplace engines) amortise faster.
	mk := anth
	mk.W, mk.Write = cost.Weights{Read: 0.25, Write5m: 1}, 1
	mk.Remaining = 4
	if d := pl.ShouldCommit(mk, out); !d.Yes {
		t.Fatalf("no write premium: should pay back inside 4 turns: %+v", d)
	}
}

func TestPlannerStartTriggers(t *testing.T) {
	pl := DefaultPlanner()
	base := State{ThreadTokens: 5_000, PromptTokens: 20_000, ContextWindow: 1_000_000, Warm: true}
	if pl.ShouldStart(base).Yes {
		t.Fatal("no pressure, warm: do nothing")
	}
	soft := base
	soft.ThreadTokens = 25_000
	if !pl.ShouldStart(soft).Yes {
		t.Fatal("soft limit")
	}
	cold := base
	cold.Warm = false
	cold.MaskableTokens, cold.SinceMask = 4_000, 100
	if d := pl.ShouldStart(cold); !d.Yes || d.Mode != ModeMask {
		t.Fatalf("a cold cache with something to mask is a free compaction window: %+v", d)
	}
	nothing := cold
	nothing.MaskableTokens = 0
	if pl.ShouldStart(nothing).Yes {
		t.Fatal("a cold cache with nothing to mask has nothing to do (a fork would pay a cold prefix write)")
	}
	recent := cold
	recent.SinceMask = 2
	if pl.ShouldStart(recent).Yes {
		t.Fatal("a mask commit just happened: no storm")
	}
	tiny := cold
	tiny.ThreadTokens = 1_000
	if pl.ShouldStart(tiny).Yes {
		t.Fatal("not worth a call below the minimum")
	}
	win := base
	win.PromptTokens = 700_000
	if !pl.ShouldStart(win).Yes {
		t.Fatal("context-window pressure")
	}
}

func TestIsCold(t *testing.T) {
	pl := DefaultPlanner()
	now := time.Now()
	ttl := 5 * time.Minute
	if pl.IsCold(now.Add(-time.Minute), now, ttl) {
		t.Fatal("1m old entry is warm")
	}
	if !pl.IsCold(now.Add(-5*time.Minute), now, ttl) {
		t.Fatal("5m old entry is cold")
	}
	if !pl.IsCold(time.Time{}, now, ttl) {
		t.Fatal("never-used prefix is cold")
	}
	if pl.IsCold(now.Add(-time.Hour), now, 0) {
		t.Fatal("no modelled TTL: rely on measured hit ratio instead")
	}
}

func TestForkPromptSharesParentPrefixExactly(t *testing.T) {
	s := newStack(t)
	th := buildThread(6, 3000)
	s.Thread = th.Snapshot()
	e := est()
	opts := RenderOpts{Caps: caps(), Policy: DefaultPolicy(), Est: e, Hot: []core.Block{core.Text("<live>board</live>")},
		Params: core.Params{MaxTokens: 4096, Effort: "high"}}
	parent := Render(s, opts)
	fork := ForkPrompt(s, opts, Instruction(s, e, DefaultApplyPolicy()))

	// Everything the parent sent except its hot tail must be an exact prefix of the fork.
	pj, fj := blocksJSON(t, parent.Prompt, core.BlockRef{Msg: 1 << 20}), blocksJSON(t, fork, core.BlockRef{Msg: 1 << 20})
	if len(fj) != len(pj)+1 {
		t.Fatalf("fork should add exactly one block (the instruction): parent %d fork %d", len(pj), len(fj))
	}
	for i := range pj {
		if pj[i] != fj[i] {
			t.Fatalf("fork diverges from parent at block %d: it would miss the parent's cache", i)
		}
	}
	// Every request parameter is the parent's: on Anthropic a changed tool_choice
	// (or thinking / effort) invalidates the messages tier and the fork would pay a
	// full write instead of reading the parent's prefix. "No tools" is text only.
	if fork.Params.ToolChoice != parent.Prompt.Params.ToolChoice || fork.Params.Effort != "high" || fork.Params.MaxTokens != 4096 {
		t.Fatalf("fork must keep the parent's params exactly: %+v vs %+v", fork.Params, parent.Prompt.Params)
	}
	if len(fork.Tools) != len(parent.Prompt.Tools) {
		t.Fatal("fork must keep the parent's tool list (tools render first)")
	}
	instr := fj[len(fj)-1]
	for _, want := range []string{"compactor-task", "keep_from", "t2-t3", "go test ./pkg1/"} {
		if !strings.Contains(instr, want) {
			t.Fatalf("instruction missing %q:\n%s", want, instr)
		}
	}
}

func TestMechanicalPatchHonoursTarget(t *testing.T) {
	th := buildThread(10, 8000)
	s := stackFor(th)
	e := core.NewBytesEstimator().WithRatio(4)
	total := s.Thread.Tokens(e)
	p := MechanicalPatch(s, e, total/4, DefaultApplyPolicy())
	res, err := Apply(s, p, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.RetainedTokens > total/3 {
		t.Fatalf("retained %d of %d tokens, target was %d", res.RetainedTokens, total, total/4)
	}
	if res.RemovedTurns == 0 || res.Mechanical == 0 {
		t.Fatalf("expected folded turns with mechanical lines: %+v", res)
	}
	if err := Validate(res.Replacement); err != nil {
		t.Fatal(err)
	}
}
