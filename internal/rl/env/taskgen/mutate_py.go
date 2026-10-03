package taskgen

import (
	"sort"
	"strings"
)

// A small Python lexer, just precise enough to find operators and keywords
// outside strings and comments. It is not a parser: the mutations it enables
// are single-token or single-insertion edits that keep valid code valid, and
// whatever still breaks is caught when the project's tests are run.

type pyKind int

const (
	pyName pyKind = iota
	pyNumber
	pyOp
	pyNewline
)

type pyTok struct {
	kind       pyKind
	text       string
	start, end int
	line       int
	first      bool // first token of its logical line
}

var pyOps3 = []string{"**=", "//=", ">>=", "<<=", "..."}
var pyOps2 = []string{"==", "!=", "<=", ">=", "->", ":=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "**", "//", "<<", ">>", "@="}

// isPyIdentStart accepts ASCII letters, underscore, and non-ASCII bytes for the mutation scanner;
// it does not validate Unicode identifiers.
func isPyIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// isPyIdentChar accepts an identifier-start byte or an ASCII digit in the Python mutation scanner.
func isPyIdentChar(c byte) bool { return isPyIdentStart(c) || c >= '0' && c <= '9' }

// lexPython tokenizes src, skipping comments and string literals (triple-quoted,
// prefixed and raw ones included).
func lexPython(src []byte) []pyTok {
	var toks []pyTok
	line := 1
	newLogicalLine := true
	depth := 0 // bracket depth: newlines inside brackets do not end a logical line
	i := 0
	emit := func(k pyKind, s, e int) {
		toks = append(toks, pyTok{kind: k, text: string(src[s:e]), start: s, end: e, line: line, first: newLogicalLine})
		newLogicalLine = false
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			if depth == 0 && !newLogicalLine {
				toks = append(toks, pyTok{kind: pyNewline, text: "\n", start: i, end: i + 1, line: line})
				newLogicalLine = true
			}
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
		case c == '\\' && i+1 < len(src) && (src[i+1] == '\n' || src[i+1] == '\r'):
			i++ // line continuation
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'':
			i = skipPyString(src, i, &line)
			newLogicalLine = false
		case isPyIdentStart(c):
			s := i
			for i < len(src) && isPyIdentChar(src[i]) {
				i++
			}
			// a string prefix (r"..", b'..', f"..", rb"..") directly followed by a quote
			if i < len(src) && (src[i] == '"' || src[i] == '\'') && len(src[s:i]) <= 2 && strings.Trim(strings.ToLower(string(src[s:i])), "rbfu") == "" {
				i = skipPyString(src, i, &line)
				newLogicalLine = false
				continue
			}
			emit(pyName, s, i)
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			s := i
			for i < len(src) && (isPyIdentChar(src[i]) || src[i] == '.' || (src[i] == '+' || src[i] == '-') && (src[i-1] == 'e' || src[i-1] == 'E') && !strings.HasPrefix(strings.ToLower(string(src[s:i])), "0x")) {
				i++
			}
			emit(pyNumber, s, i)
		default:
			s := i
			op := string(c)
			for _, o := range pyOps3 {
				if strings.HasPrefix(string(src[i:min(i+3, len(src))]), o) {
					op = o
					break
				}
			}
			if len(op) == 1 {
				for _, o := range pyOps2 {
					if strings.HasPrefix(string(src[i:min(i+2, len(src))]), o) {
						op = o
						break
					}
				}
			}
			i += len(op)
			switch op {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				if depth > 0 {
					depth--
				}
			}
			emit(pyOp, s, i)
		}
	}
	return toks
}

// skipPyString returns the index just after the string literal that starts at
// the quote at i.
func skipPyString(src []byte, i int, line *int) int {
	q := src[i]
	triple := i+2 < len(src) && src[i+1] == q && src[i+2] == q
	if triple {
		i += 3
	} else {
		i++
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			if src[i+1] == '\n' {
				*line++
			}
			i += 2
		case c == '\n':
			*line++
			if !triple {
				return i // unterminated: stop at the line end
			}
			i++
		case c == q:
			if !triple {
				return i + 1
			}
			if i+2 < len(src) && src[i+1] == q && src[i+2] == q {
				return i + 3
			}
			i++
		default:
			i++
		}
	}
	return i
}

var pyCmpFlip = map[string]string{"==": "!=", "!=": "==", "<": "<=", "<=": "<", ">": ">=", ">=": ">"}

// PythonMutations lists the mutations available in one Python file:
// comparison operators flipped, ±1 swapped, and/or exchanged, "is None" turned
// into "is not None" and back, "not" dropped, statement conditions negated and
// boolean returns inverted. Sorted by position.
func PythonMutations(file string, src []byte) []Mutation {
	toks := lexPython(src)
	var out []Mutation
	add := func(op string, t pyTok, start, end int, repl string) {
		out = append(out, Mutation{File: file, Op: op, Line: t.line, Start: start, End: end, Old: string(src[start:end]), New: repl})
	}
	prev := func(i int) *pyTok {
		if i == 0 {
			return nil
		}
		return &toks[i-1]
	}
	for i, t := range toks {
		switch t.kind {
		case pyOp:
			if repl, ok := pyCmpFlip[t.text]; ok {
				add("cmp-flip", t, t.start, t.end, repl)
			}
			if (t.text == "+" || t.text == "-") && i+1 < len(toks) && toks[i+1].kind == pyNumber && toks[i+1].text == "1" {
				if p := prev(i); p != nil && (p.kind == pyName && !isPyKeyword(p.text) || p.kind == pyNumber || p.text == ")" || p.text == "]") {
					repl := "-"
					if t.text == "-" {
						repl = "+"
					}
					add("boundary", t, t.start, t.end, repl)
				}
			}
		case pyName:
			switch t.text {
			case "and":
				add("logic-swap", t, t.start, t.end, "or")
			case "or":
				add("logic-swap", t, t.start, t.end, "and")
			case "is":
				if i+1 < len(toks) && toks[i+1].text == "not" && i+2 < len(toks) && toks[i+2].text == "None" {
					add("none-check", t, t.start, toks[i+1].end, "is")
				} else if i+1 < len(toks) && toks[i+1].text == "None" {
					add("none-check", t, t.start, t.end, "is not")
				}
			case "not":
				p := prev(i)
				if p != nil && p.text == "is" {
					break // handled with "is not None"
				}
				if i+1 < len(toks) && toks[i+1].text == "in" {
					break
				}
				end := t.end
				for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
					end++
				}
				add("negate", t, t.start, end, "")
			case "return":
				if i+1 < len(toks) && (toks[i+1].text == "True" || toks[i+1].text == "False") &&
					(i+2 >= len(toks) || toks[i+2].kind == pyNewline || toks[i+2].text == ";") {
					repl := "True"
					if toks[i+1].text == "True" {
						repl = "False"
					}
					add("return-bool", toks[i+1], toks[i+1].start, toks[i+1].end, repl)
				}
			case "if", "elif", "while":
				// Only statements (`if cond:` at the start of a logical line), not
				// conditional expressions or comprehension filters.
				if t.first && i+1 < len(toks) && toks[i+1].text != "not" {
					add("negate", t, t.end, t.end, " not")
				}
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Start != out[b].Start {
			return out[a].Start < out[b].Start
		}
		return out[a].Op < out[b].Op
	})
	return out
}

// isPyKeyword recognizes the expression-boundary words used by the mutation scanner, rather than
// the complete Python keyword set.
func isPyKeyword(s string) bool {
	switch s {
	case "return", "and", "or", "not", "in", "is", "if", "elif", "else", "while", "for", "yield", "await", "assert", "lambda", "print":
		return true
	}
	return false
}
