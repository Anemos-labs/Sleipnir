package perm

import (
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// cwdSet is the set of directories the shell may be in. A cd inside a subshell
// or pipeline does not leak out, but tracking that exactly is not worth the
// risk of being wrong, so after a cd every later command is judged against
// both the old and the new directory.
type cwdSet struct {
	dirs    []string
	unknown bool // some cd could not be followed
}

// add retains up to eight distinct possible working directories and marks further possibilities
// unknown.
func (c *cwdSet) add(d string) {
	for _, x := range c.dirs {
		if x == d {
			return
		}
	}
	if len(c.dirs) >= 8 {
		c.unknown = true
		return
	}
	c.dirs = append(c.dirs, d)
}

// hasGlob detects wildcard or character-class openers in a path expression.
func hasGlob(s string) bool { return strings.ContainsAny(s, "*?[") }

// resolve turns one file operand into the accesses it stands for: one per
// possible working directory, per glob match, with symlinks followed. An
// operand that cannot be pinned down yields a dynamic access.
func (ev *evaluator) resolve(u pathUse, cw *cwdSet) []access {
	raw := u.raw
	if raw == "" || raw == "-" {
		return nil
	}
	if u.unresolved {
		return []access{{raw: raw, read: true, dynamic: true, unresolved: true}}
	}
	if why := pathProblem(raw); why != "" {
		return []access{{raw: raw, invalid: why}}
	}
	base := access{
		raw: raw, read: !u.write || u.both, write: u.write || u.both,
		tree: u.tree, content: u.content, nameOnly: u.nameOnly, format: u.format, guess: u.both,
	}
	// mk builds an access. unclean is the path as the kernel will walk it, ".."
	// components included: "link/../x" is the parent of link's target, so it must
	// reach realPath uncleaned. lex is its lexical reading, used as a second form
	// for rule matching.
	mk := func(unclean string, dyn bool) access {
		a := base
		a.dynamic = dyn
		if !dyn {
			a.lex, a.real = cleanPath(unclean), realPath(unclean)
			if dir, name := splitParent(unclean); u.noFollow && name != "" && name != ".." && name != "." {
				parent := realPath(dir)
				if why := pathProblem(parent); why != "" {
					a.invalid = why
					return a
				}
				a.real = cleanPath(filepath.Join(parent, name))
			}
			a.invalid = pathProblem(a.real)
		}
		return a
	}
	var out []access
	// addKnown adds the accesses of a word whose value is known: relative to cwd, and each file a glob in it matches.
	addKnown := func(w, cwd string) {
		abs := w
		if !filepath.IsAbs(abs) {
			if cwd == "" {
				out = append(out, mk("", true))
				return
			}
			abs = cwd + "/" + abs
		}
		if hasGlob(abs) {
			ms, ok := expandGlob(abs, &ev.globBudget)
			switch {
			case !ok:
				out = append(out, mk("", true))
			case len(ms) == 0: // the shell passes the pattern through unchanged
				out = append(out, mk(abs, false))
			default:
				for _, m := range ms {
					out = append(out, mk(m, false))
				}
			}
			return
		}
		out = append(out, mk(abs, false))
	}
	one := func(cwd string) {
		w, partial, dyn := ev.rs.expandWordVars(filepath.ToSlash(raw), cwd, ev.expansion())
		if dyn {
			a := mk("", true)
			if partial != "" {
				// The known part still says where the word points: ~/.ssh/$KEY
				// is somewhere inside ~/.ssh whatever $KEY is.
				if !filepath.IsAbs(partial) && cwd != "" {
					dir := strings.HasSuffix(partial, "/")
					partial = filepath.Join(cwd, partial)
					if dir {
						partial += "/" // docs/$X is inside docs, not beside it (Join drops the slash)
					}
				}
				if filepath.IsAbs(partial) {
					dir := partial
					if !strings.HasSuffix(partial, "/") {
						dir = filepath.Dir(partial)
					}
					a.prefix, a.lex = true, cleanPath(dir)
					a.real = realPath(a.lex)
				}
			}
			out = append(out, a)
			return
		}
		addKnown(w, cwd)
		// A part that comes from a command's output is empty when the command fails: the word then names something else
		// ("$d/proc/self/environ" is /proc/self/environ, "$D"/* is /*), which is judged too. A word that is empty then names nothing.
		if e, _, edyn := ev.rs.expandWordVars(filepath.ToSlash(raw), cwd, ev.emptiedExpansion()); !edyn && e != w && e != "" {
			addKnown(e, cwd)
		}
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "~") ||
		strings.HasPrefix(raw, "$HOME") || strings.HasPrefix(raw, "${HOME}") {
		d := ""
		if len(cw.dirs) > 0 {
			d = cw.dirs[0]
		}
		one(d)
		return out
	}
	for _, d := range cw.dirs {
		one(d)
	}
	if cw.unknown || len(cw.dirs) == 0 {
		out = append(out, mk("", true))
	}
	return out
}

// cdTo returns the working directories after a cd to target from cw.
func (ev *evaluator) cdTo(cw *cwdSet, target string) *cwdSet {
	next := &cwdSet{unknown: cw.unknown}
	if target == "-" || hasGlob(target) {
		next.unknown = true
		return next
	}
	if len(cw.dirs) == 0 {
		next.unknown = true
	}
	for _, d := range cw.dirs {
		w, _, dyn := ev.rs.expandWordVars(target, d, ev.expansion())
		if dyn {
			next.unknown = true
			continue
		}
		if !filepath.IsAbs(w) {
			w = filepath.Join(d, w)
		}
		next.add(cleanPath(w))
	}
	return next
}

// redirAccesses turns redirections into file accesses. Descriptor duplication
// (2>&1, >&2, 3>&-) and heredocs touch no file; ">" and friends write.
func (ev *evaluator) redirAccesses(rds []shellparse.Redirect, cw *cwdSet) []access {
	var out []access
	for _, rd := range rds {
		op := strings.TrimLeft(rd.Op, "0123456789")
		var use pathUse
		switch op {
		case "<":
			use = pathUse{raw: rd.Target}
		case ">", ">>", ">|", "&>", "&>>":
			use = pathUse{raw: rd.Target, write: true}
		case "<>":
			use = pathUse{raw: rd.Target, both: true}
		case ">&":
			if allDigits(rd.Target) || rd.Target == "-" {
				continue
			}
			use = pathUse{raw: rd.Target, write: true}
		default: // <<, <<<, <&
			continue
		}
		for _, a := range ev.resolve(use, cw) {
			a.redir = true
			out = append(out, a)
		}
	}
	return out
}

// allDigits requires a nonempty sequence of ASCII decimal digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
