package perm

import (
	"context"
	"path/filepath"
	"testing"
)

// Every profile of a role is judged: a configuration entry and a definition both restrict it, and neither loosens the other.
func TestRoleProfilesInAlsoAreAllEnforced(t *testing.T) {
	root := t.TempDir()
	e, err := NewEngine(Config{Mode: ModeBypass, Root: root, Home: t.TempDir(), Roles: map[string]RoleProfile{
		"r": {Allow: []string{"Bash(touch:*)"}, Also: []RoleProfile{{Mode: ModePlan}, {Deny: []string{"Read(./secret)"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		what  string
		req   Request
		allow bool
	}{
		{"plan mode of the second profile holds", Request{Role: "r", Tool: "bash", Command: "touch x", Cwd: root}, false},
		{"the third profile's denial holds", Request{Role: "r", Tool: "read", Paths: []string{filepath.Join(root, "secret")}}, false},
		{"what all allow is allowed", Request{Role: "r", Tool: "read", Paths: []string{filepath.Join(root, "other")}}, true},
		{"another role is not affected", Request{Role: "q", Tool: "bash", Command: "touch x", Cwd: root}, true},
	} {
		if d := e.Check(context.Background(), tc.req); d.Allow != tc.allow {
			t.Errorf("%s: allow = %v (%s)", tc.what, d.Allow, d.Reason)
		}
		if c := e.Classify(tc.req); (c.Verdict == Allow) != tc.allow {
			t.Errorf("%s: Classify = %v", tc.what, c.Verdict)
		}
	}
}

// A session recorded outside the state directory has its record guarded the same way, in every mode.
func TestRecordDirsAreGuarded(t *testing.T) {
	root := t.TempDir()
	rec := filepath.Join(root, "runs", "s1")
	e, err := NewEngine(Config{Mode: ModeBypass, Root: root, Home: t.TempDir(), RecordDirs: []string{rec}})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Check(context.Background(), Request{Tool: "write", Writes: true, Paths: []string{filepath.Join(rec, "events.jsonl")}}); d.Allow {
		t.Errorf("a write to the session's record was allowed: %+v", d)
	}
	if d := e.Check(context.Background(), Request{Tool: "bash", Command: "mv x " + filepath.Join(rec, "events.jsonl"), Cwd: root}); d.Allow {
		t.Errorf("a move into the session's record was allowed: %+v", d)
	}
	if d := e.Check(context.Background(), Request{Tool: "write", Writes: true, Paths: []string{filepath.Join(root, "other.txt")}}); !d.Allow {
		t.Errorf("an ordinary write was refused: %+v", d)
	}
}
