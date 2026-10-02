package memtool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

type recording struct {
	seen  []perm.Request
	allow bool
}

func (r *recording) Check(_ context.Context, q perm.Request) perm.Decision {
	r.seen = append(r.seen, q)
	return perm.Decision{Allow: r.allow, Reason: "no"}
}

func run(t *testing.T, tool *Tool, p perm.Requester, in map[string]any) *tools.Result {
	t.Helper()
	b, _ := json.Marshal(in)
	res, err := tool.Run(context.Background(), &tools.Call{Name: "memory", Input: b, Env: &tools.Env{Perm: p}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAddListRemoveRoundTripThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "MEMORY.md")
	tool := New(path)
	allow := &recording{allow: true}
	if r := run(t, tool, allow, map[string]any{"action": "list"}); r.IsError || r.Text != "no notes yet" {
		t.Fatalf("empty: %+v", r)
	}
	for _, n := range []string{"The user prefers table-driven tests.", "Their CI is GitHub Actions;\n\tdo not suggest Jenkins."} {
		if r := run(t, tool, allow, map[string]any{"action": "add", "text": n}); r.IsError {
			t.Fatalf("add: %s", r.Text)
		}
	}
	b, _ := os.ReadFile(path)
	want := header + "\n- The user prefers table-driven tests.\n- Their CI is GitHub Actions; do not suggest Jenkins.\n"
	if string(b) != want {
		t.Errorf("file:\n%q\nwant\n%q", b, want)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Error(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows keeps no POSIX modes
		t.Errorf("mode %v: the notes are the user's, not the world's", fi.Mode().Perm())
	}
	if r := run(t, tool, allow, map[string]any{"action": "add", "text": "the user prefers TABLE-driven tests."}); r.Text != "already saved" {
		t.Errorf("a duplicate is not saved twice: %+v", r)
	}
	if r := run(t, tool, allow, map[string]any{"action": "remove", "text": "jenkins"}); r.IsError || !strings.Contains(r.Text, "removed 1") {
		t.Fatalf("remove: %+v", r)
	}
	if r := run(t, tool, allow, map[string]any{"action": "list"}); r.Text != "1. The user prefers table-driven tests." {
		t.Errorf("list: %q", r.Text)
	}
	// Every change asked the permission engine, with the note itself in the summary and the file as its path.
	if len(allow.seen) != 3 || !strings.Contains(allow.seen[0].Summary, "table-driven") || !allow.seen[0].Writes || allow.seen[0].Paths[0] != path ||
		!strings.HasPrefix(allow.seen[2].Summary, "forget: ") {
		t.Errorf("permission requests: %+v", allow.seen)
	}
}

func TestADeniedChangeChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MEMORY.md")
	tool := New(path)
	r := run(t, tool, &recording{}, map[string]any{"action": "add", "text": "a note"})
	if !r.IsError || !strings.Contains(r.Text, "permission denied") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the file exists though the person said no")
	}
}

func TestLimitsAreInBandAndNeverSilent(t *testing.T) {
	tool := New(filepath.Join(t.TempDir(), "MEMORY.md"))
	allow := &recording{allow: true}
	if r := run(t, tool, allow, map[string]any{"action": "add", "text": strings.Repeat("x", MaxNoteRunes+1)}); !r.IsError || !strings.Contains(r.Text, "limit is 300") {
		t.Errorf("a long note: %+v", r)
	}
	if r := run(t, tool, allow, map[string]any{"action": "add", "text": "  \n "}); !r.IsError {
		t.Errorf("an empty note: %+v", r)
	}
	for i := 0; i < MaxNotes; i++ {
		if r := run(t, tool, allow, map[string]any{"action": "add", "text": "note number " + string(rune('A'+i/26)) + string(rune('a'+i%26))}); r.IsError {
			t.Fatalf("note %d: %s", i, r.Text)
		}
	}
	r := run(t, tool, allow, map[string]any{"action": "add", "text": "one too many"})
	if !r.IsError || !strings.Contains(r.Text, "memory is full") || !strings.Contains(r.Text, "1. note number Aa") || !strings.Contains(r.Text, "40. ") {
		t.Errorf("a full memory lists its notes so the model can merge: %.200s", r.Text)
	}
	if r := run(t, tool, allow, map[string]any{"action": "remove", "text": "note number"}); !r.IsError || !strings.Contains(r.Text, "matches every note") {
		t.Errorf("a remove that would empty the memory by a vague word: %.120s", r.Text)
	}
	if r := run(t, tool, allow, map[string]any{"action": "remove", "text": "no"}); !r.IsError {
		t.Errorf("a two-letter match: %+v", r)
	}
	if r := run(t, tool, allow, map[string]any{"action": "remove", "text": "note number Ab"}); r.IsError || !strings.Contains(r.Text, "removed 1") {
		t.Errorf("a precise remove: %+v", r)
	}
}
