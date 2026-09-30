// Package memory discovers project instruction files (AGENTS.md, CLAUDE.md,
// SLEIPNIR.md and friends) and renders them as one deterministic block of
// markdown.
//
// The rendered text feeds a byte-stable cached prompt layer, so everything here
// is built to produce identical bytes for identical content: the order is fixed
// by the discovery rules, line endings and comments are normalised, and paths
// are shown relative to the project root or as "~/..." so neither the machine's
// directory layout nor the user's home directory leaks into the prompt.
//
// Discovery order (later entries have higher precedence):
//
//  1. ~/.sleipnir/SLEIPNIR.md (scope "user")
//  2. for each directory from the project root down to cwd, AGENTS.md,
//     CLAUDE.md, SLEIPNIR.md and .sleipnir/SLEIPNIR.md (scope "project" at the
//     root, "dir" below it)
//  3. for each directory from the root down to cwd, SLEIPNIR.local.md and
//     .sleipnir/SLEIPNIR.local.md (scope "local")
//
// A line consisting of "@path" imports another file (relative to the importing
// file, "~/" for the home directory). Imports are followed to a depth of five,
// each file is included once, and an imported file appears right after the file
// that imported it with scope "import". Imports are restricted to markdown and
// text files inside the project root or the user's home directory (or under the
// user's ~/.sleipnir, which may be a symlink to a dotfiles checkout), so a
// repository cannot use an import line to pull an arbitrary file (an ssh key, a
// credentials file) into a prompt.
//
// Sizes are capped at 64 KB per file and 256 KB overall. A file over its cap is
// cut at a line boundary and says so. When the total is exceeded, the
// highest-precedence (latest) sources keep their text and the earliest give up
// theirs first, each with a note. Empty files and files that are only comments
// are skipped.
package memory

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
)

// Scopes name where a Source came from.
const (
	ScopeUser    = "user"
	ScopeProject = "project"
	ScopeDir     = "dir"
	ScopeLocal   = "local"
	ScopeImport  = "import"
)

const (
	// MaxFileBytes caps one file; longer files are cut and say so.
	MaxFileBytes = 64 << 10
	// MaxTotalBytes caps all sources together.
	MaxTotalBytes = 256 << 10
	// MaxImportDepth is how many levels of "@path" imports are followed.
	MaxImportDepth = 5
	// maxSources bounds the work a hostile tree of imports can cause.
	maxSources = 128
	// minPartial is the smallest slice of the total budget worth spending on a
	// partial file; below it the file is omitted with a note instead.
	minPartial = 512
)

// Source is one instruction file (or import) that made it into the prompt.
type Source struct {
	// Path is how the file is shown: relative to the project root with "/"
	// separators, or "~/..." under the user's home directory. It is never an
	// absolute machine path, so rendering is reproducible across machines.
	Path  string
	Scope string
	// Text is the file's content with line endings normalised, HTML comments
	// removed and surrounding blank lines trimmed.
	Text string
	// Truncated reports that Text ends with a truncation note.
	Truncated bool
}

// Opts says where to look.
type Opts struct {
	// Root is the project root. Empty means Cwd.
	Root string
	// Cwd is the working directory; files in every directory from Root down to
	// it are loaded. Empty means Root (or the process working directory when
	// both are empty).
	Cwd string
	// Home is the user's home directory. Empty means the current user's home
	// directory (os.UserHomeDir); tests always set it.
	Home string
}

type loader struct {
	root string
	home string // "" when unknown
	// userDir is ~/.sleipnir; anything lexically under it may be imported even
	// if it is a symlink to a dotfiles checkout elsewhere.
	userDir  string
	realRoot string
	realHome string
	dirs     []string // root ... cwd

	out  []Source
	seen []os.FileInfo
	errs []error
}

// Load discovers, reads and expands the instruction files for opts. Missing
// files are simply absent from the result. A non-nil error means some existing
// file could not be read (or looks binary); the returned sources are still
// everything that could be loaded, so callers can log the error and carry on.
func Load(opts Opts) ([]Source, error) {
	ld, err := newLoader(opts)
	if err != nil {
		return nil, err
	}
	if ld.home != "" {
		ld.file(filepath.Join(ld.userDir, "SLEIPNIR.md"), ScopeUser, 0)
	}
	for i, d := range ld.dirs {
		scope := ScopeDir
		if i == 0 {
			scope = ScopeProject
		}
		for _, name := range []string{"AGENTS.md", "CLAUDE.md", "SLEIPNIR.md", filepath.Join(".sleipnir", "SLEIPNIR.md")} {
			ld.file(filepath.Join(d, name), scope, 0)
		}
	}
	for _, d := range ld.dirs {
		for _, name := range []string{"SLEIPNIR.local.md", filepath.Join(".sleipnir", "SLEIPNIR.local.md")} {
			ld.file(filepath.Join(d, name), ScopeLocal, 0)
		}
	}
	ld.applyBudget()
	return ld.out, errors.Join(ld.errs...)
}

func newLoader(o Opts) (*loader, error) {
	root, cwd := o.Root, o.Cwd
	if root == "" && cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("memory: %w", err)
		}
		cwd = wd
	}
	if root == "" {
		root = cwd
	}
	if cwd == "" {
		cwd = root
	}
	home := o.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	ld := &loader{}
	var err error
	// Everything is canonicalised up front (symlinks resolved) so that display
	// paths do not depend on how the caller happened to spell a directory.
	if ld.root, err = canonical(root); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	cwd, err = canonical(cwd)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	ld.realRoot = ld.root
	if home != "" {
		if ld.home, err = canonical(home); err != nil {
			return nil, fmt.Errorf("memory: %w", err)
		}
		ld.realHome = ld.home
		ld.userDir = filepath.Join(ld.home, ".sleipnir")
	}
	ld.dirs = []string{ld.root}
	if rel, ok := under(ld.root, cwd); ok && rel != "." {
		cur := ld.root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			ld.dirs = append(ld.dirs, cur)
		}
	}
	return ld, nil
}

// canonical makes p absolute, cleaned and (where it exists) symlink-free.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r, nil
	}
	return abs, nil
}

// under reports whether p is base or inside it, and p relative to base.
func under(base, p string) (string, bool) {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return "", false
	}
	if rel == "." || filepath.IsLocal(rel) {
		return rel, true
	}
	return "", false
}

// file loads one instruction file and, recursively, its imports.
func (ld *loader) file(path, scope string, depth int) {
	if len(ld.out) >= maxSources {
		return
	}
	fi, err := os.Stat(path) // follows symlinks: CLAUDE.md -> AGENTS.md is one file
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			ld.errs = append(ld.errs, fmt.Errorf("memory: %s: %w", ld.display(path, scope), unwrapPath(err)))
		}
		return
	}
	// Directories, devices and FIFOs are not instruction files (and opening a
	// FIFO would block forever).
	if !fi.Mode().IsRegular() {
		return
	}
	for _, s := range ld.seen {
		if os.SameFile(s, fi) {
			return
		}
	}
	ld.seen = append(ld.seen, fi)

	raw, truncated, err := readCapped(path)
	if err != nil {
		ld.errs = append(ld.errs, fmt.Errorf("memory: %s: %w", ld.display(path, scope), unwrapPath(err)))
		return
	}
	if bytes.IndexByte(raw[:min(len(raw), 8192)], 0) >= 0 {
		ld.errs = append(ld.errs, fmt.Errorf("memory: %s: looks like a binary file; skipped", ld.display(path, scope)))
		return
	}
	text := clean(raw)
	imports := findImports(text)
	if strings.TrimSpace(text) == "" {
		return // nothing to tell the model
	}
	src := Source{Path: ld.display(path, scope), Scope: scope, Text: text}
	if truncated {
		src.Text += fileNote
		src.Truncated = true
	}
	ld.out = append(ld.out, src)

	if depth >= MaxImportDepth {
		return
	}
	for _, spec := range imports {
		if target, ok := ld.resolveImport(path, spec); ok {
			ld.file(target, ScopeImport, depth+1)
		}
	}
}

var fileNote = fmt.Sprintf("\n\n[... truncated: this file is larger than %d KB; the rest is not included]", MaxFileBytes>>10)

// readCapped reads at most MaxFileBytes of a file, reporting whether more was
// there. A cut never leaves half a UTF-8 character or, when a line break is
// close, half a line.
func readCapped(path string) (data []byte, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) <= MaxFileBytes {
		return data, false, nil
	}
	return cutAt(data, MaxFileBytes), true, nil
}

// cutAt shortens b to at most n bytes at a clean boundary.
func cutAt(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	b = b[:n]
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 && i >= len(b)-1024 {
		return b[:i]
	}
	for range utf8.UTFMax {
		if r, size := utf8.DecodeLastRune(b); r == utf8.RuneError && size <= 1 && len(b) > 0 {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// display renders a path the way it appears in Source.Path.
func (ld *loader) display(path, scope string) string {
	if scope == ScopeUser && ld.home != "" {
		if rel, ok := under(ld.home, path); ok {
			return sanitize("~/" + filepath.ToSlash(rel))
		}
	}
	if rel, ok := under(ld.root, path); ok {
		return sanitize(filepath.ToSlash(rel))
	}
	if ld.home != "" {
		if rel, ok := under(ld.home, path); ok {
			return sanitize("~/" + filepath.ToSlash(rel))
		}
	}
	return sanitize(filepath.Base(path))
}

// sanitize keeps a hostile file name from injecting lines into the prompt.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 { // controls and Unicode line separators
			return utf8.RuneError
		}
		return r
	}, s)
}

var importExts = []string{".md", ".markdown", ".mdx", ".txt"}

// resolveImport turns an "@spec" into a file path, or reports that the import
// should be ignored (unsupported type, outside the allowed locations, missing).
func (ld *loader) resolveImport(importer, spec string) (string, bool) {
	var target string
	switch {
	case spec == "~" || strings.HasPrefix(spec, "~/"):
		if ld.home == "" {
			return "", false
		}
		target = filepath.Join(ld.home, filepath.FromSlash(strings.TrimPrefix(spec, "~")))
	case filepath.IsAbs(filepath.FromSlash(spec)):
		target = filepath.Clean(filepath.FromSlash(spec))
	default:
		target = filepath.Join(filepath.Dir(importer), filepath.FromSlash(spec))
	}
	if !slices.Contains(importExts, strings.ToLower(filepath.Ext(target))) {
		return "", false
	}
	// The user's own ~/.sleipnir may be a symlink into a dotfiles repository, so
	// it is judged by where it is, not where it points. Everything else is judged
	// by its real location: a symlink inside the repository must not be a way
	// out of it.
	if ld.userDir != "" {
		if _, ok := under(ld.userDir, target); ok {
			return target, true
		}
	}
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false // missing: the line stays as plain text
	}
	if _, ok := under(ld.realRoot, real); ok {
		return target, true
	}
	if ld.realHome != "" {
		if _, ok := under(ld.realHome, real); ok {
			return target, true
		}
	}
	return "", false
}

// applyBudget enforces MaxTotalBytes. Later sources have higher precedence, so
// they get the budget first and the earliest ones are cut down or omitted.
func (ld *loader) applyBudget() {
	total := 0
	for _, s := range ld.out {
		total += len(s.Text)
	}
	if total <= MaxTotalBytes {
		return
	}
	budget := MaxTotalBytes
	for i := len(ld.out) - 1; i >= 0; i-- {
		s := &ld.out[i]
		n := len(s.Text)
		switch {
		case n <= budget:
			budget -= n
		case budget >= minPartial:
			s.Text = string(cutAt([]byte(s.Text), budget)) + totalNote
			s.Truncated = true
			budget = 0
		default:
			s.Text = omittedNote
			s.Truncated = true
		}
	}
}

var (
	totalNote   = fmt.Sprintf("\n\n[... truncated: the instruction files together exceed %d KB]", MaxTotalBytes>>10)
	omittedNote = fmt.Sprintf("[... omitted: the instruction files together exceed %d KB]", MaxTotalBytes>>10)
)

// Render formats sources as markdown: one "### <path> (<scope>)" block per
// source, in order, separated by a blank line. The output depends only on the
// sources, so it is byte-stable across runs and machines.
func Render(srcs []Source) string {
	var b strings.Builder
	for i, s := range srcs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("### ")
		b.WriteString(s.Path)
		b.WriteString(" (")
		b.WriteString(s.Scope)
		b.WriteString(")\n")
		b.WriteString(s.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// Hash identifies the rendered content: equal hashes mean equal bytes, so it can
// version the prompt layer built from it.
func Hash(srcs []Source) string {
	return string(core.HashString(Render(srcs)))
}
