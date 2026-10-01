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
//     per-call state; everything per-call lives in a call value that one
//     goroutine owns for the duration of the call.
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

// call is the per-invocation context: created for one tool call, used by one
// goroutine, never shared.
type call struct {
	ctx  context.Context
	tool string
	in   json.RawMessage
	env  *tools.Env

	// Working directory in the two spellings display needs, computed on first use.
	cwdInit         bool
	cwdAbs, cwdReal string
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

// bounded limits the call to the configured default timeout. Tools that walk
// directory trees (glob, ls, grep) use it so that pointing one at a huge tree
// ends in an error the model can act on instead of an unbounded wait.
func (k *call) bounded() context.CancelFunc {
	d := k.env.Limits.DefaultTimeout
	if d <= 0 {
		return func() {}
	}
	var cancel context.CancelFunc
	k.ctx, cancel = context.WithTimeout(k.ctx, d)
	return cancel
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
			if inner := bytes.TrimSpace([]byte(s)); len(inner) > 0 && inner[0] == '{' {
				raw = inner
			}
		}
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return k.fail("%s", describeArgError(err, raw, dst))
	}
	return nil
}

// describeArgError says what is wrong with the arguments in words a model can act on, and names the argument. The message must
// not depend on the Go version that built the binary, and the decoder's own answer does: it names the field of a type that does
// not match (from Go 1.27 with the positions of an array in the path, "edits.0.new_string" where it said "edits.new_string"), but
// since Go 1.27 it names none for an error that a UnmarshalJSON method returned (intArg's), where it used to; and with two wrong
// arguments it reports a different one. So the first wrong member of an object, in the order the model wrote them, is found by
// trying each alone, and the answer is about that one.
func describeArgError(err error, raw []byte, dst any) string {
	var ute *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &se):
		return "arguments are not valid JSON: " + se.Error()
	case errors.As(err, &ute):
		field := withoutIndexes(ute.Field)
		if key, e := firstBadMember(raw, dst); e != nil {
			ute, field = e, withoutIndexes(e.Field)
			if field == "" {
				field = key
			}
		}
		if field == "" {
			return "arguments must be a JSON object"
		}
		return fmt.Sprintf("argument %q must be %s (got %s)", field, kindName(ute.Type), ute.Value)
	}
	return "invalid arguments: " + err.Error()
}

// withoutIndexes drops the array positions from a field path: "edits.0.new_string" is "edits.new_string".
func withoutIndexes(path string) string {
	parts := strings.Split(path, ".")
	keep := parts[:0]
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, ".")
}

// firstBadMember tries the members of the JSON object raw one at a time against a fresh dst, and returns the first that does not
// fit with the type error it causes. A document that is not an object has no member to name.
func firstBadMember(raw []byte, dst any) (string, *json.UnmarshalTypeError) {
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer {
		return "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", nil
	}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return "", nil
		}
		var val json.RawMessage
		if dec.Decode(&val) != nil {
			return "", nil
		}
		one, err := json.Marshal(map[string]json.RawMessage{name: val})
		if err != nil {
			return "", nil
		}
		var e *json.UnmarshalTypeError
		if err := json.Unmarshal(one, reflect.New(t.Elem()).Interface()); err != nil && errors.As(err, &e) {
			return name, e
		}
	}
	return "", nil
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
