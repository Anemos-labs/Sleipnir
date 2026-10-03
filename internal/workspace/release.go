package workspace

import (
	"context"
	"fmt"
)

// Release relinquishes a stopped worker's tree without changing its files, index,
// or branch. The handle becomes unusable; a subsequent Manager may adopt the tree.
// The caller must first stop every operation using this handle.
func (t *Tree) Release(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isRemoved() {
		return nil
	}
	m := t.m
	m.st.lock.Lock()
	defer m.st.lock.Unlock()
	admin, mk, err := m.verifyTreeDir(ctx, t.Path)
	if err != nil {
		return err
	}
	if mk.Agent != t.Agent || mk.PID != m.st.self.pid {
		return fmt.Errorf("%w: %s is no longer owned by this manager", ErrForeign, t.Path)
	}
	mk.PID, mk.Start, mk.BootID, mk.PIDNS = 0, 0, "", ""
	if err := writeMarker(admin, *mk); err != nil {
		return err
	}
	t.removed.Store(true)
	livePaths.CompareAndDelete(t.Path, m)
	m.mu.Lock()
	if m.trees[t.Agent] == t {
		delete(m.trees, t.Agent)
	}
	m.mu.Unlock()
	return nil
}
