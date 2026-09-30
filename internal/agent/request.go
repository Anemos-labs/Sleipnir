package agent

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider"
)

// RetryBase is the first backoff step; tests shrink it.
var RetryBase = time.Second

const maxAttempts = 6

// request renders the agent's prompt and performs one model call, with retry,
// cache accounting and drift detection.
func (a *Agent) request(ctx context.Context) (*provider.Response, error) {
	retriedContext := false
	for {
		resp, err := a.requestOnce(ctx)
		if err == nil {
			return resp, nil
		}
		// A prompt over the window is a compaction problem, not a failure: fold
		// the thread mechanically (no model call needed) and try again once.
		if pe, ok := provider.AsError(err); ok && pe.Kind == provider.ErrContextLength && !retriedContext && !a.cfg.NoCompaction {
			retriedContext = true
			a.cfg.Sink.Notice(a.cfg.ID, "warn", "context window exceeded; compacting mechanically")
			if cerr := a.emergencyCompact(ctx, "provider reported context length exceeded"); cerr == nil {
				continue
			}
		}
		return nil, err
	}
}

func (a *Agent) requestOnce(ctx context.Context) (*provider.Response, error) {
	a.mu.Lock()
	stack := a.stack
	stack.Thread = a.thread.Snapshot()
	epoch := a.epoch + stack.Thread.Epoch
	warmBefore := a.isWarmLocked(a.cfg.Now())
	a.reqN++
	n := a.reqN
	a.mu.Unlock()

	var hot []core.Block
	if a.cfg.Hot != nil {
		hot = a.cfg.Hot(a.cfg.ID)
	}
	prof := a.cfg.Provider.Profile()
	r := kv.Render(&stack, kv.RenderOpts{
		Hot: hot, Caps: prof.KVCaps(), Policy: a.cfg.KVPolicy, Params: a.cfg.Params,
		CacheKey: a.cacheKey(&stack), Est: a.est,
	})
	check := a.guard.Observe(r, epoch, a.est)
	if check.Drift {
		a.emit(events.TypeCacheAnomaly, map[string]any{
			"kind": "drift", "diverged": check.Diverged, "shared_blocks": check.SharedBlocks,
		})
		a.cfg.Sink.Notice(a.cfg.ID, "warn", "prompt prefix changed without a declared rebase (layer "+check.Diverged+"); cache will miss")
	}

	reqID := fmt.Sprintf("%s.%d", a.cfg.ID, n)
	a.recordRequest(reqID, r, hot, check, prof)

	started := func(bool) {}
	if a.cfg.Gate != nil {
		s, gerr := a.cfg.Gate.Enter(ctx, string(stack.GlobalKey()))
		if gerr != nil {
			return nil, gerr
		}
		started = s
	}
	start := a.cfg.Now()
	resp, err := a.call(ctx, &provider.Request{Prompt: r.Prompt, Label: reqID}, a.cfg.Priority, func(e provider.Event) {
		a.forward(e)
		if e.Kind == provider.EvStart {
			started(true)
		}
	})
	started(err == nil)
	if err != nil {
		a.emit(events.TypeModelError, map[string]any{"req": reqID, "error": err.Error()})
		return nil, err
	}

	u := resp.Usage
	usd := a.cfg.Model.Price.USD(u)
	if resp.CostUSD != nil {
		usd = *resp.CostUSD
	}
	a.mu.Lock()
	a.usage = a.usage.Add(u)
	a.costUSD += usd
	a.lastStart, a.lastHit, a.haveHit = start, u.HitRatio(), true
	a.mainReqs++
	a.mu.Unlock()
	a.est.Observe(kv.PromptBytes(r.Prompt), u.TotalInput())

	expected := check.SharedTokens
	anomaly := warmBefore && expected >= prof.Cache.MinPrefixTokens && u.CacheReadTokens < expected*7/10
	if anomaly {
		a.emit(events.TypeCacheAnomaly, map[string]any{
			"kind": "low_hit", "req": reqID, "expected_read": expected, "actual_read": u.CacheReadTokens,
			"diverged": check.Diverged,
		})
	}
	a.emit(events.TypeModelResponse, map[string]any{
		"req": reqID, "id": resp.ID, "model": resp.Model, "provider": resp.Provider,
		"usage": u, "cost_usd": usd, "gateway_cost": resp.CostUSD != nil,
		"hit_ratio": u.HitRatio(), "expected_read": expected, "anomaly": anomaly,
		"stop": resp.Stop, "ttfb_ms": resp.TTFB.Milliseconds(), "total_ms": resp.Total.Milliseconds(),
		"transformations": resp.Transformations,
	})
	a.cfg.Sink.Response(a.cfg.ID, resp, u.HitRatio())
	return resp, nil
}

// forward relays streaming events to the UI sink.
func (a *Agent) forward(e provider.Event) {
	switch e.Kind {
	case provider.EvText:
		a.cfg.Sink.Text(a.cfg.ID, e.Text)
	case provider.EvThinking:
		a.cfg.Sink.Thinking(a.cfg.ID, e.Text)
	}
}

// recordRequest logs the request as a recipe: layer hashes and sizes, thread
// range, hot-block hash. Layer texts are stored once in the blob store by hash,
// so a session with dozens of agents sharing a pinned prefix logs that prefix
// once, and the full prompt can be reconstructed exactly for training data.
func (a *Agent) recordRequest(reqID string, r *kv.Rendered, hot []core.Block, c kv.Check, prof provider.Profile) {
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
	a.emit(events.TypeModelRequest, map[string]any{
		"req": reqID, "model": r.Prompt.Model, "provider": prof.Name, "dialect": prof.Dialect,
		"sections": secs, "thread_from": from, "thread_to": to, "hot": hotHash,
		"cache_key": r.Prompt.CacheKey, "prefix_key": r.PrefixKey, "params": r.Prompt.Params,
		"breakpoints": r.Prompt.Breakpoints, "shared_blocks": c.SharedBlocks, "shared_tokens": c.SharedTokens,
		"tools": len(r.Prompt.Tools),
	})
}

func (a *Agent) storeLayerTexts(r *kv.Rendered) {
	s := a.Stack()
	for _, l := range []*kv.Layer{s.Const, s.Shared, s.RoleL, s.Notes, s.Spine} {
		if !l.Empty() {
			_, _ = a.cfg.Blobs.Put([]byte(l.Text()))
		}
	}
}

// cacheKey is the provider routing key: requests that share it are pinned to
// one engine, so agents with the same constitution and shared pin reuse one
// resident copy of that prefix. Shards spread a large swarm over several
// engines; each shard pays one cold prefill and then serves its agents.
func (a *Agent) cacheKey(s *kv.Stack) string {
	shard := 0
	if a.cfg.AffinityShards > 1 {
		h := fnv.New32a()
		h.Write([]byte(a.cfg.ID))
		shard = int(h.Sum32()) % a.cfg.AffinityShards
		if shard < 0 {
			shard = -shard
		}
	}
	sid := a.cfg.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	return fmt.Sprintf("sl:%s:%s:%d", sid, s.GlobalKey().Short(), shard)
}

// isWarmLocked estimates whether the agent's cached prefix is still resident.
// Callers hold a.mu.
func (a *Agent) isWarmLocked(now time.Time) bool {
	if a.mainReqs == 0 || a.lastStart.IsZero() {
		return false
	}
	if a.cfg.Planner.IsCold(a.lastStart, now, a.cfg.Model.Cache.DefaultTTL()) {
		return false
	}
	// The first request cannot show hits; it wrote the prefix, so the cache is
	// warm by construction. After that, engines without a modelled TTL evict
	// under memory pressure, so trust the measured hit ratio of the last request.
	return a.mainReqs == 1 || a.lastHit >= 0.3
}

// call performs a provider request with retry and rate-limit gating.
func (a *Agent) call(ctx context.Context, req *provider.Request, prio int, on func(provider.Event)) (*provider.Response, error) {
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rel, err := a.cfg.Limiter.Acquire(ctx, prio)
		if err != nil {
			return nil, err
		}
		resp, err := a.cfg.Provider.Do(ctx, req, on)
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
		delay := backoff(attempt, pe.RetryAfter)
		a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("%s; retrying in %s", pe.Kind, delay.Round(time.Millisecond)))
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

func backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := RetryBase << attempt
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	jitter := 0.75 + rand.Float64()*0.5
	d = time.Duration(float64(d) * jitter)
	if retryAfter > d {
		d = retryAfter
	}
	return d
}
