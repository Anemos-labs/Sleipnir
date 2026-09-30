package perm

import (
	"context"
	"strings"
	"testing"
)

// A model that invented a path ("/root/repo", "/workspace") is refused; what the refusal says is the only thing that tells
// it where it actually is, so it names the workspace. Without this the real-model pilots kept guessing: one model asked
// for nine invented paths in a row.
func TestRefusalOutsideTheWorkspaceNamesTheWorkspace(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		mode Mode
		req  rq
		ask  string // the start of the sentence that must precede the workspace
	}{
		{"read", ModeDefault, read("{out}/secret.txt"), "reads "},
		{"read in accept-edits", ModeAcceptEdits, read("{out}/secret.txt"), "reads "},
		{"write in accept-edits", ModeAcceptEdits, write("{out}/new.txt"), "writes "},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			e := f.engine(t, Config{Mode: c.mode})
			d := e.Check(context.Background(), f.request(c.req))
			if d.Allow {
				t.Fatalf("allowed: %s", d.Reason)
			}
			want := "outside the workspace (" + f.root + ")"
			if !strings.Contains(d.Reason, want) || !strings.Contains(d.Reason, c.ask) {
				t.Errorf("reason %q does not say %q", d.Reason, c.ask+"… "+want)
			}
		})
	}
}

// Without a configured root there is nothing to name, and the sentence stays as it was.
func TestRefusalWithoutAWorkspaceDoesNotInventOne(t *testing.T) {
	f := newFixture(t)
	e, err := NewEngine(Config{Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Check(context.Background(), f.request(read("{out}/secret.txt")))
	if d.Allow || strings.Contains(d.Reason, "()") {
		t.Errorf("allow=%v reason=%q", d.Allow, d.Reason)
	}
}
