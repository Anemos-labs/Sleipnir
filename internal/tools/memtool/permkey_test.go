package memtool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// The memory tool's permission request carries the call's input: two notes asked about
// at once are two questions, not one answer for both.
func TestPermissionRequestCarriesTheInput(t *testing.T) {
	tool := New(filepath.Join(t.TempDir(), "MEMORY.md"))
	rec := &recording{allow: true}
	for _, text := range []string{"note A", "note B"} {
		if r := run(t, tool, rec, map[string]any{"action": "add", "text": text}); r.IsError {
			t.Fatal(r.Text)
		}
	}
	if len(rec.seen) != 2 {
		t.Fatalf("requests: %+v", rec.seen)
	}
	for i, text := range []string{"note A", "note B"} {
		var in map[string]any
		if err := json.Unmarshal(rec.seen[i].Input, &in); err != nil || in["text"] != text {
			t.Fatalf("request %d input %q (%v)", i, rec.seen[i].Input, err)
		}
	}
}

// Under the session's real rules a note is saved without a question in every mode that asks (the protection of ~/.sleipnir is for
// what an agent writes there itself, not for the tool's own file), while the person's rules for the memory tool still decide: an
// ask rule puts each change to them, a deny rule refuses it.
func TestTheSessionsRulesSaveANoteWithoutAQuestion(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".sleipnir", "MEMORY.md")
	for _, c := range []struct {
		mode      perm.Mode
		ask, deny []string
		saved     bool
		asked     int
	}{
		{perm.ModeDefault, nil, nil, true, 0},
		{perm.ModeAcceptEdits, nil, nil, true, 0},
		{perm.ModeDefault, []string{"memory"}, nil, true, 1},
		{perm.ModeAcceptEdits, nil, []string{"memory"}, false, 0},
	} {
		asked := 0
		e, err := perm.NewEngine(perm.Config{Root: t.TempDir(), Home: home, Mode: c.mode, Ask: append([]string{"Edit(~/.sleipnir/**)"}, c.ask...), Deny: c.deny,
			Prompter: func(context.Context, perm.Request) perm.Decision { asked++; return perm.Decision{Allow: true} }})
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(path)
		r := run(t, New(path), e, map[string]any{"action": "add", "text": "uses pnpm"})
		if r.IsError == c.saved || asked != c.asked {
			t.Errorf("%s ask %v deny %v: %+v, asked %d", c.mode, c.ask, c.deny, r, asked)
		}
	}
}
