package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

// How a tool call looks in the scrollback:
//
//	● Bash go test ./...  ✓ 1.4s
//	  ⎿ ok   example.com/orders  0.318s
//
// and for the tools whose output is not the point (read, grep, glob, ls) a single line with what they found, and for an edit the
// diff itself, with line numbers. The text is whatever the model asked for and the tool answered, so it is data: it goes through
// textLines or a widget before it is anything else.

// toolKind sorts the tools by how their result is shown.
type toolKind uint8

const (
	kindOther   toolKind = iota // the output, collapsed
	kindCommand                 // bash: the output, collapsed, its tail kept
	kindLook                    // read, grep, glob, ls, recall, web and the like: one line with what was found
	kindEdit                    // edit: the diff the tool made
	kindWrite                   // write: the new file, or what was overwritten
	kindPatch                   // apply_patch: the patch, as a diff
)

func kindOf(name string) toolKind {
	switch strings.ToLower(name) {
	case "bash", "bash_output", "bash_kill":
		return kindCommand
	case "read", "grep", "glob", "ls", "recall", "web_fetch", "web_search", "skill":
		return kindLook
	case "edit":
		return kindEdit
	case "write":
		return kindWrite
	case "apply_patch":
		return kindPatch
	}
	return kindOther
}

// toolTitle is the name a tool is shown under: "bash" is Bash. A name that is not a plain word (an MCP tool's
// mcp__server__tool) is shown as it is.
func toolTitle(name string) string {
	name = strings.TrimSpace(clean(name))
	if name == "" {
		return "tool"
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && r != '_' {
			return name
		}
	}
	if strings.Contains(name, "__") {
		return name
	}
	rs := []rune(name)
	rs[0] = unicode.ToUpper(rs[0])
	return strings.ReplaceAll(string(rs), "_", " ")
}

// summaryKeys are the arguments that say what a call is about, in the order they are looked for.
var summaryKeys = []string{"command", "path", "file_path", "pattern", "url", "query", "title", "action", "name", "id"}

// toolSummary is what a call was asked to do, on one line: the command, the path, the pattern. Paths under cwd are shown relative to
// it.
func toolSummary(name string, input json.RawMessage, cwd string) string {
	var in map[string]any
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, k := range summaryKeys {
		v, _ := in[k].(string)
		if strings.TrimSpace(v) == "" {
			continue
		}
		switch k {
		case "path", "file_path":
			v = relPath(cwd, v)
			if edits, ok := in["edits"].([]any); ok && len(edits) > 1 {
				v += fmt.Sprintf(" (%d edits)", len(edits))
			}
		case "pattern":
			if p, _ := in["path"].(string); p != "" {
				v += "  in " + relPath(cwd, p)
			}
		case "command":
			if kindOf(name) == kindCommand {
				if bg, _ := in["run_in_background"].(bool); bg {
					v += "  (in the background)"
				}
			}
		}
		return oneLineOf(v)
	}
	return ""
}

// oneLineOf makes text one line: the first line, and a mark when there were more.
func oneLineOf(s string) string { return firstLine(s, 400, "…") }

// relPath shows path relative to dir when it lies under it.
func relPath(dir, path string) string {
	path = strings.TrimSpace(path)
	if dir == "" {
		return path
	}
	dir = strings.TrimRight(dir, "/")
	if strings.HasPrefix(path, dir+"/") {
		return path[len(dir)+1:]
	}
	return path
}

// duration is how long a call took, the way a status line says it.
func duration(d time.Duration) string { return widget.Duration(d) }

var exitMark = regexp.MustCompile(`^\[exit code -?\d+\]$`)

// resultLines are the lines of a result as they are shown: the tool's own trailing "[exit code N]" is left off (the call line says
// how it ended), blank lines at the end too.
func resultLines(text string, kind toolKind) []string {
	lines := textLines(text)
	if kind == kindCommand {
		for len(lines) > 0 && (strings.TrimSpace(lines[len(lines)-1]) == "" || exitMark.MatchString(strings.TrimSpace(lines[len(lines)-1]))) {
			lines = lines[:len(lines)-1]
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Collapsing: a result of more than collapseAbove lines is shown as its first collapseHead and last collapseTail lines with a row
// between them that says how many are left out. The tail is kept because that is where a failing test or a build says what
// went wrong; the head because that is where a listing starts.
const (
	collapseAbove = 9
	collapseHead  = 3
	collapseTail  = 3
)

// expandable is the whole of a result that was collapsed, for ctrl+o.
type expandable struct {
	title string
	lines []string
}

// expandLimit is how many lines of one collapsed result ctrl+o can bring back: a result is truncated by the tool long before.
const expandLimit = 2000

// doneTool is a finished tool call as the scrollback shows it.
type doneTool struct {
	agent string
	call  core.Block
	res   toolResult
	took  time.Duration
	cwd   string
	// worker is true for an agent that is not the one whose words are the answer: its line says whose it is, and no output.
	worker bool
}

// toolLines lays a finished call out for the scrollback at width cells. The second result is what ctrl+o expands, when the output
// was collapsed.
func (k *chatLook) toolLines(t doneTool, width int) (lines []cell.Line, exp *expandable) {
	name := t.call.ToolName
	kind := kindOf(name)
	summary := toolSummary(name, t.call.Input, t.cwd)
	failed := t.res.Failed
	bulletSt, mark := k.st.good, k.g.ok
	if failed {
		bulletSt, mark = k.st.bad, k.g.fail
	}

	// the call line: ● Name summary  ✓ 1.4s
	var head row
	if t.worker {
		head.add(k.st.dim, "["+clean(t.agent)+"] ")
	}
	head.add(bulletSt, k.g.bullet+" ").add(k.st.info.With(cell.Bold), toolTitle(name))
	status := mark + " " + duration(t.took)
	if failed && t.res.Outcome != "" {
		status += " " + k.g.dot + " " + clean(t.res.Outcome)
	}
	extra := k.toolExtra(kind, t)
	tail := "  " + status
	if extra != "" {
		tail += "  " + extra
	}
	room := width - head.w - 1 - cell.StringWidth(tail)
	if summary != "" && room > 3 {
		head.add(cell.Style{}, " ").add(cell.Style{}, cutCells(summary, room, k.g.ellipsis))
	}
	head.add(k.statusStyle(failed), "  "+status)
	if extra != "" {
		head.add(k.st.dim, "  "+extra)
	}
	lines = append(lines, k.fit(head.line(), width))
	if t.worker {
		return lines, nil // a worker's tool is one line: its output is its own business
	}

	body, exp := k.toolBody(kind, t, width)
	return append(lines, body...), exp
}

func (k *chatLook) statusStyle(failed bool) cell.Style {
	if failed {
		return k.st.bad
	}
	return k.st.good
}

// toolExtra is the few words a one-line tool adds after its status: how many lines a read returned, how many files an edit changed.
func (k *chatLook) toolExtra(kind toolKind, t doneTool) string {
	if t.res.Failed {
		return ""
	}
	m := t.res.Meta
	switch kind {
	case kindLook:
		switch strings.ToLower(t.call.ToolName) {
		case "read":
			if n, ok := metaInt(m, "lines"); ok {
				return count(n, "line", "lines")
			}
		case "glob", "grep":
			if n, ok := metaInt(m, "matches"); ok {
				return count(n, "match", "matches")
			}
		case "web_search":
			if n, ok := metaInt(m, "results"); ok {
				return count(n, "result", "results")
			}
		default:
			if l := resultLines(t.res.Text, kindLook); len(l) > 0 {
				return cutCells(strings.TrimSpace(l[0]), 50, k.g.ellipsis)
			}
		}
	case kindEdit:
		a, aok := metaInt(m, "added")
		r, rok := metaInt(m, "removed")
		if aok && rok {
			return fmt.Sprintf("+%d %s%d", a, minusSign(k), r)
		}
	case kindPatch:
		a, aok := metaInt(m, "added")
		r, rok := metaInt(m, "removed")
		if aok && rok {
			return fmt.Sprintf("+%d %s%d", a, minusSign(k), r)
		}
	}
	return ""
}

func minusSign(k *chatLook) string {
	if k.Unicode {
		return "−"
	}
	return "-"
}

// metaInt reads a number out of a result's Meta, whatever type it went through.
func metaInt(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

// count is a number with its noun: "1 line", "84 lines".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// toolBody is what goes under a call line: the diff of an edit, the new file of a write, the collapsed output of a command.
func (k *chatLook) toolBody(kind toolKind, t doneTool, width int) ([]cell.Line, *expandable) {
	if t.res.Failed {
		return k.outputLines(t, kind, width, true)
	}
	switch kind {
	case kindEdit:
		if d, _ := t.res.Meta["diff"].(string); strings.TrimSpace(d) != "" {
			path := relPath(t.cwd, pathOf(t.call.Input))
			return k.diffBlock("--- a/"+path+"\n+++ b/"+path+"\n"+d, width), nil
		}
	case kindWrite:
		if created, _ := t.res.Meta["created"].(bool); created {
			if content, ok := stringField(t.call.Input, "content"); ok && content != "" {
				return k.newFileBlock(relPath(t.cwd, pathOf(t.call.Input)), content, width), nil
			}
		}
		return k.outputLines(t, kindOther, width, false)
	case kindPatch:
		if patch, ok := stringField(t.call.Input, "patch"); ok {
			if u := patchToUnified(patch); u != "" {
				return k.diffBlock(u, width), nil
			}
		}
	case kindLook:
		return nil, nil
	}
	return k.outputLines(t, kind, width, false)
}

func pathOf(input json.RawMessage) string {
	if p, ok := stringField(input, "path"); ok {
		return p
	}
	p, _ := stringField(input, "file_path")
	return p
}

// stringField reads one string argument of a call.
func stringField(input json.RawMessage, key string) (string, bool) {
	var in map[string]any
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return "", false
	}
	s, ok := in[key].(string)
	return s, ok
}

// Diffs are shown at most diffRows rows tall in the scrollback: the rest is named, and is in the file.
const diffRows = 24

// diffBlock renders a unified diff under a call line, indented to hang under the tool's name.
func (k *chatLook) diffBlock(patch string, width int) []cell.Line {
	lines := widget.UnifiedDiffWith(patch, max(width-2, 10), k.Theme, widget.DiffOptions{NoHeader: true, MaxLines: diffRows})
	return indentLines(lines, cell.Text("  "))
}

// newFileBlock shows what a write created, the way a diff shows a new file.
func (k *chatLook) newFileBlock(path, content string, width int) []cell.Line {
	lines := widget.Diff(path, "", content, max(width-2, 10), k.Theme, widget.DiffOptions{NoHeader: true, MaxLines: diffRows})
	return indentLines(lines, cell.Text("  "))
}

// outputLines is a result's text under the call line: behind the result mark, collapsed when long. An error is in the alarm style.
func (k *chatLook) outputLines(t doneTool, kind toolKind, width int, failed bool) ([]cell.Line, *expandable) {
	all := resultLines(t.res.Text, kind)
	if len(all) == 0 {
		return nil, nil
	}
	shown := all
	var exp *expandable
	folded := 0
	if len(all) > collapseAbove {
		folded = len(all) - collapseHead - collapseTail
		shown = append(append([]string(nil), all[:collapseHead]...), all[len(all)-collapseTail:]...)
		full := all
		if len(full) > expandLimit {
			full = full[:expandLimit]
		}
		exp = &expandable{title: toolTitle(t.call.ToolName) + " " + toolSummary(t.call.ToolName, t.call.Input, t.cwd), lines: full}
	}
	textSt := cell.Style{}
	if failed && t.res.IsError {
		textSt = k.st.bad
	} else if kind == kindCommand || kind == kindOther {
		textSt = cell.Style{}
	}
	first, pad := "  "+k.g.result+" ", strings.Repeat(" ", 2+cell.StringWidth(k.g.result)+1)
	avail := max(width-cell.StringWidth(first), 1)
	var out []cell.Line
	emit := func(text string, st cell.Style) {
		prefix := pad
		if len(out) == 0 {
			prefix = first
		}
		out = append(out, cell.Join(cell.Styled(k.st.dim, prefix), cell.Styled(st, cutCells(text, avail, k.g.ellipsis))))
	}
	for i, l := range shown {
		if folded > 0 && i == collapseHead {
			emit(fmt.Sprintf("%s +%d lines (ctrl+o to expand)", k.g.ellipsis, folded), k.st.dim)
		}
		emit(l, textSt)
	}
	return out, exp
}

// patchToUnified turns the text of an apply_patch call (the format models are trained on: *** Begin Patch, *** Update File: ...) into
// a unified diff the diff widget can lay out. It takes what it understands and leaves the rest: a patch it cannot read is "".
func patchToUnified(patch string) string {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	var b strings.Builder
	var adds []string
	flushAdd := func(path string) {
		if path == "" {
			return
		}
		b.WriteString("--- /dev/null\n+++ b/" + path + "\n")
		b.WriteString("@@ -0,0 +1," + strconv.Itoa(len(adds)) + " @@\n")
		for _, a := range adds {
			b.WriteString("+" + a + "\n")
		}
		adds = nil
	}
	addPath := ""
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "*** Begin Patch"), strings.HasPrefix(ln, "*** End Patch"), strings.HasPrefix(ln, "*** End of File"):
			flushAdd(addPath)
			addPath = ""
		case strings.HasPrefix(ln, "*** Add File:"):
			flushAdd(addPath)
			addPath = strings.TrimSpace(strings.TrimPrefix(ln, "*** Add File:"))
		case strings.HasPrefix(ln, "*** Delete File:"):
			flushAdd(addPath)
			addPath = ""
			p := strings.TrimSpace(strings.TrimPrefix(ln, "*** Delete File:"))
			b.WriteString("--- a/" + p + "\n+++ /dev/null\n")
		case strings.HasPrefix(ln, "*** Update File:"):
			flushAdd(addPath)
			addPath = ""
			p := strings.TrimSpace(strings.TrimPrefix(ln, "*** Update File:"))
			b.WriteString("--- a/" + p + "\n+++ b/" + p + "\n")
		case strings.HasPrefix(ln, "*** Move to:"):
			// the widget has no word for a rename of a patch without a header: the path it moves to is noted
			b.WriteString("rename to " + strings.TrimSpace(strings.TrimPrefix(ln, "*** Move to:")) + "\n")
		case addPath != "":
			adds = append(adds, strings.TrimPrefix(ln, "+"))
		case strings.HasPrefix(ln, "@@"):
			b.WriteString(ln + "\n")
		case strings.HasPrefix(ln, " "), strings.HasPrefix(ln, "-"), strings.HasPrefix(ln, "+"):
			b.WriteString(ln + "\n")
		}
	}
	flushAdd(addPath)
	return b.String()
}
