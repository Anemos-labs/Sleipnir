package mdfile

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The YAML dialect of frontmatter.
//
// There is no YAML dependency, and full YAML is a poor fit for a file that
// arrives from a repository: anchors, aliases, tags, merge keys and multiple
// documents are all ways to make a small file mean something a reviewer cannot
// see. This parser accepts the block-structured subset that Claude Code style
// frontmatter uses and rejects nothing else silently:
//
//   - block mappings and block sequences, nested to a bounded depth, including
//     a sequence written at the same indentation as its key and sequences of
//     mappings ("- key: value");
//   - flow sequences and mappings ("[a, b]", "{a: 1}"), possibly over several
//     lines;
//   - plain, single-quoted and double-quoted scalars, folded over several
//     lines when they continue on more-indented lines;
//   - literal and folded block scalars ("|", ">") with chomping and explicit
//     indentation indicators;
//   - "#" comments.
//
// Everything is text (see Value): true, 12 and null are typed by whoever reads
// the field, not here.
//
// Anchors, aliases, tags and "<<" are not interpreted; a value that begins with
// "&", "*" or "!" is an ordinary string. Two things are deliberately more
// forgiving than YAML because real files depend on them and the result is
// unambiguous: a plain scalar may contain ": " ("description: Use when: the
// user asks"), and a value that starts like a flow collection but is followed
// by more text ("argument-hint: [issue] [priority]") is a plain string.
//
// Everything structural is strict, with the line number in the error:
// indentation that does not line up, tabs used for indentation, duplicate keys,
// unterminated quotes or brackets, and nesting or size beyond the limits.

const (
	maxDepth    = 8    // nesting of blocks and flow collections
	maxNodes    = 4096 // values in one document
	maxKeyLen   = 128
	maxContinue = 200 // lines of one multi-line scalar or flow collection
)

type yamlLine struct {
	no     int    // 1-based line number in the file
	indent int    // leading spaces
	text   string // content after the indentation, without trailing white space
	raw    string // the line as it is, for block scalars
	tab    bool   // indentation continues with a tab (an error where it matters)
}

// blank reports whether a parsed YAML line has empty text.
func (l yamlLine) blank() bool { return l.text == "" }

// comment identifies parsed YAML lines whose text begins with a comment marker.
func (l yamlLine) comment() bool { return l.text != "" && l.text[0] == '#' }

// SyntaxError is a located frontmatter error.
type SyntaxError struct {
	Line int
	Msg  string
}

// Error prefixes a YAML syntax message with its line number when known.
func (e *SyntaxError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

// errAt formats a syntax error with the source line responsible for it.
func errAt(line int, format string, args ...any) error {
	return &SyntaxError{Line: line, Msg: fmt.Sprintf(format, args...)}
}

type yparser struct {
	lines []yamlLine
	i     int
	pend  *yamlLine // a synthetic line pushed back ("- key: v" becomes "key: v" one column in)
	nodes int
}

// parseYAML parses the frontmatter region (the lines between the fences) whose
// first line is line firstNo of the file.
func parseYAML(region string, firstNo int) (Value, error) {
	parts := strings.Split(region, "\n")
	lines := make([]yamlLine, 0, len(parts))
	for i, raw := range parts {
		indent := 0
		for indent < len(raw) && raw[indent] == ' ' {
			indent++
		}
		text := strings.TrimRight(raw[indent:], " \t")
		l := yamlLine{no: firstNo + i, indent: indent, text: text, raw: raw}
		if text != "" && text[0] == '\t' {
			l.tab = true
			l.text = strings.TrimLeft(text, "\t")
		}
		lines = append(lines, l)
	}
	p := &yparser{lines: lines}
	first := p.peek()
	if first == nil {
		return Value{Kind: KindMap}, nil
	}
	if first.tab {
		return Value{}, errAt(first.no, "tabs cannot be used for indentation; use spaces")
	}
	if isSeqItem(first.text) {
		return Value{}, errAt(first.no, "frontmatter must be \"key: value\" pairs, not a list")
	}
	if _, _, ok := splitKey(first.text); !ok {
		return Value{}, errAt(first.no, "frontmatter must be \"key: value\" pairs; found %q", clip(first.text, 40))
	}
	v, err := p.mapping(first.indent, 0)
	if err != nil {
		return Value{}, err
	}
	if extra := p.peek(); extra != nil {
		return Value{}, errAt(extra.no, "unexpected content %q (check the indentation)", clip(extra.text, 40))
	}
	return v, nil
}

// peek returns the pending or next meaningful YAML line, advancing past blank lines and comments;
// it returns nil at EOF.
func (p *yparser) peek() *yamlLine {
	if p.pend != nil {
		return p.pend
	}
	for p.i < len(p.lines) {
		l := &p.lines[p.i]
		if l.blank() || l.comment() {
			p.i++
			continue
		}
		return l
	}
	return nil
}

// next consumes a pending synthetic line before advancing the underlying line index.
func (p *yparser) next() {
	if p.pend != nil {
		p.pend = nil
		return
	}
	p.i++
}

// count increments parsed frontmatter values and returns a positioned error when the complexity
// cap is exceeded.
func (p *yparser) count(line int) error {
	p.nodes++
	if p.nodes > maxNodes {
		return errAt(line, "frontmatter is too complex (more than %d values)", maxNodes)
	}
	return nil
}

// isSeqItem recognizes a bare dash or a dash followed by a space as a YAML sequence marker.
func isSeqItem(t string) bool { return t == "-" || strings.HasPrefix(t, "- ") }

// splitKey splits "key: rest". The colon must be followed by a space or end the
// line, so "http://x" and "a:b" are not keys. Quoted keys are accepted.
func splitKey(t string) (key, rest string, ok bool) {
	if t == "" {
		return "", "", false
	}
	switch t[0] {
	case '"', '\'':
		s, end, closed, err := scanQuoted(t)
		if err != nil || !closed {
			return "", "", false
		}
		after := strings.TrimLeft(t[end:], " ")
		if !strings.HasPrefix(after, ":") || (len(after) > 1 && after[1] != ' ') {
			return "", "", false
		}
		if s == "" {
			return "", "", false
		}
		return s, strings.TrimLeft(after[1:], " "), true
	case '[', '{', '#', '|', '>':
		return "", "", false // a flow collection, comment or block scalar, never a plain key
	}
	if isSeqItem(t) {
		return "", "", false // "- item" is a sequence entry ("-a: b" is a key)
	}
	for i := 0; i < len(t); i++ {
		if t[i] != ':' {
			continue
		}
		if i+1 == len(t) || t[i+1] == ' ' {
			key = strings.TrimRight(t[:i], " ")
			if key == "" {
				return "", "", false
			}
			return key, strings.TrimLeft(t[i+1:], " "), true
		}
	}
	return "", "", false
}

// mapping parses a block mapping whose keys are at column indent.
func (p *yparser) mapping(indent, depth int) (Value, error) {
	first := p.peek()
	if depth > maxDepth {
		return Value{}, errAt(first.no, "frontmatter is nested more than %d levels deep", maxDepth)
	}
	v := Value{Kind: KindMap, Line: first.no}
	seen := map[string]int{}
	for {
		l := p.peek()
		if l == nil || l.indent < indent {
			break
		}
		if l.indent > indent {
			return Value{}, errAt(l.no, "unexpected indentation (%d spaces here, %d in the block above)", l.indent, indent)
		}
		if l.tab {
			return Value{}, errAt(l.no, "tabs cannot be used for indentation; use spaces")
		}
		if isSeqItem(l.text) {
			break
		}
		key, rest, ok := splitKey(l.text)
		if !ok {
			return Value{}, errAt(l.no, "expected \"key: value\", found %q", clip(l.text, 40))
		}
		if len(key) > maxKeyLen {
			return Value{}, errAt(l.no, "key %q is longer than %d characters", clip(key, 20), maxKeyLen)
		}
		if at, dup := seen[key]; dup {
			return Value{}, errAt(l.no, "duplicate key %q (first on line %d)", key, at)
		}
		seen[key] = l.no
		ln := *l
		p.next()
		val, err := p.value(rest, ln, indent, depth+1, true)
		if err != nil {
			return Value{}, err
		}
		if err := p.count(ln.no); err != nil {
			return Value{}, err
		}
		v.Keys = append(v.Keys, key)
		v.Vals = append(v.Vals, val)
	}
	return v, nil
}

// sequence parses a block sequence whose dashes are at column indent.
func (p *yparser) sequence(indent, depth int) (Value, error) {
	first := p.peek()
	if depth > maxDepth {
		return Value{}, errAt(first.no, "frontmatter is nested more than %d levels deep", maxDepth)
	}
	v := Value{Kind: KindList, Line: first.no}
	for {
		l := p.peek()
		if l == nil || l.indent < indent {
			break
		}
		if l.indent > indent {
			return Value{}, errAt(l.no, "unexpected indentation (%d spaces here, %d in the list above)", l.indent, indent)
		}
		if !isSeqItem(l.text) {
			break
		}
		if l.tab {
			return Value{}, errAt(l.no, "tabs cannot be used for indentation; use spaces")
		}
		ln := *l
		p.next()
		rest := ln.text[1:]
		trimmed := strings.TrimLeft(rest, " ")
		off := 1 + len(rest) - len(trimmed) // columns taken by "-" and the spaces after it
		var (
			item Value
			err  error
		)
		switch {
		case trimmed == "" || trimmed[0] == '#':
			item, err = p.nested(ln, indent, depth+1, false)
		case isSeqItem(trimmed):
			p.pend = &yamlLine{no: ln.no, indent: ln.indent + off, text: trimmed, raw: strings.Repeat(" ", ln.indent+off) + trimmed}
			item, err = p.sequence(ln.indent+off, depth+1)
		default:
			if _, _, isMap := splitKey(trimmed); isMap {
				p.pend = &yamlLine{no: ln.no, indent: ln.indent + off, text: trimmed, raw: strings.Repeat(" ", ln.indent+off) + trimmed}
				item, err = p.mapping(ln.indent+off, depth+1)
			} else {
				item, err = p.value(trimmed, ln, indent, depth+1, false)
			}
		}
		if err != nil {
			return Value{}, err
		}
		if err := p.count(ln.no); err != nil {
			return Value{}, err
		}
		v.List = append(v.List, item)
	}
	return v, nil
}

// nested parses what follows an empty "key:" or "-": a block on the next lines
// (more indented than the parent), or, for a mapping key, a sequence at the
// key's own indentation, or nothing.
func (p *yparser) nested(ln yamlLine, parentIndent, depth int, inMap bool) (Value, error) {
	l := p.peek()
	if l == nil {
		return Value{Kind: KindNull, Line: ln.no}, nil
	}
	if l.indent > parentIndent {
		return p.node(l.indent, depth)
	}
	if inMap && l.indent == parentIndent && isSeqItem(l.text) {
		return p.sequence(l.indent, depth)
	}
	return Value{Kind: KindNull, Line: ln.no}, nil
}

// node parses the block that starts at the next line, at column indent.
func (p *yparser) node(indent, depth int) (Value, error) {
	l := p.peek()
	if l.tab {
		return Value{}, errAt(l.no, "tabs cannot be used for indentation; use spaces")
	}
	switch {
	case isSeqItem(l.text):
		return p.sequence(indent, depth)
	default:
		if _, _, ok := splitKey(l.text); ok {
			return p.mapping(indent, depth)
		}
	}
	// A scalar (or flow collection) on a line of its own.
	ln := *l
	p.next()
	return p.value(ln.text, ln, indent-1, depth, false)
}

// value parses the text that follows "key:" or "- " on line ln. Continuation
// lines must be indented more than parentIndent.
func (p *yparser) value(rest string, ln yamlLine, parentIndent, depth int, inMap bool) (Value, error) {
	rest = strings.TrimLeft(rest, " ")
	if rest == "" || rest[0] == '#' {
		return p.nested(ln, parentIndent, depth, inMap)
	}
	switch rest[0] {
	case '|', '>':
		if v, ok, err := p.blockScalar(rest, ln, parentIndent); ok || err != nil {
			return v, err
		}
	case '[', '{':
		return p.flow(rest, ln, depth)
	case '"', '\'':
		return p.quoted(rest, ln)
	}
	return p.plain(rest, ln, parentIndent)
}

// plainScalar makes the Value of unquoted text: empty and null-like words are
// KindNull, everything else a string.
func plainScalar(s string, line int) Value {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return Value{Kind: KindNull, Line: line}
	}
	return Value{Kind: KindString, Str: s, Line: line}
}

// cutComment removes a trailing " #..." comment from plain text.
func cutComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return strings.TrimRight(s[:i], " \t")
		}
	}
	return strings.TrimRight(s, " \t")
}

// plain parses a plain scalar, folding continuation lines: a line break
// becomes one space and a blank line becomes a newline.
func (p *yparser) plain(rest string, ln yamlLine, parentIndent int) (Value, error) {
	first := cutComment(rest)
	var b strings.Builder
	b.WriteString(first)
	blanks, lines := 0, 1
	multi := false
	for p.i < len(p.lines) && p.pend == nil {
		nl := &p.lines[p.i]
		if nl.blank() {
			blanks++
			p.i++
			continue
		}
		if nl.indent <= parentIndent || nl.comment() {
			break
		}
		txt := cutComment(nl.text)
		if txt == "" {
			break
		}
		p.i++
		lines++
		if lines > maxContinue {
			return Value{}, errAt(ln.no, "a value continues over more than %d lines", maxContinue)
		}
		if blanks > 0 {
			b.WriteString(strings.Repeat("\n", blanks))
		} else {
			b.WriteByte(' ')
		}
		blanks = 0
		b.WriteString(txt)
		multi = true
	}
	if !multi {
		return plainScalar(first, ln.no), nil
	}
	return Value{Kind: KindString, Str: b.String(), Line: ln.no}, nil
}

// blockScalar parses "|" and ">" scalars. ok is false when the text after the
// indicator is not a valid header, in which case the caller treats the value as
// plain text ("> quoted" is a string, not a folded scalar).
func (p *yparser) blockScalar(rest string, ln yamlLine, parentIndent int) (Value, bool, error) {
	style := rest[0]
	i := 1
	var chomp byte
	explicit := 0
indicators:
	for i < len(rest) {
		c := rest[i]
		switch {
		case (c == '+' || c == '-') && chomp == 0:
			chomp = c
		case c >= '1' && c <= '9' && explicit == 0:
			explicit = int(c - '0')
		default:
			break indicators
		}
		i++
	}
	// Only an optional comment, set off by a space, may follow the indicators.
	tail := rest[i:]
	if t := strings.TrimSpace(tail); t != "" && (t[0] != '#' || tail[0] != ' ') {
		return Value{}, false, nil
	}
	blockIndent := -1
	if explicit > 0 {
		blockIndent = max(parentIndent, 0) + explicit
	}
	var content []string
	j := p.i
	for j < len(p.lines) {
		l := p.lines[j]
		if l.blank() { // the line may hold spaces
			content = append(content, "")
			j++
			continue
		}
		if blockIndent < 0 {
			if l.indent <= parentIndent {
				break
			}
			blockIndent = l.indent
		}
		if l.indent < blockIndent {
			break
		}
		content = append(content, l.raw[blockIndent:])
		j++
		if len(content) > maxNodes {
			return Value{}, false, errAt(ln.no, "a block scalar is longer than %d lines", maxNodes)
		}
	}
	p.i = j
	end := len(content)
	for end > 0 && content[end-1] == "" {
		end--
	}
	body, trailing := content[:end], len(content)-end
	var s string
	if style == '|' {
		s = strings.Join(body, "\n")
	} else {
		s = foldLines(body)
	}
	switch chomp {
	case '-':
	case '+':
		if len(body) > 0 {
			s += "\n"
		}
		s += strings.Repeat("\n", trailing)
	default:
		if len(body) > 0 {
			s += "\n"
		}
	}
	return Value{Kind: KindString, Str: s, Line: ln.no}, true, nil
}

// foldLines joins the lines of a ">" scalar: neighbouring lines become one
// paragraph, blank lines become newlines, and more-indented lines keep their
// line breaks.
func foldLines(lines []string) string {
	var b strings.Builder
	prevMore := false
	blanks := 0
	started := false
	for _, ln := range lines {
		if ln == "" {
			blanks++
			continue
		}
		more := ln[0] == ' ' || ln[0] == '\t'
		switch {
		case !started:
			b.WriteString(strings.Repeat("\n", blanks))
		case blanks > 0 && !prevMore && !more:
			b.WriteString(strings.Repeat("\n", blanks))
		case blanks > 0:
			b.WriteString(strings.Repeat("\n", blanks+1))
		case prevMore || more:
			b.WriteByte('\n')
		default:
			b.WriteByte(' ')
		}
		started = true
		blanks = 0
		b.WriteString(ln)
		prevMore = more
	}
	return b.String()
}

// scanQuoted decodes the quoted scalar that starts at s[0]. closed is false
// when the input ends before the closing quote; end is the index just after it.
// Line breaks inside the quotes fold: one becomes a space, a run of blank lines
// becomes newlines.
func scanQuoted(s string) (out string, end int, closed bool, err error) {
	q := s[0]
	var b strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		switch {
		case q == '\'' && c == '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			return b.String(), i + 1, true, nil
		case q == '"' && c == '"':
			return b.String(), i + 1, true, nil
		case q == '"' && c == '\\':
			if i+1 >= len(s) {
				return "", 0, false, nil
			}
			i++
			switch e := s[i]; e {
			case '0':
				b.WriteByte(0)
			case 'a':
				b.WriteByte(7)
			case 'b':
				b.WriteByte(8)
			case 't', '\t':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'v':
				b.WriteByte(11)
			case 'f':
				b.WriteByte(12)
			case 'r':
				b.WriteByte('\r')
			case 'e':
				b.WriteByte(27)
			case ' ', '"', '/', '\\':
				b.WriteByte(e)
			case 'N':
				b.WriteString("\u0085")
			case '_':
				b.WriteString(" ")
			case 'L':
				b.WriteString(" ")
			case 'P':
				b.WriteString(" ")
			case 'x', 'u', 'U':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if i+n >= len(s) {
					return "", 0, false, nil
				}
				code, perr := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
				if perr != nil || !utf8.ValidRune(rune(code)) {
					return "", 0, false, fmt.Errorf("invalid escape \\%c%s", e, s[i+1:i+1+n])
				}
				b.WriteRune(rune(code))
				i += n
			case '\n':
				// An escaped line break joins the lines without a space.
				for i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t') {
					i++
				}
			default:
				return "", 0, false, fmt.Errorf("unknown escape \\%c", e)
			}
			i++
		case c == '\n':
			str := strings.TrimRight(b.String(), " \t")
			b.Reset()
			b.WriteString(str)
			nl := 1
			i++
			for i < len(s) {
				for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
					i++
				}
				if i < len(s) && s[i] == '\n' {
					nl++
					i++
					continue
				}
				break
			}
			if nl == 1 {
				b.WriteByte(' ')
			} else {
				b.WriteString(strings.Repeat("\n", nl-1))
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return "", 0, false, nil
}

// quoted parses a quoted scalar that may continue over several lines.
func (p *yparser) quoted(rest string, ln yamlLine) (Value, error) {
	buf := rest
	for extra := 0; ; extra++ {
		s, end, closed, err := scanQuoted(buf)
		if err != nil {
			return Value{}, errAt(ln.no, "%v", err)
		}
		if closed {
			tail := strings.TrimSpace(buf[end:])
			if tail != "" && tail[0] != '#' {
				if extra == 0 {
					return plainScalar(cutComment(rest), ln.no), nil // "quoted" and more text: a plain string
				}
				return Value{}, errAt(ln.no, "unexpected text %q after the closing quote", clip(tail, 30))
			}
			return Value{Kind: KindString, Str: s, Quoted: true, Line: ln.no}, nil
		}
		if p.i >= len(p.lines) || extra >= maxContinue {
			return Value{}, errAt(ln.no, "unterminated quoted string")
		}
		buf += "\n" + p.lines[p.i].raw
		p.i++
	}
}
