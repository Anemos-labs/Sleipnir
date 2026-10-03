package shellparse

import "strings"

type tokKind uint8

const (
	tkWord  tokKind = iota
	tkOp            // ; ;; ;& ;;& & && || | |& ( ) and newline
	tkRedir         // a redirection operator; the target is the next word token
)

// token is one lexical unit. Words carry their dequoted value; substitutions
// found inside a token are parsed while lexing and travel with it in nested so
// the parser can attach them to the command the token ends up in.
type token struct {
	kind   tokKind
	text   string
	fd     string // redirect: descriptor prefix as written ("2"), if any
	start  int
	end    int
	quoted bool // word contained quoting or escaping (reserved words need !quoted)
	assign bool // word starts with an unquoted NAME= (env assignment shape)
	nested []Simple
	alts   []string // assign-shaped words only: the brace expansion, if any

	// body is set on the delimiter word of a heredoc once its body has been
	// read; hasBody distinguishes an empty body from none.
	body    string
	hasBody bool
}

type pendingHeredoc struct {
	delim  string
	strip  bool // <<- : leading tabs are ignored on body lines
	quoted bool // quoted delimiter: the body is literal, no expansions
	tokIdx int  // index of the delimiter token in the slice being built
}

type lexer struct {
	src      string
	pos, end int
	st       *state
	depth    int // nesting of substitutions (each gets its own lexer)
	nest     int // nesting of ${...} and $((...)) inside this lexer
	heredocs []pendingHeredoc
}

// maxNest bounds ${ ${ ${ ... and $(( $(( ... inside one lexer; anything deeper
// is not real shell code and would only cost quadratic time.
const maxNest = 24

// enter records one more level of expansion nesting and reports whether it is
// allowed; on refusal the input is abandoned as unparsed.
func (l *lexer) enter() bool {
	if l.nest >= maxNest {
		l.st.problem("expansion nesting too deep")
		l.pos = l.end
		return false
	}
	l.nest++
	return true
}

// leave decrements the lexer nesting depth after a matching enter.
func (l *lexer) leave() { l.nest-- }

// newLexer scans the full source with shared parser state and the supplied expansion depth.
func newLexer(src string, st *state, depth int) *lexer {
	return &lexer{src: src, end: len(src), st: st, depth: depth}
}

// isDigit recognizes ASCII decimal digits in shell syntax.
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isNameStart accepts ASCII letters and underscore at the start of shell variable names.
func isNameStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// isNameChar accepts shell name-start characters and ASCII digits.
func isNameChar(c byte) bool { return isNameStart(c) || isDigit(c) }

// skipBlanks skips spaces, tabs and backslash-newline continuations.
func (l *lexer) skipBlanks() {
	for l.pos < l.end {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t':
			l.pos++
		case c == '\\' && l.pos+1 < l.end && l.src[l.pos+1] == '\n':
			l.pos += 2
		default:
			return
		}
	}
}

// lexUntil reads tokens to the end of input, or - when stopParen is set - to
// the ')' that closes the substitution being read. closed reports whether the
// terminator was found (always true at EOF when stopParen is unset).
func (l *lexer) lexUntil(stopParen bool) (toks []token, closed bool) {
	parens := 0
	op := func(text string, start int) {
		toks = append(toks, token{kind: tkOp, text: text, start: start, end: l.pos})
	}
	for {
		l.skipBlanks()
		if l.pos >= l.end || l.st.dead {
			return toks, !stopParen && !l.st.dead
		}
		if !l.st.tick() {
			return toks, false
		}
		start := l.pos
		c := l.src[l.pos]
		rest := l.src[l.pos:l.end]
		switch c {
		case '#':
			// A comment runs to the end of the line; '#' inside a word never
			// reaches here because lexWord consumes it.
			for l.pos < l.end && l.src[l.pos] != '\n' {
				l.pos++
			}
		case '\n':
			l.pos++
			op("\n", start)
			if len(l.heredocs) > 0 {
				toks[len(toks)-1].nested = l.readHeredocs(toks)
			}
		case ';':
			switch {
			case strings.HasPrefix(rest, ";;&"):
				l.pos += 3
			case strings.HasPrefix(rest, ";;"), strings.HasPrefix(rest, ";&"):
				l.pos += 2
			default:
				l.pos++
			}
			op(l.src[start:l.pos], start)
		case '|':
			switch {
			case strings.HasPrefix(rest, "||"), strings.HasPrefix(rest, "|&"):
				l.pos += 2
			default:
				l.pos++
			}
			op(l.src[start:l.pos], start)
		case '&':
			switch {
			case strings.HasPrefix(rest, "&&"):
				l.pos += 2
				op("&&", start)
			case strings.HasPrefix(rest, "&>"):
				toks = l.lexRedirect(toks, "", start)
			default:
				l.pos++
				op("&", start)
			}
		case '(':
			l.pos++
			if stopParen {
				parens++
			}
			op("(", start)
		case ')':
			l.pos++
			if stopParen {
				if parens == 0 {
					return toks, true
				}
				parens--
			}
			op(")", start)
		case '<', '>':
			if strings.HasPrefix(rest[1:], "(") {
				toks = append(toks, l.procSubst())
			} else {
				toks = l.lexRedirect(toks, "", start)
			}
		default:
			if isDigit(c) {
				// A word made only of digits directly before < or > is a file
				// descriptor prefix ("2>file"), not an argument.
				j := l.pos
				for j < l.end && isDigit(l.src[j]) {
					j++
				}
				if j < l.end && (l.src[j] == '<' || l.src[j] == '>') &&
					!(j+1 < l.end && l.src[j+1] == '(') {
					fd := l.src[l.pos:j]
					l.pos = j
					toks = l.lexRedirect(toks, fd, start)
					continue
				}
			}
			toks = append(toks, l.lexWord(false)...)
		}
	}
}

// lexRedirect reads a redirection operator at l.pos (fd, if any, is already
// consumed) and appends it. Heredoc operators also read their delimiter word
// and queue the body for the next newline.
func (l *lexer) lexRedirect(toks []token, fd string, start int) []token {
	rest := l.src[l.pos:l.end]
	var op string
	for _, cand := range [...]string{"&>>", "&>", "<<<", "<<-", "<<", "<&", "<>", "<", ">>", ">&", ">|", ">"} {
		if strings.HasPrefix(rest, cand) {
			op = cand
			break
		}
	}
	if op == "" { // unreachable, but never loop
		l.pos++
		return toks
	}
	l.pos += len(op)
	toks = append(toks, token{kind: tkRedir, text: op, fd: fd, start: start, end: l.pos})
	if op != "<<" && op != "<<-" {
		return toks
	}
	l.st.an.HasHeredoc = true
	l.skipBlanks()
	if l.pos >= l.end || strings.IndexByte(" \t\n;&|()<>", l.src[l.pos]) >= 0 {
		l.st.problem("missing here-document delimiter")
		return toks
	}
	w := l.lexWord(true)
	if len(w) == 0 {
		return toks
	}
	toks = append(toks, w[0])
	l.heredocs = append(l.heredocs, pendingHeredoc{
		delim: w[0].text, strip: op == "<<-", quoted: w[0].quoted, tokIdx: len(toks) - 1,
	})
	return toks
}

// readHeredocs consumes the bodies of all queued heredocs, which start right
// after the newline just read. Unquoted heredoc bodies are subject to
// expansion, so commands inside $(...) there really run; they are returned.
func (l *lexer) readHeredocs(toks []token) []Simple {
	hs := l.heredocs
	l.heredocs = nil
	var nested []Simple
	for _, h := range hs {
		bodyStart := l.pos
		bodyEnd := -1
		for l.pos < l.end {
			lineStart := l.pos
			eol := strings.IndexByte(l.src[l.pos:l.end], '\n')
			var line string
			if eol < 0 {
				line = l.src[l.pos:l.end]
				l.pos = l.end
			} else {
				line = l.src[l.pos : l.pos+eol]
				l.pos += eol + 1
			}
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				bodyEnd = lineStart
				break
			}
		}
		if bodyEnd < 0 {
			l.st.problem("unterminated here-document")
			bodyEnd = l.end
		}
		if h.tokIdx < len(toks) {
			toks[h.tokIdx].body = l.src[bodyStart:bodyEnd]
			toks[h.tokIdx].hasBody = true
		}
		if !h.quoted && bodyEnd > bodyStart {
			l.scanExpansions(bodyStart, bodyEnd, &nested)
		}
	}
	return nested
}

// procSubst reads <(...) or >(...) at l.pos as one word token.
func (l *lexer) procSubst() token {
	start := l.pos
	l.st.an.HasProcessSubstitution = true
	l.pos += 2
	var nested []Simple
	if l.depth >= maxDepth {
		l.st.problem("nesting too deep")
		l.pos = l.end
	} else {
		sub := &lexer{src: l.src, pos: l.pos, end: l.end, st: l.st, depth: l.depth + 1}
		toks, closed := sub.lexUntil(true)
		l.pos = sub.pos
		if !closed {
			l.st.problem("unterminated process substitution")
		}
		nested = parseTokens(l.st, l.src, toks, l.depth+1, false, true)
	}
	return token{kind: tkWord, text: l.src[start:l.pos], start: start, end: l.pos, quoted: true, nested: nested}
}
