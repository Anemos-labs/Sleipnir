package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// compactionState tracks one agent's background compaction. At most one job
// runs at a time, and at most one finished patch waits for a commit moment.
type compactionState struct {
	running  bool
	ready    *readyPatch
	count    int
	failures int
	failedAt time.Time
	// cancel ends the job in flight (nil when none): Close and the interruption of a
	// run call it.
	cancel context.CancelFunc
	// prefixWarned is set once the notice about a pinned prefix that leaves no room
	// has been given; lastEmergencyErr is why the previous emergency compaction failed.
	prefixWarned     bool
	lastEmergencyErr string
}

// compactionJobTimeout bounds one background compaction request, on top of its
// cancellation with the agent or the run it was started for.
const compactionJobTimeout = 3 * time.Minute

// UnverifiedPrefix starts the text of every promotion handed to OnPromote: the board
// shows a proposal as it is written, and what a compactor proposes is unverified.
const UnverifiedPrefix = "(unverified) "

// readyPatch is a validated patch computed against a snapshot. It is committed
// with a compare-and-swap on the thread epoch, so the agent keeps running while
// the compactor thinks, and turns appended in the meantime survive the commit
// (re-written by the provider, but not lost).
type readyPatch struct {
	res      *kv.ApplyResult
	patch    *kv.Patch
	epoch    uint64
	snapLen  int
	at       time.Time
	atReq    int // the agent's request counter when the patch became ready
	reason   string
	itc      float64 // compactor call cost, input-token equivalents
	fallback bool    // mechanical patch used instead of the model's
	snapLive int     // live thread tokens, as sent, when the snapshot was taken
	manual   bool    // a person's /compact: the session announces it to the hooks, not the agent
}

// compactionCooldown avoids hammering a failing compactor.
const compactionCooldown = 30 * time.Second

// boundary runs between turns: the only place layers change. It commits a ready
// compaction patch or starts a new compaction according to the cost planner.
// (Thinking is never stripped here: every rebase strips it in the same atomic
// step that performs it, see commit and SyncShared.)
func (a *Agent) boundary(ctx context.Context) {
	if a.cfg.NoCompaction {
		return
	}
	a.mu.Lock()
	st := a.plannerStateLocked()
	rp := a.comp.ready
	running := a.comp.running
	cooling := !a.comp.failedAt.IsZero() && a.cfg.Now().Sub(a.comp.failedAt) < compactionCooldown
	held := 0
	if rp != nil {
		held = a.reqN - rp.atReq
	}
	a.mu.Unlock()

	// A patch must not be able to hold compaction hostage: one computed against a
	// thread the agent has since outgrown, or one that has waited too long for a
	// commit moment, is dropped and proposed again below.
	if rp != nil {
		if stale, why := a.cfg.Planner.Stale(rp.snapLive, st.ThreadTokens, held); stale {
			a.mu.Lock()
			if a.comp.ready == rp {
				a.comp.ready = nil
			}
			a.mu.Unlock()
			a.emit(events.TypeCompactReject, map[string]any{"stage": "stale", "reason": why})
			rp = nil
		}
	}
	if rp != nil {
		// Priced against the live agent (hard and window pressure look at the live
		// thread), on the region the patch covers plus the tail it will re-write.
		a.mu.Lock()
		o := a.outcomeLocked(rp)
		a.mu.Unlock()
		d := a.cfg.Planner.ShouldCommit(st, o)
		a.emit(events.TypeCompactPlan, map[string]any{
			"decision": "commit?", "yes": d.Yes, "net_ite": d.NetITE, "reason": d.Reason, "warm": st.Warm,
			"thread_tokens": st.ThreadTokens, "snap_tokens": o.SnapTokens, "tail_tokens": o.TailTokens,
			"age_ms": a.cfg.Now().Sub(rp.at).Milliseconds(), "held_requests": held,
		})
		if d.Yes {
			if err := a.commit(ctx, rp, d.Reason); err != nil {
				a.emit(events.TypeCompactReject, map[string]any{"reason": err.Error()})
			}
			return
		}
		// Held. The safety net still runs: a prompt about to blow the window cannot
		// wait for an economic moment.
		if a.overWindow(st) {
			a.overWindowCompact(ctx, st)
		}
		return
	}
	if running || cooling {
		// Safety net: a prompt about to blow the window cannot wait for a
		// background job that may never land.
		if a.overWindow(st) && !running {
			a.overWindowCompact(ctx, st)
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
		a.emit(events.TypeCompactPlan, map[string]any{
			"decision": "start", "mode": modeName(d.Mode), "reason": d.Reason, "warm": st.Warm,
			"thread_tokens": st.ThreadTokens, "maskable_tokens": st.MaskableTokens, "net_ite": d.NetITE,
		})
		if d.Mode == kv.ModeMask {
			a.mu.Lock()
			a.lastMaskReq = a.reqN // throttle on attempts too, so a failing mask cannot storm
			a.mu.Unlock()
			if err := a.maskCommit(ctx, d.Reason, false); err != nil {
				a.emit(events.TypeCompactReject, map[string]any{"stage": "mask", "reason": err.Error()})
			}
		} else {
			a.startCompaction(ctx, d.Reason)
		}
	}
	if a.overWindow(st) {
		a.overWindowCompact(ctx, st)
	}
}

// callHook runs a hook callback that has no outcome the agent depends on.
func (a *Agent) callHook(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			a.emit("agent.panic", map[string]any{"id": a.cfg.ID, "where": what + " hook", "panic": fmt.Sprint(r), "stack": string(debug.Stack())})
		}
	}()
	fn()
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

// stackPrefixTokens is the size of the tools and the three stable layers of s.
func (a *Agent) stackPrefixTokens(s kv.Stack) int {
	prefix := 0
	for _, t := range s.Tools {
		prefix += a.est.Tokens(t.Name) + a.est.Tokens(t.Description) + a.est.Tokens(string(t.InputSchema)) + 8
	}
	for _, l := range []*kv.Layer{s.Const, s.Shared, s.RoleL} {
		prefix += l.Tokens(a.est)
	}
	return prefix
}

// plannerStateLocked measures the agent for the planner, in tokens as sent.
// Callers hold a.mu.
func (a *Agent) plannerStateLocked() kv.State {
	s := a.stack
	s.Thread = a.thread.Snapshot()
	prof := a.cfg.Provider.Profile()
	caps := a.caps(prof)
	z := kv.Sizer{Est: a.est, Caps: caps}
	prefix := a.stackPrefixTokens(s)
	notes, spine := s.Notes.Tokens(a.est), s.Spine.Tokens(a.est)
	thread := z.Turns(s.Thread.Turns)
	w := a.cfg.Model.Price.Weights()
	explicit := caps.MaxBreakpoints > 0
	write := w.Write5m
	if !explicit {
		write = 1 // engines with automatic prefix caching charge no write premium
	}
	pol := a.cfg.ApplyPolicy
	pol.Caps = caps
	// The agent knows the step cap of this run; beyond that it has no idea how much
	// work is left, so the planner's horizon applies.
	remaining := 0.0
	horizon := a.cfg.Planner.HorizonTurns
	if horizon == 0 {
		horizon = kv.DefaultPlanner().HorizonTurns
	}
	if left := float64(a.cfg.MaxSteps - a.stepInRun); left < horizon {
		remaining = left
		if remaining < 1 {
			remaining = 1
		}
	}
	return kv.State{
		PrefixTokens: prefix, NotesTokens: notes, SpineTokens: spine,
		ThreadTokens: thread, PromptTokens: prefix + notes + spine + thread,
		ContextWindow: a.cfg.Model.ContextTokens, Warm: a.isWarmLocked(a.cfg.Now()),
		Remaining: remaining, W: w, Write: write, Explicit: explicit,
		MaskableTokens: kv.MaskableTokens(&s, a.est, pol), SinceMask: a.reqN - a.lastMaskReq,
	}
}

// outcomeLocked prices a ready patch against the agent as it is now: what it
// replaces, and the turns appended since that an earlier request already sent
// (they sit behind the changed prefix and are re-written too). Callers hold a.mu.
func (a *Agent) outcomeLocked(rp *readyPatch) kv.Outcome {
	res := rp.res
	o := kv.Outcome{
		SnapTokens: res.SnapTokens, SpineAdded: res.SpineAdded, RetainedTokens: res.RetainedTokens,
		SpineAfter: res.SpineAfter, SpineRewritten: res.SpineEvicted > 0,
		NotesChanged: res.NotesChanged, NotesAfter: res.NotesAfter,
		CompactorITE: rp.itc,
	}
	turns := a.thread.Snapshot().Turns
	end := a.lastReqLen
	if end > len(turns) {
		end = len(turns)
	}
	if rp.snapLen < end {
		z := kv.Sizer{Est: a.est, Caps: a.caps(a.cfg.Provider.Profile())}
		o.TailTokens = z.Turns(turns[rp.snapLen:end])
	}
	return o
}

// startCompaction launches the compactor in the background.
//
// The job belongs to the agent, not to the goroutine that happened to start it: it
// runs on the agent's lifetime (Close cancels it and waits for it), it is cancelled when
// the run it was started for is interrupted (see Run), it is bounded by
// compactionJobTimeout, and a panic in it (a provider adapter, the renderer, a sink) is
// contained and reported as a failed compaction instead of taking the process down. It
// deliberately survives a run that ends normally: an interactive agent's Run returns
// after every answer and the patch is committed at a later boundary.
func (a *Agent) startCompaction(ctx context.Context, reason string) {
	a.mu.Lock()
	if a.closed || a.comp.running || a.comp.ready != nil || ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	a.comp.running = true
	snap := a.stack
	snap.Thread = a.thread.Snapshot()
	jobCtx, cancel := context.WithTimeout(a.life, compactionJobTimeout)
	a.comp.cancel = cancel
	a.jobs.Add(1)
	a.mu.Unlock()
	// The run may have been cancelled between the check above and the registration of
	// cancel, in which case its AfterFunc found nothing to cancel.
	if ctx.Err() != nil {
		cancel()
	}

	go func() {
		defer a.jobs.Done()
		defer cancel()
		var (
			rp  *readyPatch
			err error
		)
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("the compactor panicked: %v", r)
					a.emit("agent.panic", map[string]any{"id": a.cfg.ID, "where": "compactor", "panic": fmt.Sprint(r), "stack": string(debug.Stack())})
				}
			}()
			rp, err = a.propose(jobCtx, snap, reason)
		}()
		a.mu.Lock()
		a.comp.running = false
		a.comp.cancel = nil
		if err != nil {
			a.comp.failures++
			a.comp.failedAt = a.cfg.Now()
		} else {
			rp.atReq = a.reqN
			a.comp.ready = rp
			a.comp.failures = 0
		}
		a.mu.Unlock()
		if err != nil {
			a.emit(events.TypeCompactReject, map[string]any{"reason": err.Error(), "stage": "propose"})
		}
	}()
}

// cancelCompaction cancels the compaction job in flight, if any.
func (a *Agent) cancelCompaction() {
	a.mu.Lock()
	cancel := a.comp.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// propose asks the model (as a fork of this agent) for a patch, validates it
// and computes its effect. Any failure falls back to a mechanical patch, so a
// triggered compaction always yields something applicable.
//
// The fork is the agent's own request byte for byte plus one instruction block,
// with the same parameters: on Anthropic a different tool_choice, thinking or
// effort setting would invalidate the whole messages tier and turn the fork's
// cache read into a full write. "Do not call tools" is therefore only text, and a
// reply that calls one anyway is not run: it counts as a failed compaction only when
// what the model said holds no patch.
func (a *Agent) propose(ctx context.Context, snap kv.Stack, reason string) (*readyPatch, error) {
	return a.proposeFocus(ctx, snap, reason, "")
}

// proposeFocus is propose with the user's own words about what matters (manual
// /compact): they are typed by the person driving the agent, so they may steer the
// compactor, unlike text the agent found in tool output.
func (a *Agent) proposeFocus(ctx context.Context, snap kv.Stack, reason, focus string) (*readyPatch, error) {
	prov, model := a.compactorFor(snap)
	prof := prov.Profile()
	pol := a.applyPolicy()
	instr := kv.Instruction(&snap, a.est, pol)
	if focus = strings.TrimSpace(focus); focus != "" {
		// The person's own words, so they may steer the compactor; still one escaped,
		// bounded line, so they cannot close the brief either.
		line := "\nThe user asked for this compaction and says what matters most: " + kv.EscapeLine(focus, 400) + "\n"
		instr = strings.Replace(instr, "</compactor-task>", line+"</compactor-task>", 1)
	}
	p := kv.ForkPrompt(&snap, kv.RenderOpts{
		Caps: a.caps(prof), Policy: a.cfg.KVPolicy, Params: a.cfg.Params,
		CacheKey: a.cacheKey(&snap), Est: a.est,
	}, instr)
	a.mu.Lock()
	a.forkN++
	label := fmt.Sprintf("%s.c%d", a.cfg.ID, a.forkN)
	a.mu.Unlock()
	a.recordRequest(label, &kv.Rendered{Prompt: p, Sections: nil}, nil, kv.Check{}, prof, KindCompactor, false)
	a.emit(events.TypeCompactPatch, map[string]any{"stage": "request", "reason": reason, "thread_from": firstID(snap), "thread_to": lastID(snap)})

	z := kv.Sizer{Est: a.est, Caps: pol.Caps}
	snapLive := z.Turns(snap.Thread.Turns)
	rp := &readyPatch{epoch: snap.Thread.Epoch, snapLen: len(snap.Thread.Turns), at: a.cfg.Now(), reason: reason, snapLive: snapLive}
	var patch *kv.Patch
	resp, err := a.callOn(ctx, prov, &provider.Request{Prompt: p, Label: label, Capture: a.cfg.CaptureTokens}, PrioBackground, nil)
	if err == nil {
		a.account(resp, label, model)
		w := model.Price.Weights()
		rp.itc = float64(resp.Usage.InputTokens) + w.Read*float64(resp.Usage.CacheReadTokens) +
			w.Write5m*float64(resp.Usage.CacheWriteTokens()) + float64(resp.Usage.OutputTokens)*w.Output
		// Only what the model said, never its reasoning: a thinking block that
		// drafts a JSON shape must not be mistaken for the answer.
		patch, err = kv.ParsePatch(kv.AnswerText(resp.Turn))
		if calls := len(resp.Turn.ToolCalls()); calls > 0 {
			// The call is never run. When the text holds a patch it is judged like any other (some models call a tool in the same
			// reply: 20 of one model's 127 compactions on a benchmark were refused for it with a valid patch in them); when it does
			// not, the reply was a tool call instead of a patch.
			if err != nil {
				err = errors.New("the compactor answered with a tool call instead of a patch")
			} else {
				patch.Warnings = append(patch.Warnings, "the reply also called a tool, which was not run")
			}
		}
	}
	var res *kv.ApplyResult
	if err == nil {
		res, err = kv.Apply(&snap, patch, a.est, pol)
	}
	if err != nil {
		// Fallback: deterministic compaction. Never leave the agent stuck with a
		// full thread because a model answered badly.
		a.emit(events.TypeCompactReject, map[string]any{"stage": "model_patch", "reason": err.Error(), "fallback": "mechanical"})
		target := snapLive / 4
		if target < 2000 {
			target = 2000
		}
		patch = kv.MechanicalPatch(&snap, a.est, target, pol)
		res, err = kv.Apply(&snap, patch, a.est, pol)
		if err != nil {
			return nil, err
		}
		rp.fallback = true
	}
	rp.patch, rp.res = patch, res
	a.emit(events.TypeCompactPatch, map[string]any{
		"stage": "ready", "keep_from": res.KeepFrom, "removed_tokens": res.RemovedTokens, "retained_tokens": res.RetainedTokens,
		"snap_tokens": res.SnapTokens, "spine_added": res.SpineAdded, "masked": res.MaskedResults, "mechanical_lines": res.Mechanical,
		"fallback": rp.fallback, "warnings": res.Warnings, "cost_ite": rp.itc,
	})
	return rp, nil
}

// compactorFor picks who writes the patch for snap: the session's compactor model while the thread fits its
// window with room for the answer, else the agent's own.
func (a *Agent) compactorFor(snap kv.Stack) (provider.Provider, cost.Model) {
	c := a.cfg.Compactor
	if c == nil {
		return a.cfg.Provider, a.cfg.Model
	}
	z := kv.Sizer{Est: a.est, Caps: a.caps(c.Profile())}
	if w := a.cfg.CompactorModel.ContextTokens; w > 0 && z.Turns(snap.Thread.Turns)+a.stackPrefixTokens(snap) < w*3/4 {
		return c, a.cfg.CompactorModel
	}
	return a.cfg.Provider, a.cfg.Model
}

// account books a side request (compactor, curator) against the agent.
func (a *Agent) account(resp *provider.Response, label string, model cost.Model) {
	usd := model.Price.USD(resp.Usage)
	if resp.CostUSD != nil {
		usd = *resp.CostUSD
	}
	a.mu.Lock()
	a.usage = a.usage.Add(resp.Usage)
	a.costUSD += usd
	a.mu.Unlock()
	a.emit(events.TypeModelResponse, a.responsePayload(map[string]any{
		"req": label, "id": resp.ID, "model": resp.Model, "usage": resp.Usage, "cost_usd": usd,
		"hit_ratio": resp.Usage.HitRatio(), "side": true, "stop": resp.Stop,
	}, resp))
}

// commit applies a ready patch: an atomic replacement of the retained thread
// plus new spine and notes layers, followed by a declared rebase.
//
// The turns appended while the patch was being computed are carried over, and
// they were produced against the prefix this commit replaces: on a route that
// enforces preserved thinking their thinking blocks are void, so they are
// stripped in the same atomic step (the retained region already was, by Apply).
func (a *Agent) commit(ctx context.Context, rp *readyPatch, why string) error {
	ch, _ := a.cfg.Hooks.(CompactionHooks)
	// A patch computed for a thread that has been rebased since is rejected below;
	// the hooks are not told of a compaction that is not going to happen.
	if ch == nil || rp.manual || a.thread.Snapshot().Epoch != rp.epoch {
		return a.applyCommit(rp, why)
	}
	// An automatic compaction: the agent's hooks hear of it just before it is applied
	// (the moment to save what is about to be folded away) and just after, only if it
	// was. A person's /compact is announced by the session instead.
	a.callHook("before compact", func() { ch.BeforeCompact(ctx, a.cfg.ID, a.cfg.Role, why) })
	if err := a.applyCommit(rp, why); err != nil {
		return err
	}
	a.callHook("after compact", func() { ch.AfterCompact(ctx, a.cfg.ID, a.cfg.Role, why) })
	return nil
}

// applyCommit is the atomic part of commit.
func (a *Agent) applyCommit(rp *readyPatch, why string) error {
	res := rp.res
	a.mu.Lock()
	if err := a.thread.CommitWith(rp.epoch, res.Replacement, rp.snapLen, a.stripTurn); err != nil {
		a.comp.ready = nil
		a.mu.Unlock()
		return fmt.Errorf("commit rejected: %w", err)
	}
	a.stack.Spine = res.Spine
	if res.NotesChanged {
		a.stack.Notes = res.Notes
	}
	a.epoch++
	a.comp.ready = nil
	a.comp.count++
	a.resyncHotLocked()
	a.mu.Unlock()

	a.emit(events.TypeCompactCommit, map[string]any{
		"reason": why, "keep_from": res.KeepFrom, "removed_turns": res.RemovedTurns,
		"removed_tokens": res.RemovedTokens, "retained_tokens": res.RetainedTokens, "snap_tokens": res.SnapTokens,
		"spine_added": res.SpineAdded, "spine_tokens": res.SpineAfter, "spine_evicted": res.SpineEvicted,
		"masked": res.MaskedResults, "masked_tokens": res.MaskedTokens, "mechanical_lines": res.Mechanical,
		"squeezed": res.SqueezedResults, "squeezed_tokens": res.SqueezedTokens,
		"notes_changed": res.NotesChanged, "notes_tokens": res.NotesAfter, "notes_evicted": res.NotesEvicted,
		"notes_over_budget": res.NotesOverBudget, "notices_dropped": res.NoticesDropped,
		"user_text_cut": res.UserTextCut, "proposals": len(res.Proposals),
		"fallback": rp.fallback, "warnings": res.Warnings,
		"held_ms": a.cfg.Now().Sub(rp.at).Milliseconds(),
	})
	if len(res.UserTextCut) > 0 {
		// The one case in which what a person typed is not pinned in full.
		a.cfg.Sink.Notice(a.cfg.ID, "warn", "a message of yours is longer than the notes keep in full ("+strings.Join(res.UserTextCut, "; ")+"): its beginning and end stay pinned, and the whole text is in the archive (recall)")
	}
	if res.NotesOverBudget {
		// The consumer of the over-budget signal: the oldest lines were evicted (see
		// notes_evicted) and the next compactor instruction asks for a consolidation
		// pass while notes are near their budget (kv.Instruction).
		a.emit(events.TypeCacheAnomaly, map[string]any{"kind": "notes_over_budget", "evicted": res.NotesEvicted, "notes_tokens": res.NotesAfter})
	}
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
		// What the board shows is the text, so the proposal says what it is in it.
		props := make([]kv.Promotion, len(res.Proposals))
		for i, p := range res.Proposals {
			p.Text = UnverifiedPrefix + p.Text
			props[i] = p
		}
		a.cfg.OnPromote(a.cfg.ID, props)
	}
	a.cfg.Sink.Notice(a.cfg.ID, "info", fmt.Sprintf("compacted %d turns (%s→%s tokens)", res.RemovedTurns, tokenCount(res.SnapTokens), tokenCount(res.SpineAdded+res.RetainedTokens)))
	a.saveSnapshot()
	return nil
}

// overWindowCompact is the boundary's safety net: the prompt is over 85% of the
// window, so something is folded now, without a model. It never fails the run: a
// failure is recorded (once per distinct cause, since it would repeat at every step),
// and when the pinned prefix alone (tools, constitution, pins, notes, spine) already
// takes that much room, no compaction of the thread can help and the person is told
// once what to shrink.
func (a *Agent) overWindowCompact(ctx context.Context, st kv.State) {
	err := a.emergencyCompact(ctx, "prompt over 85% of the context window")
	if err == nil {
		return
	}
	a.mu.Lock()
	repeated := a.comp.lastEmergencyErr == err.Error()
	a.comp.lastEmergencyErr = err.Error()
	prefix := st.PrefixTokens + st.NotesTokens + st.SpineTokens
	warn := !a.comp.prefixWarned && st.ContextWindow > 0 && float64(prefix) >= 0.85*float64(st.ContextWindow)
	if warn {
		a.comp.prefixWarned = true
	}
	a.mu.Unlock()
	if !repeated {
		a.emit(events.TypeCompactReject, map[string]any{"stage": "emergency", "reason": err.Error(), "prefix_tokens": prefix, "window": st.ContextWindow})
	}
	if warn {
		a.cfg.Sink.Notice(a.cfg.ID, "warn", fmt.Sprintf("the pinned prefix alone is about %dk tokens, over 85%% of this model's %dk-token window: compacting the conversation cannot make room; shorten the project instruction files or use a model with a larger window", prefix/1000, st.ContextWindow/1000))
	}
}

// emergencyCompact compacts synchronously without a model.
func (a *Agent) emergencyCompact(ctx context.Context, reason string) error {
	a.mu.Lock()
	snap := a.stack
	snap.Thread = a.thread.Snapshot()
	a.comp.ready = nil
	a.mu.Unlock()
	pol := a.applyPolicy()
	live := kv.Sizer{Est: a.est, Caps: pol.Caps}.Turns(snap.Thread.Turns)
	target := live / 4
	if target < 2000 {
		target = 2000
	}
	patch := kv.MechanicalPatch(&snap, a.est, target, pol)
	res, err := kv.Apply(&snap, patch, a.est, pol)
	if errors.Is(err, kv.ErrNothingToCompact) {
		// Nothing older to fold (an empty thread, or a fresh agent whose first exchange
		// is what is too big): shrink the bulkiest tool results instead, if there are any.
		res, err = kv.SqueezeOnly(&snap, a.est, pol, target)
		patch = &kv.Patch{Target: target}
	}
	if err != nil {
		return err
	}
	rp := &readyPatch{res: res, patch: patch, epoch: snap.Thread.Epoch, snapLen: len(snap.Thread.Turns), at: a.cfg.Now(), reason: reason, fallback: true, snapLive: live}
	return a.commit(ctx, rp, "emergency: "+reason)
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
// It only commits when something was actually masked (kv.MaskOnly).
func (a *Agent) maskCommit(ctx context.Context, reason string, manual bool) error {
	a.mu.Lock()
	snap := a.stack
	snap.Thread = a.thread.Snapshot()
	a.mu.Unlock()
	pol := a.applyPolicy()
	res, err := kv.MaskOnly(&snap, a.est, pol)
	if err != nil {
		return err
	}
	live := res.SnapTokens
	rp := &readyPatch{res: res, epoch: snap.Thread.Epoch, snapLen: len(snap.Thread.Turns), at: a.cfg.Now(), reason: reason, fallback: true, snapLive: live, manual: manual}
	return a.commit(ctx, rp, "mask: "+reason)
}

// CompactReport says what a manual compaction did.
type CompactReport struct {
	// Mode is "model" (the compactor's patch), "mechanical" (its answer was
	// unusable, so a deterministic patch was used), "mask" (bulky results only) or
	// "none" (nothing to fold yet).
	Mode         string
	FoldedTurns  int
	TokensBefore int
	TokensAfter  int
}

// CompactNow folds the thread now, at the user's request, and waits for it. It
// takes the same path as the background compaction (propose, then commit), so it
// is a declared, priced rebase like any other: the user chose the moment, and the
// planner's economics do not get a vote. With focus, the compactor is told what
// the user cares about. Call it between runs.
func (a *Agent) CompactNow(ctx context.Context, focus string) (CompactReport, error) {
	a.mu.Lock()
	if a.comp.running {
		a.mu.Unlock()
		return CompactReport{}, errors.New("a compaction is already running; try again in a moment")
	}
	a.comp.running = true // hold the slot: the background planner must not start another
	a.comp.ready = nil
	snap := a.stack
	snap.Thread = a.thread.Snapshot()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.comp.running = false
		a.mu.Unlock()
	}()

	z := kv.Sizer{Est: a.est, Caps: a.applyPolicy().Caps}
	rep := CompactReport{Mode: "none", TokensBefore: z.Turns(snap.Thread.Turns)}
	switch {
	case a.foldableUnits() >= 2:
		rp, err := a.proposeFocus(ctx, snap, "manual /compact", focus)
		if errors.Is(err, kv.ErrNothingToCompact) {
			return rep, nil
		}
		if err != nil {
			return rep, err
		}
		rp.manual = true
		if err := a.commit(ctx, rp, "manual /compact"); err != nil {
			return rep, err
		}
		rep.Mode = "model"
		if rp.fallback {
			rep.Mode = "mechanical"
		}
		rep.FoldedTurns = rp.res.RemovedTurns
	case a.foldableUnits() >= 1:
		if err := a.maskCommit(ctx, "manual /compact", true); err != nil {
			if errors.Is(err, kv.ErrNothingToMask) {
				return rep, nil
			}
			return rep, err
		}
		rep.Mode = "mask"
	default:
		return rep, nil
	}
	rep.TokensAfter = z.Turns(a.thread.Snapshot().Turns)
	return rep, nil
}

// tokenCount is a number of tokens as a person reads it: whole below a thousand, one decimal below ten thousand, then thousands ("669", "2.4k",
// "31k"). It was thousands rounded down, which made a small conversation "0k→0k tokens".
func tokenCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%dk", n/1000)
}
