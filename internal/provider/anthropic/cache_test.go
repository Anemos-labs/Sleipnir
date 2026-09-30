package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/provider/anthropic"
	"github.com/reee344/sleipnir/internal/provider/mock"
)

// These tests drive the adapter against the Anthropic-dialect mock, whose
// explicit cache implements the documented rules (breakpoints, 20-position
// lookback, TTLs, minimum prefix, tiers, first-byte readability). They are the
// end-to-end check that the bodies Build produces are ones the cache can use.

func big(n int, word string) string { return strings.Repeat(word+" ", n) }

// tokensOf mirrors the mock tokenizer (four bytes per token).
func tokensOf(s string) int { return (len(s) + 3) / 4 }

func newMockClient(t *testing.T, mcfg mock.AnthropicConfig, r mock.Responder, cfg anthropic.Config) (*anthropic.Client, *mock.AnthropicServer) {
	t.Helper()
	srv := mock.NewAnthropic(mcfg, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	cfg.BaseURL = ts.URL
	if cfg.APIKey == "" {
		cfg.APIKey = testKey
	}
	if cfg.StreamIdleTimeout == 0 {
		cfg.StreamIdleTimeout = 10 * time.Second
	}
	return anthropic.New(cfg), srv
}

func bigTools(descTokens int) []core.ToolSpec {
	mk := func(name string) core.ToolSpec {
		return core.ToolSpec{Name: name, Description: big(descTokens*4/len(name+" "), name), InputSchema: json.RawMessage(bashSchema)}
	}
	return []core.ToolSpec{mk("bash"), mk("read")}
}

// layers is a kv-shaped prompt: tools, a constitution as system, and a first user
// message of pinned layers and the task.
func layers(model string, tools []core.ToolSpec, extra ...core.Message) *core.Prompt {
	p := &core.Prompt{
		Model:  model,
		Tools:  tools,
		System: []core.Block{core.Text(big(500, "constitution"))},
		Messages: []core.Message{
			user(core.Text(big(600, "shared")), core.Text(big(300, "role")), core.Text("Fix the failing test.")),
		},
		Params: core.Params{MaxTokens: 512},
	}
	p.Messages = append(p.Messages, extra...)
	return p
}

// stdMarkers places the breakpoints a planner would: tools, constitution, the
// shared layer and the rolling one on the last block.
func stdMarkers(p *core.Prompt) {
	p.Breakpoints = []core.Breakpoint{
		bp(toolsRef(), time.Hour, "tools"),
		bp(sysRef(0), time.Hour, "const"),
		bp(ref(0, 0), time.Hour, "shared"),
	}
	last := len(p.Messages) - 1
	p.Breakpoints = append(p.Breakpoints, bp(ref(last, len(p.Messages[last].Blocks)-1), 0, "thread"))
}

// ok is a responder that answers briefly, so output never dominates a test.
func ok(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} }

func do(t *testing.T, c *anthropic.Client, p *core.Prompt) *provider.Response {
	t.Helper()
	resp, err := c.Do(context.Background(), &provider.Request{Prompt: p}, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestCacheRepeatRequestReadsTheWholePrefix(t *testing.T) {
	c, srv := newMockClient(t, mock.AnthropicConfig{}, ok, anthropic.Config{Model: "mock-1"})
	p := layers("mock-1", bigTools(400))
	stdMarkers(p)
	first := do(t, c, p)
	second := do(t, c, p)

	if first.Usage.CacheReadTokens != 0 || first.Usage.CacheWriteTokens() == 0 {
		t.Fatalf("cold: %+v", first.Usage)
	}
	// Everything up to the last marker is written and nothing is left uncached.
	// The 1h markers come first, so the prefix up to the highest 1h marker is
	// billed as a 1h write and the rolling tail after it as a 5m write.
	if first.Usage.CacheWrite1hTokens == 0 || first.Usage.CacheWrite5mTokens == 0 || first.Usage.InputTokens != 0 {
		t.Fatalf("cold usage: %+v", first.Usage)
	}
	wantTail := tokensOf(big(300, "role")) + tokensOf("Fix the failing test.")
	if first.Usage.CacheWrite5mTokens != wantTail {
		t.Fatalf("the 5m tail is the blocks after the shared layer: %d, want %d", first.Usage.CacheWrite5mTokens, wantTail)
	}
	if second.Usage.CacheReadTokens != first.Usage.CacheWriteTokens() || second.Usage.CacheWriteTokens() != 0 || second.Usage.InputTokens != 0 {
		t.Fatalf("warm: %+v vs cold %+v", second.Usage, first.Usage)
	}
	if second.Usage.HitRatio() != 1 {
		t.Errorf("hit ratio = %v", second.Usage.HitRatio())
	}
	st := srv.AnthropicStats()
	if st[1].ReadTiers.Tools == 0 || st[1].ReadTiers.System == 0 || st[1].ReadTiers.Messages == 0 {
		t.Errorf("every tier is read: %+v", st[1].ReadTiers)
	}
	// The adapter's normalisation agrees with what the mock billed.
	for i, resp := range []*provider.Response{first, second} {
		s := st[i]
		if resp.Usage.CacheReadTokens != s.Read || resp.Usage.CacheWrite5mTokens != s.Write5m || resp.Usage.CacheWrite1hTokens != s.Write1h || resp.Usage.InputTokens != s.Uncached {
			t.Errorf("request %d: usage %+v, mock billed %+v", i+1, resp.Usage, s)
		}
	}
}

func TestCacheGrowthReadsTheOldMarkerAndWritesOnlyTheTail(t *testing.T) {
	c, srv := newMockClient(t, mock.AnthropicConfig{}, func(cl *mock.Call) mock.Reply {
		return mock.Reply{Text: "step " + big(20, "thinking-aloud"), ToolCalls: []mock.ToolCall{{ID: fmt.Sprint("toolu_", len(cl.Messages)), Name: "bash", Args: `{"command":"ls"}`}}}
	}, anthropic.Config{Model: "mock-1"})
	p := layers("mock-1", bigTools(300))
	var totals []int
	for round := 0; round < 6; round++ {
		stdMarkers(p)
		resp := do(t, c, p)
		totals = append(totals, resp.Usage.TotalInput())
		u := resp.Usage
		if round > 0 {
			// Everything the previous request sent was cached at its rolling
			// marker: this request reads all of it and writes only what the last
			// turn added (the assistant reply and the tool result).
			if u.CacheReadTokens != totals[round-1] || u.InputTokens != 0 {
				t.Fatalf("round %d: read %d uncached %d, want read %d", round+1, u.CacheReadTokens, u.InputTokens, totals[round-1])
			}
			if u.CacheWriteTokens() != totals[round]-totals[round-1] || u.CacheWriteTokens() == 0 {
				t.Fatalf("round %d: wrote %d, want the delta %d", round+1, u.CacheWriteTokens(), totals[round]-totals[round-1])
			}
		}
		id := resp.Turn.ToolCalls()[0].ToolID
		p.Messages = append(p.Messages,
			core.Message{Role: core.RoleAssistant, Blocks: resp.Turn.Blocks},
			user(core.ToolResult(id, false, core.Text(big(150, "output")))))
	}
	// Tools and system are read on every request after the first.
	for i, s := range srv.AnthropicStats() {
		if i > 0 && (s.ReadTiers.Tools == 0 || s.ReadTiers.System == 0) {
			t.Errorf("request %d: tiers %+v", i+1, s.ReadTiers)
		}
	}
}

func TestCacheBreakpointLimitAndLookback(t *testing.T) {
	t.Run("more than four planner markers are capped before sending", func(t *testing.T) {
		var got []anthropic.Warning
		c, srv := newMockClient(t, mock.AnthropicConfig{}, nil, anthropic.Config{Model: "mock-1", OnWarnings: func(_ *provider.Request, w []anthropic.Warning) { got = w }})
		p := layers("mock-1", bigTools(300))
		p.Breakpoints = []core.Breakpoint{
			bp(toolsRef(), time.Hour, "tools"), bp(sysRef(0), time.Hour, "const"), bp(ref(0, 0), time.Hour, "shared"),
			bp(ref(0, 1), 0, "role"), bp(ref(0, 2), 0, "thread"),
		}
		do(t, c, p) // the API would answer a 400 to five markers
		if st := srv.AnthropicStats(); st[0].Markers != 4 {
			t.Fatalf("markers = %d", st[0].Markers)
		}
		if len(got) != 1 || got[0].Code != "marker_capped" {
			t.Errorf("warnings = %v", got)
		}
	})

	// A burst of 25 appended blocks (a mail digest, a fan-in of results) pushes the
	// previous rolling entry beyond the 20-position window.
	burst := func(n int) []core.Block {
		var bs []core.Block
		for i := 0; i < n; i++ {
			bs = append(bs, core.Text(fmt.Sprintf("mail %d: %s", i, big(20, "note"))))
		}
		return bs
	}
	run := func(t *testing.T, appended int, intermediate int) provider.Response {
		c, _ := newMockClient(t, mock.AnthropicConfig{}, nil, anthropic.Config{Model: "mock-1"})
		mk := func(extra []core.Block) *core.Prompt {
			p := &core.Prompt{Model: "mock-1", Params: core.Params{MaxTokens: 64},
				System:   []core.Block{core.Text(big(500, "constitution"))},
				Messages: []core.Message{user(append([]core.Block{core.Text(big(600, "task"))}, extra...)...)}}
			last := len(p.Messages[0].Blocks) - 1
			p.Breakpoints = []core.Breakpoint{bp(sysRef(0), 0, "const"), bp(ref(0, last), 0, "thread")}
			if intermediate > 0 {
				p.Breakpoints = append(p.Breakpoints, bp(ref(0, intermediate), 0, "guard"))
			}
			return p
		}
		do(t, c, mk(nil))
		return *do(t, c, mk(burst(appended)))
	}
	sysTokens := tokensOf(big(500, "constitution"))
	taskTokens := tokensOf(big(600, "task"))

	t.Run("25 appended blocks orphan the old marker", func(t *testing.T) {
		r := run(t, 25, 0)
		// Only the system breakpoint still finds its entry: the task block's entry
		// is 26 positions back from the new rolling marker.
		if r.Usage.CacheReadTokens != sysTokens {
			t.Fatalf("read %d: expected only the system prefix (%d)", r.Usage.CacheReadTokens, sysTokens)
		}
		if r.Usage.CacheWriteTokens() < taskTokens {
			t.Fatalf("the task block is rewritten: %+v", r.Usage)
		}
	})
	t.Run("an intermediate marker inside the window saves it", func(t *testing.T) {
		r := run(t, 25, 5)
		if r.Usage.CacheReadTokens != sysTokens+taskTokens {
			t.Fatalf("read %d, want system + task (%d)", r.Usage.CacheReadTokens, sysTokens+taskTokens)
		}
		if r.Usage.CacheWriteTokens() > 30*tokensOf(burst(1)[0].Text)+50 {
			t.Fatalf("only the burst is written: %+v", r.Usage)
		}
	})
	t.Run("a run of tool_use blocks is one position", func(t *testing.T) {
		c, _ := newMockClient(t, mock.AnthropicConfig{}, nil, anthropic.Config{Model: "mock-1"})
		mk := func(n int) *core.Prompt {
			p := &core.Prompt{Model: "mock-1", Params: core.Params{MaxTokens: 64},
				Tools:    bigTools(300),
				System:   []core.Block{core.Text(big(500, "constitution"))},
				Messages: []core.Message{user(core.Text(big(600, "task")))}}
			if n > 0 {
				var uses, results []core.Block
				for i := 0; i < n; i++ {
					id := fmt.Sprint("toolu_", i)
					uses = append(uses, core.ToolUse(id, "bash", json.RawMessage(`{"command":"ls"}`)))
					results = append(results, core.ToolResult(id, false, core.Text(big(10, "ok"))))
				}
				p.Messages = append(p.Messages, core.Message{Role: core.RoleAssistant, Blocks: uses}, user(results...))
			}
			last := len(p.Messages) - 1
			p.Breakpoints = []core.Breakpoint{bp(ref(last, len(p.Messages[last].Blocks)-1), 0, "thread")}
			return p
		}
		do(t, c, mk(0))
		r := do(t, c, mk(25)) // 25 parallel tool calls, 25 results: 2 positions
		if r.Usage.CacheReadTokens < sysTokens+taskTokens {
			t.Fatalf("a large parallel tool round must not orphan the previous entry: %+v", r.Usage)
		}
	})
}

func TestCacheTTLs(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	mk := func(ttl time.Duration, tag string) *core.Prompt {
		p := &core.Prompt{Model: "mock-1", Params: core.Params{MaxTokens: 64},
			System:   []core.Block{core.Text(big(500, "constitution-"+tag))},
			Messages: []core.Message{user(core.Text("go"))}}
		p.Breakpoints = []core.Breakpoint{bp(sysRef(0), ttl, "const")}
		return p
	}
	mcfg := mock.AnthropicConfig{}
	mcfg.Now = clock

	t.Run("a 5m entry expires, and a read refreshes it", func(t *testing.T) {
		c, _ := newMockClient(t, mcfg, nil, anthropic.Config{Model: "mock-1"})
		w := do(t, c, mk(0, "a"))
		if w.Usage.CacheWrite5mTokens == 0 || w.Usage.CacheWrite1hTokens != 0 {
			t.Fatalf("a marker without a TTL writes at 5m: %+v", w.Usage)
		}
		advance(4 * time.Minute)
		if r := do(t, c, mk(0, "a")); r.Usage.CacheReadTokens != w.Usage.CacheWrite5mTokens {
			t.Fatalf("at 4m: %+v", r.Usage)
		}
		advance(4 * time.Minute) // 8m after the write, 4m after the refresh
		if r := do(t, c, mk(0, "a")); r.Usage.CacheReadTokens == 0 {
			t.Fatalf("the read at 4m must have refreshed the entry: %+v", r.Usage)
		}
		advance(6 * time.Minute)
		if r := do(t, c, mk(0, "a")); r.Usage.CacheReadTokens != 0 || r.Usage.CacheWrite5mTokens == 0 {
			t.Fatalf("after 6 idle minutes: %+v", r.Usage)
		}
	})
	t.Run("a 1h entry survives, and is billed as a 1h write", func(t *testing.T) {
		c, _ := newMockClient(t, mcfg, nil, anthropic.Config{Model: "mock-1"})
		w := do(t, c, mk(time.Hour, "b"))
		if w.Usage.CacheWrite1hTokens == 0 || w.Usage.CacheWrite5mTokens != 0 {
			t.Fatalf("a >= 1h TTL renders ttl 1h: %+v", w.Usage)
		}
		advance(30 * time.Minute)
		if r := do(t, c, mk(time.Hour, "b")); r.Usage.CacheReadTokens != w.Usage.CacheWrite1hTokens {
			t.Fatalf("at 30m: %+v", r.Usage)
		}
		advance(61 * time.Minute)
		if r := do(t, c, mk(time.Hour, "b")); r.Usage.CacheReadTokens != 0 || r.Usage.CacheWrite1hTokens == 0 {
			t.Fatalf("after an idle hour: %+v", r.Usage)
		}
	})
	t.Run("59 minutes is still the default lifetime", func(t *testing.T) {
		c, _ := newMockClient(t, mcfg, nil, anthropic.Config{Model: "mock-1"})
		w := do(t, c, mk(59*time.Minute, "c"))
		if w.Usage.CacheWrite5mTokens == 0 || w.Usage.CacheWrite1hTokens != 0 {
			t.Fatalf("%+v", w.Usage)
		}
	})
}

func TestCacheStampedeOverAColdPrefix(t *testing.T) {
	mcfg := mock.AnthropicConfig{}
	mcfg.FirstToken = 150 * time.Millisecond
	mcfg.DecodePer = 15 * time.Millisecond
	slow := func(*mock.Call) mock.Reply { return mock.Reply{Text: big(30, "long-answer")} }
	prompt := func(task string) *core.Prompt {
		p := layers("mock-1", bigTools(300), user(core.Text(task)))
		p.Messages = p.Messages[:1]
		p.Messages[0].Blocks = append(p.Messages[0].Blocks[:2:2], core.Text(task))
		p.Breakpoints = []core.Breakpoint{bp(sysRef(0), time.Hour, "const"), bp(ref(0, 1), time.Hour, "shared")}
		return p
	}

	t.Run("two concurrent requests both write; one after the first byte reads", func(t *testing.T) {
		c, srv := newMockClient(t, mcfg, slow, anthropic.Config{Model: "mock-1"})
		var wg sync.WaitGroup
		var mu sync.Mutex
		var usage []core.Usage
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := do(t, c, prompt(fmt.Sprint("task ", i)))
				mu.Lock()
				usage = append(usage, r.Usage)
				mu.Unlock()
			}(i)
		}
		wg.Wait()
		if usage[0].CacheReadTokens != 0 || usage[1].CacheReadTokens != 0 || usage[0].CacheWriteTokens() == 0 || usage[1].CacheWriteTokens() == 0 {
			t.Fatalf("the stampede pays twice: %+v", usage)
		}
		third := do(t, c, prompt("task 3"))
		if third.Usage.CacheReadTokens != usage[0].CacheWriteTokens() || third.Usage.CacheWriteTokens() != 0 {
			t.Fatalf("after the first byte: %+v", third.Usage)
		}
		if n := len(srv.AnthropicStats()); n != 3 {
			t.Errorf("stats %d", n)
		}
	})

	t.Run("a request launched at the first streamed byte reads the entry", func(t *testing.T) {
		// This is the contract the swarm's warm gate relies on: EvStart from the
		// adapter means "entries written by this request are now readable".
		c, _ := newMockClient(t, mcfg, slow, anthropic.Config{Model: "mock-1"})
		started := make(chan struct{})
		var once sync.Once
		primerDone := make(chan *provider.Response, 1)
		go func() {
			r, err := c.Do(context.Background(), &provider.Request{Prompt: prompt("primer")}, func(e provider.Event) {
				if e.Kind == provider.EvStart {
					once.Do(func() { close(started) })
				}
			})
			if err != nil {
				t.Error(err)
			}
			primerDone <- r
		}()
		<-started
		follower := do(t, c, prompt("follower"))
		primer := <-primerDone
		if follower.Usage.CacheReadTokens != primer.Usage.CacheWriteTokens() || follower.Usage.CacheReadTokens == 0 {
			t.Fatalf("follower %+v primer %+v", follower.Usage, primer.Usage)
		}
		if follower.Total >= primer.Total {
			t.Errorf("the follower was released while the primer was still streaming: %v vs %v", follower.Total, primer.Total)
		}
	})

	t.Run("a request launched before the first byte does not", func(t *testing.T) {
		c, _ := newMockClient(t, mcfg, slow, anthropic.Config{Model: "mock-1"})
		done := make(chan *provider.Response, 1)
		go func() { done <- do(t, c, prompt("primer")) }()
		time.Sleep(30 * time.Millisecond) // well inside the primer's 150ms first-token delay
		early := do(t, c, prompt("early"))
		<-done
		if early.Usage.CacheReadTokens != 0 {
			t.Fatalf("entries are not readable before the first byte: %+v", early.Usage)
		}
	})
}

func TestCacheToolChoiceKeepsToolsAndSystemButNotMessages(t *testing.T) {
	var choices []string
	c, srv := newMockClient(t, mock.AnthropicConfig{}, func(cl *mock.Call) mock.Reply {
		if raw, ok := cl.Raw["tool_choice"]; ok {
			choices = append(choices, string(raw))
		} else {
			choices = append(choices, "")
		}
		return mock.Reply{Text: "ok"}
	}, anthropic.Config{Model: "mock-1"})
	p := layers("mock-1", bigTools(500))
	stdMarkers(p)

	do(t, c, p)
	same := do(t, c, p)

	// A compactor-style fork asks for tool_choice none.
	forked := *p
	forked.Params.ToolChoice = "none"
	fork := do(t, c, &forked)

	// Back to the original: the entries were bypassed, not destroyed.
	back := do(t, c, p)
	st := srv.AnthropicStats()

	if same.Usage.CacheReadTokens == 0 || st[1].ReadTiers.Messages == 0 {
		t.Fatalf("control: %+v", st[1].ReadTiers)
	}
	if st[2].ReadTiers.Tools == 0 || st[2].ReadTiers.System == 0 {
		t.Errorf("tools and system survive a tool_choice change: %+v", st[2].ReadTiers)
	}
	if st[2].ReadTiers.Messages != 0 || fork.Usage.CacheWriteTokens() == 0 {
		t.Errorf("the messages tier does not: read %+v, wrote %+v", st[2].ReadTiers, fork.Usage)
	}
	if st[3].ReadTiers.Messages == 0 || back.Usage.CacheReadTokens != same.Usage.CacheReadTokens {
		t.Errorf("flipping back hits the original entries: %+v", st[3].ReadTiers)
	}
	// The adapter never invents tool_choice: only the request that asked for it carries one.
	if strings.Join(choices, "|") != `||{"type":"none"}|` {
		t.Errorf("tool_choice on the wire: %q", choices)
	}
}

func TestCachePrewarm(t *testing.T) {
	var raws []map[string]json.RawMessage
	c, srv := newMockClient(t, mock.AnthropicConfig{}, func(cl *mock.Call) mock.Reply { raws = append(raws, cl.Raw); return mock.Reply{Text: "real"} }, anthropic.Config{Model: "mock-1"})
	warmPrompt := layers("mock-1", bigTools(300))
	warmPrompt.Messages[0].Blocks = warmPrompt.Messages[0].Blocks[:2]
	warmPrompt.Messages = append(warmPrompt.Messages[:1], user(core.Text("placeholder")))
	warmPrompt.Messages[0].Blocks = warmPrompt.Messages[0].Blocks[:2]
	warmPrompt.Breakpoints = []core.Breakpoint{bp(toolsRef(), time.Hour, "tools"), bp(sysRef(0), time.Hour, "const"), bp(ref(0, 1), time.Hour, "shared")}

	var evs recorder
	w, err := c.Do(context.Background(), &provider.Request{Prompt: warmPrompt, Warm: true}, evs.on)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Turn.Blocks) != 0 || w.Stop != core.StopMaxTokens || w.Usage.OutputTokens != 0 || w.Usage.CacheWrite1hTokens == 0 {
		t.Fatalf("a pre-warm writes and generates nothing: %+v", w)
	}
	if evs.count(provider.EvStart) != 1 {
		t.Errorf("events = %v", evs.kinds())
	}
	st := srv.AnthropicStats()
	if st[0].Streamed || st[0].Completion != 0 || len(raws) != 1 || string(raws[0]["max_tokens"]) != "0" || raws[0]["stream"] != nil {
		t.Errorf("the mock saw max_tokens=%s stream=%s", raws[0]["max_tokens"], raws[0]["stream"])
	}

	// The real request shares the warmed prefix and reads exactly what the warm-up wrote.
	real := layers("mock-1", bigTools(300))
	real.Breakpoints = warmPrompt.Breakpoints
	r := do(t, c, real)
	if r.Usage.CacheReadTokens != w.Usage.CacheWrite1hTokens {
		t.Fatalf("real request read %d, warm-up wrote %d", r.Usage.CacheReadTokens, w.Usage.CacheWrite1hTokens)
	}

	t.Run("an endpoint without zero-token warm-ups gets one token", func(t *testing.T) {
		prof := anthropic.DefaultProfile("x", "", "mock-1")
		prof.PrewarmZeroTokens = false
		var raw map[string]json.RawMessage
		c, _ := newMockClient(t, mock.AnthropicConfig{}, func(cl *mock.Call) mock.Reply { raw = cl.Raw; return mock.Reply{Text: "x"} }, anthropic.Config{Profile: &prof})
		if _, err := c.Do(context.Background(), &provider.Request{Prompt: warmPrompt, Warm: true}, nil); err != nil {
			t.Fatal(err)
		}
		if string(raw["max_tokens"]) != "1" || raw["stream"] != nil {
			t.Errorf("max_tokens=%s stream=%s", raw["max_tokens"], raw["stream"])
		}
	})
}

func TestBelowTheMinimumPrefixNothingIsCached(t *testing.T) {
	c, srv := newMockClient(t, mock.AnthropicConfig{}, nil, anthropic.Config{Model: "claude-haiku-4-5"})
	p := &core.Prompt{Model: "claude-haiku-4-5", Params: core.Params{MaxTokens: 64},
		System:   []core.Block{core.Text(big(600, "constitution"))}, // ~1.2k tokens: over most minimums, under Haiku's 4096
		Messages: []core.Message{user(core.Text("hi"))}}
	p.Breakpoints = []core.Breakpoint{bp(sysRef(0), 0, "const")}
	if c.Profile().Cache.MinPrefixTokens != 4096 {
		t.Fatalf("profile minimum = %d", c.Profile().Cache.MinPrefixTokens)
	}
	do(t, c, p)
	r := do(t, c, p)
	st := srv.AnthropicStats()
	if r.Usage.CacheReadTokens != 0 || r.Usage.CacheWriteTokens() != 0 || len(st[0].Skipped) != 1 {
		t.Fatalf("%+v %+v", r.Usage, st[0])
	}
}

// ---------------------------------------------------------------------------
// preserved thinking

// loopAgent drives an agent-like tool loop through the adapter with a chosen way
// of delivering the per-turn hot block.
type loopAgent struct {
	t      *testing.T
	c      *anthropic.Client
	model  string
	hot    string // "clear_at", "inline" or ""
	mode   string // Request.BindingMode
	thread []core.Message
	rounds int
	usage  []core.Usage
	hotTok []int
	resps  []*provider.Response
}

func newLoop(t *testing.T, mcfg mock.AnthropicConfig, cfg anthropic.Config, model, hot, mode string) *loopAgent {
	t.Helper()
	c, _ := newMockClient(t, mcfg, func(cl *mock.Call) mock.Reply {
		return mock.Reply{Reasoning: fmt.Sprintf("reasoning for turn %d: %s", len(cl.Messages), big(10, "hmm")),
			ToolCalls: []mock.ToolCall{{ID: fmt.Sprint("toolu_", len(cl.Messages)), Name: "bash", Args: `{"command":"ls"}`}}}
	}, cfg)
	return &loopAgent{t: t, c: c, model: model, hot: hot, mode: mode, thread: []core.Message{user(core.Text(big(700, "task")))}}
}

func (a *loopAgent) hotText(round int) string {
	return fmt.Sprintf("board view %d: %s", round, big(150, "tasks"))
}

// step renders and sends the next request the way kv would: the persistent thread
// plus, for "inline", a fresh hot block appended to the last user message, or,
// for "clear_at", the hot messages already persisted in the thread.
func (a *loopAgent) step() (*provider.Response, error) {
	a.t.Helper()
	msgs := append([]core.Message(nil), a.thread...)
	rolling := len(msgs) - 1
	rollingBlk := len(msgs[rolling].Blocks) - 1
	if a.hot == "inline" {
		last := msgs[rolling]
		blocks := append(append([]core.Block(nil), last.Blocks...), core.Text(a.hotText(a.rounds+1)))
		blocks[len(blocks)-1].Ephemeral = true
		msgs[rolling] = core.Message{Role: last.Role, Blocks: blocks}
	}
	p := &core.Prompt{
		Model:    a.model,
		Tools:    bigTools(400),
		System:   []core.Block{core.Text(big(500, "constitution"))},
		Messages: msgs,
		Params:   core.Params{MaxTokens: 1024, Thinking: "adaptive"},
		Breakpoints: []core.Breakpoint{
			bp(sysRef(0), time.Hour, "const"),
			bp(ref(rolling, rollingBlk), 0, "thread"),
		},
	}
	if a.hot == "clear_at" {
		rolling = len(msgs) - 1
		p.Breakpoints[1] = bp(ref(rolling, len(msgs[rolling].Blocks)-1), 0, "thread") // may be the hot block itself
	}
	resp, err := a.c.Do(context.Background(), &provider.Request{Prompt: p, BindingMode: a.mode}, nil)
	if err != nil {
		return nil, err
	}
	a.rounds++
	a.usage = append(a.usage, resp.Usage)
	a.resps = append(a.resps, resp)
	calls := resp.Turn.ToolCalls()
	if len(calls) == 0 {
		return resp, nil
	}
	a.thread = append(a.thread, core.Message{Role: core.RoleAssistant, Blocks: resp.Turn.Blocks})
	results := user(core.ToolResult(calls[0].ToolID, false, core.Text(big(150, fmt.Sprint("output", a.rounds)))))
	a.thread = append(a.thread, results)
	if a.hot == "clear_at" {
		text := a.hotText(a.rounds + 1)
		a.hotTok = append(a.hotTok, tokensOf(text))
		a.thread = append(a.thread, core.Message{Role: core.RoleSystem, ClearAt: "next_user_message", Blocks: []core.Block{core.Text(text)}})
	}
	return resp, nil
}

var enforced = mock.AnthropicConfig{EnforceThinkingBinding: true}

func TestPreservedThinkingAppendOnlyLoopWithTurnScopedHotBlocks(t *testing.T) {
	a := newLoop(t, enforced, anthropic.Config{Model: "claude-opus-5-5"}, "claude-opus-5-5", "clear_at", "")
	for i := 0; i < 8; i++ {
		if _, err := a.step(); err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
	}
	for i, r := range a.resps {
		if len(r.Turn.Blocks) == 0 || r.Turn.Blocks[0].Kind != core.BlockThinking || len(r.Turn.Blocks[0].Wire) == 0 {
			t.Fatalf("round %d: the response must carry a replayable thinking block: %+v", i+1, r.Turn.Blocks)
		}
	}
	// The hot block costs nothing once it clears and never disturbs the cache:
	// each request reads everything the previous one sent except that request's
	// (then still active) hot text, which was after the rolling marker.
	for i := 1; i < len(a.usage); i++ {
		prev := a.usage[i-1].TotalInput()
		wantRead := prev - a.hotTok[i-1]
		// The previous request's own hot block was not in it: the hot block a
		// round appends is sent for the first time with the NEXT request.
		if i == 1 {
			wantRead = prev
		}
		got := a.usage[i]
		if got.CacheReadTokens < wantRead-a.hotTok[i-1]-2 || got.CacheReadTokens > prev {
			t.Errorf("round %d: read %d, previous total %d", i+1, got.CacheReadTokens, prev)
		}
		if got.HitRatio() < 0.5 {
			t.Errorf("round %d: hit ratio %.2f", i+1, got.HitRatio())
		}
	}
}

func TestPreservedThinkingInlineHotTailIsRejectedOnRoundTwo(t *testing.T) {
	a := newLoop(t, enforced, anthropic.Config{Model: "claude-opus-5-5"}, "claude-opus-5-5", "inline", "")
	if _, err := a.step(); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	_, err := a.step()
	pe := wantKind(t, err, provider.ErrThinkingBinding, false)
	if pe.Status != 400 || !strings.Contains(pe.Message, "messages.1.content.0") || !strings.Contains(pe.Message, "bound to a different conversation") {
		t.Errorf("error = %v", pe)
	}
}

func TestPreservedThinkingInlineHotTailWithDropBlockDegradesInstead(t *testing.T) {
	a := newLoop(t, enforced, anthropic.Config{Model: "claude-opus-5-5"}, "claude-opus-5-5", "inline", "drop_block")
	for i := 0; i < 4; i++ {
		if _, err := a.step(); err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
	}
	// Round 1 has nothing to drop; from round 2 the rewritten tail makes every
	// earlier thinking block stale, the API drops them, and the adapter reports it.
	if len(a.resps[0].Transformations) != 0 {
		t.Errorf("round 1: %+v", a.resps[0].Transformations)
	}
	for i := 1; i < 4; i++ {
		ts := a.resps[i].Transformations
		if len(ts) == 0 || ts[0].Type != "thinking_dropped" || ts[0].Reason != "prefix_binding_mismatch" || !strings.HasPrefix(ts[0].Path, "messages.") {
			t.Errorf("round %d transformations = %+v", i+1, ts)
		}
	}
	// The cost of the pattern: the rewritten tail is never cached.
	for i := 1; i < 4; i++ {
		if a.usage[i].InputTokens == 0 {
			t.Errorf("round %d: expected uncached input from the inline hot block", i+1)
		}
	}
}

func TestPreservedThinkingFoldedHotBlocksAreAppendOnlyToo(t *testing.T) {
	// The documented fallback for endpoints without turn-scoped system messages:
	// the hot text becomes a trailing text block of the tool_result message and
	// stays there. It piles up in the prefix, but every earlier byte is stable.
	prof := anthropic.DefaultProfile("x", "", "claude-opus-5-5")
	prof.TurnScopedSystem = false
	a := newLoop(t, enforced, anthropic.Config{Profile: &prof}, "claude-opus-5-5", "clear_at", "")
	for i := 0; i < 8; i++ {
		if _, err := a.step(); err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
	}
	// The rolling marker rides on the folded text, so the cache keeps everything.
	for i := 1; i < len(a.usage); i++ {
		if a.usage[i].CacheReadTokens < a.usage[i-1].TotalInput()-2 {
			t.Errorf("round %d: read %d of the previous %d", i+1, a.usage[i].CacheReadTokens, a.usage[i-1].TotalInput())
		}
	}
}

func TestPreservedThinkingWithoutTheCheckNothingBreaks(t *testing.T) {
	// The same inline pattern on a model that does not run the check: no error
	// (the bug is silent there, and only costs cache and reasoning).
	a := newLoop(t, mock.AnthropicConfig{}, anthropic.Config{Model: "mock-1"}, "mock-1", "inline", "")
	for i := 0; i < 3; i++ {
		if _, err := a.step(); err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
	}
}
