package kv

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
)

func est() core.Estimator { return core.NewBytesEstimator().WithRatio(4) }

func big(word string, tokens int) string {
	return strings.Repeat(word+" ", tokens) // ~ (len(word)+1)/4 tokens per repeat with ratio 4 -> scale below
}

func newStack(t *testing.T) *Stack {
	t.Helper()
	tools, err := SortTools([]core.ToolSpec{
		{Name: "zeta", Description: "z", InputSchema: json.RawMessage(`{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"string"}}}`)},
		{Name: "alpha", Description: "a", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Stack{
		Agent: "be-1", Role: "backend", Model: "test-model",
		Tools:  tools,
		Const:  NewLayer("const", KindConst, 1, []Segment{{Key: "", Text: strings.Repeat("You are Sleipnir. ", 400)}}),
		Shared: NewLayer("shared", KindShared, 1, []Segment{{Key: "repo", Text: big("file", 2500), Vol: VolEpoch}}),
		RoleL:  NewLayer("role:backend", KindRole, 1, []Segment{{Key: "conv", Text: big("rule", 2200), Vol: VolEpoch}}),
		Notes:  NewLayer("notes:be-1", KindNotes, 1, []Segment{{Key: "facts", Text: big("fact", 1800), Vol: VolSlow}}),
		Spine:  NewLayer("spine:be-1", KindSpine, 1, []Segment{{Text: "t1-t4 explored repo", Vol: VolFast}}),
	}
}

func caps() Caps {
	return Caps{Dialect: "anthropic", MaxBreakpoints: 4, LookbackBlocks: 20, MinPrefixTokens: 512, ReplayThinking: true}
}

func addExchange(th *Thread, n int) {
	id := "toolu_" + strings.Repeat("x", n)
	th.Append(core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{
		core.Text("looking"), core.ToolUse(id, "alpha", json.RawMessage(`{}`)),
	}})
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{
		core.ToolResult(id, false, core.Text(strings.Repeat("output line\n", 50))),
	}})
}

func blocksJSON(t *testing.T, p *core.Prompt, upTo core.BlockRef) []string {
	t.Helper()
	var out []string
	p.WalkBlocks(func(ref core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		if b == nil || b.Ephemeral {
			return
		}
		if !ref.Sys && (ref.Msg > upTo.Msg || (ref.Msg == upTo.Msg && ref.Blk > upTo.Blk)) {
			return
		}
		j, _ := json.Marshal(b)
		out = append(out, string(j))
	})
	return out
}

func TestLayerHashIgnoresVersion(t *testing.T) {
	a := NewLayer("shared", KindShared, 1, []Segment{{Key: "k", Text: "same", Vol: VolEpoch}})
	b := a.With([]Segment{{Key: "k", Text: "same", Vol: VolEpoch}})
	if a.Hash() != b.Hash() || b.Version != 2 {
		t.Fatalf("version leaked into bytes or not bumped: %v %v v%d", a.Hash().Short(), b.Hash().Short(), b.Version)
	}
}

func TestSegmentsOrderedByVolatility(t *testing.T) {
	l := NewLayer("n", KindNotes, 1, []Segment{
		{Key: "fast", Text: "F", Vol: VolFast},
		{Key: "frozen", Text: "Z", Vol: VolFrozen},
		{Key: "slow", Text: "S", Vol: VolSlow},
	})
	iz, is, iff := strings.Index(l.Text(), "## frozen"), strings.Index(l.Text(), "## slow"), strings.Index(l.Text(), "## fast")
	if !(iz < is && is < iff) {
		t.Fatalf("segments not ordered stable->volatile:\n%s", l.Text())
	}
}

func TestRenderIsAppendOnlyPrefixStable(t *testing.T) {
	s := newStack(t)
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("implement pagination")}})
	addExchange(th, 1)

	s.Thread = th.Snapshot()
	r1 := Render(s, RenderOpts{Caps: caps(), Policy: DefaultPolicy(), Est: est(), Hot: []core.Block{core.Text("<live>board v1</live>")}})
	roll1 := r1.Prompt.Breakpoints[len(r1.Prompt.Breakpoints)-1].After
	before := blocksJSON(t, r1.Prompt, roll1)

	addExchange(th, 2)
	s.Thread = th.Snapshot()
	r2 := Render(s, RenderOpts{Caps: caps(), Policy: DefaultPolicy(), Est: est(), Hot: []core.Block{core.Text("<live>board v2 changed</live>")}})
	after := blocksJSON(t, r2.Prompt, roll1)

	if len(after) != len(before) {
		t.Fatalf("prefix length changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("block %d changed between requests:\n%s\n%s", i, before[i], after[i])
		}
	}
	if err := Validate(th.Snapshot().Turns); err != nil {
		t.Fatal(err)
	}
}

func TestHotTailIsAfterLastBreakpointAndEphemeral(t *testing.T) {
	s := newStack(t)
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("go")}})
	s.Thread = th.Snapshot()
	r := Render(s, RenderOpts{Caps: caps(), Policy: DefaultPolicy(), Est: est(), Hot: []core.Block{core.Text("HOT")}})
	last := r.Prompt.Messages[len(r.Prompt.Messages)-1]
	hot := last.Blocks[len(last.Blocks)-1]
	if !hot.Ephemeral || hot.Text != "HOT" {
		t.Fatalf("hot block wrong: %+v", hot)
	}
	rolling := r.Prompt.Breakpoints[len(r.Prompt.Breakpoints)-1]
	if rolling.After.Blk != len(last.Blocks)-2 {
		t.Fatalf("rolling breakpoint at blk %d, want %d (just before hot)", rolling.After.Blk, len(last.Blocks)-2)
	}
}

func TestBreakpointPlanning(t *testing.T) {
	s := newStack(t)
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("go")}})
	s.Thread = th.Snapshot()

	labels := func(c Caps, pol Policy) []string {
		r := Render(s, RenderOpts{Caps: c, Policy: pol, Est: est()})
		var out []string
		for _, b := range r.Prompt.Breakpoints {
			out = append(out, b.Label)
		}
		return out
	}
	if got := labels(caps(), DefaultPolicy()); strings.Join(got, ",") != "shared,role,notes,thread" {
		t.Fatalf("4 breakpoints: %v", got)
	}
	c2 := caps()
	c2.MaxBreakpoints = 2
	if got := labels(c2, DefaultPolicy()); strings.Join(got, ",") != "shared,thread" {
		t.Fatalf("2 breakpoints should keep thread+shared: %v", got)
	}
	c0 := caps()
	c0.MaxBreakpoints = 0
	if got := labels(c0, DefaultPolicy()); len(got) != 0 {
		t.Fatalf("automatic-caching providers get no markers: %v", got)
	}
	// A role pin under the provider minimum gets no marker of its own (the slot goes
	// to the constitution instead); the shared pin needs only a big enough prefix.
	s.RoleL = NewLayer("role:backend", KindRole, 2, []Segment{{Key: "c", Text: "tiny", Vol: VolEpoch}})
	if got := labels(caps(), DefaultPolicy()); strings.Join(got, ",") != "const,shared,notes,thread" {
		t.Fatalf("tiny role layer should not get a breakpoint: %v", got)
	}
	// Prompt shorter than the provider minimum is not cacheable at all.
	small := &Stack{Model: "m", Const: NewLayer("c", KindConst, 1, []Segment{{Text: "hi"}})}
	small.Thread = Snapshot{Turns: []core.Turn{{ID: 1, Role: core.RoleUser, Blocks: []core.Block{core.Text("x")}}}}
	r := Render(small, RenderOpts{Caps: caps(), Policy: DefaultPolicy(), Est: est()})
	if len(r.Prompt.Breakpoints) != 0 {
		t.Fatalf("sub-minimum prompt must not get breakpoints: %+v", r.Prompt.Breakpoints)
	}
}

func TestThinkingReplayRules(t *testing.T) {
	s := newStack(t)
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("go")}})
	th.Append(core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{
		{Kind: core.BlockThinking, Wire: json.RawMessage(`{"type":"thinking","thinking":"hm","signature":"sig"}`), WireFormat: "anthropic"},
		core.Text("done"),
	}})
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("more")}})
	s.Thread = th.Snapshot()
	count := func(o RenderOpts) int {
		n := 0
		for _, m := range Render(s, o).Prompt.Messages {
			for _, b := range m.Blocks {
				if b.Kind == core.BlockThinking {
					n++
				}
			}
		}
		return n
	}
	base := RenderOpts{Caps: caps(), Policy: DefaultPolicy(), Est: est()}
	if count(base) != 1 {
		t.Fatal("matching dialect should replay thinking verbatim")
	}
	strip := base
	strip.StripThinking = true
	if count(strip) != 0 {
		t.Fatal("StripThinking must drop thinking")
	}
	other := base
	other.Caps.Dialect = "openai-chat"
	if count(other) != 0 {
		t.Fatal("foreign-dialect thinking must not be replayed")
	}
}

func TestUnitsKeepToolPairsTogether(t *testing.T) {
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("task")}})
	addExchange(th, 1)
	addExchange(th, 2)
	th.Append(core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("done")}})
	us := Units(th.Snapshot().Turns)
	if len(us) != 4 {
		t.Fatalf("units = %d, want 4 (task, ex1, ex2, final)", len(us))
	}
	if us[1].End-us[1].Start != 2 {
		t.Fatal("exchange must span two turns")
	}
}

func TestValidateCatchesOrphans(t *testing.T) {
	bad := []core.Turn{
		{ID: 1, Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("nope", false, core.Text("x"))}},
	}
	if Validate(bad) == nil {
		t.Fatal("orphan tool_result must fail validation")
	}
	unanswered := []core.Turn{
		{ID: 1, Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("a", "alpha", json.RawMessage(`{}`))}},
		{ID: 2, Role: core.RoleUser, Blocks: []core.Block{core.Text("hi")}},
	}
	if Validate(unanswered) == nil {
		t.Fatal("unanswered tool_use must fail validation")
	}
}

func TestThreadCommitCAS(t *testing.T) {
	th := NewThread()
	for i := 0; i < 3; i++ {
		th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("x")}})
	}
	snap := th.Snapshot()
	// Turns appended after the snapshot survive a commit derived from it.
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("late")}})
	repl := []core.Turn{{ID: 3, Role: core.RoleUser, Blocks: []core.Block{core.Text("digest")}}}
	if err := th.Commit(snap.Epoch, repl, len(snap.Turns)); err != nil {
		t.Fatal(err)
	}
	got := th.Snapshot()
	if len(got.Turns) != 2 || got.Turns[1].Blocks[0].Text != "late" || got.Epoch != 1 {
		t.Fatalf("unexpected thread after commit: %+v", got)
	}
	if err := th.Commit(snap.Epoch, repl, len(snap.Turns)); err != ErrStaleEpoch {
		t.Fatalf("stale commit must be rejected, got %v", err)
	}
}

func TestPrefixKeySharedAcrossAgents(t *testing.T) {
	a := newStack(t)
	b := newStack(t)
	b.Agent = "be-2"
	b.Notes = NewLayer("notes:be-2", KindNotes, 1, []Segment{{Key: "facts", Text: "different", Vol: VolSlow}})
	if a.PrefixKey() != b.PrefixKey() {
		t.Fatal("agents of one role must share a prefix key regardless of private notes")
	}
	b.RoleL = NewLayer("role:frontend", KindRole, 1, []Segment{{Key: "c", Text: "other role", Vol: VolEpoch}})
	if a.PrefixKey() == b.PrefixKey() {
		t.Fatal("different role pins must yield different prefix keys")
	}
}
