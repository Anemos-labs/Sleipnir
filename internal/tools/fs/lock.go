package fs

import (
	"sort"
	"sync"
)

// pathLocks serialises mutations of the same file across every agent goroutine.
//
// It is deliberately process-wide rather than a field of a tool or of a registry:
// mutual exclusion is a property of the filesystem, so two registries (or two
// tool instances) writing the same path must still exclude each other. Entries
// are reference counted and removed when idle, so the table does not grow with
// the number of files ever touched.
type pathLocks struct {
	mu sync.Mutex
	m  map[string]*pathLock
}

type pathLock struct {
	mu   sync.Mutex
	refs int
}

var fileLocks = &pathLocks{m: map[string]*pathLock{}}

// acquire locks every path (canonical spellings) and returns the unlock
// function. Paths are locked in sorted order so multi-file operations
// (apply_patch) cannot deadlock against each other.
func (p *pathLocks) acquire(paths ...string) func() {
	keys := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, s := range paths {
		if !seen[s] {
			seen[s] = true
			keys = append(keys, s)
		}
	}
	sort.Strings(keys)

	locks := make([]*pathLock, len(keys))
	p.mu.Lock()
	for i, key := range keys {
		l := p.m[key]
		if l == nil {
			l = &pathLock{}
			p.m[key] = l
		}
		l.refs++
		locks[i] = l
	}
	p.mu.Unlock()

	for _, l := range locks {
		l.mu.Lock()
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].mu.Unlock()
		}
		p.mu.Lock()
		for i, key := range keys {
			locks[i].refs--
			if locks[i].refs == 0 {
				delete(p.m, key)
			}
		}
		p.mu.Unlock()
	}
}
