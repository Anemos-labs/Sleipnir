package reward

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// A tiny comment and string lexer for source fragments taken from diffs. The
// detectors match code patterns ("t.Skip(", "it.skip("), and both directions of
// error are exploitable by a policy that has learned what the detectors look
// for:
//
//   - text inside a comment or a string must not fire a detector (a log line
//     mentioning "t.Skip()" is not a skipped test), and
//   - a string that merely contains "//" must not hide the code after it
//     (`x := "//"; t.Skip()`), which is what naive comment stripping does.
//
// So the lexer tracks string state while it removes comments, and can either
// keep string contents (for names) or blank them (for patterns). It is a single
// linear pass, works on fragments (a hunk may start inside a block comment; the
// worst case is a misjudged span, never a panic or superlinear time) and
// preserves newlines so line-based checks still line up.

type lexSyntax struct {
	lineComment string
	block       bool // /* */
	backtick    bool // raw / template strings that may span lines
	triple      bool // Python-style triple quotes
	singleQuote bool // 'x' delimits a string or char
	rustQuote   bool // ' may be a lifetime; only 'x' and '\x..' are literals
}

// syntaxFor selects comment and string delimiters for the heuristic lexer, defaulting to
// JavaScript-like syntax.
func syntaxFor(lang string) lexSyntax {
	switch lang {
	case "py":
		return lexSyntax{lineComment: "#", triple: true, singleQuote: true}
	case "rb":
		return lexSyntax{lineComment: "#", backtick: true, singleQuote: true}
	case "sh":
		return lexSyntax{lineComment: "#", singleQuote: true, backtick: true}
	case "rs":
		return lexSyntax{lineComment: "//", block: true, rustQuote: true}
	case "go":
		return lexSyntax{lineComment: "//", block: true, backtick: true, singleQuote: true}
	case "php":
		return lexSyntax{lineComment: "//", block: true, singleQuote: true}
	default: // js, java, cs, kotlin, ...
		return lexSyntax{lineComment: "//", block: true, backtick: true, singleQuote: true}
	}
}

// lexCode returns src without comments. With keepStrings false the contents of
// string literals are dropped (the quotes stay), so patterns cannot match inside
// them.
func lexCode(src, lang string, keepStrings bool) string {
	sx := syntaxFor(lang)
	var out strings.Builder
	out.Grow(len(src))
	n := len(src)
	i := 0
	emit := func(s string) { out.WriteString(s) }
	for i < n {
		c := src[i]
		switch {
		case sx.lineComment != "" && strings.HasPrefix(src[i:], sx.lineComment):
			for i < n && src[i] != '\n' {
				i++
			}
		case sx.block && c == '/' && i+1 < n && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				// Unterminated: the rest of the fragment is inside the comment.
				for ; i < n; i++ {
					if src[i] == '\n' {
						out.WriteByte('\n')
					}
				}
				break
			}
			seg := src[i : i+2+end+2]
			out.WriteString(strings.Repeat("\n", strings.Count(seg, "\n")))
			i += 2 + end + 2
		case sx.triple && (strings.HasPrefix(src[i:], `"""`) || strings.HasPrefix(src[i:], `'''`)):
			q := src[i : i+3]
			end := strings.Index(src[i+3:], q)
			var body string
			if end < 0 {
				body = src[i+3:]
				i = n
			} else {
				body = src[i+3 : i+3+end]
				i += 3 + end + 3
			}
			if keepStrings {
				emit(q + body + q)
			} else {
				emit(q + strings.Repeat("\n", strings.Count(body, "\n")) + q)
			}
		case c == '"' || (c == '\'' && sx.singleQuote):
			j := i + 1
			for j < n && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' && j+1 < n && src[j+1] != '\n' {
					j++
				}
				j++
			}
			body := src[i+1 : min(j, n)]
			if keepStrings {
				emit(string(c) + body)
			} else {
				emit(string(c))
			}
			if j < n && src[j] == c {
				emit(string(c))
				j++
			}
			i = j
		case c == '\'' && sx.rustQuote:
			// 'x' or '\n' is a char literal; anything else is a lifetime.
			switch {
			case i+2 < n && src[i+1] != '\\' && src[i+2] == '\'':
				emit("'")
				if keepStrings {
					emit(src[i+1 : i+2])
				}
				emit("'")
				i += 3
			case i+1 < n && src[i+1] == '\\':
				j := strings.IndexByte(src[i+2:], '\'')
				if j < 0 || j > 10 {
					emit("'")
					i++
				} else {
					emit("''")
					i += j + 3
				}
			default:
				emit("'")
				i++
			}
		case c == '`' && sx.backtick:
			end := strings.IndexByte(src[i+1:], '`')
			var body string
			if end < 0 {
				body = src[i+1:]
				i = n
			} else {
				body = src[i+1 : i+1+end]
				i += 1 + end + 1
			}
			if keepStrings {
				emit("`" + body + "`")
			} else {
				emit("`" + strings.Repeat("\n", strings.Count(body, "\n")) + "`")
			}
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// foldLine canonicalises a code line for matching: invisible format characters
// (zero-width joiners, bidi controls) are dropped, fullwidth ASCII is mapped to
// ASCII and whitespace runs become one space.
func foldLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			if c == ' ' || (c >= '\t' && c <= '\r') {
				space = true
				continue
			}
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteByte(c)
			continue
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		switch {
		case unicode.Is(unicode.Cf, r):
			continue
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// tight removes whitespace around member access, calls and decorators, so that
// "t . Skip ( )" and "t.Skip()" are one token stream. Line breaks count as
// whitespace, which also defeats a call split across lines ("t.\nSkip()").
// The characters involved are ASCII, so it works on bytes.
func tight(s string) string {
	s = foldLine(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' {
			var prev, next byte
			if i > 0 {
				prev = s[i-1]
			}
			if i+1 < len(s) {
				next = s[i+1]
			}
			if prev == '.' || prev == '(' || prev == '@' || prev == '[' || next == '.' || next == '(' || next == ')' || next == '[' {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// splitCall parses the argument list of a call whose opening parenthesis is at
// s[open]. It returns the top-level, comma-separated arguments (trimmed) and the
// index after the closing parenthesis.
func splitCall(s string, open int) (args []string, end int, ok bool) {
	if open >= len(s) || s[open] != '(' {
		return nil, 0, false
	}
	depth := 0
	start := open + 1
	var quote byte
	for i := open; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				args = append(args, strings.TrimSpace(s[start:i]))
				return args, i + 1, true
			}
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return nil, 0, false
}
