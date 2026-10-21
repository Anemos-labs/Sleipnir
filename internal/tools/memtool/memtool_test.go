package memtool

import (
	"context"
	"encoding/json"
	"fmt"
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

// A session without the global memory keeps the tool (the tool list never depends on the session) but reads and writes nothing:
// every action says the memory is off and asks no permission question.
func TestTheToolOfASessionWithoutMemoryTouchesNothing(t *testing.T) {
	ask := &recording{allow: true}
	for _, in := range []map[string]any{{"action": "list"}, {"action": "add", "text": "a note"}, {"action": "remove", "text": "note"}} {
		if r := run(t, Off(), ask, in); r.IsError || !strings.Contains(r.Text, "--no-memory") {
			t.Errorf("%v: %+v", in, r)
		}
	}
	if len(ask.seen) != 0 {
		t.Errorf("asked %d permission questions", len(ask.seen))
	}
	if Off().Spec().Name != New("x").Spec().Name || string(Off().Spec().InputSchema) != string(New("x").Spec().InputSchema) {
		t.Error("the tool's spec differs when the memory is off")
	}
}

// Project notes live in the project, are git-ignored beside it, say so when asked, and are listed apart from the user's.
func TestProjectNotesAreKeptApartAndIgnoredByGit(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "home", "MEMORY.md")
	proj := filepath.Join(dir, "repo", ".sleipnir", "MEMORY.local.md")
	tool := NewScoped(user, proj)
	allow := &recording{allow: true}
	if r := run(t, tool, allow, map[string]any{"action": "add", "text": "Prefers short answers."}); r.IsError {
		t.Fatal(r.Text)
	}
	r := run(t, tool, allow, map[string]any{"action": "add", "scope": "project", "text": "make gen regenerates the client."})
	if r.IsError || !strings.Contains(r.Text, "for this project") {
		t.Fatalf("project add: %+v", r)
	}
	if q := allow.seen[len(allow.seen)-1]; !strings.HasPrefix(q.Summary, "remember for this project: ") || q.Paths[0] != proj {
		t.Errorf("approval asked for %+v", q)
	}
	b, _ := os.ReadFile(proj)
	if !strings.Contains(string(b), "- make gen regenerates the client.") {
		t.Errorf("project file:\n%s", b)
	}
	if b, _ := os.ReadFile(user); strings.Contains(string(b), "make gen") {
		t.Error("a project note reached the user's file")
	}
	gi, _ := os.ReadFile(filepath.Join(dir, "repo", ".sleipnir", ".gitignore"))
	if string(gi) != "MEMORY.local.md\n" {
		t.Errorf(".gitignore = %q", gi)
	}
	run(t, tool, allow, map[string]any{"action": "add", "scope": "project", "text": "Second fact."})
	if gi2, _ := os.ReadFile(filepath.Join(dir, "repo", ".sleipnir", ".gitignore")); string(gi2) != string(gi) {
		t.Errorf("the .gitignore line was repeated: %q", gi2)
	}
	all := run(t, tool, allow, map[string]any{"action": "list"}).Text
	if !strings.Contains(all, "user notes (every project):\n1. Prefers short answers.") || !strings.Contains(all, "project notes (this project):\n1. make gen") {
		t.Errorf("list:\n%s", all)
	}
	if r := run(t, tool, allow, map[string]any{"action": "remove", "scope": "project", "text": "second"}); r.IsError {
		t.Errorf("project remove: %s", r.Text)
	}
	if r := run(t, New(user), allow, map[string]any{"action": "add", "scope": "project", "text": "x"}); !r.IsError || !strings.Contains(r.Text, "no project") {
		t.Errorf("project scope without a project: %+v", r)
	}
	if r := run(t, tool, allow, map[string]any{"action": "add", "scope": "team", "text": "x"}); !r.IsError {
		t.Errorf("unknown scope accepted: %+v", r)
	}
}

func TestIgnoreBesideKeepsAnExistingGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("config.local.json"), 0o644)
	if err := ignoreBeside(filepath.Join(dir, "MEMORY.local.md")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(b) != "config.local.json\nMEMORY.local.md\n" {
		t.Errorf(".gitignore = %q", b)
	}
}

// CheckNotes keeps notes as the tool would: one line each, repeats once, and names the limit a list passes.
func TestCheckNotesCleansAndHoldsTheLimits(t *testing.T) {
	got, err := CheckNotes([]string{" a  b\tc ", "", "A B C", "x\x07y"})
	if err != nil || strings.Join(got, "|") != "a b c|xy" {
		t.Fatalf("CheckNotes = %q, %v", got, err)
	}
	if _, err := CheckNotes([]string{strings.Repeat("n", MaxNoteRunes+1)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("a long note: %v", err)
	}
	many := make([]string, MaxNotes+1)
	for i := range many {
		many[i] = fmt.Sprintf("note %d", i)
	}
	if _, err := CheckNotes(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many notes: %v", err)
	}
}

// An edit of a file that is already past the limits (written by hand) may remove or correct notes: only what the edit adds or grows is
// held to them.
func TestCheckEditLetsAnOverfullFileShrink(t *testing.T) {
	long := strings.Repeat("l", MaxNoteRunes+50)
	prev := []string{long}
	for i := 0; i < MaxNotes+10; i++ {
		prev = append(prev, fmt.Sprintf("note %d", i))
	}
	if _, err := CheckEdit(prev, prev[:len(prev)-1]); err != nil {
		t.Errorf("removing one note of an overfull file: %v", err)
	}
	if _, err := CheckEdit(prev, append(append([]string(nil), prev...), "one more")); err == nil {
		t.Error("an overfull file took one more note")
	}
	if _, err := CheckEdit(prev, []string{long + "x"}); err == nil {
		t.Error("a new note over the limit was taken")
	}
}

// A file edited by hand keeps its other lines when the notes are written: headings and text stay where they were, the notes take the
// place of the old ones.
func TestWriteNotesKeepsTheLinesWrittenByHand(t *testing.T) {
	p := filepath.Join(t.TempDir(), "MEMORY.md")
	hand := "# My notes\n\nKeep this paragraph.\n- old one\n- old two\n\n## Later\nmore text\n"
	if err := os.WriteFile(p, []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteNotes(p, []string{"new one"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if want := "# My notes\n\nKeep this paragraph.\n- new one\n\n## Later\nmore text\n"; string(b) != want {
		t.Errorf("file = %q, want %q", b, want)
	}
	if err := WriteNotes(p, nil); err != nil {
		t.Fatal(err)
	}
	if err := WriteNotes(p, []string{"back"}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if want := "# My notes\n\nKeep this paragraph.\n\n## Later\nmore text\n- back\n"; string(b) != want {
		t.Errorf("after removing every note and adding one: %q, want %q", b, want)
	}
}
