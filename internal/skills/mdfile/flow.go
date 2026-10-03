package mdfile

import (
	"fmt"
	"strings"
)

// flow parses a flow collection ("[a, b]", "{a: 1}") that starts the value on
// line ln. It may continue over several lines. Text after the closing bracket,
// or a collection that does not parse on a single line, makes the whole value a
// plain string: "[issue] [priority]" and "{{name}}" are hints and templates that
// authors write, not collections.
func (p *yparser) flow(rest string, ln yamlLine, depth int) (Value, error) {
	buf := rest
	for lines := 0; ; lines++ {
		end, balanced, err := flowEnd(buf)
		if err != nil {
			if lines == 0 {
				return plainScalar(cutComment(rest), ln.no), nil
			}
			return Value{}, errAt(ln.no, "%v", err)
		}
		if balanced {
			tail := strings.TrimSpace(buf[end:])
			if tail != "" && tail[0] != '#' {
				if lines == 0 {
					return plainScalar(cutComment(rest), ln.no), nil
				}
				return Value{}, errAt(ln.no, "unexpected text %q after the closing bracket", clip(tail, 30))
			}
			fp := &flowParser{s: buf[:end], line: ln.no, p: p}
			v, err := fp.top(depth)
			if err != nil {
				if lines == 0 {
					return plainScalar(cutComment(rest), ln.no), nil
				}
				return Value{}, err
			}
			v.Raw = strings.Join(strings.Fields(buf[:end]), " ")
			return v, nil
		}
		if p.i >= len(p.lines) || lines >= maxContinue {
			return Value{}, errAt(ln.no, "unterminated %q: the closing bracket is missing", clip(rest, 20))
		}
		buf += "\n" + p.lines[p.i].raw
		p.i++
	}
}

// flowEnd finds the end of the flow collection that starts at s[0]. balanced is
// false when s ends before the collection does (the caller appends a line).
// Quotes only open where a scalar can start, so an apostrophe inside a plain
// word ("don't") is not mistaken for one.
func flowEnd(s string) (end int, balanced bool, err error) {
	depth := 0
	var prev byte // last significant byte outside quotes and comments
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '[' || c == '{':
			depth++
			prev = c
		case c == ']' || c == '}':
			depth--
			if depth < 0 {
				return 0, false, fmt.Errorf("unexpected %q", string(c))
			}
			prev = c
			if depth == 0 {
				return i + 1, true, nil
			}
		case (c == '"' || c == '\'') && (prev == 0 || prev == '[' || prev == '{' || prev == ',' || prev == ':'):
			_, e, closed, qerr := scanQuoted(s[i:])
			if qerr != nil {
				return 0, false, qerr
			}
			if !closed {
				return len(s), false, nil
			}
			i += e - 1
			prev = 'q'
		case c == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '\n'):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return len(s), false, nil
			}
			i += j
		case c == ' ' || c == '\t' || c == '\n':
		default:
			prev = c
		}
	}
	return len(s), false, nil
}

type flowParser struct {
	s    string
	i    int
	line int
	p    *yparser
}

// top parses one flow value and rejects trailing text after whitespace and comments.
func (f *flowParser) top(depth int) (Value, error) {
	v, err := f.value(depth)
	if err != nil {
		return Value{}, err
	}
	f.skip()
	if f.i != len(f.s) {
		return Value{}, errAt(f.line, "unexpected text after the collection")
	}
	return v, nil
}

// skip advances over whitespace and hash comments whose marker follows whitespace.
func (f *flowParser) skip() {
	for f.i < len(f.s) {
		c := f.s[f.i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			f.i++
		case c == '#' && f.i > 0 && (f.s[f.i-1] == ' ' || f.s[f.i-1] == '\t' || f.s[f.i-1] == '\n'):
			if j := strings.IndexByte(f.s[f.i:], '\n'); j < 0 {
				f.i = len(f.s)
			} else {
				f.i += j
			}
		default:
			return
		}
	}
}

func (f *flowParser) value(depth int) (Value, error) {
	if depth > maxDepth {
		return Value{}, errAt(f.line, "frontmatter is nested more than %d levels deep", maxDepth)
	}
	f.skip()
	if f.i >= len(f.s) {
		return Value{}, errAt(f.line, "a value is missing in the collection")
	}
	if err := f.p.count(f.line); err != nil {
		return Value{}, err
	}
	switch f.s[f.i] {
	case '[':
		return f.seq(depth)
	case '{':
		return f.mapv(depth)
	case '"', '\'':
		s, end, closed, err := scanQuoted(f.s[f.i:])
		if err != nil || !closed {
			return Value{}, errAt(f.line, "bad quoted string in the collection")
		}
		f.i += end
		return Value{Kind: KindString, Str: s, Quoted: true, Line: f.line}, nil
	}
	s := f.plain(false)
	if s == "" {
		return Value{}, errAt(f.line, "an empty item in the collection")
	}
	return plainScalar(s, f.line), nil
}

// plain reads an unquoted scalar up to the next flow indicator. In a mapping
// key position it also stops at a colon that ends the key.
func (f *flowParser) plain(stopColon bool) string {
	start := f.i
	for f.i < len(f.s) {
		c := f.s[f.i]
		if c == ',' || c == ']' || c == '}' {
			break
		}
		if stopColon && c == ':' && (f.i+1 >= len(f.s) || strings.IndexByte(" \t\n,]}", f.s[f.i+1]) >= 0) {
			break
		}
		if c == '#' && f.i > start && (f.s[f.i-1] == ' ' || f.s[f.i-1] == '\t' || f.s[f.i-1] == '\n') {
			break
		}
		f.i++
	}
	s := strings.TrimSpace(f.s[start:f.i])
	if strings.IndexByte(s, '\n') >= 0 {
		s = strings.Join(strings.Fields(s), " ")
	}
	return s
}

func (f *flowParser) seq(depth int) (Value, error) {
	f.i++ // [
	v := Value{Kind: KindList, Line: f.line}
	for {
		f.skip()
		if f.i >= len(f.s) {
			return Value{}, errAt(f.line, "unterminated list")
		}
		if f.s[f.i] == ']' {
			f.i++
			return v, nil
		}
		item, err := f.value(depth + 1)
		if err != nil {
			return Value{}, err
		}
		v.List = append(v.List, item)
		f.skip()
		if f.i >= len(f.s) {
			return Value{}, errAt(f.line, "unterminated list")
		}
		switch f.s[f.i] {
		case ',':
			f.i++
		case ']':
			f.i++
			return v, nil
		default:
			return Value{}, errAt(f.line, "expected \",\" or \"]\" in the list, found %q", string(f.s[f.i]))
		}
	}
}

func (f *flowParser) mapv(depth int) (Value, error) {
	f.i++ // {
	v := Value{Kind: KindMap, Line: f.line}
	seen := map[string]bool{}
	for {
		f.skip()
		if f.i >= len(f.s) {
			return Value{}, errAt(f.line, "unterminated mapping")
		}
		if f.s[f.i] == '}' {
			f.i++
			return v, nil
		}
		var key string
		if c := f.s[f.i]; c == '"' || c == '\'' {
			s, end, closed, err := scanQuoted(f.s[f.i:])
			if err != nil || !closed {
				return Value{}, errAt(f.line, "bad quoted key in the mapping")
			}
			f.i += end
			key = s
		} else {
			key = f.plain(true)
		}
		if key == "" {
			return Value{}, errAt(f.line, "an empty key in the mapping")
		}
		if len(key) > maxKeyLen {
			return Value{}, errAt(f.line, "key %q is longer than %d characters", clip(key, 20), maxKeyLen)
		}
		if seen[key] {
			return Value{}, errAt(f.line, "duplicate key %q", key)
		}
		seen[key] = true
		f.skip()
		val := Value{Kind: KindNull, Line: f.line}
		if f.i < len(f.s) && f.s[f.i] == ':' {
			f.i++
			f.skip()
			if f.i < len(f.s) && f.s[f.i] != ',' && f.s[f.i] != '}' {
				var err error
				if val, err = f.value(depth + 1); err != nil {
					return Value{}, err
				}
			}
		}
		v.Keys = append(v.Keys, key)
		v.Vals = append(v.Vals, val)
		f.skip()
		if f.i >= len(f.s) {
			return Value{}, errAt(f.line, "unterminated mapping")
		}
		switch f.s[f.i] {
		case ',':
			f.i++
		case '}':
			f.i++
			return v, nil
		default:
			return Value{}, errAt(f.line, "expected \",\" or \"}\" in the mapping, found %q", string(f.s[f.i]))
		}
	}
}
