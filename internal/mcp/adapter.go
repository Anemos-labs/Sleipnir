package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// maxInputBytes bounds the arguments a model may pass to one MCP call.
const maxInputBytes = 4 << 20

// mcpTool adapts one server tool to tools.Tool.
//
// Its spec is frozen when the snapshot is taken: the model-visible name,
// description and schema never change under a running session, whatever the
// server does later (that is what keeps the cached prefix byte-identical for
// every agent). Run resolves the live connection at call time, so a server that
// restarted in between is used transparently, and a tool the server no longer
// offers fails with a model-visible error instead of a wrong call.
type mcpTool struct {
	m      *Manager
	server string // configured server name
	tool   string // the server's own tool name, exactly as it must be sent back
	spec   core.ToolSpec
	remote bool
}

// Spec implements tools.Tool.
func (t *mcpTool) Spec() core.ToolSpec { return cloneSpec(t.spec) }

// Run implements tools.Tool. Problems with the call, the permission or the
// server are model-visible results; a Go error is never returned, because
// nothing here is a failure of the harness itself.
func (t *mcpTool) Run(ctx context.Context, c *tools.Call) (res *tools.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = tools.Errorf("MCP tool %s failed unexpectedly: %v", t.spec.Name, r), nil
		}
	}()
	name := t.spec.Name
	env := c.Env
	if env == nil || env.Perm == nil {
		// A tool that reaches a third party must never run unchecked, and the
		// default of a nil Requester elsewhere in the harness is allow-all.
		return tools.Errorf("MCP tool %s was not run: no permission checker is configured", name), nil
	}

	input := bytes.TrimSpace(c.Input)
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage("{}")
	}
	switch {
	case len(input) > maxInputBytes:
		return tools.Errorf("arguments for %s are too large (%d bytes; the limit is %d)", name, len(input), maxInputBytes), nil
	case input[0] != '{' || !json.Valid(input):
		return tools.Errorf("arguments for %s must be a JSON object", name), nil
	}

	srv := t.m.serverByName(t.server)
	if srv == nil {
		return tools.Errorf("MCP server %q is not configured", t.server), nil
	}
	if !srv.cfg.toolAllowed(t.tool, name) {
		return tools.Errorf("tool %s is disabled by the MCP server configuration", name), nil
	}
	client, err := srv.awaitClient(ctx)
	if err != nil {
		return tools.Errorf("%s", modelError(t.server, err)), nil
	}

	// Ask before acting. What an MCP tool does is unknown to us, so the request
	// says so: writes unless the server's own annotation claims otherwise (an
	// untrusted hint, which is why it can only ever make the question less
	// alarming, never skip it), network for remote transports.
	d := env.Perm.Check(ctx, perm.Request{
		Agent: env.Agent, Role: env.Role, Tool: name, Input: json.RawMessage(input),
		Summary: t.summary(input),
		Network: t.remote,
		Writes:  !t.spec.ReadOnly,
		Risk:    perm.RiskMedium,
	})
	if !d.Allow {
		reason := d.Reason
		if reason == "" {
			reason = "not allowed"
		}
		return tools.Errorf("permission denied for %s: %s", name, oneLine(reason, 300)), nil
	}

	start := time.Now()
	copts := CallOptions{Timeout: srv.callTimeout(), MaxTotal: t.m.opts.MaxCallDuration}
	if env.Out != nil {
		var mu sync.Mutex
		var last time.Time
		copts.OnProgress = func(p Progress) {
			mu.Lock()
			skip := time.Since(last) < 250*time.Millisecond
			if !skip {
				last = time.Now()
			}
			mu.Unlock()
			if skip {
				return
			}
			line := fmt.Sprintf("%s: progress %g", name, p.Progress)
			if p.Total > 0 {
				line += fmt.Sprintf("/%g", p.Total)
			}
			if p.Message != "" {
				line += " " + oneLine(p.Message, 200)
			}
			env.Out("info", line)
		}
	}
	result, err := client.CallTool(ctx, t.tool, input, copts)
	took := time.Since(start)
	if err != nil {
		r := tools.Errorf("%s", modelError(t.server, err))
		r.Meta = map[string]any{"mcp_server": t.server, "mcp_tool": t.tool, "ms": took.Milliseconds()}
		return r, nil
	}

	rd := renderResult(result, env.Blobs, t.m.opts.AttachMedia, srv.red)
	limits := env.Limits
	if n := srv.cfg.MaxOutputChars; n > 0 && (limits.MaxOutputChars <= 0 || n < limits.MaxOutputChars) {
		limits.MaxOutputChars = n // an entry can lower the cap, never raise it
	}
	e2 := *env
	e2.Limits = limits
	e2.Defaults()
	out := e2.Finish(rd.text, result.IsError)
	out.Blocks = append(out.Blocks, rd.blocks...)
	out.Meta = map[string]any{"mcp_server": t.server, "mcp_tool": t.tool, "ms": took.Milliseconds()}
	if len(rd.media) > 0 {
		out.Meta["media"] = rd.media
	}
	if len(result.StructuredContent) > 0 {
		out.Meta["structured"] = true
	}
	return out, nil
}

// summary is the one line a permission prompt shows: the tool and a short
// digest of the arguments. Arguments are model-written, hence sanitised.
func (t *mcpTool) summary(input []byte) string {
	s := fmt.Sprintf("MCP %s/%s", t.server, t.tool)
	if args := bytes.TrimSpace(input); len(args) > 2 { // more than "{}"
		s += " " + oneLine(string(args), 160)
	}
	s, _ = truncateRunes(oneLine(s, 400), 240)
	return s
}

// modelError turns a failure into the short, actionable text a model can act
// on. Everything server-supplied inside err was sanitised and redacted where
// it was decoded.
func modelError(server string, err error) string {
	var rpc *RPCError
	var to *timeoutError
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Sprintf("MCP call to %q was cancelled", server)
	case errors.As(err, &to):
		return fmt.Sprintf("MCP server %q did not respond in time: %v", server, err)
	case errors.Is(err, ErrMessageTooLarge):
		return fmt.Sprintf("MCP server %q returned a result too large to accept; ask for less data", server)
	case errors.As(err, &rpc):
		return fmt.Sprintf("MCP server %q rejected the call: %s (code %d)", server, rpc.Message, rpc.Code)
	case errors.Is(err, ErrNotConnected):
		return fmt.Sprintf("MCP server %q is not connected: %s", server, trimPrefix(err.Error(), ErrNotConnected.Error()+": "))
	case errors.Is(err, ErrClosed):
		return fmt.Sprintf("MCP server %q connection was lost: %s", server, trimPrefix(err.Error(), ErrClosed.Error()+": "))
	case errors.Is(err, ErrUnsupported):
		return fmt.Sprintf("MCP server %q does not support tools", server)
	}
	msg, _ := truncateRunes(oneLine(err.Error(), 300), 300)
	return fmt.Sprintf("MCP call to %q failed: %s", server, msg)
}

func trimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}
