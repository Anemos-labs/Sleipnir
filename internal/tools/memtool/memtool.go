// Package memtool is the model's long-term memory: a short list of notes in ~/.sleipnir/MEMORY.md that memory.Load puts in the
// shared layer of every later session. The model adds a note when it learns something worth keeping (how the user likes
// things done, a fact about their setup, a lesson that cost time) and removes one that is stale.
//
// What keeps it safe and small:
//   - a note is one line of at most MaxNoteRunes, with control characters removed; the file holds at most MaxNotes notes and
//     MaxBytes bytes. A full memory is not a silent loss and not a dead end: the answer lists the notes by number and asks the
//     model to remove or merge before it adds;
//   - every change is a write the permission engine is asked about, with the exact note in the summary, so a note that text
//     read from a web page or a file talked the model into writing is seen by the person before it is kept;
//   - the file says that its notes are the model's own and may be wrong; the user's instructions win.
//
// The prompt of the running session does not change when a note is saved (the shared layer is built at the start), so saving
// costs no cache; the note is there from the next session.
package memtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

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

// Tool is the memory tool over one file.
type Tool struct{ path string }

// New returns the tool for the notes file at path (the directory is created on the first note).
func New(path string) *Tool { return &Tool{path: path} }

const schema = `{"type":"object","properties":{` +
	`"action":{"type":"string","enum":["add","remove","list"]},` +
	`"text":{"type":"string","description":"add: the note, one fact in one sentence. remove: a few words that only that note contains"}},"required":["action"]}`

// Spec implements tools.Tool.
func (*Tool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "memory",
		Description: "Your long-term memory: short notes shown to you at the start of every future session, in any project. " +
			"add one when you learn something lasting: how the user wants things done, a fact about their setup, a mistake not to repeat. " +
			"One fact per note, under 300 characters. Never save task progress, anything the code or the project files already say, or secrets. " +
			"remove a note that is wrong or stale; list shows them all. The user is asked to approve each change.",
		InputSchema: json.RawMessage(schema),
	}
}

type input struct {
	Action string `json:"action"`
	Text   string `json:"text"`
}

// Run implements tools.Tool.
func (t *Tool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	var in input
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	notes, err := t.read()
	if err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
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
		if r := t.authorize(ctx, c, "remember: "+note); r != nil {
			return r, nil
		}
		return t.write(append(notes, note), "saved; it will be in front of you from the next session on")
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
		return t.write(keep, fmt.Sprintf("removed %d note(s)", len(gone)))
	}
	return tools.Errorf(`action is "add", "remove" or "list"`), nil
}

func (t *Tool) authorize(ctx context.Context, c *tools.Call, summary string) *tools.Result {
	d := c.Env.Perm.Check(ctx, perm.Request{
		Agent: c.Env.Agent, Role: c.Env.Role, Tool: "memory", Summary: summary,
		Paths: []string{t.path}, Writes: true, Risk: perm.RiskMedium,
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

func numbered(notes []string) string {
	var b strings.Builder
	for i, n := range notes {
		fmt.Fprintf(&b, "%d. %s\n", i+1, n)
	}
	return strings.TrimRight(b.String(), "\n")
}

func size(notes []string) int {
	n := len(header) + 1
	for _, s := range notes {
		n += len(s) + 3
	}
	return n
}

// read returns the notes of the file: its lines that start with "- ". A missing file holds none.
func (t *Tool) read() ([]string, error) {
	b, err := os.ReadFile(t.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, l := range strings.Split(string(b), "\n") {
		if n, ok := strings.CutPrefix(l, "- "); ok && strings.TrimSpace(n) != "" {
			notes = append(notes, strings.TrimSpace(n))
		}
	}
	return notes, nil
}

func (t *Tool) write(notes []string, msg string) (*tools.Result, error) {
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, n := range notes {
		b.WriteString("- " + n + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.path), ".memory-*")
	if err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return tools.Errorf("memory: %v", err), nil
	}
	if err := tmp.Close(); err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	if err := os.Rename(tmp.Name(), t.path); err != nil {
		return tools.Errorf("memory: %v", err), nil
	}
	return &tools.Result{Text: fmt.Sprintf("%s (%d notes)", msg, len(notes))}, nil
}
