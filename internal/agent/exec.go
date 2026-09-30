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
	"github.com/reee344/sleipnir/internal/tools"
)

// maxParallelTools bounds concurrent read-only tool calls from one turn.
const maxParallelTools = 8

// runTools executes a turn's tool calls and returns tool_result blocks in call
// order. Consecutive read-only calls run concurrently; anything that writes is a
// barrier and runs alone, in order, so a model's sequence of edits keeps its
// meaning while its searches and reads fan out.
func (a *Agent) runTools(ctx context.Context, calls []core.Block) []core.Block {
	results := make([]core.Block, len(calls))
	for i := 0; i < len(calls); {
		if a.readOnly(calls[i]) {
			j := i
			for j < len(calls) && a.readOnly(calls[j]) {
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
	return results
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
	res, err = t.Run(ctx, &tools.Call{ID: call.ToolID, Name: call.ToolName, Input: call.Input, Env: a.env()})
	if err != nil {
		res = tools.Errorf("%s failed: %v", call.ToolName, err)
	}
	if res == nil {
		res = tools.Errorf("%s returned nothing", call.ToolName)
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
		"truncated": res.Truncated, "handle": res.Handle, "ms": took.Milliseconds(), "meta": res.Meta,
	})
	return core.ToolResult(call.ToolID, res.IsError, content...)
}

var _ = fmt.Sprintf
