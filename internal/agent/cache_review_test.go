package agent_test

// Adversarial review tests (lens: prompt-cache economics and correctness).
// See docs/reviews/cache-economics.md. Same convention as internal/kv's
// cache_review_test.go: tests PASS while the defect is present and FAIL once it
// is fixed; REVIEW_STRICT=1 inverts that.
//
//	go test -race -count=1 -run CacheEcon -v ./internal/agent
//	REVIEW_STRICT=1 go test -count=1 -run CacheEcon ./internal/agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	sigErr *provider.Error
	handle func(p *cxProv, req *provider.Request) (*provider.Response, error)
	mainN  int
	prevIn int  // total input tokens of the previous main request (for perfect-cache usage)
	plain  bool // produce turns without thinking blocks (providers that do not replay thinking)
}

func (p *cxProv) Profile() provider.Profile { return p.prof }

func (p *cxProv) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req.Prompt)
	p.mu.Unlock()
	if err := cxVerify(req.Prompt); err != nil {
		p.mu.Lock()
		p.sigErr = err
		p.mu.Unlock()
		return nil, err
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
		n := cxAssistants(pr)
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
	m, _ := cost.Defaults().Lookup("claude-opus-5-5")
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
		Hot: o.hot, AffinityShards: o.shards, MaxSteps: o.steps,
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

// ---------------------------------------------------------------------------
// R-PT2: the hot tail is a per-request rewrite of the last user message. The
// signature of the assistant turn produced in response to request N is bound to
// a prefix that contains hot_N; request N+1 no longer contains it.
// ---------------------------------------------------------------------------

func TestCacheEcon_HotTailBreaksPreservedThinking(t *testing.T) {
	// Control: append-only, no hot block. Six tool rounds with replayed thinking work.
	prov := &cxProv{prof: cxAnthropicProfile()}
	prov.handle = cxWorkModel(6, nil)
	a, _ := cxAgent(t, cxOpts{prov: prov, noCompct: true})
	res, err := a.Run(context.Background(), "do the work")
	if err != nil || res.Steps != 7 {
		t.Fatalf("control (append-only, no hot) must pass the binding check: steps=%v err=%v", res, err)
	}
	thinking := 0
	for _, m := range prov.requests()[len(prov.requests())-1].Messages {
		for _, b := range m.Blocks {
			if b.Kind == core.BlockThinking {
				thinking++
			}
		}
	}
	if thinking != 6 {
		t.Fatalf("control should replay 6 thinking blocks, saw %d", thinking)
	}

	// Sleipnir's design: a fresh <live> block after the last breakpoint on every request.
	prov2 := &cxProv{prof: cxAnthropicProfile()}
	prov2.handle = cxWorkModel(6, nil)
	hotN := 0
	hot := func(string) []core.Block {
		hotN++
		return []core.Block{core.Text(fmt.Sprintf(`<live board="v%d">tasks: none</live>`, hotN))}
	}
	b, _ := cxAgent(t, cxOpts{prov: prov2, hot: hot, noCompct: true})
	_, err = b.Run(context.Background(), "do the work")
	pe, ok := provider.AsError(err)
	t.Logf("with the hot tail: requests served=%d err=%v", len(prov2.requests()), err)
	cxBug(t, ok && pe.Kind == provider.ErrThinkingBinding && len(prov2.requests()) == 2,
		"with thinking replay on, the second request is rejected (HTTP 400 thinking binding) and Run gives up: no recovery path exists (agent/request.go handles only ErrContextLength)")
}

// ---------------------------------------------------------------------------
// R-PT1 (agent level): a background compaction lands, the turns produced while it
// ran are carried over with pre-commit thinking, and the next request is a 400.
// ---------------------------------------------------------------------------

func TestCacheEcon_BackgroundCommitBreaksThinkingOfCarriedTurns(t *testing.T) {
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
	pe, ok := provider.AsError(err)
	t.Logf("commits=%d err=%v", len(commits), err)
	if len(commits) == 0 {
		t.Fatalf("test setup: no compaction committed (plan events=%d rejects=%d) err=%v", len(log.OfType(events.TypeCompactPlan)), len(log.OfType(events.TypeCompactReject)), err)
	}
	cxBug(t, ok && pe.Kind == provider.ErrThinkingBinding,
		"the first request after a background commit replays the carried-over turns' pre-commit thinking and is rejected: %v", err)
}

// ---------------------------------------------------------------------------
// R-SS1: Agent.SyncShared only sets a flag; the durable thinking strip runs at
// the start of the NEXT boundary. A shared-layer epoch that lands after
// boundary() has run but before requestOnce() snapshots the stack (swarm.SetShared
// runs on another goroutine) sends the new shared layer together with thinking
// bound to the old one. The hook below makes that interleaving deterministic:
// drainInbox() -> push() -> Emit(turn.append) fires SyncShared.
// ---------------------------------------------------------------------------

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

func TestCacheEcon_SyncSharedBetweenBoundaryAndRequestSendsStaleThinking(t *testing.T) {
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
		return err
	}
	if err := run(false); err != nil {
		t.Fatalf("control: an epoch that lands before the boundary must strip cleanly, got %v", err)
	}
	err := run(true)
	pe, ok := provider.AsError(err)
	t.Logf("epoch lands between boundary() and requestOnce(): %v", err)
	cxBug(t, ok && pe.Kind == provider.ErrThinkingBinding,
		"the request carries the new shared layer plus pre-epoch thinking (HTTP 400); the strip is a flag consumed one boundary too late")
}

// ---------------------------------------------------------------------------
// R-WM1: "warm" is inferred from lastHit >= 0.3, which measures how much of the
// prompt is OLD, not whether the cache is warm.
// ---------------------------------------------------------------------------

func TestCacheEcon_WarmHeuristicMisreadsABigToolResult(t *testing.T) {
	prov := &cxProv{prof: cxAnthropicProfile()}
	prov.handle = cxWorkModel(6, nil)
	step := 0
	pl := kv.DefaultPlanner() // Soft 20k / Min 4k: only the "cold" branch can start a compaction here
	a, log := cxAgent(t, cxOpts{prov: prov, planner: pl, toolOut: func(json.RawMessage) *tools.Result {
		step++
		if step == 3 { // `cat big.log`
			return &tools.Result{Text: strings.Repeat("0123456789abcdef", 3500)} // ~14k tokens
		}
		return &tools.Result{Text: "ok"}
	}})
	if _, err := a.Run(context.Background(), "do the work"); err != nil {
		t.Fatal(err)
	}
	var coldStart string
	for _, e := range log.OfType(events.TypeCompactPlan) {
		var d struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
			Warm     bool   `json:"warm"`
			Thread   int    `json:"thread_tokens"`
		}
		_ = json.Unmarshal(e.Data, &d)
		if d.Decision == "start" && !d.Warm {
			coldStart = fmt.Sprintf("%s (thread %d tokens)", d.Reason, d.Thread)
		}
	}
	var lastHit float64
	for _, e := range log.OfType(events.TypeModelResponse) {
		var m struct {
			Hit  float64 `json:"hit_ratio"`
			Side bool    `json:"side"`
		}
		_ = json.Unmarshal(e.Data, &m)
		if !m.Side {
			t.Logf("request hit ratio %.2f", m.Hit)
			lastHit = m.Hit
		}
	}
	_ = lastHit
	cxBug(t, coldStart != "",
		"the provider served 100%% of the previous prefix from cache, yet a 14k-token tool result drops the hit ratio under 0.3, the agent is declared cold and the cold-path compaction starts (a rebase that would rewrite a warm cache): %s", coldStart)
}

// A provider that does not report cached tokens (the design doc says the
// marketplace's Anthropic-style route does not) looks permanently cold.
func TestCacheEcon_NonReportingProviderLooksColdForever(t *testing.T) {
	prov := &cxProv{prof: cxAnthropicProfile()}
	inner := cxWorkModel(8, nil)
	prov.handle = func(p *cxProv, req *provider.Request) (*provider.Response, error) {
		r, err := inner(p, req)
		if r != nil {
			r.Usage.InputTokens += r.Usage.CacheReadTokens
			r.Usage.CacheReadTokens = 0 // gateway strips cache usage
		}
		return r, err
	}
	pl := kv.DefaultPlanner()
	a, log := cxAgent(t, cxOpts{prov: prov, planner: pl, toolOut: func(json.RawMessage) *tools.Result {
		return &tools.Result{Text: strings.Repeat("0123456789abcdef", 300)} // ~1.2k tokens per step
	}})
	if _, err := a.Run(context.Background(), "do the work"); err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, e := range log.OfType(events.TypeCompactPlan) {
		var d struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal(e.Data, &d)
		if d.Decision == "start" && strings.Contains(d.Reason, "cold") {
			starts++
		}
	}
	cxBug(t, starts > 0, "%d cold-path compaction(s) started ('cache is cold: shrink for free by masking') although the provider merely does not report cache usage and the thread was only a few thousand tokens (MinThreadTokens=4000, Soft=20000)", starts)
}

// ---------------------------------------------------------------------------
// R-LH1: the low_hit anomaly is a fixed 70% relative test.
// ---------------------------------------------------------------------------

func TestCacheEcon_LowHitAnomalyIsSilentOnLargeMisses(t *testing.T) {
	const degradedFrom = 6 // requests 1-5 are healthy; from the 6th on the provider reads only `frac` of the prefix
	run := func(frac float64) (anomalies, degraded int) {
		prov := &cxProv{prof: cxAnthropicProfile()}
		inner := cxWorkModel(10, nil)
		prov.handle = func(p *cxProv, req *provider.Request) (*provider.Response, error) {
			r, err := inner(p, req)
			if r != nil && p.mainN >= degradedFrom && !strings.Contains(cxLastUserText(req.Prompt), "<compactor-task>") {
				read := int(float64(r.Usage.CacheReadTokens) * frac)
				r.Usage.InputTokens += r.Usage.CacheReadTokens - read
				r.Usage.CacheReadTokens = read
				degraded++
			}
			return r, err
		}
		a, log := cxAgent(t, cxOpts{prov: prov, noCompct: true, toolOut: func(json.RawMessage) *tools.Result {
			return &tools.Result{Text: strings.Repeat("0123456789abcdef", 1500)} // 6k tokens/step: the thread dominates
		}})
		if _, err := a.Run(context.Background(), "do the work"); err != nil {
			t.Fatal(err)
		}
		return len(log.OfType(events.TypeCacheAnomaly)), degraded
	}
	if n, _ := run(1.0); n != 0 {
		t.Fatalf("control: perfect cache must not alarm, got %d", n)
	}
	n30, deg30 := run(0.3)
	if n30 == 0 {
		t.Fatal("control: a 70% miss must alarm at least once")
	}
	n75, deg75 := run(0.75)
	t.Logf("70%% miss for %d consecutive requests -> %d anomaly event(s); 25%% miss for %d requests -> %d anomaly event(s)", deg30, n30, deg75, n75)
	cxBug(t, n75 == 0 && n30 < deg30,
		"(a) a persistent 25%% miss (the recent thread re-written at 1.25x every step) never fires low_hit (fixed 70%% relative threshold); (b) a persistent 70%% miss fires %d time(s) for %d bad requests because the alarm is gated on the same lastHit>=0.3 'warm' heuristic and silences itself", n30, deg30)
}

// ---------------------------------------------------------------------------
// R-WG1: the warm gate is keyed by GlobalKey only, but routing (shard) and the
// role pin both split the physical prefix.
// ---------------------------------------------------------------------------

type cxGate struct {
	mu   sync.Mutex
	keys []string
}

func (g *cxGate) Enter(_ context.Context, key string) (func(bool), error) {
	g.mu.Lock()
	g.keys = append(g.keys, key)
	g.mu.Unlock()
	return func(bool) {}, nil
}

func TestCacheEcon_GateKeyIgnoresShardAndRole(t *testing.T) {
	gate := &cxGate{}
	cacheKeys := map[string]bool{}
	roles := []string{"backend", "frontend"}
	for i := 0; i < 8; i++ {
		prov := &cxProv{prof: cxAnthropicProfile()}
		inner := cxWorkModel(0, nil)
		prov.handle = func(p *cxProv, req *provider.Request) (*provider.Response, error) {
			p.mu.Lock()
			cacheKeys[req.Prompt.CacheKey] = true
			p.mu.Unlock()
			return inner(p, req)
		}
		reg := tools.NewRegistry()
		reg.Register(cxTool{run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "x"} }})
		specs, _ := reg.Specs()
		m, _ := cost.Defaults().Lookup("claude-opus-5-5")
		role := roles[i%2]
		a, err := agent.New(agent.Config{
			ID: fmt.Sprintf("w-%d", i), Role: role, Model: m, Provider: prov, Tools: reg, ToolSpecs: specs, Gate: gate,
			Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: "constitution"}}),
			Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "p", Text: "shared", Vol: kv.VolEpoch}}),
			RoleL:  kv.NewLayer("role:"+role, kv.KindRole, 1, []kv.Segment{{Key: role, Text: "pin for " + role, Vol: kv.VolEpoch}}),
			Params: core.Params{MaxTokens: 64}, SessionID: "review", AffinityShards: 4, NoCompaction: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Run(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
	}
	gateKeys := map[string]bool{}
	for _, k := range gate.keys {
		gateKeys[k] = true
	}
	t.Logf("8 agents, 2 roles, 4 shards: %d distinct gate keys, %d distinct routing keys", len(gateKeys), len(cacheKeys))
	cxBug(t, len(gateKeys) == 1 && len(cacheKeys) > 1,
		"one gate key (%d) for %d routing keys and 2 role pins: followers on other shards/roles are released after the primer's first byte although their engine/prefix is still cold", len(gateKeys), len(cacheKeys))
}

// ---------------------------------------------------------------------------
// R-CS1 (verified FIXED in the current tree): "cold cache: compaction is free".
// Starting a fork because the agent is cold made the fork and the main request
// race, each prefilling the cold prompt. The planner now answers ModeMask for a
// cold agent (deterministic masking, no model call). Kept as a regression test.
// ---------------------------------------------------------------------------

type cxClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *cxClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *cxClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestCacheEcon_ColdStartNoLongerForksAModelCall(t *testing.T) {
	clk := &cxClock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	agent.RetryBase = time.Millisecond
	srv := mock.New(mock.Config{
		Engine:     mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64, TTL: 5 * time.Minute},
		FirstToken: 120 * time.Millisecond, Now: clk.Now,
	}, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.LastUser(), "<compactor-task>") {
			return mock.Reply{Text: `{"keep_from":"t5","spine":[{"turns":"t1-t4","line":"explored the repo"}],"mask":[],"notes":[],"promote":[]}`}
		}
		n := 0
		for _, m := range c.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		if n < 6 {
			return mock.Reply{Text: fmt.Sprintf("step %d", n), ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("call_%d", n), Name: "work", Args: fmt.Sprintf(`{"n":%d}`, n)}}}
		}
		return mock.Reply{Text: "done"}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	reg := tools.NewRegistry()
	reg.Register(cxTool{run: func(json.RawMessage) *tools.Result {
		return &tools.Result{Text: strings.Repeat("build output line with some detail\n", 160)}
	}})
	specs, _ := reg.Specs()
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens, pl.HardThreadTokens = 100_000, 200_000 // pressure never triggers; only "cold" can
	log := events.NewMemLog()
	a, err := agent.New(agent.Config{
		ID: "be-1", Role: "backend", Model: model, Provider: client, Tools: reg, ToolSpecs: specs, Now: clk.Now,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("You are Sleipnir, a careful coding agent. ", 150)}}),
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go service. ", 150), Vol: kv.VolEpoch}}),
		Params: core.Params{MaxTokens: 256}, Events: log, Planner: pl, SessionID: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "build it"); err != nil {
		t.Fatal(err)
	}
	warmReqs := len(srv.Stats())
	clk.Advance(6 * time.Minute) // longer than the provider TTL: everything is cold
	if _, err := a.Run(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	after := srv.Stats()[warmReqs:]
	forks := 0
	for _, e := range log.OfType(events.TypeCompactPatch) {
		if strings.Contains(string(e.Data), `"stage":"request"`) {
			forks++
		}
	}
	coldPrefills := 0
	for i, s := range after {
		t.Logf("after idle #%d: prompt=%d cached=%d", i+1, s.PromptTokens, s.Cached)
		if s.Cached == 0 && s.PromptTokens > 500 {
			coldPrefills++
		}
	}
	if forks != 0 || coldPrefills != 1 {
		t.Fatalf("cold start must mask without a model call and prefill the cold prompt once: forks=%d cold prefills=%d", forks, coldPrefills)
	}
}

// ---------------------------------------------------------------------------
// R-HP1: a ready patch that is marginal when it is computed is held "for a cold
// moment" and blocks every later compaction. Its economics are frozen at the
// snapshot (cs.ThreadTokens = snapTotal), so the hard-limit override in
// ShouldCommit never sees the live thread, and boundary() returns before the
// emergency check while a patch is pending. A continuously warm agent (the
// normal case) then grows without bound.
// ---------------------------------------------------------------------------

func cxCompactionRun(t *testing.T, plain, reportCache bool, steps int) (log *events.MemLog, thread int, prov *cxProv) {
	t.Helper()
	prov = &cxProv{prof: cxAnthropicProfile(), plain: plain}
	if plain {
		prov.prof.ReplayThinking = false
	}
	inner := cxWorkModel(steps, func(p *cxProv, pr *core.Prompt) string {
		// A cautious compactor: folds only the first eight turns.
		return `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"did the early work"}],"mask":[],"notes":[],"promote":[]}`
	})
	prov.handle = func(p *cxProv, req *provider.Request) (*provider.Response, error) {
		r, err := inner(p, req)
		if r != nil && !reportCache {
			r.Usage.InputTokens += r.Usage.CacheReadTokens
			r.Usage.CacheReadTokens = 0
		}
		return r, err
	}
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens, pl.HardThreadTokens, pl.MinThreadTokens = 1500, 3000, 800
	var a *agent.Agent
	a, log = cxAgent(t, cxOpts{prov: prov, planner: pl, steps: steps + 20, toolOut: func(json.RawMessage) *tools.Result {
		return &tools.Result{Text: strings.Repeat("ok line\n", 25)} // ~50 tokens: never worth masking
	}})
	if _, err := a.Run(context.Background(), "do the work"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	return log, a.Thread().Snapshot().Tokens(core.NewBytesEstimator()), prov
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

func TestCacheEcon_HeldMarginalPatchBlocksCompactionForever(t *testing.T) {
	log, thread, _ := cxCompactionRun(t, false, true, 60)
	starts := cxCount(log, events.TypeCompactPlan, `"decision":"start"`)
	holds := cxCount(log, events.TypeCompactPlan, `"yes":false`)
	commits := cxCount(log, events.TypeCompactCommit, "")
	t.Logf("warm agent, 60 steps, hard limit 3000: compactions started=%d, 'commit?' evaluations that held=%d, commits=%d, live thread ~%d tokens", starts, holds, commits, thread)
	cxBug(t, starts == 1 && commits == 0 && holds > 20 && thread > 2*3000,
		"one marginal patch is held for the rest of the run (net stays at its snapshot value), no second compaction starts, and the live thread reaches ~%d tokens against a hard limit of 3000", thread)
}

// ---------------------------------------------------------------------------
// R-MM1 (introduced by the ModeMask fix). "Cold" is inferred from lastHit<0.3, so
// a provider that hides cache usage is cold forever, and every boundary takes
// the mask path. kv.MaskOnly counts stripped thinking as progress: one commit per
// step, each a full rebase (thread rewritten, reasoning dropped), masking nothing.
// ---------------------------------------------------------------------------

func TestCacheEcon_ColdModeCommitsEveryStepByStrippingThinking(t *testing.T) {
	log, thread, _ := cxCompactionRun(t, false, false, 60)
	commits := cxCount(log, events.TypeCompactCommit, "")
	maskCommits := cxCount(log, events.TypeCompactCommit, `"reason":"mask:`)
	noMasks := cxCount(log, events.TypeCompactCommit, `"masked":0`)
	t.Logf("provider hides cache usage, 60 steps: %d commits (%d 'mask:' commits, %d of them masked no result); thread ~%d tokens", commits, maskCommits, noMasks, thread)
	cxBug(t, maskCommits > 30 && noMasks == maskCommits,
		"%d of 60 steps ended in a 'mask' commit that masked nothing: each is a declared rebase that rewrites the whole thread and drops the model's reasoning", maskCommits)
}

// With nothing to strip either (a provider that does not replay thinking) the mask
// path has nothing to do, but it is still the only path: the thread is never folded.
func TestCacheEcon_ColdModeWithNothingToMaskNeverFolds(t *testing.T) {
	log, thread, _ := cxCompactionRun(t, true, false, 120)
	commits := cxCount(log, events.TypeCompactCommit, "")
	rejects := cxCount(log, events.TypeCompactReject, `"stage":"mask"`)
	forks := cxCount(log, events.TypeCompactPatch, `"stage":"request"`)
	t.Logf("no thinking, cache hidden: commits=%d, rejected mask attempts=%d, model compactions started=%d, live thread ~%d tokens (hard limit 3000)", commits, rejects, forks, thread)
	cxBug(t, commits == 0 && forks == 0 && rejects > 5 && thread > 2*3000,
		"cold => ModeMask even above the HARD limit => MaskOnly finds nothing => no fork fallback: %d failed attempts, no fold, thread ~%d tokens", rejects, thread)
}

// ---------------------------------------------------------------------------
// R-TK1: the planner sizes the thread with Thread.Snapshot().Tokens, which counts
// thinking blocks' text. When the profile does not replay thinking (the default
// openaichat profile: ReplayThinking=false) Render drops those blocks, so the
// planner sees a thread several times larger than the prompt the provider gets:
// compaction pressure arrives early and every compaction/rewrite is paid for
// tokens that were never sent.
// ---------------------------------------------------------------------------

func TestCacheEcon_PlannerCountsReasoningThatIsNeverSent(t *testing.T) {
	agent.RetryBase = time.Millisecond
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		n := 0
		for _, m := range c.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		if n < 8 {
			return mock.Reply{Reasoning: strings.Repeat("hmm, considering the options carefully. ", 150), // ~1500 tokens of reasoning per step
				Text: "ok", ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("call_%d", n), Name: "work", Args: `{}`}}}
		}
		return mock.Reply{Text: "done"}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	reg := tools.NewRegistry()
	reg.Register(cxTool{run: func(json.RawMessage) *tools.Result { return &tools.Result{Text: "ok"} }})
	specs, _ := reg.Specs()
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	a, err := agent.New(agent.Config{
		ID: "be-1", Role: "backend", Model: model, Provider: client, Tools: reg, ToolSpecs: specs, NoCompaction: true,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: strings.Repeat("You are Sleipnir. ", 100)}}),
		Params: core.Params{MaxTokens: 256}, SessionID: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	st := srv.Stats()
	sentThread := st[len(st)-1].PromptTokens - st[0].PromptTokens // what the engine actually received for the thread
	plannerThread := a.Thread().Snapshot().Tokens(core.NewBytesEstimator().WithRatio(4))
	t.Logf("thread as the provider received it: ~%d tokens; thread as the planner measures it: ~%d tokens", sentThread, plannerThread)
	cxBug(t, plannerThread > 4*sentThread, "planner thread size %d vs %d actually sent: reasoning text is counted although ReplayThinking=false drops it", plannerThread, sentThread)
}

func init() { _ = errors.New }
