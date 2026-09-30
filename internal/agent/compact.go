package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/provider"
)

// compactionState tracks one agent's background compaction. At most one job
// runs at a time, and at most one finished patch waits for a commit moment.
type compactionState struct {
	running  bool
	ready    *readyPatch
	count    int
	failures int
	failedAt time.Time
}

// readyPatch is a validated patch computed against a snapshot. It is committed
// with a compare-and-swap on the thread epoch, so the agent keeps running while
// the compactor thinks, and turns appended in the meantime survive the commit.
type readyPatch struct {
	res       *kv.ApplyResult
	patch     *kv.Patch
	epoch     uint64
	snapLen   int
	at        time.Time
	reason    string
	itc       float64 // compactor call cost, input-token equivalents
	fallback  bool    // mechanical patch used instead of the model's
	snapTotal int     // thread tokens covered by the patch
}

// compactionCooldown avoids hammering a failing compactor.
const compactionCooldown = 30 * time.Second

// boundary runs between turns: the only place layers change. It first
// performs any deferred thinking strip, then commits or starts compaction
// according to the cost planner.
func (a *Agent) boundary(ctx context.Context) {
	a.stripPending()
	if a.cfg.NoCompaction {
		return
	}
	a.mu.Lock()
	st := a.plannerStateLocked()
	rp := a.comp.ready
	running := a.comp.running
	cooling := !a.comp.failedAt.IsZero() && a.cfg.Now().Sub(a.comp.failedAt) < compactionCooldown
	a.mu.Unlock()

	if rp != nil {
		// Evaluate on the region the patch covers; turns appended since ride
		// along unchanged either way.
		cs := st
		cs.ThreadTokens = rp.snapTotal
		d := a.cfg.Planner.ShouldCommit(cs, kv.Outcome{
			SpineAdded: rp.res.SpineAdded, RetainedTokens: rp.res.RetainedTokens, CompactorITE: rp.itc,
		})
		a.emit(events.TypeCompactPlan, map[string]any{
			"decision": "commit?", "yes": d.Yes, "net_ite": d.NetITE, "reason": d.Reason, "warm": st.Warm,
			"thread_tokens": st.ThreadTokens, "age_ms": a.cfg.Now().Sub(rp.at).Milliseconds(),
		})
		if d.Yes {
			if err := a.commit(rp, d.Reason); err != nil {
				a.emit(events.TypeCompactReject, map[string]any{"reason": err.Error()})
			}
		}
		return
	}
	if running || cooling {
		// Safety net: a prompt about to blow the window cannot wait for a
		// background job that may never land.
		if a.overWindow(st) && !running {
			_ = a.emergencyCompact(ctx, "prompt over 85% of the context window")
		}
		return
	}
	d := a.cfg.Planner.ShouldStart(st)
	// A model compaction needs something to fold: at least two units beyond the
	// ones that always stay verbatim. Masking only needs one.
	if d.Yes && d.Mode == kv.ModeFork && a.foldableUnits() < 2 {
		d.Yes = false
	}
	if d.Yes && d.Mode == kv.ModeMask && a.foldableUnits() < 1 {
		d.Yes = false
	}
	if d.Yes {
		a.emit(events.TypeCompactPlan, map[string]any{"decision": "start", "mode": modeName(d.Mode), "reason": d.Reason, "warm": st.Warm, "thread_tokens": st.ThreadTokens})
		if d.Mode == kv.ModeMask {
			if err := a.maskCommit(d.Reason); err != nil {
				a.emit(events.TypeCompactReject, map[string]any{"stage": "mask", "reason": err.Error()})
			}
		} else {
			a.startCompaction(ctx, d.Reason)
		}
	}
	if a.overWindow(st) {
		_ = a.emergencyCompact(ctx, "prompt over 85% of the context window")
	}
}

// foldableUnits counts thread units eligible for folding.
func (a *Agent) foldableUnits() int {
	keep := a.cfg.ApplyPolicy.MinKeepUnits
	if keep < 1 {
		keep = 1
	}
	return len(kv.Units(a.thread.Snapshot().Turns)) - keep
}

func (a *Agent) overWindow(st kv.State) bool {
	return st.ContextWindow > 0 && float64(st.PromptTokens) >= 0.85*float64(st.ContextWindow)
}

// plannerStateLocked measures the agent for the planner. Callers hold a.mu.
func (a *Agent) plannerStateLocked() kv.State {
	s := a.stack
	s.Thread = a.thread.Snapshot()
	prefix := 0
	for _, t := range s.Tools {
		prefix += a.est.Tokens(t.Name) + a.est.Tokens(t.Description) + a.est.Tokens(string(t.InputSchema)) + 8
	}
	for _, l := range []*kv.Layer{s.Const, s.Shared, s.RoleL, s.Notes, s.Spine} {
		prefix += l.Tokens(a.est)
	}
	thread := s.Thread.Tokens(a.est)
	w := a.cfg.Model.Price.Weights()
	write := w.Write5m
	if a.cfg.Model.Cache.Auto && !a.cfg.Model.Cache.Explicit {
		write = 1 // engines with automatic prefix caching charge no write premium
	}
	return kv.State{
		PrefixTokens: prefix, ThreadTokens: thread, PromptTokens: prefix + thread,
		ContextWindow: a.cfg.Model.ContextTokens, Warm: a.isWarmLocked(a.cfg.Now()),
		W: w, Write: write,
	}
}

// startCompaction launches the compactor in the background.
func (a *Agent) startCompaction(ctx context.Context, reason string) {
	a.mu.Lock()
	if a.comp.running || a.comp.ready != nil {
		a.mu.Unlock()
		return
	}
	a.comp.running = true
	snap := a.stack
	snap.Thread = a.thread.Snapshot()
	a.mu.Unlock()

	// The job outlives the current Run (an interactive agent's Run returns
	// after every answer) but not the process.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	go func() {
		defer cancel()
		rp, err := a.propose(jobCtx, snap, reason)
		a.mu.Lock()
		a.comp.running = false
		if err != nil {
			a.comp.failures++
			a.comp.failedAt = a.cfg.Now()
		} else {
			a.comp.ready = rp
			a.comp.failures = 0
		}
		a.mu.Unlock()
		if err != nil {
			a.emit(events.TypeCompactReject, map[string]any{"reason": err.Error(), "stage": "propose"})
		}
	}()
}

// propose asks the model (as a fork of this agent) for a patch, validates it
// and computes its effect. Any failure falls back to a mechanical patch, so a
// triggered compaction always yields something applicable.
func (a *Agent) propose(ctx context.Context, snap kv.Stack, reason string) (*readyPatch, error) {
	prof := a.cfg.Provider.Profile()
	instr := kv.Instruction(&snap, a.est, a.cfg.ApplyPolicy)
	p := kv.ForkPrompt(&snap, kv.RenderOpts{
		Caps: prof.KVCaps(), Policy: a.cfg.KVPolicy, Params: a.cfg.Params,
		CacheKey: a.cacheKey(&snap), Est: a.est,
	}, instr)
	label := "compactor:" + a.cfg.ID
	a.emit(events.TypeCompactPatch, map[string]any{"stage": "request", "reason": reason, "thread_from": firstID(snap), "thread_to": lastID(snap)})

	rp := &readyPatch{epoch: snap.Thread.Epoch, snapLen: len(snap.Thread.Turns), at: a.cfg.Now(), reason: reason}
	var patch *kv.Patch
	resp, err := a.call(ctx, &provider.Request{Prompt: p, Label: label}, PrioBackground, nil)
	if err == nil {
		a.account(resp, label)
		w := a.cfg.Model.Price.Weights()
		rp.itc = float64(resp.Usage.InputTokens) + w.Read*float64(resp.Usage.CacheReadTokens) + float64(resp.Usage.OutputTokens)*w.Output
		patch, err = kv.ParsePatch(resp.Turn.PlainText())
	}
	var res *kv.ApplyResult
	if err == nil {
		res, err = kv.Apply(&snap, patch, a.est, a.cfg.ApplyPolicy)
	}
	if err != nil {
		// Fallback: deterministic compaction. Never leave the agent stuck with a
		// full thread because a model answered badly.
		a.emit(events.TypeCompactReject, map[string]any{"stage": "model_patch", "reason": err.Error(), "fallback": "mechanical"})
		target := snap.Thread.Tokens(a.est) / 4
		if target < 2000 {
			target = 2000
		}
		patch = kv.MechanicalPatch(&snap, a.est, target, a.cfg.ApplyPolicy)
		res, err = kv.Apply(&snap, patch, a.est, a.cfg.ApplyPolicy)
		if err != nil {
			return nil, err
		}
		rp.fallback = true
	}
	rp.patch, rp.res = patch, res
	rp.snapTotal = res.RemovedTokens + res.RetainedTokens
	a.emit(events.TypeCompactPatch, map[string]any{
		"stage": "ready", "keep_from": res.KeepFrom, "removed_tokens": res.RemovedTokens, "retained_tokens": res.RetainedTokens,
		"spine_added": res.SpineAdded, "masked": res.MaskedResults, "mechanical_lines": res.Mechanical,
		"fallback": rp.fallback, "warnings": res.Warnings, "cost_ite": rp.itc,
	})
	return rp, nil
}

// account books a side request (compactor, curator) against the agent.
func (a *Agent) account(resp *provider.Response, label string) {
	usd := a.cfg.Model.Price.USD(resp.Usage)
	if resp.CostUSD != nil {
		usd = *resp.CostUSD
	}
	a.mu.Lock()
	a.usage = a.usage.Add(resp.Usage)
	a.costUSD += usd
	a.mu.Unlock()
	a.emit(events.TypeModelResponse, map[string]any{
		"req": label, "id": resp.ID, "model": resp.Model, "usage": resp.Usage, "cost_usd": usd,
		"hit_ratio": resp.Usage.HitRatio(), "side": true, "stop": resp.Stop,
	})
}

// commit applies a ready patch: an atomic replacement of the retained thread
// plus new spine and notes layers, followed by a declared rebase.
func (a *Agent) commit(rp *readyPatch, why string) error {
	res := rp.res
	if err := a.thread.Commit(rp.epoch, res.Replacement, rp.snapLen); err != nil {
		a.mu.Lock()
		a.comp.ready = nil
		a.mu.Unlock()
		return fmt.Errorf("commit rejected: %w", err)
	}
	a.mu.Lock()
	a.stack.Spine = res.Spine
	if res.NotesChanged {
		a.stack.Notes = res.Notes
	}
	a.epoch++
	a.comp.ready = nil
	a.comp.count++
	a.mu.Unlock()

	a.emit(events.TypeCompactCommit, map[string]any{
		"reason": why, "keep_from": res.KeepFrom, "removed_turns": res.RemovedTurns,
		"removed_tokens": res.RemovedTokens, "retained_tokens": res.RetainedTokens, "spine_added": res.SpineAdded,
		"masked": res.MaskedResults, "mechanical_lines": res.Mechanical, "notes_changed": res.NotesChanged,
		"notes_over_budget": res.NotesOverBudget, "fallback": rp.fallback, "warnings": res.Warnings,
		"held_ms": a.cfg.Now().Sub(rp.at).Milliseconds(),
	})
	var spineVer uint64
	if res.Spine != nil {
		spineVer = res.Spine.Version
	}
	a.emit(events.TypeLayerCommit, map[string]any{
		"scope": "agent", "spine": res.Spine.Hash().Short(), "spine_version": spineVer,
		"notes": res.Notes.Hash().Short(), "notes_changed": res.NotesChanged,
	})
	for _, l := range []*kv.Layer{res.Spine, res.Notes} {
		if !l.Empty() {
			_, _ = a.cfg.Blobs.Put([]byte(l.Text()))
		}
	}
	if len(res.Proposals) > 0 && a.cfg.OnPromote != nil {
		a.cfg.OnPromote(a.cfg.ID, res.Proposals)
	}
	a.cfg.Sink.Notice(a.cfg.ID, "info", fmt.Sprintf("compacted %d turns (%dk→%dk tokens)", res.RemovedTurns, (res.RemovedTokens+res.RetainedTokens)/1000, (res.SpineAdded+res.RetainedTokens)/1000))
	return nil
}

// emergencyCompact compacts synchronously without a model.
func (a *Agent) emergencyCompact(ctx context.Context, reason string) error {
	a.mu.Lock()
	snap := a.stack
	snap.Thread = a.thread.Snapshot()
	a.comp.ready = nil
	a.mu.Unlock()
	target := snap.Thread.Tokens(a.est) / 4
	if target < 2000 {
		target = 2000
	}
	patch := kv.MechanicalPatch(&snap, a.est, target, a.cfg.ApplyPolicy)
	res, err := kv.Apply(&snap, patch, a.est, a.cfg.ApplyPolicy)
	if err != nil {
		return err
	}
	rp := &readyPatch{res: res, patch: patch, epoch: snap.Thread.Epoch, snapLen: len(snap.Thread.Turns), at: a.cfg.Now(), reason: reason, fallback: true}
	return a.commit(rp, "emergency: "+reason)
}

// stripPending removes thinking blocks from the thread after a shared-layer
// epoch. Their provider-side bindings are void once the prefix changed; leaving
// them in would trigger rejections or, worse, silent drops that vary from
// request to request. Stripping is done once, durably, as part of the rebase.
func (a *Agent) stripPending() {
	a.mu.Lock()
	need := a.strip
	a.strip = false
	a.mu.Unlock()
	if !need || !a.cfg.Provider.Profile().ReplayThinking {
		return
	}
	snap := a.thread.Snapshot()
	changed := false
	out := make([]core.Turn, len(snap.Turns))
	for i, tr := range snap.Turns {
		nb := make([]core.Block, 0, len(tr.Blocks))
		for _, b := range tr.Blocks {
			if b.Kind == core.BlockThinking || b.Kind == core.BlockRedactedThinking {
				changed = true
				continue
			}
			nb = append(nb, b)
		}
		tr.Blocks = nb
		out[i] = tr
	}
	if changed {
		_ = a.thread.Commit(snap.Epoch, out, len(snap.Turns))
	}
}

func firstID(s kv.Stack) core.TurnID {
	if len(s.Thread.Turns) == 0 {
		return 0
	}
	return s.Thread.Turns[0].ID
}

func lastID(s kv.Stack) core.TurnID {
	if len(s.Thread.Turns) == 0 {
		return 0
	}
	return s.Thread.Turns[len(s.Thread.Turns)-1].ID
}

func modeName(m kv.Mode) string {
	if m == kv.ModeMask {
		return "mask"
	}
	return "fork"
}

// maskCommit performs the deterministic compaction immediately, at a boundary.
func (a *Agent) maskCommit(reason string) error {
	a.mu.Lock()
	snap := a.stack
	snap.Thread = a.thread.Snapshot()
	a.mu.Unlock()
	res, err := kv.MaskOnly(&snap, a.est, a.cfg.ApplyPolicy)
	if err != nil {
		return err
	}
	rp := &readyPatch{res: res, epoch: snap.Thread.Epoch, snapLen: len(snap.Thread.Turns), at: a.cfg.Now(), reason: reason, fallback: true}
	return a.commit(rp, "mask: "+reason)
}
