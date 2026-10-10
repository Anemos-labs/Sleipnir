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
// lock is per path (cleaned and made absolute; symbolic links are not resolved, so two spellings of one file through a link are two
// locks) and is not reentrant: a caller that holds it must not call Save on the same path. It does nothing across processes.
func WriteLock(path string) (unlock func()) {
	key := filepath.Clean(path)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
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
