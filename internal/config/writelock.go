package config

import (
	"path/filepath"
	"sync"
)

// The files the harness keeps for the person (the user configuration, auth.json, trust.json, mcp-approvals.json, schedule.json) are
// read, changed and written back whole, with an atomic rename and no lock between processes: two programs that change one at the same
// moment can lose the change that was written first, never leave a half-written file. Inside one process that loss is avoidable, and a
// server that answers many pages at once is exactly where it would happen, so every read-modify-write of such a file in this process
// holds the file's write lock (Save takes it itself).

var (
	writeLocksMu sync.Mutex
	writeLocks   = map[string]*sync.Mutex{}
)

// WriteLock serialises the read-modify-write cycles of one file within this process and returns the function that ends the cycle.
// Callers re-read the file after taking the lock and before they write, so the change of another goroutine is never written over. The
// lock is per file: the path is made absolute and its symbolic links are resolved as far as the path exists (so a file that does not
// exist yet has the lock it will have once it does, and two spellings of one file through a link share one lock). It is not
// reentrant: a caller that holds it must not call Save on the same path. It does nothing across processes.
func WriteLock(path string) (unlock func()) {
	key := resolveExisting(path)
	writeLocksMu.Lock()
	mu := writeLocks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		writeLocks[key] = mu
	}
	writeLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// resolveExisting is path made absolute and clean, with the symbolic links of the part that exists resolved: the file itself when it
// exists, else its nearest existing ancestor joined with the rest. A file that is created later is thereby named as it will be named
// once it exists (on macOS the temporary directory is a link, so the two spellings differ).
func resolveExisting(path string) string {
	abs := filepath.Clean(path)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	rest := ""
	for dir := abs; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
