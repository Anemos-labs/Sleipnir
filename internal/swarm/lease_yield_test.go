package swarm

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The manager's lease yields to a worker: in a trial the manager's one-line fix to tests/test_api.py held the file for ten minutes
// against the backend worker whose task owned it. A worker's lease still holds against the manager and against other workers.
func TestTheManagersLeaseYieldsToAWorker(t *testing.T) {
	root := t.TempDir()
	l := NewLeases(10*time.Minute, nil)
	l.SetRoots(root)
	roles := map[string]string{"mgr": "manager", "be-1": "backend", "be-2": "backend"}
	l.SetOwners(nil, func(id string) string { return roles[id] })
	file := filepath.Join(root, "tests", "test_api.py")
	if err := l.BeforeWrite("mgr", file); err != nil {
		t.Fatal(err)
	}
	if err := l.BeforeWrite("be-1", file); err != nil {
		t.Fatalf("the worker was locked out by the manager's lease: %v", err)
	}
	if err := l.BeforeWrite("be-2", file); err == nil || !strings.Contains(err.Error(), "be-1") {
		t.Errorf("another worker took be-1's file: %v", err)
	}
	if err := l.BeforeWrite("mgr", file); err == nil || !strings.Contains(err.Error(), "be-1") {
		t.Errorf("the manager took be-1's file: %v", err)
	}
}
