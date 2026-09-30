// The swarm cockpit, composed from the widgets of this package (docs/UX.md, "Watch"; swarm.png): a title bar, the horse and the fan
// of riders, the agent table, the gantt and the task board, the merge queue, mail, governor and heatmap, the live feed and the
// keys. One function from data and a size to lines; the layout that fits the size is chosen here.

package widget

import (
	"strconv"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// dashMode is how much of the cockpit the width allows.
type dashMode uint8

const (
	dashFull    dashMode = iota // 96 columns and more: as sketched, with the gantt and the heatmap
	dashCompact                 // 70 to 95: no gantt and no heatmap, three panels at the bottom
	dashNarrow                  // 60 to 69: two panels at the bottom
)

func dashModeFor(width int) dashMode {
	switch {
	case width >= 96:
		return dashFull
	case width >= 70:
		return dashCompact
	}
	return dashNarrow
}

// dashPlan is one layout of the cockpit: which bands it has and how big the flexible ones are.
type dashPlan struct {
	rows   int  // agents in the table (one row of which says how many more there are)
	feed   int  // lines of the live feed; 0 leaves it out
	panels bool // the bottom panels
	mid    bool // the gantt and the task board
	top    bool // the horse and the fan
	// horseRows is the tallest horse allowed, in rows.
	horseRows int
	// extra is blank rows added above the keys, to fill the screen.
	extra int
}

// Dashboard draws the swarm cockpit of the sketch swarm.png in width by height cells, for the frame of the animation (the horse's
// gait, the spinners, a mail arriving; any integer is a frame, and a pace that follows the load is worked out inside).
//
// What is shown follows the size. From 96 columns it is as sketched: title bar with the clock, the agents, the spend and the hit
// ratio; the horse and the fan of riders over the shared prefix and its clock; the agent table; the gantt of the last minute beside
// the task board; the merge queue, the mail, the governor and the heatmap of all the agents; the live feed; the keys. From 70 columns the gantt
// and the heatmap are left out, from 60 the governor too; below 60 columns, or below 8 rows, it is a plain one-column list.
//
// It is also fitted to the height. When there is not room for everything the live feed goes first (shrunk, then dropped), then the
// table and the lanes are shortened, then the bottom panels are dropped, then the gantt and the board, then the horse; the title
// bar, the table and the keys stay. Spare rows go to a bigger horse, a longer feed and more rows of the table. With more agents
// than rows the table keeps the stuck ones and says how many more there are, and the heatmap shows them all.
//
// The result is exactly height lines of at most width cells (a list may be shorter); a width or height <= 0 draws nothing. Text
// in the data is cleaned of control characters.
func Dashboard(d DashboardData, width, height int, frame int, p Palette) []cell.Line {
	if width <= 0 || height <= 0 {
		return nil
	}
	if width < 60 || height < 8 {
		return dashList(d, width, height, frame, p)
	}
	natural := min(len(d.Agents), 10)
	if natural < 1 {
		natural = 1
	}
	c := &dashCtx{d: d, width: width, frame: frame, p: p, top: map[int]dashTopEntry{}, table: map[int][]cell.Line{}, mid: map[int]dashBand{},
		feed: map[int][]cell.Line{}}

	// From the richest layout to the poorest: the feed shrinks and goes, the table and the lanes are cut down to five agents, and
	// only then is a band dropped (the panels, then the gantt and the board, then the horse); when even the table alone with
	// five agents does not fit, it is cut further.
	type size struct{ feed, rows int }
	structures := []dashPlan{{panels: true, mid: true, top: true}, {mid: true, top: true}, {top: true}, {}}
	sizes := []size{{4, natural}, {2, natural}, {0, natural}, {0, 6}, {0, 5}}
	var best []dashBand
	var plan dashPlan
search:
	for si, st := range structures {
		list := sizes
		if si == len(structures)-1 {
			list = append(append([]size{}, sizes...), size{0, 4}, size{0, 3}, size{0, 2}, size{0, 1})
		}
		for _, sz := range list {
			if sz.rows > natural {
				continue
			}
			pl := st
			pl.feed, pl.rows, pl.horseRows = sz.feed, sz.rows, 10
			if bands := c.bands(pl); dashMeasure(bands) <= height {
				best, plan = bands, pl
				break search
			}
		}
	}
	if best == nil {
		return dashList(d, width, height, frame, p)
	}

	// spare rows: the table back up, then the feed, then a bigger horse
	try := func(pl dashPlan) bool {
		if bands := c.bands(pl); dashMeasure(bands) <= height {
			best, plan = bands, pl
			return true
		}
		return false
	}
	for plan.rows < natural {
		next := plan
		next.rows++
		if !try(next) {
			break
		}
	}
	for _, f := range []int{2, 4} {
		if next := plan; f > plan.feed && next.rows > 0 {
			next.feed = f
			try(next)
		}
	}
	if plan.top {
		for _, h := range []int{13, 16} {
			next := plan
			next.horseRows = h
			try(next)
		}
	}
	if plan.extra = height - dashMeasure(best); plan.extra > 0 {
		best = c.bands(plan)
	}
	return dashAssemble(d, width, best, p)
}

// dashCtx is what one call of Dashboard works with. The layouts it tries share most of their bands, so what does not depend on the
// whole plan is made once and kept here, for this call only.
type dashCtx struct {
	d     DashboardData
	width int
	frame int
	p     Palette

	top    map[int]dashTopEntry // by the tallest horse allowed
	table  map[int][]cell.Line  // by rows and whether the heatmap is there to point to
	mid    map[int]dashBand     // by rows and whether there are panels under it
	feed   map[int][]cell.Line  // by lines
	panels *dashBand
	pw     []int
	ptitle []string
}

type dashTopEntry struct {
	band dashBand
	ok   bool
}

// bands are the bands of the cockpit for one plan.
func (c *dashCtx) bands(pl dashPlan) []dashBand {
	d, p := c.d, c.p
	iw := c.width - 2
	mode := dashModeFor(c.width)
	var bands []dashBand

	// the agents the table shows, and how many are left out
	rows := max(pl.rows, 1)
	shown, more := len(d.Agents), 0
	if len(d.Agents) > rows {
		shown = rows
		if rows >= 3 {
			shown = rows - 1
			more = len(d.Agents) - shown
		}
	}
	key := rows * 2
	if pl.panels {
		key++
	}
	agents := func() []AgentRow {
		idx := dashPick(d.Agents, shown)
		out := make([]AgentRow, len(idx))
		for i, k := range idx {
			out[i] = d.Agents[k]
		}
		return out
	}

	if pl.top {
		e, ok := c.top[pl.horseRows]
		if !ok {
			e.band, e.ok = dashTopBand(d, iw, pl.horseRows, c.frame, p)
			c.top[pl.horseRows] = e
		}
		if e.ok {
			bands = append(bands, e.band)
		}
	}

	table, ok := c.table[key]
	if !ok {
		table = AgentTable(agents(), iw-2, c.frame, p)
		if more > 0 {
			hint := "… and " + strconv.Itoa(more) + " more"
			if pl.panels && mode == dashFull {
				hint += " (all of them are in the heatmap)"
			}
			table = append(table, cell.Styled(p.dimSt(), hint))
		}
		c.table[key] = table
	}
	bands = append(bands, dashBand{widths: []int{iw}, titles: []string{"agents"}, lines: [][]cell.Line{table}, rule: true})

	if pl.panels && c.panels == nil {
		c.pw, c.ptitle = dashPanelWidths(len(d.Agents), iw, mode)
		b := dashPanelsBand(d, c.pw, c.ptitle, mode, c.frame, p)
		c.panels = &b
	}
	if pl.mid {
		b, ok := c.mid[key]
		if !ok {
			var below []int
			if pl.panels {
				below = c.pw
			}
			b = dashMidBand(d, agents(), iw, mode, below, p)
			c.mid[key] = b
		}
		bands = append(bands, b)
	}
	if pl.panels {
		bands = append(bands, *c.panels)
	}
	if pl.feed > 0 {
		lines, ok := c.feed[pl.feed]
		if !ok {
			lines = dashFeed(d.Feed, pl.feed, iw-2, p)
			c.feed[pl.feed] = lines
		}
		bands = append(bands, dashBand{widths: []int{iw}, titles: []string{"live feed"}, lines: [][]cell.Line{lines}, rule: true})
	}
	hints := make([]cell.Line, 0, pl.extra+1)
	for i := 0; i < pl.extra; i++ {
		hints = append(hints, nil)
	}
	hints = append(hints, dashHints(iw-2, p))
	bands = append(bands, dashBand{widths: []int{iw}, lines: [][]cell.Line{hints}})

	return bands
}

// dashMeasure is how many lines the bands take in the box: the top and the bottom, the bands, and the rules between them.
func dashMeasure(bands []dashBand) int {
	n := 2 + bands[0].tallest()
	sep := len(bands[0].widths) > 1
	for _, b := range bands[1:] {
		if b.rule || sep {
			n++
		}
		n += b.tallest()
		sep = len(b.widths) > 1
	}
	return n
}

// dashAssemble puts the bands in the box: the title bar as its top, the bands with the rules that cut them apart (and a rule
// wherever a band above ends in vertical lines, so that they end in a junction), and the bottom.
func dashAssemble(d DashboardData, width int, bands []dashBand, p Palette) []cell.Line {
	out := []cell.Line{dashTitleBar(d, width, bands[0].seps(), p)}
	out = append(out, bands[0].render(p)...)
	prev := bands[0].seps()
	for _, b := range bands[1:] {
		if b.rule || len(prev) > 0 {
			out = append(out, dashRule(width, "├", "┤", prev, b.seps(), b.widths, b.titles, p))
		}
		out = append(out, b.render(p)...)
		prev = b.seps()
	}
	return append(out, dashRule(width, "╰", "╯", prev, nil, nil, nil, p))
}

// dashTopBand is the horse with the fan of riders beside it; the horse is left out when the width holds none.
func dashTopBand(d DashboardData, iw, horseRows, frame int, p Palette) (dashBand, bool) {
	const fanMin = 26
	legs := dashLegs(d.Agents)
	hw := min(iw-fanMin-1, max(iw*2/5, 28))
	var horse []cell.Line
	if hw >= 28 {
		if d.NoAnim {
			horse = StandIn(hw-2, horseRows, p)
		} else {
			horse = GallopIn(legs, dashGait(frame, legs), hw-2, horseRows, p)
		}
	}
	if len(horse) == 0 {
		fan := dashFan(d, iw-2, p)
		return dashBand{widths: []int{iw}, lines: [][]cell.Line{fan}}, len(fan) > 0
	}
	fw := iw - hw - 1
	return dashBand{widths: []int{hw, fw}, lines: [][]cell.Line{horse, dashFan(d, fw-2, p)}}, true
}

// dashMidBand is the gantt beside the task board; without the width for a gantt it is the board alone. The line between the two is
// put where one of the panels below has its own, when one is near enough, so that the lines join instead of missing each other
// by a cell.
func dashMidBand(d DashboardData, agents []AgentRow, iw int, mode dashMode, below []int, p Palette) dashBand {
	if mode != dashFull {
		return dashBand{widths: []int{iw}, titles: []string{"task board"}, lines: [][]cell.Line{dashKanban(d.Kanban, iw-2, 8, p)}, rule: true}
	}
	w := dashSplit(iw, []int{55, 45}, []int{40, 34})
	best := -1
	x := 0
	for i := 0; i+1 < len(below); i++ {
		x += below[i]
		if x >= 40 && iw-x-1 >= 34 && (best < 0 || dashAbs(x-iw*55/100) < dashAbs(best-iw*55/100)) {
			best = x
		}
		x++
	}
	if best >= 0 {
		w = []int{best, iw - best - 1}
	}
	lanes := make([]Lane, len(agents))
	for i, a := range agents {
		lanes[i] = Lane{Name: a.ID, Color: a.RoleColor, Levels: a.Levels, First: a.LevelsFrom, Marks: a.Marks}
	}
	gantt := Gantt(lanes, w[0]-2, d.NowBucket, p)
	rows := max(len(gantt), 5)
	return dashBand{
		widths: w, titles: []string{"swarm gantt · last 60 s", "task board"}, rule: true,
		lines: [][]cell.Line{gantt, dashKanban(d.Kanban, w[1]-2, rows, p)},
	}
}

// dashKanban is the task board cut to maxRows lines: the columns that list their cards show the first ones, and the merged column,
// which only grows, shows the newest; every header still says how many there are.
func dashKanban(cols []KanbanCol, w, maxRows int, p Palette) []cell.Line {
	cut := make([]KanbanCol, len(cols))
	copy(cut, cols)
	for i := range cut {
		cut[i].Count = cut[i].count()
		if cut[i].Kind != ColMerged && len(cut[i].Cards) > maxRows-1 {
			cut[i].Cards = cut[i].Cards[:max(0, maxRows-1)]
		}
	}
	lines := Kanban(cut, w, p)
	for len(lines) > maxRows {
		shrunk := false
		for i := range cut {
			if cut[i].Kind == ColMerged && len(cut[i].Cards) > 0 {
				cut[i].Cards = cut[i].Cards[min(len(cut[i].Cards), len(cut[i].Cards)/8+1):]
				shrunk = true
			}
		}
		if !shrunk {
			break
		}
		lines = Kanban(cut, w, p)
	}
	return lines
}

func dashAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// dashPanelWidths are the widths and the names of the bottom panels for a width: the merge queue, the mail, the governor and,
// from the full width, the heatmap of every agent.
func dashPanelWidths(agents, iw int, mode dashMode) (widths []int, titles []string) {
	var weights, mins []int
	switch mode {
	case dashFull:
		weights, mins = []int{25, 30, 19, 26}, []int{22, 28, 20, 23}
		titles = []string{"merge queue", "mail", "governor", dashAgents(agents)}
	case dashCompact:
		weights, mins = []int{34, 40, 26}, []int{20, 24, 18}
		titles = []string{"merge queue", "mail", "governor"}
	default:
		weights, mins = []int{45, 55}, []int{22, 26}
		titles = []string{"merge queue", "mail"}
	}
	return dashSplit(iw, weights, mins), titles
}

// dashPanelsBand is the row of panels under the gantt, in the widths dashPanelWidths gave.
func dashPanelsBand(d DashboardData, w []int, titles []string, mode dashMode, frame int, p Palette) dashBand {
	rows := 5
	merge := MergeQueue(d.Merge, frame, w[0]-2, p)
	var gov, heat []cell.Line
	if mode != dashNarrow {
		gov = dashGovernor(d, w[2]-2, p)
	}
	if mode == dashFull {
		states := make([]AgentState, len(d.Agents))
		for i, a := range d.Agents {
			states[i] = a.State
		}
		cw := w[3] - 2
		heat = Heatmap(states, (cw+1)/2, p)
	}
	for _, n := range []int{len(merge), len(heat), len(gov)} {
		rows = max(rows, n)
	}
	rows = min(rows, 10)
	if len(merge) > rows { // the counts on the last line matter more than the tail of the list
		merge = append(merge[:rows-1:rows-1], merge[len(merge)-1])
	}
	lines := [][]cell.Line{merge, dashMail(d, rows, w[1]-2, frame, p)}
	if mode != dashNarrow {
		lines = append(lines, gov)
	}
	if mode == dashFull {
		lines = append(lines, heat)
	}
	for i := range lines { // no panel makes the band taller than its budget
		if len(lines[i]) > rows {
			lines[i] = lines[i][:rows]
		}
	}
	return dashBand{widths: w, titles: titles, lines: lines, rule: true}
}
