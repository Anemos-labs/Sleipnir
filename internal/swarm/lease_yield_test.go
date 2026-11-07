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

// The swarm gives its leases the roles even when no path has an owner: the yield above needs them, and a run without path owners
// left be-1 refused for minutes behind lib/region.js, which the manager had edited once.
func TestTheSwarmsLeasesKnowTheRolesWithoutPathOwners(t *testing.T) {
	s := New(Config{SessionID: "t"}, Deps{}, nil)
	s.Leases.mu.Lock()
	roleOf := s.Leases.roleOf
	s.Leases.mu.Unlock()
	if roleOf == nil {
		t.Fatal("the leases have no way to tell the manager from a worker")
	}
}

// A refused write's alert names the file, so the manager need not ask which one.
func TestALeaseAlertNamesTheFile(t *testing.T) {
	root := t.TempDir()
	b := NewBoard(nil)
	l := NewLeases(10*time.Minute, b)
	l.SetRoots(root)
	file := filepath.Join(root, "lib", "region.js")
	if err := l.BeforeWrite("be-1", file); err != nil {
		t.Fatal(err)
	}
	if err := l.BeforeWrite("be-2", file); err == nil {
		t.Fatal("be-2 took be-1's file")
	}
	alerts := b.Snapshot().Alerts
	if len(alerts) != 1 || alerts[0].Text != "be-2 wanted lib/region.js, leased to be-1" {
		t.Errorf("alerts = %+v", alerts)
	}
}
