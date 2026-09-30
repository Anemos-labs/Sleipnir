package swarm

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

// Leases give agents exclusive write access to files for as long as they keep
// working on them. They complement, not replace, content-hash staleness checks
// in tools.FileState: leases stop two agents from wasting effort on the same
// file, while the hash check guarantees correctness even if a lease is stolen.
//
// Reads are never blocked, only writes, so agents can freely study each other's
// files.
type Leases struct {
	ttl   time.Duration
	now   func() time.Time
	board *Board

	mu   sync.Mutex
	held map[string]*lease
}

type lease struct {
	holder string
	last   time.Time
}

// NewLeases returns a lease table. board may be nil.
func NewLeases(ttl time.Duration, board *Board) *Leases {
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	return &Leases{ttl: ttl, now: time.Now, board: board, held: map[string]*lease{}}
}

// BeforeWrite implements tools.Guard.
func (l *Leases) BeforeWrite(agent, path string) error {
	path = filepath.Clean(path)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if cur, ok := l.held[path]; ok && cur.holder != agent && now.Sub(cur.last) < l.ttl {
		ago := now.Sub(cur.last).Round(time.Second)
		if l.board != nil {
			l.board.RaiseAlert("lease", fmt.Sprintf("%s wanted %s (held by %s)", agent, filepath.Base(path), cur.holder))
		}
		return fmt.Errorf("%s is being edited by %s (last write %s ago). Work on something else, or mail %s if you need a change there", path, cur.holder, ago, cur.holder)
	}
	l.held[path] = &lease{holder: agent, last: now}
	return nil
}

// AfterWrite implements tools.Guard: a write refreshes the lease.
func (l *Leases) AfterWrite(agent, path string) {
	path = filepath.Clean(path)
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.held[path]; ok && cur.holder == agent {
		cur.last = l.now()
	}
}

// ReleaseAll drops every lease an agent holds and returns the paths.
func (l *Leases) ReleaseAll(agent string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for p, cur := range l.held {
		if cur.holder == agent {
			out = append(out, p)
			delete(l.held, p)
		}
	}
	return out
}

// Holder reports who holds a path.
func (l *Leases) Holder(path string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.held[filepath.Clean(path)]
	if !ok || l.now().Sub(cur.last) >= l.ttl {
		return "", false
	}
	return cur.holder, true
}

// HeldBy lists an agent's leases.
func (l *Leases) HeldBy(agent string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for p, cur := range l.held {
		if cur.holder == agent {
			out = append(out, p)
		}
	}
	return out
}
