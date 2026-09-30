package fs

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file is the one glob engine behind glob, ls, grep's glob filter and
// .gitignore matching. It is deliberately small and allocation-free on the hot
// path: it runs once per directory entry per rule.

// segMatch reports whether name matches the single-segment pattern pat. Neither
// contains '/'. Supported: * ? [abc] [a-z] [!abc] [^abc] [[:alpha:]] and
// backslash escapes. An unterminated '[' is a literal.
func segMatch(pat, name string) bool {
	pi, ni := 0, 0
	starP, starN := -1, 0
	for ni < len(name) {
		advanced := false
		if pi < len(pat) {
			switch pat[pi] {
			case '*':
				starP, starN = pi, ni
				pi++
				continue
			case '?':
				_, w := utf8.DecodeRuneInString(name[ni:])
				pi++
				ni += w
				advanced = true
			case '[':
				r, w := utf8.DecodeRuneInString(name[ni:])
				ok, n := matchClass(pat[pi+1:], r)
				switch {
				case n >= 0 && ok:
					pi += 1 + n
					ni += w
					advanced = true
				case n < 0 && name[ni] == '[':
					pi++
					ni++
					advanced = true
				}
			case '\\':
				if pi+1 < len(pat) {
					_, pw := utf8.DecodeRuneInString(pat[pi+1:])
					if strings.HasPrefix(name[ni:], pat[pi+1:pi+1+pw]) {
						pi += 1 + pw
						ni += pw
						advanced = true
					}
				} else if name[ni] == '\\' {
					pi++
					ni++
					advanced = true
				}
			default:
				if pat[pi] == name[ni] {
					pi++
					ni++
					advanced = true
				}
			}
		}
		if advanced {
			continue
		}
		if starP >= 0 {
			_, w := utf8.DecodeRuneInString(name[starN:])
			starN += w
			pi, ni = starP+1, starN
			continue
		}
		return false
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}

// matchClass evaluates a bracket expression whose body starts at p[0] (just
// after the '['). It returns whether r matches and how many bytes of p the
// expression used including the closing ']', or -1 when it is unterminated.
func matchClass(p string, r rune) (bool, int) {
	i := 0
	neg := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		neg = true
		i++
	}
	matched := false
	first := true
	for i < len(p) {
		c := p[i]
		if c == ']' && !first {
			return matched != neg, i + 1
		}
		first = false
		if c == '[' && i+1 < len(p) && p[i+1] == ':' {
			if end := strings.Index(p[i+2:], ":]"); end >= 0 {
				if posixClass(p[i+2:i+2+end], r) {
					matched = true
				}
				i += 2 + end + 2
				continue
			}
		}
		lo, w := classRune(p[i:])
		i += w
		hi := lo
		if i+1 < len(p) && p[i] == '-' && p[i+1] != ']' {
			i++
			hi, w = classRune(p[i:])
			i += w
		}
		if lo <= r && r <= hi {
			matched = true
		}
	}
	return false, -1
}

func classRune(s string) (rune, int) {
	if s[0] == '\\' && len(s) > 1 {
		r, w := utf8.DecodeRuneInString(s[1:])
		return r, 1 + w
	}
	return utf8.DecodeRuneInString(s)
}

func posixClass(name string, r rune) bool {
	switch name {
	case "alpha":
		return unicode.IsLetter(r)
	case "digit":
		return r >= '0' && r <= '9'
	case "alnum":
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	case "upper":
		return unicode.IsUpper(r)
	case "lower":
		return unicode.IsLower(r)
	case "space":
		return unicode.IsSpace(r)
	case "blank":
		return r == ' ' || r == '\t'
	case "punct":
		return unicode.IsPunct(r) || unicode.IsSymbol(r)
	case "xdigit":
		return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
	case "word":
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
	case "cntrl":
		return unicode.IsControl(r)
	case "print":
		return unicode.IsPrint(r)
	case "graph":
		return unicode.IsGraphic(r) && !unicode.IsSpace(r)
	}
	return false
}

// matchSegs matches a path (as segments) against pattern segments, where a
// "**" segment stands for zero or more whole segments.
func matchSegs(pat, segs []string) bool {
	stars := 0
	for _, p := range pat {
		if p == "**" {
			stars++
		}
	}
	m := segMatcher{pat: pat, segs: segs}
	if stars >= 2 {
		// Several ** make naive backtracking exponential on adversarial
		// patterns; remembering failed states makes it O(len(pat)*len(segs)).
		m.failed = map[int]bool{}
	}
	return m.match(0, 0)
}

type segMatcher struct {
	pat, segs []string
	failed    map[int]bool
}

func (m *segMatcher) match(pi, si int) bool {
	for pi < len(m.pat) {
		if m.pat[pi] == "**" {
			for pi < len(m.pat) && m.pat[pi] == "**" {
				pi++
			}
			if pi == len(m.pat) {
				return true
			}
			key := pi*(len(m.segs)+1) + si
			if m.failed != nil && m.failed[key] {
				return false
			}
			for i := si; i <= len(m.segs); i++ {
				if m.match(pi, i) {
					return true
				}
			}
			if m.failed != nil {
				m.failed[key] = true
			}
			return false
		}
		if si >= len(m.segs) || !segMatch(m.pat[pi], m.segs[si]) {
			return false
		}
		pi++
		si++
	}
	return si == len(m.segs)
}

// prefixMatch reports whether segs could still be extended into a match of pat:
// every segment matches the corresponding pattern segment, or a "**" absorbs the
// rest. It lets the walker skip directories that cannot contain a match.
func prefixMatch(pat, segs []string) bool {
	if len(segs) == 0 {
		return true
	}
	if len(pat) == 0 {
		return false
	}
	if pat[0] == "**" {
		return true
	}
	if !segMatch(pat[0], segs[0]) {
		return false
	}
	return prefixMatch(pat[1:], segs[1:])
}

// expandBraces expands {a,b,c} alternatives (nested ones included) into the
// list of brace-free patterns. A brace group without a top-level comma, or
// without a closing brace, stays literal. ok is false when the expansion would
// exceed limit patterns.
func expandBraces(pat string, limit int) ([]string, bool) {
	for i := 0; i < len(pat); i++ {
		switch pat[i] {
		case '\\':
			i++
		case '[':
			if n := classLen(pat[i:]); n > 0 {
				i += n - 1
			}
		case '{':
			closeAt, commas := scanBrace(pat, i)
			if closeAt < 0 || len(commas) == 0 {
				continue
			}
			prefix, suffix := pat[:i], pat[closeAt+1:]
			body := pat[i+1 : closeAt]
			var alts []string
			last := 0
			for _, c := range commas {
				alts = append(alts, body[last:c-(i+1)])
				last = c - (i + 1) + 1
			}
			alts = append(alts, body[last:])
			var out []string
			for _, alt := range alts {
				sub, ok := expandBraces(prefix+alt+suffix, limit)
				if !ok {
					return nil, false
				}
				out = append(out, sub...)
				if len(out) > limit {
					return nil, false
				}
			}
			return out, true
		}
	}
	return []string{pat}, true
}

// scanBrace finds the '}' matching the '{' at pat[open] and the positions of
// the commas that sit directly inside it. closeAt is -1 if there is none.
func scanBrace(pat string, open int) (closeAt int, commas []int) {
	depth := 0
	for i := open; i < len(pat); i++ {
		switch pat[i] {
		case '\\':
			i++
		case '[':
			if n := classLen(pat[i:]); n > 0 {
				i += n - 1
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, commas
			}
		case ',':
			if depth == 1 {
				commas = append(commas, i)
			}
		}
	}
	return -1, nil
}

// classLen returns the length of the bracket expression at the start of s, or 0
// when s does not start with a well-formed one.
func classLen(s string) int {
	if len(s) < 2 || s[0] != '[' {
		return 0
	}
	if _, n := matchClass(s[1:], 0); n >= 0 {
		return 1 + n
	}
	return 0
}

// globPattern is a compiled pattern for the glob tool: standard glob semantics
// (a pattern with no slash matches only the top level) with ** and braces.
type globPattern struct {
	alts    [][]string
	dirOnly bool
}

const maxBraceAlternatives = 128

func compileGlob(pattern string) (*globPattern, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, errors.New("pattern is empty")
	}
	if strings.IndexByte(pattern, 0) >= 0 {
		return nil, errors.New("pattern contains a NUL byte")
	}
	g := &globPattern{}
	if strings.HasSuffix(pattern, "/") {
		g.dirOnly = true
		pattern = strings.TrimRight(pattern, "/")
	}
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.TrimLeft(pattern, "/")
	alts, ok := expandBraces(pattern, maxBraceAlternatives)
	if !ok {
		return nil, errors.New("pattern has too many {a,b} alternatives")
	}
	for _, alt := range alts {
		var segs []string
		for _, s := range strings.Split(alt, "/") {
			switch s {
			case "", ".":
				continue
			case "..":
				return nil, errors.New("pattern must not contain '..'; use the path argument to search elsewhere")
			}
			if s == "**" && len(segs) > 0 && segs[len(segs)-1] == "**" {
				continue
			}
			segs = append(segs, s)
		}
		if len(segs) == 0 {
			return nil, errors.New("pattern is empty")
		}
		g.alts = append(g.alts, segs)
	}
	return g, nil
}

func (g *globPattern) match(segs []string) bool {
	for _, a := range g.alts {
		if matchSegs(a, segs) {
			return true
		}
	}
	return false
}

func (g *globPattern) couldContain(dirSegs []string) bool {
	for _, a := range g.alts {
		if prefixMatch(a, dirSegs) {
			return true
		}
	}
	return false
}

// hasMeta reports whether s contains glob metacharacters.
func hasMeta(s string) bool { return strings.ContainsAny(s, "*?[{\\") }
