package shellparse

import (
	"strings"
	"unicode/utf8"
)

// wbuf accumulates a word's value together with a mask: mask[i] is 1 when
// value[i] came from quoting, an escape or an expansion and therefore must not
// take part in brace expansion.
type wbuf struct {
	v, m []byte
}

func (w *wbuf) raw(c byte) { w.v = append(w.v, c); w.m = append(w.m, 0) }
func (w *wbuf) lit(c byte) { w.v = append(w.v, c); w.m = append(w.m, 1) }
func (w *wbuf) litStr(s string) {
	for i := 0; i < len(s); i++ {
		w.lit(s[i])
	}
}

// clamp keeps l.pos inside the input after a skip that may overshoot.
func (l *lexer) clamp() {
	if l.pos > l.end {
		l.pos = l.end
	}
}

// lexWord reads one word at l.pos. It usually returns a single token; an
// unquoted brace expression such as {a,b} makes it several, exactly as the
// shell would. noBrace suppresses that (heredoc delimiters).
func (l *lexer) lexWord(noBrace bool) []token {
	start := l.pos
	var w wbuf
	var nested []Simple
	quoted, assign, assignOK := false, false, true
loop:
	for l.pos < l.end && l.st.tick() {
		c := l.src[l.pos]
		switch c {
		case ' ', '\t', '\n', ';', '&', '|', '(', ')', '<', '>':
			break loop
		case '\\':
			if l.pos+1 >= l.end {
				w.lit('\\')
				quoted, assignOK = true, false
				l.pos++
				continue
			}
			if l.src[l.pos+1] == '\n' { // line continuation joins the word
				l.pos += 2
				continue
			}
			_, size := utf8.DecodeRuneInString(l.src[l.pos+1 : l.end])
			w.litStr(l.src[l.pos+1 : l.pos+1+size])
			quoted, assignOK = true, false
			l.pos += 1 + size
		case '\'':
			quoted, assignOK = true, false
			j := strings.IndexByte(l.src[l.pos+1:l.end], '\'')
			if j < 0 {
				w.litStr(l.src[l.pos+1 : l.end])
				l.pos = l.end
				l.st.problem("unterminated single quote")
				break loop
			}
			w.litStr(l.src[l.pos+1 : l.pos+1+j])
			l.pos += j + 2
		case '"':
			quoted, assignOK = true, false
			l.pos++
			if !l.dqBody(&w, &nested) {
				l.st.problem("unterminated double quote")
			}
		case '`':
			assignOK = false
			l.backtick(&w, &nested, false)
		case '$':
			assignOK = false
			if l.pos+1 < l.end && (l.src[l.pos+1] == '\'' || l.src[l.pos+1] == '"') {
				quoted = true
			}
			l.dollar(&w, &nested, false)
		default:
			if assignOK {
				switch {
				case isNameChar(c) && !(len(w.v) == 0 && isDigit(c)):
				case c == '+' && len(w.v) > 0 && l.pos+1 < l.end && l.src[l.pos+1] == '=':
				case c == '=' && len(w.v) > 0:
					assign, assignOK = true, false
				default:
					assignOK = false
				}
			}
			w.raw(c)
			l.pos++
		}
	}
	l.clamp()
	if l.pos == start { // defensive: never return without progress
		l.pos++
		return nil
	}
	tok := token{kind: tkWord, start: start, end: l.pos, quoted: quoted, assign: assign}
	tok.text = string(w.v)
	tok.nested = nested
	if noBrace || !hasBraceCandidate(&w) {
		return []token{tok}
	}
	vals, over := braceExpand(w.v, w.m)
	if over {
		l.st.problem("brace expansion too large")
	}
	if assign {
		// NAME={a,b} is one assignment before the command word but two
		// arguments after it; only the parser knows which, so keep both.
		tok.alts = vals
		return []token{tok}
	}
	out := make([]token, 0, len(vals))
	for _, v := range vals {
		if v == "" && !quoted {
			continue // like any unquoted empty expansion, an empty alternative vanishes: {,a} is just "a"
		}
		t := tok
		t.text = v
		t.nested = nil
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	out[0].nested = nested
	return out
}

// dqBody reads the inside of a double-quoted string; l.pos is just past the
// opening quote. Expansions stay active inside double quotes, which is why
// "$(rm -rf ~)" is still a command substitution.
func (l *lexer) dqBody(w *wbuf, nested *[]Simple) bool {
	for l.pos < l.end && l.st.tick() {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.pos++
			return true
		case '\\':
			if l.pos+1 >= l.end {
				w.lit('\\')
				l.pos++
				continue
			}
			switch n := l.src[l.pos+1]; n {
			case '$', '`', '"', '\\':
				w.lit(n)
				l.pos += 2
			case '\n':
				l.pos += 2
			default:
				w.lit('\\')
				l.pos++
			}
		case '$':
			l.dollar(w, nested, true)
		case '`':
			l.backtick(w, nested, true)
		default:
			w.lit(c)
			l.pos++
		}
	}
	return false
}

// dollar handles an expansion that starts with '$' at l.pos, appending its
// source text (or, for $'..', its decoded value) to w. dq is true inside double
// quotes, where $'..' is not special.
func (l *lexer) dollar(w *wbuf, nested *[]Simple, dq bool) {
	i := l.pos
	if i+1 >= l.end {
		w.lit('$')
		l.pos++
		return
	}
	n := l.src[i+1]
	switch {
	case n == '(':
		if i+2 < l.end && l.src[i+2] == '(' && l.arith(w, nested) {
			return
		}
		l.cmdSubst(w, nested)
	case n == '{':
		l.paramExp(w, nested)
	case n == '\'' && !dq:
		l.ansiC(w)
	case n == '"' && !dq:
		l.pos++ // $"..." is a locale-translated double-quoted string
	case isNameStart(n):
		j := i + 2
		for j < l.end && isNameChar(l.src[j]) {
			j++
		}
		w.litStr(l.src[i:j])
		l.pos = j
	case isDigit(n) || strings.IndexByte("@*#?$!-", n) >= 0:
		w.litStr(l.src[i : i+2])
		l.pos = i + 2
	default:
		w.lit('$')
		l.pos++
	}
}

// cmdSubst reads $( ... ) at l.pos.
func (l *lexer) cmdSubst(w *wbuf, nested *[]Simple) {
	start := l.pos
	if strings.HasPrefix(l.src[l.pos:l.end], "$(pwd)") {
		// the idiom for "here" (cd "$(pwd)") is the same text as $PWD to every check that follows
		w.litStr("$PWD")
		l.pos += len("$(pwd)")
		return
	}
	l.st.an.HasCommandSubstitution = true
	l.pos += 2
	if l.depth >= maxDepth {
		l.st.problem("nesting too deep")
		l.pos = l.end
		return
	}
	sub := &lexer{src: l.src, pos: l.pos, end: l.end, st: l.st, depth: l.depth + 1}
	toks, closed := sub.lexUntil(true)
	l.pos = sub.pos
	if !closed {
		l.st.problem("unterminated $(")
	}
	w.litStr(l.src[start:l.pos])
	*nested = append(*nested, parseTokens(l.st, l.src, toks, l.depth+1, false, true)...)
}

// backtick reads a legacy `...` substitution at l.pos. The body is unescaped
// (\` \\ \$) and parsed as its own command line.
func (l *lexer) backtick(w *wbuf, nested *[]Simple, dq bool) {
	start := l.pos
	l.st.an.HasCommandSubstitution = true
	l.pos++
	var body []byte
	closed := false
	for l.pos < l.end && l.st.tick() {
		c := l.src[l.pos]
		if c == '`' {
			l.pos++
			closed = true
			break
		}
		if c == '\\' && l.pos+1 < l.end {
			if n := l.src[l.pos+1]; n == '`' || n == '\\' || n == '$' || (dq && n == '"') {
				body = append(body, n)
				l.pos += 2
				continue
			}
		}
		body = append(body, c)
		l.pos++
	}
	if !closed {
		l.st.problem("unterminated backquote")
	}
	w.litStr(l.src[start:l.pos])
	if l.depth >= maxDepth {
		l.st.problem("nesting too deep")
		return
	}
	inner := string(body)
	sub := newLexer(inner, l.st, l.depth+1)
	toks, _ := sub.lexUntil(false)
	*nested = append(*nested, parseTokens(l.st, inner, toks, l.depth+1, false, true)...)
}

// arith reads $(( ... )) at l.pos. It reports false, consuming nothing, when
// the text is not arithmetic after all ($((a);(b)) is a subshell inside a
// command substitution), so the caller can fall back to cmdSubst.
func (l *lexer) arith(w *wbuf, nested *[]Simple) bool {
	start := l.pos
	if l.nest >= maxNest {
		return false // fall back to cmdSubst, which is depth limited
	}
	depth := 0
	for i := start + 3; i < l.end; i++ {
		switch l.src[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
				continue
			}
			if i+1 < l.end && l.src[i+1] == ')' {
				l.nest++
				l.scanExpansions(start+3, i, nested)
				l.nest--
				w.litStr(l.src[start : i+2])
				l.pos = i + 2
				return true
			}
			return false
		}
	}
	return false
}

// paramExp reads ${ ... } at l.pos, extracting substitutions in its operands
// (${x:-$(cmd)} runs cmd).
func (l *lexer) paramExp(w *wbuf, nested *[]Simple) {
	start := l.pos
	l.pos += 2
	if !l.enter() {
		return
	}
	defer l.leave()
	var scratch wbuf
	closed := false
	for l.pos < l.end && !closed && l.st.tick() {
		switch l.src[l.pos] {
		case '}':
			l.pos++
			closed = true
		case '\\':
			l.pos += 2
		case '\'':
			if j := strings.IndexByte(l.src[l.pos+1:l.end], '\''); j < 0 {
				l.pos = l.end
			} else {
				l.pos += j + 2
			}
		case '"':
			l.pos++
			l.dqBody(&scratch, nested)
		case '$':
			l.dollar(&scratch, nested, false)
		case '`':
			l.backtick(&scratch, nested, false)
		default:
			l.pos++
		}
	}
	l.clamp()
	if !closed {
		l.st.problem("unterminated ${")
	}
	w.litStr(l.src[start:l.pos])
}

// scanExpansions looks for substitutions in src[from:to] (a heredoc body or an
// arithmetic expression) and collects their commands.
func (l *lexer) scanExpansions(from, to int, nested *[]Simple) {
	savePos, saveEnd := l.pos, l.end
	l.pos, l.end = from, to
	var scratch wbuf
	for l.pos < l.end && l.st.tick() {
		switch l.src[l.pos] {
		case '\\':
			l.pos += 2
		case '$':
			l.dollar(&scratch, nested, true)
		case '`':
			l.backtick(&scratch, nested, true)
		default:
			l.pos++
		}
	}
	l.pos, l.end = savePos, saveEnd
}

// ansiC decodes $'...' at l.pos (l.src[l.pos:] starts with $'). Escapes such as
// \x2f and \057 are decoded because they can spell a path or command the raw
// text does not show. Like bash, a NUL ends the string.
func (l *lexer) ansiC(w *wbuf) {
	i := l.pos + 2
	stopped := false
	emit := func(b ...byte) {
		if stopped {
			return
		}
		for _, c := range b {
			if c == 0 {
				stopped = true
				return
			}
			w.lit(c)
		}
	}
	for i < l.end && l.st.tick() {
		c := l.src[i]
		if c == '\'' {
			l.pos = i + 1
			return
		}
		if c != '\\' {
			emit(c)
			i++
			continue
		}
		if i+1 >= l.end {
			break
		}
		e := l.src[i+1]
		i += 2
		switch e {
		case 'a':
			emit(7)
		case 'b':
			emit(8)
		case 'e', 'E':
			emit(27)
		case 'f':
			emit(12)
		case 'n':
			emit(10)
		case 'r':
			emit(13)
		case 't':
			emit(9)
		case 'v':
			emit(11)
		case '\\', '\'', '"', '?':
			emit(e)
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := int(e - '0')
			for k := 0; k < 2 && i < l.end && l.src[i] >= '0' && l.src[i] <= '7'; k++ {
				v = v*8 + int(l.src[i]-'0')
				i++
			}
			emit(byte(v))
		case 'x', 'u', 'U':
			max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			v, n := 0, 0
			for n < max && i < l.end {
				d := hexVal(l.src[i])
				if d < 0 {
					break
				}
				v = v*16 + d
				i++
				n++
			}
			switch {
			case n == 0:
				emit('\\', e)
			case e == 'x':
				emit(byte(v))
			default:
				var buf [4]byte
				emit(buf[:utf8.EncodeRune(buf[:], rune(v))]...)
			}
		case 'c':
			if i < l.end {
				emit(l.src[i] & 0x1f)
				i++
			} else {
				emit('\\', 'c')
			}
		default:
			emit('\\', e)
		}
	}
	l.st.problem("unterminated $'")
	l.pos = l.end
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
