package widget_test

// Properties every signature widget has, tried on random data at every width from -5 to 210 (and with hostile text): it never
// panics, no line is wider than the width it was given, no text holds a control character, the same input gives the same lines
// (styles included), and the plain text does not depend on the palette (the horse, whose legs are shades without colours, and the
// cockpit that has the horse in it, are compared by shape). Zero, negative and NaN inputs are in the random data.

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

var showHostile = []string{
	"", "plain", "a\x1b[31mred\x1b[0m", "line\nbreak", "tab\there", "\x00nul", "\u202eRTL", "wide中文", "horse \U0001F40E\U0001F40E",
	"e\u0301\u0301", "\xff\xfe", strings.Repeat("long ", 40), "\u200b\u200d", "\x7f\u009b", "\u2028sep", "\x1b]0;title\x07", "\r\n",
}

func showText(r *rand.Rand) string { return showHostile[r.Intn(len(showHostile))] }

func showFloat(r *rand.Rand) float64 {
	switch r.Intn(9) {
	case 0:
		return math.NaN()
	case 1:
		return math.Inf(1 - 2*r.Intn(2))
	case 2:
		return -float64(r.Intn(5))
	case 3:
		return 1e18
	}
	return float64(r.Intn(1001)) / 1000
}

func showInt(r *rand.Rand) int {
	switch r.Intn(10) {
	case 0:
		return math.MaxInt
	case 1:
		return math.MinInt
	case 2:
		return -r.Intn(100)
	case 3:
		return 1 << 40
	case 4:
		return 0
	}
	return r.Intn(60000)
}

func showRandAgents(r *rand.Rand, n int) []widget.AgentRow {
	rows := make([]widget.AgentRow, n)
	for i := range rows {
		lv := make([]uint8, r.Intn(80))
		for k := range lv {
			lv[k] = uint8(r.Intn(12))
		}
		var marks []widget.LaneMark
		for k := r.Intn(4); k > 0; k-- {
			marks = append(marks, widget.LaneMark{Bucket: showInt(r), Kind: widget.LaneMarkKind(r.Intn(5))})
		}
		rows[i] = widget.AgentRow{
			ID: showText(r), Role: showText(r), RoleColor: showInt(r), State: widget.AgentState(r.Intn(10)), Doing: showText(r),
			Shared: showInt(r), Own: showInt(r), Hit: showFloat(r), Cost: showFloat(r), Scope: showText(r),
			Levels: lv, LevelsFrom: showInt(r) % 100, Marks: marks,
		}
	}
	return rows
}

func showRandKanban(r *rand.Rand) []widget.KanbanCol {
	cols := make([]widget.KanbanCol, r.Intn(6))
	for i := range cols {
		cols[i] = widget.KanbanCol{Kind: widget.ColKind(r.Intn(6)), Title: showText(r), Count: showInt(r)}
		for k := r.Intn(14); k > 0; k-- {
			cols[i].Cards = append(cols[i].Cards, widget.KanbanCard{ID: showText(r), Label: showText(r), Failed: r.Intn(4) == 0})
		}
	}
	return cols
}

func showRandMerge(r *rand.Rand) []widget.MergeItem {
	items := make([]widget.MergeItem, r.Intn(10))
	for i := range items {
		items[i] = widget.MergeItem{ID: showText(r), Stage: widget.MergeStage(r.Intn(7)), Failed: r.Intn(3) == 0, Note: showText(r), Worker: showText(r)}
	}
	return items
}

func showRandMails(r *rand.Rand) []widget.Mail {
	mails := make([]widget.Mail, r.Intn(8))
	for i := range mails {
		mails[i] = widget.Mail{From: showText(r), To: showText(r), Subject: showText(r), Tokens: showInt(r), Born: r.Intn(20) - 4}
	}
	return mails
}

func showRandDashboard(r *rand.Rand) widget.DashboardData {
	feed := make([]widget.FeedLine, r.Intn(7))
	for i := range feed {
		feed[i] = widget.FeedLine{At: time.Duration(showInt(r)) * time.Millisecond, Agent: showText(r), Color: showInt(r), Kind: widget.FeedKind(r.Intn(7)), Text: showText(r), Tail: showText(r)}
	}
	return widget.DashboardData{
		Title: showText(r), Elapsed: time.Duration(showInt(r)) * time.Second, Spend: showFloat(r), Budget: showFloat(r), HitRatio: showFloat(r),
		Shared: showRandLayers(r), PrefixLeft: time.Duration(showInt(r)) * time.Second, PrefixTTL: time.Duration(showInt(r)) * time.Second,
		Agents: showRandAgents(r, []int{0, 1, 2, 3, 8, 9, 20, 50}[r.Intn(8)]), NowBucket: showInt(r) % 200,
		Kanban: showRandKanban(r), Merge: showRandMerge(r), Mail: showRandMails(r),
		MailStats: widget.MailStats{Routed: showInt(r), Delivered: showInt(r), Dup: showInt(r), Ignored: showInt(r)},
		Governor:  widget.Governor{RPM: showInt(r), RPMLimit: showInt(r), Err429: showInt(r), Retries: showInt(r), Speedup: showFloat(r)},
		Feed:      feed, NoAnim: r.Intn(4) == 0,
	}
}

// showMonoShape says how a widget's mono text must relate to its coloured text.
type showMonoShape int

const (
	showSameText   showMonoShape = iota // the plain text is identical
	showSameShape                       // the same cells are blank (the horse: its legs and hair are shades without colours)
	showSameBelow                       // identical below the horse's band, and to the right of the horse above it
	showNoMonoTest                      // not compared
)

type showCase struct {
	name string
	mono showMonoShape
	draw func(r *rand.Rand, w int, p widget.Palette) []cell.Line
	// limit is the widest a line may be for a width; nil means the width itself. The heatmap has no width, only columns: its grid
	// is two cells to a column less one and its legend is never more than 25 cells.
	limit func(w int) int
}

func showCases() []showCase {
	return []showCase{
		{"StackBar", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			o := widget.NewStackOpts(w, showInt(r))
			o.Sweep, o.Warm, o.BreakAt = showFloat(r), showFloat(r), r.Intn(10)-2
			bar, labels := widget.StackBar(showRandLayers(r), o, p)
			return []cell.Line{bar, labels}
		}, nil},
		{"StackTable", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			o := widget.NewStackOpts(w, showInt(r))
			o.BreakAt = r.Intn(10) - 2
			return widget.StackTable(showRandLayers(r), o, p)
		}, nil},
		{"Spark", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			vals := make([]float64, r.Intn(300))
			for i := range vals {
				vals[i] = showFloat(r)
			}
			var marks []widget.Mark
			for k := r.Intn(8); k > 0; k-- {
				marks = append(marks, widget.Mark{At: r.Intn(len(vals)+5) - 2, Kind: widget.MarkKind(r.Intn(4))})
			}
			return widget.Spark(vals, w, marks, p)
		}, nil},
		{"TTL", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			return []cell.Line{widget.TTL(time.Duration(showInt(r))*time.Second, time.Duration(showInt(r))*time.Second, w, p)}
		}, nil},
		{"Fold", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			return widget.Fold(showInt(r), showInt(r), showFloat(r), w, p)
		}, nil},
		{"Fork", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			return widget.Fork(showRandLayers(r), showInt(r), showFloat(r), w, p)
		}, nil},
		{"Fan", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			n := []int{0, 1, 3, 8, 30, 200}[r.Intn(6)]
			labels, weights := make([]string, n), make([]int, r.Intn(n+2))
			for i := range labels {
				labels[i] = showText(r)
			}
			for i := range weights {
				weights[i] = showInt(r)
			}
			if r.Intn(2) == 0 {
				return widget.Fan(labels, weights, w, p)
			}
			return widget.FanShared(showRandLayers(r), labels, weights, w, p)
		}, nil},
		{"Gallop", showSameShape, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			legs := make([]widget.Leg, r.Intn(11))
			for i := range legs {
				legs[i] = widget.Leg{Busy: r.Intn(2) == 0, Color: showInt(r)}
			}
			if r.Intn(2) == 0 {
				return widget.Gallop(legs, showInt(r), w, p)
			}
			return widget.GallopIn(legs, showInt(r), w, r.Intn(20)-2, p)
		}, nil},
		{"Stand", showSameShape, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			if r.Intn(2) == 0 {
				return widget.Stand(w, p)
			}
			return widget.StandIn(w, r.Intn(20)-2, p)
		}, nil},
		{"Heatmap", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			states := make([]widget.AgentState, r.Intn(120))
			for i := range states {
				states[i] = widget.AgentState(r.Intn(10))
			}
			// the heatmap has no width: its columns are what the caller fits to a width
			return widget.Heatmap(states, (w+1)/2, p)
		}, func(w int) int {
			cols := (w + 1) / 2
			if cols < 1 {
				cols = 10
			}
			return max(2*cols-1, 25)
		}},
		{"Gantt", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			agents := showRandAgents(r, r.Intn(12))
			lanes := make([]widget.Lane, len(agents))
			for i, a := range agents {
				lanes[i] = widget.Lane{Name: a.ID, Color: a.RoleColor, Levels: a.Levels, First: a.LevelsFrom, Marks: a.Marks}
			}
			return widget.Gantt(lanes, w, showInt(r)%300, p)
		}, nil},
		{"Kanban", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			return widget.Kanban(showRandKanban(r), w, p)
		}, nil},
		{"MergeQueue", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			return widget.MergeQueue(showRandMerge(r), showInt(r), w, p)
		}, nil},
		{"MailFlow", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			return widget.MailFlow(showRandMails(r), showInt(r)%30, w, p)
		}, nil},
		{"AgentTable", showSameText, func(r *rand.Rand, w int, p widget.Palette) []cell.Line {
			return widget.AgentTable(showRandAgents(r, r.Intn(14)), w, showInt(r), p)
		}, nil},
	}
}

// showWidthsProps is every width from -5 up to 12, then a sweep of the layouts' thresholds up to 210.
func showWidthsProps() []int {
	ws := []int{-5, -1}
	return append(ws, showWidths()...)
}

func TestShowWidgetsProperties(t *testing.T) {
	for _, c := range showCases() {
		t.Run(c.name, func(t *testing.T) {
			for _, w := range showWidthsProps() {
				for seed := int64(1); seed <= 3; seed++ {
					seed := seed*7919 + int64(w)
					var lines [2][]cell.Line
					var plains [2]string
					for k, name := range []string{"default", "mono"} {
						p := showPalettes()[name]
						func() {
							defer func() {
								if e := recover(); e != nil {
									t.Fatalf("%s at width %d seed %d (%s palette) panicked: %v", c.name, w, seed, name, e)
								}
							}()
							lines[k] = c.draw(showRand(seed), w, p)
						}()
						plains[k] = showPlain(lines[k])
						limit := max(w, 0)
						if c.limit != nil {
							limit = c.limit(w)
						}
						showNoWider(t, fmt.Sprintf("%s w=%d seed=%d", c.name, w, seed), lines[k], limit)
						showNoControl(t, fmt.Sprintf("%s w=%d seed=%d", c.name, w, seed), lines[k])
						if name == "mono" {
							showNoColour(t, fmt.Sprintf("%s w=%d seed=%d (mono)", c.name, w, seed), lines[k])
						}
					}
					// the same input, the same lines: styles and all
					again := c.draw(showRand(seed), w, widget.DefaultPalette())
					if !reflect.DeepEqual(again, lines[0]) {
						t.Fatalf("%s at width %d seed %d is not deterministic", c.name, w, seed)
					}
					switch c.mono {
					case showSameText:
						if plains[0] != plains[1] {
							t.Fatalf("%s at width %d seed %d: the colours change the text:\n%s\n--\n%s", c.name, w, seed, plains[0], plains[1])
						}
					case showSameShape:
						a, b := strings.Split(plains[0], "\n"), strings.Split(plains[1], "\n")
						if len(a) != len(b) {
							t.Fatalf("%s at width %d: %d rows coloured, %d mono", c.name, w, len(a), len(b))
						}
						for y := range a {
							ra, rb := []rune(a[y]), []rune(b[y])
							if len(ra) != len(rb) {
								t.Fatalf("%s at width %d row %d: %d cells coloured, %d mono", c.name, w, y, len(ra), len(rb))
							}
							for x := range ra {
								if (ra[x] == ' ') != (rb[x] == ' ') {
									t.Fatalf("%s at width %d: cell (%d,%d) is %q coloured and %q mono", c.name, w, x, y, ra[x], rb[x])
								}
							}
						}
					}
				}
			}
		})
	}
}

// showPlain is the plain text of the lines, one to a row.
func showPlain(ls []cell.Line) string {
	rows := make([]string, len(ls))
	for i, l := range ls {
		rows[i] = l.Plain()
	}
	return strings.Join(rows, "\n")
}

// The cockpit, at every size from a few cells to a large terminal, on random swarms: it fills the screen exactly when it is a box,
// is never wider than the width or taller than the height, is deterministic, and is the same with no colours but for the horse.
func TestShowDashboardProperties(t *testing.T) {
	// a width on each side of every threshold of the layout (60, 70 and 96 columns; 8 rows), and heights from nothing to a tall
	// terminal; the swarms differ from size to size
	heights := []int{-3, 0, 1, 3, 7, 8, 9, 12, 18, 24, 30, 40, 48, 60}
	widths := []int{-5, 0, 1, 7, 30, 59, 60, 61, 69, 70, 71, 95, 96, 97, 120, 200}
	for _, w := range widths {
		for _, h := range heights {
			seed := int64(w*131 + h*17 + 5)
			d := showRandDashboard(showRand(seed))
			frame := int(seed % 97)
			var out [2][]cell.Line
			for k, name := range []string{"default", "mono"} {
				p := showPalettes()[name]
				func() {
					defer func() {
						if e := recover(); e != nil {
							t.Fatalf("Dashboard %dx%d seed %d (%s) panicked: %v", w, h, seed, name, e)
						}
					}()
					out[k] = widget.Dashboard(d, w, h, frame, p)
				}()
				what := fmt.Sprintf("Dashboard %dx%d seed %d", w, h, seed)
				showNoWider(t, what, out[k], max(w, 0))
				showNoControl(t, what, out[k])
				if name == "mono" {
					showNoColour(t, what+" (mono)", out[k])
				}
				if len(out[k]) > max(h, 0) {
					t.Fatalf("%s: %d lines, height %d", what, len(out[k]), h)
				}
				if w >= 60 && h >= 8 && len(out[k]) != h {
					t.Fatalf("%s: a box is exactly the height: %d lines", what, len(out[k]))
				}
				if w >= 60 && h >= 8 {
					for i, l := range out[k] {
						if l.Width() != w {
							t.Fatalf("%s: line %d is %d wide, a box is exactly the width: %q", what, i, l.Width(), l.Plain())
						}
					}
				}
			}
			if (w+h)%3 == 0 { // determinism on a third of the sizes: the widgets' own tests do it on every one
				if again := widget.Dashboard(d, w, h, frame, widget.DefaultPalette()); !reflect.DeepEqual(again, out[0]) {
					t.Fatalf("Dashboard %dx%d seed %d is not deterministic", w, h, seed)
				}
			}
			showDashMonoSame(t, fmt.Sprintf("Dashboard %dx%d seed %d", w, h, seed), out[0], out[1])
		}
	}
}

// showDashMonoSame compares the cockpit with and without colours: identical everywhere but the horse, whose legs and hair are
// shades without colours.
func showDashMonoSame(t *testing.T, what string, colour, mono []cell.Line) {
	t.Helper()
	if len(colour) != len(mono) {
		t.Fatalf("%s: %d lines coloured, %d mono", what, len(colour), len(mono))
	}
	// the horse's band is the rows up to the first rule, and in it the cells left of the line between the horse and the fan
	end := len(colour)
	for i, l := range colour {
		if strings.HasPrefix(l.Plain(), "├") || strings.HasPrefix(l.Plain(), "╰") {
			end = i
			break
		}
	}
	for i := range colour {
		a, b := []rune(colour[i].Plain()), []rune(mono[i].Plain())
		if i > 0 && i < end {
			cut := 0
			seps := 0
			for x, r := range a {
				if r == '│' {
					seps++
					if seps == 2 {
						cut = x
						break
					}
				}
			}
			if cut > 0 && cut <= len(a) && cut <= len(b) {
				a, b = a[cut:], b[cut:]
			} else { // the band is one panel
				a, b = nil, nil
			}
		}
		if string(a) != string(b) {
			t.Fatalf("%s: line %d changes with the palette:\n%s\n%s", what, i, string(a), string(b))
		}
	}
}
