package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// The permission dialog. A tool that needs a person's word puts a question to the prompter (perm.Prompter), which forwards it to
// the program (ChatLink.Prompter); the program shows it in the live region as a widget.Dialog, takes the answer from the keys, and
// hands the decision back.
//
// The one thing that must not happen is a question answered by keys that were meant for something else: a line typed while the
// agent worked, and the letters of a sentence that is half typed when the question appears. The line version of this (cmd/sleipnir
// chat_input.go) settled it by reading the question's answer only from a line that arrives after the question was shown. Keys are
// one at a time, so the rule is this: a question takes keys only when the keyboard has been quiet for AnswerAfter since the
// question appeared, or since the last key that was not for it. Until then every key goes where it would have gone without the
// question, to the prompt, and each one starts the wait again; a person who has read the question and then presses 1 is always
// heard, one who is typing never answers it by accident, and Ctrl-C (which can only cancel) is always heard. A question that has
// waited behind another is in the same case when it comes to the front: the key that answered the one before was not meant for it.

// dialog is a question that is on the screen.
type dialog struct {
	q     *question
	sel   int
	armAt time.Time // keys answer from this moment
	// shownAt is when the question appeared, and toolKey the call it is about (empty when the program has not seen it start): the time
	// between them and the answer is not part of the call's duration.
	shownAt   time.Time
	toolKey   string
	opts      []widget.DialogOption
	decisions []perm.Decision // what each option decides
	title     string
	body      []cell.Line
	width     int    // the width body was laid out for
	current   string // what the file of a write holds as the question appears: what the preview is a change of
}

// defaultAnswerAfter is the pause before a question takes an answer from the keyboard.
const defaultAnswerAfter = 350 * time.Millisecond

// dialogOptions are the answers of a question and the decision of each. A question about an action has perm.Answers: 1 yes; 2 yes, and
// don't ask again (only when the engine can remember a rule that lets the request through: Request.RememberRules); 3 yes, and allow the
// builds and tests (for a build or test command: Request.OffersTests); and no, last, with the number the web page gives it too (3, or 4
// after the builds and tests). The harness's own two questions have three answers each: the second remembers, for a project's tool
// server the exact entry for the project, for the project's own files the files as they are now.
//
// The options are chosen by their numbers, by the arrows and enter, and the last one by esc. They have no letters (the line prompt's
// y, a and n): a letter is what a sentence is made of, and a question answered by the letters of a line that is being typed is the
// fault this program exists to be safe against.
func dialogOptions(r perm.Request) (opts []widget.DialogOption, decisions []perm.Decision) {
	no := widget.DialogOption{Label: perm.AnswerNoLabel, Hint: "(esc)", Keys: []string{"esc"}}
	switch r.Tool {
	case perm.ToolMCPServer:
		no.Label = "No"
		return []widget.DialogOption{{Label: "Yes, start it this time"}, {Label: "Yes, and remember this exact entry for this project"}, no},
			[]perm.Decision{perm.DecisionYes, {Allow: true, Reason: "approved by user for this project", Remember: perm.ScopeProject}, perm.DecisionNo}
	case perm.ToolProjectTrust:
		no.Label = "No, leave them out"
		return []widget.DialogOption{{Label: "Yes, use them this time"}, {Label: "Yes, and remember them until they change"}, no},
			[]perm.Decision{perm.DecisionYes, {Allow: true, Reason: "trusted by user until the files change", Remember: perm.ScopeProject}, perm.DecisionNo}
	}
	for _, a := range perm.Answers(r) {
		o := widget.DialogOption{Label: a.Label, Num: a.Key}
		if !a.Decision.Allow {
			o.Hint, o.Keys = no.Hint, no.Keys
		}
		opts = append(opts, o)
		decisions = append(decisions, a.Decision)
	}
	return opts, decisions
}

// AnswerFor is what a key decides when it is pressed at the dialog of a question: a digit that is the number of one of the dialog's
// options (dialogOptions) chooses it, and any other key decides nothing (ok is false): there is no letter that answers. It is the
// dialog's own table. The recording of a chat (sleipnir chat-record) answers the questions of its session with it, so that what the
// session was told is what the dialog would have told it, and the player of the recording (PlayChat) checks that the program, given the
// same key, decides the same.
func AnswerFor(r perm.Request, k input.Key) (dec perm.Decision, ok bool) {
	opts, decisions := dialogOptions(r)
	if !isChoice(k, opts) {
		return perm.Decision{}, false
	}
	i, _ := widget.DialogKeys(string(k.R), opts)
	return decisions[i], true
}

// decision is the answer option i stands for.
func (d *dialog) decision(i int) perm.Decision {
	if i < 0 || i >= len(d.decisions) {
		return perm.DecisionNo
	}
	return d.decisions[i]
}

// armed says whether the question takes an answer at now.
func (d *dialog) armed(now time.Time) bool { return !now.Before(d.armAt) }

// choose is what a key does to an armed dialog: it moves the selection, chooses an option (the index, true) or does nothing.
func (d *dialog) choose(k input.Key) (idx int, chosen bool) {
	n := len(d.opts)
	switch {
	case k.Is(input.Up, 0), k.Is(input.Tab, input.Shift), k.IsRune('p', input.Ctrl):
		d.sel = (d.sel + n - 1) % n
	case k.Is(input.Down, 0), k.Is(input.Tab, 0), k.IsRune('n', input.Ctrl):
		d.sel = (d.sel + 1) % n
	case k.Is(input.Enter, 0):
		return d.sel, true
	case k.Is(input.Esc, 0):
		if i, ok := widget.DialogKeys("esc", d.opts); ok {
			return i, true
		}
		return n - 1, true
	case k.Kind == input.KindRune && (k.Mod == 0 || k.Mod == input.Shift):
		if i, ok := widget.DialogKeys(string(k.R), d.opts); ok {
			return i, true
		}
	}
	return 0, false
}

// request is a question as the dialog puts it: the title says what is being asked, the body shows what would happen, in full (a
// person must see what they are approving), and the reason the engine asked is last.
//
// call is the tool call the question is about, when the program has seen it start: the file tools do not send their arguments with
// the question, so what an edit would change is read from the call.
func (k *chatLook) requestBody(r perm.Request, call *toolRun, inner int, cwd, current string) (title string, body []cell.Line) {
	what, why := splitWhy(r.Summary)
	tool := strings.ToLower(r.Tool)
	switch {
	case r.Tool == perm.ToolMCPServer:
		title = "Start a tool server"
		for _, l := range textLines(what) {
			body = append(body, cell.Text(l))
		}
	case r.Tool == perm.ToolProjectTrust:
		// The question, a line for each file the project has, a blank line and what it means: the title is the question, the
		// files are shown as they are and the explanation is text that wraps.
		title = "Use this project's own files?"
		lines := textLines(what)
		if len(lines) > 0 {
			lines = lines[1:]
		}
		rest := -1
		for i, l := range lines {
			if l == "" {
				rest = i
				break
			}
			body = append(body, cell.Text(l))
		}
		if rest >= 0 {
			body = append(body, nil)
			body = append(body, paragraph(k.st.dim, strings.Join(lines[rest+1:], " "), max(inner, 1))...)
		}
	case r.Command != "" || tool == "bash":
		title = "Run a command"
		cmd := r.Command
		if cmd == "" && call != nil {
			cmd, _ = stringField(call.input, "command")
		}
		if cmd == "" {
			cmd = what
		}
		// the command as it runs, nothing of it left out: what the terminal would act on is written out, not removed
		lines := textLines(tools.EscapeForTerminal(cmd))
		for i, l := range lines {
			pre := "  "
			if i == 0 {
				pre = "$ "
			}
			body = append(body, cell.Join(cell.Styled(k.st.dim, pre), cell.Text(l)))
		}
		if r.Cwd != "" && cwd != "" && r.Cwd != cwd {
			body = append(body, cell.Styled(k.st.dim, "in "+relPath(cwd, clean(r.Cwd))))
		}
	case tool == "edit" || tool == "write" || tool == "apply_patch":
		title = map[string]string{"edit": "Edit a file", "write": "Write a file", "apply_patch": "Apply a patch"}[tool]
		body = k.changeBody(tool, r, call, inner, cwd, current)
	case tool == "web_fetch":
		title = "Fetch a page"
		body = plainBody(what)
	case tool == "web_search":
		title = "Search the web"
		body = plainBody(what)
	default:
		title = "Allow " + toolTitle(r.Tool) + "?"
		body = plainBody(what)
		for _, p := range r.Paths {
			body = append(body, cell.Styled(k.st.dim, "  "+relPath(cwd, clean(p))))
		}
	}
	if who := k.agentTag(r.Agent); who != "" && who != "main" {
		title += " " + k.g.dot + " " + who
	}
	if why != "" {
		body = append(body, nil)
		for _, l := range paragraph(k.st.dim, why, max(inner, 1)) {
			body = append(body, l)
		}
	}
	return title, body
}

// plainBody converts split text lines into unstyled terminal-cell lines.
func plainBody(s string) []cell.Line {
	var out []cell.Line
	for _, l := range textLines(s) {
		out = append(out, cell.Text(l))
	}
	return out
}

// splitWhy cuts the reason off a question's summary: the engine appends " [why it asks]" to the line the tool wrote.
func splitWhy(summary string) (what, why string) {
	s := strings.TrimSpace(summary)
	if strings.HasSuffix(s, "]") {
		if i := strings.LastIndex(s, " ["); i > 0 {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2 : len(s)-1])
		}
	}
	return s, ""
}

// What a person is asked to allow is shown in full: the dialog fits it to the window (fitBody), and what does not fit is written into
// the scrollback whole (ask). These are only the bounds beyond which a request is absurd, not what is shown: the first real approval was
// of a 65-line file, which showed twelve lines and "53 more lines", with nowhere to read them.
const (
	approvalDiffRows = 5000 // lines of one change
	approvalEdits    = 200  // edits of one call
)

// changeBody shows what a file tool is about to do: the path, then the change as a diff where the call says what it is.
func (k *chatLook) changeBody(tool string, r perm.Request, call *toolRun, inner int, cwd, current string) []cell.Line {
	path := ""
	if len(r.Paths) > 0 {
		path = relPath(cwd, clean(r.Paths[0]))
	}
	var body []cell.Line
	if path != "" {
		body = append(body, cell.Styled(cell.Style{Attr: cell.Bold}, path))
	}
	if call == nil {
		return append(body, plainBody(r.Summary)...)
	}
	switch tool {
	case "edit":
		var a struct {
			Old   *string `json:"old_string"`
			New   *string `json:"new_string"`
			Edits []struct {
				Old string `json:"old_string"`
				New string `json:"new_string"`
			} `json:"edits"`
		}
		if json.Unmarshal(call.input, &a) != nil {
			break
		}
		type pair struct{ old, new string }
		var pairs []pair
		if a.Old != nil && a.New != nil {
			pairs = append(pairs, pair{*a.Old, *a.New})
		}
		for _, e := range a.Edits {
			pairs = append(pairs, pair{e.Old, e.New})
		}
		for i, p := range pairs {
			if i >= approvalEdits {
				body = append(body, cell.Styled(k.st.dim, count(len(pairs)-i, "more edit", "more edits")))
				break
			}
			body = append(body, widget.Diff("", p.old, p.new, inner, k.Theme, widget.DiffOptions{NoHeader: true, NoLineNumbers: true, MaxLines: approvalDiffRows})...)
		}
	case "write":
		if content, ok := stringField(call.input, "content"); ok {
			body = append(body, widget.Diff("", current, content, inner, k.Theme, widget.DiffOptions{NoHeader: true, NoLineNumbers: true, MaxLines: approvalDiffRows})...)
		}
	case "apply_patch":
		if patch, ok := stringField(call.input, "patch"); ok {
			// the diff names each file it changes: the path above it would say the same again
			body = widget.UnifiedDiffWith(patchToUnified(patch), inner, k.Theme, widget.DiffOptions{MaxLines: approvalDiffRows})
		}
	}
	return body
}

// currentText is what the file a write is about to replace holds now, so that the approval shows what the write changes and not the
// whole file as added; "" for a file that is not there, is not text, or is too big to be worth reading for a preview.
func currentText(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	// a FIFO or a device is not opened at all: opening one can wait for a writer, and this runs on the loop that takes the answer
	if fi, err := os.Stat(paths[0]); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	f, err := os.OpenFile(paths[0], readFlags, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return ""
	}
	return string(b)
}

// fitBody makes a body fit maxRows: a body that is too tall is cut in the middle, where a row says how many are left out, so that
// both its beginning and its end are in view. The second result is how many rows were cut (0: none, and the body is as it was).
func (k *chatLook) fitBody(body []cell.Line, maxRows int) ([]cell.Line, int) {
	if maxRows < 3 || len(body) <= maxRows {
		return body, 0
	}
	head := maxRows / 2
	tail := maxRows - 1 - head
	cut := len(body) - head - tail
	out := append([]cell.Line(nil), body[:head]...)
	out = append(out, cell.Styled(k.st.dim, k.g.ellipsis+" "+count(cut, "line", "lines")+" more: the whole of it is in the scrollback above"))
	return append(out, body[len(body)-tail:]...), cut
}
