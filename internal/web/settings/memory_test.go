package settings

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/lore"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

const testLessons = `{"version":1,"lessons":[
{"id":"a1b2c3d4e5f6","kind":"strategy","text":"Write the CLI test first.","task":"a link checker","vec":[1,0],"uses":3,"wins":2,"losses":1,"created":"2026-10-01T10:00:00Z","last":"2026-10-02T10:00:00Z"},
{"id":"0f9e8d7c6b5a","kind":"pitfall","text":"Two dev servers on one port collide.","vec":[0,1],"uses":0,"wins":0,"losses":0,"created":"2026-10-01T10:00:00Z","last":"2026-10-01T10:00:00Z"}]}`

// memEnv is a server whose home has notes, an instruction file and lessons, and whose project has an AGENTS.md.
func memEnv(t *testing.T) *testEnv {
	return newEnv(t, func(home, repo string) {
		write(t, filepath.Join(home, ".sleipnir", "MEMORY.md"), "Notes the agent saved with its memory tool.\n- The user prefers pnpm.\n- CI runs Node 22.\n")
		write(t, filepath.Join(home, ".sleipnir", "SLEIPNIR.md"), "Answer in plain English.\n")
		write(t, filepath.Join(home, ".sleipnir", "lessons.json"), testLessons)
		write(t, filepath.Join(repo, "AGENTS.md"), "# Rules\n- Use Go.\n")
	})
}

// fileRow finds a place in a view.
func fileRow(t *testing.T, v wire.MemoryView, path string) wire.MemoryFile {
	t.Helper()
	for _, f := range v.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no %s among %+v", path, v.Files)
	return wire.MemoryFile{}
}

func TestMemoryViewListsNotesFilesAndLessons(t *testing.T) {
	e := memEnv(t)
	var v wire.MemoryView
	e.ok(http.MethodGet, "/api/sessions/t1/memory", nil, nil, &v)
	if strings.Join(v.Notes.Notes, "|") != "The user prefers pnpm.|CI runs Node 22." || v.Notes.Version == "" || !v.Notes.On || v.Notes.Path != "~/.sleipnir/MEMORY.md" {
		t.Errorf("notes = %+v", v.Notes)
	}
	if u := fileRow(t, v, "~/.sleipnir/SLEIPNIR.md"); !u.Exists || !u.Loaded || !u.Editable || u.Text != "Answer in plain English.\n" || u.Scope != "user" {
		t.Errorf("the user's file = %+v", u)
	}
	// the project is not trusted: its file is listed, editable, and not read
	if a := fileRow(t, v, "AGENTS.md"); !a.Exists || a.Loaded || !a.Editable || !strings.Contains(a.Why, "not trusted") {
		t.Errorf("AGENTS.md = %+v", a)
	}
	if c := fileRow(t, v, "CLAUDE.md"); c.Exists || c.Version != "" || !c.Editable || c.Scope != "project" {
		t.Errorf("a place with no file = %+v", c)
	}
	fileRow(t, v, "SLEIPNIR.local.md")
	for _, f := range v.Files {
		if strings.HasSuffix(f.Path, "MEMORY.md") {
			t.Errorf("the notes file is listed among the instruction files: %+v", f)
		}
	}
	if len(v.Lessons.Lessons) != 2 || v.Lessons.Lessons[0].ID != "a1b2c3d4e5f6" || v.Lessons.Lessons[0].Task != "a link checker" || v.Lessons.Lessons[0].Utility <= 0.5 {
		t.Errorf("lessons = %+v", v.Lessons)
	}
	if v.Trusted || v.Root == "" {
		t.Errorf("trusted %v, root %q", v.Trusted, v.Root)
	}
}

func TestMemoryViewOfATrustedProjectReadsItsFiles(t *testing.T) {
	e := memEnv(t)
	e.startSession(session.Options{TrustProject: true})
	var v wire.MemoryView
	e.ok(http.MethodGet, "/api/sessions/t1/memory", nil, nil, &v)
	if a := fileRow(t, v, "AGENTS.md"); !a.Loaded || a.Why != "" {
		t.Errorf("AGENTS.md of a trusted project = %+v", a)
	}
}

func TestMemoryNotesAreReplacedFromTheirVersion(t *testing.T) {
	e := memEnv(t)
	var v wire.MemoryView
	e.ok(http.MethodGet, "/api/sessions/t1/memory", nil, nil, &v)
	old := v.Notes.Version
	e.ok(http.MethodPut, "/api/sessions/t1/memory/notes", wire.MemoryNotesWrite{Notes: []string{"  The user   prefers pnpm\tand Bun. ", "", "CI runs Node 22.", "ci runs node 22."}, Version: old}, nil, &v)
	if strings.Join(v.Notes.Notes, "|") != "The user prefers pnpm and Bun.|CI runs Node 22." || v.Notes.Version == old {
		t.Fatalf("notes after the write = %+v", v.Notes)
	}
	b, _ := os.ReadFile(filepath.Join(e.home, ".sleipnir", "MEMORY.md"))
	if !strings.HasPrefix(string(b), "Notes the agent saved") || !strings.Contains(string(b), "\n- The user prefers pnpm and Bun.\n- CI runs Node 22.\n") {
		t.Errorf("MEMORY.md = %q", b)
	}
	if rec := e.do(http.MethodPut, "/api/sessions/t1/memory/notes", wire.MemoryNotesWrite{Notes: []string{"x"}, Version: old}, nil); rec.Code != http.StatusConflict || errCode(rec) != "changed" {
		t.Errorf("a write from a stale version: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodPut, "/api/sessions/t1/memory/notes", wire.MemoryNotesWrite{Notes: []string{strings.Repeat("a", 301)}, Version: v.Notes.Version}, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit is 300") {
		t.Errorf("a note over the limit: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMemoryFilesAreWrittenOnlyInTheirPlaces(t *testing.T) {
	e := memEnv(t)
	var v wire.MemoryView
	e.ok(http.MethodGet, "/api/sessions/t1/memory", nil, nil, &v)
	agents := fileRow(t, v, "AGENTS.md")
	e.ok(http.MethodPut, "/api/sessions/t1/memory/file", wire.MemoryFileWrite{Path: "AGENTS.md", Text: "# Rules\n- Use Go 1.25.\n", Version: agents.Version}, nil, &v)
	if b, _ := os.ReadFile(filepath.Join(e.repo, "AGENTS.md")); string(b) != "# Rules\n- Use Go 1.25.\n" {
		t.Errorf("AGENTS.md = %q", b)
	}
	if rec := e.do(http.MethodPut, "/api/sessions/t1/memory/file", wire.MemoryFileWrite{Path: "AGENTS.md", Text: "lost", Version: agents.Version}, nil); rec.Code != http.StatusConflict || errCode(rec) != "changed" {
		t.Errorf("a write from a stale version: %d %s", rec.Code, rec.Body.String())
	}
	// a place with no file yet is created with its folder
	e.ok(http.MethodPut, "/api/sessions/t1/memory/file", wire.MemoryFileWrite{Path: ".sleipnir/SLEIPNIR.md", Text: "Use conventional commits.\n"}, nil, &v)
	if b, _ := os.ReadFile(filepath.Join(e.repo, ".sleipnir", "SLEIPNIR.md")); string(b) != "Use conventional commits.\n" {
		t.Errorf(".sleipnir/SLEIPNIR.md = %q", b)
	}
	e.ok(http.MethodPut, "/api/memory/file", wire.MemoryFileWrite{Path: "~/.sleipnir/SLEIPNIR.md", Text: "Be brief.\n", Version: fileRow(t, v, "~/.sleipnir/SLEIPNIR.md").Version}, nil, &v)
	if b, _ := os.ReadFile(filepath.Join(e.home, ".sleipnir", "SLEIPNIR.md")); string(b) != "Be brief.\n" {
		t.Errorf("the user's file = %q", b)
	}
	for _, p := range []string{"README.md", "../AGENTS.md", "/etc/passwd", "~/.sleipnir/MEMORY.md", "~/.sleipnir/config.json", ""} {
		if rec := e.do(http.MethodPut, "/api/sessions/t1/memory/file", wire.MemoryFileWrite{Path: p, Text: "x"}, nil); rec.Code != http.StatusBadRequest || errCode(rec) != "bad_path" {
			t.Errorf("%q: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do(http.MethodPut, "/api/sessions/t1/memory/file", wire.MemoryFileWrite{Path: "CLAUDE.md", Text: "a\x00b"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("text with a NUL: %d %s", rec.Code, rec.Body.String())
	}
	// the project's AGENTS.md that is a link is shown, not written: the write would land wherever the link leads
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	write(t, outside, "elsewhere\n")
	if err := os.Remove(filepath.Join(e.repo, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.repo, "AGENTS.md")); err != nil {
		t.Skip(err)
	}
	e.ok(http.MethodGet, "/api/sessions/t1/memory", nil, nil, &v)
	if a := fileRow(t, v, "AGENTS.md"); a.Editable || !a.Exists || a.Text != "" {
		t.Errorf("a linked AGENTS.md = %+v", a)
	}
	if rec := e.do(http.MethodPut, "/api/sessions/t1/memory/file", wire.MemoryFileWrite{Path: "AGENTS.md", Text: "x"}, nil); rec.Code != http.StatusBadRequest || errCode(rec) != "bad_path" {
		t.Errorf("a linked AGENTS.md written: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(outside); string(b) != "elsewhere\n" {
		t.Errorf("the link's target changed: %q", b)
	}
}

func TestMemoryLessonsCanBeRemoved(t *testing.T) {
	e := memEnv(t)
	var v wire.MemoryView
	e.ok(http.MethodDelete, "/api/sessions/t1/memory/lessons/0f9e8d7c6b5a", nil, nil, &v)
	if len(v.Lessons.Lessons) != 1 || v.Lessons.Lessons[0].ID != "a1b2c3d4e5f6" {
		t.Fatalf("lessons after the removal = %+v", v.Lessons.Lessons)
	}
	if rec := e.do(http.MethodDelete, "/api/sessions/t1/memory/lessons/0f9e8d7c6b5a", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("removing it again: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodDelete, "/api/sessions/t1/memory/lessons/NOT-AN-ID", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMemoryWithoutATabShowsOnlyTheUsersFiles(t *testing.T) {
	e := memEnv(t)
	var v wire.MemoryView
	e.ok(http.MethodGet, "/api/memory", nil, nil, &v)
	if len(v.Files) != 1 || v.Files[0].Path != "~/.sleipnir/SLEIPNIR.md" || v.Root != "" || len(v.Notes.Notes) != 2 || v.Project != nil || v.Lore != nil {
		t.Errorf("the view without a tab = %+v", v)
	}
	if rec := e.do(http.MethodPut, "/api/memory/file", wire.MemoryFileWrite{Path: "AGENTS.md", Text: "x"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a project file without a tab: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/sessions/nope/memory", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown tab: %d", rec.Code)
	}
}

// The project's own notes and its team's lore by role are on the page: the notes are edited like the user's (scope project), with the
// .gitignore that keeps them out of git; the lore is read from the state directory, each role in name order.
func TestMemoryViewShowsTheProjectsNotesAndLore(t *testing.T) {
	e := memEnv(t)
	write(t, filepath.Join(e.repo, ".sleipnir", "MEMORY.local.md"), "- make gen regenerates the client.\n")
	dir := lore.ProjectDir(session.StateRoot(e.home), e.repo)
	write(t, filepath.Join(dir, "reviewer.md"), "# What the reviewer role learned in this project\n\n- [canon] Money is integer cents.\n- [insight ×2] Run make check before approving.\n")
	write(t, filepath.Join(dir, "backend.md"), "- [insight] The API client is generated.\n")
	var v wire.MemoryView
	e.ok(http.MethodGet, "/api/sessions/t1/memory", nil, nil, &v)
	if v.Project == nil || strings.Join(v.Project.Notes, "|") != "make gen regenerates the client." || v.Project.Path != ".sleipnir/MEMORY.local.md" || v.Project.Version == "" {
		t.Fatalf("project notes = %+v", v.Project)
	}
	if v.Lore == nil || len(v.Lore.Roles) != 2 || v.Lore.Roles[0].Role != "backend" || v.Lore.Roles[1].Role != "reviewer" {
		t.Fatalf("lore = %+v", v.Lore)
	}
	if it := v.Lore.Roles[1].Items; len(it) != 2 || it[0].Stage != "canon" || it[1].Confirmed != 2 || it[1].Text != "Run make check before approving." {
		t.Errorf("reviewer lore = %+v", it)
	}
	e.ok(http.MethodPut, "/api/sessions/t1/memory/notes", wire.MemoryNotesWrite{Scope: "project", Notes: []string{"make gen regenerates the client.", "Tests need Docker."}, Version: v.Project.Version}, nil, &v)
	if strings.Join(v.Project.Notes, "|") != "make gen regenerates the client.|Tests need Docker." || len(v.Notes.Notes) != 2 {
		t.Errorf("after the edit: project %+v, user %+v", v.Project, v.Notes)
	}
	if b, err := os.ReadFile(filepath.Join(e.repo, ".sleipnir", ".gitignore")); err != nil || !strings.Contains(string(b), "MEMORY.local.md") {
		t.Errorf(".gitignore: %q %v", b, err)
	}
	if rec := e.do(http.MethodPut, "/api/sessions/t1/memory/notes", wire.MemoryNotesWrite{Scope: "team", Notes: []string{"x"}}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown scope: %d", rec.Code)
	}
	if rec := e.do(http.MethodPut, "/api/memory/notes", wire.MemoryNotesWrite{Scope: "project", Notes: []string{"x"}}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("project notes without a tab: %d", rec.Code)
	}
}
