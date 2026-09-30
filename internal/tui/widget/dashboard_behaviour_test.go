package widget_test

// Behaviour of the cockpit that a golden picture shows only indirectly: which panels the width allows, which bands the height
// keeps, which agents are kept when there are too many, what the animation moves, and what it does with data that makes no sense.
// The pictures themselves are in dashboard_test.go.

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

func TestDashboardModesByWidth(t *testing.T) {
	p := widget.DefaultPalette()
	data := showSketchDashboard(8)
	for _, c := range []struct {
		width int
		has   []string
		not   []string
	}{
		{120, []string{"agents", "swarm gantt", "task board", "merge queue", "mail", "governor", "8 agents", "live feed"}, nil},
		{96, []string{"agents", "swarm gantt", "task board", "merge queue", "mail", "governor", "8 agents", "live feed"}, nil},
		{95, []string{"agents", "task board", "merge queue", "mail", "governor", "live feed"}, []string{"swarm gantt", "8 agents"}},
		{70, []string{"agents", "task board", "merge queue", "mail", "governor", "live feed"}, []string{"swarm gantt", "8 agents"}},
		{69, []string{"agents", "task board", "merge queue", "mail", "live feed"}, []string{"swarm gantt", "governor", "8 agents"}},
		{60, []string{"agents", "task board", "merge queue", "mail", "live feed"}, []string{"swarm gantt", "governor", "8 agents"}},
	} {
		ls := widget.Dashboard(data, c.width, 48, 0, p)
		what := fmt.Sprintf("width %d", c.width)
		if len(ls) != 48 {
			t.Errorf("%s: %d lines, want 48", what, len(ls))
		}
		rules := showRules(ls)
		for _, w := range c.has {
			if !strings.Contains(rules, w) {
				t.Errorf("%s: no panel called %q in\n%s", what, w, rules)
			}
		}
		for _, w := range c.not {
			if strings.Contains(rules, w) {
				t.Errorf("%s: a panel called %q that this width has no room for", what, w)
			}
		}
		if text := showPlain(ls); !strings.Contains(text, "one prefix, eight riders") || len(showHorse(ls)) == 0 {
			t.Errorf("%s: the horse and the fan are the heart of the cockpit and always there", what)
		}
		if title := ls[0].Plain(); !strings.Contains(title, "8 agents") || !strings.Contains(title, "SLEIPNIR") {
			t.Errorf("%s: the title bar is %q", what, title)
		}
	}

	// below 60 columns or 8 rows it is a list: no frame, the title first, at most the height
	for _, sz := range []struct{ w, h int }{{59, 48}, {40, 30}, {24, 12}, {60, 7}, {100, 7}, {100, 1}} {
		ls := widget.Dashboard(data, sz.w, sz.h, 0, p)
		what := fmt.Sprintf("%dx%d", sz.w, sz.h)
		if len(ls) == 0 || len(ls) > sz.h {
			t.Fatalf("%s: %d lines", what, len(ls))
		}
		showNoBox(t, what, ls)
		if !strings.HasPrefix(ls[0].Plain(), "SLEIPNIR") {
			t.Errorf("%s: a list starts with the title: %q", what, ls[0].Plain())
		}
	}
	full := showPlain(widget.Dashboard(data, 59, 48, 0, p))
	for _, want := range []string{"◷ 02:10 · 8 agents · $0.31/$20 · ⛁ 94%", "w7 ⚠ npm test - same failure", "todo 3 · running 4 · verifying 1 · merged 5", "rpm 212/240 · prefix 3:41", "02:09 w7 ⚠ same failure twice"} {
		if !strings.Contains(full, want) {
			t.Errorf("the list at 59x48 lacks %q:\n%s", want, full)
		}
	}

	// 60 columns and 8 rows is the smallest box
	for _, sz := range []struct{ w, h int }{{60, 8}, {100, 8}, {60, 9}} {
		ls := widget.Dashboard(data, sz.w, sz.h, 0, p)
		if len(ls) != sz.h || !strings.HasPrefix(ls[0].Plain(), "╭") || !strings.HasPrefix(ls[sz.h-1].Plain(), "╰") {
			t.Errorf("%dx%d is a box of that height, got %d lines starting %q", sz.w, sz.h, len(ls), ls[0].Plain())
		}
	}
}

func TestDashboardGrowsWithTheHeight(t *testing.T) {
	p := widget.DefaultPalette()
	for _, c := range []struct{ w, agents, step int }{{100, 8, 1}, {100, 50, 3}, {80, 8, 2}, {80, 50, 3}, {60, 8, 3}, {140, 8, 3}} {
		data := showSketchDashboard(c.agents)
		index := map[string]int{}
		stuck := 0
		for i, a := range data.Agents {
			index[a.ID] = i
			if a.State == widget.StateStuck {
				stuck++
			}
		}
		var prev showLayout
		for h := 8; h <= 64; h += c.step {
			what := fmt.Sprintf("%dx%d with %d agents", c.w, h, c.agents)
			ls := widget.Dashboard(data, c.w, h, 0, p)
			if len(ls) != h {
				t.Fatalf("%s: %d lines", what, len(ls))
			}
			lay := showLayoutOf(ls)
			// the bands come and go in a fixed order: the panels hang from the board, which hangs from the horse, and a taller
			// terminal never loses one
			if lay.panels && !lay.mid || lay.mid && !lay.top {
				t.Errorf("%s: a band without the one it follows: %+v", what, lay)
			}
			if prev.top && !lay.top || prev.mid && !lay.mid || prev.panels && !lay.panels {
				t.Errorf("%s: a taller terminal lost a band: %+v after %+v", what, lay, prev)
			}
			if lay.horse < prev.horse {
				t.Errorf("%s: the horse shrank from %d to %d rows", what, prev.horse, lay.horse)
			}
			if lay.feed > 4 {
				t.Errorf("%s: %d lines of feed, the data has 4", what, lay.feed)
			}
			prev = lay

			// the table stays, in the order given, with the stuck agents first to be kept
			ids, more := showTable(ls)
			if len(ids) == 0 {
				t.Errorf("%s: no agent in the table", what)
				continue
			}
			if (more == 0) != (len(ids) == c.agents) || more > 0 && len(ids)+more != c.agents {
				t.Errorf("%s: the table lists %d agents and says %d more, of %d", what, len(ids), more, c.agents)
			}
			if lay.top && len(ids) < min(4, c.agents) {
				t.Errorf("%s: the table was cut to %d agents to make room for the horse", what, len(ids))
			}
			last, nstuck := -1, 0
			for _, id := range ids {
				i, ok := index[id]
				if !ok || i <= last {
					t.Errorf("%s: the table lists %v, not in the order given", what, ids)
					break
				}
				last = i
				if data.Agents[i].State == widget.StateStuck {
					nstuck++
				}
			}
			if want := min(len(ids), stuck); nstuck != want {
				t.Errorf("%s: %d of the %d agents shown are stuck, want %d: %v", what, nstuck, len(ids), want, ids)
			}
			if !strings.Contains(ls[h-2].Plain(), "q back to shell") {
				t.Errorf("%s: the row above the bottom is %q, the keys", what, ls[h-2].Plain())
			}
		}

		// with room to spare everything is there, and the horse is as big as the width lets it be
		ls := widget.Dashboard(data, c.w, 64, 0, p)
		lay := showLayoutOf(ls)
		ids, more := showTable(ls)
		if c.agents == 8 && (more != 0 || len(ids) != 8 || lay.feed != 4) {
			t.Errorf("%d wide, 64 high: %d agents in the table, %d more, %d lines of feed", c.w, len(ids), more, lay.feed)
		}
		wantHorse := map[int]int{60: 10, 80: 10, 100: 13, 140: 16}[c.w]
		if lay.horse != wantHorse {
			t.Errorf("%d wide, 64 high: the horse is %d rows, want %d", c.w, lay.horse, wantHorse)
		}
	}
}

func TestDashboardFiftyAgents(t *testing.T) {
	p := widget.DefaultPalette()
	data := showSketchDashboard(50)
	var stuck []string
	for _, a := range data.Agents {
		if a.State == widget.StateStuck {
			stuck = append(stuck, a.ID)
		}
	}
	if len(stuck) != 3 {
		t.Fatalf("the test data has %d stuck agents, want 3: %v", len(stuck), stuck)
	}
	legend := regexp.MustCompile(`⚠ stuck\s+3`)
	for _, sz := range showDashSizes {
		what := fmt.Sprintf("%dx%d", sz.w, sz.h)
		ls := widget.Dashboard(data, sz.w, sz.h, 0, p)
		text := showPlain(ls)
		ids, more := showTable(ls)
		if more == 0 || len(ids)+more != 50 {
			t.Errorf("%s: %d agents listed and %d more, of 50", what, len(ids), more)
		}
		for _, s := range stuck {
			if !strings.Contains(" "+strings.Join(ids, " ")+" ", " "+s+" ") {
				t.Errorf("%s: the stuck agent %s is not in the table: %v", what, s, ids)
			}
		}
		if !strings.Contains(text, "one prefix, 50 riders") {
			t.Errorf("%s: the fan does not say how many riders there are", what)
		}
		named, extra := showRiders(ls)
		if extra == 0 || len(named)+extra != 50 || named[0] != "m0" {
			t.Errorf("%s: the fan names %v and counts %d more", what, named, extra)
		}
		heat := strings.Contains(showRules(ls), "50 agents")
		if heat != (sz.w >= 96) {
			t.Errorf("%s: the heatmap panel is there: %v", what, heat)
		}
		if heat {
			// the heatmap counts every agent, not only the ones in the table
			if !legend.MatchString(text) || !strings.Contains(text, "(all of them are in the heatmap)") {
				t.Errorf("%s: the legend of the heatmap does not count the 3 stuck agents of 50, or the table does not point to it:\n%s", what, text)
			}
		}
	}
}

func TestDashboardHeadlineAndFan(t *testing.T) {
	p := widget.DefaultPalette()
	ls := widget.Dashboard(showSketchDashboard(8), 100, 40, 0, p)
	for _, want := range []string{"SLEIPNIR", "swarm “add pagination", "◷ 02:10", "8 agents", "$0.31/$20", "⛁ 94%"} {
		if !strings.Contains(ls[0].Plain(), want) {
			t.Errorf("the title bar lacks %q: %q", want, ls[0].Plain())
		}
	}
	text := showPlain(ls)
	for _, want := range []string{"one prefix, eight riders", "shared G0–G2 41.2k tokens", "◕ warm 3:41 ▰▰▰▰▰▰▱▱", "  G0 ", " G1 ", " G2 "} {
		if !strings.Contains(text, want) {
			t.Errorf("the fan lacks %q:\n%s", want, text)
		}
	}
	named, more := showRiders(ls)
	if want := []string{"m0", "w1", "w2", "w3", "w4", "w5", "w6", "w7"}; !reflect.DeepEqual(named, want) || more != 0 {
		t.Errorf("the riders are %v and %d more, want %v", named, more, want)
	}
	if !strings.Contains(text, "┃") || !strings.Contains(text, "│") || !strings.Contains(text, "╎") {
		t.Errorf("the drops of the fan are heavy where a rider's share of the prefix is big, and light where it is small")
	}

	// the title bar without a budget, and with numbers that make no sense
	d := showSketchDashboard(8)
	d.Budget = 0
	if title := widget.Dashboard(d, 100, 40, 0, p)[0].Plain(); !strings.Contains(title, "$0.31 ") || strings.Contains(title, "$0.31/") {
		t.Errorf("without a budget the title bar says the spend alone: %q", title)
	}
	d = showSketchDashboard(8)
	d.Spend, d.HitRatio, d.Elapsed = math.NaN(), math.NaN(), -5
	if title := widget.Dashboard(d, 100, 40, 0, p)[0].Plain(); !strings.Contains(title, "◷ 00:00 · 8 agents · $0/$20 · ⛁ 0%") {
		t.Errorf("NaN money and a negative clock are zero: %q", title)
	}

	// one rider and none
	d = showSketchDashboard(8)
	d.Agents = d.Agents[:1]
	ls = widget.Dashboard(d, 100, 40, 0, p)
	if !strings.Contains(ls[0].Plain(), " 1 agent ·") || !strings.Contains(showPlain(ls), "one prefix, one rider ") {
		t.Errorf("one agent is a rider, not riders:\n%s", showPlain(ls))
	}
	d.Agents = nil
	ls = widget.Dashboard(d, 100, 40, 0, p)
	if !strings.Contains(ls[0].Plain(), " 0 agents ·") || !strings.Contains(showPlain(ls), "one prefix, no riders") {
		t.Errorf("no agents, no riders:\n%s", showPlain(ls))
	}
}

func TestDashboardAnimation(t *testing.T) {
	p := widget.DefaultPalette()
	data := showSketchDashboard(8) // w1 runs a tool and w2 edits: two legs are busy
	at := func(d widget.DashboardData, f int) []cell.Line { return widget.Dashboard(d, 80, 30, f, p) }
	horse := func(d widget.DashboardData, f int) string { return strings.Join(showHorse(at(d, f)), "\n") }
	withStates := func(s widget.AgentState) widget.DashboardData {
		d := data
		d.Agents = append([]widget.AgentRow(nil), data.Agents...)
		for i := range d.Agents {
			d.Agents[i].State = s
		}
		return d
	}
	still := data
	still.NoAnim = true
	stand := horse(still, 0)

	// busy legs: the four steps of the gait (two busy legs make a step every two frames) are four different pictures
	seen := map[string]int{}
	for f := 0; f < 8; f += 2 {
		seen[horse(data, f)] = f
	}
	if len(seen) != 4 {
		t.Errorf("two busy legs over eight frames show %d different horses, want 4", len(seen))
	}
	if _, ok := seen[stand]; ok {
		t.Errorf("a horse with busy legs looks like the standing one")
	}
	if horse(data, 0) != horse(data, 1) || horse(data, 2) != horse(data, 3) {
		t.Errorf("two busy legs step every two frames: frames 0 and 1 are one step")
	}

	// every leg busy: a step a frame or a little faster, every one different from standing
	busy := withStates(widget.StateTool)
	pictures := map[string]bool{}
	for f := 0; f < 4; f++ {
		pic := horse(busy, f)
		pictures[pic] = true
		if pic == stand {
			t.Errorf("all legs busy and the horse stands at frame %d", f)
		}
	}
	if len(pictures) != 4 {
		t.Errorf("all legs busy over four frames show %d different horses, want 4", len(pictures))
	}

	// nobody busy, or no animation: the standing horse, whatever the frame
	for _, c := range []struct {
		name string
		d    widget.DashboardData
	}{{"idle", withStates(widget.StateIdle)}, {"thinking", withStates(widget.StateThinking)}, {"waiting", withStates(widget.StateWait)}, {"stuck", withStates(widget.StateStuck)}, {"no animation", still}} {
		for f := 0; f < 12; f++ {
			if got := horse(c.d, f); got != stand {
				t.Fatalf("%s: the horse at frame %d is not the standing one:\n%s\n%s", c.name, f, got, stand)
			}
		}
	}
	noAnimBusy := busy
	noAnimBusy.NoAnim = true
	if horse(noAnimBusy, 3) != stand {
		t.Errorf("--no-anim with every leg busy: the horse must stand")
	}

	// the spinner of a thinking agent turns with the frame; an agent that is idle does not move
	row := func(f int, id string) string { return showLine(at(data, f), "│ "+id+" ") }
	if row(0, "m0") == row(1, "m0") || row(1, "m0") == row(2, "m0") {
		t.Errorf("the thinking agent's spinner does not turn:\n%s\n%s", row(0, "m0"), row(1, "m0"))
	}
	if row(0, "w5") != row(1, "w5") || row(1, "w5") != row(7, "w5") {
		t.Errorf("an idle agent's row changes with the frame: %q", row(1, "w5"))
	}
}

func TestDashboardMailArrives(t *testing.T) {
	p := widget.DefaultPalette()
	data := showSketchDashboard(8) // the last mail, w2 to w1, is born at frame 10
	mail := func(f int) string { return showLine(widget.Dashboard(data, 120, 48, f, p), "✉ w2 ➜ w1") }
	for _, c := range []struct {
		frame int
		has   string
		not   string
	}{
		{0, "✉ w2 ➜ w1", "“"},
		{10, "✉ w2 ➜ w1", "“"},
		{11, "“rename”", "ListO"},
		{12, "“rename ListO”", "Options"},
		{13, "“rename ListOptions?”", ""},
		{14, "“rename ListOptions?”", ""},
	} {
		got := mail(c.frame)
		if got == "" || !strings.Contains(got, c.has) || c.not != "" && strings.Contains(got, c.not) {
			t.Errorf("frame %d: the mail row is %q, want %q and not %q", c.frame, got, c.has, c.not)
		}
	}
	if a, b := mail(14), mail(200); a != b {
		t.Errorf("a settled mail changes: %q and %q", a, b)
	}
}

func TestDashboardAnimationRepeatsAndAnyFrameWorks(t *testing.T) {
	p := widget.DefaultPalette()
	data := showSketchDashboard(8)
	data.Mail[3].Born = -1 // the arrival of a mail is the one thing that does not repeat
	// the spinners have a period of 10 frames and the gait of 32: the whole cockpit repeats every 160
	for _, f := range []int{0, 1, 7, 13, 99, -1, -33} {
		a := widget.Dashboard(data, 100, 40, f, p)
		if b := widget.Dashboard(data, 100, 40, f+160, p); !reflect.DeepEqual(a, b) {
			t.Errorf("frame %d and frame %d differ:\n%s\n%s", f, f+160, showPlain(a), showPlain(b))
		}
	}
	for _, f := range []int{math.MinInt, math.MinInt + 1, -1, math.MaxInt - 1, math.MaxInt} {
		for _, sz := range showDashSizes {
			if ls := widget.Dashboard(data, sz.w, sz.h, f, p); len(ls) != sz.h {
				t.Errorf("frame %d at %dx%d: %d lines", f, sz.w, sz.h, len(ls))
			}
		}
	}
}

// showHostileDashboard is the sketch swarm with every piece of text that comes from outside replaced by the nastiest strings there
// are, one after the other.
func showHostileDashboard() widget.DashboardData {
	d := showSketchDashboard(8)
	n := 0
	next := func() string {
		n++
		return showHostile[n%len(showHostile)]
	}
	d.Title = next()
	for i := range d.Agents {
		a := &d.Agents[i]
		a.ID, a.Role, a.Doing, a.Lease = next(), next(), next(), next()
	}
	for i := range d.Kanban {
		d.Kanban[i].Title = next()
		for k := range d.Kanban[i].Cards {
			d.Kanban[i].Cards[k].ID, d.Kanban[i].Cards[k].Label = next(), next()
		}
	}
	for i := range d.Merge {
		d.Merge[i].ID, d.Merge[i].Note, d.Merge[i].Worker = next(), next(), next()
	}
	for i := range d.Mail {
		d.Mail[i].From, d.Mail[i].To, d.Mail[i].Subject = next(), next(), next()
	}
	for i := range d.Feed {
		d.Feed[i].Agent, d.Feed[i].Text, d.Feed[i].Tail = next(), next(), next()
	}
	for i := range d.Shared {
		d.Shared[i].Name, d.Shared[i].Hash = next(), next()
	}
	return d
}

func TestDashboardOddData(t *testing.T) {
	p := widget.DefaultPalette()

	// nothing to show at all: the zero value still draws a box of the size
	for _, sz := range showDashSizes {
		what := fmt.Sprintf("the zero value at %dx%d", sz.w, sz.h)
		ls := widget.Dashboard(widget.DashboardData{}, sz.w, sz.h, 0, p)
		if len(ls) != sz.h {
			t.Fatalf("%s: %d lines", what, len(ls))
		}
		showNoWider(t, what, ls, sz.w)
		if title := ls[0].Plain(); !strings.Contains(title, "SLEIPNIR") || !strings.Contains(title, "0 agents") {
			t.Errorf("%s: the title bar is %q", what, title)
		}
		if ids, more := showTable(ls); len(ids) != 0 || more != 0 {
			t.Errorf("%s: an empty swarm lists %v and %d more", what, ids, more)
		}
	}

	// no size, no picture
	for _, sz := range []struct{ w, h int }{{0, 0}, {0, 10}, {10, 0}, {-1, 10}, {10, -1}, {-5, -5}, {math.MinInt, 5}, {5, math.MinInt}} {
		if ls := widget.Dashboard(showSketchDashboard(8), sz.w, sz.h, 0, p); len(ls) != 0 {
			t.Errorf("%dx%d draws %d lines, want none", sz.w, sz.h, len(ls))
		}
	}

	// a huge swarm: the table shows a few, says how many are left, keeps the stuck ones, and the fan counts the riders
	big := showSketchDashboard(3000)
	for _, sz := range showDashSizes {
		what := fmt.Sprintf("3000 agents at %dx%d", sz.w, sz.h)
		ls := widget.Dashboard(big, sz.w, sz.h, 5, p)
		if len(ls) != sz.h {
			t.Fatalf("%s: %d lines", what, len(ls))
		}
		showNoWider(t, what, ls, sz.w)
		ids, more := showTable(ls)
		if len(ids) == 0 || len(ids)+more != 3000 {
			t.Errorf("%s: %d agents listed and %d more", what, len(ids), more)
		}
		if !strings.Contains(showPlain(ls), "one prefix, 3000 riders") {
			t.Errorf("%s: the fan does not count the riders", what)
		}
	}

	// text from outside is data: nothing in it acts on the terminal or draws wider than the cell it was given
	hostile := showHostileDashboard()
	for _, sz := range append([]struct{ w, h int }{{59, 20}, {30, 10}}, showDashSizes...) {
		what := fmt.Sprintf("hostile text at %dx%d", sz.w, sz.h)
		ls := widget.Dashboard(hostile, sz.w, sz.h, 3, p)
		showNoWider(t, what, ls, sz.w)
		showNoControl(t, what, ls)
	}
}
