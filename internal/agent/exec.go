package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/tools"
)

// maxParallelTools bounds concurrent read-only tool calls from one turn.
const maxParallelTools = 8

// runTools executes a turn's tool calls and returns tool_result blocks in call
// order. Consecutive read-only calls run concurrently; anything that writes is a
// barrier and runs alone, in order, so a model's sequence of edits keeps its
// meaning while its searches and reads fan out.
//
// A turn is bounded twice (S24). Only the first MaxToolCallsPerTurn calls run; every
// other call is answered with an error that tells the model to issue fewer, so each
// call still has its result and the thread stays valid. And the results together
// may put at most MaxTurnResultChars into the next request: see budgetResults.
func (a *Agent) runTools(ctx context.Context, calls []core.Block) []core.Block {
	results := make([]core.Block, len(calls))
	run := len(calls)
	if limit := a.cfg.MaxToolCallsPerTurn; limit > 0 && run > limit {
		run = limit
	}
	for i := 0; i < run; {
		if a.readOnly(calls[i]) {
			j := i
			for j < run && a.readOnly(calls[j]) {
				j++
			}
			var wg sync.WaitGroup
			sem := make(chan struct{}, maxParallelTools)
			for k := i; k < j; k++ {
				wg.Add(1)
				sem <- struct{}{}
				go func(k int) {
					defer wg.Done()
					defer func() { <-sem }()
					results[k] = a.runOne(ctx, calls[k])
				}(k)
			}
			wg.Wait()
			i = j
			continue
		}
		results[i] = a.runOne(ctx, calls[i])
		i++
	}
	for i := run; i < len(calls); i++ {
		results[i] = a.refuseCall(calls[i], len(calls), run)
	}
	a.budgetResults(results, len(calls)-run)
	return results
}

// refuseCall answers a call that was not run because its turn asked for too many.
func (a *Agent) refuseCall(call core.Block, asked, limit int) core.Block {
	msg := fmt.Sprintf("Not run: this turn asked for %d tool calls and at most %d are run per turn. Issue fewer calls (a grep or a glob instead of many reads, several edits in one apply_patch) and call this one again if you still need it.", asked, limit)
	res := tools.Errorf("%s", msg)
	res.Meta = withErrorKind(res.Meta, ErrKindTooManyCalls)
	a.emit(events.TypeToolResult, map[string]any{
		"id": call.ToolID, "name": call.ToolName, "error": true, "chars": len(msg), "refused": true, "meta": res.Meta,
	})
	return core.ToolResult(call.ToolID, true, core.Text(msg))
}

// spillFloor is the most a result that does not fit keeps as its floor when its turn is
// over budget: enough to see what it was and to decide whether to page through the rest.
// With many results the floor shrinks (spillFloorFor) to no less than minSpillFloor, so
// that the floors of a turn of a hundred results do not add up to more than the budget.
const (
	spillFloor    = 1500
	minSpillFloor = 300
	// spillNoteRoom is about what the note naming a handle adds to an excerpt: a result
	// hardly longer than its floor is kept whole, since cutting it would not make it
	// shorter.
	spillNoteRoom = 200
)

// spillFloorFor is the floor for a turn of n results under budget characters: a quarter
// of the budget shared out between them, between minSpillFloor and spillFloor.
func spillFloorFor(budget, n int) int {
	return min(spillFloor, max(minSpillFloor, budget/(4*max(n, 1))))
}

// budgetResults keeps one turn's tool output within about MaxTurnResultChars. Results
// are taken in call order and kept whole while they fit; one that does not fit keeps a
// head-and-tail excerpt (at least the floor, so no result vanishes) and its full text
// goes to the blob store behind a recall handle that the result names. Nothing is lost,
// the model is told what it is looking at, and one turn cannot put hundreds of
// kilobytes into every later request. The budget is exceeded only by the floors of the
// results after it ran out, which are small (spillFloorFor) and, with the notes that
// name the handles, come to at most about half of the budget again at the default call
// cap.
func (a *Agent) budgetResults(results []core.Block, refused int) {
	budget := a.cfg.MaxTurnResultChars
	spilled, chars := 0, 0
	if budget > 0 {
		floor := spillFloorFor(budget, len(results))
		used := 0
		for i := range results {
			b := results[i]
			var text strings.Builder
			var other []core.Block
			for _, c := range b.Result {
				if c.Kind == core.BlockText {
					text.WriteString(c.Text)
				} else {
					other = append(other, c)
				}
			}
			full := text.String()
			remaining := budget - used
			if len(full) <= remaining || len(full) <= floor+spillNoteRoom {
				used += len(full)
				continue
			}
			keep := max(remaining, floor)
			shown, _ := tools.Truncate(full, keep)
			note := fmt.Sprintf("\n[this turn's tool output exceeded %d characters, so %d of this result's %d are shown", budget, len(shown), len(full))
			if h, err := a.cfg.Blobs.Put([]byte(full)); err == nil {
				handle := a.cfg.Handles.Add(h, len(full))
				// Recorded so that a resumed session can mint the same handle again.
				a.emit("tool.spill", map[string]any{"id": b.ToolID, "handle": handle, "ref": h, "chars": len(full)})
				note += fmt.Sprintf("; the whole result is saved as %s: recall(handle=%q) pages through it]", handle, handle)
			} else {
				note += "; the rest could not be saved, so re-run the call with a narrower request]"
			}
			results[i] = core.ToolResult(b.ToolID, b.IsError, append([]core.Block{core.Text(shown + note)}, other...)...)
			used += len(shown) + len(note)
			spilled++
		}
		chars = used
	}
	if spilled > 0 || refused > 0 {
		a.emit("tool.budget", map[string]any{
			"calls": len(results), "refused": refused, "spilled": spilled, "chars": chars, "budget_chars": budget,
			"max_calls": a.cfg.MaxToolCallsPerTurn,
		})
	}
}

func (a *Agent) readOnly(c core.Block) bool {
	t, ok := a.cfg.Tools.Get(c.ToolName)
	if !ok {
		return true // errors are cheap and side-effect free
	}
	return t.Spec().ReadOnly
}

func (a *Agent) env() *tools.Env {
	return (&tools.Env{
		Agent: a.cfg.ID, Role: a.cfg.Role, Cwd: a.cfg.Workdir, Root: a.cfg.Root,
		Files: a.cfg.Files, Guard: a.cfg.Guard, Perm: a.cfg.Perm, Snap: a.cfg.Snap,
		Blobs: a.cfg.Blobs, Emit: a.cfg.Events, Handles: a.cfg.Handles,
		Limits: a.cfg.Limits, Now: a.cfg.Now,
		Out: func(stream, text string) {},
	}).Defaults()
}

// runOne executes one call, never panicking and never returning an empty result.
func (a *Agent) runOne(ctx context.Context, call core.Block) (out core.Block) {
	start := time.Now()
	a.cfg.Sink.ToolStart(a.cfg.ID, call)
	a.emit(events.TypeToolCall, map[string]any{"id": call.ToolID, "name": call.ToolName, "input": json.RawMessage(call.Input)})

	var res *tools.Result
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("tool %s crashed: %v", call.ToolName, r)
			a.emit("tool.panic", map[string]any{"name": call.ToolName, "stack": string(debug.Stack())})
		}
		out = a.finishResult(call, res, time.Since(start))
	}()

	switch {
	case call.Invalid != "":
		res = tools.Errorf("The arguments for %s were not valid JSON (%s), most likely cut off by the output limit. Send a smaller call.", call.ToolName, call.Invalid)
		res.Meta = withErrorKind(res.Meta, ErrKindInvalidInput)
		return
	case ctx.Err() != nil:
		res = tools.Errorf("interrupted before %s ran", call.ToolName)
		return
	}
	t, ok := a.cfg.Tools.Get(call.ToolName)
	if !ok {
		res = tools.Errorf("unknown tool %q", call.ToolName)
		res.Meta = withErrorKind(res.Meta, ErrKindUnknownTool)
		return
	}
	hc := ToolHookCall{Agent: a.cfg.ID, Role: a.cfg.Role, Tool: call.ToolName, Input: call.Input}
	var hookText []string
	if h := a.cfg.Hooks; h != nil {
		o := h.BeforeTool(ctx, hc)
		if o.Veto {
			res = tools.Errorf("A hook blocked this call: %s", o.Reason)
			res.Meta = withErrorKind(res.Meta, ErrKindHook)
			return
		}
		if len(o.UpdatedInput) > 0 {
			call.Input = o.UpdatedInput
			hc.Input = o.UpdatedInput
		}
		if o.Context != "" {
			hookText = append(hookText, o.Context)
		}
	}
	var err error
	tctx, stopClock := a.toolContext(ctx)
	res, err = t.Run(tctx, &tools.Call{ID: call.ToolID, Name: call.ToolName, Input: call.Input, Env: a.env()})
	timedOut := tctx.Err() == context.DeadlineExceeded && ctx.Err() == nil
	stopClock()
	if err != nil {
		res = tools.Errorf("%s failed: %v", call.ToolName, err)
	}
	if res == nil {
		res = tools.Errorf("%s returned nothing", call.ToolName)
	}
	if timedOut && res.IsError {
		// The tool came back because the deadline passed, not of its own accord. A result
		// that is not an error is kept: the work finished, if late.
		was := res.Text
		res = tools.Errorf("%s ran longer than %s and was stopped (%s). Ask for less, or split the work up.", call.ToolName, a.cfg.ToolTimeout, kv.EscapeLine(was, 200))
		res.Meta = withErrorKind(res.Meta, ErrKindTimeout)
		a.emit("tool.timeout", map[string]any{"id": call.ToolID, "name": call.ToolName, "limit_ms": a.cfg.ToolTimeout.Milliseconds()})
	}
	if h := a.cfg.Hooks; h != nil {
		o := h.AfterTool(ctx, hc, res)
		if o.Reason != "" {
			hookText = append(hookText, o.Reason)
		}
		if o.Context != "" {
			hookText = append(hookText, o.Context)
		}
	}
	if len(hookText) > 0 {
		res.Text += "\n[hook] " + strings.Join(hookText, "\n[hook] ")
	}
	return
}

// toolContext is the context of one tool call: the run's, with the per-call deadline if
// there is one. stop releases its timer.
func (a *Agent) toolContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if d := a.cfg.ToolTimeout; d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return ctx, func() {}
}

// perTurnResultCap bounds a single result even if a tool forgot to truncate.
const perResultCap = 60_000

func (a *Agent) finishResult(call core.Block, res *tools.Result, took time.Duration) core.Block {
	if res == nil {
		res = tools.Errorf("no result")
	}
	text := res.Text
	if len(text) > perResultCap {
		text, _ = tools.Truncate(text, perResultCap)
	}
	if strings.TrimSpace(text) == "" && len(res.Blocks) == 0 {
		text = "(no output)"
	}
	content := []core.Block{core.Text(text)}
	content = append(content, res.Blocks...)
	if res.IsError {
		if _, ok := res.Meta["error_kind"]; !ok {
			res.Meta = withErrorKind(res.Meta, classifyToolError(res.Text))
		}
	}
	a.cfg.Sink.ToolEnd(a.cfg.ID, call, res, took)
	a.emit(events.TypeToolResult, map[string]any{
		"id": call.ToolID, "name": call.ToolName, "error": res.IsError, "chars": len(res.Text),
		"truncated": res.Truncated, "handle": res.Handle, "ref": res.FullRef, "full_chars": res.FullChars, "ms": took.Milliseconds(), "meta": res.Meta,
	})
	return core.ToolResult(call.ToolID, res.IsError, content...)
}

var _ = fmt.Sprintf
