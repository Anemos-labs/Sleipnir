// Package memtool is the model's long-term memory: short lists of notes that memory.Load puts in the shared layer of every
// later session, in two scopes. User notes (~/.sleipnir/MEMORY.md) are shown in every project: how the user likes things
// done, a fact about their machine or accounts. Project notes (.sleipnir/MEMORY.local.md under the project root, git-ignored
// by a .sleipnir/.gitignore the tool writes beside it) are shown only in that project, and only when it is trusted: a fact
// about this code base, a lesson that cost time here. The model adds a note when it learns something worth keeping and
// removes one that is stale.
//
// What keeps it safe and small:
//   - a note is one line of at most MaxNoteRunes, with control characters removed; the file holds at most MaxNotes notes and
//     MaxBytes bytes. A full memory is not a silent loss and not a dead end: the answer lists the notes by number and asks the
//     model to remove or merge before it adds;
//   - a change is not put to the person: the file is the harness's own, never a path the model chose, so it is not judged as a write
//     to ~/.sleipnir. The permission engine still sees the change with the exact note in its summary, so a deny rule (memory) refuses
//     it and an ask rule (memory) puts each one to the person again. Instead the change is announced: the result's Meta says what
//     changed (MetaChange), the web interface counts it on the Memory item of the left bar, and the Memory page shows the note to
//     keep, edit or remove;
//   - the file says that its notes are the model's own and may be wrong; the user's instructions win.
//
// The prompt of the running session does not change when a note is saved (the shared layer is built at the start), so saving
// costs no cache; the note is there from the next session.
package memtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

const (
	MaxNotes     = 40
	MaxNoteRunes = 300
	MaxBytes     = 8 << 10
	// header is plain text, not an HTML comment: the instruction loader removes comments, and this line must reach the model.
	header = "Notes the agent saved with its memory tool. They may be wrong or out of date; the user's own instructions win."
)

// MetaChange is the key of a result's Meta that says the call changed the notes: "added" or "removed". A UI counts it as news.
const MetaChange = "memory_change"

// Tool is the memory tool over the user's notes file and, when a project is known, the project's.
type Tool struct{ path, project string }

// New returns the tool for the user's notes file at path (the directory is created on the first note), without project notes.
func New(path string) *Tool { return &Tool{path: path} }

// NewScoped returns the tool over the user's notes at userPath and the project's at projectPath ("" for none: scope project is
// then refused with the reason).
func NewScoped(userPath, projectPath string) *Tool {
	return &Tool{path: userPath, project: projectPath}
}

// Off returns the tool of a session started without the global memory: the same spec, so the tool list does not change, and every
// call answers that the memory is off, reading and writing nothing.
func Off() *Tool { return &Tool{} }

// offText is what every call of the tool says in a session without the global memory.
const offText = "the global memory is off in this session (it was started with --no-memory): notes are neither shown nor saved"

const schema = `{"type":"object","properties":{` +
	`"action":{"type":"string","enum":["add","remove","list"]},` +
	`"scope":{"type":"string","enum":["user","project"],"description":"user (default): shown in every project. project: shown only in this project"},` +
	`"text":{"type":"string","description":"add: the note, one fact in one sentence. remove: a few words that only that note contains"}},"required":["action"]}`

// Spec implements tools.Tool.
func (*Tool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: perm.ToolMemory,
		Description: "Your long-term memory: short notes shown to you at the start of every future session. " +
			"add one when you learn something lasting. scope user (the default) is for every project: how the user wants things done, a fact about their setup. " +
			"scope project is for this project only: a fact about this code base, a mistake not to repeat here. " +
			"One fact per note, under 300 characters. Never save task progress, anything the code or the project files already say, or secrets. " +
			"remove a note that is wrong or stale; list shows them all. The user sees each change and can edit or remove it.",
		InputSchema: json.RawMessage(schema),
	}
}

type input struct {
	Action string `json:"action"`
	Scope  string `json:"scope"`
	Text   string `json:"text"`
}

// Run implements tools.Tool.
func (t *Tool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	var in input
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if t.path == "" {
		return &tools.Result{Text: offText}, nil
	}
	if in.Action == "list" && in.Scope == "" {
		return t.listAll()
	}
	file := t.path
	switch in.Scope {
	case "", "user":
	case "project":
		if t.project == "" {
			return tools.Errorf("this session keeps no project notes (it has no project, or the project is not trusted): use scope user"), nil
		}
		file = t.project
	default:
		return tools.Errorf(`scope is "user" or "project"`), nil
	}
	f := &Tool{path: file}
	project := file != t.path
	if project {
		if err := CheckProjectPath(file); err != nil {
			return tools.Errorf("memory: %v", err), nil
		}
	}
	notes, err := f.read()
	if err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	return f.run(ctx, c, in, notes, project)
}

// listAll lists the notes of both scopes, user first.
func (t *Tool) listAll() (*tools.Result, error) {
	user, err := t.read()
	if err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	if t.project == "" {
		if len(user) == 0 {
			return &tools.Result{Text: "no notes yet"}, nil
		}
		return &tools.Result{Text: numbered(user)}, nil
	}
	if err := CheckProjectPath(t.project); err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	proj, err := (&Tool{path: t.project}).read()
	if err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	if len(user) == 0 && len(proj) == 0 {
		return &tools.Result{Text: "no notes yet"}, nil
	}
	var b strings.Builder
	b.WriteString("user notes (every project):\n")
	if len(user) == 0 {
		b.WriteString("(none)")
	} else {
		b.WriteString(numbered(user))
	}
	b.WriteString("\n\nproject notes (this project):\n")
	if len(proj) == 0 {
		b.WriteString("(none)")
	} else {
		b.WriteString(numbered(proj))
	}
	return &tools.Result{Text: b.String()}, nil
}

// run carries out one action on the notes of t's one file; project says it is a project's file, which the first note
// git-ignores.
func (t *Tool) run(ctx context.Context, c *tools.Call, in input, notes []string, project bool) (*tools.Result, error) {
	switch in.Action {
	case "list":
		if len(notes) == 0 {
			return &tools.Result{Text: "no notes yet"}, nil
		}
		return &tools.Result{Text: numbered(notes)}, nil
	case "add":
		note := clean(in.Text)
		switch {
		case note == "":
			return tools.Errorf(`give the note in "text": one fact in one sentence`), nil
		case utf8.RuneCountInString(note) > MaxNoteRunes:
			return tools.Errorf("that note is %d characters; the limit is %d. Say it shorter, or as two notes", utf8.RuneCountInString(note), MaxNoteRunes), nil
		}
		for _, n := range notes {
			if strings.EqualFold(n, note) {
				return &tools.Result{Text: "already saved"}, nil
			}
		}
		if len(notes) >= MaxNotes || size(append(notes, note)) > MaxBytes {
			return tools.Errorf("memory is full (%d notes). Remove or merge notes that are stale or say the same thing (action remove), then add again:\n%s", len(notes), numbered(notes)), nil
		}
		what := "remember: "
		if project {
			what = "remember for this project: "
		}
		if r := t.authorize(ctx, c, what+note); r != nil {
			return r, nil
		}
		if project {
			if err := CheckProjectPath(t.path); err != nil {
				return tools.Errorf("memory: %v", err), nil
			}
			if err := ignoreBeside(t.path); err != nil {
				return tools.Errorf("memory: %v", err), nil
			}
			return t.write(notes, append(slices.Clone(notes), note), "saved for this project; it will be in front of you from the next session on, when the project is trusted", "added")
		}
		return t.write(notes, append(slices.Clone(notes), note), "saved; it will be in front of you from the next session on", "added")
	case "remove":
		key := strings.ToLower(clean(in.Text))
		if utf8.RuneCountInString(key) < 3 {
			return tools.Errorf(`give "text": a few words that only the note to remove contains (see action list)`), nil
		}
		var keep, gone []string
		for _, n := range notes {
			if strings.Contains(strings.ToLower(n), key) {
				gone = append(gone, n)
			} else {
				keep = append(keep, n)
			}
		}
		switch {
		case len(gone) == 0:
			return tools.Errorf("no note contains %q:\n%s", key, numbered(notes)), nil
		case len(gone) > 1 && len(gone) == len(notes) && len(notes) > 1:
			return tools.Errorf("%q matches every note; use words that only one contains:\n%s", key, numbered(notes)), nil
		}
		if r := t.authorize(ctx, c, "forget: "+strings.Join(gone, " | ")); r != nil {
			return r, nil
		}
		if project {
			if err := CheckProjectPath(t.path); err != nil { // again: the parent may have become a link while the person was asked
				return tools.Errorf("memory: %v", err), nil
			}
		}
		return t.write(notes, keep, fmt.Sprintf("removed %d note(s)", len(gone)), "removed")
	}
	return tools.Errorf(`action is "add", "remove" or "list"`), nil
}

// authorize asks the permission engine about one change. The request names no path: the file is the tool's own, so the
// protection of ~/.sleipnir and .sleipnir (which guards what an agent could write there to steer later sessions) does not ask about
// it, and only the person's rules for the memory tool decide.
func (t *Tool) authorize(ctx context.Context, c *tools.Call, summary string) *tools.Result {
	d := c.Env.Perm.Check(ctx, perm.Request{
		Agent: c.Env.Agent, Role: c.Env.Role, Tool: perm.ToolMemory, Summary: summary, Input: c.Input,
	})
	if d.Allow {
		return nil
	}
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "not allowed by the permission policy"
	}
	return tools.Errorf("permission denied (%s): %s", summary, reason)
}

// clean makes s one line of single-spaced words without control characters.
func clean(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)), " ")
}

// numbered formats notes as a one-based numbered list without a final newline.
func numbered(notes []string) string {
	var b strings.Builder
	for i, n := range notes {
		fmt.Fprintf(&b, "%d. %s\n", i+1, n)
	}
	return strings.TrimRight(b.String(), "\n")
}

// size estimates serialized note bytes from the header and per-note overhead.
func size(notes []string) int {
	n := len(header) + 1
	for _, s := range notes {
		n += len(s) + 3
	}
	return n
}

// read returns the notes of the tool's file.
func (t *Tool) read() ([]string, error) { return ReadNotes(t.path) }

// write replaces the tool's file with notes, made from was, and answers msg with the count; the answer's Meta[MetaChange] is change
// (added or removed). The file is read again under its lock
// first (the Memory page of the web interface replaces the same file): when it no longer holds was, nothing is written and the
// model is told to look again. The lock is not held while the person is asked, so a page's write never waits on an answer.
func (t *Tool) write(was, notes []string, msg, change string) (*tools.Result, error) {
	unlock := config.WriteLock(t.path)
	defer unlock()
	if cur, err := ReadNotes(t.path); err != nil || !slices.Equal(cur, was) {
		return tools.Errorf("the notes changed meanwhile (the person edited them); nothing was saved: list them and try again"), nil
	}
	if err := WriteNotes(t.path, notes); err != nil {
		return tools.Errorf("%v", err), nil
	}
	return &tools.Result{Text: fmt.Sprintf("%s (%d notes)", msg, len(notes)), Meta: map[string]any{MetaChange: change}}, nil
}

// ReadNotes returns the notes of the file at path: its lines that start with "- ". A missing file holds none.
func ReadNotes(path string) ([]string, error) {
	_, notes, err := LoadNotes(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return notes, err
}

// MaxFileBytes bounds what is read of a notes file. The tool writes at most MaxBytes; a file longer than this was not written as
// notes, and reading it all (a link to an endless device, say) would only cost memory.
const MaxFileBytes = 1 << 20

// LoadNotes reads the notes file at path: its bytes and its notes. A missing file holds none; anything but a regular file of at
// most MaxFileBytes is an error (fs.ErrNotExist for a missing one).
func LoadNotes(path string) ([]byte, []string, error) {
	return loadNotes(func() (*os.File, os.FileInfo, error) {
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, fi, err
		}
		f, err := os.Open(path)
		return f, fi, err
	})
}

// LoadNotesIn is LoadNotes for rel inside the directory root: rel is resolved within root, so no link in it leads outside.
func LoadNotesIn(root, rel string) ([]byte, []string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	return loadNotes(func() (*os.File, os.FileInfo, error) {
		fi, err := r.Stat(rel)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, fi, err
		}
		f, err := r.Open(rel)
		return f, fi, err
	})
}

// loadNotes reads what open opens (nil when the stat it made shows no regular file), bounded.
func loadNotes(open func() (*os.File, os.FileInfo, error)) ([]byte, []string, error) {
	f, fi, err := open()
	if err != nil {
		return nil, nil, err
	}
	if f == nil {
		return nil, nil, fmt.Errorf("the notes file is not a regular file (%v)", fi.Mode().Type())
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, nil, errors.New("the notes file is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(b) > MaxFileBytes {
		return nil, nil, fmt.Errorf("the notes file is larger than %d bytes", MaxFileBytes)
	}
	var notes []string
	for _, l := range strings.Split(string(b), "\n") {
		if n, ok := strings.CutPrefix(l, "- "); ok && strings.TrimSpace(n) != "" {
			notes = append(notes, strings.TrimSpace(n))
		}
	}
	return b, notes, nil
}

// CheckNotes returns notes as the tool would keep them, for an editor that replaces the whole list: each note made one line of
// single-spaced words without control characters, empty ones dropped and repeats (ignoring case) kept once. A note over
// MaxNoteRunes, more than MaxNotes notes or more than MaxBytes in all is an error that says which limit was passed.
func CheckNotes(notes []string) ([]string, error) {
	return CheckEdit(nil, notes)
}

// CheckEdit is CheckNotes for an edit of the notes prev the file holds now: what the file already breaks is no reason to refuse
// an edit that does not make it worse. A note over MaxNoteRunes that prev already has is kept as it is, and more than MaxNotes notes
// or MaxBytes in all is refused only when the edit grows past what prev has, so a file written by hand past the limits can still
// have a note removed or corrected.
func CheckEdit(prev, notes []string) ([]string, error) {
	had := map[string]bool{}
	for _, n := range prev {
		had[strings.ToLower(clean(n))] = true
	}
	out := make([]string, 0, len(notes))
	seen := map[string]bool{}
	for i, n := range notes {
		n = clean(n)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		if c := utf8.RuneCountInString(n); c > MaxNoteRunes && !had[strings.ToLower(n)] {
			return nil, fmt.Errorf("note %d is %d characters; the limit is %d", i+1, c, MaxNoteRunes)
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	if len(out) > MaxNotes && len(out) > len(had) {
		return nil, fmt.Errorf("%d notes; the memory holds at most %d", len(out), MaxNotes)
	}
	if size(out) > MaxBytes && size(out) > size(prev) {
		return nil, fmt.Errorf("the notes come to %d bytes; the memory holds at most %d", size(out), MaxBytes)
	}
	return out, nil
}

// WriteNotes replaces the notes of the file at path with notes, one "- " line each, atomically and readable by the user only; the
// directory is created when it is missing. The other lines of the file (its header, headings and text written by hand) are kept
// where they are: the notes take the place of the first note line, or follow the text when it has none. A new file starts with the
// header.
func WriteNotes(path string, notes []string) error {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: %w", err)
	}
	var b strings.Builder
	put := func() {
		for _, n := range notes {
			b.WriteString("- " + n + "\n")
		}
	}
	if strings.TrimSpace(string(old)) == "" {
		b.WriteString(header + "\n")
		put()
	} else {
		placed := false
		for _, l := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
			if n, ok := strings.CutPrefix(l, "- "); ok && strings.TrimSpace(n) != "" {
				if !placed {
					put()
					placed = true
				}
				continue
			}
			b.WriteString(l + "\n")
		}
		if !placed {
			put()
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	return nil
}

// CheckProjectPath refuses a project's notes file at path that a link would send elsewhere: its directory (.sleipnir), the file
// or the .gitignore beside it being a symbolic link. The project is the repository's to shape, and its notes are written without
// a question, so they are written only where they appear to be.
func CheckProjectPath(path string) error {
	dir := filepath.Dir(path)
	for _, q := range []string{dir, path, filepath.Join(dir, ".gitignore")} {
		if fi, err := os.Lstat(q); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link: the project's notes are not written through it", filepath.Base(q))
		}
	}
	return nil
}

// WriteProjectNotes is WriteNotes for a project's notes file: the .gitignore beside it keeps the file out of git first. A path a
// link would send elsewhere is refused (CheckProjectPath).
func WriteProjectNotes(path string, notes []string) error {
	if err := CheckProjectPath(path); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	if err := ignoreBeside(path); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	return WriteNotes(path, notes)
}

// ignoreBeside makes git ignore the notes file at p with a .gitignore in its directory: created when missing, and given the
// file's name when it does not list it yet. The file's name is matched exactly (a line of its own). A .gitignore the tool
// creates lists itself too, so the directory leaves no untracked file behind: a run with --commit needs a clean checkout, and
// a resumed one was refused for it.
func ignoreBeside(p string) error {
	dir, name := filepath.Dir(p), filepath.Base(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	gi := filepath.Join(dir, ".gitignore")
	b, err := os.ReadFile(gi)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) {
		return os.WriteFile(gi, []byte(".gitignore\n"+name+"\n"), 0o644)
	}
	// The last line about the name decides, as in git: a later "!name" undoes an earlier "name", and the name is then
	// listed again at the end.
	ignored := false
	for _, l := range strings.Split(string(b), "\n") {
		switch strings.TrimSpace(l) {
		case name, "/" + name:
			ignored = true
		case "!" + name, "!/" + name:
			ignored = false
		}
	}
	if ignored {
		return nil
	}
	f, err := os.OpenFile(gi, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	line := name + "\n"
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
