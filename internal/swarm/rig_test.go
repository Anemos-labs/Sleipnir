package swarm

// Adversarial-review helpers: an in-process fake provider whose replies can block
// on channels, so races in the swarm runtime can be provoked deterministically
// instead of by luck. Everything here is prefixed rv to stay clear of the
// helpers in the other test files of this package.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/tools"
)

// concGate skips a repro of a finding that is still open unless SLEIPNIR_REVIEW is set (same switch the
// security review uses). Repros assert the CORRECT behaviour and therefore fail
// while the finding is open; TestConcSound_* tests are ungated regression checks
// for behaviour the review found sound.
func concGate(t *testing.T) {
	t.Helper()
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("concurrency-review repro: set SLEIPNIR_REVIEW=1 (asserts the correct behaviour, fails while the finding is open)")
	}
}

// rvCall is what the fake provider learns about one request.
type rvCall struct {
	Agent, Role string // parsed from the hot block ("you: be-1 (backend)")
	Assistants  int    // assistant turns already in the thread
	Prompt      *core.Prompt
	Label       string
}

var rvWho = regexp.MustCompile(`you: (\S+) \((\w+)\)`)

func rvParse(p *core.Prompt, label string) *rvCall {
	c := &rvCall{Prompt: p, Label: label}
	for _, m := range p.Messages {
		if m.Role == core.RoleAssistant {
			c.Assistants++
		}
		for _, b := range m.Blocks {
			if b.Kind == core.BlockText {
				if x := rvWho.FindAllStringSubmatch(b.Text, -1); len(x) > 0 {
					last := x[len(x)-1]
					c.Agent, c.Role = last[1], last[2]
				}
			}
		}
	}
	return c
}

// Sees reports whether any text (or tool result) in the prompt contains sub.
func (c *rvCall) Sees(sub string) bool {
	for _, m := range c.Prompt.Messages {
		for _, b := range m.Blocks {
			if strings.Contains(b.PlainText(), sub) {
				return true
			}
		}
	}
	return false
}

type rvToolCall struct {
	Name string
	Args any
}

type rvReply struct {
	Text  string
	Tools []rvToolCall
	Usage core.Usage
}

type rvProvider struct {
	fn   func(ctx context.Context, c *rvCall) rvReply
	prof provider.Profile
	n    atomic.Int64

	mu    sync.Mutex
	calls []*rvCall
}

func (p *rvProvider) Profile() provider.Profile { return p.prof }

func (p *rvProvider) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	c := rvParse(req.Prompt, req.Label)
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.mu.Unlock()
	if on != nil {
		on(provider.Event{Kind: provider.EvStart})
	}
	r := p.fn(ctx, c)
	if err := ctx.Err(); err != nil {
		return nil, &provider.Error{Kind: provider.ErrNetwork, Message: "request cancelled", Err: err}
	}
	n := p.n.Add(1)
	turn := core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Model: "rv-1"}
	if r.Text != "" || len(r.Tools) == 0 {
		txt := r.Text
		if txt == "" {
			txt = "ok"
		}
		turn.Blocks = append(turn.Blocks, core.Text(txt))
	}
	for i, tc := range r.Tools {
		b, _ := json.Marshal(tc.Args)
		turn.Blocks = append(turn.Blocks, core.ToolUse(fmt.Sprintf("call_%d_%d", n, i), tc.Name, b))
	}
	stop := core.StopEnd
	if len(r.Tools) > 0 {
		stop = core.StopToolUse
	}
	return &provider.Response{ID: fmt.Sprintf("rv-%d", n), Model: "rv-1", Turn: turn, Usage: r.Usage, Stop: stop}, nil
}

// sawEver reports whether any request so far contained sub anywhere in its prompt.
func (p *rvProvider) sawEver(sub string) bool {
	p.mu.Lock()
	calls := append([]*rvCall(nil), p.calls...)
	p.mu.Unlock()
	for _, c := range calls {
		if c.Sees(sub) {
			return true
		}
	}
	return false
}

func (p *rvProvider) callsFor(agentID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c.Agent == agentID {
			n++
		}
	}
	return n
}

// rvFakeTool is a configurable tool.
type rvFakeTool struct {
	name string
	ro   bool
	run  func(ctx context.Context, c *tools.Call) *tools.Result
}

func (f rvFakeTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: f.name, Description: "fake " + f.name, InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: f.ro}
}
func (f rvFakeTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	return f.run(ctx, c), nil
}

type rvRig struct {
	t    *testing.T
	sw   *Swarm
	prov *rvProvider
	log  *events.MemLog
}

// newRVRig builds a started swarm over the fake provider. fn scripts the model.
func newRVRig(t *testing.T, cfg Config, fn func(ctx context.Context, c *rvCall) rvReply) *rvRig {
	t.Helper()
	return newRVRigWith(t, cfg, fn, nil)
}

// newRVRigWith is newRVRig with a hook to adjust the swarm's Deps before New.
func newRVRigWith(t *testing.T, cfg Config, fn func(ctx context.Context, c *rvCall) rvReply, tweak func(*Deps)) *rvRig {
	t.Helper()
	agent.RetryBase = time.Millisecond
	prov := &rvProvider{fn: fn, prof: provider.Profile{Name: "rv", Dialect: "openai-chat", Cache: cost.OpenAICacheModel()}}
	model := cost.Model{ID: "rv-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	log := events.NewMemLog()
	blobs := events.NewMemBlobs()
	deps := Deps{
		Provider: prov, Model: model, Events: log, Blobs: blobs, Archive: kv.NewArchive(blobs),
		Const:   kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: "You are a careful coding agent."}}),
		Shared:  kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: "The repo is a Go API.", Vol: kv.VolEpoch}}),
		Workdir: t.TempDir(), Root: t.TempDir(), Params: core.Params{MaxTokens: 512},
		Files: tools.NewFileState(), Handles: tools.NewHandles(),
	}
	if tweak != nil {
		tweak(&deps)
	}
	sw := New(cfg, deps, nil)
	reg := tools.NewRegistry()
	reg.Register(rvFakeTool{name: "edit", run: func(ctx context.Context, c *tools.Call) *tools.Result {
		var in struct{ Path string }
		_ = json.Unmarshal(c.Input, &in)
		if err := c.Env.Guard.BeforeWrite(c.Env.Agent, in.Path); err != nil {
			return tools.Errorf("%v", err)
		}
		c.Env.Guard.AfterWrite(c.Env.Agent, in.Path)
		return &tools.Result{Text: "edited " + in.Path}
	}})
	reg.Register(rvFakeTool{name: "read", ro: true, run: func(context.Context, *tools.Call) *tools.Result { return &tools.Result{Text: "contents"} }})
	for _, tl := range sw.Tools() {
		reg.Register(tl)
	}
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	sw.SetToolset(reg, specs)
	sw.Start(context.Background())
	t.Cleanup(sw.Shutdown)
	return &rvRig{t: t, sw: sw, prov: prov, log: log}
}

// rvWait polls cond for up to 10s.
func rvWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// rvBlock waits for ch or ctx (so a blocked fake model never hangs Shutdown).
func rvBlock(ctx context.Context, ch <-chan struct{}) {
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

func (r *rvRig) idle(id string) bool {
	m := r.sw.get(id)
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.life == lifeIdle
}

func (r *rvRig) running(id string) bool {
	m := r.sw.get(id)
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.life == lifeRunning
}

// callTool runs one of the swarm's coordination tools as agent `as` with role `role`.
func (r *rvRig) callTool(ctx context.Context, name, as, role string, args any) *tools.Result {
	r.t.Helper()
	reg := r.sw.deps.Registry
	tl, ok := reg.Get(name)
	if !ok {
		r.t.Fatalf("no tool %s", name)
	}
	b, _ := json.Marshal(args)
	env := (&tools.Env{Agent: as, Role: role, Cwd: r.t.TempDir(), Root: r.t.TempDir(), Blobs: r.sw.deps.Blobs}).Defaults()
	res, err := tl.Run(ctx, &tools.Call{ID: "t", Name: name, Input: b, Env: env})
	if err != nil {
		r.t.Fatalf("%s: %v", name, err)
	}
	return res
}
