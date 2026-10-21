package settings

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/lessons"
	"github.com/anemos-labs/sleipnir/internal/lore"
	"github.com/anemos-labs/sleipnir/internal/memory"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools/memtool"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The Memory page: what the agents carry from one session to the next, and the person's way to read and change it.
//
//   - the notes of the memory tool (~/.sleipnir/MEMORY.md), replaced as a whole list under the tool's own limits;
//   - the instruction files (~/.sleipnir/SLEIPNIR.md, the project's AGENTS.md, CLAUDE.md, SLEIPNIR.md and the local files), each
//     place memory.Load reads, whether a file is there or not, and the files they import (read-only);
//   - the lessons of earlier goals (~/.sleipnir/lessons.json), which can be removed (a lesson's text is the key of its embedding,
//     so it is not edited in place).
//
// A write names the version (a hash of the bytes) it was edited from and is refused with 409 changed when the file moved on since,
// so an agent's note saved in the meantime is not lost. A project file is written inside the project root only (os.Root: a link
// that leaves it fails), and a project file that is itself a link is not written. Only the places memory.Files lists are written.

// memoryLessonRe is the shape of a lesson id (lessons.Lesson.ID).
var memoryLessonRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// registerMemory adds the routes of the Memory page: under a tab (its project's files and the home of its session), and under
// /api/memory for a page with no tab yet (the user's own files only, in the server's home).
func (s *service) registerMemory() {
	for _, pre := range []string{"/api/sessions/{id}/memory", "/api/memory"} {
		s.srv.HandleFunc("GET "+pre, s.handleMemory, web.RouteOpts{})
		s.srv.HandleFunc("PUT "+pre+"/notes", s.handleMemoryNotes, web.RouteOpts{MaxBody: 64 << 10})
		s.srv.HandleFunc("PUT "+pre+"/file", s.handleMemoryFile, web.RouteOpts{MaxBody: memory.MaxFileBytes + 16<<10})
		s.srv.HandleFunc("DELETE "+pre+"/lessons/{lesson}", s.handleMemoryLesson, web.RouteOpts{NoBody: true})
	}
}

// handleMemory is GET /api/sessions/{id}/memory.
func (s *service) handleMemory(w http.ResponseWriter, r *http.Request) {
	tc, err := s.memTab(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, s.memoryView(tc), nil)
}

// handleMemoryNotes is PUT /api/sessions/{id}/memory/notes: the whole list of notes, from the version the page read.
func (s *service) handleMemoryNotes(w http.ResponseWriter, r *http.Request) {
	tc, err := s.memTab(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var body wire.MemoryNotesWrite
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	p, write := notesPath(s.homeOf(tc)), memtool.WriteNotes
	switch body.Scope {
	case "", "user":
	case "project":
		pp, why := projectNotesPath(tc)
		if why != "" {
			reply(w, nil, fail(http.StatusBadRequest, "bad_path", why))
			return
		}
		p, write = pp, memtool.WriteProjectNotes
	default:
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", `scope is "user" or "project"`))
		return
	}
	unlock := config.WriteLock(p)
	defer unlock()
	v, ok := fileVersion(os.ReadFile(p))
	if !ok {
		reply(w, nil, fail(http.StatusInternalServerError, "internal", "the notes file cannot be read"))
		return
	}
	if v != body.Version {
		reply(w, nil, fail(http.StatusConflict, "changed", "the notes changed since the page read them: read them again"))
		return
	}
	prev, _ := memtool.ReadNotes(p) // what the file breaks already does not stop an edit that leaves it no worse
	notes, err := memtool.CheckEdit(prev, body.Notes)
	if err != nil {
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", err.Error()))
		return
	}
	if err := write(p, notes); err != nil {
		reply(w, nil, fail(http.StatusInternalServerError, "internal", "the notes were not saved"))
		return
	}
	reply(w, s.memoryView(tc), nil)
}

// handleMemoryFile is PUT /api/sessions/{id}/memory/file: one instruction file, created when it is not there yet.
func (s *service) handleMemoryFile(w http.ResponseWriter, r *http.Request) {
	tc, err := s.memTab(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var body wire.MemoryFileWrite
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	switch {
	case len(body.Text) > memory.MaxFileBytes:
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", "the text is over 64 KB, the most an instruction file is read for"))
		return
	case !utf8.ValidString(body.Text) || strings.ContainsRune(body.Text, 0):
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", "the text is not plain UTF-8 text"))
		return
	}
	f, ok := s.memoryFile(tc, body.Path)
	if !ok {
		reply(w, nil, fail(http.StatusBadRequest, "bad_path", "that is not a place instruction files are read from"))
		return
	}
	unlock := config.WriteLock(f.Abs())
	defer unlock()
	cur := readMemoryFile(f)
	switch {
	case cur.why != "" && cur.exists:
		reply(w, nil, fail(http.StatusBadRequest, "bad_path", "this file is not edited here: "+cur.why))
		return
	case cur.version != body.Version:
		reply(w, nil, fail(http.StatusConflict, "changed", "the file changed since the page read it: read it again"))
		return
	}
	if err := writeMemoryFile(f, cur, body.Text); err != nil {
		reply(w, nil, fail(http.StatusInternalServerError, "internal", "the file was not saved"))
		return
	}
	reply(w, s.memoryView(tc), nil)
}

// handleMemoryLesson is DELETE /api/sessions/{id}/memory/lessons/{lesson}.
func (s *service) handleMemoryLesson(w http.ResponseWriter, r *http.Request) {
	tc, err := s.memTab(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	id := r.PathValue("lesson")
	if !memoryLessonRe.MatchString(id) {
		reply(w, nil, fail(http.StatusBadRequest, "bad_request", "the lesson id is not valid"))
		return
	}
	found, err := lessons.NewStore(lessonsPath(s.homeOf(tc))).Remove(id)
	switch {
	case err != nil:
		reply(w, nil, fail(http.StatusInternalServerError, "internal", "the lesson was not removed"))
		return
	case !found:
		reply(w, nil, fail(http.StatusNotFound, "not_found", "there is no such lesson"))
		return
	}
	reply(w, s.memoryView(tc), nil)
}

// memTab is the tab of a request under /api/sessions/{id}/memory, or no tab (no project) for one under /api/memory.
func (s *service) memTab(r *http.Request) (*tabCtx, error) {
	if r.PathValue("id") == "" {
		return &tabCtx{}, nil
	}
	return s.tabOf(r)
}

// memOpts are the discovery options of a tab: its project, or the user's files alone when there is none.
func (s *service) memOpts(tc *tabCtx) memory.Opts {
	return memory.Opts{Root: tc.root, Cwd: tc.cwd, Home: s.homeOf(tc), NoNotes: true, NoProject: tc.root == "" && tc.cwd == ""}
}

// notesPath is the memory tool's file under home, as the session gives it to the tool.
func notesPath(home string) string { return filepath.Join(home, ".sleipnir", "MEMORY.md") }

// projectNotesPath is the memory tool's file of the tab's project (memory.ProjectNotes under its root), or why the page does not
// write it: there is no project, or .sleipnir, the file or the .gitignore beside it is a link (memtool.CheckProjectPath).
func projectNotesPath(tc *tabCtx) (string, string) {
	if tc.root == "" {
		return "", "this page has no project: only your own notes are here"
	}
	p := filepath.Join(tc.root, filepath.FromSlash(memory.ProjectNotes))
	if err := memtool.CheckProjectPath(p); err != nil {
		return "", "the project's notes are not edited here: " + err.Error()
	}
	return p, ""
}

// lessonsPath is the lesson memory's file of home, as the session opens it.
func lessonsPath(home string) string { return filepath.Join(session.StateRoot(home), "lessons.json") }

// memoryView reads everything the Memory page shows for a tab.
func (s *service) memoryView(tc *tabCtx) wire.MemoryView {
	home := s.homeOf(tc)
	on := func(string) bool { return true }
	if set, _, _, err := plugin.LoadSet(tc.cwd, tc.root, home, tc.trusted); err == nil {
		on = set.On
	}
	v := wire.MemoryView{Root: s.display(tc.root), Trusted: tc.trusted, Files: []wire.MemoryFile{}}

	np := notesPath(home)
	raw, rerr := os.ReadFile(np)
	notes, _ := memtool.ReadNotes(np)
	v.Notes = wire.MemoryNotes{Path: s.display(np), Notes: []string{}, On: on(plugin.MemoryTool), Max: memtool.MaxNotes, MaxLen: memtool.MaxNoteRunes}
	v.Notes.Version, _ = fileVersion(raw, rerr)
	for _, n := range notes {
		v.Notes.Notes = append(v.Notes.Notes, provider.SanitizeText(n, memtool.MaxNoteRunes*4))
	}

	if tc.root != "" {
		pp := filepath.Join(tc.root, filepath.FromSlash(memory.ProjectNotes))
		raw, rerr := os.ReadFile(pp)
		notes, _ := memtool.ReadNotes(pp)
		pn := &wire.MemoryNotes{Path: filepath.ToSlash(memory.ProjectNotes), Notes: []string{}, On: on(plugin.MemoryTool), Max: memtool.MaxNotes, MaxLen: memtool.MaxNoteRunes}
		pn.Version, _ = fileVersion(raw, rerr)
		for _, n := range notes {
			pn.Notes = append(pn.Notes, provider.SanitizeText(n, memtool.MaxNoteRunes*4))
		}
		v.Project = pn
		v.Lore = s.memoryLore(home, tc.root, on(plugin.MemoryTool))
	}

	v.Files = s.memoryFiles(tc, on(plugin.Instructions))

	lp := lessonsPath(home)
	v.Lessons = wire.MemoryLessons{Path: s.display(lp), On: on(plugin.Lessons), Lessons: []wire.MemoryLesson{}}
	ls, err := lessons.NewStore(lp).List()
	if err != nil {
		v.Lessons.Error = "the lessons file could not be read"
	}
	for _, l := range ls {
		v.Lessons.Lessons = append(v.Lessons.Lessons, wire.MemoryLesson{
			ID: l.ID, Kind: l.Kind, Text: provider.SanitizeText(l.Text, lessons.MaxTextRunes*4), Task: provider.SanitizeText(l.Task, lessons.MaxTaskRunes*4),
			Uses: l.Uses, Wins: l.Wins, Losses: l.Losses, Utility: l.Utility(), Created: rfc3339(l.Created), Last: rfc3339(l.Last),
		})
	}
	return v
}

// memoryLore reads the project's lore by role (internal/lore), each role's file in name order.
func (s *service) memoryLore(home, root string, on bool) *wire.MemoryLore {
	dir := lore.ProjectDir(session.StateRoot(home), root)
	l := &wire.MemoryLore{Dir: s.display(dir), On: on, Roles: []wire.MemoryLoreRole{}}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			l.Error = "the lore folder could not be read"
		}
		return l
	}
	st := lore.New(dir)
	for _, e := range ents { // ReadDir sorts by name
		role, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		items, err := st.Load(role)
		r := wire.MemoryLoreRole{Role: role, Path: s.display(filepath.Join(dir, e.Name())), Items: []wire.MemoryLoreItem{}}
		if err != nil {
			r.Error = "the file could not be read"
		}
		for _, it := range items {
			r.Items = append(r.Items, wire.MemoryLoreItem{Stage: it.Stage, Confirmed: it.Confirmed, Text: provider.SanitizeText(it.Text, lore.MaxTextRunes*4)})
		}
		l.Roles = append(l.Roles, r)
	}
	return l
}

// memoryFiles lists the places instruction files are read from for the tab, each with its text and whether a session started now
// reads it, then the files those import.
func (s *service) memoryFiles(tc *tabCtx, pluginOn bool) []wire.MemoryFile {
	out := []wire.MemoryFile{}
	opts := s.memOpts(tc)
	files, err := memory.Files(opts)
	if err != nil {
		return out
	}
	srcs, _ := memory.Load(opts)
	loaded := map[string]memory.Source{}
	for _, src := range srcs {
		if tc.trusted || src.FromUser() {
			loaded[src.Path] = src
		}
	}
	for _, f := range files {
		cur := readMemoryFile(f)
		row := wire.MemoryFile{Path: f.Path, Scope: f.Scope, Exists: cur.exists, Text: cur.text, Version: cur.version, Editable: cur.why == "", Why: cur.why}
		if src, ok := loaded[f.Path]; ok && pluginOn {
			row.Loaded, row.Truncated = true, src.Truncated
		} else if cur.exists && row.Why == "" {
			switch {
			case !pluginOn:
				row.Why = "the Instruction files plugin is off: sessions read no instruction file"
			case !f.User && !tc.trusted:
				row.Why = "this project is not trusted: its sessions do not read the project's own files"
			default:
				row.Why = "not in the prompt: it is empty or only comments"
			}
		}
		out = append(out, row)
	}
	for _, src := range srcs {
		if src.Scope != memory.ScopeImport || !(tc.trusted || src.FromUser()) {
			continue
		}
		out = append(out, wire.MemoryFile{Path: src.Path, Scope: src.Scope, Exists: true, Loaded: pluginOn, Text: src.Text, Truncated: src.Truncated,
			Why: "imported by an instruction file (an @path line): edit it in its own place"})
	}
	return out
}

// memoryFile finds the place p (as memory.Files shows it) among the tab's instruction files.
func (s *service) memoryFile(tc *tabCtx, p string) (memory.File, bool) {
	if p == "" || len(p) > 1024 {
		return memory.File{}, false
	}
	files, err := memory.Files(s.memOpts(tc))
	if err != nil {
		return memory.File{}, false
	}
	for _, f := range files {
		if f.Path == p {
			return f, true
		}
	}
	return memory.File{}, false
}

// memFile is an instruction file as it is on disk: whether something is there, its text and version, its mode and whether it is a
// link, and why it is not edited from the page ("" when it is).
type memFile struct {
	exists  bool
	text    string
	version string
	mode    fs.FileMode
	link    bool
	why     string
}

// readMemoryFile reads one place of memory.Files. A project file is read inside the project root; the user's may be a link (a
// dotfiles checkout). Only a regular file is opened, so a FIFO under an instruction file's name cannot hold the request.
func readMemoryFile(f memory.File) memFile {
	var m memFile
	var fi fs.FileInfo
	var err error
	var open func() (io.ReadCloser, error)
	if f.User {
		abs := f.Abs()
		if fi, err = os.Lstat(abs); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			m.link = true
			fi, err = os.Stat(abs)
		}
		open = func() (io.ReadCloser, error) { return os.Open(abs) }
	} else {
		root, rerr := os.OpenRoot(f.Dir)
		if rerr != nil {
			m.why = "the project folder cannot be opened"
			return m
		}
		defer root.Close()
		rel := filepath.FromSlash(f.Rel)
		if fi, err = root.Lstat(rel); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			m.exists, m.link = true, true
			m.why = "it is a link: edit the file it leads to"
			return m
		}
		open = func() (io.ReadCloser, error) { return root.Open(rel) }
	}
	if errors.Is(err, fs.ErrNotExist) {
		return m
	}
	m.exists = true
	if err != nil || !fi.Mode().IsRegular() {
		m.why = "it is not a regular file"
		return m
	}
	m.mode = fi.Mode().Perm()
	rc, err := open()
	if err != nil {
		m.why = "it cannot be read"
		return m
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, memory.MaxFileBytes+1))
	if err != nil {
		m.why = "it cannot be read"
		return m
	}
	m.version, _ = fileVersion(b, nil)
	switch {
	case len(b) > memory.MaxFileBytes:
		m.text = strings.ToValidUTF8(string(b[:memory.MaxFileBytes]), "�")
		m.why = "it is over 64 KB: edit it in an editor"
	case !utf8.Valid(b) || strings.ContainsRune(string(b), 0):
		m.why = "it is not plain text"
	default:
		m.text = string(b)
	}
	return m
}

// writeMemoryFile replaces f's text, or creates the file with its folder. A regular file is replaced atomically and keeps its
// mode; the user's file that is a link is written in place, so the link stays and its target changes.
func writeMemoryFile(f memory.File, cur memFile, text string) error {
	mode := cur.mode
	if mode == 0 {
		mode = 0o644
		if f.User {
			mode = 0o600
		}
	}
	if f.User {
		abs := f.Abs()
		if cur.link {
			fh, err := os.OpenFile(abs, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(fh, text); err != nil {
				fh.Close()
				return err
			}
			return fh.Close()
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return err
		}
		root, err := os.OpenRoot(f.Dir)
		if err != nil {
			return err
		}
		defer root.Close()
		return replaceIn(root, f.Rel, text, mode)
	}
	root, err := os.OpenRoot(f.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if d := path.Dir(f.Rel); d != "." {
		if err := root.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			return err
		}
	}
	return replaceIn(root, f.Rel, text, mode)
}

// replaceIn writes text to rel inside root through a temporary file beside it and a rename.
func replaceIn(root *os.Root, rel, text string, mode fs.FileMode) error {
	rel = filepath.FromSlash(rel)
	tmp := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".sleipnir-"+rand.Text()[:10])
	if err := root.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	if err := root.Chmod(tmp, mode); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// fileVersion is the version of a file's bytes as read by os.ReadFile: "" for a file that is not there, else the first 16 hex
// characters of their SHA-256. ok is false for another read error.
func fileVersion(b []byte, err error) (v string, ok bool) {
	if err != nil {
		return "", errors.Is(err, fs.ErrNotExist)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16], true
}

// rfc3339 is t in RFC 3339, or "" for the zero time.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
