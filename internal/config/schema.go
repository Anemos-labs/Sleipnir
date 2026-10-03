package config

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// field is one JSON-visible field of a struct type.
type field struct {
	name string
	typ  reflect.Type
}

// structFields lists a struct's JSON fields by their json tag names.
func structFields(t reflect.Type) []field {
	var out []field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = sf.Name
		}
		out = append(out, field{name: name, typ: sf.Type})
	}
	return out
}

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// checker validates a parsed document against the Config schema. It reports
// every problem it finds (not just the first) and converts the document to the
// plain-value tree that layers are merged in, dropping what did not check out.
type checker struct {
	file   string
	src    []byte // the document's text, for line/column; nil when it has none
	issues []Issue
}

// report appends a schema issue with copied path segments and an optional source position.
func (c *checker) report(sev Severity, off int, segs []string, format string, args ...any) {
	is := Issue{
		Severity: sev,
		Source:   c.file,
		Path:     fmtPath(segs),
		Message:  fmt.Sprintf(format, args...),
		segs:     cloneSegs(segs),
	}
	if c.src != nil && off >= 0 {
		is.Line, is.Col = lineCol(c.src, off)
	}
	c.issues = append(c.issues, is)
}

// wrongType records the expected and actual node types at the field's source offset.
func (c *checker) wrongType(n *node, segs []string, want string) {
	c.report(SeverityError, n.off, segs, "expected %s, got %s", want, n.kind)
}

// convert checks n against type t and returns its plain-value form. Unknown
// keys inside sections are reported and dropped (they could otherwise be picked
// up by encoding/json's case-insensitive matching); unknown top-level keys are
// reported and kept, because Config.Extra preserves them. A JSON null converts
// to nil, which the merge treats as "unset".
func (c *checker) convert(n *node, t reflect.Type, segs []string, top bool) any {
	if n.kind == nNull {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == rawMessageType || t.Kind() == reflect.Interface:
		return toValue(n)

	case t.Kind() == reflect.Struct:
		if n.kind != nObject {
			c.wrongType(n, segs, "an object")
			return nil
		}
		fields := structFields(t)
		names := make([]string, len(fields))
		byName := make(map[string]field, len(fields))
		for i, f := range fields {
			names[i] = f.name
			byName[f.name] = f
		}
		out := make(map[string]any, len(n.keys))
		for _, k := range n.keys {
			m := n.mem[k]
			f, ok := byName[k]
			if !ok {
				if top {
					out[k] = toValue(m.val) // preserved in Config.Extra
				}
				if top && strings.HasPrefix(k, "$") {
					continue // "$schema" and friends are editor metadata, not typos
				}
				msg := fmt.Sprintf("unknown key %q", k)
				if s := closest(k, names); s != "" {
					msg += fmt.Sprintf(" (did you mean %q?)", s)
				}
				if top {
					msg += "; it is kept but has no effect"
				} else {
					msg += "; it is ignored"
				}
				c.report(SeverityWarning, m.keyOff, cloneSegs(segs, k), "%s", msg)
				c.issues[len(c.issues)-1].code = codeUnknownKey
				continue
			}
			out[k] = c.convert(m.val, f.typ, cloneSegs(segs, k), false)
		}
		return out

	case t.Kind() == reflect.Map:
		if n.kind != nObject {
			c.wrongType(n, segs, "an object")
			return nil
		}
		out := make(map[string]any, len(n.keys))
		for _, k := range n.keys {
			out[k] = c.convert(n.mem[k].val, t.Elem(), cloneSegs(segs, k), false)
		}
		return out

	case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
		if n.kind != nArray {
			c.wrongType(n, segs, "a list")
			return nil
		}
		out := make([]any, 0, len(n.arr))
		for i, e := range n.arr {
			es := cloneSegs(segs, fmt.Sprintf("[%d]", i))
			if e.kind == nNull {
				c.report(SeverityError, e.off, es, "null is not allowed in a list")
				continue
			}
			// An element that failed its check is dropped (already reported), not kept
			// as a nil that would decode to a zero value and trigger a second,
			// misleading error.
			if v := c.convert(e, t.Elem(), es, false); v != nil {
				out = append(out, v)
			}
		}
		return out

	case t.Kind() == reflect.String:
		if n.kind != nString {
			c.wrongType(n, segs, "a string")
			return nil
		}
		return n.str

	case t.Kind() == reflect.Bool:
		if n.kind != nBool {
			c.wrongType(n, segs, "true or false")
			return nil
		}
		return n.b

	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		if n.kind != nNumber {
			c.wrongType(n, segs, "an integer")
			return nil
		}
		if _, err := strconv.ParseInt(n.num, 10, t.Bits()); err != nil {
			c.report(SeverityError, n.off, segs, "expected an integer, got %s", n.num)
			return nil
		}
		return json.Number(n.num)

	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		if n.kind != nNumber {
			c.wrongType(n, segs, "a number")
			return nil
		}
		if f, err := strconv.ParseFloat(n.num, 64); err != nil || math.IsInf(f, 0) {
			c.report(SeverityError, n.off, segs, "%s is out of range", n.num)
			return nil
		}
		return json.Number(n.num)
	}
	return toValue(n)
}

const codeUnknownKey = "unknown-key"

// closest returns the candidate most like key, if any is close enough to be a
// plausible typo.
func closest(key string, cands []string) string {
	lk := strings.ToLower(key)
	best, bestD := "", 1<<30
	for _, cand := range cands {
		lc := strings.ToLower(cand)
		if lc == lk {
			return cand
		}
		if d := levenshtein(lk, lc); d < bestD {
			best, bestD = cand, d
		}
	}
	limit := 2
	if len(key) <= 4 {
		limit = 1
	}
	if bestD <= limit {
		return best
	}
	return ""
}

// levenshtein computes rune-based edit distance with insertion, deletion, and substitution cost
// one, using one previous row.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
