package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/cost"
)

// AnthropicConfig configures the Anthropic-dialect mock.
type AnthropicConfig struct {
	// Config carries the latency model (FirstToken, PrefillPer, DecodePer), the
	// clock, the request-per-minute limit and the price table shared with the
	// chat mock. Its engine settings are ignored: caching is explicit here.
	Config
	// Cache configures the explicit-cache engine. Its Now defaults to Config.Now.
	Cache ExplicitConfig
	// MinPrefixTokens overrides the per-model minimum cacheable prefix (which
	// otherwise comes from the price table: 512 for the newest models, up to 4096).
	MinPrefixTokens int
	// APIKey, when set, must arrive as x-api-key or as a bearer token.
	APIKey string
	// EnforceThinkingBinding makes every model run the preserved-thinking prefix
	// check as enforced: a replayed thinking block whose preceding prefix differs
	// from what produced it is a 400, exactly as on an account created on or
	// after 2026-08-31. Without it the check runs only on the models that have it
	// and is enforced only for requests that set thinking.block_binding.
	EnforceThinkingBinding bool
	// KnownBetas lists accepted anthropic-beta values (default DefaultKnownBetas).
	KnownBetas []string
	// SignatureKey seeds thinking signatures. Default "mock-signing-key".
	SignatureKey string
	// OmitThinkingText returns thinking blocks with an empty thinking string, the
	// way models that hide their reasoning do: it lives in the signature only.
	OmitThinkingText bool
	// MinimalDeltaUsage makes message_delta report only output_tokens (older API
	// behaviour); by default it repeats the cumulative input counters too.
	MinimalDeltaUsage bool
	// ImageTokens is the token cost of one image. Default 1000.
	ImageTokens int
}

// AnthropicStat is one served request, with what the cache did.
type AnthropicStat struct {
	CallStat
	// Read, Write5m, Write1h and Uncached are the usage counters:
	// PromptTokens = Read + Write5m + Write1h + Uncached.
	Read, Write5m, Write1h, Uncached int
	// ReadTiers and WriteTiers attribute the read and written tokens to the
	// tools, system and messages tiers.
	ReadTiers, WriteTiers TierTokens
	Hits                  []ExplicitHit
	Skipped               []string
	Markers               int
	Betas                 []string
	// Transformations are the input_transformations the response reported.
	Transformations []string
	Stop            string
	Err             string
}

// AnthropicServer is the Anthropic-dialect mock provider. It embeds the chat
// Server so the Responder, Call, Reply, Fault and CallStat types, the counters,
// Stats and SetResponder are shared; only the wire format and the cache differ.
type AnthropicServer struct {
	*Server
	acfg  AnthropicConfig
	cache *ExplicitEngine

	amu    sync.Mutex
	astats []AnthropicStat
}

// NewAnthropic builds the mock. A nil responder echoes the last user message.
func NewAnthropic(cfg AnthropicConfig, r Responder) *AnthropicServer {
	base := New(cfg.Config, r)
	if cfg.SignatureKey == "" {
		cfg.SignatureKey = "mock-signing-key"
	}
	if cfg.KnownBetas == nil {
		cfg.KnownBetas = DefaultKnownBetas
	}
	cc := cfg.Cache
	if cc.Now == nil {
		cc.Now = base.cfg.Now
	}
	if cc.MinPrefix == nil {
		fixed := cfg.MinPrefixTokens
		cc.MinPrefix = func(model string) int {
			if fixed > 0 {
				return fixed
			}
			if m, ok := cost.Defaults().Lookup(model); ok && m.Cache.MinPrefixTokens > 0 {
				return m.Cache.MinPrefixTokens
			}
			return 1024
		}
	}
	return &AnthropicServer{Server: base, acfg: cfg, cache: NewExplicitEngine(cc)}
}

// Handler serves POST /v1/messages.
func (a *AnthropicServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", a.handleMessages)
	return mux
}

// Start launches an httptest server; callers Close it. (It must be defined here:
// the promoted Server.Start would serve the chat routes.)
func (a *AnthropicServer) Start() *httptest.Server { return httptest.NewServer(a.Handler()) }

// Cache exposes the explicit-cache engine for tests.
func (a *AnthropicServer) Cache() *ExplicitEngine { return a.cache }

// AnthropicStats returns a copy of every served call, in completion order.
func (a *AnthropicServer) AnthropicStats() []AnthropicStat {
	a.amu.Lock()
	defer a.amu.Unlock()
	return append([]AnthropicStat(nil), a.astats...)
}

// Reset clears stats and counters (the cache is kept).
func (a *AnthropicServer) Reset() {
	a.Server.Reset()
	a.amu.Lock()
	a.astats = nil
	a.amu.Unlock()
}

func (a *AnthropicServer) recordA(st AnthropicStat) {
	a.amu.Lock()
	a.astats = append(a.astats, st)
	a.amu.Unlock()
	a.Server.record(st.CallStat)
}

func writeAErr(w http.ResponseWriter, e *aErr, reqID string, extra http.Header) {
	for k, v := range extra {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", reqID)
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":       "error",
		"error":      map[string]any{"type": e.typ, "message": e.msg},
		"request_id": reqID,
	})
}

// errType maps an HTTP status to the API's error type.
func errType(status int) string {
	switch status {
	case 400, 422:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 402:
		return "billing_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 504:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	}
	if status >= 500 {
		return "api_error"
	}
	return "invalid_request_error"
}

// limitA is the per-minute request limit in the API's error shape.
func (a *AnthropicServer) limitA(w http.ResponseWriter, reqID string) bool {
	s := a.Server
	if s.cfg.RPM <= 0 {
		return false
	}
	now := s.cfg.Now()
	s.mu.Lock()
	cut := now.Add(-time.Minute)
	kept := s.window[:0]
	for _, t := range s.window {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.window = kept
	remaining := s.cfg.RPM - len(s.window)
	limited := remaining <= 0
	if !limited {
		s.window = append(s.window, now)
		remaining--
	}
	s.mu.Unlock()
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Requests-Limit", strconv.Itoa(s.cfg.RPM))
	h.Set("Anthropic-Ratelimit-Requests-Remaining", strconv.Itoa(max(remaining, 0)))
	h.Set("Anthropic-Ratelimit-Requests-Reset", now.Add(time.Minute).UTC().Format(time.RFC3339))
	if limited {
		h.Set("Retry-After", "1")
		writeAErr(w, &aErr{status: 429, typ: "rate_limit_error", msg: "Number of request tokens has exceeded your per-minute rate limit"}, reqID, h)
		return true
	}
	for k, v := range h {
		w.Header()[k] = v
	}
	return false
}

func (a *AnthropicServer) handleMessages(w http.ResponseWriter, r *http.Request) {
	s := a.Server
	s.mu.Lock()
	s.n++
	n := s.n
	resp := s.resp
	s.mu.Unlock()
	reqID := "req_mock_" + strconv.Itoa(n)
	started := s.cfg.Now()
	stat := AnthropicStat{CallStat: CallStat{N: n, StartedAt: started, Header: r.Header.Clone()}}
	fail := func(e *aErr, extra http.Header) {
		stat.Status, stat.Err = e.status, e.msg
		a.recordA(stat)
		writeAErr(w, e, reqID, extra)
	}

	if r.Method != http.MethodPost {
		fail(&aErr{status: 405, typ: "invalid_request_error", msg: "Method Not Allowed"}, nil)
		return
	}
	if a.limitA(w, reqID) {
		stat.Status = 429
		a.recordA(stat)
		return
	}
	if r.Header.Get("Anthropic-Version") == "" {
		fail(badReq("anthropic-version: header is required"), nil)
		return
	}
	if k := a.acfg.APIKey; k != "" {
		if r.Header.Get("X-Api-Key") != k && r.Header.Get("Authorization") != "Bearer "+k {
			fail(&aErr{status: 401, typ: "authentication_error", msg: "invalid x-api-key"}, nil)
			return
		}
	}
	betas, e := parseBetas(r.Header.Values("Anthropic-Beta"), a.acfg.KnownBetas)
	if e != nil {
		fail(e, nil)
		return
	}
	for b := range betas {
		stat.Betas = append(stat.Betas, b)
	}
	const limit = 32 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		fail(badReq("%v", err), nil)
		return
	}
	if len(body) > limit {
		fail(&aErr{status: 413, typ: "request_too_large", msg: "Request exceeds the maximum allowed number of bytes."}, nil)
		return
	}
	q, e := parseRequest(body, betas)
	if e != nil {
		fail(e, nil)
		return
	}
	stat.Model, stat.Streamed = q.model, q.stream
	stat.Session = firstNonEmpty(r.Header.Get("X-Session-Id"), q.userID)

	bind := a.checkBinding(q)
	if bind.err != nil {
		fail(bind.err, nil)
		return
	}
	xreq, e := a.explicitRequest(q, bind.dropped)
	if e != nil {
		fail(e, nil)
		return
	}
	plan, perr := a.cache.Lookup(xreq, started)
	if perr != nil {
		fail(badReq("%s", perr.Error()), nil)
		return
	}
	stat.PromptTokens, stat.Cached = plan.Total, plan.Read
	stat.Read, stat.Write5m, stat.Write1h, stat.Uncached = plan.Read, plan.Write5m, plan.Write1h, plan.Uncached
	stat.ReadTiers, stat.WriteTiers, stat.Hits, stat.Skipped, stat.Markers = plan.ReadTiers, plan.WriteTiers, plan.Hits, plan.Skipped, plan.Markers
	for _, x := range bind.xforms {
		stat.Transformations = append(stat.Transformations, x.Type+":"+x.Reason)
	}

	call := q.call(n, plan)
	reply := resp(call)

	if reply.Fault != nil && !reply.Fault.MidStream {
		h := http.Header{}
		if d := reply.Fault.RetryAfter; d > 0 {
			h.Set("Retry-After", strconv.Itoa(int(d.Seconds())))
		}
		msg := reply.Fault.Message
		if msg == "" {
			msg = http.StatusText(reply.Fault.Status)
		}
		fail(&aErr{status: reply.Fault.Status, typ: errType(reply.Fault.Status), msg: msg}, h)
		return
	}

	// Prefill takes time proportional to the tokens that were not cached; the
	// entries this request wrote become readable only once its first byte is out.
	sleep(s.cfg.FirstToken + time.Duration(plan.Total-plan.Read)*s.cfg.PrefillPer)
	a.cache.Publish(plan)
	stat.FirstByteAt = s.cfg.Now()

	out := a.compose(q, reply, plan, bind, n)
	stat.Completion, stat.Stop = out.usage.OutputTokens, out.stop
	stat.Cost = a.price(q.model).USD(usageOf(plan, out.usage.OutputTokens))
	stat.Status = 200
	// Recorded before the response is written, so that a client that has seen the
	// reply (or its first byte) can already find the call in Stats.
	a.recordA(stat)

	w.Header().Set("Request-Id", reqID)
	if !q.stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out.message(true))
		return
	}
	a.writeStream(w, q, reply, out)
}

// call builds the Responder's view of the request, in the chat mock's shape: an
// assistant message carries its tool_use blocks as ToolCalls and each tool_result
// arrives as a role "tool" message, so responders written for the chat mock work
// unchanged. Turn-scoped system messages appear with role "system".
func (q *aReq) call(n int, plan *ExplicitPlan) *Call {
	c := &Call{N: n, Model: q.model, Cached: plan.Read, Prompt: plan.Total, Raw: q.raw}
	var sys []string
	for _, b := range q.system {
		sys = append(sys, b.text)
	}
	c.System = strings.Join(sys, "\n\n")
	for _, t := range q.tools {
		c.Tools = append(c.Tools, t.name)
	}
	for _, m := range q.msgs {
		msg := Msg{Role: m.role}
		var text []string
		flush := func() {
			if len(text) > 0 || msg.Role == "assistant" {
				msg.Content = strings.Join(text, "")
				c.Messages = append(c.Messages, msg)
				msg = Msg{Role: m.role}
				text = nil
			}
		}
		for _, b := range m.blocks {
			switch b.typ {
			case "tool_use":
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.id, Name: b.name, Args: string(b.input)})
			case "tool_result":
				c.Messages = append(c.Messages, Msg{Role: "tool", ToolCallID: b.toolUseID, Content: b.plain()})
			case "text":
				text = append(text, b.text)
			}
		}
		flush()
	}
	return c
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a *AnthropicServer) price(model string) cost.Price {
	if m, ok := cost.Defaults().Lookup(model); ok {
		return m.Price
	}
	p := a.Server.cfg.Price
	if p.CacheWrite5mPerM == 0 {
		p.CacheWrite5mPerM = p.InputPerM * 1.25
	}
	if p.CacheWrite1hPerM == 0 {
		p.CacheWrite1hPerM = p.InputPerM * 2
	}
	return p
}
