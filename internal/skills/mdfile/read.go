package mdfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultMaxBytes caps a file read when ReadOpts.MaxBytes is not set.
const DefaultMaxBytes = 128 << 10

var (
	// ErrEscapes reports a file whose real path (symlinks resolved) lies outside
	// the directory it must stay in.
	ErrEscapes = errors.New("resolves outside its allowed directory")
	// ErrNotRegular reports a directory, device, socket or FIFO where a file was
	// expected.
	ErrNotRegular = errors.New("not a regular file")
	// ErrBinary reports a file that contains NUL bytes.
	ErrBinary = errors.New("looks like a binary file")
)

// ReadOpts bounds and confines a read.
type ReadOpts struct {
	// MaxBytes caps how much is read; a longer file is cut and File.Truncated is
	// set. 0 means DefaultMaxBytes.
	MaxBytes int64
	// Contain, when not empty, is a symlink-free directory the file's real path
	// must lie inside. This is the defence against a repository that commits a
	// symlink named SKILL.md pointing at ~/.ssh/id_ed25519.
	Contain string
}

// File is what ReadFile returns.
type File struct {
	Data      []byte
	Truncated bool
	// Real is the path with symlinks resolved; set only when Contain was given.
	Real string
}

// ReadFile reads a regular file within the limits of o.
//
// The file is opened first and judged afterwards, on the open descriptor: it
// must be a regular file (opening with O_NONBLOCK, so that a FIFO planted in
// place of a definition cannot hang the loader), and, when Contain is set, the
// inode that was opened must be the one the resolved path names and that path
// must be inside Contain. Swapping a symlink between the check and the read
// therefore fails the check instead of redirecting the read.
func ReadFile(path string, o ReadOpts) (File, error) {
	limit := o.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	f, err := openRead(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return File{}, err
	}
	if !fi.Mode().IsRegular() {
		return File{}, ErrNotRegular
	}
	var real string
	if o.Contain != "" {
		if real, err = filepath.EvalSymlinks(path); err != nil {
			return File{}, err
		}
		if !Within(o.Contain, real) {
			return File{}, ErrEscapes
		}
		rfi, err := os.Stat(real)
		if err != nil {
			return File{}, err
		}
		if !os.SameFile(fi, rfi) {
			return File{}, ErrEscapes
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return File{}, err
	}
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		return File{}, ErrBinary
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return File{Data: data, Truncated: truncated, Real: real}, nil
}

// Within reports whether p is root or lies inside it, comparing cleaned paths.
// Both should already be symlink-free (see Canonical).
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || filepath.IsLocal(rel)
}

// Canonical makes p absolute and resolves its symlinks. It fails when p does
// not exist.
func Canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// TruncNote is appended to text that was cut to fit a size cap, so neither the
// model nor the user mistakes it for the whole file.
func TruncNote(limitBytes int64) string {
	return fmt.Sprintf("\n\n[... truncated: the file is larger than %d KB; the rest is not included]", limitBytes>>10)
}

// Parsed is a markdown definition file read, cleaned and split.
type Parsed struct {
	// Doc has Body cleaned: hidden characters and HTML comments removed, blank
	// lines around it trimmed, and a truncation note appended when the file was
	// cut.
	Doc Doc
	// Hidden counts the invisible characters removed from the file.
	Hidden Hidden
	// Truncated says the file exceeded the size cap.
	Truncated bool
	// Real is the file's symlink-free path (set when Contain was given).
	Real string
}

// ReadDoc reads path with ReadFile, normalises and sanitises the text, and
// splits the frontmatter from the body. Everything a model may later read has
// been through Sanitize and StripComments.
func ReadDoc(path string, o ReadOpts) (Parsed, error) {
	f, err := ReadFile(path, o)
	if err != nil {
		return Parsed{}, err
	}
	text, hidden := Sanitize(Normalize(f.Data))
	doc, err := Parse(text)
	if err != nil {
		return Parsed{}, err
	}
	body := strings.Trim(StripComments(doc.Body), "\n")
	if f.Truncated {
		if i := strings.LastIndexByte(body, '\n'); i > 0 && i >= len(body)-1024 {
			body = body[:i]
		}
		limit := o.MaxBytes
		if limit <= 0 {
			limit = DefaultMaxBytes
		}
		body += TruncNote(limit)
	}
	doc.Body = body
	return Parsed{Doc: doc, Hidden: hidden, Truncated: f.Truncated, Real: f.Real}, nil
}

// ReadDirLimited lists a directory in name order, reading at most limit
// entries (more is reported through more). A hostile directory of a million
// files must cost a bounded amount of work.
func ReadDirLimited(dir string, limit int) (entries []os.DirEntry, more bool, err error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	entries, err = f.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if len(entries) > limit {
		entries, more = entries[:limit], true
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, more, nil
}
