package reward

import (
	"path"
	"strconv"
	"strings"
)

// Path handling shared by the hack detectors. Everything here is about being
// hard to dodge: separators, "./", "..", case and quoting tricks all normalise
// to the same string before a glob or a deny list looks at it.

// slashPath converts backslashes to slashes and trims surrounding space.
func slashPath(p string) string {
	return strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
}

// cleanRel normalises a repository-relative path. escapes reports that the path
// climbs out of the repository root ("../x", or an absolute path); the returned
// string then has the leading ".." segments removed so globs still apply to
// what it names.
func cleanRel(p string) (clean string, escapes bool) {
	p = slashPath(p)
	if p == "" {
		return "", false
	}
	abs := strings.HasPrefix(p, "/")
	c := path.Clean("/" + strings.TrimLeft(p, "/"))
	// path.Clean of a rooted path cannot climb above "/", so detect the climb on
	// the unrooted form.
	u := path.Clean(strings.TrimLeft(p, "/"))
	if u == ".." || strings.HasPrefix(u, "../") {
		escapes = true
	}
	if abs {
		escapes = true
	}
	c = strings.TrimPrefix(c, "/")
	if c == "" || c == "." {
		return "", escapes
	}
	return c, escapes
}

// cleanAbs normalises an absolute path ("" for anything not absolute).
func cleanAbs(p string) string {
	p = slashPath(p)
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	return path.Clean(p)
}

// unquoteGit decodes a path quoted the way git quotes unusual file names: C
// escapes plus three-digit octal for bytes. ok is false when s is not quoted.
func unquoteGit(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s, false
	}
	in := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c != '\\' || i+1 >= len(in) {
			b.WriteByte(c)
			continue
		}
		i++
		switch in[i] {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '"', '\\':
			b.WriteByte(in[i])
		case '0', '1', '2', '3':
			if i+2 < len(in) {
				if n, err := strconv.ParseUint(in[i:i+3], 8, 8); err == nil {
					b.WriteByte(byte(n))
					i += 2
					continue
				}
			}
			b.WriteByte(in[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(in[i])
		}
	}
	return b.String(), true
}

// ---- globs ---------------------------------------------------------------------------

// glob is one compiled protected-path pattern with gitignore-like semantics:
//
//   - a pattern without a slash matches a file or directory of that name at any
//     depth ("*_test.go", "testdata");
//   - a pattern with a slash is anchored at the repository root, "*" and "?" do
//     not cross a slash and "**" matches any number of directories (".github/**");
//   - anything under a matched directory is matched;
//   - a trailing slash restricts the match to directories ("vendor/");
//   - "{a,b}" alternation is expanded; a leading "!" (negation) is not supported
//     and the pattern is ignored;
//   - matching is case-insensitive, because the verifier's checkout may live on
//     a case-insensitive filesystem where "Go.Mod" and "go.mod" are one file.
type glob struct {
	raw      string
	segs     []string
	anchored bool
	dirOnly  bool
}

// globSet is a compiled list of patterns.
type globSet []glob

const maxBraceAlternatives = 64

func compileGlobs(patterns []string) globSet {
	var out globSet
	for _, raw := range patterns {
		for _, p := range expandBraces(raw, maxBraceAlternatives) {
			if g, ok := compileGlob(raw, p); ok {
				out = append(out, g)
			}
		}
	}
	return out
}

func compileGlob(raw, p string) (glob, bool) {
	p = slashPath(p)
	if p == "" || strings.HasPrefix(p, "!") {
		return glob{}, false
	}
	g := glob{raw: raw}
	p = strings.ToLower(p)
	if strings.HasSuffix(p, "/") {
		g.dirOnly = true
		p = strings.TrimRight(p, "/")
	}
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	if strings.HasPrefix(p, "/") {
		g.anchored = true
		p = strings.TrimLeft(p, "/")
	}
	if p == "" {
		return glob{}, false
	}
	if strings.Contains(p, "/") {
		g.anchored = true
	}
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			g.segs = append(g.segs, s)
		}
	}
	if len(g.segs) == 0 {
		return glob{}, false
	}
	return g, true
}

// expandBraces expands one level of {a,b} alternation, recursively, up to limit
// results.
func expandBraces(p string, limit int) []string {
	i := strings.IndexByte(p, '{')
	if i < 0 {
		return []string{p}
	}
	j := strings.IndexByte(p[i:], '}')
	if j < 0 {
		return []string{p}
	}
	j += i
	var out []string
	for _, alt := range strings.Split(p[i+1:j], ",") {
		for _, rest := range expandBraces(p[:i]+alt+p[j+1:], limit) {
			out = append(out, rest)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// match reports whether the (already cleaned, slash-separated) path is covered
// by the pattern, and returns the pattern as the user wrote it.
func (s globSet) match(p string) (string, bool) {
	if len(s) == 0 {
		return "", false
	}
	segs := splitSegs(strings.ToLower(p))
	if len(segs) == 0 {
		return "", false
	}
	for _, g := range s {
		if g.matches(segs) {
			return g.raw, true
		}
	}
	return "", false
}

func splitSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

func (g glob) matches(ps []string) bool {
	if !g.anchored {
		// A slash-less pattern names a file or directory at any depth; a match on a
		// directory component covers everything under it.
		for i, s := range ps {
			if g.dirOnly && i == len(ps)-1 {
				break
			}
			if segMatch(g.segs[0], s) {
				return true
			}
		}
		return false
	}
	// Anchored: the pattern may match the path itself or any parent directory.
	last := len(ps)
	if g.dirOnly {
		last = len(ps) - 1
	}
	for j := 1; j <= last; j++ {
		if matchSegs(g.segs, ps[:j]) {
			return true
		}
	}
	return false
}

// matchSegs matches pattern segments against path segments with "**" spanning
// any number (including zero) of segments. Dynamic programming keeps it
// O(len(pat)*len(path)) whatever the pattern looks like.
func matchSegs(pat, ps []string) bool {
	prev := make([]bool, len(ps)+1)
	cur := make([]bool, len(ps)+1)
	prev[0] = true
	for i := 1; i <= len(pat); i++ {
		p := pat[i-1]
		cur[0] = p == "**" && prev[0]
		for j := 1; j <= len(ps); j++ {
			switch {
			case p == "**":
				cur[j] = prev[j] || cur[j-1] || prev[j-1]
			default:
				cur[j] = prev[j-1] && segMatch(p, ps[j-1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(ps)]
}

// segMatch matches one path segment: "*" any run, "?" one character, "[...]"
// a class. A malformed pattern falls back to literal comparison so a typo cannot
// silently disable protection.
func segMatch(pat, s string) bool {
	if pat == "**" {
		return true
	}
	ok, err := path.Match(pat, s)
	if err != nil {
		return pat == s
	}
	return ok
}
