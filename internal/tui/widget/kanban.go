// The task board: todo, running, verifying, merged, with a card for every task (docs/UX.md, "the task board"; swarm.png).

package widget

import (
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// ColKind is which column of the board: it decides the card's glyph and the colour of the header.
type ColKind uint8

const (
	// ColTodo is a task nobody has started (▢).
	ColTodo ColKind = iota
	// ColRunning is a task an agent is working on (▣).
	ColRunning
	// ColVerifying is a finished task being rebased and tested before the merge (◌).
	ColVerifying
	// ColMerged is a task that is in (✓).
	ColMerged
)

func (k ColKind) title() string {
	switch k {
	case ColRunning:
		return "running"
	case ColVerifying:
		return "verifying"
	case ColMerged:
		return "merged"
	}
	return "todo"
}

// short is the name that still fits when the column is narrow.
func (k ColKind) short() string {
	switch k {
	case ColRunning:
		return "run"
	case ColVerifying:
		return "verify"
	}
	return k.title()
}

func (k ColKind) glyph() string {
	switch k {
	case ColRunning:
		return "▣"
	case ColVerifying:
		return "◌"
	case ColMerged:
		return "✓"
	}
	return "▢"
}

func (k ColKind) style(p Palette) (header, card cell.Style) {
	switch k {
	case ColRunning:
		return p.goodSt().With(cell.Bold), cell.Style{}
	case ColVerifying:
		return p.infoSt().With(cell.Bold), p.infoSt()
	case ColMerged:
		return p.layerSt(1).With(cell.Bold), p.goodSt()
	}
	return p.warnSt().With(cell.Bold), p.dimSt()
}

// KanbanCard is a task.
type KanbanCard struct {
	ID    string // t3
	Label string // what it is about: ord
	// Failed is a task whose verification failed and that was bounced back: it stays in its column with ✗ in the alarm style.
	Failed bool
}

// KanbanCol is a column of the board.
type KanbanCol struct {
	Kind ColKind
	// Title overrides the column's name (todo, running, verifying, merged).
	Title string
	Cards []KanbanCard
	// Count is the number in the header when it is not the number of cards: a list that was cut to what fits (the newest cards of
	// a long merged column) still says how many there are. Zero means len(Cards).
	Count int
}

// title prefers a nonempty sanitized column title and otherwise uses the column kind's default
// title.
func (c KanbanCol) title() string {
	if t := showClean(c.Title); t != "" {
		return t
	}
	return c.Kind.title()
}

// count is the number the header shows.
func (c KanbanCol) count() int {
	if c.Count > 0 {
		return c.Count
	}
	return len(c.Cards)
}

// Kanban draws the columns side by side in width cells: a header with the name and the number of cards, and a card under it for
// every task: ▢ waiting, ▣ running, ◌ verifying, ✓ merged, ✗ failed (the glyph is the state; colour only backs it up).
//
//	todo 3        running 4     verifying 1    merged 5
//	▢ t9  docs    ▣ t3  ord     ◌ t7           ✓ t1 ✓ t2
//	▢ t10 e2e     ▣ t4  usr                    ✓ t4 ✓ t5
//	▢ t11 doc     ▣ t5  web                    ✓ t6
//
// The merged column packs its cards side by side, since it only grows; the others list one card a line with its label, cut to
// fit. When the columns would be narrower than a short card the board is a line of counts (todo 3 · running 4 · ...) cut to
// width. The height is the longest column and no line is wider than width. No columns, or a width <= 0, draws nothing.
func Kanban(cols []KanbanCol, width int, p Palette) []cell.Line {
	n := len(cols)
	if n == 0 || width <= 0 {
		return nil
	}
	const gap = 2
	cw := (width - gap*(n-1)) / n
	if cw < 7 {
		var b showRowBuf
		for i, c := range cols {
			if i > 0 {
				b.add(p.dimSt(), " · ")
			}
			h, _ := c.Kind.style(p)
			b.add(h, c.title()+" "+strconv.Itoa(c.count()))
		}
		return []cell.Line{showFit(b.line(), width)}
	}

	blocks := make([][]cell.Line, n)
	height := 0
	for i, c := range cols {
		hs, cs := c.Kind.style(p)
		head := c.title() + " " + strconv.Itoa(c.count())
		if cell.StringWidth(head) > cw && showClean(c.Title) == "" { // a column that is not named by hand has a short name
			head = c.Kind.short() + " " + strconv.Itoa(c.count())
		}
		blocks[i] = append(blocks[i], showFit(cell.Styled(hs, head), cw))
		idW := 0
		for _, cd := range c.Cards {
			idW = max(idW, cell.StringWidth(showClean(cd.ID)))
		}
		if c.Kind == ColMerged {
			// the cards flow from left to right, as many as fit in a row
			var row showRowBuf
			for _, cd := range c.Cards {
				text, st := kanbanCard(c.Kind, cd, 0, cs, p)
				if row.w > 0 && row.w+1+cell.StringWidth(text) > cw {
					blocks[i] = append(blocks[i], showFit(row.line(), cw))
					row = showRowBuf{}
				}
				if row.w > 0 {
					row.space(1)
				}
				row.add(st, text)
			}
			if row.w > 0 {
				blocks[i] = append(blocks[i], showFit(row.line(), cw))
			}
		} else {
			for _, cd := range c.Cards {
				text, st := kanbanCard(c.Kind, cd, idW, cs, p)
				blocks[i] = append(blocks[i], showFit(cell.Styled(st, text), cw))
			}
		}
		height = max(height, len(blocks[i]))
	}
	out := make([]cell.Line, height)
	for y := range out {
		var b showRowBuf
		for i := range blocks {
			if i > 0 {
				b.space(gap)
			}
			var l cell.Line
			if y < len(blocks[i]) {
				l = blocks[i][y]
			}
			b.addLine(l).space(cw - l.Width())
		}
		out[y] = horseTrimBlank(b.line())
	}
	return out
}

// kanbanCard is a card as text and its style: the glyph of its column (✗ if it failed), its id padded to idW cells and, when
// idW says there are labels to line up, its label.
func kanbanCard(kind ColKind, c KanbanCard, idW int, base cell.Style, p Palette) (string, cell.Style) {
	g, st := kind.glyph(), base
	if c.Failed {
		g, st = "✗", p.badSt()
	}
	id := showClean(c.ID)
	if l := showClean(c.Label); idW > 0 && l != "" {
		return g + " " + showPadR(id, idW) + " " + l, st
	}
	return g + " " + id, st
}
