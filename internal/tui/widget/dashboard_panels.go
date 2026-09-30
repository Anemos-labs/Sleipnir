// The panels of the cockpit that are not widgets of their own: the title bar, the prefix fan with its clock, the governor, the live
// feed and the key hints, and what is derived from the agents (legs of the horse, riders of the fan, the ones to show).

package widget

import (
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// dashMoney is a sum of dollars as the headline and the governor write it: $0.31, $20, $0.0004, $123. Negative and NaN are $0.
func dashMoney(f float64) string {
	if f != f || f < 0 {
		f = 0
	}
	switch {
	case f >= 1e9:
		return "$1e9+"
	case f == float64(int64(f)):
		return "$" + strconv.FormatInt(int64(f), 10)
	case f < 0.01:
		return "$" + strconv.FormatFloat(f, 'f', 4, 64)
	case f < 100:
		return "$" + strconv.FormatFloat(f, 'f', 2, 64)
	}
	return "$" + strconv.FormatFloat(f, 'f', 0, 64)
}

// dashCount writes a small number in words, the way the sketch does ("one prefix, eight riders"), and larger ones in digits.
func dashCount(n int) string {
	words := [...]string{"no", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"}
	if n >= 0 && n < len(words) {
		return words[n]
	}
	return strconv.Itoa(n)
}

// dashAgents is how many agents there are, as the title bar and the list write it: "8 agents", "1 agent".
func dashAgents(n int) string {
	if n == 1 {
		return "1 agent"
	}
	return strconv.Itoa(max(n, 0)) + " agents"
}

// dashPick chooses which k of the agents to show: the ones that are stuck first (they need someone), then the others in the order
// given; the result is in the order given too, so rows do not jump about.
func dashPick(agents []AgentRow, k int) []int {
	n := len(agents)
	if k >= n {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	if k <= 0 {
		return nil
	}
	keep := make([]bool, n)
	left := k
	for i, a := range agents {
		if a.State.valid() == StateStuck && left > 0 {
			keep[i] = true
			left--
		}
	}
	for i := range agents {
		if !keep[i] && left > 0 {
			keep[i] = true
			left--
		}
	}
	var idx []int
	for i, ok := range keep {
		if ok {
			idx = append(idx, i)
		}
	}
	return idx
}

// dashLegs are the eight legs of the horse: leg k is the agent k, and with more than eight agents they share the legs round
// robin, a leg being busy while any of its agents runs a tool or edits.
func dashLegs(agents []AgentRow) []Leg {
	legs := make([]Leg, 8)
	for k := range legs {
		legs[k].Color = k
	}
	for i, a := range agents {
		l := &legs[i%8]
		if i < 8 {
			l.Color = a.RoleColor
		}
		if s := a.State.valid(); s == StateTool || s == StateEdit {
			l.Busy = true
		}
	}
	return legs
}

// dashGait is the frame of the gait: the horse's pace follows the number of busy legs (a step every 8/3 frames for one leg, every
// 0.8 for all eight), in integers so that it is the same everywhere. The frame is reduced modulo 32 first, which is a whole number
// of gaits at every pace: the animation repeats exactly, for negative frames and for huge ones too (no overflow).
func dashGait(frame int, legs []Leg) int {
	busy := 0
	for _, l := range legs {
		if l.Busy {
			busy++
		}
	}
	return ((frame%32 + 32) % 32) * (2 + busy) / 8
}

// dashTitleBar is the top border of the box: the name and the task on the left, the clock, the agents, the spend and the hit
// ratio on the right, and a line between. What does not fit is cut: first the right side piece by piece, then the task.
func dashTitleBar(d DashboardData, width int, below []int, p Palette) cell.Line {
	agents := dashAgents(len(d.Agents))
	spend := dashMoney(d.Spend)
	if d.Budget > 0 {
		spend += "/" + dashMoney(d.Budget)
	}
	stats := []string{"◷ " + showElapsed(d.Elapsed), agents, spend, "⛁ " + showPercent(d.HitRatio)}
	if st := showClean(d.Status); st != "" {
		stats = append([]string{st}, stats...)
	}
	return frameTitleBar(d.Title, stats, width, below, p)
}

// frameTitleBar is the top border of a full-screen view: SLEIPNIR and the task on the left, the stats on the right, dropped from
// the end when they do not fit.
func frameTitleBar(task string, stats []string, width int, below []int, p Palette) cell.Line {
	c := newShowCanvas(width)
	faint := p.faintSt()
	c.put(0, "╭", faint)
	c.fill(1, width-2, "─", faint)
	c.put(width-1, "╮", faint)
	for _, x := range below {
		c.put(x+1, "┬", faint)
	}

	// leave out what does not fit: the last stat first
	right := ""
	for n := len(stats); n > 0; n-- {
		if r := " " + strings.Join(stats[:n], " · ") + " "; cell.StringWidth(r) <= width-14 {
			right = r
			break
		}
	}
	rw := cell.StringWidth(right)
	c.put(width-1-rw-1, right, cell.Style{})

	room := width - 2 - rw - 1
	head := " SLEIPNIR "
	if room > cell.StringWidth(head)+2 {
		c.put(1, head, p.accentSt().With(cell.Bold))
		task := showClean(task)
		if task != "" {
			c.put(1+cell.StringWidth(head), "▸ ", p.dimSt())
			c.put(1+cell.StringWidth(head)+2, showTrunc(task, room-cell.StringWidth(head)-3)+" ", cell.Style{})
		}
	} else if room > 2 {
		c.put(1, showTrunc(" SLEIPNIR ", room), p.accentSt().With(cell.Bold))
	}
	return c.line()
}

// dashFan is the panel of the prefix: its title, the prefix and its clock, and the fan of riders.
func dashFan(d DashboardData, w int, p Palette) []cell.Line {
	if w < 8 {
		return nil
	}
	var out []cell.Line
	n := len(d.Agents)
	noun := " riders"
	if n == 1 {
		noun = " rider"
	}
	out = append(out, showFit(cell.Styled(cell.Style{Attr: cell.Bold}, "one prefix, "+dashCount(n)+noun), w))

	var tokens int
	layers := make([]string, 0, 2)
	for i, l := range d.Shared {
		tokens += showTok(l.Tokens)
		if l.Tokens > 0 {
			layers = append(layers, l.label(i))
		}
	}
	var b showRowBuf
	b.add(cell.Style{Attr: cell.Bold}, "shared")
	if len(layers) > 0 {
		b.add(cell.Style{Attr: cell.Bold}, " "+layers[0])
		if len(layers) > 1 {
			b.add(cell.Style{Attr: cell.Bold}, "–"+layers[len(layers)-1])
		}
	}
	b.add(p.dimSt(), " "+showTokens(tokens)+" tokens")
	if d.PrefixTTL > 0 || d.PrefixLeft > 0 {
		if room := w - b.w - 2; room >= 6 {
			b.space(2).addLine(TTL(d.PrefixLeft, d.PrefixTTL, room, p))
		}
	}
	out = append(out, showFit(b.line(), w), nil)

	riders := make([]string, n)
	weights := make([]int, n)
	for i, a := range d.Agents {
		riders[i] = a.ID
		weights[i] = int(float64(showTok(a.Shared)) * showUnit(a.Hit))
	}
	return append(out, FanShared(d.Shared, riders, weights, w, p)...)
}

// dashMeter is a little meter of ▰ and ▱ for a ratio, green while there is room and amber and red as it fills.
func dashMeter(ratio float64, cells int, p Palette) cell.Line {
	ratio = showUnit(ratio)
	on := showDiv(int64(showPermille(ratio))*int64(cells), 1000)
	if ratio > 0 && on == 0 {
		on = 1
	}
	st := p.goodSt()
	switch {
	case ratio >= 0.9:
		st = p.badSt()
	case ratio >= 0.7:
		st = p.warnSt().With(cell.Bold)
	}
	var b showRowBuf
	return b.add(st, showRepeat("▰", on)).add(p.faintSt(), showRepeat("▱", cells-on)).line()
}

// dashGovernor is the panel of the governor: the request rate, the rate-limit answers and retries, the spend against the
// budget, how long the shared prefix will last, and how much faster than serial the swarm is.
func dashGovernor(d DashboardData, w int, p Palette) []cell.Line {
	var out []cell.Line
	g := d.Governor
	meter := func(label string, ratio float64, value string) cell.Line {
		var b showRowBuf
		b.add(p.dimSt(), label).add(cell.Style{}, value)
		if room := w - b.w - 1; room >= 4 {
			b.space(1).addLine(dashMeter(ratio, min(room, 10), p))
		}
		return showFit(b.line(), w)
	}
	if g.RPMLimit > 0 {
		out = append(out, meter("rpm ", float64(g.RPM)/float64(g.RPMLimit), strconv.Itoa(g.RPM)+"/"+strconv.Itoa(g.RPMLimit)))
	} else {
		out = append(out, showFit(cell.Styled(p.dimSt(), "rpm "+strconv.Itoa(g.RPM)), w))
	}
	var b showRowBuf
	b.add(p.dimSt(), "429s ")
	if g.Err429 > 0 {
		b.add(p.badSt(), strconv.Itoa(g.Err429))
	} else {
		b.add(p.goodSt(), "0")
	}
	b.add(p.dimSt(), "  retries ").add(cell.Style{}, strconv.Itoa(g.Retries))
	out = append(out, showFit(b.line(), w))
	if d.Budget > 0 {
		out = append(out, meter("", d.Spend/d.Budget, dashMoney(d.Spend)+"/"+dashMoney(d.Budget)))
	} else {
		out = append(out, showFit(cell.Styled(cell.Style{}, dashMoney(d.Spend)), w))
	}
	if d.PrefixTTL > 0 || d.PrefixLeft > 0 {
		out = append(out, showFit(cell.Join(cell.Styled(p.dimSt(), "shared prefix "), cell.Styled(p.warnSt(), showClock(d.PrefixLeft))), w))
	}
	if g.Speedup > 0 {
		x := "≈ " + strconv.FormatFloat(g.Speedup, 'f', -1, 64) + "×"
		text := x
		for _, try := range []string{x + " faster than serial", x + " faster"} {
			if cell.StringWidth(try) <= w {
				text = try
				break
			}
		}
		out = append(out, showFit(cell.Styled(p.dimSt(), text), w))
	}
	return out
}

// feedGlyph is the glyph and style of a kind of feed line.
func feedGlyph(k FeedKind, p Palette) (string, cell.Style) {
	switch k {
	case FeedOK:
		return "✓", p.goodSt()
	case FeedWarn:
		return "⚠", p.badSt()
	case FeedCompact:
		return "◆", p.accentSt().With(cell.Bold)
	case FeedMail:
		return "✉", p.warnSt()
	}
	return "·", p.dimSt()
}

// dashFeed is the n newest lines of the live feed (the feed is newest first), each: the time, who, a glyph for what kind of event,
// what happened and, at the right edge, the small print. The text is cut before the small print is.
func dashFeed(feed []FeedLine, n, w int, p Palette) []cell.Line {
	var out []cell.Line
	for i := 0; i < n && i < len(feed); i++ {
		f := feed[i]
		var b showRowBuf
		g, gst := feedGlyph(f.Kind, p)
		b.add(p.dimSt(), showElapsed(f.At)).add(cell.Style{}, " ").add(p.roleSt(f.Color), showTrunc(showClean(f.Agent), 6)).add(cell.Style{}, " ").add(gst, g+" ")
		text, tail := showClean(f.Text), showClean(f.Tail)
		if tail != "" && b.w+cell.StringWidth(text)+2+cell.StringWidth(tail) > w {
			tail = "" // the text matters more
		}
		b.add(cell.Style{}, showTrunc(text, w-b.w))
		if tail != "" {
			b.space(w-b.w-cell.StringWidth(tail)).add(p.dimSt(), tail)
		}
		out = append(out, showFit(b.line(), w))
	}
	return out
}

// Hint is one entry of the row of keys at the bottom of the cockpit: the text as shown ("p pause") and how long it is kept when the
// row is too narrow for all of them (a higher Rank is dropped sooner; 1 is the last to go).
type Hint struct {
	Text string
	Rank int
}

// defaultHints are the keys of the cockpit as designed (docs/UX.md); a program that does not have all of them says which it has
// in DashboardData.Hints.
var defaultHints = []Hint{{"↑↓ agent", 3}, {"enter transcript", 4}, {"m mail", 5}, {"b board", 6}, {"c cache", 7}, {"t stack", 8}, {"p pause", 9}, {"? help", 2}, {"q back to shell", 1}}

// dashHints is the row of keys at the bottom. Whatever does not fit is left out whole, the least important first.
func dashHints(hints []Hint, w int, p Palette) cell.Line {
	if hints == nil {
		hints = defaultHints
	}
	keep := make([]bool, len(hints))
	for i := range keep {
		keep[i] = true
	}
	width := func() int {
		n, sep := 0, 0
		for i, h := range hints {
			if keep[i] {
				n += cell.StringWidth(h.Text) + sep
				sep = 3
			}
		}
		return n
	}
	for width() > w {
		worst := -1
		for i, h := range hints {
			if keep[i] && (worst < 0 || h.Rank > hints[worst].Rank) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		keep[worst] = false
	}
	var b showRowBuf
	for i, h := range hints {
		if !keep[i] {
			continue
		}
		if b.w > 0 {
			b.add(p.dimSt(), " · ")
		}
		b.add(p.dimSt(), h.Text)
	}
	return showFit(b.line(), w)
}

// dashMail is the panel of mail, rows lines tall: the newest messages that fit and, on the last line, the router's counts.
func dashMail(d DashboardData, rows, w int, frame int, p Palette) []cell.Line {
	s := d.MailStats
	counts := s.Routed+s.Delivered+s.Dup+s.Ignored > 0 && rows > 2
	room := rows
	if counts {
		room--
	}
	mails := d.Mail
	if len(mails) > room {
		mails = mails[len(mails)-room:]
	}
	out := MailFlow(mails, frame, w, p)
	if counts {
		for len(out) < rows-1 {
			out = append(out, nil)
		}
		txt := "routed " + strconv.Itoa(s.Routed) + " · delivered " + strconv.Itoa(s.Delivered) + " · dup " + strconv.Itoa(s.Dup) + " · ignored " + strconv.Itoa(s.Ignored)
		if cell.StringWidth(txt) > w {
			txt = "routed " + strconv.Itoa(s.Routed) + " · dup " + strconv.Itoa(s.Dup) + " · ignored " + strconv.Itoa(s.Ignored)
		}
		out = append(out, showFit(cell.Styled(p.dimSt(), txt), w))
	}
	return out
}
