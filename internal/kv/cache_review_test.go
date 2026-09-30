package kv

// Adversarial review tests (lens: prompt-cache economics and correctness).
//
// Every test here demonstrates a specific defect found in review; see
// docs/reviews/cache-economics.md for the numbered findings (R1..R30).
//
// Convention: by default a review test PASSES while the defect is present (so
// the tree stays green) and FAILS once the defect is fixed, telling the fixer
// to invert or delete it. Run with REVIEW_STRICT=1 to get the opposite: tests
// FAIL while the defect is present.
//
//	go test -race -count=1 -run CacheEcon -v ./internal/kv
//	REVIEW_STRICT=1 go test -count=1 -run CacheEcon ./internal/kv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
)

func cxBug(t *testing.T, present bool, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if os.Getenv("REVIEW_STRICT") != "" {
		if present {
			t.Fatalf("DEFECT PRESENT: %s", msg)
		}
		return
	}
	if !present {
		t.Fatalf("defect no longer reproduces (%s): invert or delete this review test", msg)
	}
	t.Logf("DEFECT CONFIRMED: %s", msg)
}

func cxEst() core.Estimator { return core.NewBytesEstimator().WithRatio(4) }

// cxText returns text that the ratio-4 estimator counts as exactly n tokens.
func cxText(seed string, n int) string {
	s := strings.Repeat("abcd", n)
	if seed != "" && len(seed) <= len(s) {
		s = seed + s[len(seed):]
	}
	return s
}

func cxCaps() Caps {
	return Caps{Dialect: "anthropic", MaxBreakpoints: 4, LookbackBlocks: 20, MinPrefixTokens: 512, ReplayThinking: true}
}

// ---------------------------------------------------------------------------
// A small, honest model of Anthropic explicit prompt caching:
//   * entries exist only at positions where an earlier request placed a
//     breakpoint (hash of the cumulative prefix ending at that block);
//   * each breakpoint looks back at most 20 positions for such an entry, where a
//     run of consecutive tool_use blocks (or tool_result blocks) is one position;
//   * a request reads up to the highest hit, writes from there to its last
//     breakpoint, and pays plain input for the rest.
// ---------------------------------------------------------------------------

type cxSim struct{ entries map[string]bool }

func newRvSim() *cxSim { return &cxSim{entries: map[string]bool{}} }

func (s *cxSim) clone() *cxSim {
	c := newRvSim()
	for k := range s.entries {
		c.entries[k] = true
	}
	return c
}

type cxUse struct{ read, write, plain int }

func (u cxUse) ite(r, w float64) float64 {
	return r*float64(u.read) + w*float64(u.write) + float64(u.plain)
}

type cxBlk struct {
	ref  core.BlockRef
	cum  int
	hash string
	pos  int
	eph  bool
}

func cxBlockBytes(b core.Block) string {
	var sb strings.Builder
	sb.WriteString(string(b.Kind))
	sb.WriteString("|")
	sb.WriteString(b.Text)
	sb.WriteString("|")
	sb.WriteString(b.ToolID + "|" + b.ToolName + "|" + string(b.Input))
	for _, c := range b.Result {
		sb.WriteString("{" + cxBlockBytes(c) + "}")
	}
	return sb.String()
}

func cxWalk(p *core.Prompt, est core.Estimator) (blks []cxBlk, total int) {
	h := sha256.New()
	pos := 0
	var lastKind core.BlockKind
	step := func(raw string, tokens int, ref core.BlockRef, kind core.BlockKind, eph bool) {
		h.Write([]byte(raw))
		h.Write([]byte{0})
		total += tokens
		sum := h.Sum(nil)
		collapse := (kind == core.BlockToolUse || kind == core.BlockToolResult) && kind == lastKind
		if !collapse {
			pos++
		}
		lastKind = kind
		blks = append(blks, cxBlk{ref: ref, cum: total, hash: hex.EncodeToString(sum[:8]), pos: pos, eph: eph})
	}
	p.WalkBlocks(func(ref core.BlockRef, tool *core.ToolSpec, b *core.Block) {
		switch {
		case tool != nil:
			step("tool:"+tool.Name+string(tool.InputSchema)+tool.Description,
				est.Tokens(tool.Name)+est.Tokens(tool.Description)+est.Tokens(string(tool.InputSchema))+8, ref, "tool", false)
		case b != nil && ref.Sys:
			step("sys:"+b.Text, BlockTokens(*b, est), ref, core.BlockText, false)
		case b != nil:
			step(fmt.Sprintf("m%d:%s:%s", ref.Msg, p.Messages[ref.Msg].Role, cxBlockBytes(*b)), BlockTokens(*b, est), ref, b.Kind, b.Ephemeral)
		}
	})
	return
}

// request simulates one request against the cache and updates it.
func (s *cxSim) request(p *core.Prompt, est core.Estimator, minPrefix int) cxUse {
	blks, total := cxWalk(p, est)
	idx := map[core.BlockRef]int{}
	for i, b := range blks {
		idx[b.ref] = i
	}
	type bp struct{ k int }
	var bps []bp
	for _, b := range p.Breakpoints {
		k, ok := idx[b.After]
		if !ok || blks[k].cum < minPrefix {
			continue
		}
		bps = append(bps, bp{k})
	}
	hit := 0
	for _, b := range bps {
		lo := blks[b.k].pos - 19
		for j := b.k; j >= 0 && blks[j].pos >= lo; j-- {
			if s.entries[blks[j].hash] {
				if blks[j].cum > hit {
					hit = blks[j].cum
				}
				break
			}
		}
	}
	last := 0
	for _, b := range bps {
		s.entries[blks[b.k].hash] = true
		if blks[b.k].cum > last {
			last = blks[b.k].cum
		}
	}
	u := cxUse{read: hit}
	if last > hit {
		u.write = last - hit
	}
	u.plain = total - hit - u.write
	return u
}

// ---------------------------------------------------------------------------

func cxTools(t *testing.T) []core.ToolSpec {
	t.Helper()
	tools, err := SortTools([]core.ToolSpec{
		{Name: "bash", Description: "run a command", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)},
		{Name: "read", Description: "read a file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type cxSizes struct{ constT, shared, role, notes, spine int }

func cxStack(t *testing.T, agent string, sz cxSizes) *Stack {
	t.Helper()
	s := &Stack{Agent: agent, Role: "backend", Model: "m", Tools: cxTools(t)}
	s.Const = NewLayer("const", KindConst, 1, []Segment{{Text: cxText("constitution ", sz.constT)}})
	if sz.shared > 0 {
		s.Shared = NewLayer("shared", KindShared, 1, []Segment{{Key: "project", Text: cxText("shared ", sz.shared), Vol: VolEpoch}})
	}
	if sz.role > 0 {
		s.RoleL = NewLayer("role:backend", KindRole, 1, []Segment{{Key: "backend", Text: cxText("role ", sz.role), Vol: VolEpoch}})
	}
	if sz.notes > 0 {
		s.Notes = NewLayer("notes:"+agent, KindNotes, 1, []Segment{{Key: "facts", Text: cxText("notes-"+agent+" ", sz.notes), Vol: VolSlow}})
	}
	if sz.spine > 0 {
		s.Spine = NewLayer("spine:"+agent, KindSpine, 1, []Segment{{Text: cxText("t1-t9 · ", sz.spine), Vol: VolFast}})
	}
	return s
}

func cxExchange(th *Thread, id string, resultTokens int) {
	th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{
		core.Text("step " + id), core.ToolUse("call_"+id, "bash", json.RawMessage(`{"command":"go test ./"}`)),
	}})
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{
		core.ToolResult("call_"+id, false, core.Text(cxText("out-"+id+" ", resultTokens))),
	}})
}

func cxRender(s *Stack, th *Thread, pol Policy) *Rendered {
	s.Thread = th.Snapshot()
	return Render(s, RenderOpts{Caps: cxCaps(), Policy: pol, Est: cxEst()})
}

func cxLabels(r *Rendered) []string {
	var out []string
	for _, b := range r.Prompt.Breakpoints {
		out = append(out, b.Label)
	}
	return out
}

// ---------------------------------------------------------------------------
// R-BP1: breakpoint policy vs. the swarm's "one trie, many leaves" claim.
// ---------------------------------------------------------------------------

// The built-in role pins are ~150-250 tokens (internal/swarm/roles.go) and a
// young project's shared pin can easily be under 1500 tokens. MinLayerForBreakpoint
// (1500) then drops the shared and role markers, and G0 (constitution + tools)
// never has a marker of its own. On an explicit-cache provider the second agent
// of a swarm therefore cannot read the first agent's shared prefix at all, and
// a compaction commit rewrites the whole deep prefix.
func TestCacheEcon_SmallLayersLoseCrossAgentSharing(t *testing.T) {
	e := cxEst()
	sz := cxSizes{constT: 3000, shared: 900, role: 200, notes: 300}
	mk := func(agent string) (*Stack, *Thread) {
		s := cxStack(t, agent, sz)
		th := NewThread()
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("task for " + agent)}})
		cxExchange(th, agent+"1", 200)
		return s, th
	}
	sa, ta := mk("be-1")
	sb, tb := mk("be-2")

	sim := newRvSim()
	ra := cxRender(sa, ta, DefaultPolicy())
	ua := sim.request(ra.Prompt, e, 512)
	rb := cxRender(sb, tb, DefaultPolicy())
	ub := sim.request(rb.Prompt, e, 512)
	sharedTokens := 3000 + 900 + 200 // const + shared + role, identical bytes for both agents

	t.Logf("agent A breakpoints=%v first request %+v", cxLabels(ra), ua)
	t.Logf("agent B breakpoints=%v first request %+v (identical G0+G1+G2 = ~%d tokens)", cxLabels(rb), ub, sharedTokens)
	cxBug(t, ub.read == 0 && strings.Join(cxLabels(rb), ",") == "thread",
		"second agent reads %d of %d shared prefix tokens; only marker is %v", ub.read, sharedTokens, cxLabels(rb))

	// Control: with the size floor removed the same layout shares the prefix.
	sa2, ta2 := mk("be-1")
	sb2, tb2 := mk("be-2")
	sim2 := newRvSim()
	pol := DefaultPolicy()
	pol.MinLayerForBreakpoint = 1
	sim2.request(cxRender(sa2, ta2, pol).Prompt, e, 512)
	rb2 := cxRender(sb2, tb2, pol)
	ub2 := sim2.request(rb2.Prompt, e, 512)
	t.Logf("control (no floor): breakpoints=%v second agent %+v", cxLabels(rb2), ub2)
	if ub2.read < 3000+900 {
		t.Fatalf("control failed: with markers the shared prefix should be read, got %+v", ub2)
	}

	// After a commit the same thing happens to the agent itself: nothing survives.
	sim3 := newRvSim()
	s3, th3 := mk("be-3")
	sim3.request(cxRender(s3, th3, DefaultPolicy()).Prompt, e, 512)
	s3.Spine = NewLayer("spine:be-3", KindSpine, 2, []Segment{{Text: "t1-t2 did things", Vol: VolFast}}) // a commit changes the spine
	after := sim3.request(cxRender(s3, th3, DefaultPolicy()).Prompt, e, 512)
	t.Logf("same agent after a spine append: %+v", after)
	cxBug(t, after.read == 0, "a spine change rewrites the entire prompt (read=%d) because no marker sits before it", after.read)
}

// ---------------------------------------------------------------------------
// R-BP2: the 20-position lookback is carried in Caps but never planned for.
// ---------------------------------------------------------------------------

func TestCacheEcon_LookbackWindowIsNotPlannedFor(t *testing.T) {
	e := cxEst()
	run := func(mailBlocks int) (cxUse, int, []string) {
		s := cxStack(t, "mgr", cxSizes{constT: 3000, shared: 2500, role: 2200, notes: 1800})
		th := NewThread()
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("goal")}})
		for i := 0; i < 3; i++ {
			cxExchange(th, fmt.Sprint(i), 3000)
		}
		sim := newRvSim()
		r1 := cxRender(s, th, DefaultPolicy())
		sim.request(r1.Prompt, e, 512)
		_, prev := cxWalk(r1.Prompt, e) // persistent + hot; no hot here
		// The manager wakes from wait() with a burst of mail: agent.takeInbox turns
		// every queued message into its own text block (agent.go:388).
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text("waiting")}})
		var mail []core.Block
		for i := 0; i < mailBlocks; i++ {
			mail = append(mail, core.Text(fmt.Sprintf("[mail %d] worker finished", i)))
		}
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginMail, Blocks: mail})
		r2 := cxRender(s, th, DefaultPolicy())
		return sim.request(r2.Prompt, e, 512), prev, cxLabels(r2)
	}
	few, prevFew, _ := run(5)
	many, prevMany, labels := run(25)
	t.Logf("5 mail blocks : %+v (previous prompt %d tokens)", few, prevFew)
	t.Logf("25 mail blocks: %+v (previous prompt %d tokens) breakpoints=%v", many, prevMany, labels)
	if few.read < prevFew-20 {
		t.Fatalf("control: with few blocks the rolling entry should be found, %+v vs %d", few, prevFew)
	}
	cxBug(t, many.read < prevMany-8000 && len(labels) == 4,
		"one turn with 25 blocks pushes the previous rolling entry out of the 20-position window: read %d of %d tokens (the whole thread is rewritten at the write premium), no intermediate marker planned (Caps.LookbackBlocks is never used)", many.read, prevMany)
}

// ---------------------------------------------------------------------------
// R-PL1: the planner's warm-commit penalty vs. what the cache actually charges.
// ---------------------------------------------------------------------------

func TestCacheEcon_PlannerPenaltyOmitsTailSpineAndNotes(t *testing.T) {
	e := cxEst()
	const r, w = 0.1, 1.25
	s := cxStack(t, "be-1", cxSizes{constT: 3000, shared: 2500, role: 2200, notes: 1800, spine: 3000})
	th := NewThread()
	// A thread that starts mid-history (an earlier commit already moved the
	// task text into the notes, so this commit will not touch the notes).
	for i := 0; i < 14; i++ {
		cxExchange(th, fmt.Sprintf("a%02d", i), 3000)
	}
	sim := newRvSim()
	sim.request(cxRender(s, th, DefaultPolicy()).Prompt, e, 512)

	// The compactor (a background job) snapshots here.
	snap := th.Snapshot()
	s.Thread = snap
	res, err := Apply(s, &Patch{KeepFrom: snap.Turns[len(snap.Turns)-4].ID}, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.NotesChanged {
		t.Fatal("test setup: notes should not change in this commit")
	}
	A, R := res.SpineAdded, res.RetainedTokens
	T := res.RemovedTokens + res.RetainedTokens // what agent.propose stores as snapTotal

	// While it thinks, the agent keeps working: 3 more steps, each a request.
	for i := 0; i < 3; i++ {
		cxExchange(th, fmt.Sprintf("b%02d", i), 2500)
		if i < 2 {
			sim.request(cxRender(s, th, DefaultPolicy()).Prompt, e, 512)
		}
	}
	// The last exchange was appended after the last request; the patch lands at
	// the next turn boundary.
	noCommit := sim.clone()
	uNo := noCommit.request(cxRender(s, th, DefaultPolicy()).Prompt, e, 512)

	s2 := *s
	th2 := NewThread()
	th2.turns, th2.next = append([]core.Turn(nil), th.turns...), th.next
	if err := th2.Commit(snap.Epoch, res.Replacement, len(snap.Turns)); err != nil {
		t.Fatal(err)
	}
	s2.Spine = res.Spine
	commit := sim.clone()
	uCommit := commit.request(cxRender(&s2, th2, DefaultPolicy()).Prompt, e, 512)

	actualPenalty := uCommit.ite(r, w) - uNo.ite(r, w)
	docPenalty := w*float64(A+R) - r*float64(T)
	perTurn := r * float64(res.RemovedTokens-A) // shrink measured on the same basis
	t.Logf("A=%d R=%d T(snapTotal)=%d removed=%d oldSpine=%d", A, R, T, res.RemovedTokens, s.Spine.Tokens(e))
	t.Logf("next request without commit: %+v  with commit: %+v", uNo, uCommit)
	t.Logf("planner penalty w(A+R)-rT = %.0f ITE; actual extra cost of the first post-commit request = %.0f ITE", docPenalty, actualPenalty)
	planner := DefaultPlanner()
	d := planner.ShouldCommit(State{ThreadTokens: T, Warm: true, W: cost.Weights{Read: r, Write5m: w}, Write: w, Remaining: 25}, Outcome{SpineAdded: A, RetainedTokens: R})
	t.Logf("ShouldCommit says yes=%v net=%.0f ITE over 25 turns; recomputed with actual penalty: %.0f ITE", d.Yes, d.NetITE, 25*perTurn-actualPenalty)

	cxBug(t, actualPenalty > 2*docPenalty && actualPenalty-docPenalty > 5000,
		"actual first-request penalty %.0f ITE is %.1fx the planner's %.0f: the formula omits the old spine (whole spine block is rewritten), the turns that arrived during compaction, and any notes rewrite", actualPenalty, actualPenalty/docPenalty, docPenalty)
}

// The same omission flips the decision in the marginal regime: a modest shrink
// that ShouldCommit accepts loses money over its own 25-turn horizon.
func TestCacheEcon_PlannerAcceptsMoneyLosingCommit(t *testing.T) {
	e := cxEst()
	const r, w = 0.1, 1.25
	const horizon = 25.0
	s := cxStack(t, "be-1", cxSizes{constT: 3000, shared: 2500, role: 2200, notes: 1800, spine: 6000})
	th := NewThread()
	for i := 0; i < 12; i++ {
		cxExchange(th, fmt.Sprintf("a%02d", i), 1100) // below MaskMinTokens: masking plays no part
	}
	sim := newRvSim()
	sim.request(cxRender(s, th, DefaultPolicy()).Prompt, e, 512)
	snap := th.Snapshot()
	s.Thread = snap
	res, err := Apply(s, &Patch{KeepFrom: snap.Turns[len(snap.Turns)-14].ID}, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.NotesChanged || res.MaskedResults > 0 {
		t.Fatalf("setup: notes changed=%v masked=%d", res.NotesChanged, res.MaskedResults)
	}
	A, R := res.SpineAdded, res.RetainedTokens
	T := res.RemovedTokens + res.RetainedTokens
	for i := 0; i < 4; i++ { // arrives while the compactor works; the last one after the last request
		cxExchange(th, fmt.Sprintf("b%02d", i), 1100)
		if i < 3 {
			sim.request(cxRender(s, th, DefaultPolicy()).Prompt, e, 512)
		}
	}
	no := sim.clone().request(cxRender(s, th, DefaultPolicy()).Prompt, e, 512)
	s2 := *s
	th2 := NewThread()
	th2.turns, th2.next = append([]core.Turn(nil), th.turns...), th.next
	if err := th2.Commit(snap.Epoch, res.Replacement, len(snap.Turns)); err != nil {
		t.Fatal(err)
	}
	s2.Spine = res.Spine
	yes := sim.clone().request(cxRender(&s2, th2, DefaultPolicy()).Prompt, e, 512)
	actualPenalty := yes.ite(r, w) - no.ite(r, w)
	perTurn := r * float64(res.RemovedTokens-A)
	d := DefaultPlanner().ShouldCommit(State{ThreadTokens: T, Warm: true, W: cost.Weights{Read: r, Write5m: w}, Write: w, Remaining: horizon}, Outcome{SpineAdded: A, RetainedTokens: R})
	actualNet := horizon*perTurn - actualPenalty
	t.Logf("A=%d R=%d T=%d shrink=%d; planner: yes=%v net=%.0f ITE; simulated cache: penalty %.0f => net %.0f ITE over %.0f turns", A, R, T, res.RemovedTokens-A, d.Yes, d.NetITE, actualPenalty, actualNet, horizon)
	cxBug(t, d.Yes && actualNet < 0, "ShouldCommit approves (net %+.0f ITE) a commit that costs %+.0f ITE over its own horizon", d.NetITE, actualNet)
}

// The T the planner uses is RemovedTokens+RetainedTokens where RetainedTokens is
// measured AFTER masking and thinking-stripping, so the saving from masking (the
// design's flagship cheap compaction) never enters the commit decision.
func TestCacheEcon_SnapTotalExcludesMaskingSavings(t *testing.T) {
	e := cxEst()
	s := cxStack(t, "be-1", cxSizes{constT: 1000})
	th := NewThread()
	for i := 0; i < 12; i++ {
		cxExchange(th, fmt.Sprintf("m%02d", i), 8000)
	}
	s.Thread = th.Snapshot()
	live := s.Thread.Tokens(e)
	res, err := Apply(s, &Patch{KeepFrom: s.Thread.Turns[2].ID}, e, DefaultApplyPolicy()) // fold almost nothing; masking does the work
	if err != nil {
		t.Fatal(err)
	}
	snapTotal := res.RemovedTokens + res.RetainedTokens
	t.Logf("live thread %d tokens; planner's T (removed+retained-after-masking) = %d; masked results=%d", live, snapTotal, res.MaskedResults)
	cxBug(t, res.MaskedResults > 0 && snapTotal < live*8/10,
		"planner T=%d understates the real thread (%d): %d tokens of masking savings are invisible to ShouldCommit", snapTotal, live, live-snapTotal)
}

// ---------------------------------------------------------------------------
// R-PT1: Thread.Commit carries the tail verbatim, thinking included.
// ---------------------------------------------------------------------------

func TestCacheEcon_CommitTailKeepsPreRebaseThinking(t *testing.T) {
	e := cxEst()
	s := cxStack(t, "be-1", cxSizes{constT: 1000, shared: 800})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("task")}})
	think := func(sig string) core.Block {
		return core.Block{Kind: core.BlockThinking, Wire: json.RawMessage(`{"type":"thinking","signature":"` + sig + `"}`), WireFormat: "anthropic"}
	}
	add := func(id string, sig string) {
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{think(sig), core.Text("s" + id), core.ToolUse("c"+id, "bash", json.RawMessage(`{}`))}})
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{core.ToolResult("c"+id, false, core.Text(cxText("o"+id, 300)))}})
	}
	for i := 0; i < 6; i++ {
		add(fmt.Sprint(i), fmt.Sprintf("sig-old-%d", i))
	}
	snap := th.Snapshot()
	s.Thread = snap
	res, err := Apply(s, &Patch{KeepFrom: snap.Turns[7].ID}, e, DefaultApplyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	// The agent keeps running while the compactor thinks: a new assistant turn
	// is produced against the OLD prefix, so its signature binds to that prefix.
	add("tail", "sig-produced-before-the-commit")
	if err := th.Commit(snap.Epoch, res.Replacement, len(snap.Turns)); err != nil {
		t.Fatal(err)
	}
	s.Spine, s.Notes = res.Spine, res.Notes
	s.Thread = th.Snapshot()
	r := Render(s, RenderOpts{Caps: cxCaps(), Policy: DefaultPolicy(), Est: e})
	var stale []string
	for _, m := range r.Prompt.Messages {
		for _, b := range m.Blocks {
			if b.Kind == core.BlockThinking {
				stale = append(stale, string(b.Wire))
			}
		}
	}
	t.Logf("thinking blocks replayed after the commit: %v", stale)
	cxBug(t, len(stale) > 0, "%d thinking block(s) produced before the rebase are replayed after it; on Claude models that enforce the prefix binding this is an HTTP 400 (CACHE-DESIGN §5 says the strip happens 'at commit'; it only covers the snapshot's retained turns)", len(stale))
}

// ---------------------------------------------------------------------------
// R-UI1: "user instructions survive verbatim" (CACHE-DESIGN §4.1).
// ---------------------------------------------------------------------------

func TestCacheEcon_UserInstructionGuarantees(t *testing.T) {
	e := cxEst()

	t.Run("truncated at 600 tokens without a recall pointer", func(t *testing.T) {
		s := cxStack(t, "be-1", cxSizes{constT: 500})
		th := NewThread()
		spec := "SPEC-START " + cxText("requirement ", 5000) + " SPEC-END-MARKER"
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text(spec)}})
		for i := 0; i < 4; i++ {
			cxExchange(th, fmt.Sprint(i), 100)
		}
		s.Thread = th.Snapshot()
		res, err := Apply(s, &Patch{KeepFrom: s.Thread.Turns[5].ID}, e, DefaultApplyPolicy())
		if err != nil {
			t.Fatal(err)
		}
		seg, _ := res.Notes.Segment("instructions")
		t.Logf("instructions segment: %d bytes of a %d byte task; has recall pointer=%v", len(seg.Text), len(spec), strings.Contains(seg.Text, "recall"))
		cxBug(t, !strings.Contains(seg.Text, "SPEC-END-MARKER") && !strings.Contains(seg.Text, "recall"),
			"a %d-token task is cut to 600 tokens; the model is not told the rest exists or how to recall it", 5000)
	})

	t.Run("steering delivered with tool results is not preserved", func(t *testing.T) {
		s := cxStack(t, "be-1", cxSizes{constT: 500})
		th := NewThread()
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("build it")}})
		cxExchange(th, "0", 100)
		// agent.Run: blocks = results + inbox text, pushed as Origin: OriginTool (agent.go:355).
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text("s"), core.ToolUse("c1", "bash", json.RawMessage(`{}`))}})
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{
			core.ToolResult("c1", false, core.Text("ok")),
			core.Text("[from user] never touch the billing package"),
		}})
		for i := 2; i < 6; i++ {
			cxExchange(th, fmt.Sprint(i), 100)
		}
		s.Thread = th.Snapshot()
		res, err := Apply(s, &Patch{KeepFrom: s.Thread.Turns[len(s.Thread.Turns)-3].ID}, e, DefaultApplyPolicy())
		if err != nil {
			t.Fatal(err)
		}
		seg, _ := res.Notes.Segment("instructions")
		cxBug(t, !strings.Contains(seg.Text, "billing") && !strings.Contains(res.Spine.Text(), "billing"),
			"a human steering message (Agent.Send) rides in an OriginTool turn and vanishes when folded: instructions=%q spine=%q", seg.Text, res.Spine.Text())
	})

	t.Run("a later compactor note op can overwrite the instructions segment", func(t *testing.T) {
		s := cxStack(t, "be-1", cxSizes{constT: 500})
		th := NewThread()
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("never delete data")}})
		for i := 0; i < 8; i++ {
			cxExchange(th, fmt.Sprint(i), 100)
		}
		s.Thread = th.Snapshot()
		res1, err := Apply(s, &Patch{KeepFrom: s.Thread.Turns[6].ID}, e, DefaultApplyPolicy())
		if err != nil {
			t.Fatal(err)
		}
		if seg, _ := res1.Notes.Segment("instructions"); !strings.Contains(seg.Text, "never delete data") {
			t.Fatal("first commit must preserve the instruction")
		}
		th2 := NewThread()
		th2.turns, th2.next = res1.Replacement, 100
		for i := 20; i < 26; i++ {
			cxExchange(th2, fmt.Sprint(i), 100)
		}
		s2 := *s
		s2.Spine, s2.Notes, s2.Thread = res1.Spine, res1.Notes, th2.Snapshot()
		res2, err := Apply(&s2, &Patch{KeepFrom: s2.Thread.Turns[len(s2.Thread.Turns)-3].ID,
			Notes: []NoteOp{{Op: "set", Key: "instructions", Text: "- be careful"}}}, e, DefaultApplyPolicy())
		if err != nil {
			t.Fatal(err)
		}
		seg, _ := res2.Notes.Segment("instructions")
		cxBug(t, !strings.Contains(seg.Text, "never delete data"),
			"a model-written note op replaced the harness-owned instructions segment: %q", seg.Text)
	})
}

// ---------------------------------------------------------------------------
// R-FK1: the compactor fork changes tool_choice (and caps max_tokens).
// ---------------------------------------------------------------------------

func TestCacheEcon_ForkChangesToolChoice(t *testing.T) {
	s := cxStack(t, "be-1", cxSizes{constT: 3000, shared: 2500, role: 2200, notes: 1800})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("goal")}})
	for i := 0; i < 6; i++ {
		cxExchange(th, fmt.Sprint(i), 1000)
	}
	s.Thread = th.Snapshot()
	opts := RenderOpts{Caps: cxCaps(), Policy: DefaultPolicy(), Est: cxEst(), Params: core.Params{MaxTokens: 8192, ToolChoice: "auto", Thinking: "adaptive"}}
	parent := Render(s, opts)
	fork := ForkPrompt(s, opts, Instruction(s, cxEst(), DefaultApplyPolicy()))
	t.Logf("parent params %+v; fork params %+v", parent.Prompt.Params, fork.Params)
	// prompt-caching.md "Invalidation hierarchy": tool_choice changes keep tools+system
	// cache but invalidate the messages cache. Sleipnir puts G1..G5 in messages, so the
	// fork re-writes the whole conversation at 1.25x instead of reading it.
	cxBug(t, parent.Prompt.Params.ToolChoice != fork.Params.ToolChoice,
		"fork sets tool_choice=%q vs parent %q: on Anthropic this forfeits the ENTIRE messages-tier cache (kv/fork.go:29; enshrined by kv/compact_test.go:419)", fork.Params.ToolChoice, parent.Prompt.Params.ToolChoice)
	if fork.Params.MaxTokens != 3000 {
		t.Fatalf("expected the fork to cap max_tokens at 3000, got %d", fork.Params.MaxTokens)
	}
}

// ---------------------------------------------------------------------------
// R-GD1: what Guard cannot see.
// ---------------------------------------------------------------------------

func TestCacheEcon_GuardBlindSpots(t *testing.T) {
	e := cxEst()
	s := cxStack(t, "be-1", cxSizes{constT: 800, shared: 600})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("go")}})
	cxExchange(th, "1", 100)
	s.Thread = th.Snapshot()
	g := &Guard{}
	base := RenderOpts{Caps: cxCaps(), Policy: DefaultPolicy(), Est: e, Params: core.Params{MaxTokens: 100, Effort: "high", ToolChoice: "auto"},
		Hot: []core.Block{core.Text("<live v1>")}}
	g.Observe(Render(s, base), 0, e)

	changed := base
	changed.Params = core.Params{MaxTokens: 100, Effort: "low", ToolChoice: "none", Thinking: "off"} // all of these are cache/binding inputs on Anthropic
	changed.Hot = []core.Block{core.Text("<live v2 completely different>")}                          // a per-request rewrite of the last user message
	r := Render(s, changed)
	c := g.Observe(r, 0, e)
	t.Logf("effort/tool_choice/thinking/hot all changed between requests: %+v", c)

	s2 := *s
	s2.Model = "some-other-model"
	c2 := g.Observe(Render(&s2, changed), 0, e)
	t.Logf("model changed: %+v", c2)

	// Message boundaries and roles are not hashed either: the same blocks split
	// differently (or attributed to another role) look identical to the guard.
	a, b := core.Text(cxText("a", 300)), core.Text(cxText("b", 300))
	g2 := &Guard{}
	g2.Observe(&Rendered{Prompt: &core.Prompt{Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{a, b}}}}}, 0, e)
	c3 := g2.Observe(&Rendered{Prompt: &core.Prompt{Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{a}}, {Role: core.RoleAssistant, Blocks: []core.Block{b}}}}}, 0, e)
	t.Logf("same blocks, different message boundaries and roles: %+v", c3)

	cxBug(t, !c.Drift && !c2.Drift && !c3.Drift,
		"Guard reports no drift for changed effort/thinking/tool_choice, a changed hot tail (invisible to thinking bindings), a different model, or re-split messages with different roles; it hashes only Block JSON, skips Ephemeral blocks and ignores Params/Model/roles")
}

// ---------------------------------------------------------------------------
// R-CO1: structural soundness of Apply/Commit/Render (checked-and-found-sound
// unless a case below fails).
// ---------------------------------------------------------------------------

func TestCacheEcon_ApplyCommitRenderStructuralSoundness(t *testing.T) {
	e := cxEst()
	rng := rand.New(rand.NewSource(20260930))
	var bad []string
	twoUserAtStart := 0
	for iter := 0; iter < 400; iter++ {
		s := cxStack(t, "be-1", cxSizes{constT: 300})
		th := NewThread()
		turns := 3 + rng.Intn(14)
		// Random shape: units of exchanges, standalone user text (mail/steer), standalone assistant text.
		for i := 0; i < turns; i++ {
			switch rng.Intn(5) {
			case 0:
				th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginMail, Blocks: []core.Block{core.Text("mail")}})
			case 1:
				th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text("thinking aloud")}})
			default:
				cxExchange(th, fmt.Sprintf("%d_%d", iter, i), 20+rng.Intn(3000))
			}
		}
		// Sometimes the thread ends in an unanswered tool call (request in flight).
		if rng.Intn(4) == 0 {
			th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.ToolUse("pending", "bash", json.RawMessage(`{}`))}})
		}
		snap := th.Snapshot()
		s.Thread = snap
		pol := DefaultApplyPolicy()
		pol.AutoMaskAfterUnits = 1 + rng.Intn(4)
		pol.MaskMinTokens = 100
		kf := snap.Turns[rng.Intn(len(snap.Turns))].ID + core.TurnID(rng.Intn(3)) // may land mid-unit or past the end
		res, err := Apply(s, &Patch{KeepFrom: kf}, e, pol)
		if err != nil {
			continue
		}
		// Turns that arrive while the compactor works: complete exchanges or (in flight) nothing.
		tail := 0
		if snap.Turns[len(snap.Turns)-1].Role == core.RoleAssistant && len(snap.Turns[len(snap.Turns)-1].ToolCalls()) > 0 {
			th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginTool, Blocks: []core.Block{core.ToolResult("pending", false, core.Text("late result"))}})
			tail++
		}
		for k := rng.Intn(3); k > 0; k-- {
			cxExchange(th, fmt.Sprintf("tail%d_%d", iter, k), 50)
			tail += 2
		}
		if err := th.Commit(snap.Epoch, res.Replacement, len(snap.Turns)); err != nil {
			bad = append(bad, fmt.Sprintf("iter %d: commit: %v", iter, err))
			continue
		}
		after := th.Snapshot()
		if err := Validate(after.Turns); err != nil {
			bad = append(bad, fmt.Sprintf("iter %d: Validate after commit: %v (keep_from t%d, %d turns)", iter, err, kf, len(snap.Turns)))
			continue
		}
		s.Spine, s.Notes, s.Thread = res.Spine, res.Notes, after
		r := Render(s, RenderOpts{Caps: cxCaps(), Policy: DefaultPolicy(), Est: e})
		for i, m := range r.Prompt.Messages {
			if i == 0 && m.Role != core.RoleUser {
				bad = append(bad, fmt.Sprintf("iter %d: first message is %s", iter, m.Role))
			}
			if i > 0 && m.Role == r.Prompt.Messages[i-1].Role {
				if i == 1 {
					twoUserAtStart++ // known: see TestCacheEcon_RenderLeavesTwoUserMessagesAtStart
					continue
				}
				bad = append(bad, fmt.Sprintf("iter %d: messages %d,%d both %s (kv.Render left same-role neighbours)", iter, i-1, i, m.Role))
				break
			}
			if len(m.Blocks) == 0 {
				bad = append(bad, fmt.Sprintf("iter %d: empty message %d", iter, i))
			}
		}
	}
	t.Logf("400 random threads/patches/tails: %d rendered with two user messages at positions 0,1", twoUserAtStart)
	if len(bad) > 0 {
		max := len(bad)
		if max > 8 {
			max = 8
		}
		t.Fatalf("%d structural problems, first: %s", len(bad), strings.Join(bad[:max], "; "))
	}
}

// The defensive merge in Render is guarded by n > 1, so a thread that opens with
// two user turns (a retry after a failed first request, or a retained region
// that starts with two standalone user turns) renders as two adjacent user
// messages. Anthropic and OpenAI accept that; strict chat templates (Mistral
// style) and the Guard (which hashes blocks, not messages) do not care or reject.
func TestCacheEcon_RenderLeavesTwoUserMessagesAtStart(t *testing.T) {
	s := cxStack(t, "be-1", cxSizes{constT: 300, shared: 300})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("build it")}})
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("build it (retry)")}})
	r := cxRender(s, th, DefaultPolicy())
	var roles []string
	for _, m := range r.Prompt.Messages {
		roles = append(roles, string(m.Role))
	}
	cxBug(t, len(roles) >= 2 && roles[0] == "user" && roles[1] == "user", "messages roles = %v", roles)
}

// ---------------------------------------------------------------------------
// R-GR1: nothing bounds the spine or the instructions segment, and every commit
// rewrites the whole spine block (one text block) at the write premium.
// ---------------------------------------------------------------------------

func TestCacheEcon_SpineAndInstructionsGrowWithoutBound(t *testing.T) {
	e := cxEst()
	s := cxStack(t, "mgr", cxSizes{constT: 3000, shared: 2500, role: 2200})
	th := NewThread()
	next := 0
	var spineTok, instrTok, over int
	cumRewrite := 0.0
	for cycle := 1; cycle <= 150; cycle++ {
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text(fmt.Sprintf("also handle case %d: %s", cycle, cxText("detail ", 30)))}})
		for i := 0; i < 6; i++ {
			next++
			cxExchange(th, fmt.Sprintf("g%d", next), 300)
		}
		snap := th.Snapshot()
		s.Thread = snap
		p := &Patch{KeepFrom: snap.Turns[len(snap.Turns)-4].ID,
			Spine: []SpineEntry{{From: snap.Turns[0].ID, To: snap.Turns[len(snap.Turns)-5].ID, Line: "worked on the task and verified the results with the test suite; nothing left open"}}}
		res, err := Apply(s, p, e, DefaultApplyPolicy())
		if err != nil {
			t.Fatal(err)
		}
		if err := th.Commit(snap.Epoch, res.Replacement, len(snap.Turns)); err != nil {
			t.Fatal(err)
		}
		s.Spine, s.Notes = res.Spine, res.Notes
		if res.NotesOverBudget {
			over++
		}
		spineTok = s.Spine.Tokens(e)
		cumRewrite += 1.25 * float64(spineTok) // the whole spine block is rewritten at every commit
		if seg, ok := s.Notes.Segment("instructions"); ok {
			instrTok = e.Tokens(seg.Text)
		}
	}
	t.Logf("after 150 commits: spine %d tokens, instructions %d tokens, NotesOverBudget=true on %d commits (nothing consumes it); cumulative spine rewrite cost %.0fk ITE", spineTok, instrTok, over, cumRewrite/1000)
	cxBug(t, spineTok > 3000 && instrTok > 3*1500 && over > 100,
		"spine=%d tokens (+~24 per commit) and instructions=%d tokens (3.6x its 1500-token section cap, +~36 per user turn) keep growing linearly; both are read on every request and the spine is re-written on every commit", spineTok, instrTok)
}

// ---------------------------------------------------------------------------
// R-BP3: do the planned breakpoints obey Anthropic's rules, and does the hot
// tail ever land before one? (checked-and-found-sound, except the thinking case)
// ---------------------------------------------------------------------------

func TestCacheEcon_BreakpointRulesHoldOnRandomStacks(t *testing.T) {
	e := cxEst()
	rng := rand.New(rand.NewSource(7))
	pos := func(a core.BlockRef) int { return a.Msg*1000 + a.Blk }
	for iter := 0; iter < 500; iter++ {
		sz := cxSizes{constT: 200 + rng.Intn(4000)}
		if rng.Intn(3) > 0 {
			sz.shared = 200 + rng.Intn(4000)
		}
		if rng.Intn(3) > 0 {
			sz.role = 100 + rng.Intn(3000)
		}
		if rng.Intn(3) > 0 {
			sz.notes = 100 + rng.Intn(3000)
		}
		if rng.Intn(3) > 0 {
			sz.spine = 100 + rng.Intn(2000)
		}
		s := cxStack(t, "a", sz)
		th := NewThread()
		th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("go")}})
		for i := rng.Intn(6); i > 0; i-- {
			cxExchange(th, fmt.Sprint(iter, "_", i), 50+rng.Intn(2000))
		}
		s.Thread = th.Snapshot()
		pol := DefaultPolicy()
		pol.SharedTTL = []time.Duration{0, time.Hour}[rng.Intn(2)]
		caps := cxCaps()
		caps.MaxBreakpoints = 1 + rng.Intn(4)
		var hot []core.Block
		if rng.Intn(2) == 0 {
			hot = []core.Block{core.Text("<live>tail</live>")}
		}
		r := Render(s, RenderOpts{Caps: caps, Policy: pol, Est: e, Hot: hot})
		bps := r.Prompt.Breakpoints
		if len(bps) > caps.MaxBreakpoints {
			t.Fatalf("iter %d: %d breakpoints > max %d", iter, len(bps), caps.MaxBreakpoints)
		}
		lastPersistent := core.BlockRef{Msg: -1}
		for mi, m := range r.Prompt.Messages {
			for bi, b := range m.Blocks {
				if !b.Ephemeral {
					lastPersistent = core.BlockRef{Msg: mi, Blk: bi}
				}
			}
		}
		for i, b := range bps {
			if i > 0 {
				if pos(b.After) <= pos(bps[i-1].After) {
					t.Fatalf("iter %d: breakpoints not strictly ascending: %+v", iter, bps)
				}
				if b.TTL > bps[i-1].TTL && bps[i-1].TTL != 0 {
					t.Fatalf("iter %d: TTL increases along the prompt (a longer TTL must come first): %+v", iter, bps)
				}
				if bps[i-1].TTL == 0 && b.TTL > 0 {
					t.Fatalf("iter %d: a 5m entry precedes a 1h entry: %+v", iter, bps)
				}
			}
			blk := r.Prompt.Messages[b.After.Msg].Blocks[b.After.Blk]
			if blk.Ephemeral {
				t.Fatalf("iter %d: breakpoint on an ephemeral block", iter)
			}
			if blk.Kind == core.BlockText && blk.Text == "" {
				t.Fatalf("iter %d: breakpoint on an empty text block", iter)
			}
		}
		if len(bps) > 0 && bps[len(bps)-1].Label == "thread" && pos(bps[len(bps)-1].After) != pos(lastPersistent) {
			t.Fatalf("iter %d: rolling breakpoint %+v is not the last persistent block %+v (hot tail would be cached)", iter, bps[len(bps)-1].After, lastPersistent)
		}
		if len(hot) > 0 {
			last := r.Prompt.Messages[len(r.Prompt.Messages)-1]
			if !last.Blocks[len(last.Blocks)-1].Ephemeral {
				t.Fatalf("iter %d: hot block is not last", iter)
			}
		}
	}

}

// The one case the planner does not guard: a trailing thinking block.
func TestCacheEcon_RollingBreakpointCanLandOnAThinkingBlock(t *testing.T) {
	e := cxEst()
	s := cxStack(t, "a", cxSizes{constT: 3000, shared: 2000})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("go")}})
	th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{
		core.Text("partial"), {Kind: core.BlockThinking, Wire: json.RawMessage(`{"type":"thinking","signature":"s"}`), WireFormat: "anthropic"},
	}})
	s.Thread = th.Snapshot()
	r := Render(s, RenderOpts{Caps: cxCaps(), Policy: DefaultPolicy(), Est: e})
	last := r.Prompt.Breakpoints[len(r.Prompt.Breakpoints)-1].After
	blk := r.Prompt.Messages[last.Msg].Blocks[last.Blk]
	cxBug(t, blk.Kind == core.BlockThinking, "the rolling breakpoint is placed on a thinking block (kind=%s); thinking blocks cannot carry cache_control", blk.Kind)
}

// ---------------------------------------------------------------------------
// R-BY1: byte stability. The calibrating estimator may only move breakpoints,
// never bytes; a layer near MinLayerForBreakpoint makes its marker flicker.
// ---------------------------------------------------------------------------

func TestCacheEcon_EstimatorMovesBreakpointsButNeverBytes(t *testing.T) {
	s := cxStack(t, "a", cxSizes{constT: 3000, shared: 2500, role: 2200, notes: 1500})
	th := NewThread()
	th.Append(core.Turn{Role: core.RoleUser, Origin: core.OriginUser, Blocks: []core.Block{core.Text("go")}})
	cxExchange(th, "1", 400)
	s.Thread = th.Snapshot()
	marshal := func(p *core.Prompt) string {
		q := *p
		q.Breakpoints = nil
		b, _ := json.Marshal(q)
		return string(b)
	}
	var bytes0 string
	labels := map[string]bool{}
	for _, ratio := range []float64{2.0, 3.6, 3.9, 4.0, 4.1, 5.0, 8.0} {
		est := core.NewBytesEstimator().WithRatio(ratio)
		r := Render(s, RenderOpts{Caps: cxCaps(), Policy: DefaultPolicy(), Est: est})
		b := marshal(r.Prompt)
		if bytes0 == "" {
			bytes0 = b
		} else if b != bytes0 {
			t.Fatalf("estimator ratio %.1f changed prompt bytes", ratio)
		}
		labels[strings.Join(cxLabels(r), ",")] = true
	}
	var seen []string
	for l := range labels {
		seen = append(seen, l)
	}
	t.Logf("same state, estimator ratio 2.0..8.0: prompt bytes identical; breakpoint sets seen: %v", seen)
	cxBug(t, len(labels) > 1, "a notes layer of ~1500 tokens gains/loses its marker as the EMA ratio moves between requests (%d distinct breakpoint sets); harmless to bytes, but the notes entry is not refreshed on the requests where it is skipped", len(labels))
}

// ---------------------------------------------------------------------------
// R-FK2: the compactor's reply is parsed from Turn.PlainText(), which
// concatenates thinking text ahead of the answer; ParsePatch takes the FIRST
// balanced {...}. A reasoning model that drafts a JSON shape while thinking makes
// every model patch fail (silent fallback to the mechanical patch).
// ---------------------------------------------------------------------------

func TestCacheEcon_CompactorReplyIncludesThinkingText(t *testing.T) {
	answer := `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"explored"}],"mask":[],"notes":[],"promote":[]}`
	turn := core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{
		{Kind: core.BlockThinking, Text: `I should return an object like {"keep_from": ...} and think about which units to fold. Options: {a} or {b}.`},
		core.Text(answer),
	}}
	if _, err := ParsePatch(answer); err != nil {
		t.Fatalf("control: the answer alone parses: %v", err)
	}
	_, err := ParsePatch(turn.PlainText())
	t.Logf("ParsePatch(turn.PlainText()) error: %v", err)
	cxBug(t, err != nil, "agent/compact.go:203 feeds resp.Turn.PlainText() (thinking included) to ParsePatch: %v", err)
}
