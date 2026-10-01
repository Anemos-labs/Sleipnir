package perm

import (
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// expandWord expands the parts of a shell word that a permission check can
// know: a leading tilde, $HOME / ${HOME}, $PWD / ${PWD} and, when the session has a
// private scratch directory, $TMPDIR / ${TMPDIR}. dynamic reports that
// something else (another variable, a substitution, ~user) is left, in which
// case the word does not name a file the engine can judge; partial is then the
// text before the first unknown piece, so "~/.ssh/$KEY" is still seen to be
// inside ~/.ssh.
func (rs *resolver) expandWord(w, cwd string) (out, partial string, dynamic bool) {
	switch {
	case w == "~" || strings.HasPrefix(w, "~/"):
		if rs.home == "" {
			return "", "", true
		}
		w = rs.home + w[1:]
	case w == "~+" || strings.HasPrefix(w, "~+/"):
		if cwd == "" {
			return "", "", true
		}
		w = cwd + w[2:]
	case strings.HasPrefix(w, "~"):
		return "", "", true // ~user, ~-, ~1
	}
	if !strings.ContainsAny(w, "$`") {
		return w, "", false
	}
	var b strings.Builder
	for i := 0; i < len(w); {
		c := w[i]
		if c == '`' {
			return "", b.String(), true
		}
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		j := i + 1
		var name string
		if j < len(w) && w[j] == '{' {
			k := strings.IndexByte(w[j:], '}')
			if k < 0 {
				return "", b.String(), true
			}
			name = w[j+1 : j+k]
			j += k + 1
		} else {
			k := j
			for k < len(w) && (w[k] == '_' || w[k] >= 'a' && w[k] <= 'z' || w[k] >= 'A' && w[k] <= 'Z' || w[k] >= '0' && w[k] <= '9') {
				k++
			}
			name = w[j:k]
			j = k
		}
		switch name {
		case "HOME":
			if rs.home == "" {
				return "", b.String(), true
			}
			b.WriteString(rs.home)
		case "TMPDIR":
			if rs.tmp == "" {
				return "", b.String(), true
			}
			b.WriteString(rs.tmp)
		case "PWD":
			if cwd == "" {
				return "", b.String(), true
			}
			b.WriteString(cwd)
		default:
			return "", b.String(), true
		}
		i = j
	}
	return b.String(), "", false
}

// cwdSet is the set of directories the shell may be in. A cd inside a subshell
// or pipeline does not leak out, but tracking that exactly is not worth the
// risk of being wrong, so after a cd every later command is judged against
// both the old and the new directory.
type cwdSet struct {
	dirs    []string
	unknown bool // some cd could not be followed
}

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

func (c *cwdSet) clone() *cwdSet {
	return &cwdSet{dirs: append([]string(nil), c.dirs...), unknown: c.unknown}
}

func hasGlob(s string) bool { return strings.ContainsAny(s, "*?[") }

// resolve turns one file operand into the accesses it stands for: one per
// possible working directory, per glob match, with symlinks followed. An
// operand that cannot be pinned down yields a dynamic access.
func (ev *evaluator) resolve(u pathUse, cw *cwdSet) []access {
	raw := u.raw
	if raw == "" || raw == "-" {
		return nil
	}
	base := access{
		raw: raw, read: !u.write || u.both, write: u.write || u.both,
		tree: u.tree, content: u.content, nameOnly: u.nameOnly,
	}
	// mk builds an access. unclean is the path as the kernel will walk it, ".."
	// components included: "link/../x" is the parent of link's target, so it must
	// reach realPath uncleaned. lex is its lexical reading, used as a second form
	// for rule matching.
	mk := func(unclean string, dyn bool) access {
		a := base
		a.dynamic = dyn
		if !dyn {
			a.lex, a.real = filepath.Clean(unclean), realPath(unclean)
			if dir, name := splitParent(unclean); u.noFollow && name != "" && name != ".." && name != "." {
				a.real = filepath.Join(realPath(dir), name)
			}
		}
		return a
	}
	var out []access
	one := func(cwd string) {
		w, partial, dyn := ev.rs.expandWord(raw, cwd)
		if dyn {
			a := mk("", true)
			if partial != "" {
				// The known part still says where the word points: ~/.ssh/$KEY
				// is somewhere inside ~/.ssh whatever $KEY is.
				if !filepath.IsAbs(partial) && cwd != "" {
					partial = filepath.Join(cwd, partial)
				}
				if filepath.IsAbs(partial) {
					dir := partial
					if !strings.HasSuffix(partial, "/") {
						dir = filepath.Dir(partial)
					}
					a.prefix, a.lex = true, filepath.Clean(dir)
					a.real = realPath(a.lex)
				}
			}
			out = append(out, a)
			return
		}
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
		w, _, dyn := ev.rs.expandWord(target, d)
		if dyn {
			next.unknown = true
			continue
		}
		if !filepath.IsAbs(w) {
			w = filepath.Join(d, w)
		}
		next.add(filepath.Clean(w))
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
