// Package jsonpath evaluates a small subset of JSONPath over documents that were
// decoded with encoding/json into an any.
//
// README.md specifies the syntax, the order of the results and the errors.
package jsonpath

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Eval returns the values of doc that path selects, in the order defined in
// README.md. A valid path that selects nothing gives an empty result and a nil
// error; an invalid path gives a nil result and a non-nil error.
func Eval(doc any, path string) ([]any, error) {
	segs, err := parse(path) // the whole path is checked before anything is evaluated
	if err != nil {
		return nil, err
	}
	nodes := []any{doc}
	for _, s := range segs {
		var next []any
		for _, n := range nodes {
			next = s.apply(next, n)
		}
		nodes = next
	}
	return nodes, nil
}

type segKind int

const (
	child    segKind = iota // .name and ['text']
	index                   // [n]
	wildcard                // [*]
	descend                 // ..name
)

type segment struct {
	kind  segKind
	name  string // child, descend
	index int    // index
}

// apply appends to out what the segment selects from node.
func (s segment) apply(out []any, node any) []any {
	switch s.kind {
	case child:
		if m, ok := node.(map[string]any); ok {
			if v, ok := m[s.name]; ok {
				out = append(out, v)
			}
		}
	case index:
		if a, ok := node.([]any); ok && s.index < len(a) {
			out = append(out, a[s.index])
		}
	case wildcard:
		out = append(out, children(node)...)
	case descend:
		out = collect(out, node, s.name)
	}
	return out
}

// collect appends the values of every member called name in node and below it,
// a value's own member before the ones inside it.
func collect(out []any, node any, name string) []any {
	if m, ok := node.(map[string]any); ok {
		if v, ok := m[name]; ok {
			out = append(out, v)
		}
	}
	for _, c := range children(node) {
		out = collect(out, c, name)
	}
	return out
}

// children returns the values directly inside node: the elements of an array in
// order, the member values of an object in ascending order of member name.
func children(node any) []any {
	switch n := node.(type) {
	case []any:
		return n
	case map[string]any:
		vals := make([]any, 0, len(n))
		for _, k := range slices.Sorted(maps.Keys(n)) {
			vals = append(vals, n[k])
		}
		return vals
	}
	return nil
}

func parse(path string) ([]segment, error) {
	if !strings.HasPrefix(path, "$") {
		return nil, fmt.Errorf("jsonpath: %q does not start with $", path)
	}
	var segs []segment
	for i := 1; i < len(path); {
		var (
			seg segment
			n   int
			err error
		)
		switch {
		case strings.HasPrefix(path[i:], ".."):
			seg, n, err = parseName(path[i+2:], descend)
			n += 2
		case path[i] == '.':
			seg, n, err = parseName(path[i+1:], child)
			n++
		case path[i] == '[':
			seg, n, err = parseBracket(path[i:])
		default:
			err = fmt.Errorf("unexpected %q", path[i])
		}
		if err != nil {
			return nil, fmt.Errorf("jsonpath: %w at offset %d of %q", err, i, path)
		}
		segs = append(segs, seg)
		i += n
	}
	return segs, nil
}

// parseName reads a bare name at the start of s.
func parseName(s string, kind segKind) (segment, int, error) {
	n := 0
	for n < len(s) && isNameByte(s[n]) {
		n++
	}
	if n == 0 {
		return segment{}, 0, errors.New("name expected")
	}
	return segment{kind: kind, name: s[:n]}, n, nil
}

func isNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// parseBracket reads "[*]", "[n]" or "['text']" at the start of s.
func parseBracket(s string) (segment, int, error) {
	switch {
	case strings.HasPrefix(s, "[*]"):
		return segment{kind: wildcard}, 3, nil
	case strings.HasPrefix(s, "['"):
		end := strings.IndexByte(s[2:], '\'')
		if end < 0 {
			return segment{}, 0, errors.New("unterminated quoted name")
		}
		if !strings.HasPrefix(s[2+end+1:], "]") {
			return segment{}, 0, errors.New("] expected after quoted name")
		}
		return segment{kind: child, name: s[2 : 2+end]}, end + 4, nil
	}
	digits := 0
	for 1+digits < len(s) && s[1+digits] >= '0' && s[1+digits] <= '9' {
		digits++
	}
	if digits == 0 || !strings.HasPrefix(s[1+digits:], "]") {
		return segment{}, 0, errors.New("expected *, digits or a quoted name in brackets")
	}
	n, err := strconv.Atoi(s[1 : 1+digits])
	if err != nil { // only a number too large for an int: no array is that long
		n = math.MaxInt
	}
	return segment{kind: index, index: n}, digits + 2, nil
}
