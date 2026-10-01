package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// kind is what lives at a path.
type kind string

const (
	kAbsent kind = "absent"  // nothing there (yet)
	kFile   kind = "file"    // regular file, content saved in the blob store
	kLink   kind = "symlink" // symbolic link, target saved verbatim
	kDir    kind = "dir"     // directory (mode only; contents are never saved)
	kOther  kind = "special" // device, socket, fifo: cannot be saved or restored
	// kUnsaved is a regular file whose content could not be saved (too large,
	// unreadable). It is recorded so the rewind can say so instead of silently
	// skipping the file.
	kUnsaved kind = "unsaved"
)

// state is the recorded condition of one path at one moment. Blob is where the
// content lives; Sum is our own SHA-256 of it, kept separately so a state can
// be compared with a live file without trusting the blob store's key scheme
// and so fingerprints (which have no blob) use the same field.
type state struct {
	Kind   kind      `json:"kind"`
	Blob   core.Hash `json:"blob,omitempty"`
	Sum    core.Hash `json:"sum,omitempty"`
	Size   int64     `json:"size,omitempty"`
	Mode   uint32    `json:"mode,omitempty"` // POSIX permission + setuid/setgid/sticky bits
	MTime  int64     `json:"mtime,omitempty"`
	Target string    `json:"target,omitempty"`
	Note   string    `json:"note,omitempty"` // why the content was not saved
	// Owner is who owned the path (unix only). A restore hands a recreated or
	// replaced file back to them when the process is privileged enough to, so a
	// harness running as root in a container does not turn a user's files into
	// root's.
	Owner *owner `json:"owner,omitempty"`
}

type owner struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

// sameState reports whether two states describe the same thing on disk. mtime
// only counts for files we could not hash: for everything else content decides,
// so a bare `touch` is not a change.
func sameState(a, b state) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case kAbsent:
		return true
	case kFile:
		return a.Sum == b.Sum && a.Mode == b.Mode
	case kLink:
		return a.Target == b.Target
	case kDir:
		return a.Mode == b.Mode
	default:
		return a.Size == b.Size && a.MTime == b.MTime && a.Mode == b.Mode
	}
}

// posixMode packs a Go file mode into POSIX permission bits.
func posixMode(m fs.FileMode) uint32 {
	v := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		v |= 0o1000
	}
	return v
}

// goMode is the inverse of posixMode.
func goMode(v uint32) fs.FileMode {
	m := fs.FileMode(v & 0o777)
	if v&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if v&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if v&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

// isMissing reports "there is nothing at this path": either the entry does not
// exist or a parent component is not a directory (a write there would fail
// anyway, and for our purposes the path is absent).
func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// reason renders an I/O error without the path (the caller already names it).
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// capture reads the current state of abs. It never returns an error: anything
// that stops us from saving a file is recorded in the state (kUnsaved) so a
// snapshot failure never blocks the edit itself; the rewind reports it later.
// data is the file content for kFile states.
func (s *Store) capture(abs string) (st state, data []byte) {
	fi, err := os.Lstat(abs)
	if err != nil {
		if isMissing(err) {
			return state{Kind: kAbsent}, nil
		}
		return state{Kind: kUnsaved, Note: "cannot inspect: " + reason(err)}, nil
	}
	st = state{Mode: posixMode(fi.Mode()), MTime: fi.ModTime().UnixNano(), Size: fi.Size(), Owner: ownerOf(fi)}
	typ := fi.Mode().Type()
	switch {
	case typ&fs.ModeSymlink != 0:
		target, err := os.Readlink(abs)
		if err != nil {
			st.Kind, st.Note = kUnsaved, "cannot read link: "+reason(err)
			return st, nil
		}
		st.Kind, st.Target, st.Size, st.Mode = kLink, target, 0, 0
	case typ.IsDir():
		st.Kind, st.Size = kDir, 0
	case typ.IsRegular():
		return s.captureFile(abs, fi, st)
	default:
		st.Kind, st.Note = kOther, "special file ("+typ.String()+")"
	}
	return st, nil
}

func (s *Store) captureFile(abs string, fi fs.FileInfo, st state) (state, []byte) {
	tooLarge := func(size int64) (state, []byte) {
		st.Kind = kUnsaved
		st.Size = size
		st.Note = fmt.Sprintf("too large to save (%s; the cap is %s)", humanBytes(size), humanBytes(s.maxBytes))
		return st, nil
	}
	if fi.Size() > s.maxBytes {
		return tooLarge(fi.Size())
	}
	f, err := os.Open(abs)
	if err != nil {
		st.Kind, st.Note = kUnsaved, "cannot read: "+reason(err)
		return st, nil
	}
	defer f.Close()
	// The file may grow between Lstat and read, so the cap is enforced on what
	// was actually read as well.
	data, err := io.ReadAll(io.LimitReader(f, s.maxBytes+1))
	if err != nil {
		st.Kind, st.Note = kUnsaved, "cannot read: "+reason(err)
		return st, nil
	}
	if int64(len(data)) > s.maxBytes {
		return tooLarge(int64(len(data)))
	}
	st.Kind = kFile
	st.Size = int64(len(data))
	st.Sum = core.HashBytes(data)
	return st, data
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// writeFileAtomic replaces path with data via a temporary file in the same
// directory plus rename, so a crash or a concurrent reader never sees a
// half-written file. mode is applied explicitly (the umask must not alter a
// restored file's permission bits); own, when set, is applied first because
// changing an owner can clear setuid bits.
func writeFileAtomic(path string, data []byte, mode fs.FileMode, own *owner) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sleipnir-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	done := false
	defer func() {
		if !done {
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	chownPath(name, own)
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	done = true
	return nil
}

// symlinkAtomic replaces path with a symlink to target, atomically.
func symlinkAtomic(target, path string, own *owner) error {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".sleipnir-tmp-"+hex.EncodeToString(rnd[:]))
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	chownPath(tmp, own)
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// resolveDir resolves symlinks in dir, tolerating a tail that does not exist
// yet: the deepest existing ancestor is resolved and the rest appended as is.
func resolveDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return dir
	}
	return filepath.Join(resolveDir(parent), filepath.Base(dir))
}

// depth counts path separators, used to order deep paths before shallow ones.
func depth(p string) int { return strings.Count(filepath.ToSlash(p), "/") }

// chmodNoFollow is os.Chmod for a path that must not be a symlink: Chmod follows
// one, and a rewind must never change the permissions of something the path only
// happens to lead to.
func chmodNoFollow(path string, mode fs.FileMode) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return errors.New("the path is a symlink")
	}
	return os.Chmod(path, mode)
}

// errUnusable marks a manifest file that is not something to read at all (not a
// regular file, a symlink, or over the size limit), as opposed to an I/O error.
var errUnusable = errors.New("unusable file")

// readBounded reads a regular file of at most limit bytes, refusing to follow a
// symlink at the end of the path or to wait on a FIFO. What it refuses is
// reported as errUnusable.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, openReadFlags, 0)
	if err != nil {
		if isSymlinkRefusal(err) {
			return nil, fmt.Errorf("%w: it is a symlink", errUnusable)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", errUnusable)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: larger than %s", errUnusable, humanBytes(limit))
	}
	return data, nil
}

// cleanText makes text read from a manifest safe to show: control characters and
// Unicode line separators are dropped, and it is cut to max runes.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}
