package agent

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// RetryBase is the first backoff step; tests shrink it.
var RetryBase = time.Second

// MaxRetryAfter caps how long a server's Retry-After may hold one request back.
var MaxRetryAfter = provider.DefaultMaxRetryAfter

const maxAttempts = 6

// request renders the agent's prompt and performs one model call, with retry,
// cache accounting, drift detection and the two recoveries a rejected prompt
// allows. It returns the rebase counter the prompt was rendered at, so the
// caller can tell whether the response's thinking is still bound to the thread
// it is appended to.
func (a *Agent) request(ctx context.Context) (*provider.Response, uint64, error) {
	retriedContext, retriedBinding := false, false
	var opt reqOpt
	for {
		resp, epoch, err := a.requestOnce(ctx, opt)
		if err == nil {
			return resp, epoch, nil
		}
		if pe, ok := provider.AsError(err); ok {
			switch {
			// A prompt over the window is a compaction problem, not a failure: fold
			// the thread mechanically (no model call needed) and try again once.
			case pe.Kind == provider.ErrContextLength && !retriedContext && !a.cfg.NoCompaction:
				retriedContext = true
				a.cfg.Sink.Notice(a.cfg.ID, "warn", "context window exceeded; compacting mechanically")
				if cerr := a.emergencyCompact(ctx, "provider reported context length exceeded"); cerr == nil {
					continue
				}
			// A rejected thinking block means the transcript changed under a
			// binding. Removing all thinking is always allowed: strip it durably (a
			// declared rebase, not a render-time toggle that would flip back) and
			// try once more, asking the endpoint to drop rather than reject if it can.
			case pe.Kind == provider.ErrThinkingBinding && !retriedBinding:
				retriedBinding = true
				if a.recoverBinding(pe) {
					opt.dropBinding = true
					continue
				}
			}
		}
		return nil, 0, err
	}
}

// reqOpt carries per-attempt overrides.
type reqOpt struct {
	// dropBinding asks the endpoint to drop mismatching thinking blocks instead
	// of failing (Request.BindingMode "drop_block"), when it has the control.
	dropBinding bool
}

// recoverBinding strips thinking from the thread after the provider rejected a
// block and reports whether anything was removed (if nothing was, the rejection
// has another cause and retrying would only repeat it).
func (a *Agent) recoverBinding(pe *provider.Error) bool {
	a.mu.Lock()
	changed := a.thread.Rewrite(func(t core.Turn) (core.Turn, bool) { return kv.StripThinkingTurn(t) })
	if changed {
		a.epoch++
	}
	a.mu.Unlock()
	if !changed {
		return false
	}
	// The rejection is proof the route binds signatures. flagged=false means the
	// model table did not say so: the route is now treated as one that does.
	a.enforcing.Store(true)
	a.emit(events.TypeCacheAnomaly, map[string]any{
		"kind": "thinking_binding", "action": "stripped all thinking durably and retried once", "error": pe.Message,
		"flagged": a.cfg.Model.PreservedThinking,
	})
	a.emit(events.TypeLayerCommit, map[string]any{"scope": "thinking-strip", "reason": "provider rejected a thinking block"})
	a.cfg.Sink.Notice(a.cfg.ID, "warn", "provider rejected a thinking block; reasoning history dropped and the request retried")
	return true
}

func (a *Agent) requestOnce(ctx context.Context, opt reqOpt) (*provider.Response, uint64, error) {
	prof := a.cfg.Provider.Profile()
	caps := a.caps(prof)

	a.mu.Lock()
	stack := a.stack
	stack.Thread = a.thread.Snapshot()
	epoch := a.epoch + stack.Thread.Epoch
	first := a.mainReqs == 0
	a.reqN++
	n := a.reqN
	reqID := fmt.Sprintf("%s.%d", a.cfg.ID, n)
	a.markPendingLocked(reqID)
	var prevRolling *core.BlockRef
	if a.rollValid && a.rollEpoch == epoch {
		r := a.rollRef
		prevRolling = &r
	}
	rebased := !first && epoch != a.lastReqEpoch
	scale := a.tokenScaleLocked()
	a.mu.Unlock()
	defer a.clearPending(reqID)

	var hot []core.Block
	if a.cfg.Hot != nil && caps.HotMode == kv.HotInline {
		hot = a.hotBlocks()
	}
	r := kv.Render(&stack, kv.RenderOpts{
		Hot: hot, Caps: caps, Policy: a.cfg.KVPolicy, Params: a.cfg.Params,
		CacheKey: a.cacheKey(&stack), Est: a.est, PrevRolling: prevRolling,
	})
	check := a.guard.Observe(r, epoch, a.est)
	if check.Drift {
		a.emit(events.TypeCacheAnomaly, map[string]any{
			"kind": "drift", "diverged": check.Diverged, "shared_blocks": check.SharedBlocks,
		})
		a.cfg.Sink.Notice(a.cfg.ID, "warn", "prompt prefix changed without a declared rebase (layer "+check.Diverged+"); cache will miss")
	}

	a.recordRequest(reqID, r, hot, check, prof, KindMain, true)

	started := func(bool) {}
	sharedWarm := false
	if a.cfg.Gate != nil {
		key := a.gateKey(&stack, prof)
		// A gate that can tell who is asking lets a request that outranks the primer of
		// a cold prefix through at once, as the governor would (a manager's request must
		// not wait for a worker that is still queued for its slot).
		var s func(bool)
		var gerr error
		if pg, ok := a.cfg.Gate.(PriorityGate); ok {
			s, gerr = pg.EnterPrio(ctx, key, a.cfg.Priority)
		} else {
			s, gerr = a.cfg.Gate.Enter(ctx, key)
		}
		if gerr != nil {
			return nil, 0, gerr
		}
		started = s
		if w, ok := a.cfg.Gate.(interface{ Warm(string) bool }); ok {
			sharedWarm = w.Warm(key)
		}
	}

	req := &provider.Request{Prompt: r.Prompt, Label: reqID, Capture: a.cfg.CaptureTokens}
	if prof.BindingControls && (opt.dropBinding || rebased) {
		// Right after a declared rebase any thinking the strip missed would be a
		// hard failure; where the endpoint can drop it instead, say so.
		req.BindingMode = "drop_block"
	}
	start := a.cfg.Now()
	resp, err := a.call(ctx, req, a.cfg.Priority, func(e provider.Event) {
		a.forward(e)
		if e.Kind == provider.EvStart {
			started(true)
		}
	})
	started(err == nil)
	if err != nil {
		a.emit(events.TypeModelError, map[string]any{"req": reqID, "error": err.Error()})
		return nil, 0, err
	}

	u := resp.Usage
	usd := a.cfg.Model.Price.USD(u)
	if resp.CostUSD != nil {
		usd = *resp.CostUSD
	}
	totalIn := u.TotalInput()
	estAll := kv.PromptTokens(r.Prompt, a.est)

	// What should the provider have read? The exact prefix shared with the
	// previous request, up to what its cache markers let it find, scaled by how far
	// off our estimator was on that request. A first request has no predecessor;
	// if the gate says the shared prefix was already warm when it was admitted,
	// the shared prefix is the expectation.
	expected := int(float64(check.ReadableTokens) * scale)
	firstCheck := false
	if first && sharedWarm && !prof.Cache.MessageBoundaries {
		expected, firstCheck = r.SharedPrefixTokens(a.est), true
	}

	a.mu.Lock()
	prevStart := a.lastStart
	ttl := a.ttl(prof)
	expectedCold := !first && ttl > 0 && a.cfg.Planner.IsCold(prevStart, start, ttl)
	a.usage = a.usage.Add(u)
	a.costUSD += usd
	delete(a.pendingRequests, reqID)
	a.lastStart = start
	a.mainReqs++
	a.reportsCache = a.reportsCache || u.CacheReadTokens > 0 || u.CacheWriteTokens() > 0
	reports := a.reportsCache
	a.lastReqEpoch, a.lastReqLen = epoch, len(stack.Thread.Turns)
	if r.Rolling != nil {
		a.rollRef, a.rollEpoch, a.rollValid = *r.Rolling, epoch, true
	} else {
		a.rollValid = false
	}
	if estAll > 0 && totalIn > 0 {
		a.lastEstAll, a.lastTotalIn = estAll, totalIn
	}
	a.hotAge++
	missed := expected - u.CacheReadTokens
	// A miss is more than 5% of the prompt and at least 2,000 tokens that could
	// have been read (the rule Claude Code applies), where it can be judged at
	// all: the provider reports cache usage, the prefix was big enough to cache,
	// and the entry cannot be expected to have expired.
	judged := reports && !expectedCold && expected >= prof.Cache.MinPrefixTokens && expected > 0
	floor := totalIn / 20
	if floor < 2000 {
		floor = 2000
	}
	anomaly := judged && missed >= floor
	a.lastMiss = judged && u.CacheReadTokens*10 < expected*7
	streak := a.anomStreak
	if anomaly {
		a.anomStreak++
	} else {
		a.anomStreak = 0
	}
	a.mu.Unlock()
	a.est.Observe(kv.PromptBytes(r.Prompt), totalIn)
	if a.cfg.Model.Provider == "unknown" { // a model whose window nobody told the harness: a server that cuts the prompt off shows itself here
		a.noteTruncation(kv.PromptBytes(r.Prompt), totalIn)
	}

	if anomaly {
		a.emit(events.TypeCacheAnomaly, map[string]any{
			"kind": "low_hit", "req": reqID, "expected_read": expected, "actual_read": u.CacheReadTokens,
			"missed": missed, "diverged": check.Diverged, "first_request": firstCheck,
		})
		if streak == 0 {
			why, level := "", "warn"
			if !check.Drift {
				// The guard checks internal blocks, not the server's rendered prompt
				// or cache boundaries. A stable prefix does not identify the cause.
				why, level = " (internal prefix is stable; cache availability and API rendering are unverified)", "info"
			}
			a.cfg.Sink.Notice(a.cfg.ID, level, fmt.Sprintf(CacheMissNoticePrefix+" ~%d tokens read from cache, got %d%s", expected, u.CacheReadTokens, why))
		}
	}
	// The endpoint changed the request behind our back (dropped a thinking block
	// whose binding no longer matched): the transcript still holds it, so the
	// next request would drop it again and miss the cache from that block on.
	// Strip durably, once, before the response turn is appended.
	for _, tr := range resp.Transformations {
		if strings.Contains(strings.ToLower(tr.Type+" "+tr.Reason), "thinking") {
			a.mu.Lock()
			if a.thread.Rewrite(func(t core.Turn) (core.Turn, bool) { return kv.StripThinkingTurn(t) }) {
				a.epoch++
			}
			epoch = a.epoch + a.thread.Snapshot().Epoch
			a.mu.Unlock()
			a.emit(events.TypeCacheAnomaly, map[string]any{
				"kind": "thinking_dropped", "transformations": resp.Transformations, "action": "stripped all thinking durably",
			})
			break
		}
	}
	a.emit(events.TypeModelResponse, a.responsePayload(map[string]any{
		"req": reqID, "id": resp.ID, "model": resp.Model, "provider": resp.Provider,
		"usage": u, "cost_usd": usd, "gateway_cost": resp.CostUSD != nil,
		"hit_ratio": u.HitRatio(), "expected_read": expected, "missed": missed, "anomaly": anomaly,
		"expected_cold": expectedCold, "hot_mode": caps.HotMode.String(),
		"stop": resp.Stop, "ttfb_ms": resp.TTFB.Milliseconds(), "total_ms": resp.Total.Milliseconds(),
		"transformations": resp.Transformations,
	}, resp))
	a.cfg.Sink.Response(a.cfg.ID, resp, u.HitRatio())
	return resp, epoch, nil
}

// forward relays streaming events to the UI sink.
func (a *Agent) forward(e provider.Event) {
	switch e.Kind {
	case provider.EvText:
		a.cfg.Sink.Text(a.cfg.ID, e.Text)
	case provider.EvThinking:
		a.cfg.Sink.Thinking(a.cfg.ID, e.Text)
	case provider.EvReset:
		if r, ok := a.cfg.Sink.(Resetter); ok {
			r.Reset(a.cfg.ID)
		}
	}
}

// Request kinds recorded in model.request: what a call was for.
const (
	KindMain      = "main"
	KindCompactor = "compactor"
)

// recordRequest logs the request as a recipe (layer hashes and sizes, thread
// range, hot-block hash) and as a manifest: the exact model-visible prompt as
// content hashes, delta-encoded against this agent's previous request. Layer and
// message texts are stored once in the blob store, so a session with dozens of
// agents sharing a pinned prefix logs that prefix once, and any prompt can be
// rebuilt byte for byte (and checked against its wire hash) for training data.
//
// advance says whether this request becomes the base of the next one's delta:
// side requests (compactor forks) are recorded against the last main request but
// do not replace it.
func (a *Agent) recordRequest(reqID string, r *kv.Rendered, hot []core.Block, c kv.Check, prof provider.Profile, kind string, advance bool) {
	type sec struct {
		Name   string    `json:"name"`
		Hash   core.Hash `json:"hash"`
		Tokens int       `json:"tokens"`
		BP     bool      `json:"bp,omitempty"`
	}
	secs := make([]sec, 0, len(r.Sections)+1)
	for _, s := range r.Sections {
		secs = append(secs, sec{s.Name, s.Hash, s.Tokens, s.Breakpoint})
	}
	var from, to core.TurnID
	for _, m := range r.Prompt.Messages {
		if m.Turn != 0 {
			if from == 0 || m.Turn < from {
				from = m.Turn
			}
			if m.Turn > to {
				to = m.Turn
			}
		}
	}
	var hotHash core.Hash
	if len(hot) > 0 {
		txt := ""
		for _, b := range hot {
			txt += b.Text + "\n"
		}
		hotHash, _ = a.cfg.Blobs.Put([]byte(txt))
	}
	a.storeLayerTexts(r)

	a.mu.Lock()
	prev := a.man
	a.mu.Unlock()
	man, next, err := core.BuildManifest(r.Prompt, prev, reqID, a.cfg.Blobs.Put)
	if err != nil {
		// Logging must never stop the agent; the recipe below is still recorded.
		a.cfg.Sink.Notice(a.cfg.ID, "warn", "could not record prompt manifest: "+err.Error())
	}
	role := a.cfg.Role
	if kind == KindCompactor {
		role = "compactor"
	}
	a.emit(events.TypeModelRequest, map[string]any{
		"req": reqID, "agent": a.cfg.ID, "role": role, "kind": kind,
		"model": r.Prompt.Model, "provider": prof.Name, "dialect": prof.Dialect,
		"sections": secs, "thread_from": from, "thread_to": to, "hot": hotHash,
		"cache_key": r.Prompt.CacheKey, "prefix_key": r.PrefixKey, "params": r.Prompt.Params,
		"breakpoints": r.Prompt.Breakpoints, "shared_blocks": c.SharedBlocks, "shared_tokens": c.SharedTokens,
		"tools": len(r.Prompt.Tools), "renderer": kv.RendererVersion,
		"manifest": man, "wire_hash": man.Wire,
	})
	// The next request is encoded against this one only once this one is in the log: a compaction fork that starts on its own goroutine
	// reads a.man, and with it advanced first it could log a request whose base is not in the log yet (seen under the race detector on a
	// loaded machine). A reader that follows the log as it grows then always meets a request's base before the request.
	if err == nil && advance {
		a.mu.Lock()
		a.man = next
		a.mu.Unlock()
	}
}

// responsePayload adds the action itself to a model.response: the assistant turn
// as a blob (provider-native blocks intact) and, when the endpoint returned
// them, the token ids and logprobs.
func (a *Agent) responsePayload(p map[string]any, resp *provider.Response) map[string]any {
	if b, err := core.MarshalStable(resp.Turn); err == nil {
		if h, err := a.cfg.Blobs.Put(b); err == nil {
			p["completion"] = h
		}
	}
	if resp.Tokens != nil {
		if b, err := core.MarshalStable(resp.Tokens); err == nil {
			if h, err := a.cfg.Blobs.Put(b); err == nil {
				p["tokens"] = h
			}
		}
	}
	return p
}

// storeLayerTexts persists nonempty stable layer text from the current stack to blob storage,
// ignoring storage errors.
func (a *Agent) storeLayerTexts(r *kv.Rendered) {
	s := a.Stack()
	for _, l := range []*kv.Layer{s.Const, s.Shared, s.RoleL, s.Notes, s.Spine} {
		if !l.Empty() {
			_, _ = a.cfg.Blobs.Put([]byte(l.Text()))
		}
	}
}

// shard is this agent's routing shard.
func (a *Agent) shard() int {
	shard := 0
	if a.cfg.AffinityShards > 1 {
		h := fnv.New32a()
		h.Write([]byte(a.cfg.ID))
		shard = int(h.Sum32()) % a.cfg.AffinityShards
		if shard < 0 {
			shard = -shard
		}
	}
	return shard
}

// cacheKey groups related requests for providers that use routing hints. A key
// does not guarantee placement or cache reuse. Hash the whole session ID: its
// first eight characters are a date shared by otherwise independent sessions.
func (a *Agent) cacheKey(s *kv.Stack) string {
	sid := a.cfg.SessionID
	if sid != "" {
		sid = core.HashString(sid).Short()
	}
	return fmt.Sprintf("sl:%s:%s:%d", sid, s.GlobalKey().Short(), a.shard())
}

// gateKey identifies the physical prefix a request needs warm, for the fan-out
// gate. It is two levels joined by "|", outermost first: the shared prefix on
// this agent's engine (model, tools, constitution, shared pin, the request
// parameters that key the messages tier, and the routing shard where the provider
// routes by key) and, inside it, the role prefix (the role pin too). The gate
// elects one primer per level, so agents of a second role, or on a second shard,
// are not released onto a prefix that is still cold for them.
func (a *Agent) gateKey(s *kv.Stack, prof provider.Profile) string {
	shard := 0
	if prof.Cache.KeyRouting {
		shard = a.shard()
	}
	p := a.cfg.Params
	shared := fmt.Sprintf("%s/%d/%s.%s.%s", s.GlobalKey().Short(), shard, p.Thinking, p.Effort, p.ToolChoice)
	return shared + "|" + s.PrefixKey().Short()
}

// retryNotice is the line a person sees while a request is being repeated: what failed (the kind of
// failure, the HTTP status and what the endpoint said, bounded) and when the next attempt is. "server;
// retrying in 877ms" says nothing to someone who has to decide whether to wait or to change model.
func retryNotice(pe *provider.Error, delay time.Duration) string {
	what := pe.Kind.String()
	if pe.Status != 0 {
		what += fmt.Sprintf(" (http %d)", pe.Status)
	}
	if m := provider.SanitizeText(pe.Message, 160); m != "" {
		what += ": " + m
	}
	return fmt.Sprintf("%s; retrying in %s", what, delay.Round(time.Millisecond))
}

// CacheMissNoticePrefix starts the notice an agent gives when a response read much less from the cache than its prompt promised. A
// program that draws cache breaks from the log (the chat on a terminal) leaves the notice out: it says what the break line says.
const CacheMissNoticePrefix = "cache miss: expected"

// outage says whether a failure is the endpoint being down or overloaded: it answered with a status of 500 or more, or with 429.
// Those are the failures that a wait cures. Nothing that came with no status is one: the endpoint may not exist.
func outage(pe *provider.Error) bool {
	return pe.Status >= 500 || pe.Status == http.StatusTooManyRequests
}

// maxOutageDelay is the longest wait between two attempts of a request that keeps failing for an outage: often enough to
// notice when the endpoint is back, rarely enough that a swarm of workers waiting together is not a load on it.
const maxOutageDelay = 30 * time.Second

// retryDelay says whether a request that failed on attempt (counting from 0) is tried again, and after how long. Every failure gets
// maxAttempts attempts, with a wait that doubles. A failure that is an outage gets more, while the waits add up to less than
// patience: they stop growing at maxOutageDelay, what the endpoint asked for (Retry-After, bounded by MaxRetryAfter) is still a
// floor, and the last wait is cut to what is left of the patience.
func retryDelay(attempt int, pe *provider.Error, waited, patience time.Duration) (delay time.Duration, again bool) {
	patient := patience > 0 && outage(pe) && waited < patience
	if attempt >= maxAttempts-1 && !patient {
		return 0, false
	}
	delay = backoff(attempt, pe.RetryAfter)
	if patient {
		if attempt >= maxAttempts-1 {
			delay = max(min(backoff(attempt, 0), maxOutageDelay), min(pe.RetryAfter, MaxRetryAfter))
		}
		delay = max(min(delay, patience-waited), 0)
	}
	return delay, true
}

// sayRetry says whether the retry after attempt (counted from 0) is announced to the person: each of the usual attempts is, and while an outage is
// waited out after them, the first and then one in each half minute of waiting. announced is the half minute of the last such notice (-1: none).
func sayRetry(attempt int, waited time.Duration, announced int) (say bool, half int) {
	if attempt < maxAttempts-1 {
		return true, -1 // the usual attempts are all said, and do not use up a half minute
	}
	half = int(waited / (30 * time.Second))
	return half > announced, half
}

// call performs a provider request with retry and rate-limit gating.
func (a *Agent) call(ctx context.Context, req *provider.Request, prio int, on func(provider.Event)) (*provider.Response, error) {
	return a.callOn(ctx, a.cfg.Provider, req, prio, on)
}

// callOn is call on a named provider: the compactor's, when the session gives it a model of its own.
func (a *Agent) callOn(ctx context.Context, prov provider.Provider, req *provider.Request, prio int, on func(provider.Event)) (*provider.Response, error) {
	var last error
	var waited time.Duration // how long the outage has been waited out, for OutagePatience
	announced := -1          // the half-minute of waiting that the last notice of the patient phase was made in
	for attempt := 0; ; attempt++ {
		rel, err := a.cfg.Limiter.Acquire(ctx, prio)
		if err != nil {
			return nil, err
		}
		resp, err := prov.Do(ctx, req, on)
		if resp != nil {
			rel(&resp.Usage, err)
		} else {
			rel(nil, err)
		}
		if err == nil {
			return resp, nil
		}
		pe, ok := provider.AsError(err)
		if !ok || !pe.Retryable() || ctx.Err() != nil {
			return nil, err
		}
		last = err
		delay, again := retryDelay(attempt, pe, waited, a.cfg.OutagePatience)
		if !again {
			break // that was the last one: nothing follows it, so there is nothing to wait for or to announce
		}
		waited += delay
		of := fmt.Sprintf("attempt %d of %d", attempt+2, maxAttempts)
		if attempt >= maxAttempts-1 {
			of = fmt.Sprintf("attempt %d, waited %s of %s for the endpoint", attempt+2, waited.Round(time.Second), a.cfg.OutagePatience.Round(time.Second))
		}
		// The first six attempts are each said; while an outage is waited out after them, a line a half minute is enough (a person watching saw
		// seven lines of the same 503 in a minute, and the status line already says how long the model has not answered).
		if say, half := sayRetry(attempt, waited, announced); say {
			a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("%s (%s)", retryNotice(pe, delay), of))
			announced = half
		}
		a.emit(events.TypeModelError, map[string]any{
			"req": req.Label, "kind": pe.Kind.String(), "status": pe.Status, "attempt": attempt + 1, "delay_ms": delay.Milliseconds(),
		})
		if on != nil {
			on(provider.Event{Kind: provider.EvReset})
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, last
}

// backoff computes bounded exponential retry delay with jitter and honors a larger Retry-After
// value up to MaxRetryAfter; attempt must be nonnegative.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := RetryBase << min(attempt, 16) // the shift is bounded: a request that is retried for minutes must not overflow it
	if d > 60*time.Second || d <= 0 {
		d = 60 * time.Second
	}
	jitter := 0.75 + rand.Float64()*0.5
	d = time.Duration(float64(d) * jitter)
	if retryAfter > MaxRetryAfter {
		retryAfter = MaxRetryAfter // providers clamp too; the loop does not rely on it
	}
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

// noteTruncation notices a server that reads less of the prompt than it was sent (Ollama's OpenAI-compatible endpoint cuts the prompt to the
// model's window, 4096 tokens on a small graphics card, without a word). The sign is in the usage it reports: the prompt grew between two
// requests (by 5% of its bytes or more) and the tokens it reports did not (within 1%), at a size of 512 tokens or more. The window is then
// that many tokens, rounded up to 256: the agent plans its compactions against it from now on, and says so once.
func (a *Agent) noteTruncation(bytes, totalIn int) {
	a.mu.Lock()
	prevBytes, prevIn, done := a.truncBytes, a.truncIn, a.truncated
	a.truncBytes, a.truncIn = bytes, totalIn
	a.mu.Unlock()
	if done || prevIn == 0 || totalIn < 512 {
		return
	}
	diff := totalIn - prevIn
	if diff < 0 {
		diff = -diff
	}
	if float64(bytes) < 1.05*float64(prevBytes) || diff*100 > prevIn {
		return
	}
	window := (totalIn + 255) / 256 * 256
	a.mu.Lock()
	a.truncated = true
	if a.cfg.Model.ContextTokens == 0 || window < a.cfg.Model.ContextTokens {
		a.cfg.Model.ContextTokens = window
	}
	a.mu.Unlock()
	a.emit(events.TypeCacheAnomaly, map[string]any{"kind": "truncated_prompt", "reported_tokens": totalIn, "window": window})
	a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("the server read only about %d tokens of a prompt that grew: its context window is probably that small (Ollama's default is 4096 on a small card, and it cuts the rest off without telling anyone). "+
		"Raise it in the server (OLLAMA_CONTEXT_LENGTH, or num_ctx in a Modelfile) and say so with providers.<name>.options.context_window. Meanwhile the conversation is kept inside %d tokens", totalIn, window))
}
