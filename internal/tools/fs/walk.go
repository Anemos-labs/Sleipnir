package fs

import (
	"context"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
)

type walkKind int

const (
	kindFile walkKind = iota
	kindDir
	kindLink
	kindOther
)

// entry is what the walker hands to a callback.
type entry struct {
	path string   // absolute path
	rel  string   // slash-separated, relative to the walk base
	segs []string // rel split into segments (do not retain: shared with the walker)
	kind walkKind
	dir  iofs.DirEntry
}

type walkAction int

const (
	walkContinue walkAction = iota
	// walkSkip on a directory means "do not descend".
	walkSkip
	walkStop
)

// maxWalkEntries bounds the work one call can do, so pointing a tool at "/"
// cannot pin a CPU for minutes.
const maxWalkEntries = 1_000_000

// walker is a depth-first directory walk that
//
//   - never follows symlinks (no loops, and nothing outside the tree is read
//     through a link without its own permission check),
//   - always skips .git,
//   - honours .gitignore files: those of the walked directories and, so that a
//     search inside a subdirectory still obeys the repository's rules, those of
//     the ancestors up to the origin (see ignoreOrigin),
//   - visits entries of a directory in name order.
type walker struct {
	ctx        context.Context
	base       string
	baseSegs   []string // base relative to the origin
	stack      ignoreStack
	gitignore  bool
	exclude    func(segs []string, isDir bool) bool // extra caller rules, relative to base
	fn         func(e entry) walkAction
	visited    int
	truncated  bool
	cancelled  bool
	unreadable int
}

// ignoreOrigin picks the directory whose .gitignore rules are the outermost ones
// that apply to base: the enclosing repository root when there is one,
// otherwise the outermost of the project root and the working directory that
// contains base, otherwise base itself. The .gitignore files above base are
// read only up to there.
func (k *call) ignoreOrigin(base string) string {
	for dir := base; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	origin := base
	for _, cand := range []string{k.env.Root, k.env.Cwd} {
		if cand == "" {
			continue
		}
		c, err := k.resolve(cand)
		if err != nil {
			continue
		}
		if _, ok := relWithin(c, base); ok && len(c) < len(origin) {
			origin = c
		}
	}
	return origin
}

func (k *call) newWalker(base string, useGitignore bool) *walker {
	w := &walker{ctx: k.ctx, base: base, gitignore: useGitignore}
	if !useGitignore {
		return w
	}
	origin := k.ignoreOrigin(base)
	rel, _ := filepath.Rel(origin, base)
	if rel != "." {
		w.baseSegs = splitPath(rel)
	}
	// Ancestors' .gitignore files, outermost first. baseSegs[:i] is the
	// directory i levels below the origin.
	dir := origin
	for i := 0; i < len(w.baseSegs); i++ {
		if f := loadIgnoreFile(filepath.Join(dir, ".gitignore"), i); f != nil {
			w.stack.push(f)
		}
		dir = filepath.Join(dir, w.baseSegs[i])
	}
	return w
}

// run walks the tree below w.base calling fn for every entry that survives the
// ignore rules. The base itself is never reported and never tested against
// ignore rules: asking for an ignored directory explicitly is allowed.
func (w *walker) run(fn func(e entry) walkAction) {
	w.fn = fn
	w.dirWalk(w.base, nil)
}

// list returns the admitted entries of dir in name order. It loads dir's
// .gitignore for the duration of the caller's use of the result; the returned
// function must be called when the caller is done with the directory.
func (w *walker) list(dir string, rel []string) ([]entry, func()) {
	done := func() {}
	if w.gitignore {
		if f := loadIgnoreFile(filepath.Join(dir, ".gitignore"), len(w.baseSegs)+len(rel)); f != nil {
			w.stack.push(f)
			done = w.stack.pop
		}
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		w.unreadable++
		return nil, done
	}
	out := make([]entry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if name == ".git" {
			continue
		}
		kind := kindOther
		switch typ := de.Type(); {
		case typ&iofs.ModeSymlink != 0:
			kind = kindLink
		case typ.IsDir():
			kind = kindDir
		case typ.IsRegular():
			kind = kindFile
		}
		segs := make([]string, len(rel)+1)
		copy(segs, rel)
		segs[len(rel)] = name
		isDir := kind == kindDir
		if w.exclude != nil && w.exclude(segs, isDir) {
			continue
		}
		if w.gitignore {
			full := make([]string, 0, len(w.baseSegs)+len(segs))
			full = append(append(full, w.baseSegs...), segs...)
			if w.stack.ignored(full, isDir) {
				continue
			}
		}
		out = append(out, entry{path: filepath.Join(dir, name), rel: strings.Join(segs, "/"), segs: segs, kind: kind, dir: de})
	}
	return out, done
}

func (w *walker) dirWalk(dir string, rel []string) (stop bool) {
	entries, done := w.list(dir, rel)
	defer done()
	for _, e := range entries {
		if w.ctx.Err() != nil {
			w.cancelled = true
			return true
		}
		w.visited++
		if w.visited > maxWalkEntries {
			w.truncated = true
			return true
		}
		act := w.fn(e)
		if act == walkStop {
			return true
		}
		if e.kind == kindDir && act != walkSkip {
			if w.dirWalk(e.path, e.segs) {
				return true
			}
		}
	}
	return false
}
