// Package memory discovers project instruction files (AGENTS.md, CLAUDE.md,
// SLEIPNIR.md and friends) and renders them as one deterministic block of
// markdown.
//
// The rendered text feeds a byte-stable cached prompt layer, so everything here
// is built to produce identical bytes for identical content: the order is fixed
// by the discovery rules, line endings and comments are normalised, and paths
// are shown relative to the project root or as "~/.sleipnir/..." so neither the
// machine's directory layout nor the user's home directory leaks into the
// prompt.
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
// file, or "~/" for the home directory). Imports are followed to a depth of
// five, each file is included once, and an imported file appears right after the
// file that imported it with scope "import".
//
// # Trust
//
// Project files are attacker-controlled text: anyone who can get a repository
// cloned can plant an AGENTS.md in it. Two rules keep that text from reaching
// beyond the repository.
//
// Every file is read inside a trust domain. Files of the project (scopes
// "project", "dir", "local" and everything they import) live in the project
// root: a path, whether it is spelled directly or reached through a symlink,
// that leaves the root is refused, and so is an absolute symlink. The user's
// own ~/.sleipnir (and the imports of the file in it) is the only trusted place
// outside the project; it may be a symlink into a dotfiles checkout. A project
// file therefore cannot pull in a private key, a credentials file or another
// project's notes by naming or linking to it, and cannot import from the home
// directory at all.
//
// Imports are further limited to markdown and text files (.md, .markdown, .mdx,
// .txt) and never reach into hidden directories other than .sleipnir, .claude,
// .github and .agents, so a repository's own .git, .ssh, .aws, .gnupg or .env
// stay out even when they sit inside the root. A symlinked instruction file is
// held to the same standard: it must lead to such a file inside the root. The
// number of files, the bytes they add and the depth are bounded, so a hostile
// tree of imports costs a fixed amount of work.
//
// Text that renders as nothing but is read by a model (Unicode tag characters,
// variation selectors, bidirectional controls, zero-width and other format
// characters, and control characters) is removed from every file and from every
// displayed path, so what a person sees in an editor is what the model gets.
// The scope of each Source is kept so a caller can drop the project's files when
// the project is not trusted.
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
	"path"
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
	// maxImports caps the files pulled in through "@path" lines in one Load,
	// maxImportSpecs the "@path" lines examined (loaded or not) and
	// maxImportBytes the bytes the loaded imports may add.
	maxImports     = 64
	maxImportSpecs = 256
	maxImportBytes = 2 * MaxTotalBytes
	// maxProblems bounds the problems reported by one Load.
	maxProblems = 32
	// minPartial is the smallest slice of the total budget worth spending on a
	// partial file; below it the file is omitted with a note instead.
	minPartial = 512
)

// Source is one instruction file (or import) that made it into the prompt.
type Source struct {
	// Path is how the file is shown: relative to the project root with "/"
	// separators, or "~/.sleipnir/..." under the user's own directory (the
	// user's file and what it imports: the only sources shown with a "~/"
	// prefix). It is never an absolute machine path, so rendering is
	// reproducible across machines.
	Path  string
	Scope string
	// Text is the file's content with line endings normalised, hidden and control
	// characters and HTML comments removed and surrounding blank lines trimmed.
	Text string
	// Truncated reports that Text ends with a truncation note.
	Truncated bool
}

// FromUser reports whether the source is the user's own: ~/.sleipnir's instruction file, or a file that one imports (shown with a "~/"
// path). Everything else came with the project.
func (s Source) FromUser() bool {
	return s.Scope == ScopeUser || (s.Scope == ScopeImport && strings.HasPrefix(s.Path, "~/"))
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

// domain is a place instruction files may be read from. A file and everything it
// imports stay inside one domain.
type domain struct {
	// dir is the domain's directory: symlink-free for a project root, lexically
	// ~/.sleipnir for the user's.
	dir string
	// user marks ~/.sleipnir, the one trusted location outside the project: its
	// symlinks are followed wherever they lead (a dotfiles checkout, typically).
	user bool
	// root confines every access of a project domain to dir, race-free: a
	// symlink, a ".." or an absolute link that leaves it fails the open itself.
	root *os.Root
}

// open opens the file at rel (slash-separated, relative to the domain) without
// blocking, so a FIFO planted under an instruction file's name cannot hang us.
func (d *domain) open(rel string) (*os.File, error) {
	if d.user {
		return os.OpenFile(filepath.Join(d.dir, filepath.FromSlash(rel)), openFlags, 0)
	}
	return d.root.OpenFile(filepath.FromSlash(rel), openFlags, 0)
}

type loader struct {
	home string  // canonical home directory, "" when unknown
	proj *domain // the project root; nil when it cannot be opened
	user *domain // ~/.sleipnir; nil when the home directory is unknown
	// dirs are the directories from the root down to cwd, relative to the root
	// and slash-separated ("" is the root itself).
	dirs []string

	out  []Source
	seen []os.FileInfo
	errs []error
	// dropped counts problems beyond maxProblems.
	dropped int
	// Work done by imports so far, against maxImports, maxImportSpecs and
	// maxImportBytes; limitNoted keeps the limit message to one.
	imports, specs, importBytes int
	limitNoted                  bool
}

// Load discovers, reads and expands the instruction files for opts. Missing
// files are simply absent from the result. A non-nil error means some existing
// file could not be read, looks binary, was refused (a symlink or "@path"
// import leading outside its trust domain) or had hidden characters removed;
// the returned sources are still everything that could be loaded, so callers
// can log the error and carry on.
func Load(opts Opts) ([]Source, error) {
	ld, err := newLoader(opts)
	if err != nil {
		return nil, err
	}
	defer ld.close()
	if ld.user != nil {
		ld.file(ld.user, "SLEIPNIR.md", ScopeUser, 0)
	}
	if ld.proj != nil {
		for i, d := range ld.dirs {
			scope := ScopeDir
			if i == 0 {
				scope = ScopeProject
			}
			for _, name := range []string{"AGENTS.md", "CLAUDE.md", "SLEIPNIR.md", ".sleipnir/SLEIPNIR.md"} {
				ld.file(ld.proj, path.Join(d, name), scope, 0)
			}
		}
		for _, d := range ld.dirs {
			for _, name := range []string{"SLEIPNIR.local.md", ".sleipnir/SLEIPNIR.local.md"} {
				ld.file(ld.proj, path.Join(d, name), ScopeLocal, 0)
			}
		}
	}
	ld.applyBudget()
	if ld.dropped > 0 {
		ld.errs = append(ld.errs, fmt.Errorf("memory: %d further problems not listed", ld.dropped))
	}
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
	// Everything is canonicalised up front (symlinks resolved) so that display
	// paths do not depend on how the caller happened to spell a directory, and so
	// that containment is judged against where the root really is.
	rootDir, err := canonical(root)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	cwdDir, err := canonical(cwd)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	if home != "" {
		if ld.home, err = canonical(home); err != nil {
			return nil, fmt.Errorf("memory: %w", err)
		}
		ld.user = &domain{dir: filepath.Join(ld.home, ".sleipnir"), user: true}
	}
	r, err := os.OpenRoot(rootDir)
	switch {
	case err == nil:
		ld.proj = &domain{dir: rootDir, root: r}
	case !isMissing(err):
		ld.problem(fmt.Errorf("memory: project root: %w", unwrapPath(err)))
	}
	ld.dirs = []string{""}
	if rel, ok := under(rootDir, cwdDir); ok && rel != "." {
		cur := ""
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			cur = path.Join(cur, part)
			ld.dirs = append(ld.dirs, cur)
		}
	}
	return ld, nil
}

func (ld *loader) close() {
	if ld.proj != nil && ld.proj.root != nil {
		ld.proj.root.Close()
	}
}

// problem records something worth telling the caller, up to maxProblems.
func (ld *loader) problem(err error) {
	if len(ld.errs) >= maxProblems {
		ld.dropped++
		return
	}
	ld.errs = append(ld.errs, err)
}

// canonical makes p absolute, cleaned and (where it exists) symlink-free. A path that
// does not exist yet is spelled as its deepest existing ancestor resolves plus the rest,
// so that it agrees with paths that were resolved whole (on macOS a temporary directory
// is /var/... for one and /private/var/... for the other, and containment is a
// comparison of spellings).
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r, nil
	}
	rest := ""
	for cur := abs; ; {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest), nil
		}
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

// isMissing reports that a path (or a directory on the way to it) is not there.
func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// file loads one instruction file (rel is slash-separated, relative to d) and,
// recursively, its imports.
func (ld *loader) file(d *domain, rel, scope string, depth int) {
	if len(ld.out) >= maxSources {
		return
	}
	name := ld.display(d, rel)
	if err := ld.checkTarget(d, rel); err != nil {
		ld.problem(fmt.Errorf("memory: %s: %w", name, err))
		return
	}
	raw, truncated, ok, err := ld.read(d, rel)
	if err != nil {
		if !isMissing(err) {
			ld.problem(fmt.Errorf("memory: %s: %w", name, unwrapPath(err)))
		}
		return
	}
	if !ok {
		return // not a regular file, or already loaded
	}
	if scope == ScopeImport {
		ld.importBytes += len(raw)
	}
	if bytes.IndexByte(raw[:min(len(raw), 8192)], 0) >= 0 {
		ld.problem(fmt.Errorf("memory: %s: looks like a binary file; skipped", name))
		return
	}
	text, hidden := cleanCounting(raw)
	if hidden.total() > 0 {
		ld.problem(fmt.Errorf("memory: %s: removed %d hidden characters (%s)", name, hidden.total(), hidden))
	}
	imports := findImports(text)
	if strings.TrimSpace(text) == "" {
		return // nothing to tell the model
	}
	src := Source{Path: name, Scope: scope, Text: text}
	if truncated {
		src.Text += fileNote
		src.Truncated = true
	}
	ld.out = append(ld.out, src)

	if depth >= MaxImportDepth {
		return
	}
	for _, spec := range imports {
		if ld.specs >= maxImportSpecs {
			ld.limit()
			return
		}
		ld.specs++
		ld.importFile(d, rel, spec, depth)
	}
}

var fileNote = fmt.Sprintf("\n\n[... truncated: this file is larger than %d KB; the rest is not included]", MaxFileBytes>>10)

// checkTarget vets, before a project file is opened, where its path really
// lands. A symlink may only lead somewhere inside the project, and (when a
// symlink is involved at all) only to something that could have been imported:
// otherwise AGENTS.md -> .env or -> .git/config would smuggle a repository's own
// secrets into the prompt. The open itself is confined by the domain's os.Root
// whatever this finds; the check exists to say why.
func (ld *loader) checkTarget(d *domain, rel string) error {
	if d.user {
		return nil
	}
	abs := filepath.Join(d.dir, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil // missing, looping or unreadable: the open reports what matters
	}
	realRel, ok := under(d.dir, real)
	if !ok {
		return errors.New("symlink leads outside the project root; not loaded")
	}
	if real != abs && !importable(filepath.ToSlash(realRel), false) {
		return errors.New("symlink does not lead to a markdown or text file outside hidden directories; not loaded")
	}
	return nil
}

// read opens the file at rel and returns at most MaxFileBytes of it, reporting
// whether more was there. ok is false when there is nothing to load: not a
// regular file (directories, devices and FIFOs are not instruction files), or a
// file already loaded under another name.
func (ld *loader) read(d *domain, rel string) (data []byte, truncated, ok bool, err error) {
	f, err := d.open(rel)
	if err != nil {
		return nil, false, false, err
	}
	defer f.Close()
	fi, err := f.Stat() // of the opened file, so what is judged is what is read
	if err != nil {
		return nil, false, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, false, nil
	}
	for _, s := range ld.seen {
		if os.SameFile(s, fi) { // CLAUDE.md -> AGENTS.md is one file
			return nil, false, false, nil
		}
	}
	ld.seen = append(ld.seen, fi)
	data, truncated, err = readCapped(f)
	return data, truncated, err == nil, err
}

// readCapped reads at most MaxFileBytes of f, reporting whether more was there.
// A cut never leaves half a UTF-8 character or, when a line break is close,
// half a line.
func readCapped(f io.Reader) (data []byte, truncated bool, err error) {
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

// display renders a file the way it appears in Source.Path. Both branches show a
// domain-relative path, so no machine-specific prefix can reach the prompt.
func (ld *loader) display(d *domain, rel string) string {
	if d.user {
		return sanitize("~/.sleipnir/" + rel)
	}
	return sanitize(rel)
}

// sanitize keeps a hostile file name from injecting lines, or invisible text,
// into the prompt: control characters, Unicode line separators and every hidden
// character become U+FFFD, which is visible.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 || r == 0x85 || hiddenKind(r) != hiddenNone {
			return utf8.RuneError
		}
		return r
	}, s)
}

var importExts = []string{".md", ".markdown", ".mdx", ".txt"}

// hiddenDirs are the dot-directories an import may go through: the places
// instruction files legitimately live. Every other hidden name (.git, .ssh,
// .aws, .gnupg, .env, ...) is refused.
var hiddenDirs = []string{".sleipnir", ".claude", ".github", ".agents"}

// importable reports whether rel (slash-separated, relative to a domain) is a
// file that may be imported: markdown or text, and, unless the domain is the
// user's own, nowhere below a hidden name that is not in hiddenDirs.
func importable(rel string, user bool) bool {
	if !slices.Contains(importExts, strings.ToLower(path.Ext(rel))) {
		return false
	}
	if user {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") && !slices.Contains(hiddenDirs, strings.ToLower(part)) {
			return false
		}
	}
	return true
}

// importFile follows one "@spec" line of the file at importer (relative to d).
func (ld *loader) importFile(d *domain, importer, spec string, depth int) {
	rel, err := ld.resolveImport(d, importer, spec)
	switch {
	case errors.Is(err, errIgnored):
		return // not an importable type (or no home to expand ~ against): plain text
	case err != nil:
		ld.problem(fmt.Errorf("memory: %s: import @%s refused: %w", ld.display(d, importer), sanitize(spec), err))
		return
	}
	if ld.imports >= maxImports || ld.importBytes >= maxImportBytes {
		ld.limit()
		return
	}
	before := len(ld.out)
	ld.imports++
	ld.file(d, rel, ScopeImport, depth+1)
	if len(ld.out) == before { // missing, duplicate or empty: no work worth counting
		ld.imports--
	}
}

// limit says once that imports were cut off.
func (ld *loader) limit() {
	if ld.limitNoted {
		return
	}
	ld.limitNoted = true
	ld.problem(fmt.Errorf("memory: import limit reached (%d files, %d KB, %d lines); further imports skipped", maxImports, maxImportBytes>>10, maxImportSpecs))
}

// errIgnored says an "@spec" line is plain text, not an import.
var errIgnored = errors.New("not an import")

// resolveImport turns an "@spec" into a path relative to d. errIgnored means the
// line is just text (not a markdown/text path, or nothing to expand "~"
// against); any other error is a refusal, reported because the line names a file
// that could have been imported but the trust rules forbid it. A missing file is
// neither: the open decides that.
func (ld *loader) resolveImport(d *domain, importer, spec string) (string, error) {
	var target string
	switch {
	case spec == "~" || strings.HasPrefix(spec, "~/"):
		if ld.home == "" {
			return "", errIgnored
		}
		target = filepath.Join(ld.home, filepath.FromSlash(strings.TrimPrefix(spec, "~")))
	case filepath.IsAbs(filepath.FromSlash(spec)):
		target = filepath.Clean(filepath.FromSlash(spec))
	default:
		target = filepath.Join(d.dir, filepath.FromSlash(path.Dir(importer)), filepath.FromSlash(spec))
	}
	if !slices.Contains(importExts, strings.ToLower(filepath.Ext(target))) {
		return "", errIgnored
	}
	rel, ok := under(d.dir, target)
	if !ok {
		if d.user {
			return "", errors.New("outside the user's ~/.sleipnir directory")
		}
		return "", errors.New("outside the project root")
	}
	rel = filepath.ToSlash(rel)
	if !importable(rel, d.user) {
		return "", errors.New("inside a hidden directory")
	}
	return rel, nil
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
// source, in order, separated by a blank line. Sources from the repository carry
// ", unverified" after the scope, so nothing a repository author wrote reads like
// something the user said. The output depends only on the
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
		if s.Scope != ScopeUser && !(s.Scope == ScopeImport && strings.HasPrefix(s.Path, "~/")) {
			// Text that came out of the repository is guidance, not the user's word:
			// anyone who can commit to it can write it.
			b.WriteString(", unverified")
		}
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
