package input

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

const (
	// DefaultHistoryEntries is how many prompts a History keeps, in memory and in its file.
	DefaultHistoryEntries = 1000
	// DefaultHistoryBytes is the size at which a History's file is rewritten to its newest entries (2 MiB).
	DefaultHistoryBytes = 2 << 20
	// MaxEntryBytes is the largest prompt a History stores; a bigger one (a huge paste that was expanded) is not remembered.
	MaxEntryBytes = 64 << 10

	maxHistoryLine = 4 * MaxEntryBytes // a longer line is corrupt or hostile: it is skipped, never held in memory
)

// History is the prompts the user has sent, oldest first, in memory and optionally in a file that several sessions share.
//
// The file holds one JSON string per line, so a prompt with newlines in it is still one line and a line that does not parse
// is skipped without harming its neighbours. Entries are only ever read as data: they are cleaned like any other text and
// never run, expanded or used as a format string. The file is written with mode 0600 in a directory created 0700.
//
// A History is safe for concurrent use. Two Histories (two sessions, two processes) on one path do not corrupt it: every
// entry is one O_APPEND write of one complete line. Rewriting the file to drop old entries is the one operation that can lose
// an entry another session appended in the same instant; it never produces a damaged file.
type History struct {
	mu      sync.Mutex
	path    string
	max     int
	maxFile int64
	entries []string
	lines   int   // lines believed to be in the file
	size    int64 // bytes believed to be in the file
	skipped int
	err     error
}

// NewHistory is a History that lives in memory only.
func NewHistory() *History { return &History{max: DefaultHistoryEntries, maxFile: DefaultHistoryBytes} }

// OpenHistory loads the history stored at path with the default limits. See OpenHistoryLimits.
func OpenHistory(path string) (*History, error) { return OpenHistoryLimits(path, 0, 0) }

// OpenHistoryLimits loads the history stored at path, keeping at most maxEntries prompts (0 means the default) and
// rewriting the file to its newest entries when it grows past maxBytes (0 means the default). A file that does not exist yet
// is an empty history, created on the first Add. Lines that are not valid JSON strings are skipped (Skipped counts them).
//
// It never returns nil: when the file cannot be read (it is a directory, a device, unreadable) the error says why and the
// History is still usable, in memory, with whatever could be read; Add keeps trying to write and reports its own errors.
func OpenHistoryLimits(path string, maxEntries int, maxBytes int64) (*History, error) {
	h := NewHistory()
	h.path = path
	if maxEntries > 0 {
		h.max = maxEntries
	}
	if maxBytes > 0 {
		h.maxFile = maxBytes
	}
	if path == "" {
		return h, errors.New("history: empty path")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, lines, size, skipped, err := h.readFile()
	h.entries, h.lines, h.size, h.skipped = keepNewest(entries, h.max, h.maxFile), lines, size, skipped
	h.err = err
	return h, err
}

// Path is the file the history is stored in ("" for an in-memory history).
func (h *History) Path() string { return h.path }

// Len is the number of entries.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// At is the i-th entry, 0 being the oldest; "" when i is out of range.
func (h *History) At(i int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i < 0 || i >= len(h.entries) {
		return ""
	}
	return h.entries[i]
}

// Entries is a copy of all entries, oldest first.
func (h *History) Entries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.entries...)
}

// Skipped is how many corrupt or oversized lines the last load passed over.
func (h *History) Skipped() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.skipped
}

// Err is the error of the last load or write, nil if it succeeded. A failed write does not lose the entry for this session.
func (h *History) Err() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// Reload reads the file again, replacing the in-memory entries with what is stored (other sessions' prompts included). On a
// read error the entries are left as they were.
func (h *History) Reload() error {
	if h.path == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, lines, size, skipped, err := h.readFile()
	if err != nil {
		h.err = err
		return err
	}
	h.entries, h.lines, h.size, h.skipped, h.err = keepNewest(entries, h.max, h.maxFile), lines, size, skipped, nil
	return nil
}

// Add remembers a prompt. Text that is empty or only whitespace, equal to the newest entry, or longer than MaxEntryBytes is
// not stored, and that is not an error. Control characters and escape sequences are stripped. The entry is added in memory
// first and then appended to the file; if the file cannot be written Add returns the error and the entry stays in memory.
func (h *History) Add(text string) error {
	text = strings.TrimRight(cleanText(text), " \t\r\n")
	if strings.TrimSpace(text) == "" || len(text) > MaxEntryBytes {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.entries); n > 0 && h.entries[n-1] == text {
		return nil
	}
	h.entries = keepNewest(append(h.entries, text), h.max, h.maxFile)
	if h.path == "" {
		return nil
	}
	line, err := encodeLine(text)
	if err == nil {
		err = appendFile(h.path, line)
	}
	h.err = err
	if err != nil {
		return err
	}
	h.lines++
	h.size += int64(len(line))
	if h.lines > 2*h.max || h.size > h.maxFile {
		h.compact()
	}
	return nil
}

// Find returns the index of the newest entry at or before index from that contains query, or -1. A query with no upper-case
// letter matches regardless of case ("smart case"); an empty query matches the entry at from.
func (h *History) Find(query string, from int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if from >= len(h.entries) {
		from = len(h.entries) - 1
	}
	fold := !strings.ContainsFunc(query, unicode.IsUpper)
	if fold {
		query = strings.ToLower(query)
	}
	for i := from; i >= 0; i-- {
		e := h.entries[i]
		if fold {
			e = strings.ToLower(e)
		}
		if strings.Contains(e, query) {
			return i
		}
	}
	return -1
}

// keepNewest keeps at most max entries, the newest, and at most budget bytes of them (the newest entry always stays): 1000
// prompts of 64 KiB each are not something to hold in memory.
func keepNewest(entries []string, max int, budget int64) []string {
	keep, total := 0, int64(0)
	for i := len(entries) - 1; i >= 0 && keep < max; i-- {
		total += int64(len(entries[i]))
		if keep > 0 && total > budget {
			break
		}
		keep++
	}
	if keep < len(entries) {
		entries = append([]string(nil), entries[len(entries)-keep:]...)
	}
	return entries
}

// encodeLine JSON-quotes a history entry and appends a newline for the history file.
func encodeLine(text string) ([]byte, error) {
	b, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// openRegular opens path only if it is (or will be) a regular file: a FIFO would block the open and a device or a directory
// has no business holding prompts. A symbolic link to a regular file is followed (dotfile managers make them).
func openRegular(path string, flag int, perm os.FileMode) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("history: %s is not a regular file", path)
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("history: %s is not a regular file", path)
	}
	return f, nil
}

// appendFile adds one line with one write on a descriptor opened O_APPEND, which the operating system makes atomic with
// respect to other writers, and closes it again: nothing stays open, and a file another session has replaced is followed. A
// file that does not end in a newline (cut short by a crash, edited by hand) gets one first, so the new line is never glued
// to the old one and both stay readable.
func appendFile(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := openRegular(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		if f, err = openRegular(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err != nil {
			return err
		}
	}
	if fi, err := f.Stat(); err == nil {
		if fi.Mode().Perm()&0o077 != 0 {
			_ = f.Chmod(0o600) // a history file is private, whatever it was created as
		}
		var last [1]byte
		if fi.Size() > 0 {
			if _, err := f.ReadAt(last[:], fi.Size()-1); err == nil && last[0] != '\n' {
				line = append([]byte{'\n'}, line...)
			}
		}
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readFile reads the newest part of the history file. A missing file is an empty history.
func (h *History) readFile() (entries []string, lines int, size int64, skipped int, err error) {
	f, err := openRegular(h.path, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, 0, 0, err
	}
	size = fi.Size()
	r := bufio.NewReaderSize(f, 64<<10)
	if limit := 4 * h.maxFile; size > limit { // read the tail only, and drop the line it starts in the middle of
		if _, err := f.Seek(size-limit, io.SeekStart); err != nil {
			return nil, 0, 0, 0, err
		}
		r.Reset(f)
		if _, _, err := readLine(r); err != nil {
			return nil, 0, size, 0, nil
		}
	}
	for {
		line, tooLong, err := readLine(r)
		if len(line) > 0 || tooLong {
			lines++
			var s string
			switch {
			case tooLong || json.Unmarshal(line, &s) != nil:
				skipped++
			default:
				if s = strings.TrimRight(cleanText(s), " \t\r\n"); strings.TrimSpace(s) != "" && len(s) <= MaxEntryBytes {
					if n := len(entries); n == 0 || entries[n-1] != s {
						entries = append(entries, s)
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return entries, lines, size, skipped, err
			}
			return entries, lines, size, skipped, nil
		}
	}
}

// readLine reads one line without its newline. A line longer than maxHistoryLine is discarded as it is read (tooLong), so a
// hostile file cannot make the reader hold it.
func readLine(r *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > maxHistoryLine {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case e == nil:
			return trimEOL(line), tooLong, nil
		case errors.Is(e, bufio.ErrBufferFull):
			continue
		}
		return trimEOL(line), tooLong, e
	}
}

// trimEOL removes trailing CR and LF bytes, returning a slice of the input.
func trimEOL(b []byte) []byte { return bytes.TrimRight(b, "\r\n") }

// compact rewrites the file to the newest entries (merging what other sessions appended), through a temporary file in the
// same directory and a rename, so a reader or another appender sees the old file or the new one, never half of either.
func (h *History) compact() {
	entries, _, _, _, err := h.readFile()
	if err != nil {
		return
	}
	entries = keepNewest(entries, h.max, h.maxFile)
	var total int64
	var keep []string
	var lines [][]byte
	for i := len(entries) - 1; i >= 0; i-- { // newest first, until the byte budget (half the cap, so it does not recompact at once)
		line, err := encodeLine(entries[i])
		if err != nil || (len(keep) > 0 && total+int64(len(line)) > h.maxFile/2) { // the newest entry is always kept
			break
		}
		total += int64(len(line))
		keep = append(keep, entries[i])
		lines = append(lines, line)
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), ".history-*")
	if err != nil {
		return
	}
	w := bufio.NewWriter(tmp)
	for i := len(lines) - 1; i >= 0; i-- {
		w.Write(lines[i])
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), h.path); err != nil {
		os.Remove(tmp.Name())
		return
	}
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		keep[i], keep[j] = keep[j], keep[i]
	}
	h.entries, h.lines, h.size = keep, len(keep), total
}
