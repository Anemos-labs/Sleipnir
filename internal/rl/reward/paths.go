package reward

import (
	"fmt"
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
	abs := isAbsSlash(p)
	if abs && !strings.HasPrefix(p, "/") {
		p = p[2:] // the drive letter: "C:/x" climbs out as "/x" does
	}
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

// isAbsSlash reports whether a slash-separated path is absolute on some platform: rooted ("/x", and so a UNC
// "//server/share") or carrying a drive letter ("C:/x"). It does not depend on the host, because a run recorded on
// Windows may be scored on Linux and the reverse. A drive-relative "C:x" is not absolute.
func isAbsSlash(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && p[2] == '/' && ('a' <= p[0] && p[0] <= 'z' || 'A' <= p[0] && p[0] <= 'Z')
}

// isAbsPath is isAbsSlash for a path in either separator style.
func isAbsPath(p string) bool { return isAbsSlash(slashPath(p)) }

// cleanAbs normalises an absolute path ("" for anything not absolute). Windows paths are case-insensitive, so a
// path with a drive letter is returned with an upper-case letter and the rest lower-case ("D:/Work/Repo" and
// "d:/work/repo" name one directory). A UNC path (two leading backslashes) keeps a "//" root, lower-cased the same
// way, so "\\srv\share\x" ("//srv/share/x") is not "/srv/share/x" on the current drive. A path of forward slashes
// is rooted whatever its leading slashes ("//etc/passwd" is "/etc/passwd" on Linux), which keeps the system
// locations impossible to dodge with a doubled slash; a UNC path spelled with forward slashes is read that way too.
// POSIX paths keep their case.
func cleanAbs(p string) string {
	raw := strings.TrimSpace(p)
	p = slashPath(p)
	if !isAbsSlash(p) {
		return ""
	}
	switch {
	case p[0] != '/':
		return strings.ToUpper(p[:1]) + ":" + strings.ToLower(path.Clean(p[2:]))
	case strings.HasPrefix(raw, `\\`) && len(p) > 2 && p[2] != '/':
		return "/" + strings.ToLower(path.Clean(p))
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

// maxPatternBytes bounds one pattern. Patterns are written by task authors and
// are short ("tests/**", "**/*_test.go"); the bound keeps every match at
// O(len(path)) with a small constant even for a hostile pattern met by a hostile
// path. Score rejects a task whose protected pattern exceeds it rather than
// ignore the pattern: silently unprotected paths are the failure this package
// exists to prevent.
const maxPatternBytes = 1024

// compileGlobs expands bounded brace alternatives and compiles valid globs, skipping oversized or
// invalid patterns.
func compileGlobs(patterns []string) globSet {
	var out globSet
	for _, raw := range patterns {
		if len(raw) > maxPatternBytes {
			continue // see maxPatternBytes: Score refuses such a task before it gets here
		}
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
	if p == "" || strings.HasPrefix(p, "!") || len(p) > maxPatternBytes {
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
		if s == "" || s == "." {
			continue
		}
		if s == "**" && len(g.segs) > 0 && g.segs[len(g.segs)-1] == "**" {
			continue // "**/**" is "**"
		}
		g.segs = append(g.segs, s)
	}
	if len(g.segs) == 0 {
		return glob{}, false
	}
	return g, true
}

// checkPatterns rejects protected patterns that cannot be compiled whole: ones
// too long to match safely, and ones whose brace alternation expands past the
// limit (the surplus alternatives would silently protect nothing).
func checkPatterns(field string, patterns []string) error {
	for i, p := range patterns {
		if len(p) > maxPatternBytes {
			return fmt.Errorf("reward: %s[%d]: pattern is %d bytes; at most %d can be matched safely", field, i, len(p), maxPatternBytes)
		}
		if n := len(expandBraces(p, maxBraceAlternatives+1)); n > maxBraceAlternatives {
			return fmt.Errorf("reward: %s[%d]: brace alternation expands to more than %d patterns", field, i, maxBraceAlternatives)
		}
	}
	return nil
}

// expandBraces expands one level of {a,b} alternation, recursively, up to limit
// results.
func expandBraces(p string, limit int) []string {
	i := strings.IndexByte(p, '{')
	if i < 0 || len(p) > maxPatternBytes { // each level copies the pattern: bound the depth
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

// matchAnywhere is match for a path whose repository root is unknown: the
// absolute path /work/repo/tests/a.py must still hit "tests/**", so an anchored
// pattern may start at any segment. The automaton is re-seeded at every segment
// (one pass) rather than run once per suffix, which is quadratic in the depth.
func (s globSet) matchAnywhere(p string) (string, bool) {
	if len(s) == 0 {
		return "", false
	}
	segs := splitSegs(strings.ToLower(p))
	if len(segs) == 0 {
		return "", false
	}
	for _, g := range s {
		if g.matchesAt(segs, true) {
			return g.raw, true
		}
	}
	return "", false
}

// splitSegs splits slash paths, dropping empty and dot segments but preserving parent segments.
func splitSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

// matches evaluates the glob against path segments, requiring anchored patterns to start at the
// path root.
func (g glob) matches(ps []string) bool { return g.matchesAt(ps, false) }

// matchesAt is matches, optionally letting an anchored pattern start at any
// segment of the path.
func (g glob) matchesAt(ps []string, anywhere bool) bool {
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
	// Anchored: the pattern may match the path itself or any parent directory, i.e.
	// any non-empty prefix of its segments (all but the last for a directory-only
	// pattern).
	last := len(ps)
	if g.dirOnly {
		last--
	}
	return matchPrefix(g.segs, ps[:max(last, 0)], anywhere)
}

// matchPrefix reports whether the pattern segments match some non-empty prefix of
// ps exactly, with "**" spanning any number (including zero) of segments. It
// simulates the pattern as an automaton over ps in one pass, so the cost is
// O(len(pat)*len(ps)) at worst and it stops as soon as no state is alive (an
// anchored literal fails on the first segment). Trying each prefix separately
// would cost a factor len(ps) more, which a path of thousands of segments in a
// diff would turn into a stall.
//
// With reseed the pattern may also begin at any later segment, which is how a
// suffix of the path is matched without trying each suffix.
func matchPrefix(pat, ps []string, reseed bool) bool {
	n := len(pat)
	live := make([]bool, n+1) // live[i]: the first i pattern segments are consumed
	next := make([]bool, n+1)
	// A "**" may consume nothing, so a state in front of one also reaches the next.
	spread := func(set []bool) {
		for i := 0; i < n; i++ {
			if set[i] && pat[i] == "**" {
				set[i+1] = true
			}
		}
	}
	live[0] = true
	spread(live)
	for _, seg := range ps {
		if reseed {
			live[0] = true
			spread(live)
		}
		clear(next)
		alive := false
		for i := 0; i < n; i++ {
			if !live[i] {
				continue
			}
			switch {
			case pat[i] == "**":
				next[i] = true
				alive = true
			case segMatch(pat[i], seg):
				next[i+1] = true
				alive = true
			}
		}
		if !alive && !reseed {
			return false // nothing can revive the match; a reseeded one may start again below
		}
		spread(next)
		if next[n] {
			return true
		}
		live, next = next, live
	}
	return false
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
