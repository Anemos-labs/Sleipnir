package kv

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func TestResolveHotPicksTheMechanismForTheRoute(t *testing.T) {
	for _, c := range []struct {
		name string
		want HotMode
		caps Caps
		pt   bool
		got  HotMode
	}{
		{"plain route", HotInline, Caps{}, false, HotInline},
		{"preserved thinking replayed, no turn-scoped: persist on change", HotInline, Caps{ReplayThinking: true}, true, HotPersist},
		{"preserved thinking but not replayed: nothing is bound", HotInline, Caps{}, true, HotInline},
		{"replayed thinking on a model that does not enforce bindings", HotInline, Caps{ReplayThinking: true}, false, HotInline},
		{"provider with turn-scoped system messages", HotInline, Caps{TurnScopedSystem: true}, false, HotTurnScoped},
		{"turn-scoped beats persist", HotInline, Caps{TurnScopedSystem: true, ReplayThinking: true}, true, HotTurnScoped},
		{"explicit persist", HotPersist, Caps{}, false, HotPersist},
		{"explicit turn-scoped where supported", HotTurnScoped, Caps{TurnScopedSystem: true}, false, HotTurnScoped},
		{"explicit turn-scoped without support falls back to persist", HotTurnScoped, Caps{}, false, HotPersist},
	} {
		if got := ResolveHot(c.want, c.caps, c.pt); got != c.got {
			t.Errorf("%s: ResolveHot = %v, want %v", c.name, got, c.got)
		}
	}
}

func cxNoticeThread(tasks int) *Thread {
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{
		core.Text("implement pagination"), Notice(`<live board="v1">first board</live>`)}})
	for i := 0; i < tasks; i++ {
		id := fmt.Sprint("n", i)
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{
			core.Text("step " + id), core.ToolUse("c"+id, "bash", json.RawMessage(`{}`))}})
		blocks := []core.Block{core.ToolResult("c"+id, false, core.Text(cxText("out ", 200)))}
		if i%2 == 1 { // a persisted notice every other turn
			blocks = append(blocks, Notice(fmt.Sprintf(`<live board="v%d">board %d</live>`, i+2, i)))
		}
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: blocks})
	}
	return th
}

func TestApplyKeepsOnlyTheNewestPersistedNoticeAndNeverCopiesOneIntoInstructions(t *testing.T) {
	e := cxEst()
	th := cxNoticeThread(8)
	s := cxStack(t, "be-1", cxSizes{constT: 300})
	s.Thread = th.Snapshot()
	res, err := Apply(s, &Patch{KeepFrom: s.Thread.Turns[5].ID}, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, tr := range res.Replacement {
		for _, b := range tr.Blocks {
			if IsNotice(b) {
				kept = append(kept, b.Text)
			}
		}
	}
	if len(kept) != 1 || !strings.Contains(kept[0], "board 7") {
		t.Fatalf("exactly the newest notice must survive a commit, got %v", kept)
	}
	if res.NoticesDropped == 0 {
		t.Fatal("stale notices in the retained region must be reported as dropped")
	}
	seg, _ := res.Notes.Segment("instructions")
	if !strings.Contains(seg.Text, "implement pagination") || strings.Contains(seg.Text, "<live") || strings.Contains(seg.Text, "first board") {
		t.Fatalf("the persisted hot view is not user text:\n%s", seg.Text)
	}
	if !IsNotice(Notice("x")) || IsNotice(core.Text("x")) || IsSteer(Notice("x")) || !IsSteer(Steer("x")) {
		t.Fatal("marker helpers")
	}
}

func TestTurnScopedHotIsAPersistedSystemMessage(t *testing.T) {
	e := cxEst()
	s := cxStack(t, "be-1", cxSizes{constT: 3000, shared: 2000, notes: 200})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("go")}})
	step := func(i int) {
		id := fmt.Sprint("s", i)
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text("step"), core.ToolUse("c"+id, "bash", json.RawMessage(`{}`))}})
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{core.ToolResult("c"+id, false, core.Text("ok "+id))}})
		th.Append(core.Turn{Role: core.RoleSystem, Origin: core.OriginSystem, Blocks: []core.Block{core.Text(fmt.Sprintf("<live>board after step %d</live>", i))}})
	}
	step(1)
	step(2)
	caps := cxCaps()
	caps.HotMode, caps.TurnScopedSystem = HotTurnScoped, true
	r1 := cxRenderOpts(s, th, RenderOpts{Caps: caps, Policy: DefaultPolicy(), Est: e})
	var roles []string
	for _, m := range r1.Prompt.Messages {
		roles = append(roles, string(m.Role))
		if m.Role == core.RoleSystem && m.ClearAt != ClearAtNextUser {
			t.Fatalf("system message without clear_at: %+v", m)
		}
		if m.Role != core.RoleSystem && m.ClearAt != "" {
			t.Fatalf("clear_at on a %s message", m.Role)
		}
	}
	if got := strings.Join(roles, ","); got != "user,assistant,user,system,assistant,user,system" {
		t.Fatalf("roles = %s", got)
	}
	// The rolling marker never sits on the trailing system message.
	roll := r1.Rolling
	if roll == nil || roll.Msg != 5 {
		t.Fatalf("rolling marker must move to the last markable block before the system message: %+v", roll)
	}
	// Earlier copies stay byte for byte: growth is append-only.
	step(3)
	r2 := cxRenderOpts(s, th, RenderOpts{Caps: caps, Policy: DefaultPolicy(), Est: e})
	for i, m := range r1.Prompt.Messages {
		a, _ := json.Marshal(m)
		b, _ := json.Marshal(r2.Prompt.Messages[i])
		if string(a) != string(b) {
			t.Fatalf("message %d changed between requests:\n%s\n%s", i, a, b)
		}
	}
	// The guard counts them as persistent (they are bytes the provider hashes).
	g := &Guard{}
	g.Observe(r1, 0, e)
	if c := g.Observe(r2, 0, e); c.Drift {
		t.Fatalf("append-only turn-scoped growth is not drift: %+v", c)
	}
	// A unit keeps the trailing system messages with the turn they follow.
	us := Units(th.Snapshot().Turns)
	if len(us) != 4 || us[1].End-us[1].Start != 3 || us[3].End-us[3].Start != 3 {
		t.Fatalf("units = %+v, want the task and three exchanges of three turns", us)
	}
	if err := Validate(th.Snapshot().Turns); err != nil {
		t.Fatal(err)
	}
	// Cleared messages cost nothing: only the newest counts.
	z := Sizer{Est: e, Caps: caps}
	got, want := z.Turns(th.Snapshot().Turns), (Sizer{Est: e}).Turns(th.Snapshot().Turns)
	if got >= want {
		t.Fatalf("cleared system messages must not count: %d vs %d", got, want)
	}

	// Without the feature the same turns fold into ordinary user content.
	plain := cxCaps()
	rf := cxRenderOpts(s, th, RenderOpts{Caps: plain, Policy: DefaultPolicy(), Est: e})
	for i, m := range rf.Prompt.Messages {
		if m.Role == core.RoleSystem || m.ClearAt != "" {
			t.Fatalf("message %d: system role leaked to a provider without the feature", i)
		}
		if i > 0 && m.Role == rf.Prompt.Messages[i-1].Role {
			t.Fatalf("messages %d and %d have the same role after folding", i-1, i)
		}
	}

	// Compaction drops cleared system turns and keeps the newest.
	res, err := Apply(s, &Patch{KeepFrom: th.Snapshot().Turns[2].ID}, e, DefaultApplyPolicy())
	if err == nil {
		sys := 0
		for _, tr := range res.Replacement {
			if tr.Role == core.RoleSystem {
				sys++
			}
		}
		if sys > 1 {
			t.Fatalf("only the newest turn-scoped message can still matter, kept %d", sys)
		}
	}
}

func TestSizerCountsWhatRenderSends(t *testing.T) {
	e := cxEst()
	reasoning := core.Block{Kind: core.BlockThinking, Text: cxText("hmm ", 1500)} // plain reasoning: no wire form
	signed := core.Block{Kind: core.BlockThinking, Text: "x", Wire: json.RawMessage(`{"type":"thinking","thinking":"` + cxText("y", 400) + `","signature":"sig"}`), WireFormat: "anthropic"}
	turn := core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{reasoning, signed, core.Text("ok")}}
	anth := Sizer{Est: e, Caps: Caps{Dialect: "anthropic", ReplayThinking: true}}
	chat := Sizer{Est: e, Caps: Caps{Dialect: "openai-chat"}}
	if got := anth.Block(reasoning); got != 0 {
		t.Fatalf("reasoning without a wire form is never sent, counted %d", got)
	}
	if got := anth.Block(signed); got < 400 {
		t.Fatalf("a replayed thinking block counts its wire form, got %d", got)
	}
	if got := chat.Block(signed); got != 0 {
		t.Fatalf("thinking of another dialect is dropped by Render, counted %d", got)
	}
	if chat.Turn(turn) >= TurnTokens(turn, e)/10 {
		t.Fatalf("the planner's view (%d) must not carry reasoning that is never sent (stored %d)", chat.Turn(turn), TurnTokens(turn, e))
	}
	// And it agrees with the renderer.
	s := cxStack(t, "a", cxSizes{constT: 600})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("go")}})
	th.Append(turn)
	s.Thread = th.Snapshot()
	for _, z := range []Sizer{anth, chat} {
		r := Render(s, RenderOpts{Caps: z.Caps, Est: e})
		n := 0
		for _, m := range r.Prompt.Messages[1:] {
			for _, b := range m.Blocks {
				n += SentBlockTokens(b, e)
			}
		}
		if got := z.Block(signed) + z.Block(reasoning) + z.Block(core.Text("ok")); got != n {
			t.Fatalf("%s: sizer %d vs rendered %d", z.Caps.Dialect, got, n)
		}
	}
}

func TestMaskOnlyNeedsSomethingToMask(t *testing.T) {
	e := cxEst()
	pol := DefaultApplyPolicy()
	think := core.Block{Kind: core.BlockThinking, Wire: json.RawMessage(`{"type":"thinking","signature":"s"}`), WireFormat: "anthropic"}
	small := NewThread()
	small.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("go")}})
	for i := 0; i < 12; i++ {
		id := fmt.Sprint("m", i)
		small.Append(core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{think, core.Text("s"), core.ToolUse(id, "bash", json.RawMessage(`{}`))}})
		small.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult(id, false, core.Text("ok"))}})
	}
	s := &Stack{Agent: "a", Model: "m", Thread: small.Snapshot()}
	if n := MaskableTokens(s, e, pol); n != 0 {
		t.Fatalf("nothing is bulky: maskable=%d", n)
	}
	// Stripping thinking alone rewrites the thread for no saving: not a compaction.
	if _, err := MaskOnly(s, e, pol); err != ErrNothingToMask {
		t.Fatalf("MaskOnly must refuse when nothing would be masked, got %v", err)
	}
	big := buildThread(12, 12000)
	s2 := stackFor(big)
	want := MaskableTokens(s2, e, pol)
	res, err := MaskOnly(s2, e, pol)
	if err != nil {
		t.Fatal(err)
	}
	if want == 0 || res.MaskedTokens != want || res.MaskedResults == 0 {
		t.Fatalf("MaskableTokens (%d) must predict what MaskOnly saves (%d, %d results)", want, res.MaskedTokens, res.MaskedResults)
	}
	if err := Validate(res.Replacement); err != nil {
		t.Fatal(err)
	}
}

func TestPlanMarksSpendsSlotsInPriorityOrder(t *testing.T) {
	caps := Caps{MaxBreakpoints: 4, LookbackBlocks: 20, MinPrefixTokens: 1000}
	pol := DefaultPolicy()
	blocks := func(n int) []PlanBlock {
		out := []PlanBlock{{Tokens: 200, NoMark: true}, {Tokens: 2000, End: "const", LayerTokens: 2200}}
		out = append(out, PlanBlock{Tokens: 800, End: "shared", LayerTokens: 800})
		out = append(out, PlanBlock{Tokens: 1200, End: "role", LayerTokens: 1200})
		out = append(out, PlanBlock{Tokens: 1600, End: "notes", LayerTokens: 1600})
		for i := 0; i < n; i++ {
			out = append(out, PlanBlock{Tokens: 300})
		}
		return out
	}
	labels := func(ms []Mark) string {
		var l []string
		for _, m := range ms {
			l = append(l, m.Label)
		}
		return strings.Join(l, ",")
	}
	if got := labels(PlanMarks(blocks(3), -1, caps, pol)); got != "shared,role,notes,thread" {
		t.Fatalf("all four layers contested: %s", got)
	}
	caps2 := caps
	caps2.MaxBreakpoints = 2
	if got := labels(PlanMarks(blocks(3), -1, caps2, pol)); got != "shared,thread" {
		t.Fatalf("with two slots the rolling and the shared marker win: %s", got)
	}
	caps3 := caps
	caps3.MaxBreakpoints = 5
	if got := labels(PlanMarks(blocks(3), -1, caps3, pol)); got != "const,shared,role,notes,thread" {
		t.Fatalf("a spare slot goes to the constitution: %s", got)
	}
	// Below the provider minimum nothing can be cached, and a marker whose own
	// prefix is under it is skipped.
	if PlanMarks([]PlanBlock{{Tokens: 100}, {Tokens: 300}}, -1, caps, pol) != nil {
		t.Fatal("prompt shorter than the provider minimum gets no markers")
	}
	// A burst since the previous rolling marker gets an anchor; each hop fits.
	bl := blocks(40)
	marks := PlanMarks(bl, len(blocks(2))-1, caps, pol)
	if !strings.Contains(labels(marks), "anchor") {
		t.Fatalf("40 new positions need an anchor: %s", labels(marks))
	}
	prev := len(blocks(2)) - 1
	positions := map[int]bool{prev: true}
	for _, m := range marks {
		if m.Label == "anchor" || m.Label == "thread" {
			positions[m.Block] = true
		}
	}
	last := prev
	for i := prev + 1; i < len(bl); i++ {
		if positions[i] {
			if i-last > caps.LookbackBlocks-1 {
				t.Fatalf("hop %d -> %d exceeds the lookback window", last, i)
			}
			last = i
		}
	}
	// Runs of tool_use / tool_result count once.
	runs := blocks(0)
	for i := 0; i < 30; i++ {
		runs = append(runs, PlanBlock{Tokens: 100, Run: RunToolResult})
	}
	if got := labels(PlanMarks(runs, len(blocks(0))-1, caps, pol)); strings.Contains(got, "anchor") {
		t.Fatalf("a run of 30 tool results is one position and needs no anchor: %s", got)
	}
	// Blocks that cannot carry a marker move the rolling marker back.
	tail := append(blocks(2), PlanBlock{Tokens: 50, NoMark: true})
	ms := PlanMarks(tail, -1, caps, pol)
	if ms[len(ms)-1].Label != "thread" || ms[len(ms)-1].Block != len(tail)-2 {
		t.Fatalf("rolling marker %+v must skip the unmarkable tail block", ms)
	}
	// Shared and role markers carry the long TTL and precede the 5-minute ones.
	pol.SharedTTL = 3600 * 1e9
	for _, m := range PlanMarks(blocks(3), -1, caps, pol) {
		if (m.Label == "shared" || m.Label == "role") != (m.TTL != 0) {
			t.Fatalf("only shared and role carry SharedTTL: %+v", m)
		}
	}
}

func TestThreadRewriteAndCommitWithLeaveSnapshotsUntouched(t *testing.T) {
	th := NewThread()
	think := core.Block{Kind: core.BlockThinking, Wire: json.RawMessage(`{"signature":"s"}`), WireFormat: "anthropic"}
	th.Append(core.Turn{Role: core.RoleUser, Blocks: []core.Block{core.Text("go")}})
	th.Append(core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{think, core.Text("a")}})
	snap := th.Snapshot()
	if th.Rewrite(func(tr core.Turn) (core.Turn, bool) { return tr, false }) || th.Snapshot().Epoch != snap.Epoch {
		t.Fatal("a rewrite that changes nothing must not start an epoch")
	}
	if !th.Rewrite(func(tr core.Turn) (core.Turn, bool) { return StripThinkingTurn(tr) }) {
		t.Fatal("expected a change")
	}
	if len(snap.Turns[1].Blocks) != 2 || snap.Turns[1].Blocks[0].Kind != core.BlockThinking {
		t.Fatal("an earlier snapshot must never see the rewrite")
	}
	if now := th.Snapshot(); now.Epoch != snap.Epoch+1 || len(now.Turns[1].Blocks) != 1 || HasThinking(now.Turns) {
		t.Fatalf("rewrite result: %+v", now)
	}
	if _, changed := StripThinking(th.Snapshot().Turns); changed {
		t.Fatal("nothing left to strip")
	}
}

func TestApplyPolicyWithDefaultsFillsBoundsButKeepsDeliberateZeros(t *testing.T) {
	if got := (ApplyPolicy{}).WithDefaults(); got != DefaultApplyPolicy() {
		t.Fatalf("zero policy = defaults, got %+v", got)
	}
	got := ApplyPolicy{MinKeepUnits: 3, AutoMaskAfterUnits: 0, StripThinking: false, MaxSectionTokens: 100}.WithDefaults()
	d := DefaultApplyPolicy()
	if got.MinKeepUnits != 3 || got.AutoMaskAfterUnits != 0 || got.StripThinking || got.MaxSectionTokens != 100 {
		t.Fatalf("deliberate values must survive: %+v", got)
	}
	if got.MaxSpineLineChars != d.MaxSpineLineChars || got.MaxSpineTokens != d.MaxSpineTokens || got.UserInstructionKey != d.UserInstructionKey ||
		got.MaxInstructionTokens != d.MaxInstructionTokens || got.MaskMinTokens != d.MaskMinTokens {
		t.Fatalf("size bounds must be filled: %+v", got)
	}
	caps := Caps{Dialect: "x"}
	if (ApplyPolicy{Caps: caps}).WithDefaults().Caps != caps {
		t.Fatal("caps must survive")
	}
}

func TestNotesAreHeldToTheirBudgetAndTheCompactorIsAskedToConsolidate(t *testing.T) {
	e := cxEst()
	pol := DefaultApplyPolicy()
	pol.MaxSectionTokens, pol.MaxNotesTokens = 300, 500
	th := buildThread(6, 200)
	s := stackFor(th)
	var ops []NoteOp
	for i := 0; i < 40; i++ {
		ops = append(ops, NoteOp{Op: "add", Key: "facts", Text: fmt.Sprintf("- fact %02d: %s", i, cxText("x", 20))})
	}
	for i := 0; i < 20; i++ {
		ops = append(ops, NoteOp{Op: "add", Key: "decisions", Text: fmt.Sprintf("- decision %02d: %s", i, cxText("y", 20))})
	}
	res, err := Apply(s, &Patch{KeepFrom: s.Thread.Turns[len(s.Thread.Turns)-3].ID, Notes: ops}, e, pol)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NotesOverBudget || len(res.NotesEvicted) == 0 {
		t.Fatalf("over-budget notes must be trimmed and reported: %+v", res.NotesEvicted)
	}
	facts, _ := res.Notes.Segment("facts")
	dec, _ := res.Notes.Segment("decisions")
	if e.Tokens(facts.Text) > 300 || e.Tokens(dec.Text) > 300 {
		t.Fatalf("sections over their cap: %d, %d", e.Tokens(facts.Text), e.Tokens(dec.Text))
	}
	if e.Tokens(facts.Text)+e.Tokens(dec.Text) > pol.MaxNotesTokens {
		t.Fatalf("notes total %d over budget %d", e.Tokens(facts.Text)+e.Tokens(dec.Text), pol.MaxNotesTokens)
	}
	if !strings.Contains(facts.Text, "fact 39") || !strings.Contains(facts.Text, "older lines archived to fit the notes budget") {
		t.Fatalf("newest lines stay, trimmed ones leave a marker:\n%s", facts.Text)
	}
	// The next compactor instruction asks for consolidation while notes are near
	// budget (before anything has to be trimmed).
	s.Notes = NewLayer("notes", KindNotes, 1, []Segment{{Key: "facts", Text: cxText("f", 270), Vol: VolSlow}})
	if instr := Instruction(s, e, pol); !strings.Contains(instr, "is near its budget") {
		t.Fatalf("instruction must ask for a section near its cap to be consolidated:\n%s", instr)
	}
	s.Notes = NewLayer("notes", KindNotes, 1, []Segment{{Key: "facts", Text: cxText("f", 210), Vol: VolSlow}, {Key: "decisions", Text: cxText("d", 200), Vol: VolSlow}})
	if instr := Instruction(s, e, pol); !strings.Contains(instr, "Notes are near their budget") {
		t.Fatalf("instruction must ask for the notes as a whole to be consolidated:\n%s", instr)
	}
	s.Notes = NewLayer("notes", KindNotes, 1, []Segment{{Key: "facts", Text: "- one fact", Vol: VolSlow}})
	if instr := Instruction(s, e, pol); strings.Contains(instr, "budget") {
		t.Fatal("no hint while notes are small")
	}
}

// sysPlacementProblem is what the API enforces about role:system messages in the
// array (reference: "Mid-conversation system messages"): never first, after a user
// message, and either last or followed by an assistant turn.
func sysPlacementProblem(p *core.Prompt) string {
	for i, m := range p.Messages {
		switch {
		case i > 0 && m.Role == p.Messages[i-1].Role:
			return fmt.Sprintf("messages %d and %d are both %s", i-1, i, m.Role)
		case m.Role != core.RoleSystem:
		case i == 0:
			return "a system message is first"
		case p.Messages[i-1].Role != core.RoleUser:
			return fmt.Sprintf("message %d: a system message must follow a user message", i)
		case i < len(p.Messages)-1 && p.Messages[i+1].Role != core.RoleAssistant:
			return fmt.Sprintf("message %d: a system message must be last or followed by an assistant message", i)
		}
	}
	return ""
}

func tsCaps() Caps {
	c := cxCaps()
	c.HotMode, c.TurnScopedSystem = HotTurnScoped, true
	return c
}

func tsTurn(role core.Role, text string) core.Turn {
	origin := core.OriginModel
	switch role {
	case core.RoleUser:
		origin = core.OriginUser
	case core.RoleSystem:
		origin = core.OriginSystem
	}
	return core.Turn{Role: role, Origin: origin, Blocks: []core.Block{core.Text(text)}}
}

// A turn-scoped system message is only sent as one where the API accepts it. A
// board view followed by a user turn (a retry after a failed request, mail behind
// a tool result) folds into the user text instead of becoming a 400.
func TestTurnScopedSystemMessagesObeyThePlacementRules(t *testing.T) {
	roles := map[string]core.Role{"u": core.RoleUser, "a": core.RoleAssistant, "s": core.RoleSystem}
	shape := func(seq string) *Thread {
		th := NewThread()
		for i, c := range seq {
			th.Append(tsTurn(roles[string(c)], fmt.Sprintf("turn %d", i)))
		}
		return th
	}
	for _, tc := range []struct {
		seq       string
		wantRoles string // roles of the rendered messages after the preamble
	}{
		{"usausau", "system,assistant,user,system,assistant,user"}, // the normal loop: a view behind each user turn, then the answer
		{"us", "system"},                     // the task and its view
		{"usu", ""},                          // a retry behind an unanswered view: the view folds into the user message
		{"usssa", "system,assistant"},        // views in a row: only the one before the assistant turn stays a system message
		{"uasa", "assistant,user,assistant"}, // a view behind an assistant turn does not follow a user message: folded
		{"uas", "assistant,user"},
	} {
		s := cxStack(t, "be-1", cxSizes{constT: 600})
		r := cxRenderOpts(s, shape(tc.seq), RenderOpts{Caps: tsCaps(), Policy: DefaultPolicy()})
		if msg := sysPlacementProblem(r.Prompt); msg != "" {
			t.Errorf("%s: %s", tc.seq, msg)
		}
		var got []string
		for _, m := range r.Prompt.Messages[1:] {
			got = append(got, string(m.Role))
		}
		if strings.Join(got, ",") != tc.wantRoles {
			t.Errorf("%s: roles %v, want %s", tc.seq, got, tc.wantRoles)
		}
	}

	// Random shapes, including ones the agent never produces.
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 500; iter++ {
		var sb strings.Builder
		for i, n := 0, 2+rng.Intn(14); i < n; i++ {
			sb.WriteByte("uuasas"[rng.Intn(6)])
		}
		s := cxStack(t, "be-1", cxSizes{constT: 600})
		r := cxRenderOpts(s, shape(sb.String()), RenderOpts{Caps: tsCaps(), Policy: DefaultPolicy()})
		if msg := sysPlacementProblem(r.Prompt); msg != "" {
			t.Fatalf("thread %q: %s", sb.String(), msg)
		}
		if r.Prompt.Messages[0].Role != core.RoleUser {
			t.Fatalf("thread %q: first message is %s", sb.String(), r.Prompt.Messages[0].Role)
		}
	}
}

// The sizer prices what Render sends: a view that is folded into user text is sent
// in full, one that renders as a cleared system message is not.
func TestSizerCountsFoldedBoardViewsAndSkipsClearedOnes(t *testing.T) {
	e := cxEst()
	z := Sizer{Est: e, Caps: tsCaps()}
	board := core.Turn{Role: core.RoleSystem, Origin: core.OriginSystem, Blocks: []core.Block{core.Text(cxText("board ", 500))}}
	user := func(s string) core.Turn { return tsTurn(core.RoleUser, s) }
	asst := func(s string) core.Turn { return tsTurn(core.RoleAssistant, s) }

	normal := []core.Turn{user("task"), board, asst("a"), user("r"), board}
	folded := []core.Turn{user("task"), board, user("retry"), board}
	if got, want := z.Turns(normal), z.Turn(user("task"))+z.Turn(asst("a"))+z.Turn(user("r"))+z.Turn(board); got != want {
		t.Fatalf("normal: %d, want %d (the first view is cleared, the last one counts)", got, want)
	}
	// The first view is followed by a user turn, so Render folds it into user
	// text: it is sent in full. Only the trailing one renders as a system message.
	if got, want := z.Turns(folded), z.Turn(user("task"))+z.Turn(board)+z.Turn(user("retry"))+z.Turn(board); got != want {
		t.Fatalf("folded: %d, want %d", got, want)
	}

	// And it agrees with the renderer's size for the same shapes.
	for name, turns := range map[string][]core.Turn{"normal": normal, "folded": folded} {
		s := cxStack(t, "be-1", cxSizes{constT: 600})
		th := NewThread()
		for _, tr := range turns {
			th.Append(tr)
		}
		r := cxRenderOpts(s, th, RenderOpts{Caps: tsCaps(), Policy: DefaultPolicy(), Est: e})
		sentText := 0
		for i, m := range r.Prompt.Messages {
			if m.Role == core.RoleSystem && i < len(r.Prompt.Messages)-1 {
				continue // a cleared copy renders nothing
			}
			for _, b := range m.Blocks {
				sentText += SentBlockTokens(b, e)
			}
		}
		// The stack has no pinned layers, so message 0 holds thread text only. The
		// sizer adds a fixed framing overhead per turn on top of the text.
		total := z.Turns(turns)
		if total < sentText || total > sentText+8*len(turns) {
			t.Fatalf("%s: sizer %d vs rendered text %d", name, total, sentText)
		}
	}
}

// The compactor fork appends its instruction as a user turn. On a turn-scoped
// route the parent's thread ends with the board view (a system message), and a
// system message followed by a user message is a 400: the fork drops the view and
// puts the instruction in the last user message, after the rolling marker.
func TestForkPromptOnATurnScopedThreadEndsInTheInstruction(t *testing.T) {
	e := cxEst()
	s := cxStack(t, "be-1", cxSizes{constT: 3000, shared: 2000, notes: 200})
	th := NewThread()
	th.Append(tsTurn(core.RoleUser, "go"))
	for i := 1; i <= 3; i++ {
		id := fmt.Sprint("s", i)
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text("step"), core.ToolUse("c"+id, "bash", json.RawMessage(`{}`))}})
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{core.ToolResult("c"+id, false, core.Text("ok "+id))}})
		th.Append(core.Turn{Role: core.RoleSystem, Origin: core.OriginSystem, Blocks: []core.Block{core.Text("<live>board " + id + "</live>")}})
	}
	s.Thread = th.Snapshot()
	opts := RenderOpts{Caps: tsCaps(), Policy: DefaultPolicy(), Est: e}
	parent := Render(s, opts)
	if last := parent.Prompt.Messages[len(parent.Prompt.Messages)-1]; last.Role != core.RoleSystem {
		t.Fatalf("setup: the parent's prompt must end with the board view, ends with %s", last.Role)
	}

	fork := ForkPrompt(s, opts, "INSTRUCTION")
	if msg := sysPlacementProblem(fork); msg != "" {
		t.Fatalf("the fork is not a request the API accepts: %s", msg)
	}
	msgs := fork.Messages
	last := msgs[len(msgs)-1]
	if last.Role != core.RoleUser || last.Blocks[len(last.Blocks)-1].Text != "INSTRUCTION" {
		t.Fatalf("the fork must end with the instruction in a user message, got %s / %+v", last.Role, last.Blocks[len(last.Blocks)-1])
	}
	// Everything before the parent's trailing view is untouched, so the provider
	// reads it from the parent's cache; the instruction follows the rolling marker.
	pm := parent.Prompt.Messages
	for i := 0; i < len(msgs)-1; i++ {
		a, _ := json.Marshal(msgs[i])
		b, _ := json.Marshal(pm[i])
		if string(a) != string(b) {
			t.Fatalf("message %d differs from the parent's:\n%s\n%s", i, a, b)
		}
	}
	if len(msgs) != len(pm)-1 {
		t.Fatalf("the fork has %d messages, the parent %d: only the trailing view may go", len(msgs), len(pm))
	}
	for i, b := range last.Blocks[:len(last.Blocks)-1] {
		a, _ := json.Marshal(b)
		c, _ := json.Marshal(pm[len(pm)-2].Blocks[i])
		if string(a) != string(c) {
			t.Fatalf("block %d of the last user message differs from the parent's", i)
		}
	}
	var thread *core.Breakpoint
	for i := range fork.Breakpoints {
		if fork.Breakpoints[i].Label == "thread" {
			thread = &fork.Breakpoints[i]
		}
	}
	if thread == nil || thread.After.Msg != len(msgs)-1 || thread.After.Blk != len(last.Blocks)-2 {
		t.Fatalf("the rolling marker must sit on the last block before the instruction: %+v", thread)
	}
	for _, b := range fork.Breakpoints {
		if !b.After.Sys && b.After.Msg >= 0 && msgs[b.After.Msg].Role == core.RoleSystem {
			t.Fatalf("marker %s on a system message", b.Label)
		}
	}

	// Where the thread ends in a user turn (nothing to drop) the instruction is
	// appended to it, as before.
	s2 := cxStack(t, "be-1", cxSizes{constT: 3000, shared: 2000})
	th2 := NewThread()
	th2.Append(tsTurn(core.RoleUser, "go"))
	s2.Thread = th2.Snapshot()
	f2 := ForkPrompt(s2, RenderOpts{Caps: cxCaps(), Policy: DefaultPolicy(), Est: e}, "INSTRUCTION")
	if len(f2.Messages) != 1 || f2.Messages[0].Blocks[len(f2.Messages[0].Blocks)-1].Text != "INSTRUCTION" {
		t.Fatalf("fork of a one-turn thread: %+v", f2.Messages)
	}
}
