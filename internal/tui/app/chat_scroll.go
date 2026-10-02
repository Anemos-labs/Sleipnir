package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// The blocks the chat prints into the scrollback. Each is a pure function of what happened and of the width: the program prints
// the lines once and never draws them again, so a block is the record.

// bannerLines is the start of the chat: what this is, which model, where, and how to get about.
func (k *chatLook) bannerLines(info ChatInfo, width int) []cell.Line {
	// The line is brand, version, model, directory, budget, team. What cannot be left out is the brand, the model and the size of the team
	// (nothing else says the chat is one); the budget comes next, then the version, and the directory takes what room is left, keeping its
	// tail, which is the part that says which project this is. On a narrow screen the parts that do not fit are dropped, not cut off the end.
	brand := k.g.compact + " sleipnir"
	var model, budget, team, version string
	if m := clean(info.Model); m != "" {
		model = "  " + m
	}
	if info.Agents > 1 {
		team = "  " + k.g.dot + " team of " + strconv.Itoa(info.Agents)
	}
	if b := clean(info.Budget); b != "" {
		budget = "  " + k.g.dot + " " + b
	}
	if v := clean(info.Version); v != "" {
		version = " " + v
	}
	room := width - cell.StringWidth(brand+model+team)
	fits := func(s string) string {
		if w := cell.StringWidth(s); s != "" && w <= room {
			room -= w
			return s
		}
		return ""
	}
	budget, version = fits(budget), fits(version)
	var cwd string
	if c := clean(info.Cwd); c != "" && room >= 10 {
		cwd = "  " + k.shortenPath(c, room-2)
	}
	var l1 row
	l1.add(k.st.accent, brand).add(k.st.dim, version)
	if model != "" {
		l1.add(cell.Style{}, "  ").add(k.st.info, strings.TrimPrefix(model, "  "))
	}
	l1.add(k.st.dim, cwd).add(k.st.dim, budget).add(k.st.dim, team)
	out := []cell.Line{k.fit(l1.line(), width)}
	if info.Resumed != "" {
		out = append(out, paragraph(k.st.dim, "  "+info.Resumed, width)...)
		for _, l := range info.Recap {
			out = append(out, paragraph(k.st.dim, "  "+clean(l), width)...)
		}
	}
	help := "Type a goal, / for commands, @ for files. Esc interrupts a turn; Ctrl-C twice at the prompt, Ctrl-D or /exit quits."
	out = append(out, paragraph(k.st.dim, help, width)...)
	return out
}

// shortenPath cuts a directory to at most max cells, keeping its tail and starting it at a separator where it can ("…/clients/acme/app").
func (k *chatLook) shortenPath(p string, max int) string {
	if cell.StringWidth(p) <= max {
		return p
	}
	rs := []rune(p)
	for len(rs) > 1 && cell.StringWidth(k.g.ellipsis)+cell.StringWidth(string(rs)) > max {
		rs = rs[1:]
	}
	tail := string(rs)
	if i := strings.IndexAny(tail, `/\`); i > 0 && i < len(tail)/2 {
		tail = tail[i:] // not half a directory name
	}
	return k.g.ellipsis + tail
}

// promptLines is what the person sent, as the scrollback keeps it: behind the prompt mark, bold, every line under the first indented
// to it. A very long prompt (a paste that was expanded) is cut, with the number of lines left out.
func (k *chatLook) promptLines(text string, width int) []cell.Line {
	lines := textLines(text)
	if len(lines) == 0 {
		return nil
	}
	const maxRows = 12
	more := 0
	if len(lines) > maxRows {
		more = len(lines) - maxRows
		lines = lines[:maxRows]
	}
	mark := k.g.prompt + " "
	pad := strings.Repeat(" ", cell.StringWidth(mark))
	bold := cell.Style{Attr: cell.Bold}
	var out []cell.Line
	for i, ln := range lines {
		prefix, st := pad, cell.Style{}
		if i == 0 {
			prefix, st = mark, k.accent()
		}
		out = append(out, wrapWithPrefix(cell.Styled(st, prefix), pad, cell.Styled(bold, ln), width)...)
	}
	if more > 0 {
		out = append(out, cell.Styled(k.st.dim, pad+fmt.Sprintf("%s +%d lines", k.g.ellipsis, more)))
	}
	return out
}

// wrapWithPrefix lays text out behind a prefix, wrapping at width with pad in front of every row but the first.
func wrapWithPrefix(prefix cell.Line, pad string, text cell.Line, width int) []cell.Line {
	pw := prefix.Width()
	rows := text.Wrap(max(width-pw, 1), 0)
	out := make([]cell.Line, len(rows))
	for i, r := range rows {
		if i == 0 {
			out[i] = cell.Join(prefix, r)
		} else {
			out[i] = cell.Join(cell.Text(pad), r)
		}
	}
	return out
}

// answerLines puts the assistant's markdown (already laid out for width-2 cells) behind its bullet: the first line has it, the
// rest are indented to it. from is the number of lines of the message that are already in the scrollback, which is how a message
// that streams is printed in pieces that join up: line i of the message always gets what line i would.
func (k *chatLook) answerLines(md []cell.Line, from int) []cell.Line {
	out := make([]cell.Line, 0, len(md)-min(from, len(md)))
	for i := from; i < len(md); i++ {
		switch {
		case i == 0:
			out = append(out, cell.Join(cell.Styled(k.st.dim, k.g.bullet+" "), md[i]))
		case len(md[i]) == 0:
			out = append(out, nil)
		default:
			out = append(out, cell.Join(cell.Text("  "), md[i]))
		}
	}
	return out
}

// noticeLines is a notice of the harness or of an agent: a warning, an error, or (with --verbose) information. The message came
// from wherever the harness got it, so it is cleaned like any text, and it is wrapped with a hanging indent.
func (k *chatLook) noticeLines(agent, level, msg string, width int, main bool) []cell.Line {
	st, glyph := k.st.dim, k.g.dot
	switch strings.ToLower(level) {
	case "warn", "warning":
		st, glyph = k.st.warn, k.g.warn
	case "error":
		st, glyph = k.st.bad, k.g.fail
	}
	lead := "  " + glyph + " "
	if !main && agent != "" {
		lead += "[" + k.agentTag(agent) + "] "
	}
	pad := strings.Repeat(" ", cell.StringWidth(lead))
	var out []cell.Line
	for i, ln := range textLines(msg) {
		pre := pad
		if i == 0 {
			pre = lead
		}
		out = append(out, wrapWithPrefix(cell.Styled(st, pre), pad, cell.Styled(cell.Style{Attr: k.noticeAttr(level)}, ln), width)...)
	}
	return out
}

func (k *chatLook) noticeAttr(level string) cell.Attr {
	if strings.EqualFold(level, "info") || level == "" {
		return cell.Dim
	}
	return 0
}

// foldRecord is the line a compaction leaves in the scrollback: how much the thread was before and is now, what it cost, and whether
// the cache was cold (so the rewrite was free) or warm (so it is a declared, priced rebase).
func (k *chatLook) foldRecord(c state.Compaction, who string, width int) []cell.Line {
	var r row
	r.add(k.st.accent, k.g.compact+" compacted ")
	if who != "" {
		r.add(k.st.dim, "["+k.agentTag(who)+"] ")
	}
	full, small := k.foldGlyphs()
	r.add(cell.Style{}, widget.Tokens(c.Before)).add(k.st.accent.Without(cell.Bold), " "+strings.Repeat(full, foldWidth)+" ")
	r.add(k.st.dim, k.g.arrow+" ").add(k.st.accent.Without(cell.Bold), strings.Repeat(small, foldCells(c.Before, c.After, 1, foldWidth))+" ").add(cell.Style{}, widget.Tokens(c.After))
	if c.Before > c.After && c.Before > 0 {
		r.add(k.st.dim, fmt.Sprintf("  %s%d%%", minusSign(k), (c.Before-c.After)*100/c.Before))
	}
	var bits []string
	if c.SpineAdded > 0 {
		bits = append(bits, "spine +1 resume")
	}
	switch c.Moment {
	case "cold":
		bits = append(bits, "cache rewritten while cold: free")
	case "warm":
		bits = append(bits, "a declared, priced rebase")
	}
	if len(bits) > 0 {
		r.add(k.st.dim, "  "+k.g.dot+" "+strings.Join(bits, " "+k.g.dot+" "))
	}
	return []cell.Line{k.fit(r.line(), width)}
}

// foldWidth is how many cells the thread takes in a fold, whatever its size: the bar is a picture of the ratio.
const foldWidth = 12

// foldGlyphs are the thread's block and the resume's.
func (k *chatLook) foldGlyphs() (thread, resume string) {
	if k.Unicode {
		return "▓", "▒"
	}
	return "#", "="
}

// foldCells is how many of limit cells the thread takes when it has shrunk from before to after tokens as far as progress (0 to 1)
// says: it is always one cell at least.
func foldCells(before, after int, progress float64, limit int) int {
	if before <= 0 || limit <= 0 {
		return 0
	}
	after = min(max(after, 0), before)
	cur := float64(before) - float64(before-after)*easeOut(progress)
	return min(limit, max(1, int(cur*float64(limit)/float64(before)+0.5)))
}

// foldTokens is the size of the thread as progress has brought it.
func foldTokens(before, after int, progress float64) int {
	after = min(max(after, 0), before)
	return int(float64(before) - float64(before-after)*easeOut(progress) + 0.5)
}

// easeOut is quick at first and slow at the end: the thread collapses, then settles.
func easeOut(p float64) float64 {
	if p <= 0 {
		return 0
	}
	if p >= 1 {
		return 1
	}
	return 1 - (1-p)*(1-p)
}

// anomalyLines is a cache break in the scrollback: which layer diverged, what was expected of the cache and what it gave, and what
// the miss cost at list price.
func (k *chatLook) anomalyLines(a state.Anomaly, who string, width int) []cell.Line {
	var r row
	r.add(k.st.bad, k.g.warn+" cache break")
	if who != "" {
		r.add(k.st.dim, " ["+k.agentTag(who)+"]")
	}
	if a.Layer != "" {
		r.add(k.st.dim, " in ").add(cell.Style{}, clean(a.Layer))
	}
	if a.Kind != "" && a.Kind != "drift" {
		r.add(k.st.dim, " ("+clean(a.Kind)+")")
	}
	if a.Expected > 0 {
		r.add(k.st.dim, fmt.Sprintf(" %s read %s of %s expected", k.g.dot, widget.Tokens(a.Actual), widget.Tokens(a.Expected)))
	}
	if a.MissKnown && a.MissUSD > 0 {
		r.add(k.st.dim, " "+k.g.dot+" cost ").add(k.st.warn, widget.USD(a.MissUSD))
	}
	out := []cell.Line{k.fit(r.line(), width)}
	n := clean(a.Note)
	if n == "" && a.Layer == "" && a.Kind == "low_hit" {
		// The guard found no layer that changed, so the prompt was what it had been: whose miss it is.
		n = "the prompt prefix did not change: the endpoint did not serve it"
	}
	if n != "" {
		out = append(out, paragraph(k.st.dim, "  "+n, width)...)
	}
	return out
}

// turnSummary is the record a turn leaves: how long it took, how many steps and what it cost. What the cache did is on the stats page.
func (k *chatLook) turnSummary(elapsed time.Duration, res TurnResult, width int) cell.Line {
	parts := []string{duration(elapsed.Round(time.Second))}
	if res.Steps > 0 {
		parts = append(parts, count(res.Steps, "step", "steps"))
	}
	parts = append(parts, widget.USD(res.CostUSD))
	text := k.g.rule + " " + strings.Join(parts, " "+k.g.dot+" ")
	return k.fit(cell.Styled(k.st.dim, text), width)
}
