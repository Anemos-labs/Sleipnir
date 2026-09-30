// The merge queue: finished work moving through rebase, verify and merge, and a failure bounced back to its worker with the
// reason (docs/UX.md, "Merge"; swarm.png).

package widget

import (
	"strconv"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// MergeStage is where a task is on its way into the main line.
type MergeStage uint8

const (
	// MergeQueued waits for its turn.
	MergeQueued MergeStage = iota
	// MergeRebase is being rebased on what has been merged meanwhile.
	MergeRebase
	// MergeVerify is being checked: the build and the tests, in a clean checkout.
	MergeVerify
	// MergeMerge is being merged.
	MergeMerge
	// MergeDone is merged.
	MergeDone
)

func (s MergeStage) word() string {
	switch s {
	case MergeRebase:
		return "rebase"
	case MergeVerify:
		return "verify"
	case MergeMerge:
		return "merge"
	}
	return "queued"
}

// MergeItem is a task in the queue.
type MergeItem struct {
	ID    string
	Stage MergeStage
	// Failed says the task was stopped at Stage and bounced back to its worker.
	Failed bool
	// Note is what it is doing now (the command being run) or, for a failed one, why it failed.
	Note string
	// Worker is the agent a failed task went back to.
	Worker string
}

// showSpin is one frame of the braille spinner; any frame number is a frame (it wraps around).
func showSpin(frame int) string {
	const frames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	i := ((frame % 10) + 10) % 10
	return frames[3*i : 3*i+3]
}

// MergeQueue draws the tasks waiting to be merged and what happens to them:
//
//	▸ t7 rebase ✓ ─ verify ⠹
//	    go test ./orders/...
//	  t8 rebase ⠹
//	  t9 queued
//	✗ t2 rebase ✓ ─ verify ✗
//	  ↩ back to w3: FAIL TestList (0.3s)
//
//	merged t1 t4 t5 t6
//	conflicts 0 · bounced back 1
//
// An item shows the stages it has been through, each ✓, and the one it is in with a braille spinner that turns with frame (any
// integer is a frame), so the chip is seen moving through rebase, verify, merge; the first one in progress is marked ▸ and shows
// what it is doing underneath. A failed item shows the stage it stopped at as ✗ in the alarm style and, underneath, where it
// went and why. Merged items are listed in one line, wrapped at the width, and the last line counts the conflicts (failures at
// the rebase) and the bounces.
//
// Everything is cut to width. A width <= 0 draws nothing; an empty queue still says so and gives the counts.
func MergeQueue(items []MergeItem, frame int, width int, p Palette) []cell.Line {
	if width <= 0 {
		return nil
	}
	var out []cell.Line
	var merged []string
	conflicts, bounced := 0, 0
	head := true // the first one in progress gets the marker
	for _, it := range items {
		id := showClean(it.ID)
		if it.Failed {
			bounced++
			if it.Stage <= MergeRebase {
				conflicts++
			}
		}
		if it.Stage >= MergeDone && !it.Failed {
			merged = append(merged, id)
			continue
		}
		var b showRowBuf
		mark := "  "
		switch {
		case it.Failed:
			mark = "✗ "
		case it.Stage != MergeQueued && head:
			mark, head = "▸ ", false
		}
		markSt := p.dimSt()
		if it.Failed {
			markSt = p.badSt()
		} else if mark == "▸ " {
			markSt = p.infoSt().With(cell.Bold)
		}
		idSt := cell.Style{Attr: cell.Bold}
		if it.Stage == MergeQueued && !it.Failed {
			idSt = p.dimSt()
		}
		b.add(markSt, mark).add(idSt, id)
		switch {
		case it.Stage == MergeQueued && !it.Failed:
			b.add(p.dimSt(), " queued")
		default:
			last := it.Stage
			if last > MergeMerge {
				last = MergeMerge
			}
			for s := MergeRebase; s <= last; s++ {
				if s > MergeRebase {
					b.add(p.dimSt(), " ─")
				}
				b.add(cell.Style{}, " "+s.word()+" ")
				switch {
				case s < last:
					b.add(p.goodSt(), "✓")
				case it.Failed:
					b.add(p.badSt(), "✗")
				default:
					b.add(p.infoSt().With(cell.Bold), showSpin(frame))
				}
			}
		}
		out = append(out, showFit(b.line(), width))
		if note := showClean(it.Note); note != "" {
			switch {
			case it.Failed:
				to := ""
				if w := showClean(it.Worker); w != "" {
					to = "back to " + w + ": "
				} else {
					to = "bounced back: "
				}
				out = append(out, showFit(cell.Styled(p.badSt(), "  ↩ "+to+note), width))
			case mark == "▸ ":
				out = append(out, showFit(cell.Styled(p.dimSt(), "    "+note), width))
			}
		} else if it.Failed {
			out = append(out, showFit(cell.Styled(p.badSt(), "  ↩ bounced back"), width))
		}
	}
	if len(out) == 0 {
		out = append(out, showFit(cell.Styled(p.dimSt(), "  nothing queued"), width))
	}
	out = append(out, nil)
	out = append(out, mergeList("merged", merged, width, p)...)
	var st showRowBuf
	st.add(p.dimSt(), "conflicts "+strconv.Itoa(conflicts)+" · bounced back "+strconv.Itoa(bounced))
	return append(out, showFit(st.line(), width))
}

// mergeList is "merged t1 t2 t4", wrapped at width with the continuation lines under the first name; none merged is "merged none".
func mergeList(title string, ids []string, width int, p Palette) []cell.Line {
	if len(ids) == 0 {
		return []cell.Line{showFit(cell.Styled(p.dimSt(), title+" none"), width)}
	}
	indent := cell.StringWidth(title) + 1
	var out []cell.Line
	var b showRowBuf
	b.add(p.goodSt(), title)
	for _, id := range ids {
		if b.w > indent && b.w+1+cell.StringWidth(id) > width {
			out = append(out, showFit(b.line(), width))
			b = showRowBuf{}
			b.space(indent - 1)
		}
		b.add(cell.Style{}, " "+id)
	}
	return append(out, showFit(b.line(), width))
}
