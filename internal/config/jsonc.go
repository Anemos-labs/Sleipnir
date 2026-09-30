package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file is a small JSONC parser: JSON plus // and /* */ comments and
// trailing commas. It builds a tree that remembers where every key and value
// started, so a problem found long after parsing (a wrong type, an invalid
// value in a merged configuration) can still be reported as file:line:col.
// encoding/json cannot do that: its errors carry an offset only for syntax
// errors, and it has no notion of comments or trailing commas.

type nodeKind uint8

const (
	nNull nodeKind = iota
	nBool
	nNumber
	nString
	nArray
	nObject
)

func (k nodeKind) String() string {
	switch k {
	case nBool:
		return "a boolean"
	case nNumber:
		return "a number"
	case nString:
		return "a string"
	case nArray:
		return "a list"
	case nObject:
		return "an object"
	}
	return "null"
}

// node is one parsed JSON value.
type node struct {
	kind nodeKind
	off  int // byte offset of the value's first character; -1 when it has no source position
	str  string
	num  string
	b    bool
	arr  []*node
	keys []string // object member names, in source order
	mem  map[string]*member
}

type member struct {
	keyOff int
	val    *node
}

const (
	maxNesting  = 64
	maxFileSize = 1 << 20
)

type parser struct {
	src    []byte
	file   string
	pos    int
	depth  int
	issues []Issue // warnings found while parsing (duplicate keys)
}

// parseJSONC parses src. An empty document, or one holding only comments, is an
// empty object: a freshly created config file is not an error.
func parseJSONC(file string, src []byte) (root *node, warnings []Issue, err error) {
	p := &parser{src: src, file: file}
	if bytes.HasPrefix(src, []byte{0xEF, 0xBB, 0xBF}) {
		p.pos = 3 // UTF-8 byte order mark
	}
	if err := p.skip(); err != nil {
		return nil, nil, err
	}
	if p.pos >= len(src) {
		return &node{kind: nObject, off: -1, mem: map[string]*member{}}, nil, nil
	}
	n, err := p.value()
	if err != nil {
		return nil, nil, err
	}
	if err := p.skip(); err != nil {
		return nil, nil, err
	}
	if p.pos < len(src) {
		return nil, nil, p.errAt(p.pos, "unexpected %s after the end of the top-level value", p.describeAt(p.pos))
	}
	return n, p.issues, nil
}

// lineCol converts a byte offset to a 1-based line and column (in characters).
func lineCol(src []byte, off int) (line, col int) {
	if off < 0 || off > len(src) {
		return 0, 0
	}
	line = 1
	start := 0
	if bytes.HasPrefix(src, []byte{0xEF, 0xBB, 0xBF}) {
		start = 3 // the byte order mark is not a visible column
	}
	for i := 0; i < off; i++ {
		if src[i] == '\n' {
			line++
			start = i + 1
		}
	}
	return line, utf8.RuneCount(src[start:off]) + 1
}

func (p *parser) issueAt(off int, sev Severity, format string, args ...any) Issue {
	line, col := lineCol(p.src, off)
	return Issue{Severity: sev, Source: p.file, Line: line, Col: col, Message: fmt.Sprintf(format, args...)}
}

func (p *parser) errAt(off int, format string, args ...any) error {
	return p.issueAt(off, SeverityError, format, args...)
}

// describeAt names the character at off for an error message, spelling out the
// ones that are invisible or easily mistaken for something else.
func (p *parser) describeAt(off int) string {
	if off >= len(p.src) {
		return "end of file"
	}
	r, size := utf8.DecodeRune(p.src[off:])
	switch {
	case r == utf8.RuneError && size <= 1:
		return "an invalid UTF-8 byte"
	case r == 0xA0:
		return "a non-breaking space (U+00A0; replace it with a normal space)"
	case r == 0x201C || r == 0x201D:
		return fmt.Sprintf("a curly quote (%U; JSON needs straight double quotes)", r)
	case r == 0x2018 || r == 0x2019:
		return fmt.Sprintf("a curly quote (%U; JSON strings use straight double quotes)", r)
	case r < 0x20 || r == 0x7f || r > 0x7e:
		return fmt.Sprintf("the character %U", r)
	}
	return fmt.Sprintf("%q", string(r))
}

// skip advances past whitespace and comments.
func (p *parser) skip() error {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.pos++
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			end := bytes.Index(p.src[p.pos+2:], []byte("*/"))
			if end < 0 {
				return p.errAt(p.pos, "this /* comment is never closed")
			}
			p.pos += 2 + end + 2
		default:
			return nil
		}
	}
	return nil
}

func (p *parser) value() (*node, error) {
	if p.pos >= len(p.src) {
		return nil, p.errAt(len(p.src), "unexpected end of file; a value was expected")
	}
	c := p.src[p.pos]
	switch {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		start := p.pos
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return &node{kind: nString, off: start, str: s}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case c == '\'':
		return nil, p.errAt(p.pos, "strings must use double quotes, not single quotes")
	case isWordStart(c):
		return p.word()
	}
	return nil, p.errAt(p.pos, "unexpected %s; a value was expected", p.describeAt(p.pos))
}

func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isWordByte(c byte) bool { return isWordStart(c) || (c >= '0' && c <= '9') }

// word parses true, false or null and explains anything else that looks like an
// identifier (True, None, an unquoted string, an unquoted key).
func (p *parser) word() (*node, error) {
	start := p.pos
	for p.pos < len(p.src) && isWordByte(p.src[p.pos]) {
		p.pos++
	}
	w := string(p.src[start:p.pos])
	switch w {
	case "true":
		return &node{kind: nBool, off: start, b: true}, nil
	case "false":
		return &node{kind: nBool, off: start}, nil
	case "null":
		return &node{kind: nNull, off: start}, nil
	}
	switch strings.ToLower(w) {
	case "true", "false", "null":
		return nil, p.errAt(start, "%q is not valid; the literals true, false and null are lower case", w)
	case "none", "nil", "undefined":
		return nil, p.errAt(start, "%q is not valid JSON; use null", w)
	case "nan", "infinity", "inf":
		return nil, p.errAt(start, "%q is not valid JSON; numbers must be finite", w)
	}
	return nil, p.errAt(start, "unexpected word %q; strings must be in double quotes", w)
}

func (p *parser) number() (*node, error) {
	start := p.pos
	i := p.pos
	digit := func(j int) bool { return j < len(p.src) && p.src[j] >= '0' && p.src[j] <= '9' }
	if p.src[i] == '-' {
		i++
	}
	switch {
	case !digit(i):
		return nil, p.errAt(start, "invalid number")
	case p.src[i] == '0':
		i++
		if digit(i) {
			return nil, p.errAt(start, "numbers cannot have leading zeros")
		}
	default:
		for digit(i) {
			i++
		}
	}
	if i < len(p.src) && p.src[i] == '.' {
		i++
		if !digit(i) {
			return nil, p.errAt(start, "digits are required after the decimal point")
		}
		for digit(i) {
			i++
		}
	}
	if i < len(p.src) && (p.src[i] == 'e' || p.src[i] == 'E') {
		i++
		if i < len(p.src) && (p.src[i] == '+' || p.src[i] == '-') {
			i++
		}
		if !digit(i) {
			return nil, p.errAt(start, "digits are required in the exponent")
		}
		for digit(i) {
			i++
		}
	}
	if i < len(p.src) && isWordByte(p.src[i]) {
		return nil, p.errAt(start, "invalid number %q", string(p.src[start:i+1]))
	}
	p.pos = i
	return &node{kind: nNumber, off: start, num: string(p.src[start:i])}, nil
}

// str parses a double-quoted JSON string starting at p.pos.
func (p *parser) str() (string, error) {
	start := p.pos
	p.pos++
	var b strings.Builder
	for {
		if p.pos >= len(p.src) {
			return "", p.errAt(start, "this string is never closed")
		}
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c == '\n' || c == '\r':
			return "", p.errAt(start, "this string is never closed (a string cannot contain a raw line break; write \\n)")
		case c < 0x20:
			return "", p.errAt(p.pos, "control character %U inside a string; escape it", rune(c))
		case c == '\\':
			if err := p.escape(&b); err != nil {
				return "", err
			}
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			p.pos++
		default:
			r, size := utf8.DecodeRune(p.src[p.pos:])
			if r == utf8.RuneError && size <= 1 {
				return "", p.errAt(p.pos, "invalid UTF-8 inside a string")
			}
			b.WriteString(string(r))
			p.pos += size
		}
	}
}

func (p *parser) escape(b *strings.Builder) error {
	at := p.pos
	p.pos++ // the backslash
	if p.pos >= len(p.src) {
		return p.errAt(at, "incomplete escape sequence")
	}
	c := p.src[p.pos]
	p.pos++
	switch c {
	case '"', '\\', '/':
		b.WriteByte(c)
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'n':
		b.WriteByte('\n')
	case 'r':
		b.WriteByte('\r')
	case 't':
		b.WriteByte('\t')
	case 'u':
		r, ok := p.hex4()
		if !ok {
			return p.errAt(at, "invalid \\u escape; four hexadecimal digits are required")
		}
		if r >= 0xD800 && r < 0xDC00 { // high surrogate: must pair with a low one
			save := p.pos
			if p.pos+1 < len(p.src) && p.src[p.pos] == '\\' && p.src[p.pos+1] == 'u' {
				p.pos += 2
				if lo, ok := p.hex4(); ok && lo >= 0xDC00 && lo < 0xE000 {
					r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
				} else {
					p.pos = save
					r = utf8.RuneError
				}
			} else {
				r = utf8.RuneError
			}
		} else if r >= 0xDC00 && r < 0xE000 {
			r = utf8.RuneError
		}
		b.WriteString(string(r))
	default:
		return p.errAt(at, "invalid escape sequence \\%s", string(rune(c)))
	}
	return nil
}

func (p *parser) hex4() (rune, bool) {
	if p.pos+4 > len(p.src) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.src[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	p.pos += 4
	return rune(n), true
}

// unclosed reports a container that runs into the end of the file.
func (p *parser) unclosed(start int, open byte) error {
	line, _ := lineCol(p.src, start)
	what := "object ({)"
	if open == '[' {
		what = "list ([)"
	}
	return p.errAt(len(p.src), "unexpected end of file: the %s opened on line %d is never closed", what, line)
}

func (p *parser) enter(start int) error {
	p.depth++
	if p.depth > maxNesting {
		return p.errAt(start, "nesting is deeper than %d levels", maxNesting)
	}
	return nil
}

func (p *parser) object() (*node, error) {
	start := p.pos
	if err := p.enter(start); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	p.pos++
	n := &node{kind: nObject, off: start, mem: map[string]*member{}}
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.unclosed(start, '{')
		}
		if p.src[p.pos] == '}' {
			p.pos++
			return n, nil
		}
		keyOff := p.pos
		if p.src[p.pos] != '"' {
			if isWordStart(p.src[p.pos]) {
				return nil, p.errAt(keyOff, "object keys must be double-quoted strings")
			}
			return nil, p.errAt(keyOff, "expected a double-quoted key or '}', found %s", p.describeAt(keyOff))
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.unclosed(start, '{')
		}
		if p.src[p.pos] != ':' {
			return nil, p.errAt(p.pos, "expected ':' after the key %q, found %s", key, p.describeAt(p.pos))
		}
		p.pos++
		if err := p.skip(); err != nil {
			return nil, err
		}
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		if _, dup := n.mem[key]; dup {
			p.issues = append(p.issues, p.issueAt(keyOff, SeverityWarning, "duplicate key %q; the last one wins", key))
		} else {
			n.keys = append(n.keys, key)
		}
		n.mem[key] = &member{keyOff: keyOff, val: val}

		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.unclosed(start, '{')
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return n, nil
		case '"':
			return nil, p.errAt(p.pos, "missing ',' between members")
		default:
			return nil, p.errAt(p.pos, "expected ',' or '}', found %s", p.describeAt(p.pos))
		}
	}
}

func (p *parser) array() (*node, error) {
	start := p.pos
	if err := p.enter(start); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	p.pos++
	n := &node{kind: nArray, off: start}
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.unclosed(start, '[')
		}
		if p.src[p.pos] == ']' {
			p.pos++
			return n, nil
		}
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		n.arr = append(n.arr, val)
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.unclosed(start, '[')
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return n, nil
		default:
			if c := p.src[p.pos]; c == '"' || c == '{' || c == '[' || c == '-' || (c >= '0' && c <= '9') || isWordStart(c) {
				return nil, p.errAt(p.pos, "missing ',' between list items")
			}
			return nil, p.errAt(p.pos, "expected ',' or ']', found %s", p.describeAt(p.pos))
		}
	}
}

// toValue converts a node into plain Go values (map[string]any, []any, string,
// json.Number, bool, nil), the form layers are merged in.
func toValue(n *node) any {
	switch n.kind {
	case nObject:
		m := make(map[string]any, len(n.keys))
		for _, k := range n.keys {
			m[k] = toValue(n.mem[k].val)
		}
		return m
	case nArray:
		a := make([]any, len(n.arr))
		for i, e := range n.arr {
			a[i] = toValue(e)
		}
		return a
	case nString:
		return n.str
	case nNumber:
		return json.Number(n.num)
	case nBool:
		return n.b
	}
	return nil
}

// locate finds the node at a path of segments (object keys and "[i]" indexes)
// and, when the path ends at an object member, the offset of its key.
func (n *node) locate(segs []string) (val *node, keyOff int, ok bool) {
	cur := n
	keyOff = -1
	for _, s := range segs {
		switch {
		case cur.kind == nObject:
			m := cur.mem[s]
			if m == nil {
				return nil, -1, false
			}
			cur, keyOff = m.val, m.keyOff
		case cur.kind == nArray && isIndexSeg(s):
			i, err := strconv.Atoi(s[1 : len(s)-1])
			if err != nil || i < 0 || i >= len(cur.arr) {
				return nil, -1, false
			}
			cur, keyOff = cur.arr[i], -1
		default:
			return nil, -1, false
		}
	}
	return cur, keyOff, true
}

// nodeFromValue builds a node tree from a Go value (for overrides). It has no
// source positions.
func nodeFromValue(v any) (*node, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	return fromTree(tree), nil
}

func fromTree(v any) *node {
	switch t := v.(type) {
	case map[string]any:
		n := &node{kind: nObject, off: -1, mem: make(map[string]*member, len(t))}
		for _, k := range sortedKeys(t) {
			n.keys = append(n.keys, k)
			n.mem[k] = &member{keyOff: -1, val: fromTree(t[k])}
		}
		return n
	case []any:
		n := &node{kind: nArray, off: -1}
		for _, e := range t {
			n.arr = append(n.arr, fromTree(e))
		}
		return n
	case string:
		return &node{kind: nString, off: -1, str: t}
	case json.Number:
		return &node{kind: nNumber, off: -1, num: t.String()}
	case bool:
		return &node{kind: nBool, off: -1, b: t}
	}
	return &node{kind: nNull, off: -1}
}
