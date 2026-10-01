package app

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/widget"
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
// heard, one who is typing never answers it by accident, and Ctrl-C (which can only cancel) is always heard.

// dialog is a question that is on the screen.
type dialog struct {
	q     *question
	sel   int
	armAt time.Time // keys answer from this moment
	opts  []widget.DialogOption
	mcp   bool // the question is whether to start a project's tool server, which has its own answers
	title string
	body  []cell.Line
	width int // the width body was laid out for
}

// defaultAnswerAfter is the pause before a question takes an answer from the keyboard.
const defaultAnswerAfter = 350 * time.Millisecond

// dialogOptions are the three answers of a question. The second one is what the old prompt called "always": it remembers the
// exact request for the rest of the session (for a project's tool server, the exact entry for the project).
//
// The options are chosen by their numbers, by the arrows and enter, and the last one by esc. They have no letters (the line prompt's
// y, a and n): a letter is what a sentence is made of, and a question answered by the letters of a line that is being typed is the
// fault this program exists to be safe against.
func dialogOptions(r perm.Request) (opts []widget.DialogOption, mcp bool) {
	if r.Tool == "mcp-server" {
		return []widget.DialogOption{
			{Label: "Yes, start it this time"},
			{Label: "Yes, and remember this exact entry for this project"},
			{Label: "No", Hint: "(esc)", Keys: []string{"esc"}},
		}, true
	}
	what := "this request"
	switch strings.ToLower(r.Tool) {
	case "bash":
		what = "this command"
	case "edit", "write", "apply_patch":
		what = "this change"
	}
	return []widget.DialogOption{
		{Label: "Yes"},
		{Label: "Yes, and don't ask again for " + what + " this session"},
		{Label: "No, and tell Sleipnir what to do instead", Hint: "(esc)", Keys: []string{"esc"}},
	}, false
}

// decision is the answer option i stands for.
func (d *dialog) decision(i int) perm.Decision {
	switch {
	case i == 0:
		return perm.Decision{Allow: true, Reason: "allowed by user"}
	case i == 1 && d.mcp:
		return perm.Decision{Allow: true, Reason: "approved by user for this project", Remember: perm.ScopeProject}
	case i == 1:
		return perm.Decision{Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession}
	}
	return perm.Decision{Allow: false, Reason: "denied by user"}
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
func (k *chatLook) requestBody(r perm.Request, call *toolRun, inner int, cwd string) (title string, body []cell.Line) {
	what, why := splitWhy(r.Summary)
	tool := strings.ToLower(r.Tool)
	switch {
	case r.Tool == "mcp-server":
		title = "Start a tool server"
		for _, l := range textLines(what) {
			body = append(body, cell.Text(l))
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
		lines := textLines(cmd)
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
		body = k.changeBody(tool, r, call, inner, cwd)
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

// changeBody shows what a file tool is about to do: the path, then the change as a diff where the call says what it is.
func (k *chatLook) changeBody(tool string, r perm.Request, call *toolRun, inner int, cwd string) []cell.Line {
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
			if i >= 4 {
				body = append(body, cell.Styled(k.st.dim, count(len(pairs)-i, "more edit", "more edits")))
				break
			}
			body = append(body, widget.Diff("", p.old, p.new, inner, k.Theme, widget.DiffOptions{NoHeader: true, NoLineNumbers: true, MaxLines: 12})...)
		}
	case "write":
		if content, ok := stringField(call.input, "content"); ok {
			body = append(body, widget.Diff("", "", content, inner, k.Theme, widget.DiffOptions{NoHeader: true, NoLineNumbers: true, MaxLines: 12})...)
		}
	case "apply_patch":
		if patch, ok := stringField(call.input, "patch"); ok {
			body = append(body, widget.UnifiedDiffWith(patchToUnified(patch), inner, k.Theme, widget.DiffOptions{MaxLines: 24})...)
		}
	}
	return body
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
