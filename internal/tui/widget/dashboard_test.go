package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

// showDashSizes are the sizes of the golden pictures of the cockpit.
var showDashSizes = []struct{ w, h int }{{120, 48}, {100, 40}, {80, 30}, {60, 24}}

func TestDashboardGolden(t *testing.T) {
	p := widget.DefaultPalette()
	for _, agents := range []int{8, 50} {
		for _, sz := range showDashSizes {
			var d showtest.Doc
			d.Note("Dashboard at %dx%d with %d agents, frame 12 (the last mail is arriving); the layout is what fits: read what is there and what is left out", sz.w, sz.h, agents)
			lines := widget.Dashboard(showSketchDashboard(agents), sz.w, sz.h, 12, p)
			d.Add(fmt.Sprintf("%dx%d, %d agents", sz.w, sz.h, agents), lines, sz.w)
			showGolden(t, fmt.Sprintf("dashboard_%dx%d_%d", sz.w, sz.h, agents), &d)
		}
	}
}

// BenchmarkDashboard is what one frame of the cockpit costs (the UI redraws at most 15 times a second).
func BenchmarkDashboard(b *testing.B) {
	p := widget.DefaultPalette()
	for _, agents := range []int{8, 50} {
		for _, sz := range showDashSizes {
			data := showSketchDashboard(agents)
			b.Run(fmt.Sprintf("%dx%d_%d", sz.w, sz.h, agents), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					widget.Dashboard(data, sz.w, sz.h, i, p)
				}
			})
		}
	}
}

func TestDashboardFillsTheScreenExactly(t *testing.T) {
	p := widget.DefaultPalette()
	for _, agents := range []int{0, 1, 3, 8, 50} {
		data := showSketchDashboard(max(agents, 1))
		data.Agents = showSwarm(agents, 7)
		for _, sz := range showDashSizes {
			lines := widget.Dashboard(data, sz.w, sz.h, 0, p)
			if len(lines) != sz.h {
				t.Errorf("%d agents at %dx%d: %d lines", agents, sz.w, sz.h, len(lines))
			}
			showNoWider(t, fmt.Sprintf("%d agents at %dx%d", agents, sz.w, sz.h), lines, sz.w)
			for i, l := range lines {
				if l.Width() != sz.w {
					t.Errorf("%d agents at %dx%d: line %d is %d wide: %q", agents, sz.w, sz.h, i, l.Width(), l.Plain())
					break
				}
			}
		}
	}
}

func TestDashboardListGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("below 60 columns or 8 rows the cockpit is a one-column list: the title, the clock, the states, the agents, the board, the queue, the governor, the feed")
	data := showSketchDashboard(8)
	for _, sz := range []struct{ w, h int }{{59, 30}, {40, 20}, {40, 12}, {32, 9}, {24, 6}, {20, 4}, {20, 2}, {20, 1}, {100, 7}} {
		d.Add(fmt.Sprintf("%dx%d", sz.w, sz.h), widget.Dashboard(data, sz.w, sz.h, 3, p), sz.w)
	}
	d.Note("fifty agents: the stuck ones are kept")
	d.Add("40x14, 50 agents", widget.Dashboard(showSketchDashboard(50), 40, 14, 3, p), 40)
	showGolden(t, "dashboard_list", &d)
}

// showTopBand is the title bar, the horse band and the rule under it of a cockpit drawn 80 wide and 30 high: the part of it that
// moves with the horse (the spinners of the table turn too, but they are tested with the table).
func showTopBand(data widget.DashboardData, frame int, p widget.Palette) []cell.Line {
	return widget.Dashboard(data, 80, 30, frame, p)[:12]
}

func TestDashboardAnimationGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("the top of the cockpit at 80x30 over frames 0, 2, 4 and 6: two of the eight agents are busy (w1 runs a tool, w2 edits), so two legs")
	d.Note("(the second and the third from the tail) are in the stride and the other six stand; the gait advances (2+busy)/8 of a step per frame: one step in two")
	data := showSketchDashboard(8)
	for f := 0; f < 8; f += 2 {
		d.Add(fmt.Sprintf("frame %d", f), showTopBand(data, f, p), 80)
	}
	d.Note("every agent busy: all eight legs are in the stride, one step every 0.8 frames")
	busy := data
	busy.Agents = append([]widget.AgentRow(nil), data.Agents...)
	for i := range busy.Agents {
		busy.Agents[i].State = widget.StateTool
	}
	for f := 0; f < 4; f++ {
		d.Add(fmt.Sprintf("all busy, frame %d", f), showTopBand(busy, f, p), 80)
	}
	d.Note("with --no-anim: the standing horse, whatever the frame")
	data.NoAnim = true
	d.Add("frame 0, no animation", showTopBand(data, 0, p), 80)
	d.Add("frame 5, no animation", showTopBand(data, 5, p), 80)
	showGolden(t, "dashboard_frames", &d)
}

func TestDashboardMonoGolden(t *testing.T) {
	var d showtest.Doc
	d.Note("without colours: the same text, the horse in shades")
	d.Add("100x40, 8 agents, no colours", widget.Dashboard(showSketchDashboard(8), 100, 40, 2, widget.MonoPalette()), 100)
	showGolden(t, "dashboard_mono", &d)
}

func TestDashboardStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("the title bar and two rows of the table at 100x40: the role colours on the ids, the state colours, hit and stuck")
	lines := widget.Dashboard(showSketchDashboard(8), 100, 40, 2, p)
	pick := []cell.Line{lines[0]}
	for _, l := range lines {
		if s := l.Plain(); strings.HasPrefix(s, "│ w1 ") || strings.HasPrefix(s, "│ w7 ") || strings.Contains(s, "├─ agents") {
			pick = append(pick, l)
		}
	}
	d.AddText("styled", showtest.FlattenStyled(pick, showNames(p)))
	showGolden(t, "dashboard_styled", &d)
}
