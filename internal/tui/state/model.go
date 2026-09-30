package state

import (
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

// usageWire is core.Usage as model.response writes it.
type usageWire struct {
	Input     int64 `json:"input_tokens"`
	Read      int64 `json:"cache_read_tokens"`
	Write5m   int64 `json:"cache_write_5m_tokens"`
	Write1h   int64 `json:"cache_write_1h_tokens"`
	Output    int64 `json:"output_tokens"`
	Reasoning int64 `json:"reasoning_tokens"`
}

// tokens bounds every count to [0, maxTokens]: a hostile or broken endpoint's numbers must not overflow a sum.
func (u usageWire) tokens() Tokens {
	c := func(v int64) int64 { return max(0, min(v, maxTokens)) }
	return Tokens{Input: c(u.Input), CacheRead: c(u.Read), CacheWrite: c(u.Write5m) + c(u.Write1h), Output: c(u.Output), Reasoning: c(u.Reasoning)}
}

// requestWire is what model.request records (internal/agent/request.go recordRequest).
type requestWire struct {
	Req      string `json:"req"`
	Agent    string `json:"agent"`
	Role     string `json:"role"`
	Kind     string `json:"kind"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Dialect  string `json:"dialect"`
	Sections []struct {
		Name   string `json:"name"`
		Hash   string `json:"hash"`
		Tokens int64  `json:"tokens"`
		BP     bool   `json:"bp"`
	} `json:"sections"`
	ThreadFrom  int64  `json:"thread_from"`
	ThreadTo    int64  `json:"thread_to"`
	Hot         string `json:"hot"`
	CacheKey    string `json:"cache_key"`
	PrefixKey   string `json:"prefix_key"`
	Breakpoints []struct {
		TTL   int64  `json:"ttl"`
		Label string `json:"label"`
	} `json:"breakpoints"`
	SharedBlocks int64  `json:"shared_blocks"`
	SharedTokens int64  `json:"shared_tokens"`
	Tools        int64  `json:"tools"`
	Renderer     string `json:"renderer"`
	Manifest     struct {
		Tools  string   `json:"tools"`
		System []string `json:"system"`
	} `json:"manifest"`
}

// responseWire is what model.response records.
type responseWire struct {
	Req          string    `json:"req"`
	Model        string    `json:"model"`
	Provider     string    `json:"provider"`
	Usage        usageWire `json:"usage"`
	CostUSD      *float64  `json:"cost_usd"`
	ExpectedRead int64     `json:"expected_read"`
	Missed       int64     `json:"missed"`
	Anomaly      bool      `json:"anomaly"`
	ExpectedCold bool      `json:"expected_cold"`
	Stop         string    `json:"stop"`
	TotalMs      int64     `json:"total_ms"`
	Side         bool      `json:"side"`
}

func (s *State) onRequest(e events.Event, t time.Time) {
	var p requestWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	side := p.Kind != "" && p.Kind != "main"
	s.totals.Requests++
	if side {
		s.totals.Side++
	} else {
		s.totals.Main++
	}
	s.gov.noteRequest(t)
	a := s.agent(firstOf(p.Agent, e.Agent), t)
	if a == nil {
		return
	}
	model := clip(p.Model, textID)
	a.Model = firstOf(a.Model, model)
	a.active(t)
	if len(a.reqs) >= MaxOpenRequests {
		a.reqs = append(a.reqs[:0], a.reqs[1:]...)
	}
	req := clip(p.Req, textID)
	a.takeReq(req) // the same request again (a resumed session restarts its counters): the later one stands
	a.reqs = append(a.reqs, openReq{req: req, t: t, seq: e.Seq, side: side, model: model})
	if side {
		a.syncBusy(t)
		a.refresh()
		return
	}
	a.Role = firstOf(a.Role, clip(p.Role, textID))
	first := a.mainReqs == 0
	a.mainReqs++
	a.run = runThinking
	st := &a.Stack
	st.Req, st.Seq, st.At = req, e.Seq, t
	st.Model, st.Provider, st.Dialect, st.Renderer = model, clip(p.Provider, textID), clip(p.Dialect, textID), clip(p.Renderer, textID)
	st.Sections, st.SectionTokens = nil, 0
	var sharedHash string
	var prefixTok int
	for i, sec := range p.Sections {
		if i >= MaxSections {
			break
		}
		tok := clampTokens(sec.Tokens)
		st.Sections = append(st.Sections, Section{Name: clip(sec.Name, textID), Hash: short(sec.Hash), Tokens: tok, Breakpoint: sec.BP})
		st.SectionTokens += tok
		switch sec.Name {
		case "shared":
			sharedHash, prefixTok = short(sec.Hash), prefixTok+tok
		case "role":
			prefixTok += tok
		}
	}
	st.Breakpoints = nil
	for i, b := range p.Breakpoints {
		if i >= MaxBreakpoints {
			break
		}
		st.Breakpoints = append(st.Breakpoints, Breakpoint{Label: clip(b.Label, textID), TTLSeconds: int(max(0, min(b.TTL/int64(time.Second), 30*24*3600)))})
	}
	st.Tools = clampTokens(p.Tools)
	st.ThreadFrom, st.ThreadTo = p.ThreadFrom, p.ThreadTo
	st.PrefixKey, st.CacheKey = short(p.PrefixKey), clip(p.CacheKey, textID)
	st.SharedBlocks, st.SharedTokens = clampTokens(p.SharedBlocks), clampTokens(p.SharedTokens)
	st.ToolsHash, st.HotHash = short(p.Manifest.Tools), short(p.Hot)
	st.SystemHashes = nil
	for i, h := range p.Manifest.System {
		if i >= 4 {
			break
		}
		st.SystemHashes = append(st.SystemHashes, short(h))
	}
	st.Answered = false
	if first {
		st.Inherited = s.sharedSeen(sharedHash)
	}
	s.noteShared(sharedHash)
	s.joinPrefix(a, st.PrefixKey, e.Seq, sharedHash, a.Role, model, prefixTok)
	a.syncBusy(t)
	a.refresh()
}

func (s *State) onResponse(e events.Event, t time.Time) {
	var p responseWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	a := s.agent(e.Agent, t)
	var rq openReq
	var had bool
	if a != nil {
		rq, had = a.takeReq(clip(p.Req, textID))
	}
	side := p.Side || (had && rq.side)
	u := p.Usage.tokens()
	var cost float64
	if p.CostUSD != nil {
		cost = usd(*p.CostUSD)
	}

	tot := &s.totals
	tot.Responses++
	tot.CostUSD += cost
	addTokens(&tot.Tokens, u)
	var saved float64
	var model *modelState
	if a != nil {
		model = s.priceOf(rq.model, clip(p.Model, textID), a.Model, a.Stack.Model, s.sess.Model)
	} else {
		model = s.priceOf(rq.model, clip(p.Model, textID), s.sess.Model)
	}
	if usdv, ok := saving(model, u.CacheRead); ok {
		saved = usdv
		tot.Savings.SavedUSD += usdv
		tot.Savings.PricedReadTokens += u.CacheRead
	} else {
		tot.Savings.UnpricedReadTokens += u.CacheRead
	}
	if a == nil {
		return
	}

	a.active(t)
	addTokens(&a.Tokens, u)
	a.CostUSD += cost
	a.SavedUSD += saved
	if side {
		a.SideRequests++
		a.syncBusy(t)
		a.refresh()
		s.touchCache(a, p, rq, had, t, u, false)
		return
	}
	a.Requests++

	prompt := u.Prompt()
	ratio := 0.0
	if prompt > 0 {
		ratio = float64(u.CacheRead) / float64(prompt)
	}
	a.hits.push(ratio)
	a.hitIdx++

	st := &a.Stack
	answered := had && rq.req == st.Req || (!had && p.Req != "" && p.Req == st.Req)
	st.Answered, st.RespReq, st.RespSeq = answered, clip(p.Req, textID), e.Seq
	st.Prompt, st.Read, st.Write, st.Fresh = clampTokens(prompt), clampTokens(u.CacheRead), clampTokens(u.CacheWrite), clampTokens(u.Input)
	st.Hit = ratio
	st.ExpectedRead, st.Missed = clampTokens(p.ExpectedRead), clampTokens(p.Missed)
	st.Miss, st.ExpectedCold = p.Anomaly, p.ExpectedCold
	st.Unsectioned = 0
	if answered {
		st.Unsectioned = max(st.Prompt-st.SectionTokens, 0)
	}

	if p.Stop != "tool_use" && a.run == runThinking && len(a.tools) == 0 {
		a.run = runIdle // a final answer: nothing follows but the end of the run
	}
	s.touchCache(a, p, rq, had, t, u, true)
	a.syncBusy(t)
	a.refresh()
}

func addTokens(dst *Tokens, u Tokens) {
	dst.Input += u.Input
	dst.CacheRead += u.CacheRead
	dst.CacheWrite += u.CacheWrite
	dst.Output += u.Output
	dst.Reasoning += u.Reasoning
}

// touchCache records that the agent's prompt, and the prefix it rides, were read from or written to the provider's cache by the
// request this response answers: the entry's clock restarts at the moment the request reached the provider (the response's time
// less the time the call took, not before the request was logged), which is when the provider refreshes it.
func (s *State) touchCache(a *agentState, p responseWire, rq openReq, had bool, t time.Time, u Tokens, main bool) {
	start := t
	if p.TotalMs > 0 && p.TotalMs <= 3_600_000 {
		start = t.Add(-time.Duration(p.TotalMs) * time.Millisecond)
	}
	if had && start.Before(rq.t) {
		start = rq.t
	}
	if start.After(t) {
		start = t
	}
	how := "sent"
	switch {
	case u.CacheRead > 0:
		how = "read"
	case u.CacheWrite > 0:
		how = "write"
	}
	model := firstOf(rq.model, a.Stack.Model, a.Model)
	if g := s.prefixes[a.prefix]; g != nil {
		ttl, dflt := s.ttlFor(model, a.Stack.Breakpoints, "shared", "role")
		if start.After(g.touch.last) || g.touch.last.IsZero() {
			g.touch = touchInfo{last: start, how: how, ttl: ttl, dflt: dflt, size: g.tokens}
		}
	}
	if !main {
		return
	}
	ttl, dflt := s.ttlFor(model, a.Stack.Breakpoints, "thread", "notes")
	a.touch = touchInfo{last: start, how: how, ttl: ttl, dflt: dflt, size: a.Stack.Prompt}
}

func (s *State) onModelError(e events.Event, t time.Time) {
	var p struct {
		Req     string `json:"req"`
		Kind    string `json:"kind"`
		Error   string `json:"error"`
		Status  int    `json:"status"`
		Attempt int    `json:"attempt"`
		DelayMs int64  `json:"delay_ms"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	a := s.agent(e.Agent, t)
	if p.Attempt > 0 || p.DelayMs > 0 {
		// A retry notice: the request goes on.
		s.totals.Retries++
		s.gov.retries++
		limited := p.Kind == "rate_limit" || p.Status == 429
		if limited {
			s.totals.RateLimited++
			s.gov.rateLimited++
		}
		if a != nil {
			a.Retries++
			a.active(t)
		}
		what := clip(p.Kind, textID)
		if p.Status != 0 {
			what += " (http " + strconv.Itoa(p.Status) + ")"
		}
		s.line(e.Seq, t, e.Agent, FeedRetry, GlyphWarn, "retrying: "+firstOf(what, "request failed"), "in "+fmtDur(p.DelayMs))
		return
	}
	if cancelled := strings.Contains(p.Error, "context canceled"); cancelled {
		// A request that was cancelled because its run was stopped (the manager finished, the person interrupted, the session ended)
		// did not fail: it is not an error of the endpoint or of the agent, and must not turn the agent red.
		s.totals.Cancelled++
		if a != nil {
			a.takeReq(clip(p.Req, textID))
			if a.run == runThinking && len(a.tools) == 0 {
				a.run = runIdle
			}
			a.active(t)
			a.syncBusy(t)
			a.refresh()
		}
		s.line(e.Seq, t, e.Agent, FeedNote, GlyphInfo, "request cancelled: the run was stopped", clip(p.Req, textID))
		return
	}
	s.totals.Errors++
	side := false
	if a != nil {
		a.Errors++
		rq, had := a.takeReq(clip(p.Req, textID))
		side = had && rq.side
		if !side {
			a.run = runError
		}
		a.active(t)
		a.syncBusy(t)
		a.refresh()
	}
	s.line(e.Seq, t, e.Agent, FeedError, GlyphFail, "model request failed: "+firstOf(clean(p.Error, textLine), "no reason given"), "")
}
