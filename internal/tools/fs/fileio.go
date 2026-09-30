package fs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/reee344/sleipnir/internal/tools"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// splitBOM separates a UTF-8 byte order mark from the content. Tools edit the
// body and put the mark back, so a BOM never shows up as a stray character in
// old_string matching and is never lost on write.
func splitBOM(b []byte) (bom, body []byte) {
	if bytes.HasPrefix(b, utf8BOM) {
		return b[:3], b[3:]
	}
	return nil, b
}

// eolCounts counts CRLF and bare-LF line endings.
func eolCounts(s string) (crlf, lf int) {
	for i := 0; ; {
		j := strings.IndexByte(s[i:], '\n')
		if j < 0 {
			return
		}
		j += i
		if j > 0 && s[j-1] == '\r' {
			crlf++
		} else {
			lf++
		}
		i = j + 1
	}
}

// dominantEOL is the line ending new text should use to blend into s.
func dominantEOL(s string) string {
	crlf, lf := eolCounts(s)
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}

// readFile reads a regular file of at most max bytes. On failure it returns a
// ready-to-send error result.
func (k *call) readFile(path, disp string, max int64) ([]byte, iofs.FileInfo, *tools.Result) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, k.statFailure(path, disp, err)
	}
	if fi.IsDir() {
		return nil, nil, k.fail("%s is a directory, not a file; use ls to list it", disp)
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, k.fail("%s is not a regular file (%s)", disp, fileKind(fi.Mode()))
	}
	if fi.Size() > max {
		return nil, nil, k.fail("%s is %s, larger than the %s limit for this tool; use grep or the shell for a file this size", disp, humanBytes(fi.Size()), humanBytes(max))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, k.statFailure(path, disp, err)
	}
	defer f.Close()
	// The size can grow between stat and read; never buffer more than max.
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, nil, k.fail("cannot read %s: %s", disp, osReason(err))
	}
	if int64(len(data)) > max {
		return nil, nil, k.fail("%s is larger than the %s limit for this tool", disp, humanBytes(max))
	}
	return data, fi, nil
}

func fileKind(m iofs.FileMode) string {
	switch {
	case m&iofs.ModeNamedPipe != 0:
		return "named pipe"
	case m&iofs.ModeSocket != 0:
		return "socket"
	case m&iofs.ModeDevice != 0:
		return "device"
	}
	return m.String()
}

func (k *call) statFailure(path, disp string, err error) *tools.Result {
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		msg := notFoundMessage(path, disp)
		if hasMeta(filepath.Base(disp)) || strings.Contains(disp, "**") {
			msg += " (a path is a literal file or directory, not a pattern; use glob to find files by pattern, or grep's glob parameter to filter what it searches)"
		}
		return k.fail("%s", msg)
	case errors.Is(err, iofs.ErrPermission):
		return k.fail("permission denied reading %s", disp)
	}
	return k.fail("cannot read %s: %s", disp, osReason(err))
}

// notFoundMessage explains a missing file and, when the directory holds
// something similarly named, points at it: a typo or wrong case is by far the
// most common reason a model asks for a file that is not there.
func notFoundMessage(path, disp string) string {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Sprintf("file not found: %s (its directory does not exist either)", disp)
	}
	msg := "file not found: " + disp
	if len(entries) > 5000 {
		return msg
	}
	base := filepath.Base(path)
	type cand struct {
		name  string
		score float64
	}
	var cands []cand
	for _, e := range entries {
		name := e.Name()
		s := 0.0
		ln, lb := strings.ToLower(name), strings.ToLower(base)
		switch {
		case ln == lb:
			s = 1
		case len(ln) <= 64 && len(lb) <= 64 && osaDistance(ln, lb) <= 1+len(lb)/12:
			s = 0.9
		default:
			s = diceStrings(ln, lb)
		}
		if s >= 0.6 {
			cands = append(cands, cand{name, s})
		}
	}
	if len(cands) == 0 {
		return msg
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > 3 {
		cands = cands[:3]
	}
	parent := filepath.Dir(disp)
	names := make([]string, len(cands))
	for i, c := range cands {
		names[i] = safeName(filepath.ToSlash(filepath.Join(parent, c.name)))
	}
	return msg + "; did you mean " + strings.Join(names, ", ") + "?"
}

// freshness runs FileState's staleness check for a file whose current bytes are
// current, and rewrites the path in its message to the display spelling.
func (k *call) freshness(path, disp string, current []byte, requireRead bool) *tools.Result {
	if err := k.env.Files.CheckFresh(k.env.Agent, path, current, requireRead); err != nil {
		return k.fail("%s", strings.Replace(err.Error(), path, disp, 1))
	}
	return nil
}

// commit runs the guarded write sequence for one file:
// Guard.BeforeWrite -> Snapshotter.Before -> atomic write -> FileState.RecordWrite
// -> Guard.AfterWrite. The caller holds the path lock and has already passed the
// permission and freshness checks; existing is nil when the file is created.
func (k *call) commit(path, disp string, data []byte, existing iofs.FileInfo) *tools.Result {
	if res := k.beforeWrite(path, disp); res != nil {
		return res
	}
	if existing == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return k.fail("cannot create the directory for %s: %s", disp, osReason(err))
		}
	}
	if err := atomicWrite(path, data, existing); err != nil {
		return k.fail("cannot write %s: %s", disp, osReason(err))
	}
	k.afterWrite(path, data)
	return nil
}

// beforeWrite is the veto half of a write: Guard first (a swarm lease should
// stop the write before anything is recorded), then the checkpoint.
func (k *call) beforeWrite(path, disp string) *tools.Result {
	if err := k.env.Guard.BeforeWrite(k.env.Agent, path); err != nil {
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "write to " + disp + " was refused"
		}
		return k.fail("%s", msg)
	}
	if k.env.Snap != nil {
		if err := k.env.Snap.Before(k.env.Agent, path); err != nil {
			return k.fail("cannot checkpoint %s before writing (%s); nothing was written", disp, err)
		}
	}
	return nil
}

func (k *call) afterWrite(path string, data []byte) {
	k.env.Files.RecordWrite(k.env.Agent, path, data, k.env.Now())
	k.env.Guard.AfterWrite(k.env.Agent, path)
}

// atomicWrite replaces path with data: write a sibling temp file, fsync it, then
// rename over the target, so a crash or a concurrent reader never sees a
// half-written file. The existing mode and owner are preserved; a new file gets
// 0644 (subject to the umask, because it is created with that mode).
//
// When the atomic route is impossible for permission reasons (read-only
// directory but writable file, single-file bind mount) it falls back to writing
// in place. It never falls back after a write error such as ENOSPC: truncating
// the original then would destroy it.
func atomicWrite(path string, data []byte, existing iofs.FileInfo) (err error) {
	dir := filepath.Dir(path)
	createPerm := iofs.FileMode(0o644)
	if existing != nil {
		createPerm = 0o600
	}
	f, err := createTemp(dir, createPerm)
	if err != nil {
		if existing != nil && errors.Is(err, iofs.ErrPermission) {
			return writeInPlace(path, data)
		}
		return err
	}
	tmp := f.Name()
	closed := false
	renamed := false
	defer func() {
		if !closed {
			f.Close()
		}
		if !renamed {
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if existing != nil {
		copyOwner(f, existing)
		mode := existing.Mode() & (iofs.ModePerm | iofs.ModeSetuid | iofs.ModeSetgid | iofs.ModeSticky)
		if err = f.Chmod(mode); err != nil {
			return err
		}
	}
	if err = f.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.ENOSYS) {
		// A filesystem that cannot fsync (some FUSE and network mounts) is not a
		// reason to refuse the write; any real I/O error still is.
		return err
	}
	err = nil
	closed = true
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		if existing != nil && (errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV) || errors.Is(err, iofs.ErrPermission)) {
			return writeInPlace(path, data)
		}
		return err
	}
	renamed = true
	return nil
}

func writeInPlace(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func createTemp(dir string, perm iofs.FileMode) (*os.File, error) {
	for i := 0; i < 64; i++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, ".sleipnir-"+hex.EncodeToString(b[:])+".tmp")
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, iofs.ErrExist) {
			return nil, err
		}
	}
	return nil, errors.New("cannot create a temporary file")
}

// removeFile deletes path (used by apply_patch and its rollback).
func removeFile(path string) error {
	err := os.Remove(path)
	if err != nil && errors.Is(err, iofs.ErrNotExist) {
		return nil
	}
	return err
}
