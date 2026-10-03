package perm

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// A resolver turns the paths that appear in requests and rules into forms that
// can be compared: absolute, cleaned, and with symlinks followed. Symlinks are
// resolved before anything is matched, so a link inside the workspace that points
// outside is outside, and a link into ~/.ssh is ~/.ssh.
type resolver struct {
	home, homeReal string
	tmp            string     // the session's private scratch directory (Config.Tmp), "" when it has none
	roots          []rootPair // workspace roots: Root first, then any extras
	// treeBases are "<tree parent>/*" (each parent lexical and resolved): relative
	// patterns are anchored at every per-agent tree as well as at the root.
	treeBases []string
}

type rootPair struct{ lex, real string }

// newResolver records cleaned lexical and resolved forms of home and nonempty allowed roots.
func newResolver(home, root string, extra []string) *resolver {
	rs := &resolver{home: cleanAbs(home)}
	rs.homeReal = realPath(rs.home)
	for _, r := range append([]string{root}, extra...) {
		if r == "" {
			continue
		}
		l := cleanAbs(r)
		rs.roots = append(rs.roots, rootPair{lex: l, real: realPath(l)})
	}
	return rs
}

// addTreeParents anchors relative patterns at every directory directly below each
// parent as well (see Config.TreeParents). The parent's own name is escaped, so a
// directory with glob characters in its name matches only itself; the "*" that stands
// for the trees is the only pattern character.
func (rs *resolver) addTreeParents(parents []string) {
	for _, p := range parents {
		if p == "" {
			continue
		}
		l := cleanAbs(p)
		for _, d := range uniq(l, realPath(l)) {
			rs.treeBases = append(rs.treeBases, escapeGlob(d)+"/*")
		}
	}
}

// root is the primary workspace root ("" if none was configured).
func (rs *resolver) root() rootPair {
	if len(rs.roots) == 0 {
		return rootPair{}
	}
	return rs.roots[0]
}

// anchor lists the directories relative patterns are anchored to: the
// workspace root, or the current directory when none is configured (a Deny rule
// must not silently stop applying because no Root was set), and every per-agent
// tree (see addTreeParents).
func (rs *resolver) anchor() []string {
	var out []string
	if r := rs.root(); r.lex != "" {
		out = uniq(r.lex, r.real)
	} else {
		wd := cleanAbs(".")
		out = uniq(wd, realPath(wd))
	}
	return append(out, rs.treeBases...)
}

// cleanAbs cleans nonempty paths and makes relative paths absolute when the working directory can
// be read.
func cleanAbs(p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if wd, err := os.Getwd(); err == nil {
			p = filepath.Join(wd, p)
		}
	}
	return cleanPath(p)
}

// cleanPath keeps native path semantics while using slash-separated comparison forms.
func cleanPath(p string) string { return filepath.ToSlash(filepath.Clean(p)) }

// volumeRoot splits an absolute path into its filesystem root and uncleaned remainder.
// Retaining the volume keeps drive and UNC paths anchored during component walks.
func volumeRoot(p string) (root, rest string) {
	p = filepath.ToSlash(p)
	v := filepath.ToSlash(filepath.VolumeName(p))
	return v + "/", strings.TrimPrefix(p[len(v):], "/")
}

// inside reports whether p (already resolved) is at or below dir.
func inside(dir, p string) bool {
	if dir == "" {
		return false
	}
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// inWorkspace reports whether a resolved path lies inside a workspace root.
func (rs *resolver) inWorkspace(real string) bool {
	for _, r := range rs.roots {
		if inside(r.real, real) {
			return true
		}
	}
	return false
}

// realPath resolves symlinks in p component by component, including dangling
// links (a write through a link whose target does not exist yet creates the
// target) and components that do not exist (kept literally). ".." is applied to
// the already resolved prefix, as the kernel does: "link/.." is the parent of
// what link points to, not the directory containing link; p must therefore not
// be cleaned lexically first. At the hop bound it returns the cleaned input on
// POSIX (where the kernel also refuses the chain), and an empty, invalid path on
// Windows, whose kernel can follow longer chains than this resolver permits.
func realPath(p string) string {
	if !filepath.IsAbs(p) {
		return cleanPath(p)
	}
	resolved, tail := volumeRoot(p)
	rest := strings.Split(tail, "/")
	hops := 0
	for len(rest) > 0 {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = cleanPath(filepath.Dir(resolved))
			continue
		}
		next := cleanPath(filepath.Join(resolved, part))
		fi, err := os.Lstat(next)
		if err != nil || !pathLink(fi) {
			resolved = canonicalPath(next)
			continue
		}
		if hops++; hops > 40 {
			if filepath.Separator == '\\' {
				return ""
			}
			return cleanPath(p)
		}
		target, err := os.Readlink(next)
		if err != nil {
			resolved = next
			continue
		}
		target = filepath.ToSlash(target)
		if filepath.IsAbs(target) {
			resolved, target = volumeRoot(target)
		} else if filepath.Separator == '\\' && strings.HasPrefix(target, "/") {
			// A rooted Windows link keeps the volume, not the link's directory.
			resolved = filepath.ToSlash(filepath.VolumeName(resolved)) + "/"
			target = strings.TrimPrefix(target, "/")
		}
		rest = append(strings.Split(target, "/"), rest...)
	}
	return canonicalPath(resolved)
}

// splitParent splits a path at its last slash without cleaning either part.
func splitParent(p string) (dir, base string) {
	p = filepath.ToSlash(p)
	i := strings.LastIndexByte(p, '/')
	switch {
	case i < 0:
		return ".", p
	case i == len(filepath.VolumeName(p)):
		return p[:i+1], p[i+1:]
	}
	return p[:i], p[i+1:]
}

// pathGlob is a compiled gitignore-like pattern over absolute paths. "**" as a
// whole segment matches any number of directories (including none); other
// segments use path.Match syntax (* ? [...] \). A pattern matches a path when it
// matches the path or any directory above it, which is what makes Read(~/.ssh)
// cover everything inside.
type pathGlob struct{ segs []string }

// splitSegs splits a slash-separated path and discards empty segments.
func splitSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// segMatch translates and matches a single path-segment glob, treating invalid patterns as
// nonmatches.
func segMatch(pat, seg string) bool {
	ok, err := path.Match(goPattern(pat), seg)
	return err == nil && ok
}

var posixClasses = map[string]string{
	"alpha": "a-zA-Z", "digit": "0-9", "alnum": "a-zA-Z0-9", "lower": "a-z", "upper": "A-Z",
	"xdigit": "0-9a-fA-F", "word": "a-zA-Z0-9_", "blank": " \t", "space": " \t\n\r\f\v",
	"punct": "!-/:-@\\[-`{-~",
}

// goPattern translates the glob syntax of shells and gitignore that path.Match
// lacks into its dialect: "[!x]" negation, POSIX classes such as "[[:alpha:]]",
// and a leading "]" inside a class. Without this a pattern like ~/.ssh/id_[!x]sa
// would fail to match the very file the shell expands it to.
func goPattern(p string) string {
	if !strings.Contains(p, "[") {
		return p
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			b.WriteByte(c)
			i++
			b.WriteByte(p[i])
		case c == '[':
			j := i + 1
			neg := false
			if j < len(p) && (p[j] == '!' || p[j] == '^') {
				neg = true
				j++
			}
			var cls strings.Builder
			first, closed := true, false
			for j < len(p) {
				d := p[j]
				if d == ']' {
					if !first {
						closed = true
						break
					}
					cls.WriteString("\\]")
					first = false
					j++
					continue
				}
				first = false
				if d == '[' && j+1 < len(p) && p[j+1] == ':' {
					if end := strings.Index(p[j:], ":]"); end > 0 {
						if r, ok := posixClasses[p[j+2:j+end]]; ok {
							cls.WriteString(r)
							j += end + 2
							continue
						}
					}
				}
				if d == '\\' && j+1 < len(p) {
					cls.WriteByte(d)
					j++
				}
				cls.WriteByte(p[j])
				j++
			}
			if !closed { // malformed: let path.Match report it
				b.WriteString(p[i:])
				return b.String()
			}
			b.WriteByte('[')
			if neg {
				b.WriteByte('^')
			}
			b.WriteString(cls.String())
			b.WriteByte(']')
			i = j
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// matchSegs reports whether pat matches all of s, where "**" spans zero or more
// segments. It is the usual greedy wildcard walk with backtracking to the most
// recent "**".
func matchSegs(pat, s []string) bool {
	pi, si := 0, 0
	starP, starS := -1, 0
	for si < len(s) {
		switch {
		case pi < len(pat) && pat[pi] == "**":
			starP, starS = pi, si
			pi++
		case pi < len(pat) && segMatch(pat[pi], s[si]):
			pi++
			si++
		case starP >= 0:
			starS++
			si = starS
			pi = starP + 1
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == "**" {
		pi++
	}
	return pi == len(pat)
}

// matches reports whether the glob covers the absolute path p.
func (g *pathGlob) matches(p string) bool {
	if len(g.segs) == 0 { // the pattern "/": the whole file system
		return true
	}
	segs := splitSegs(p)
	for k := 1; k <= len(segs); k++ {
		if matchSegs(g.segs, segs[:k]) {
			return true
		}
	}
	return false
}

// hasMeta detects glob operators or escape syntax in a pattern.
func hasMeta(s string) bool { return strings.ContainsAny(s, `*?[\`) }

// compilePathGlobs compiles a rule pattern into one glob per equivalent form
// (lexical and symlink-resolved). Anchoring:
//
//	~/x, ~          under the home directory
//	/x              absolute
//	./x, x/y        relative to the workspace root
//	x, *.pem        no slash: any depth (deny/ask rules: anywhere on disk;
//	                allow rules: anywhere inside the workspace)
//
// With a nil resolver it only validates the syntax.
func compilePathGlobs(p string, action Action, rs *resolver) ([]*pathGlob, error) {
	if strings.HasPrefix(p, "!") {
		return nil, errors.New("negated patterns are not supported; use a deny rule")
	}
	pat := strings.TrimSuffix(p, "/")
	if pat == "" && p != "" { // the pattern was "/": the whole file system
		pat = "/"
	}
	var bases []string
	rel := pat
	absRoot := ""
	switch {
	case pat == "~" || strings.HasPrefix(pat, "~/"):
		rel = strings.TrimPrefix(strings.TrimPrefix(pat, "~"), "/")
		if rs != nil {
			bases = uniq(rs.home, rs.homeReal)
		}
	case filepath.IsAbs(pat):
		v := filepath.VolumeName(pat)
		if filepath.Separator == '\\' && (strings.Contains(v, `\`) || strings.HasPrefix(pat[len(v):], `\`)) {
			return nil, errors.New("use forward slashes in absolute permission patterns; backslashes escape glob characters")
		}
		absRoot = strings.TrimSuffix(canonicalPath(filepath.ToSlash(v)+"/"), "/") + "/"
		rel = strings.TrimPrefix(pat[len(v):], "/")
		bases = []string{absRoot}
	case strings.HasPrefix(pat, "/"):
		// Slash-rooted patterns retain their platform-independent glob meaning.
		absRoot, rel = "/", strings.TrimPrefix(pat, "/")
		bases = []string{absRoot}
	case strings.HasPrefix(pat, "./") || strings.Contains(pat, "/"):
		rel = strings.TrimPrefix(pat, "./")
		if rs != nil {
			bases = rs.anchor()
		}
	default: // floating
		if action == Allow {
			if rs != nil {
				for _, b := range rs.anchor() {
					bases = append(bases, b+"/**")
				}
			}
		} else {
			bases = []string{"/**"}
		}
	}
	rel = path.Clean("/" + rel)[1:]
	for _, s := range splitSegs(rel) {
		if s == "**" {
			continue
		}
		if _, err := path.Match(goPattern(s), ""); err != nil {
			return nil, errors.New("malformed glob segment " + s)
		}
	}
	if rs == nil {
		return nil, nil
	}
	var out []*pathGlob
	seen := map[string]bool{}
	add := func(segs []string) {
		k := strings.Join(segs, "/")
		if !seen[k] {
			seen[k] = true
			out = append(out, &pathGlob{segs: segs})
		}
	}
	for _, b := range bases {
		if b == "" {
			continue
		}
		add(append(splitSegs(b), splitSegs(rel)...))
	}
	// An absolute pattern may itself run through a symlink (/tmp -> /private/tmp):
	// add the form with its literal prefix resolved.
	if absRoot != "" {
		segs := splitSegs(rel)
		k := 0
		for k < len(segs) && !hasMeta(segs[k]) {
			k++
		}
		if k > 0 {
			pref := absRoot + strings.Join(segs[:k], "/")
			if r := realPath(pref); r != "" && r != pref {
				add(append(splitSegs(r), segs[k:]...))
			}
		}
	}
	return out, nil
}

// uniq removes empty and duplicate strings while preserving input order.
func uniq(vals ...string) []string {
	var out []string
	for _, v := range vals {
		dup := v == ""
		for _, o := range out {
			dup = dup || o == v
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

// shellGlob is a shell-style pattern segment matcher: like path.Match, but a
// leading "." must be matched by a literal "." in the pattern, as in bash.
func shellSegMatch(pat, name string) bool {
	if strings.HasPrefix(name, ".") && !strings.HasPrefix(pat, ".") {
		return false
	}
	return segMatch(pat, name)
}

// expandGlob expands an absolute pattern containing shell metacharacters against
// the real file system, the way the shell will when the command runs. It
// returns the matching paths (unresolved, sorted) and ok=false when the work
// bound is exceeded, in which case the caller must treat the word as opaque.
// A pattern that matches nothing yields no paths (the shell then passes the
// pattern itself, which the caller checks literally).
func expandGlob(pattern string, budget *int) (matches []string, ok bool) {
	root, tail := volumeRoot(pattern)
	segs := splitSegs(tail)
	cur := []string{root}
	sawMeta := false
	for i, seg := range segs {
		last := i == len(segs)-1
		var next []string
		switch {
		case seg == ".":
			continue
		case seg == "..":
			for _, c := range cur {
				next = append(next, filepath.Dir(realPath(c)))
			}
		case !hasMeta(seg):
			for _, c := range cur {
				p := filepath.Join(c, seg)
				if sawMeta {
					// Past a wildcard a literal name must exist to be a match.
					if _, err := os.Lstat(p); err != nil {
						continue
					}
				}
				next = append(next, p)
			}
		default:
			sawMeta = true
			for _, c := range cur {
				if *budget--; *budget < 0 {
					return nil, false
				}
				d, err := os.Open(realPath(c))
				if err != nil {
					continue
				}
				// Read in batches so a directory with millions of entries costs
				// only the budget, not the whole listing.
				for {
					ents, err := d.ReadDir(256)
					for _, e := range ents {
						if *budget--; *budget < 0 {
							d.Close()
							return nil, false
						}
						if !shellSegMatch(seg, e.Name()) {
							continue
						}
						p := filepath.Join(c, e.Name())
						if !last {
							// only directories (or links to them) can continue the path
							if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
								continue
							}
						}
						next = append(next, p)
					}
					if err != nil || len(ents) == 0 {
						break
					}
				}
				d.Close()
			}
		}
		cur = next
		if len(cur) == 0 {
			return nil, true
		}
	}
	sort.Strings(cur)
	return cur, true
}
