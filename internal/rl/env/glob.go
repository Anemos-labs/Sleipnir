package env

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

// Protected-path globs.
//
// Verifier.Protected lists what the agent must not change. The syntax follows
// .gitignore because that is what task authors already know:
//
//   - a pattern without a slash ("*_test.go", "go.mod") matches that name at
//     any depth;
//   - a pattern with a slash in the middle or at the start ("docs/*.md",
//     "/Makefile") is anchored at the repository root;
//   - "**" as a whole segment matches any number of directories, including none
//     ("a/**/b", ".github/**");
//   - a trailing slash ("vendor/") restricts the pattern to directories;
//   - "*" and "?" never match "/", "[a-z]" and "[!a-z]" are character classes,
//     and "\" escapes the next character;
//   - a pattern that matches a directory also matches everything below it.
//
// Matching is case-insensitive on purpose. The clean checkout may live on a
// case-insensitive filesystem (macOS, Windows), where an agent diff touching
// "FOO_TEST.GO" would overwrite the protected "foo_test.go"; folding case makes
// protection independent of the filesystem at the price of occasionally
// protecting a same-named file that differs only in case.
//
// Negation ("!pattern") is rejected rather than half-supported: a protection
// list that can be silently re-opened by a later line is a footgun.

// pathGlob is one compiled pattern.
type pathGlob struct {
	raw     string
	segs    [][]rune // lower-cased; a single "**" segment is the two runes "**"
	dirOnly bool
}

// Matcher tests repository-relative paths against a set of protected globs.
type Matcher struct {
	globs []pathGlob
}

// CompileGlobs compiles patterns, reporting the first invalid one.
func CompileGlobs(patterns []string) (*Matcher, error) {
	m := &Matcher{}
	for _, p := range patterns {
		g, err := compileGlob(p)
		if err != nil {
			return nil, err
		}
		m.globs = append(m.globs, g)
	}
	return m, nil
}

// ValidateGlob reports why a pattern is not usable, or nil.
func ValidateGlob(p string) error {
	_, err := compileGlob(p)
	return err
}

// Empty reports whether the matcher can never match.
func (m *Matcher) Empty() bool { return m == nil || len(m.globs) == 0 }

// Match reports whether the slash-separated relative path is covered by any
// pattern. Paths are compared after path.Clean and case folding.
func (m *Matcher) Match(p string) bool {
	_, ok := m.MatchWhich(p)
	return ok
}

// MatchWhich is Match that also returns the first pattern that matched.
func (m *Matcher) MatchWhich(p string) (string, bool) {
	if m == nil || len(m.globs) == 0 {
		return "", false
	}
	segs := splitFold(p)
	if len(segs) == 0 {
		return "", false
	}
	for _, g := range m.globs {
		for i := 1; i <= len(segs); i++ {
			if g.dirOnly && i == len(segs) {
				break // a dir-only pattern never matches the file itself
			}
			if matchSegs(g.segs, segs[:i]) {
				return g.raw, true
			}
		}
	}
	return "", false
}

func splitFold(p string) [][]rune {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return nil
	}
	parts := strings.Split(p, "/")
	out := make([][]rune, len(parts))
	for i, s := range parts {
		out[i] = foldRunes(s)
	}
	return out
}

func foldRunes(s string) []rune {
	r := []rune(s)
	for i, c := range r {
		r[i] = unicode.ToLower(c)
	}
	return r
}

func compileGlob(p string) (pathGlob, error) {
	fail := func(msg string) (pathGlob, error) {
		return pathGlob{}, fmt.Errorf("invalid glob %q: %s", p, msg)
	}
	if strings.TrimSpace(p) == "" {
		return fail("empty pattern")
	}
	if strings.ContainsRune(p, 0) {
		return fail("contains a NUL byte")
	}
	if strings.HasPrefix(p, "!") {
		return fail("negation is not supported")
	}
	g := pathGlob{raw: p}
	s := p
	if strings.HasSuffix(s, "/") {
		g.dirOnly = true
		s = strings.TrimRight(s, "/")
	}
	anchored := strings.HasPrefix(s, "/")
	s = strings.TrimLeft(s, "/")
	if s == "" {
		return fail("matches nothing")
	}
	var raw []string
	for _, seg := range strings.Split(s, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			return fail("contains a '..' segment")
		}
		raw = append(raw, seg)
	}
	if len(raw) == 0 {
		return fail("matches nothing")
	}
	if len(raw) > 1 {
		anchored = true
	}
	if !anchored {
		raw = append([]string{"**"}, raw...)
	}
	for _, seg := range raw {
		if err := validateSegment(seg); err != nil {
			return fail(err.Error())
		}
		g.segs = append(g.segs, foldRunes(seg))
	}
	return g, nil
}

// validateSegment checks bracket and escape syntax so that a typo fails at
// task-load time instead of silently protecting nothing.
func validateSegment(seg string) error {
	r := []rune(seg)
	for i := 0; i < len(r); i++ {
		switch r[i] {
		case '\\':
			if i+1 >= len(r) {
				return fmt.Errorf("trailing backslash")
			}
			i++
		case '[':
			_, n, ok := parseClass(r[i:])
			if !ok {
				return fmt.Errorf("unterminated '['")
			}
			i += n - 1
		}
	}
	return nil
}

// matchSegs matches pattern segments against path segments; "**" spans zero or
// more path segments. Dynamic programming keeps hostile patterns linear.
func matchSegs(pat, segs [][]rune) bool {
	// dp[i][j]: pat[i:] matches segs[j:]
	n, m := len(pat), len(segs)
	dp := make([][]bool, n+1)
	for i := range dp {
		dp[i] = make([]bool, m+1)
	}
	dp[n][m] = true
	for i := n - 1; i >= 0; i-- {
		isStar := len(pat[i]) == 2 && pat[i][0] == '*' && pat[i][1] == '*'
		for j := m; j >= 0; j-- {
			if isStar {
				dp[i][j] = dp[i+1][j] || (j < m && dp[i][j+1])
				continue
			}
			dp[i][j] = j < m && matchSeg(pat[i], segs[j]) && dp[i+1][j+1]
		}
	}
	return dp[0][0]
}

// matchSeg matches one path segment against one pattern segment.
func matchSeg(p, s []rune) bool {
	px, sx := 0, 0
	starP, starS := -1, 0
	for sx < len(s) {
		if px < len(p) {
			switch p[px] {
			case '*':
				starP, starS = px, sx
				px++
				continue
			case '?':
				px++
				sx++
				continue
			case '[':
				if ok, n, valid := parseClass(p[px:]); valid {
					if ok(s[sx]) {
						px += n
						sx++
						continue
					}
				} else if s[sx] == '[' {
					px++
					sx++
					continue
				}
			case '\\':
				if px+1 < len(p) && p[px+1] == s[sx] {
					px += 2
					sx++
					continue
				}
			default:
				if p[px] == s[sx] {
					px++
					sx++
					continue
				}
			}
		}
		if starP >= 0 {
			starS++
			sx = starS
			px = starP + 1
			continue
		}
		return false
	}
	for px < len(p) && p[px] == '*' {
		px++
	}
	return px == len(p)
}

// parseClass parses "[...]" at the start of p. It returns a predicate, the
// number of runes consumed and whether the class is well formed.
func parseClass(p []rune) (func(rune) bool, int, bool) {
	i := 1
	negate := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		negate = true
		i++
	}
	type span struct{ lo, hi rune }
	var spans []span
	first := true
	for ; i < len(p); i++ {
		c := p[i]
		if c == ']' && !first {
			pred := func(r rune) bool {
				in := false
				for _, sp := range spans {
					if r >= sp.lo && r <= sp.hi {
						in = true
						break
					}
				}
				return in != negate
			}
			return pred, i + 1, true
		}
		first = false
		if c == '\\' && i+1 < len(p) {
			i++
			c = p[i]
		}
		lo := c
		hi := c
		if i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']' {
			hi = p[i+2]
			i += 2
			if hi == '\\' && i+1 < len(p) {
				i++
				hi = p[i]
			}
		}
		if hi < lo {
			lo, hi = hi, lo
		}
		spans = append(spans, span{lo, hi})
	}
	return nil, 0, false
}
