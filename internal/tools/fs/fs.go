// Package fs implements Sleipnir's filesystem and search tools: read, write,
// edit, apply_patch, glob, grep and ls.
//
// Three rules hold for every tool in this package and explain most of the code:
//
//   - A path is canonicalised exactly once (cleaned, made absolute against
//     Env.Cwd, symlinks resolved) and that one spelling is what permissions, the
//     swarm Guard, snapshots and FileState all see. Two agents that reach the same
//     file through different names must collide, never race.
//   - Anything that mutates a file holds a per-path lock across
//     "read current content, check it is fresh, write", because FileState can only
//     reject the loser of a race if the check and the write are atomic.
//   - Tools are singletons shared by every agent goroutine, so they keep no
//     per-call state; everything per-call lives in a call value on the stack.
package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

const (
	// maxFileBytes is the hard ceiling for any file the tools read into memory
	// (read with a range, edit, write over an existing file). Bigger files need
	// grep or the shell.
	maxFileBytes int64 = 32 << 20

	defaultReadLines = 2000
	maxLineChars     = 2000
)

// Register adds every filesystem tool to r.
func Register(r *tools.Registry) {
	for _, t := range []tools.Tool{&Read{}, &Write{}, &Edit{}, &ApplyPatch{}, &Glob{}, &Grep{}, &LS{}} {
		r.Register(t)
	}
}

// defaultsMu serialises Env.Defaults. Defaults lazily fills nil fields of an Env
// that several goroutines of one agent may share (parallel read-only calls);
// without the mutex two first calls would race on those writes.
var defaultsMu sync.Mutex

// call is the per-invocation context. It lives on the goroutine's stack.
type call struct {
	ctx  context.Context
	tool string
	in   json.RawMessage
	env  *tools.Env
}

func begin(ctx context.Context, c *tools.Call, tool string) *call {
	if ctx == nil {
		ctx = context.Background()
	}
	env := &tools.Env{}
	var in json.RawMessage
	if c != nil {
		if c.Env != nil {
			env = c.Env
		}
		in = c.Input
	}
	defaultsMu.Lock()
	env.Defaults()
	defaultsMu.Unlock()
	return &call{ctx: ctx, tool: tool, in: in, env: env}
}

// fail returns a model-visible error result. Every result goes through
// Env.Finish so oversized text is truncated and recallable.
func (k *call) fail(format string, args ...any) *tools.Result {
	return k.env.Finish(fmt.Sprintf(format, args...), true)
}

func (k *call) ok(text string) *tools.Result { return k.env.Finish(text, false) }

// decode parses the tool input into dst. Unknown fields are tolerated (models
// invent parameters); wrong types produce a message naming the field.
func (k *call) decode(dst any) *tools.Result {
	raw := bytes.TrimSpace(k.in)
	if len(raw) > 0 && raw[0] == '"' {
		// Some providers hand arguments over as a JSON string containing JSON.
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = bytes.TrimSpace([]byte(s))
		}
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return k.fail("%s", describeArgError(err))
	}
	return nil
}

func describeArgError(err error) string {
	var ute *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &ute):
		if ute.Field == "" {
			return "arguments must be a JSON object"
		}
		return fmt.Sprintf("argument %q must be %s (got %s)", ute.Field, kindName(ute.Type), ute.Value)
	case errors.As(err, &se):
		return "arguments are not valid JSON: " + se.Error()
	}
	return "invalid arguments: " + err.Error()
}

func kindName(t reflect.Type) string {
	if t == nil {
		return "a different type"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Float32, reflect.Float64:
		return "a number"
	}
	if strings.HasPrefix(t.Kind().String(), "int") || strings.HasPrefix(t.Kind().String(), "uint") {
		return "an integer"
	}
	return "a " + t.String()
}

// intArg is an optional integer argument. It accepts integral floats ("10.0",
// "1e3"), which some models emit, and rejects strings and fractions with an
// error that names the field.
type intArg struct {
	V   int
	Set bool
}

func (a *intArg) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		return nil
	}
	bad := func(v string) error { return &json.UnmarshalTypeError{Value: v, Type: reflect.TypeOf(0)} }
	switch s[0] {
	case '"':
		return bad("string")
	case 't', 'f':
		return bad("bool")
	case '{':
		return bad("object")
	case '[':
		return bad("array")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return bad("number " + s)
	}
	if f != math.Trunc(f) || math.Abs(f) > 1<<40 {
		return bad("number " + s)
	}
	a.V, a.Set = int(f), true
	return nil
}

// authorize asks the permission engine. It returns nil when the action may
// proceed and a ready error result otherwise. Permission is checked before any
// lock is taken or file read: a prompt can block on a human for minutes, and a
// denied path must not leak its content through later error messages.
func (k *call) authorize(summary string, writes bool, risk perm.Risk, paths ...string) *tools.Result {
	d := k.env.Perm.Check(k.ctx, perm.Request{
		Agent:   k.env.Agent,
		Role:    k.env.Role,
		Tool:    k.tool,
		Summary: summary,
		Paths:   paths,
		Writes:  writes,
		Risk:    risk,
	})
	if d.Allow {
		return nil
	}
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "not allowed by the permission policy"
	}
	return k.fail("permission denied (%s): %s", summary, reason)
}

// humanBytes renders a size for messages ("1.5 KB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// plural returns "1 file" / "2 files".
func plural(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
