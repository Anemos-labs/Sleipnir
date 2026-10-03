package mcptest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// pngPixel is a 1x1 transparent PNG.
const pngPixel = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGD4DwABBAEAwS2OUAAAAABJRU5ErkJggg=="

func referenceTools(s *Server) []Tool {
	arg := func(c *Call, into any) {
		if len(c.Args) > 0 {
			_ = json.Unmarshal(c.Args, into)
		}
	}
	schema := func(props string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` + props + `}}`)
	}
	return []Tool{
		{Name: "echo", Description: "Echo the message back.", Schema: schema(`"message":{"type":"string","description":"text to echo"}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Handler: func(_ context.Context, c *Call) *Result {
				var a struct {
					Message string `json:"message"`
				}
				arg(c, &a)
				return TextResult(a.Message)
			}},
		{Name: "big", Description: "Return a lot of text.", Schema: schema(`"bytes":{"type":"integer"}`),
			Handler: func(_ context.Context, c *Call) *Result {
				a := struct {
					Bytes int `json:"bytes"`
				}{100_000}
				arg(c, &a)
				return TextResult(strings.Repeat("x", a.Bytes))
			}},
		{Name: "slow", Description: "Sleep, then answer.", Schema: schema(`"ms":{"type":"integer"}`),
			Handler: func(ctx context.Context, c *Call) *Result {
				a := struct {
					Ms int `json:"ms"`
				}{1000}
				arg(c, &a)
				select {
				case <-time.After(time.Duration(a.Ms) * time.Millisecond):
					return TextResult("done")
				case <-ctx.Done():
					return &Result{Drop: true}
				}
			}},
		{Name: "crash", Description: "Kill the server mid-call.",
			Handler: func(_ context.Context, c *Call) *Result { c.Crash(); return &Result{Drop: true} }},
		{Name: "add_tool", Description: "Add a tool and announce it (tools/list_changed).",
			Handler: func(_ context.Context, c *Call) *Result {
				s.mu.Lock()
				s.added++
				name := fmt.Sprintf("added_%d", s.added)
				s.mu.Unlock()
				s.AddTool(Tool{Name: name, Description: "Added at runtime.", Handler: func(context.Context, *Call) *Result { return TextResult("hi from " + name) }})
				s.NotifyToolsChanged()
				return TextResult("added " + name)
			}},
		{Name: "image", Description: "Return an image.",
			Handler: func(context.Context, *Call) *Result {
				return &Result{Content: []map[string]any{{"type": "image", "data": pngPixel, "mimeType": "image/png"}}}
			}},
		{Name: "error", Description: "Fail as a tool.",
			Handler: func(context.Context, *Call) *Result {
				return &Result{Content: []map[string]any{Text("boom")}, IsError: true}
			}},
		{Name: "structured", Description: "Return structured content.",
			Handler: func(context.Context, *Call) *Result {
				return &Result{Content: []map[string]any{Text(`{"answer":42}`)}, Structured: map[string]any{"answer": 42}}
			}},
		{Name: "links", Description: "Return links and embedded resources.",
			Handler: func(context.Context, *Call) *Result {
				return &Result{Content: []map[string]any{
					{"type": "resource_link", "uri": "file:///readme.md", "name": "readme", "mimeType": "text/markdown", "description": "The readme"},
					{"type": "resource", "resource": map[string]any{"uri": "file:///note.txt", "mimeType": "text/plain", "text": "embedded note"}},
					{"type": "resource", "resource": map[string]any{"uri": "mem://blob", "mimeType": "application/octet-stream", "blob": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})}},
					{"type": "audio", "data": base64.StdEncoding.EncodeToString([]byte("RIFFxxxxWAVE")), "mimeType": "audio/wav"},
				}}
			}},
		{Name: "progress", Description: "Report progress.", Schema: schema(`"steps":{"type":"integer"}`),
			Handler: func(_ context.Context, c *Call) *Result {
				a := struct {
					Steps int `json:"steps"`
				}{3}
				arg(c, &a)
				for i := 1; i <= a.Steps; i++ {
					c.Progress(float64(i), float64(a.Steps), fmt.Sprintf("step %d", i))
				}
				return TextResult("progressed")
			}},
		{Name: "sampling", Description: "Ask the client to sample.",
			Handler: func(ctx context.Context, c *Call) *Result {
				res, e, err := c.Ask(ctx, "sampling/createMessage", map[string]any{
					"messages":  []any{map[string]any{"role": "user", "content": Text("hi")}},
					"maxTokens": 10,
				})
				return askResult(res, e, err)
			}},
		{Name: "roots", Description: "Ask the client for its roots.",
			Handler: func(ctx context.Context, c *Call) *Result {
				res, e, err := c.Ask(ctx, "roots/list", nil)
				return askResult(res, e, err)
			}},
		{Name: "ping_client", Description: "Ping the client.",
			Handler: func(ctx context.Context, c *Call) *Result {
				res, e, err := c.Ask(ctx, "ping", nil)
				return askResult(res, e, err)
			}},
		{Name: "env", Description: "List the process environment.",
			Handler: func(context.Context, *Call) *Result { return TextResult(strings.Join(sortedEnv(), "\n")) }},
		{Name: "pid", Description: "Return the process id.",
			Handler: func(context.Context, *Call) *Result { return TextResult(fmt.Sprint(os.Getpid())) }},
		{Name: "cwd", Description: "Return the working directory.",
			Handler: func(context.Context, *Call) *Result {
				wd, _ := os.Getwd()
				return TextResult(wd)
			}},
		{Name: "unicode", Description: "Return text with control sequences.",
			Handler: func(context.Context, *Call) *Result {
				// An ANSI colour, an OSC title, a zero-width space, a bidi override and
				// two tag characters spelling "hi". Built from code points so the
				// source stays readable.
				zwsp, rlo := string(rune(0x200B)), string(rune(0x202E))
				tags := string(rune(0xE0068)) + string(rune(0xE0069))
				return TextResult("plain \x1b[31mred\x1b[0m \x1b]0;title\x07 zero" + zwsp + "width " + rlo + "flipped " + tags + " end")
			}},
	}
}

// askResult formats a test client interaction, preferring transport errors over RPC errors and
// successful results.
func askResult(res json.RawMessage, e map[string]any, err error) *Result {
	switch {
	case err != nil:
		return TextResult("ask failed: " + err.Error())
	case e != nil:
		b, _ := json.Marshal(e)
		return TextResult("client error: " + string(b))
	}
	return TextResult("client result: " + string(res))
}
