package agent_test

// Regression tests for the prompt-cache economics review (docs/reviews/
// cache-economics.md, findings R1-R20). They began as adversarial repros that
// passed while a defect was present; they now assert the fixed behaviour and
// must pass as they are. ColdStartNoLongerForksAModelCall was already a
// regression test when the review was written.
//
// Instrument: cxProv is a fake provider that speaks the "anthropic" dialect and
// enforces preserved thinking like the API does: a thinking block's signature is
// the hash of everything on the wire before it (tools, system, every earlier
// message, hot tail included), and a replayed block whose prefix changed is HTTP
// 400 (ErrThinkingBinding).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/tools"
)

// ---------------------------------------------------------------------------
// A fake provider that speaks "anthropic" and enforces preserved thinking the
// way the API does: a thinking block's signature is bound to everything the API
// saw before it (tools, system, every earlier message *as sent*, hot tail
// included). A replayed block whose prefix changed is HTTP 400 (ErrThinkingBinding).
// ---------------------------------------------------------------------------

type cxProv struct {
	mu     sync.Mutex
	prof   provider.Profile
	reqs   []*core.Prompt
	modes  []string // Request.BindingMode of every request
	sigErr *provider.Error
	handle func(p *cxProv, req *provider.Request) (*provider.Response, error)
	mainN  int
	prevIn int  // total input tokens of the previous main request (for perfect-cache usage)
	plain  bool // produce turns without thinking blocks (providers that do not replay thinking)
	// rejects counts thinking-binding rejections; failAlways rejects every request.
	rejects    int
	failAlways bool
}

func (p *cxProv) Profile() provider.Profile { return p.prof }

func (p *cxProv) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req.Prompt)
	p.modes = append(p.modes, req.BindingMode)
	fail := p.failAlways
	p.mu.Unlock()
	verr := cxVerify(req.Prompt)
	if verr == nil && fail {
		verr = &provider.Error{Kind: provider.ErrThinkingBinding, Status: 400, Message: "rejected"}
	}
	if verr != nil {
		p.mu.Lock()
		p.sigErr = verr
		p.rejects++
		p.mu.Unlock()
		return nil, verr
	}
	resp, err := p.handle(p, req)
	if err != nil {
		return nil, err
	}
	if on != nil {
		on(provider.Event{Kind: provider.EvStart})
	}
	return resp, nil
}

func (p *cxProv) requests() []*core.Prompt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*core.Prompt(nil), p.reqs...)
}

func cxBlockWire(b core.Block) string {
	var sb strings.Builder
	sb.WriteString(string(b.Kind) + "|" + b.Text + "|" + b.ToolID + "|" + b.ToolName + "|" + string(b.Input))
	for _, c := range b.Result {
		sb.WriteString("{" + cxBlockWire(c) + "}")
	}
	return sb.String()
}

// cxSig hashes what the API has seen before block blk of message msg.
func cxSig(p *core.Prompt, msg, blk int) string {
	h := sha256.New()
	for _, t := range p.Tools {
		h.Write([]byte("tool|" + t.Name + "|" + t.Description + "|" + string(t.InputSchema)))
	}
	for _, s := range p.System {
		h.Write([]byte("sys|" + s.Text))
	}
	for i := 0; i < msg && i < len(p.Messages); i++ {
		h.Write([]byte("msg|" + string(p.Messages[i].Role)))
		for _, b := range p.Messages[i].Blocks {
			if b.Kind == core.BlockThinking {
				continue // earlier thinking is chained separately in the real API
			}
			h.Write([]byte(cxBlockWire(b))) // hot (ephemeral) blocks ARE on the wire
		}
	}
	if msg < len(p.Messages) {
		for j := 0; j < blk && j < len(p.Messages[msg].Blocks); j++ {
			if b := p.Messages[msg].Blocks[j]; b.Kind != core.BlockThinking {
				h.Write([]byte(cxBlockWire(b)))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func cxVerify(p *core.Prompt) *provider.Error {
	for mi, m := range p.Messages {
		if m.Role != core.RoleAssistant {
			continue
		}
		for bi, b := range m.Blocks {
			if b.Kind != core.BlockThinking || len(b.Wire) == 0 {
				continue
			}
			var w struct {
				Signature string `json:"signature"`
			}
			_ = json.Unmarshal(b.Wire, &w)
			if want := cxSig(p, mi, bi); w.Signature != want {
				return &provider.Error{Kind: provider.ErrThinkingBinding, Status: 400,
					Message: fmt.Sprintf("messages.%d.content.%d: Invalid `signature` in `thinking` block. The block is bound to a different conversation.", mi, bi)}
			}
		}
	}
	return nil
}

func cxLastUserText(p *core.Prompt) string {
	m := p.Messages[len(p.Messages)-1]
	var sb strings.Builder
	for _, b := range m.Blocks {
		if b.Kind == core.BlockText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func cxAssistants(p *core.Prompt) int {
	n := 0
	for _, m := range p.Messages {
		if m.Role == core.RoleAssistant {
			n++
		}
	}
	return n
}

// perfectUsage reports a perfect prefix cache: everything the previous main
// request sent is read, the rest is uncached input.
func (p *cxProv) perfectUsage(pr *core.Prompt) core.Usage {
	total := kv.PromptBytes(pr) / 4
	p.mu.Lock()
	read := p.prevIn
	p.prevIn = total
	p.mu.Unlock()
	if read > total {
		read = total
	}
	return core.Usage{InputTokens: total - read, CacheReadTokens: read, OutputTokens: 50}
}

func cxThinkingTurn(pr *core.Prompt, text string, calls ...core.Block) core.Turn {
	return cxTurn(false, pr, text, calls...)
}

func cxTurn(plain bool, pr *core.Prompt, text string, calls ...core.Block) core.Turn {
	if plain {
		return core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: append([]core.Block{core.Text(text)}, calls...)}
	}
	blocks := []core.Block{
		{Kind: core.BlockThinking, Wire: json.RawMessage(`{"type":"thinking","signature":"` + cxSig(pr, len(pr.Messages), 0) + `"}`), WireFormat: "anthropic"},
		core.Text(text),
	}
	blocks = append(blocks, calls...)
	return core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: blocks}
}

// cxWorkModel: call the `work` tool until `steps` assistant turns exist, then finish.
// Compactor forks (<compactor-task>) are answered by `compactor`.
func cxWorkModel(steps int, compactor func(p *cxProv, pr *core.Prompt) string) func(p *cxProv, req *provider.Request) (*provider.Response, error) {
	return func(p *cxProv, req *provider.Request) (*provider.Response, error) {
		pr := req.Prompt
		if strings.Contains(cxLastUserText(pr), "<compactor-task>") {
			text := "{}"
			if compactor != nil {
				text = compactor(p, pr)
			}
			return &provider.Response{ID: "c", Model: "m", Turn: core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Blocks: []core.Block{core.Text(text)}},
				Usage: core.Usage{InputTokens: 10, CacheReadTokens: kv.PromptBytes(pr) / 4, OutputTokens: 100}, Stop: core.StopEnd}, nil
		}
		p.mu.Lock()
		p.mainN++
		p.mu.Unlock()
		p.mu.Lock()
		n := p.mainN - 1 // main requests served so far: independent of what compaction folded away
		p.mu.Unlock()
		if n < steps {
			call := core.ToolUse(fmt.Sprintf("call_%d", n), "work", json.RawMessage(fmt.Sprintf(`{"n":%d}`, n)))
			return &provider.Response{ID: "r", Model: "m", Turn: cxTurn(p.plain, pr, fmt.Sprintf("step %d", n), call), Usage: p.perfectUsage(pr), Stop: core.StopToolUse}, nil
		}
		return &provider.Response{ID: "r", Model: "m", Turn: cxTurn(p.plain, pr, "all done"), Usage: p.perfectUsage(pr), Stop: core.StopEnd}, nil
	}
}

type cxTool struct {
	run func(in json.RawMessage) *tools.Result
}

func (cxTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: "work", Description: "do work", InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}
}
func (t cxTool) Run(_ context.Context, c *tools.Call) (*tools.Result, error) {
	return t.run(c.Input), nil
}

type cxOpts struct {
	id       string
	role     string
	prov     provider.Provider
	hot      agent.HotSource
	planner  kv.Planner
	noCompct bool
	now      func() time.Time
	shards   int
	toolOut  func(in json.RawMessage) *tools.Result
	roleText string
	steps    int
	emitter  events.Emitter // overrides the default MemLog (hooks)
	model    string         // default claude-opus-5-5 (enforces preserved thinking)
	gate     agent.Gate
	hotMode  kv.HotMode
}

func cxAgent(t *testing.T, o cxOpts) (*agent.Agent, *events.MemLog) {
	t.Helper()
	agent.RetryBase = time.Millisecond
	reg := tools.NewRegistry()
	out := o.toolOut
	if out == nil {
		out = func(json.RawMessage) *tools.Result {
			return &tools.Result{Text: strings.Repeat("build output line with some detail\n", 30)}
		}
	}
	reg.Register(cxTool{run: out})
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	if o.model == "" {
		o.model = "claude-opus-5-5"
	}
	m, ok := cost.Defaults().Lookup(o.model)
	if !ok {
		t.Fatalf("unknown model %s", o.model)
	}
	if o.id == "" {
		o.id = "be-1"
	}
	if o.role == "" {
		o.role = "backend"
	}
	log := events.NewMemLog()
	var emitter events.Emitter = log
	if o.emitter != nil {
		emitter = o.emitter
	}
	cfg := agent.Config{
		ID: o.id, Role: o.role, Model: m, Provider: o.prov, Tools: reg, ToolSpecs: specs,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("You are Sleipnir, a careful coding agent. ", 100)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go service. ", 100), Vol: kv.VolEpoch}}),
		Params: core.Params{MaxTokens: 512, Thinking: "adaptive"},
		Events: emitter, Planner: o.planner, NoCompaction: o.noCompct, SessionID: "review",
		Hot: o.hot, AffinityShards: o.shards, MaxSteps: o.steps, Gate: o.gate, HotMode: o.hotMode,
	}
	if o.roleText != "" {
		cfg.RoleL = kv.NewLayer("role:"+o.role, kv.KindRole, 1, []kv.Segment{{Key: o.role, Text: o.roleText, Vol: kv.VolEpoch}})
	}
	if o.now != nil {
		cfg.Now = o.now
	}
	a, err := agent.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a, log
}

func cxAnthropicProfile() provider.Profile {
	return provider.Profile{Name: "fake-anthropic", Dialect: "anthropic", Cache: cost.AnthropicCacheModel(512), ReplayThinking: true, StreamUsage: true}
}

// cxKeyedProfile is an endpoint that routes by an explicit cache key.
func cxKeyedProfile() provider.Profile {
	p := cxAnthropicProfile()
	p.Cache.KeyRouting = true
	return p
}

// cxAppendOnly checks that request cur extends request prev without touching a
// byte the provider already saw: every earlier message is identical and the
// last one (the one the model answered) is a prefix of its counterpart.
func cxAppendOnly(prev, cur *core.Prompt) error {
	if len(cur.Messages) < len(prev.Messages) {
		return fmt.Errorf("prompt shrank from %d to %d messages", len(prev.Messages), len(cur.Messages))
	}
	strip := func(m core.Message) []core.Block {
		var out []core.Block
		for _, b := range m.Blocks {
			if !b.Ephemeral {
				out = append(out, b)
			}
		}
		return out
	}
	for i, m := range prev.Messages {
		a, b := strip(m), strip(cur.Messages[i])
		if m.Role != cur.Messages[i].Role || m.ClearAt != cur.Messages[i].ClearAt {
			return fmt.Errorf("message %d changed role/clear_at", i)
		}
		if i < len(prev.Messages)-1 && len(a) != len(b) {
			return fmt.Errorf("message %d changed size %d -> %d", i, len(a), len(b))
		}
		if len(b) < len(a) {
			return fmt.Errorf("message %d lost blocks", i)
		}
		for k := range a {
			ja, _ := json.Marshal(a[k])
			jb, _ := json.Marshal(b[k])
			if string(ja) != string(jb) {
				return fmt.Errorf("message %d block %d changed:\n%s\n%s", i, k, ja, jb)
			}
		}
	}
	return nil
}

func cxCount(log *events.MemLog, typ, contains string) int {
	n := 0
	for _, e := range log.OfType(typ) {
		if contains == "" || strings.Contains(string(e.Data), contains) {
			n++
		}
	}
	return n
}

func cxThinkingIn(p *core.Prompt) int {
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

func cxBoard() agent.HotSource {
	var mu sync.Mutex
	n := 0
	return func(string) []core.Block {
		mu.Lock()
		n++
		v := n
		mu.Unlock()
		return []core.Block{core.Text(fmt.Sprintf(`<live board="v%d">tasks: %d open (a teammate moved something)</live>`, v, v))}
	}
}

// ---------------------------------------------------------------------------
// R1: the hot tail no longer breaks preserved thinking. On a model that enforces
// it, and a provider without turn-scoped system messages, the board is persisted
// as a frozen notice in the user turn, only when it changed; history stays
// append-only, so every replayed signature keeps matching.
// ---------------------------------------------------------------------------

func TestCacheEcon_HotTailKeepsPreservedThinkingValid(t *testing.T) {
	// Control: append-only, no hot block. Six tool rounds with replayed thinking work.
	prov := &cxProv{prof: cxAnthropicProfile()}
	prov.handle = cxWorkModel(6, nil)
	a, _ := cxAgent(t, cxOpts{prov: prov, noCompct: true})
	if res, err := a.Run(context.Background(), "do the work"); err != nil || res.Steps != 7 {
		t.Fatalf("control (append-only, no hot) must pass the binding check: steps=%v err=%v", res, err)
	}

	// Sleipnir's design: a board that changes on every call.
	prov2 := &cxProv{prof: cxAnthropicProfile()}
	prov2.handle = cxWorkModel(6, nil)
	b, log := cxAgent(t, cxOpts{prov: prov2, hot: cxBoard(), noCompct: true})
	res, err := b.Run(context.Background(), "do the work")
	if err != nil || res.Steps != 7 {
		t.Fatalf("with a hot tail on a preserved-thinking model the run must complete: steps=%v err=%v (rejections %d)", res, err, prov2.rejects)
	}
	reqs := prov2.requests()
	if got := cxThinkingIn(reqs[len(reqs)-1]); got != 6 {
		t.Fatalf("all 6 thinking blocks must still be replayed, got %d", got)
	}
	for i, r := range reqs {
		for _, m := range r.Messages {
			for _, blk := range m.Blocks {
				if blk.Ephemeral {
					t.Fatalf("request %d carries an ephemeral (per-request) block: the tail must be persisted", i)
				}
			}
		}
		if i > 0 {
			if err := cxAppendOnly(reqs[i-1], r); err != nil {
				t.Fatalf("request %d is not an append-only extension of request %d: %v", i, i-1, err)
			}
		}
	}
	notices := 0
	for _, m := range reqs[len(reqs)-1].Messages {
		for _, blk := range m.Blocks {
			if kv.IsNotice(blk) {
				notices++
			}
		}
	}
	// One with the task, then at most one per HotMinRequests (3) requests.
	if notices < 2 || notices > 4 {
		t.Fatalf("expected 2-4 persisted notices over 7 requests (changed board, throttled), got %d", notices)
	}
	if n := cxCount(log, events.TypeModelResponse, `"hot_mode":"persist"`); n != 7 {
		t.Fatalf("model.response must record the hot mode, got %d", n)
	}
}

func TestCacheEcon_TurnScopedHotIsPersistedAsClearAtSystemMessages(t *testing.T) {
	prof := cxAnthropicProfile()
	prof.TurnScopedSystem = true
	prov := &cxProv{prof: prof}
	prov.handle = cxWorkModel(5, nil)
	a, _ := cxAgent(t, cxOpts{prov: prov, hot: cxBoard(), noCompct: true})
	res, err := a.Run(context.Background(), "do the work")
	if err != nil || res.Steps != 6 {
		t.Fatalf("turn-scoped hot must keep thinking valid: steps=%v err=%v", res, err)
	}
	reqs := prov.requests()
	last := reqs[len(reqs)-1]
	sys := 0
	for i, m := range last.Messages {
		if m.Role == core.RoleSystem {
			sys++
			if m.ClearAt != kv.ClearAtNextUser {
				t.Fatalf("message %d: system without clear_at", i)
			}
			if i > 0 && last.Messages[i-1].Role != core.RoleUser {
				t.Fatalf("message %d: a turn-scoped message must follow a user turn", i)
			}
		}
		for _, blk := range m.Blocks {
			if blk.Ephemeral || kv.IsNotice(blk) {
				t.Fatalf("message %d: turn-scoped mode neither inlines nor notices", i)
			}
		}
	}
	if sys != 6 {
		t.Fatalf("one system message after each of the 6 user turns, got %d", sys)
	}
	for i := 1; i < len(reqs); i++ {
		if err := cxAppendOnly(reqs[i-1], reqs[i]); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	for _, bp := range last.Breakpoints {
		if bp.After.Sys {
			continue
		}
		if last.Messages[bp.After.Msg].Role == core.RoleSystem {
			t.Fatalf("marker %s on a turn-scoped message", bp.Label)
		}
	}
	// Compaction folds them like any turn: only the newest survives a commit.
	if cxThinkingIn(last) != 5 {
		t.Fatalf("thinking must be replayed: %d", cxThinkingIn(last))
	}
}

func TestCacheEcon_InlineHotRemainsTheDefaultWhereNothingIsBound(t *testing.T) {
	prov := &cxProv{prof: cxAnthropicProfile(), plain: true} // no thinking in the transcript
	prov.prof.ReplayThinking = false
	prov.handle = cxWorkModel(3, nil)
	a, _ := cxAgent(t, cxOpts{prov: prov, hot: cxBoard(), noCompct: true, model: "claude-sonnet-5"})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	reqs := prov.requests()
	eph := 0
	for _, blk := range reqs[len(reqs)-1].Messages[len(reqs[len(reqs)-1].Messages)-1].Blocks {
		if blk.Ephemeral {
			eph++
		}
	}
	if eph != 1 {
		t.Fatalf("inline mode appends exactly one ephemeral hot block after the last persistent one, got %d", eph)
	}
	for _, m := range reqs[len(reqs)-1].Messages {
		for _, blk := range m.Blocks {
			if kv.IsNotice(blk) {
				t.Fatal("inline mode persists nothing")
			}
		}
	}
	if a.Thread().Snapshot().Tokens(core.NewBytesEstimator()) > 2000 {
		t.Fatal("the board must not accumulate in the thread in inline mode")
	}
}

// If a route enforces bindings although the model is not flagged (a gateway, a
// new model), the first rejection is recovered instead of ending the run:
// thinking is stripped durably (a declared rebase), the endpoint is asked to
// drop rather than reject where it can, and the request is retried once.
func TestCacheEcon_BindingRejectionIsRecoveredOnce(t *testing.T) {
	prof := cxAnthropicProfile()
	prof.BindingControls = true
	prov := &cxProv{prof: prof}
	prov.handle = cxWorkModel(3, nil)
	// claude-sonnet-5 is not flagged PreservedThinking, so the tail is inline and
	// the fake (which enforces bindings) rejects the second request.
	a, log := cxAgent(t, cxOpts{prov: prov, hot: cxBoard(), noCompct: true, model: "claude-sonnet-5"})
	res, err := a.Run(context.Background(), "do the work")
	if err != nil || res.Steps != 4 {
		t.Fatalf("the run must recover: steps=%v err=%v", res, err)
	}
	if prov.rejects == 0 {
		t.Fatal("setup: the fake never rejected anything")
	}
	if n := cxCount(log, events.TypeCacheAnomaly, `"kind":"thinking_binding"`); n != prov.rejects {
		t.Fatalf("every rejection must be logged as an anomaly: %d vs %d", n, prov.rejects)
	}
	if n := cxCount(log, events.TypeLayerCommit, `"scope":"thinking-strip"`); n != prov.rejects {
		t.Fatalf("every strip is a declared rebase event: %d", n)
	}
	dropped := 0
	for _, m := range prov.modes {
		if m == "drop_block" {
			dropped++
		}
	}
	if dropped != prov.rejects {
		t.Fatalf("each retry asks the endpoint to drop mismatching blocks (%d of %d rejections; modes %v)", dropped, prov.rejects, prov.modes)
	}

	// A rejection that stripping cannot cure is not retried forever.
	prov2 := &cxProv{prof: cxAnthropicProfile(), failAlways: true}
	prov2.handle = cxWorkModel(3, nil)
	b, _ := cxAgent(t, cxOpts{prov: prov2, noCompct: true})
	_, err = b.Run(context.Background(), "go")
	if pe, ok := provider.AsError(err); !ok || pe.Kind != provider.ErrThinkingBinding {
		t.Fatalf("want the binding error back, got %v", err)
	}
	if n := len(prov2.requests()); n != 1 {
		t.Fatalf("with no thinking to strip there is nothing to retry, made %d requests", n)
	}
}

// ---------------------------------------------------------------------------
// R2: rebases strip thinking atomically.
// ---------------------------------------------------------------------------

func TestCacheEcon_BackgroundCommitStripsThinkingOfCarriedTurns(t *testing.T) {
	prov := &cxProv{prof: cxAnthropicProfile()}
	prov.handle = cxWorkModel(12, func(p *cxProv, pr *core.Prompt) string {
		// A compactor that thinks for two more agent steps.
		p.mu.Lock()
		start := p.mainN
		p.mu.Unlock()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p.mu.Lock()
			n := p.mainN
			p.mu.Unlock()
			if n >= start+2 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		return `{"keep_from":"t5","spine":[{"turns":"t1-t4","line":"explored"}],"mask":[],"notes":[],"promote":[]}`
	})
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens, pl.HardThreadTokens, pl.MinThreadTokens = 500, 900, 300
	a, log := cxAgent(t, cxOpts{prov: prov, planner: pl})
	_, err := a.Run(context.Background(), "do the work")
	commits := log.OfType(events.TypeCompactCommit)
	if len(commits) == 0 {
		t.Fatalf("test setup: no compaction committed (plan events=%d rejects=%d) err=%v", len(log.OfType(events.TypeCompactPlan)), len(log.OfType(events.TypeCompactReject)), err)
	}
	if err != nil || prov.rejects != 0 {
		t.Fatalf("the first request after a background commit must not replay pre-commit thinking: err=%v rejections=%d", err, prov.rejects)
	}
}

type cxHookLog struct {
	*events.MemLog
	mu    sync.Mutex
	armed bool
	fire  func()
}

func (h *cxHookLog) Emit(agent, typ string, data any, opts ...events.Opt) (uint64, error) {
	h.mu.Lock()
	fire := h.fire
	if typ == events.TypeTurnAppend && h.armed && fire != nil {
		h.armed = false
	} else {
		fire = nil
	}
	h.mu.Unlock()
	if fire != nil {
		fire()
	}
	return h.MemLog.Emit(agent, typ, data, opts...)
}

// A shared-layer epoch (swarm.SetShared runs on another goroutine) that lands
// between boundary() and the request snapshot used to send the new layer with
// thinking bound to the old one. SyncShared now strips in the same critical
// section that swaps the layers, so a snapshot sees both or neither. The hook
// makes the interleaving deterministic: drainInbox() -> push() -> Emit fires it.
func TestCacheEcon_SyncSharedStripsInTheSameStepAsTheSwap(t *testing.T) {
	newShared := kv.NewLayer("shared", kv.KindShared, 2, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo moved to a monorepo. ", 100), Vol: kv.VolEpoch}})
	run := func(syncInWindow bool) error {
		prov := &cxProv{prof: cxAnthropicProfile()}
		prov.handle = cxWorkModel(2, nil)
		hook := &cxHookLog{MemLog: events.NewMemLog()}
		a, _ := cxAgent(t, cxOpts{prov: prov, noCompct: true, emitter: hook})
		if _, err := a.Run(context.Background(), "do the work"); err != nil {
			t.Fatalf("setup run: %v", err)
		}
		// Mail arrives for the idle agent (swarm.deliver: Send then Run("")).
		a.Send("[mail] please also update the docs")
		if syncInWindow {
			hook.mu.Lock()
			hook.armed, hook.fire = true, func() { a.SyncShared(newShared, nil, "epoch") }
			hook.mu.Unlock()
		} else {
			a.SyncShared(newShared, nil, "epoch") // the normal order: the epoch lands before the run starts
		}
		prov.handle = cxWorkModel(3, nil)
		_, err := a.Run(context.Background(), "")
		if err == nil {
			last := prov.requests()[len(prov.requests())-1]
			if cxThinkingIn(last) > 2 { // only what the post-epoch steps produced may be replayed
				t.Fatalf("pre-epoch thinking survived the epoch: %d blocks", cxThinkingIn(last))
			}
		}
		return err
	}
	if err := run(false); err != nil {
		t.Fatalf("control: an epoch before the boundary must strip cleanly, got %v", err)
	}
	if err := run(true); err != nil {
		t.Fatalf("an epoch between boundary() and the request must strip cleanly, got %v", err)
	}
}

// An epoch that lands while a request is IN FLIGHT: the response's thinking is
// bound to a prefix that no longer exists. It is stripped as it is appended.
func TestCacheEcon_EpochDuringARequestStripsTheResponsesThinking(t *testing.T) {
	newShared := kv.NewLayer("shared", kv.KindShared, 2, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo moved to a monorepo. ", 100), Vol: kv.VolEpoch}})
	prov := &cxProv{prof: cxAnthropicProfile()}
	inner := cxWorkModel(3, nil)
	var a *agent.Agent
	prov.handle = func(p *cxProv, req *provider.Request) (*provider.Response, error) {
		resp, err := inner(p, req)
		p.mu.Lock()
		n := p.mainN
		p.mu.Unlock()
		if n == 2 && err == nil {
			a.SyncShared(newShared, nil, "epoch during the request") // lands after the prompt was sent
		}
		return resp, err
	}
	var log *events.MemLog
	a, log = cxAgent(t, cxOpts{prov: prov, noCompct: true})
	res, err := a.Run(context.Background(), "do the work")
	if err != nil || res.Steps != 4 || prov.rejects != 0 {
		t.Fatalf("the response of the in-flight request must not poison the next one: steps=%v err=%v rejections=%d", res, err, prov.rejects)
	}
	if cxCount(log, events.TypeLayerCommit, `"scope":"shared-sync"`) != 1 {
		t.Fatal("setup: the epoch never happened")
	}
	reqs := prov.requests()
	if got := cxThinkingIn(reqs[2]); got != 0 {
		t.Fatalf("the third request (first after the epoch) must carry no thinking bound to the old prefix, got %d", got)
	}
}
