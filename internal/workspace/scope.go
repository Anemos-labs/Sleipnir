package workspace

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scope patterns are the same glob dialect as the harness's glob tool
// (internal/tools/fs): `*` and `?` inside one path segment, `[abc]`, `[a-z]`,
// `[!abc]`, `[[:digit:]]` (ASCII only here), `\` escapes, `{a,b}` alternatives,
// and `**` as a whole segment for any number of segments. A pattern without a
// slash names the top level only, so use `**/*.go` for "Go files anywhere".
//
// One rule is added because a scope is a set of *files a task may change*: a
// pattern also covers everything beneath any directory it matches. "internal/swarm",
// "internal/swarm/" and "internal/swarm/**" are the same scope, and "internal/*"
// covers internal/swarm/leases.go. This is the same conservative reading the
// swarm's spawn-time overlap check uses.
//
// Both operations below are exact for this dialect. Match decides whether one
// path is covered. Overlap decides whether any path at all could be covered by two
// patterns, by searching the product of the two patterns' automata; it never
// enumerates paths, so `**/*.go` versus `internal/**/x_?.go` is answered
// instantly and correctly.
//
// Whitespace around a pattern is ignored unless a backslash protects it. A pattern
// that cannot be parsed (empty, NUL, "..", absurdly long or deep) is treated
// fail-closed: it overlaps everything and covers nothing.

const (
	maxScopeBytes    = 4096
	maxScopeSegments = 64
	maxScopeAlts     = 128
)

type rng struct{ lo, hi rune }

// cset is a set of runes: the listed ranges, or everything except them.
type cset struct {
	neg bool
	rs  []rng
}

var anySet = cset{neg: true}

func (c cset) has(r rune) bool {
	in := false
	for _, x := range c.rs {
		if x.lo <= r && r <= x.hi {
			in = true
			break
		}
	}
	return in != c.neg
}

// intersects reports whether some rune is in both sets.
func (c cset) intersects(d cset) bool {
	switch {
	case !c.neg && !d.neg:
		for _, a := range c.rs {
			for _, b := range d.rs {
				if a.lo <= b.hi && b.lo <= a.hi {
					return true
				}
			}
		}
		return false
	case !c.neg && d.neg:
		return anyOutside(c.rs, d.rs)
	case c.neg && !d.neg:
		return anyOutside(d.rs, c.rs)
	}
	// Two complements of finite sets always share a rune.
	return true
}

// anyOutside reports whether some rune of the union of pos lies outside the union
// of excl.
func anyOutside(pos, excl []rng) bool {
	ex := append([]rng(nil), excl...)
	sort.Slice(ex, func(i, j int) bool { return ex[i].lo < ex[j].lo })
	for _, p := range pos {
		lo := p.lo
		for _, e := range ex {
			if e.hi < lo {
				continue
			}
			if e.lo > p.hi {
				break
			}
			if e.lo > lo {
				return true // a gap before this excluded range
			}
			if e.hi >= p.hi {
				lo = p.hi + 1
				break
			}
			lo = e.hi + 1
		}
		if lo <= p.hi {
			return true
		}
	}
	return false
}

type tokKind uint8

const (
	tokSet  tokKind = iota // one rune from a set (literal, ?, [class])
	tokStar                // any run of runes within the segment
)

type token struct {
	kind tokKind
	set  cset
}

type segment struct {
	dstar bool // the whole segment is "**"
	toks  []token
}

// scopePattern is a compiled pattern: alternatives (from {a,b}), each a list of
// segments, already closed over "everything beneath".
type scopePattern struct {
	alts [][]segment
}

var posixClasses = map[string][]rng{
	"alpha":  {{'A', 'Z'}, {'a', 'z'}},
	"digit":  {{'0', '9'}},
	"alnum":  {{'0', '9'}, {'A', 'Z'}, {'a', 'z'}},
	"upper":  {{'A', 'Z'}},
	"lower":  {{'a', 'z'}},
	"space":  {{'\t', '\r'}, {' ', ' '}},
	"blank":  {{'\t', '\t'}, {' ', ' '}},
	"punct":  {{'!', '/'}, {':', '@'}, {'[', '`'}, {'{', '~'}},
	"xdigit": {{'0', '9'}, {'A', 'F'}, {'a', 'f'}},
	"word":   {{'0', '9'}, {'A', 'Z'}, {'a', 'z'}, {'_', '_'}},
	"cntrl":  {{0, 31}, {127, 127}},
	"print":  {{' ', '~'}},
	"graph":  {{'!', '~'}},
}

// parseClass parses the bracket expression whose body starts at s[0] (just after
// '['). It returns the set and the number of bytes consumed including the closing
// ']', or -1 when unterminated.
func parseClass(s string) (cset, int) {
	i := 0
	var c cset
	if i < len(s) && (s[i] == '!' || s[i] == '^') {
		c.neg = true
		i++
	}
	first := true
	for i < len(s) {
		ch := s[i]
		if ch == ']' && !first {
			return c, i + 1
		}
		first = false
		if ch == '[' && i+1 < len(s) && s[i+1] == ':' {
			if end := strings.Index(s[i+2:], ":]"); end >= 0 {
				if rs, ok := posixClasses[s[i+2:i+2+end]]; ok {
					c.rs = append(c.rs, rs...)
				}
				i += 2 + end + 2
				continue
			}
		}
		lo, w := classRune(s[i:])
		i += w
		hi := lo
		if i+1 < len(s) && s[i] == '-' && s[i+1] != ']' {
			i++
			hi, w = classRune(s[i:])
			i += w
		}
		if lo > hi {
			lo, hi = hi, lo
		}
		c.rs = append(c.rs, rng{lo, hi})
	}
	return cset{}, -1
}

func classRune(s string) (rune, int) {
	if s[0] == '\\' && len(s) > 1 {
		r, w := utf8.DecodeRuneInString(s[1:])
		return r, 1 + w
	}
	return utf8.DecodeRuneInString(s)
}

// parseSegment compiles one path segment (no '/').
func parseSegment(s string) segment {
	if s == "**" {
		return segment{dstar: true}
	}
	var toks []token
	for i := 0; i < len(s); {
		switch s[i] {
		case '*':
			if len(toks) == 0 || toks[len(toks)-1].kind != tokStar {
				toks = append(toks, token{kind: tokStar})
			}
			i++
		case '?':
			toks = append(toks, token{kind: tokSet, set: anySet})
			i++
		case '[':
			if c, n := parseClass(s[i+1:]); n >= 0 {
				toks = append(toks, token{kind: tokSet, set: c})
				i += 1 + n
			} else {
				toks = append(toks, token{kind: tokSet, set: cset{rs: []rng{{'[', '['}}}})
				i++
			}
		case '\\':
			if i+1 < len(s) {
				r, w := utf8.DecodeRuneInString(s[i+1:])
				toks = append(toks, token{kind: tokSet, set: cset{rs: []rng{{r, r}}}})
				i += 1 + w
			} else {
				toks = append(toks, token{kind: tokSet, set: cset{rs: []rng{{'\\', '\\'}}}})
				i++
			}
		default:
			r, w := utf8.DecodeRuneInString(s[i:])
			toks = append(toks, token{kind: tokSet, set: cset{rs: []rng{{r, r}}}})
			i += w
		}
	}
	return segment{toks: toks}
}

// classLen returns the length of a well-formed bracket expression at the start of
// s, or 0.
func classLen(s string) int {
	if len(s) < 2 || s[0] != '[' {
		return 0
	}
	if _, n := parseClass(s[1:]); n >= 0 {
		return 1 + n
	}
	return 0
}

// expandBraces expands {a,b} alternatives (nested ones included). A group with no
// top-level comma, or no closing brace, stays literal. ok is false when the
// expansion would exceed limit patterns.
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

var errBadScope = errors.New("workspace: invalid scope pattern")

// trimPatternSpace removes the whitespace around a pattern, which is almost always
// an accident of how the pattern was written down (a config line, a model's list),
// except where a backslash protects it: a file called "a " is the pattern `a\ `.
func trimPatternSpace(p string) string {
	p = strings.TrimLeftFunc(p, unicode.IsSpace)
	t := strings.TrimRightFunc(p, unicode.IsSpace)
	if len(t) == len(p) {
		return p
	}
	backslashes := 0
	for i := len(t) - 1; i >= 0 && t[i] == '\\'; i-- {
		backslashes++
	}
	if backslashes%2 == 1 {
		_, w := utf8.DecodeRuneInString(p[len(t):])
		return p[:len(t)+w] // the first trailing space is escaped: it is part of the name
	}
	return t
}

// compileScope parses a scope pattern.
func compileScope(pattern string) (*scopePattern, error) {
	p := trimPatternSpace(pattern)
	switch {
	case p == "":
		return nil, errBadScope
	case strings.IndexByte(p, 0) >= 0, len(p) > maxScopeBytes:
		return nil, errBadScope
	}
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimLeft(p, "/")
	alts, ok := expandBraces(p, maxScopeAlts)
	if !ok {
		return nil, errBadScope
	}
	sp := &scopePattern{}
	for _, alt := range alts {
		var segs []segment
		for _, s := range strings.Split(alt, "/") {
			switch s {
			case "", ".":
				continue
			case "..":
				return nil, errBadScope
			}
			seg := parseSegment(s)
			if seg.dstar && len(segs) > 0 && segs[len(segs)-1].dstar {
				continue
			}
			segs = append(segs, seg)
		}
		if len(segs) > maxScopeSegments {
			return nil, errBadScope
		}
		// Everything beneath a matched directory is covered too: closing the
		// pattern with "**" says so in one place, for Match and Overlap alike. An
		// empty pattern body ("." or "/") is the whole repository.
		if len(segs) == 0 || !segs[len(segs)-1].dstar {
			segs = append(segs, segment{dstar: true})
		}
		sp.alts = append(sp.alts, segs)
	}
	return sp, nil
}

// ValidateScope reports the first pattern that cannot be used as a scope.
func ValidateScope(scope []string) error {
	for _, p := range scope {
		if _, err := compileScope(p); err != nil {
			return errors.New("workspace: invalid scope pattern " + quote(p))
		}
	}
	return nil
}

func quote(s string) string {
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return `"` + printable(s) + `"`
}

// matchTokens reports whether name matches one compiled segment.
func matchTokens(toks []token, name string) bool {
	rs := []rune(name)
	m := len(rs)
	dp := make([]bool, m+1)
	dp[0] = true
	for _, t := range toks {
		next := make([]bool, m+1)
		if t.kind == tokStar {
			any := false
			for j := 0; j <= m; j++ {
				any = any || dp[j]
				next[j] = any
			}
		} else {
			for j := 0; j < m; j++ {
				if dp[j] && t.set.has(rs[j]) {
					next[j+1] = true
				}
			}
		}
		dp = next
	}
	return dp[m]
}

// matchSegs matches path segments against compiled segments ("**" stands for zero
// or more whole segments), by dynamic programming over (pattern, path) positions.
func matchSegs(pat []segment, segs []string) bool {
	np, ns := len(pat), len(segs)
	memo := make([]uint8, (np+1)*(ns+1)) // 0 unknown, 1 no, 2 yes
	var rec func(pi, si int) bool
	rec = func(pi, si int) bool {
		if pi == np {
			return si == ns
		}
		k := pi*(ns+1) + si
		if memo[k] != 0 {
			return memo[k] == 2
		}
		var ok bool
		if pat[pi].dstar {
			ok = rec(pi+1, si) || (si < ns && rec(pi, si+1))
		} else {
			ok = si < ns && matchTokens(pat[pi].toks, segs[si]) && rec(pi+1, si+1)
		}
		if ok {
			memo[k] = 2
		} else {
			memo[k] = 1
		}
		return ok
	}
	return rec(0, 0)
}

// splitPath normalizes a repository-relative path to segments; ok is false for
// paths that are absolute, empty or climb out of the root.
func splitPath(p string) ([]string, bool) {
	p = filepath.ToSlash(p) // git paths use '/'; on Unix a backslash is a file name character
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") || strings.IndexByte(p, 0) >= 0 {
		return nil, false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return nil, false
	}
	return strings.Split(c, "/"), true
}

// Match reports whether scope pattern covers file (a repository-relative,
// slash-separated path). An unparsable pattern covers nothing.
func Match(pattern, file string) bool {
	sp, err := compileScope(pattern)
	if err != nil {
		return false
	}
	segs, ok := splitPath(file)
	if !ok {
		return false
	}
	return sp.match(segs)
}

func (sp *scopePattern) match(segs []string) bool {
	for _, a := range sp.alts {
		if matchSegs(a, segs) {
			return true
		}
	}
	return false
}

// Covered reports whether any pattern of scope covers file. An empty scope covers
// nothing; callers decide what "no declared scope" means (OutOfScope treats it as
// unrestricted).
func Covered(scope []string, file string) bool {
	segs, ok := splitPath(file)
	if !ok {
		return false
	}
	for _, p := range scope {
		sp, err := compileScope(p)
		if err != nil {
			continue
		}
		if sp.match(segs) {
			return true
		}
	}
	return false
}

// OutOfScope returns the files that no pattern of scope covers, sorted and
// without duplicates. An empty scope means "no scope declared": nothing is out of
// scope. Paths that are not clean relative paths (absolute, climbing out of the
// root) are always out of scope.
func OutOfScope(files, scope []string) []string {
	if len(scope) == 0 {
		return nil
	}
	compiled := make([]*scopePattern, 0, len(scope))
	for _, p := range scope {
		if sp, err := compileScope(p); err == nil {
			compiled = append(compiled, sp)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		segs, ok := splitPath(f)
		covered := false
		if ok {
			for _, sp := range compiled {
				if sp.match(segs) {
					covered = true
					break
				}
			}
		}
		if !covered {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// Assert reports the files t changed (relative to its base, committed or not,
// renames counted at both ends) that lie outside scope. It is what the harness
// runs when an agent says it is done, so that "stay inside your scope" is checked
// by code. With an empty scope nothing is reported.
func Assert(ctx context.Context, t *Tree, scope []string) ([]string, error) {
	if len(scope) == 0 {
		return nil, nil
	}
	changed, err := t.Changed(ctx)
	if err != nil {
		return nil, err
	}
	return OutOfScope(changed, scope), nil
}

// Overlap returns the entries of a that overlap at least one entry of b: that is,
// some path could be covered by both. The result keeps a's order and has no
// duplicates. An unparsable entry overlaps everything (a scope we cannot read
// must not be granted alongside another).
func Overlap(a, b []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, pa := range a {
		if seen[pa] {
			continue
		}
		for _, pb := range b {
			if patternsOverlap(pa, pb) {
				seen[pa] = true
				out = append(out, pa)
				break
			}
		}
	}
	return out
}

// OverlapPair names two overlapping scope entries.
type OverlapPair struct{ A, B string }

// OverlapPairs lists every (a, b) pair of entries that overlap, in a-major order.
func OverlapPairs(a, b []string) []OverlapPair {
	var out []OverlapPair
	for _, pa := range a {
		for _, pb := range b {
			if patternsOverlap(pa, pb) {
				out = append(out, OverlapPair{pa, pb})
			}
		}
	}
	return out
}

func patternsOverlap(a, b string) bool {
	ca, errA := compileScope(a)
	cb, errB := compileScope(b)
	if errA != nil || errB != nil {
		return true
	}
	for _, x := range ca.alts {
		for _, y := range cb.alts {
			if segsIntersect(x, y) {
				return true
			}
		}
	}
	return false
}

// segsIntersect reports whether some path matches both segment lists.
func segsIntersect(a, b []segment) bool {
	na, nb := len(a), len(b)
	seen := make([]bool, (na+1)*(nb+1))
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		k := i*(nb+1) + j
		if seen[k] {
			return false // already explored (and it did not succeed, or we would have returned)
		}
		seen[k] = true
		if i == na && j == nb {
			return true
		}
		if i < na && a[i].dstar {
			if rec(i+1, j) || (j < nb && rec(i, j+1)) {
				return true
			}
		}
		if j < nb && b[j].dstar {
			if rec(i, j+1) || (i < na && rec(i+1, j)) {
				return true
			}
		}
		if i < na && j < nb && !a[i].dstar && !b[j].dstar && tokensIntersect(a[i].toks, b[j].toks) {
			return rec(i+1, j+1)
		}
		return false
	}
	return rec(0, 0)
}

// tokensIntersect reports whether some segment name matches both token lists.
// It searches the product of the two automata: '*' may match nothing (an epsilon
// step) or loop over any rune, every other token consumes one rune that both
// must accept.
func tokensIntersect(a, b []token) bool {
	na, nb := len(a), len(b)
	seen := make([]bool, (na+1)*(nb+1))
	stack := []int{0}
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[k] {
			continue
		}
		seen[k] = true
		i, j := k/(nb+1), k%(nb+1)
		if i == na && j == nb {
			return true
		}
		if i < na && a[i].kind == tokStar {
			stack = append(stack, (i+1)*(nb+1)+j)
		}
		if j < nb && b[j].kind == tokStar {
			stack = append(stack, i*(nb+1)+j+1)
		}
		if i < na && j < nb {
			sa, sb := a[i].set, b[j].set
			if a[i].kind == tokStar {
				sa = anySet
			}
			if b[j].kind == tokStar {
				sb = anySet
			}
			if sa.intersects(sb) {
				ni, nj := i, j
				if a[i].kind != tokStar {
					ni++
				}
				if b[j].kind != tokStar {
					nj++
				}
				stack = append(stack, ni*(nb+1)+nj)
			}
		}
	}
	return false
}
