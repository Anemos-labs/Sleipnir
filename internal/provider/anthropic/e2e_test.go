package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/anthropic"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// End-to-end: a real kv.Stack is rendered with the capabilities this adapter
// advertises, the prompt goes through the adapter to the Anthropic-dialect mock,
// and the assertions are on what the explicit cache did per tier. Nothing here
// hand-places a breakpoint or hand-builds a message: if the planner, the renderer
// and the adapter stop agreeing (a marker on a block that cannot carry one, a
// rewritten prefix, a hot block that is not append-only) a number in this file
// moves.
//
// The token arithmetic is exact because the mock tokenizes four bytes to a token
// and kv is given the same ratio, so a layer's estimated size is the size the
// mock bills for it.

const (
	e2eModel = "claude-opus-5-5" // preserved thinking, adaptive thinking, 512-token minimum
	e2eTask  = "The test in pkg1 fails. Find out why and fix it."
)

// filler returns text of about n tokens under the mock tokenizer.
func filler(n int, word string) string { return strings.Repeat(word+" ", n*4/(len(word)+1)) }

func e2eTools(t *testing.T) []core.ToolSpec {
	t.Helper()
	tools, err := kv.SortTools([]core.ToolSpec{
		{Name: "read", Description: "Read a file. " + filler(300, "read"), InputSchema: json.RawMessage(readSchema)},
		{Name: "bash", Description: "Run a command. " + filler(300, "bash"), InputSchema: json.RawMessage(bashSchema)},
		{Name: "edit", Description: "Edit a file. " + filler(300, "edit"), InputSchema: json.RawMessage(readSchema)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func e2eLayer(id string, kind kv.Kind, word string, tokens int) *kv.Layer {
	return kv.NewLayer(id, kind, 1, []kv.Segment{{Key: id, Text: filler(tokens, word), Vol: kv.VolEpoch}})
}

// e2eStack is one agent's prompt state: tools and constitution frozen for the
// session, a shared pin every agent has, a role pin every agent of the role has,
// and the agent's own notes. The sizes clear the planner's thresholds (a role pin
// above the provider minimum, notes above MinLayerForBreakpoint), so all four
// layer markers are worth their slot.
func e2eStack(t *testing.T, agent, role string) kv.Stack {
	t.Helper()
	return kv.Stack{
		Agent:  agent,
		Role:   role,
		Model:  e2eModel,
		Tools:  e2eTools(t),
		Const:  e2eLayer("const", kv.KindConst, "constitution", 3000),
		Shared: e2eLayer("shared", kv.KindShared, "project-fact", 2500),
		RoleL:  e2eLayer("role", kv.KindRole, role+"-convention", 2000),
		Notes:  e2eLayer("notes", kv.KindNotes, agent+"-note", 1800),
	}
}

func boardView(n int) string {
	return fmt.Sprintf("<board n=%d>\n%s</board>", n, filler(300, "task-line"))
}

func toolOutput(round int, tool string) string {
	return fmt.Sprintf("%s ok, round %d\n%s", tool, round, filler(600, "output"))
}

// scripted plays a model working a task: it reasons and calls bash until it has
// seen five tool results, then answers. It decides from the prompt only, so any
// agent, in any order, gets the same behaviour.
func scripted(c *mock.Call) mock.Reply {
	results := 0
	for _, m := range c.Messages {
		if m.Role == "tool" {
			results++
		}
	}
	if results >= 5 {
		return mock.Reply{Reasoning: "every check passes, so the fix is complete", Text: "The failing test passes now."}
	}
	return mock.Reply{
		Reasoning: fmt.Sprintf("step %d: %s", results+1, filler(30, "consider")),
		Text:      "Running the tests.",
		ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("toolu_%02d", results+1), Name: "bash", Args: fmt.Sprintf(`{"command":"go test ./pkg%d"}`, results+1)}},
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// warned collects what Build reported it changed or could not honour. A planner
// and an adapter that agree produce none.
type warned struct {
	mu  sync.Mutex
	got []anthropic.Warning
}

func (w *warned) collect(_ *provider.Request, ws []anthropic.Warning) {
	w.mu.Lock()
	w.got = append(w.got, ws...)
	w.mu.Unlock()
}

func (w *warned) none(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.got) != 0 {
		t.Errorf("the adapter had to adjust what kv planned: %+v", w.got)
	}
}

// routeMode is what an agent resolves for this client: the hot-tail mechanism its
// profile allows, given whether the model enforces preserved thinking.
func routeMode(c *anthropic.Client, preservedThinking bool) kv.HotMode {
	return kv.ResolveHot(kv.HotInline, c.Profile().KVCaps(), preservedThinking)
}

// e2eAgent is the smallest agent loop that goes through the same seams as
// internal/agent: render a stack snapshot with the route's capabilities, send the
// prompt through the adapter, append the reply, answer its tool calls, and deliver
// the per-turn board view the way the resolved hot mode says.
type e2eAgent struct {
	t       *testing.T
	c       *anthropic.Client
	caps    kv.Caps
	policy  kv.Policy
	stack   kv.Stack
	thread  *kv.Thread
	est     core.Estimator
	rolling *core.BlockRef // where the previous request put its rolling marker

	rendered []*kv.Rendered
	resps    []*provider.Response
	hot      []int // tokens of the board view delivered with each user turn
	grew     []int // estimated tokens each step appended to the thread
}

func newE2EAgent(t *testing.T, c *anthropic.Client, s kv.Stack, mode kv.HotMode) *e2eAgent {
	t.Helper()
	caps := c.Profile().KVCaps()
	caps.HotMode = mode
	a := &e2eAgent{
		t: t, c: c, caps: caps, stack: s, thread: kv.NewThread(),
		// The swarm configuration: shared and role markers live an hour, so they
		// outlast the idle gaps an agent has between turns.
		policy: kv.Policy{SharedTTL: time.Hour, MinLayerForBreakpoint: 1500},
		est:    core.NewBytesEstimator().WithRatio(4),
	}
	a.pushUser(core.OriginUser, []core.Block{core.Text(e2eTask)})
	return a
}

// pushUser appends a user turn and delivers the board view with it: as a system
// message that clears after the turn (turn-scoped), as a frozen notice block in the
// turn itself (persisted), or not at all here (inline: Render appends it per request).
func (a *e2eAgent) pushUser(origin core.Origin, blocks []core.Block) {
	a.t.Helper()
	view := boardView(len(a.hot) + 1)
	a.hot = append(a.hot, tokensOf(view))
	if a.caps.HotMode == kv.HotPersist {
		blocks = append(blocks, kv.Notice(view))
	}
	a.thread.Append(core.Turn{Role: core.RoleUser, Origin: origin, Blocks: blocks})
	if a.caps.HotMode == kv.HotTurnScoped {
		a.thread.Append(core.Turn{Role: core.RoleSystem, Origin: core.OriginSystem, Blocks: []core.Block{core.Text(view)}})
	}
}

func (a *e2eAgent) step() (*provider.Response, error) {
	a.t.Helper()
	s := a.stack
	s.Thread = a.thread.Snapshot()
	var hot []core.Block
	if a.caps.HotMode == kv.HotInline {
		hot = []core.Block{core.Text(boardView(len(a.hot)))}
	}
	r := kv.Render(&s, kv.RenderOpts{
		Hot: hot, Caps: a.caps, Policy: a.policy, Est: a.est, PrevRolling: a.rolling,
		Params: core.Params{MaxTokens: 1024, Thinking: "adaptive"},
	})
	resp, err := a.c.Do(context.Background(), &provider.Request{Prompt: r.Prompt}, nil)
	if err != nil {
		return nil, err
	}
	a.rendered, a.resps, a.rolling = append(a.rendered, r), append(a.resps, resp), r.Rolling

	before := len(a.thread.Snapshot().Turns)
	a.thread.Append(resp.Turn)
	if calls := resp.Turn.ToolCalls(); len(calls) > 0 {
		var results []core.Block
		for _, call := range calls {
			results = append(results, core.ToolResult(call.ToolID, false, core.Text(toolOutput(len(a.resps), call.ToolName))))
		}
		a.pushUser(core.OriginTool, results)
	}
	grew := 0
	for _, tr := range a.thread.Snapshot().Turns[before:] {
		grew += kv.TurnTokens(tr, a.est)
	}
	a.grew = append(a.grew, grew)
	return resp, nil
}

func (a *e2eAgent) run(rounds int) {
	a.t.Helper()
	for i := 1; i <= rounds; i++ {
		if _, err := a.step(); err != nil {
			a.t.Fatalf("round %d: %v", i, err)
		}
	}
}

func sectionTokens(r *kv.Rendered, name string) int {
	for _, s := range r.Sections {
		if s.Name == name {
			return s.Tokens
		}
	}
	return -1
}

func breakpointLabels(p *core.Prompt) string {
	var ls []string
	for _, b := range p.Breakpoints {
		ls = append(ls, b.Label)
	}
	return strings.Join(ls, ",")
}

func countMessages(p *core.Prompt, pred func(core.Message) bool) int {
	n := 0
	for _, m := range p.Messages {
		if pred(m) {
			n++
		}
	}
	return n
}

func countThinking(p *core.Prompt) int {
	n := 0
	for _, m := range p.Messages {
		for _, b := range m.Blocks {
			if b.Kind == core.BlockThinking {
				n++
			}
		}
	}
	return n
}

func cached(s mock.AnthropicStat) int { return s.Read + s.Write5m + s.Write1h }

// sameBilling checks the adapter's normalised usage against what the mock billed.
func sameBilling(t *testing.T, i int, u core.Usage, s mock.AnthropicStat) {
	t.Helper()
	if u.CacheReadTokens != s.Read || u.CacheWrite5mTokens != s.Write5m || u.CacheWrite1hTokens != s.Write1h || u.InputTokens != s.Uncached {
		t.Errorf("request %d: adapter reports %+v, the mock billed read %d write5m %d write1h %d uncached %d", i+1, u, s.Read, s.Write5m, s.Write1h, s.Uncached)
	}
}

func hasBeta(s mock.AnthropicStat, beta string) bool {
	for _, b := range s.Betas {
		if b == beta {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------

func TestEndToEndKVStackSixAgentRounds(t *testing.T) {
	var w warned
	c, srv := newMockClient(t, enforced, scripted, anthropic.Config{Model: e2eModel, OnWarnings: w.collect})
	mode := routeMode(c, true)
	if mode != kv.HotTurnScoped {
		t.Fatalf("this route delivers the board view as %v, want turn-scoped", mode)
	}
	a := newE2EAgent(t, c, e2eStack(t, "be-1", "backend"), mode)
	a.run(6)
	w.none(t)

	st := srv.AnthropicStats()
	if len(st) != 6 {
		t.Fatalf("%d requests reached the server", len(st))
	}
	for i := range st {
		sameBilling(t, i, a.resps[i].Usage, st[i])
		if st[i].Status != 200 || !st[i].Streamed {
			t.Errorf("request %d: status %d streamed %v", i+1, st[i].Status, st[i].Streamed)
		}
		if !hasBeta(st[i], anthropic.BetaTurnScopedSystem) {
			t.Errorf("request %d: a turn-scoped system message needs its beta; sent %v", i+1, st[i].Betas)
		}
	}
	if calls := a.resps[5].Turn.ToolCalls(); len(calls) != 0 || a.resps[5].Stop != core.StopEnd {
		t.Fatalf("the script must end on an answer: %+v", a.resps[5])
	}

	// Round 1 is cold. The planner spends its four markers on the layers and the
	// thread; the shared and role markers carry the swarm TTL, so the prefix through
	// the role pin is written as a 1h entry and the agent-specific rest as 5m.
	if got := breakpointLabels(a.rendered[0].Prompt); got != "shared,role,notes,thread" {
		t.Fatalf("planned markers = %s", got)
	}
	first := st[0]
	toolsTok, sysTok := first.WriteTiers.Tools, first.WriteTiers.System
	sharedTok, roleTok, notesTok := sectionTokens(a.rendered[0], "shared"), sectionTokens(a.rendered[0], "role"), sectionTokens(a.rendered[0], "notes")
	if toolsTok == 0 || sysTok == 0 {
		t.Fatalf("round 1 must write the tools and system tiers: %+v", first.WriteTiers)
	}
	if first.Read != 0 {
		t.Errorf("round 1 read %d tokens from a cold cache", first.Read)
	}
	if want := toolsTok + sysTok + sharedTok + roleTok; first.Write1h != want {
		t.Errorf("round 1 wrote %d tokens at 1h, want tools+system+shared+role = %d", first.Write1h, want)
	}
	if want := notesTok + tokensOf(e2eTask); first.Write5m != want {
		t.Errorf("round 1 wrote %d tokens at 5m, want notes+task = %d", first.Write5m, want)
	}
	// The board view follows the last marker, so it is billed once, uncached, while active.
	if first.Uncached != a.hot[0] {
		t.Errorf("round 1 uncached %d, want the active board view (%d)", first.Uncached, a.hot[0])
	}

	for i := 1; i < len(st); i++ {
		prev, cur, round := st[i-1], st[i], i+1
		// Everything the previous request cached is read back, tier by tier: the tools
		// and system tiers whole, the messages tier up to the previous rolling marker.
		if want := cached(prev); cur.Read != want {
			t.Errorf("round %d read %d tokens, want everything round %d cached (%d)", round, cur.Read, round-1, want)
		}
		if cur.ReadTiers.Tools != toolsTok || cur.ReadTiers.System != sysTok {
			t.Errorf("round %d read tiers %+v, want tools %d and system %d whole", round, cur.ReadTiers, toolsTok, sysTok)
		}
		if want := prev.ReadTiers.Messages + prev.WriteTiers.Messages; cur.ReadTiers.Messages != want {
			t.Errorf("round %d read %d message tokens, want %d", round, cur.ReadTiers.Messages, want)
		}
		if cur.WriteTiers.Tools != 0 || cur.WriteTiers.System != 0 || cur.Write1h != 0 {
			t.Errorf("round %d rewrote stable tiers: write tiers %+v, 1h %d", round, cur.WriteTiers, cur.Write1h)
		}
		// Only the last exchange is new, and the board view of the previous round has
		// cleared: it costs nothing and did not move the prefix.
		if cur.Write5m == 0 || cur.Write5m+cur.Uncached > a.grew[i-1] {
			t.Errorf("round %d wrote %d and left %d uncached; the last step appended about %d tokens", round, cur.Write5m, cur.Uncached, a.grew[i-1])
		}
		if cur.Uncached != a.hot[i] {
			t.Errorf("round %d uncached %d, want only the active board view (%d)", round, cur.Uncached, a.hot[i])
		}
		if ratio := a.resps[i].Usage.HitRatio(); ratio < 0.8 {
			t.Errorf("round %d hit ratio %.2f", round, ratio)
		}
		// The prompt is append-only: every earlier board view is still in the array
		// (rendering nothing), and every earlier thinking block is replayed.
		p := a.rendered[i].Prompt
		if n := countMessages(p, func(m core.Message) bool { return m.ClearAt != "" }); n != round {
			t.Errorf("round %d carries %d turn-scoped system messages, want %d", round, n, round)
		}
		if n := countThinking(p); n != i {
			t.Errorf("round %d replays %d thinking blocks, want %d", round, n, i)
		}
	}
	for i, s := range st {
		t.Logf("round %d: read %5d (tools %d system %d messages %d) write5m %4d write1h %5d uncached %3d hit %.2f",
			i+1, s.Read, s.ReadTiers.Tools, s.ReadTiers.System, s.ReadTiers.Messages, s.Write5m, s.Write1h, s.Uncached, a.resps[i].Usage.HitRatio())
	}
}

// The swarm view: the shared and role markers carry a 1h lifetime, so other agents
// and a resumed agent read them long after the 5m thread entries are gone.
func TestEndToEndKVSharedLayersServeOtherAgentsAcrossAnIdleGap(t *testing.T) {
	clock := newClock()
	mcfg := enforced
	mcfg.Now = clock.now
	var w warned
	c, srv := newMockClient(t, mcfg, scripted, anthropic.Config{Model: e2eModel, OnWarnings: w.collect})
	mode := routeMode(c, true)

	a := newE2EAgent(t, c, e2eStack(t, "be-1", "backend"), mode)
	a.run(1)
	st := srv.AnthropicStats()
	toolsTok, sysTok := st[0].WriteTiers.Tools, st[0].WriteTiers.System
	sharedTok, roleTok := sectionTokens(a.rendered[0], "shared"), sectionTokens(a.rendered[0], "role")
	shared1h := st[0].Write1h
	if shared1h != toolsTok+sysTok+sharedTok+roleTok {
		t.Fatalf("precondition: agent A wrote %d at 1h, want tools+system+shared+role = %d", shared1h, toolsTok+sysTok+sharedTok+roleTok)
	}

	// Six idle minutes: agent A's 5m entries (its notes and thread) are gone.
	clock.advance(6 * time.Minute)

	// Agent B: same role, different notes. It reads the whole 1h prefix and writes
	// only what is its own.
	b := newE2EAgent(t, c, e2eStack(t, "be-2", "backend"), mode)
	b.run(1)
	sb := srv.AnthropicStats()[1]
	if sb.Read != shared1h || sb.ReadTiers.Tools != toolsTok || sb.ReadTiers.System != sysTok || sb.ReadTiers.Messages != sharedTok+roleTok {
		t.Errorf("agent B read %d (tiers %+v), want the 1h prefix %d: tools %d system %d shared+role %d", sb.Read, sb.ReadTiers, shared1h, toolsTok, sysTok, sharedTok+roleTok)
	}
	if want := sectionTokens(b.rendered[0], "notes") + tokensOf(e2eTask); sb.Write5m != want || sb.Write1h != 0 {
		t.Errorf("agent B wrote 5m %d 1h %d, want notes+task = %d at 5m", sb.Write5m, sb.Write1h, want)
	}

	// Agent C: another role. The shared pin still serves it; its role pin is new.
	cc := newE2EAgent(t, c, e2eStack(t, "fe-1", "frontend"), mode)
	cc.run(1)
	sc := srv.AnthropicStats()[2]
	if want := toolsTok + sysTok + sharedTok; sc.Read != want || sc.ReadTiers.Messages != sharedTok {
		t.Errorf("agent C read %d (tiers %+v), want tools+system+shared = %d", sc.Read, sc.ReadTiers, want)
	}
	if want := sectionTokens(cc.rendered[0], "role"); sc.Write1h != want {
		t.Errorf("agent C wrote %d at 1h, want its role pin (%d)", sc.Write1h, want)
	}

	// Agent A resumes six more minutes later. Its own entries expired long ago, but
	// the reads by B and C kept the 1h entries alive, so A pays for its notes and
	// thread again and nothing above them.
	clock.advance(6 * time.Minute)
	if _, err := a.step(); err != nil {
		t.Fatal(err)
	}
	sa := srv.AnthropicStats()[3]
	if sa.Read != shared1h || sa.ReadTiers.Tools != toolsTok || sa.ReadTiers.System != sysTok || sa.ReadTiers.Messages != sharedTok+roleTok {
		t.Errorf("resumed agent A read %d (tiers %+v), want the 1h prefix %d", sa.Read, sa.ReadTiers, shared1h)
	}
	if sa.Write1h != 0 || sa.Write5m == 0 || sa.Write5m < sectionTokens(a.rendered[1], "notes") {
		t.Errorf("resumed agent A wrote 5m %d 1h %d; its notes and thread are re-written at 5m", sa.Write5m, sa.Write1h)
	}

	// Left alone for over an hour the swarm entries expire too.
	clock.advance(2 * time.Hour)
	late := newE2EAgent(t, c, e2eStack(t, "be-3", "backend"), mode)
	late.run(1)
	if sl := srv.AnthropicStats()[4]; sl.Read != 0 || sl.Write1h != shared1h {
		t.Errorf("after two idle hours: read %d write1h %d, want a cold start that re-writes %d", sl.Read, sl.Write1h, shared1h)
	}
	w.none(t)
}

// Endpoints without turn-scoped system messages: kv resolves the persisted mode,
// the board view becomes a notice block in the user turn and stays, and the run is
// as append-only as the native one (the price is the growth, not a cache miss).
func TestEndToEndKVPersistedBoardViewWithoutTurnScopedSystem(t *testing.T) {
	prof := anthropic.DefaultProfile("gateway", "", e2eModel)
	prof.TurnScopedSystem = false
	var w warned
	c, srv := newMockClient(t, enforced, scripted, anthropic.Config{Profile: &prof, OnWarnings: w.collect})
	mode := routeMode(c, true)
	if mode != kv.HotPersist {
		t.Fatalf("resolved %v: a preserved-thinking route without turn-scoped messages must persist the view", mode)
	}
	a := newE2EAgent(t, c, e2eStack(t, "be-1", "backend"), mode)
	a.run(6)
	w.none(t)

	st := srv.AnthropicStats()
	for i := range st {
		sameBilling(t, i, a.resps[i].Usage, st[i])
		if hasBeta(st[i], anthropic.BetaTurnScopedSystem) {
			t.Errorf("request %d asked for the turn-scoped system beta on an endpoint without it: %v", i+1, st[i].Betas)
		}
		if n := countMessages(a.rendered[i].Prompt, func(m core.Message) bool { return m.Role == core.RoleSystem }); n != 0 {
			t.Errorf("request %d has %d system messages", i+1, n)
		}
		// The rolling marker rides on the last block, the newest notice, so nothing
		// is ever left uncached.
		if st[i].Uncached != 0 {
			t.Errorf("request %d left %d tokens uncached", i+1, st[i].Uncached)
		}
	}
	for i := 1; i < len(st); i++ {
		prev, cur := st[i-1], st[i]
		if cur.Read != cached(prev) || cur.ReadTiers.Tools != prev.ReadTiers.Tools+prev.WriteTiers.Tools || cur.ReadTiers.System != prev.ReadTiers.System+prev.WriteTiers.System {
			t.Errorf("round %d read %d (tiers %+v), want all %d that round %d cached", i+1, cur.Read, cur.ReadTiers, cached(prev), i)
		}
		if cur.Write5m < a.hot[i] || cur.Write5m > a.grew[i-1] {
			t.Errorf("round %d wrote %d; the new notice alone is %d tokens and the step appended about %d", i+1, cur.Write5m, a.hot[i], a.grew[i-1])
		}
	}
}

// What ResolveHot exists to prevent: an inline board view on a route that enforces
// preserved thinking rewrites the message an earlier thinking block was produced
// against. The adapter must surface that as the recoverable ErrThinkingBinding.
func TestEndToEndKVInlineBoardViewOnAnEnforcingRouteFails(t *testing.T) {
	var w warned
	c, _ := newMockClient(t, enforced, scripted, anthropic.Config{Model: e2eModel, OnWarnings: w.collect})
	a := newE2EAgent(t, c, e2eStack(t, "be-1", "backend"), kv.HotInline)
	a.run(1) // nothing to bind yet
	w.none(t)

	_, err := a.step()
	pe := wantKind(t, err, provider.ErrThinkingBinding, false)
	if pe.Status != 400 {
		t.Errorf("status = %d", pe.Status)
	}
	// The adapter saw it coming: the request that is about to fail is the first
	// one that carries thinking under an inline tail.
	w.mu.Lock()
	if len(w.got) != 1 || w.got[0].Code != "ephemeral_inline" {
		t.Errorf("warnings = %+v", w.got)
	}
	w.mu.Unlock()

	// The agent's recovery: strip thinking from the thread durably and retry. It
	// works once; the next thinking block is bound to an inline tail again.
	if !a.thread.Rewrite(func(tr core.Turn) (core.Turn, bool) { return kv.StripThinkingTurn(tr) }) {
		t.Fatal("the thread had thinking to strip")
	}
	if _, err := a.step(); err != nil {
		t.Fatalf("after stripping thinking: %v", err)
	}
	_, err = a.step()
	wantKind(t, err, provider.ErrThinkingBinding, false)
}

// A family without mid-conversation system messages (the mock, like the API,
// answers such a message with a 400). Two ways it must not matter: the profile
// derived from the model turns the feature off so kv picks another delivery, and
// a profile that wrongly claims the feature still cannot get one sent.
func TestEndToEndKVOnAModelWithoutMidConversationSystem(t *testing.T) {
	const model = "claude-sonnet-5"
	stack := func() kv.Stack {
		s := e2eStack(t, "be-1", "backend")
		s.Model = model
		return s
	}

	t.Run("the model's profile leaves the board view inline", func(t *testing.T) {
		var w warned
		c, srv := newMockClient(t, mock.AnthropicConfig{}, scripted, anthropic.Config{Model: model, OnWarnings: w.collect})
		if mode := routeMode(c, false); mode != kv.HotInline {
			t.Fatalf("mode = %v", mode)
		}
		a := newE2EAgent(t, c, stack(), kv.HotInline)
		a.run(3)
		w.none(t)
		st := srv.AnthropicStats()
		for i, s := range st {
			if hasBeta(s, anthropic.BetaTurnScopedSystem) || s.Status != 200 {
				t.Errorf("request %d: status %d betas %v", i+1, s.Status, s.Betas)
			}
		}
		// The inline board view is the only thing after the rolling marker, and it
		// is rebuilt every request: the cache still reads everything before it.
		for i := 1; i < len(st); i++ {
			if st[i].Read != cached(st[i-1]) || st[i].Uncached != a.hot[i] {
				t.Errorf("request %d: read %d (want %d) uncached %d (want %d)", i+1, st[i].Read, cached(st[i-1]), st[i].Uncached, a.hot[i])
			}
		}
	})

	t.Run("an optimistic profile still cannot send one", func(t *testing.T) {
		prof := anthropic.DefaultProfile("gateway", "", "") // unknown model: claims the feature
		if !prof.TurnScopedSystem {
			t.Fatal("precondition: the profile claims turn-scoped system messages")
		}
		var w warned
		c, srv := newMockClient(t, mock.AnthropicConfig{}, scripted, anthropic.Config{Profile: &prof, OnWarnings: w.collect})
		a := newE2EAgent(t, c, stack(), kv.HotTurnScoped)
		a.run(3)
		st := srv.AnthropicStats()
		for i, s := range st {
			if hasBeta(s, anthropic.BetaTurnScopedSystem) || s.Status != 200 {
				t.Errorf("request %d: status %d betas %v", i+1, s.Status, s.Betas)
			}
		}
		// Every request reports the fold, and only that.
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.got) == 0 {
			t.Fatal("folding the board view must be reported")
		}
		for _, x := range w.got {
			if x.Code != "system_folded" || !strings.Contains(x.Message, model) {
				t.Errorf("warning %v", x)
			}
		}
		// Folded text is ordinary, append-only content: what the previous request
		// cached is read back whole.
		for i := 1; i < len(st); i++ {
			if st[i].Read != cached(st[i-1]) {
				t.Errorf("request %d read %d, want %d", i+1, st[i].Read, cached(st[i-1]))
			}
		}
	})
}
