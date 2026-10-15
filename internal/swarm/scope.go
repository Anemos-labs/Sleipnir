package swarm

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// A scope is the list of paths a task may touch: directories ("src/api", with or
// without a trailing slash: the directory and everything under it), files
// ("notes.txt") and globs ("api/**", "web/*.tsx", "src/{a,b}/**"). Patterns are
// relative to the repository root; "**" spans directories, "*" does not.
//
// Three things use them. Spawn and claim refuse tasks whose scopes overlap another
// active writer's (scopesOverlap: conservative, it may report an overlap that is
// not one but never misses one), the lease guard refuses a write outside the
// writer's own scope (scopeMatch: exact), and a pool child's scope must lie inside
// its parent's (scopeContains: conservative the other way, it may refuse a child
// that fits but never admits one that does not).

const (
	maxScopeEntries = 16
	maxScopeLen     = 200
	maxScopeSegs    = 32
	maxBraceAlts    = 16
)

// normScope validates and canonicalises one scope entry: forward slashes, no "."
// or ".." segments, no duplicate slashes, no NUL. "." and "/" mean the whole repo
// ("**"). Absolute paths are kept (the swarm turns them into repo-relative ones
// where it knows the root).
func normScope(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty scope entry")
	}
	if len(p) > maxScopeLen {
		return "", fmt.Errorf("scope entry longer than %d characters", maxScopeLen)
	}
	for _, r := range p {
		if unicode.IsControl(r) || r == '<' || r == '>' || r == 0x2028 || r == 0x2029 || (r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || r == 0xFEFF {
			return "", errors.New("scope entry contains a control character or an angle bracket")
		}
	}
	p = strings.ReplaceAll(p, `\`, "/")
	abs := strings.HasPrefix(p, "/")
	p = path.Clean(p)
	if p == "." || p == "/" {
		return "**", nil
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("scope %q leaves the repository", p)
	}
	if !abs {
		p = strings.TrimPrefix(p, "./")
	}
	if len(strings.Split(p, "/")) > maxScopeSegs {
		return "", fmt.Errorf("scope %q is nested too deeply", p)
	}
	if _, err := expandBraces(p); err != nil {
		return "", err
	}
	return p, nil
}

// cleanScopes normalises a list of scope entries (drops duplicates, caps the count).
func cleanScopes(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxScopeEntries {
		return nil, fmt.Errorf("a task scope can list at most %d paths (got %d): use a directory or a glob", maxScopeEntries, len(in))
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		n, err := normScope(p)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// expandBraces expands one level of {a,b} alternatives ("src/{a,b}/**" gives
// "src/a/**", "src/b/**"). Nested braces are not supported and are an error.
func expandBraces(p string) ([]string, error) {
	out := []string{p}
	for iter := 0; iter < maxBraceAlts; iter++ {
		var next []string
		changed := false
		for _, s := range out {
			i := strings.IndexByte(s, '{')
			if i < 0 {
				next = append(next, s)
				continue
			}
			j := strings.IndexByte(s[i:], '}')
			if j < 0 {
				return nil, fmt.Errorf("scope %q has an unclosed {", p)
			}
			j += i
			body := s[i+1 : j]
			if strings.ContainsAny(body, "{") {
				return nil, fmt.Errorf("scope %q nests braces, which is not supported", p)
			}
			changed = true
			for _, alt := range strings.Split(body, ",") {
				next = append(next, s[:i]+alt+s[j+1:])
			}
			if len(next) > maxBraceAlts {
				return nil, fmt.Errorf("scope %q expands to more than %d alternatives", p, maxBraceAlts)
			}
		}
		out = next
		if !changed {
			return out, nil
		}
	}
	return nil, fmt.Errorf("scope %q has too many alternatives", p)
}

// hasMeta detects glob metacharacters in a path segment without validating their syntax.
func hasMeta(seg string) bool { return strings.ContainsAny(seg, "*?[") }

// segments splits a normalised pattern. A pattern whose last segment has no glob
// characters names a file or a directory: it also covers everything below it, so
// a "**" is appended.
func segments(p string) (segs []string, abs bool) {
	abs = strings.HasPrefix(p, "/")
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{"**"}, abs
	}
	segs = strings.Split(p, "/")
	if last := segs[len(segs)-1]; last != "**" && !hasMeta(last) {
		segs = append(segs, "**")
	}
	return segs, abs
}

// matchSegs matches path segments against pattern segments ("**" spans zero or
// more segments; other segments use path.Match, so "*" and "?" stay within one).
func matchSegs(p, s []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for len(p) > 0 && p[0] == "**" {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if matchSegs(p, s[i:]) {
					return true
				}
			}
			return false
		}
		if len(s) == 0 {
			return false
		}
		ok, err := path.Match(p[0], s[0])
		if err != nil || !ok {
			return false
		}
		p, s = p[1:], s[1:]
	}
	return len(s) == 0
}

// scopeMatch reports whether the repo-relative slash path rel lies inside scope.
func scopeMatch(scope, rel string) bool {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return false
	}
	rs := strings.Split(rel, "/")
	if len(rs) > maxScopeSegs*2 {
		return false
	}
	alts, err := expandBraces(scope)
	if err != nil {
		return false
	}
	for _, a := range alts {
		ps, abs := segments(a)
		if abs {
			continue // absolute scopes are converted to relative ones before they are stored
		}
		if matchSegs(ps, rs) {
			return true
		}
	}
	return false
}

// inScope reports whether rel lies inside any of the scopes.
func inScope(scopes []string, rel string) bool {
	for _, s := range scopes {
		if scopeMatch(s, rel) {
			return true
		}
	}
	return false
}

// scopesOverlap reports whether two scope entries can match a common path. It is
// conservative: spelling variants of one directory ("src//a", "./src/a",
// "src/../src/a", "Src/a", "src\a") are the same, an absolute pattern is compared
// with a relative one at every alignment, and a glob segment against a glob segment
// counts as an overlap; a directory is not confused with a longer name ("src/a"
// versus "src/ab"). "**" is aligned as matching aligns it, so "**/*.md" and
// "web/**" overlap (web/README.md) while "*.md" and "web/**" do not. A path
// without a glob also covers what lies below it ("web/package.json" covers
// "web/package.json/x.md"); scopesOverlapIn can rule that out for files.
func scopesOverlap(a, b string) bool { return scopesOverlapIn(a, b, nil) }

// scopesOverlapIn is scopesOverlap knowing which entries name files: file(p), for
// a cleaned relative entry p without a glob, reports that p names a regular file,
// which has nothing below it, so the entry covers that file alone ("**/*.md" and
// the file "web/package.json" are then disjoint). A nil file, or a false answer
// (a directory, a path that does not exist yet), keeps the directory reading.
func scopesOverlapIn(a, b string, file func(string) bool) bool {
	ax, aerr := altsOf(a, file)
	bx, berr := altsOf(b, file)
	if aerr != nil || berr != nil {
		return true // cannot analyse: assume they collide
	}
	for _, x := range ax {
		for _, y := range bx {
			if altOverlap(x, y) {
				return true
			}
		}
	}
	return false
}

// scopeAlt is one brace alternative of a scope entry, lower-cased for comparison:
// file says it names a regular file (see scopesOverlapIn).
type scopeAlt struct {
	pat  string
	file bool
}

// altsOf cleans a scope entry for comparison and expands its braces. Whether an
// alternative names a file is asked with its own spelling, before lower-casing.
func altsOf(p string, file func(string) bool) ([]scopeAlt, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if p == "" {
		return nil, errors.New("empty scope")
	}
	abs := strings.HasPrefix(p, "/")
	c := path.Clean(p)
	if c == "." {
		c = "**"
	} else if !abs {
		c = strings.TrimPrefix(c, "./")
	}
	alts, err := expandBraces(c)
	if err != nil {
		return nil, err
	}
	out := make([]scopeAlt, len(alts))
	for i, a := range alts {
		out[i] = scopeAlt{pat: strings.ToLower(a), file: file != nil && !abs && !hasMeta(a) && file(a)}
	}
	return out, nil
}

// altOverlap compares two brace alternatives: relative ones segment by segment,
// an absolute one against a relative one at every alignment (absVsRel).
func altOverlap(a, b scopeAlt) bool {
	sa, aAbs := segments(a.pat)
	sb, bAbs := segments(b.pat)
	if a.file {
		sa = strings.Split(a.pat, "/") // a file: no "**" below it
	}
	if b.file {
		sb = strings.Split(b.pat, "/")
	}
	switch {
	case aAbs == bAbs:
		return segsOverlap(sa, sb)
	case aAbs:
		return absVsRel(sa, sb)
	default:
		return absVsRel(sb, sa)
	}
}

// absVsRel compares an absolute pattern with a relative one: the relative pattern
// is anchored at some directory of the absolute one.
func absVsRel(abs, rel []string) bool {
	for i := 0; i <= len(abs); i++ {
		if segsOverlap(abs[i:], rel) {
			return true
		}
	}
	return false
}

// segsOverlap reports whether some path matches both segment patterns. "**" spans
// zero or more segments on either side, as in matchSegs: at each step one side's
// "**" either ends or takes the segment the other side matches next, and two other
// segments must be compatible (segCompat). Every pair of positions is explored at
// most once, so many "**" cannot make it slow.
func segsOverlap(a, b []string) bool {
	tried := make(map[[2]int]bool) // positions already explored; they led nowhere
	var from func(i, j int) bool
	from = func(i, j int) bool {
		if i == len(a) && j == len(b) {
			return true
		}
		if tried[[2]int{i, j}] {
			return false
		}
		tried[[2]int{i, j}] = true
		switch {
		case i < len(a) && a[i] == "**":
			return from(i+1, j) || j < len(b) && from(i, j+1)
		case j < len(b) && b[j] == "**":
			return from(i, j+1) || i < len(a) && from(i+1, j)
		case i < len(a) && j < len(b):
			return segCompat(a[i], b[j]) && from(i+1, j+1)
		}
		return false
	}
	return from(0, 0)
}

// segCompat reports whether one path segment could match both segment patterns: literal against literal must be equal, a glob
// must match a literal, two globs of the form "*"+suffix ("*.md", "*_test.go") need one suffix to end the other, and any other
// glob against a glob counts as compatible (deciding it exactly is not worth it here).
func segCompat(x, y string) bool {
	hx, hy := hasMeta(x), hasMeta(y)
	switch {
	case !hx && !hy:
		return x == y
	case hx && !hy:
		ok, err := path.Match(x, y)
		return err != nil || ok
	case !hx && hy:
		ok, err := path.Match(y, x)
		return err != nil || ok
	}
	sx, okx := starSuffix(x)
	sy, oky := starSuffix(y)
	if okx && oky {
		return strings.HasSuffix(sx, sy) || strings.HasSuffix(sy, sx)
	}
	return true
}

// starSuffix returns the literal suffix of a segment pattern of the form "*"+suffix (no other glob character, no escape), and
// whether it has that form. "*" is the empty suffix.
func starSuffix(seg string) (string, bool) {
	suffix, ok := strings.CutPrefix(seg, "*")
	if !ok || strings.ContainsAny(suffix, `*?[]\`) {
		return "", false
	}
	return suffix, true
}

// scopeContains reports whether every path the child scope entry names lies inside
// one of the parent entries. A child names the paths it matches, not what lies
// below them: "README.md" is that file, so it lies inside "**/*.md" as scopeMatch
// places it, and "docs/*.md" is the Markdown files directly in docs. A parent
// entry keeps its full reading (a directory and everything under it). A glob child
// is compared segment by segment (globSegsContain), each of its brace alternatives
// inside one parent entry; what that cannot decide (a glob that only a union of
// parent entries covers, an absolute entry) is outside.
func scopeContains(parents []string, child string) bool {
	if slices.Contains(parents, child) {
		return true
	}
	kids, err := expandBraces(child)
	if err != nil || strings.HasPrefix(child, "/") {
		return false
	}
	for _, k := range kids {
		if !inParent(parents, strings.Split(strings.Trim(k, "/"), "/")) {
			return false
		}
	}
	return true
}

// inParent reports whether one parent entry (one of its brace alternatives) contains every path of the child segments.
func inParent(parents []string, kid []string) bool {
	for _, p := range parents {
		alts, err := expandBraces(p)
		if err != nil {
			continue
		}
		for _, a := range alts {
			if ps, abs := segments(a); !abs && globSegsContain(ps, kid) {
				return true
			}
		}
	}
	return false
}

// globSegsContain reports whether every path that matches the segment pattern c
// also matches p ("**" spans zero or more segments in both). It is sound, not
// complete: a "**" of c is covered only by a "**" of p, and a glob segment of c
// only as segContains decides. Every pair of positions is explored at most once.
func globSegsContain(p, c []string) bool {
	tried := make(map[[2]int]bool) // positions already explored; they led nowhere
	var from func(i, j int) bool
	from = func(i, j int) bool {
		if j == len(c) {
			return allGlobstar(p[i:])
		}
		if i == len(p) || tried[[2]int{i, j}] {
			return false
		}
		tried[[2]int{i, j}] = true
		switch {
		case p[i] == "**":
			// p's "**" matches nothing here, or takes what c[j] matches (one segment, or all that c's own "**" spans)
			return from(i+1, j) || from(i, j+1)
		case c[j] == "**":
			return false
		}
		return segContains(p[i], c[j]) && from(i+1, j+1)
	}
	return from(0, 0)
}

// allGlobstar reports whether all remaining segments are **, including an empty segment list.
func allGlobstar(s []string) bool {
	for _, x := range s {
		if x != "**" {
			return false
		}
	}
	return true
}

// segContains reports whether every segment that matches c also matches p (neither is "**"): an equal pattern, a literal c that
// p matches, p "*", or p "*" followed by a literal suffix that c, a valid pattern, also ends with ("*.md" contains "api*.md").
func segContains(p, c string) bool {
	switch {
	case p == c || p == "*":
		return true
	case !hasMeta(c):
		ok, err := path.Match(p, c)
		return err == nil && ok
	}
	suffix, ok := starSuffix(p)
	if !ok || !strings.HasSuffix(c, suffix) {
		return false
	}
	_, err := path.Match(c, "")
	return err == nil
}

// relTo returns p relative to root as a slash path, and whether p lies inside it.
// A relative p is taken to be relative to root already.
func relTo(root, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		c := path.Clean(filepath.ToSlash(p))
		if c == ".." || strings.HasPrefix(c, "../") {
			return "", false
		}
		return c, true
	}
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
