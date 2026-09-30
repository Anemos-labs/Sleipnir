// Package probe measures how an endpoint really behaves.
//
// Gateways differ in what they pass through, and engines differ in how they
// cache: block size, minimum prefix, whether parallel requests can share a
// prefix that is still being prefilled, whether reasoning blocks round-trip.
// Rather than assuming, `sleipnir doctor` runs a short battery of requests and
// turns the results into a Profile refinement plus a human-readable report.
package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

// Config configures a probe run.
type Config struct {
	Provider provider.Provider
	Model    string
	// Deep also measures cache granularity, minimum prefix and warm-up needs.
	Deep bool
	// Headers returns the most recent response headers, for rate-limit info.
	Headers func() http.Header
	// Log receives progress lines.
	Log func(string)
	// CacheKey enables affinity in requests (as the harness would send it).
	CacheKey bool
	// Capture also checks token capture: whether the endpoint returns prompt and
	// completion token ids and logprobs, and whether prompt ids are prefix-stable
	// across an append-only conversation (what RL training data relies on to pack
	// a segment into one sequence). The provider's profile must allow capture.
	Capture bool
}

// Step is one probe request.
type Step struct {
	Name   string        `json:"name"`
	OK     bool          `json:"ok"`
	Detail string        `json:"detail,omitempty"`
	Took   time.Duration `json:"took_ns"`
	Usage  core.Usage    `json:"usage"`
	Cost   *float64      `json:"cost_usd,omitempty"`
}

// Findings are the conclusions the harness acts on.
type Findings struct {
	Streaming            bool     `json:"streaming"`
	Tools                bool     `json:"tools"`
	ToolRoundTrip        bool     `json:"tool_round_trip"`
	UsageReported        bool     `json:"usage_reported"`
	CostReported         bool     `json:"cost_reported"`
	CachedTokensReported bool     `json:"cached_tokens_reported"`
	CacheWorks           bool     `json:"cache_works"`
	CacheHitRatio        float64  `json:"cache_hit_ratio"`
	CacheGranularity     int      `json:"cache_granularity"`
	MinCachePrefix       int      `json:"min_cache_prefix"`
	WarmupNeeded         *bool    `json:"warmup_needed,omitempty"`
	ReasoningSeen        bool     `json:"reasoning_seen"`
	ReasoningDetails     bool     `json:"reasoning_details"`
	TokenIDs             bool     `json:"token_ids,omitempty"`
	TokenLogprobs        bool     `json:"token_logprobs,omitempty"`
	TokenPrefixStable    bool     `json:"token_prefix_stable,omitempty"`
	RateLimit            string   `json:"rate_limit,omitempty"`
	BytesPerToken        float64  `json:"bytes_per_token"`
	TTFB                 string   `json:"ttfb"`
	Notes                []string `json:"notes,omitempty"`
}

// Report is the full result.
type Report struct {
	Model    string    `json:"model"`
	At       time.Time `json:"at"`
	Steps    []Step    `json:"steps"`
	Findings Findings  `json:"findings"`
	TotalUSD float64   `json:"total_usd"`
}

type runner struct {
	cfg   Config
	rep   *Report
	nonce string
	key   string
}

// Run executes the probe.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if cfg.Log == nil {
		cfg.Log = func(string) {}
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	r := &runner{cfg: cfg, rep: &Report{Model: cfg.Model, At: time.Now().UTC()}, nonce: hex.EncodeToString(b)}
	if cfg.CacheKey {
		r.key = "probe-" + r.nonce
	}
	steps := []func(context.Context) error{r.basic, r.tools, r.cache, r.reasoning}
	if cfg.Deep {
		steps = append(steps, r.minPrefix, r.warmup)
	}
	if cfg.Capture {
		steps = append(steps, r.capture)
	}
	for _, s := range steps {
		if err := s(ctx); err != nil && ctx.Err() != nil {
			return r.rep, ctx.Err()
		}
	}
	if cfg.Headers != nil {
		if h := cfg.Headers(); h != nil && h.Get("X-RateLimit-Limit") != "" {
			r.rep.Findings.RateLimit = h.Get("X-RateLimit-Limit") + "/min (remaining " + h.Get("X-RateLimit-Remaining") + ")"
		}
	}
	return r.rep, nil
}

func (r *runner) prompt(system string, msgs ...core.Message) *core.Prompt {
	p := &core.Prompt{Model: r.cfg.Model, Params: core.Params{MaxTokens: 256}, CacheKey: r.key}
	if system != "" {
		p.System = []core.Block{core.Text(system)}
	}
	p.Messages = msgs
	return p
}

func user(s string) core.Message {
	return core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text(s)}}
}

// do runs one request and records a step.
func (r *runner) do(ctx context.Context, name string, p *core.Prompt, noStream bool) (*provider.Response, error) {
	start := time.Now()
	resp, err := r.cfg.Provider.Do(ctx, &provider.Request{Prompt: p, Label: "probe:" + name, NoStream: noStream}, nil)
	st := Step{Name: name, Took: time.Since(start)}
	if err != nil {
		st.Detail = err.Error()
		r.rep.Steps = append(r.rep.Steps, st)
		r.cfg.Log(fmt.Sprintf("  ✗ %-14s %v", name, err))
		return nil, err
	}
	st.OK, st.Usage, st.Cost = true, resp.Usage, resp.CostUSD
	if resp.CostUSD != nil {
		r.rep.TotalUSD += *resp.CostUSD
	}
	r.rep.Steps = append(r.rep.Steps, st)
	r.cfg.Log(fmt.Sprintf("  ✓ %-14s %5dms  in=%d cached=%d out=%d", name, st.Took.Milliseconds(), resp.Usage.TotalInput(), resp.Usage.CacheReadTokens, resp.Usage.OutputTokens))
	return resp, nil
}

func (r *runner) note(s string) { r.rep.Findings.Notes = append(r.rep.Findings.Notes, s) }

func (r *runner) basic(ctx context.Context) error {
	r.cfg.Log("basic")
	first := time.Time{}
	start := time.Now()
	resp, err := r.cfg.Provider.Do(ctx, &provider.Request{Prompt: r.prompt("", user("Reply with the single word: pong"))}, func(e provider.Event) {
		if e.Kind == provider.EvStart && first.IsZero() {
			first = time.Now()
		}
	})
	st := Step{Name: "basic", Took: time.Since(start)}
	if err != nil {
		st.Detail = err.Error()
		r.rep.Steps = append(r.rep.Steps, st)
		r.cfg.Log("  ✗ basic          " + err.Error())
		return err
	}
	st.OK, st.Usage, st.Cost = true, resp.Usage, resp.CostUSD
	r.rep.Steps = append(r.rep.Steps, st)
	f := &r.rep.Findings
	f.Streaming = !first.IsZero()
	f.UsageReported = resp.Usage.TotalInput() > 0 && resp.Usage.OutputTokens > 0
	f.CostReported = resp.CostUSD != nil
	if resp.CostUSD != nil {
		r.rep.TotalUSD += *resp.CostUSD
	}
	if !first.IsZero() {
		f.TTFB = first.Sub(start).Round(time.Millisecond).String()
	}
	if strings.TrimSpace(resp.Turn.PlainText()) == "" {
		r.note("basic reply was empty (reasoning models may need a larger max_tokens)")
	}
	r.cfg.Log(fmt.Sprintf("  ✓ basic          %5dms  in=%d out=%d cost=%v", st.Took.Milliseconds(), resp.Usage.TotalInput(), resp.Usage.OutputTokens, resp.CostUSD != nil))
	return nil
}

const weatherSchema = `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`

func (r *runner) tools(ctx context.Context) error {
	r.cfg.Log("tools")
	tool := core.ToolSpec{Name: "get_weather", Description: "Get the current weather for a city.", InputSchema: json.RawMessage(weatherSchema)}
	p := r.prompt("You must use tools when they apply.", user("What is the weather in Paris right now? Use the get_weather tool."))
	p.Tools = []core.ToolSpec{tool}
	p.Params.MaxTokens = 1024
	resp, err := r.do(ctx, "tools", p, false)
	if err != nil {
		return err
	}
	calls := resp.Turn.ToolCalls()
	if len(calls) == 0 {
		r.note("model did not call the offered tool")
		return nil
	}
	r.rep.Findings.Tools = true
	p2 := r.prompt("You must use tools when they apply.",
		user("What is the weather in Paris right now? Use the get_weather tool."),
		core.Message{Role: core.RoleAssistant, Blocks: resp.Turn.Blocks},
		core.Message{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult(calls[0].ToolID, false, core.Text("18C, light rain"))}},
	)
	p2.Tools = []core.ToolSpec{tool}
	p2.Params.MaxTokens = 1024
	resp2, err := r.do(ctx, "tool-result", p2, false)
	if err != nil {
		r.note("tool result round trip failed: " + err.Error())
		return err
	}
	r.rep.Findings.ToolRoundTrip = strings.TrimSpace(resp2.Turn.PlainText()) != ""
	return nil
}

// filler returns deterministic pseudo-source text of roughly n bytes, unique to
// the nonce so earlier probe runs cannot pre-warm the cache.
func filler(nonce string, n int) string {
	words := []string{"handler", "request", "context", "session", "config", "parser", "buffer", "stream", "router", "schema", "commit", "branch", "module", "import", "return", "value", "error", "state", "queue", "index"}
	var sb strings.Builder
	sb.WriteString("Project notes " + nonce + "\n")
	x := uint32(2166136261)
	for _, c := range nonce {
		x = (x ^ uint32(c)) * 16777619
	}
	line := 0
	for sb.Len() < n {
		line++
		fmt.Fprintf(&sb, "%04d func %s_%s(%s %s) %s { // %s\n", line,
			words[x%20], words[(x>>5)%20], words[(x>>10)%20], words[(x>>15)%20], words[(x>>20)%20], words[(x>>25)%20])
		x = x*1664525 + 1013904223
	}
	return sb.String()
}

func (r *runner) cache(ctx context.Context) error {
	r.cfg.Log("cache")
	sys := filler(r.nonce, 12000) // ~3k tokens
	p1 := r.prompt(sys, user("Say ok. #1"))
	a, err := r.do(ctx, "cache-cold", p1, false)
	if err != nil {
		return err
	}
	r.rep.Findings.BytesPerToken = float64(len(sys)) / float64(max(a.Usage.TotalInput(), 1))
	p2 := r.prompt(sys, user("Say ok. #2"))
	b, err := r.do(ctx, "cache-warm", p2, false)
	if err != nil {
		return err
	}
	f := &r.rep.Findings
	f.CachedTokensReported = b.Usage.CacheReadTokens > 0
	if b.Usage.TotalInput() > 0 {
		f.CacheHitRatio = float64(b.Usage.CacheReadTokens) / float64(b.Usage.TotalInput())
	}
	f.CacheWorks = f.CacheHitRatio >= 0.5
	if !f.CacheWorks {
		r.note("second identical-prefix request did not hit the cache; affinity, engine or minimum size may be the cause")
	}
	// Granularity: send a series of prompts, each extending the previous system
	// text by an uneven amount. Each request's reads then cover the previous
	// prompt rounded down to a whole cache block, so the reported counts are
	// different multiples of the block size and their gcd reveals it.
	var reads []int
	text := sys + filler(r.nonce+"g", 4000)
	cur := len(sys)
	for _, inc := range []int{70, 130, 197, 310, 89, 421, 155, 263} {
		cur += inc
		p := r.prompt(text[:cur], user("Say ok."))
		g, err := r.do(ctx, fmt.Sprintf("cache-grow-%d", inc), p, false)
		if err != nil {
			break
		}
		if g.Usage.CacheReadTokens > 0 {
			reads = append(reads, g.Usage.CacheReadTokens)
		}
	}
	if g := granularity(reads); g > 1 && g <= 512 {
		f.CacheGranularity = g
	}
	return nil
}

// granularity is the gcd of the differences between reported read counts. Raw
// counts can share a large accidental factor; their differences only share the
// true block size.
func granularity(vals []int) int {
	if len(vals) < 3 {
		return 0
	}
	g := 0
	for _, v := range vals[1:] {
		d := v - vals[0]
		if d < 0 {
			d = -d
		}
		g = gcd(g, d)
	}
	return g
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func (r *runner) reasoning(ctx context.Context) error {
	r.cfg.Log("reasoning")
	p := r.prompt("", user("What is 17 * 23? Think it through."))
	p.Params.Effort = "low"
	p.Params.MaxTokens = 1024
	resp, err := r.do(ctx, "reasoning", p, false)
	if err != nil {
		r.note("reasoning_effort request failed: " + err.Error())
		return nil
	}
	for _, b := range resp.Turn.Blocks {
		if b.Kind == core.BlockThinking {
			r.rep.Findings.ReasoningSeen = true
			if len(b.Wire) > 0 {
				r.rep.Findings.ReasoningDetails = true
			}
		}
	}
	return nil
}

// capture checks token-id capture and the prefix-stability that segment packing
// needs: prompt ids of an append-only follow-up must start with the previous
// prompt ids followed by the sampled completion ids.
func (r *runner) capture(ctx context.Context) error {
	r.cfg.Log("token capture")
	ask := func(name string, p *core.Prompt) (*provider.Response, error) {
		start := time.Now()
		resp, err := r.cfg.Provider.Do(ctx, &provider.Request{Prompt: p, Label: "probe:" + name, Capture: true}, nil)
		st := Step{Name: name, Took: time.Since(start)}
		if err != nil {
			st.Detail = err.Error()
		} else {
			st.OK, st.Usage, st.Cost = true, resp.Usage, resp.CostUSD
			if resp.CostUSD != nil {
				r.rep.TotalUSD += *resp.CostUSD
			}
		}
		r.rep.Steps = append(r.rep.Steps, st)
		return resp, err
	}
	p1 := r.prompt("You are terse.", user("Reply with exactly the words: one two three."))
	r1, err := ask("capture-1", p1)
	if err != nil {
		r.note("token capture request failed: " + err.Error())
		return nil
	}
	tr := r1.Tokens
	if tr == nil || len(tr.CompletionIDs) == 0 {
		r.note("endpoint returned no token ids (needs a vLLM/SGLang-style server started with token-id support; profile.CaptureTokens must be on)")
		return nil
	}
	r.rep.Findings.TokenIDs = len(tr.PromptIDs) > 0
	r.rep.Findings.TokenLogprobs = len(tr.Logprobs) == len(tr.CompletionIDs)
	p2 := r.prompt("You are terse.", user("Reply with exactly the words: one two three."),
		core.Message{Role: core.RoleAssistant, Blocks: r1.Turn.Blocks},
		user("Now reply with exactly the words: four five six."))
	r2, err := ask("capture-2", p2)
	if err != nil || r2.Tokens == nil {
		r.note("second capture request returned no token ids")
		return nil
	}
	a, b, c := tr.PromptIDs, r2.Tokens.PromptIDs, tr.CompletionIDs
	stable := len(a) > 0 && len(b) >= len(a)+len(c)
	for i := 0; stable && i < len(a); i++ {
		stable = a[i] == b[i]
	}
	for i := 0; stable && i < len(c); i++ {
		stable = c[i] == b[len(a)+i]
	}
	r.rep.Findings.TokenPrefixStable = stable
	if !stable {
		r.note("prompt ids are not an extension of previous prompt+completion ids (chat template re-renders the assistant turn): segments will be exported per step, not packed")
	}
	return nil
}

// minPrefix finds the smallest prompt size at which a repeat request hits.
func (r *runner) minPrefix(ctx context.Context) error {
	r.cfg.Log("min-prefix")
	for _, bytes := range []int{300, 600, 1200, 2400, 4800} {
		nonce := r.nonce + strconv.Itoa(bytes)
		sys := filler(nonce, bytes)
		if _, err := r.do(ctx, fmt.Sprintf("minp-%d-a", bytes), r.prompt(sys, user("ok")), false); err != nil {
			return nil
		}
		b, err := r.do(ctx, fmt.Sprintf("minp-%d-b", bytes), r.prompt(sys, user("ok")), false)
		if err != nil {
			return nil
		}
		if b.Usage.CacheReadTokens > 0 {
			r.rep.Findings.MinCachePrefix = b.Usage.CacheReadTokens
			return nil
		}
	}
	r.note("no cache hits at any tested prefix size up to ~1.2k tokens")
	return nil
}

// warmup checks whether a parallel burst over a cold prefix shares anything.
func (r *runner) warmup(ctx context.Context) error {
	r.cfg.Log("warm-up")
	sys := filler(r.nonce+"w", 12000)
	burst := func(tag string) (cached, total int) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				resp, err := r.cfg.Provider.Do(ctx, &provider.Request{Prompt: r.prompt(sys, user(fmt.Sprintf("Say ok. %s%d", tag, i)))}, nil)
				if err != nil {
					return
				}
				mu.Lock()
				cached += resp.Usage.CacheReadTokens
				total += resp.Usage.TotalInput()
				mu.Unlock()
			}(i)
		}
		wg.Wait()
		return
	}
	coldHit, coldTot := burst("cold")
	warmHit, warmTot := burst("warm")
	needed := coldTot > 0 && float64(coldHit)/float64(coldTot) < 0.25 && warmTot > 0 && float64(warmHit)/float64(warmTot) > 0.5
	r.rep.Findings.WarmupNeeded = &needed
	r.cfg.Log(fmt.Sprintf("  cold burst hit %d/%d, warm burst hit %d/%d", coldHit, coldTot, warmHit, warmTot))
	return nil
}

// Apply refines a profile with what the probe measured.
func (rep *Report) Apply(p provider.Profile) provider.Profile {
	f := rep.Findings
	if f.MinCachePrefix > 0 {
		p.Cache.MinPrefixTokens = f.MinCachePrefix
	}
	if f.CacheGranularity > 1 {
		p.Cache.Granularity = f.CacheGranularity
	}
	if f.ReasoningDetails {
		p.ReplayThinking = true
	}
	return p
}

// Text renders the report for humans.
func (rep *Report) Text() string {
	f := rep.Findings
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "NO"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Endpoint probe for %s\n", rep.Model)
	fmt.Fprintf(&sb, "  streaming            %s (first byte %s)\n", yn(f.Streaming), f.TTFB)
	fmt.Fprintf(&sb, "  usage reported       %s\n", yn(f.UsageReported))
	fmt.Fprintf(&sb, "  exact cost reported  %s\n", yn(f.CostReported))
	fmt.Fprintf(&sb, "  tool calling         %s (round trip %s)\n", yn(f.Tools), yn(f.ToolRoundTrip))
	fmt.Fprintf(&sb, "  cached tokens shown  %s\n", yn(f.CachedTokensReported))
	fmt.Fprintf(&sb, "  prefix cache works   %s (repeat-request hit ratio %.0f%%)\n", yn(f.CacheWorks), f.CacheHitRatio*100)
	if f.CacheGranularity > 0 {
		fmt.Fprintf(&sb, "  cache granularity    ~%d tokens\n", f.CacheGranularity)
	}
	if f.MinCachePrefix > 0 {
		fmt.Fprintf(&sb, "  smallest cached size ~%d tokens\n", f.MinCachePrefix)
	}
	if f.WarmupNeeded != nil {
		fmt.Fprintf(&sb, "  warm-up before burst %s\n", yn(*f.WarmupNeeded))
	}
	fmt.Fprintf(&sb, "  reasoning exposed    %s (structured details %s)\n", yn(f.ReasoningSeen), yn(f.ReasoningDetails))
	if f.TokenIDs || f.TokenLogprobs || f.TokenPrefixStable {
		fmt.Fprintf(&sb, "  token ids            %s (logprobs %s, prefix-stable for packing %s)\n", yn(f.TokenIDs), yn(f.TokenLogprobs), yn(f.TokenPrefixStable))
	}
	if f.RateLimit != "" {
		fmt.Fprintf(&sb, "  rate limit           %s\n", f.RateLimit)
	}
	fmt.Fprintf(&sb, "  bytes per token      %.2f\n", f.BytesPerToken)
	fmt.Fprintf(&sb, "  probe cost           $%.6f over %d requests\n", rep.TotalUSD, len(rep.Steps))
	notes := append([]string(nil), f.Notes...)
	sort.Strings(notes)
	for _, n := range notes {
		fmt.Fprintf(&sb, "  note: %s\n", n)
	}
	return sb.String()
}
