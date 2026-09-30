package swarm

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// A scope is the list of paths a task may touch: directories ("src/api", with or
// without a trailing slash: the directory and everything under it), files
// ("notes.txt") and globs ("api/**", "web/*.tsx", "src/{a,b}/**"). Patterns are
// relative to the repository root; "**" spans directories, "*" does not.
//
// Two things use them. Spawn and claim refuse tasks whose scopes overlap another
// active writer's (scopesOverlap: conservative, it may report an overlap that is
// not one but never misses one), and the lease guard refuses a write outside the
// writer's own scope (scopeMatch: exact).

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
	if strings.ContainsRune(p, 0) {
		return "", errors.New("scope entry contains a NUL byte")
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
// with a relative one at every alignment, and a glob against a glob counts as an
// overlap; a directory is not confused with a longer name ("src/a" versus "src/ab").
func scopesOverlap(a, b string) bool {
	ax, aerr := altsOf(a)
	bx, berr := altsOf(b)
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

func altsOf(p string) ([]string, error) {
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
	return expandBraces(strings.ToLower(c))
}

func altOverlap(a, b string) bool {
	sa, aAbs := segments(a)
	sb, bAbs := segments(b)
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

func segsOverlap(a, b []string) bool {
	for i := 0; ; i++ {
		switch {
		case i >= len(a) && i >= len(b):
			return true
		case i >= len(a):
			return allGlobstar(b[i:])
		case i >= len(b):
			return allGlobstar(a[i:])
		}
		x, y := a[i], b[i]
		if x == "**" || y == "**" {
			return true
		}
		if !segCompat(x, y) {
			return false
		}
	}
}

func allGlobstar(s []string) bool {
	for _, x := range s {
		if x != "**" {
			return false
		}
	}
	return true
}

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
	return true
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
