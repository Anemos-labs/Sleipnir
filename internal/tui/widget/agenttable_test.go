package widget_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

func TestAgentTableGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("AgentTable: the agents of the sketch; the bar is the prompt (inherited, then own; bright = from the cache, ▓ where that ends, dim = paid)")
	rows := showSketchAgents()
	for _, w := range []int{100, 84, 70, 56, 44, 32, 20} {
		d.Add(fmt.Sprintf("eight agents, width %d", w), widget.AgentTable(rows, w, 0, p), w)
	}
	d.Note("the spinner of a thinking agent turns with the frame")
	for f := 0; f < 3; f++ {
		lines := widget.AgentTable(rows[:5], 84, f, p)
		d.Add(fmt.Sprintf("frame %d", f), lines, 84)
	}
	d.Note("odd rows")
	odd := []widget.AgentRow{
		{ID: "a", State: widget.StateStuck},
		{ID: "long-agent-name", Role: "very-long-role-name", RoleColor: 99, State: 77, Doing: "x", Shared: 1 << 40, Own: -5, Hit: math.NaN(), Cost: math.Inf(1)},
		{ID: "b", Shared: 10, Own: 10, Hit: 5, Cost: -1, Lease: "docs/**/*.md"},
	}
	d.Add("odd rows", widget.AgentTable(odd, 84, 0, p), 84)
	showGolden(t, "agenttable", &d)
}

func TestAgentTableStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("role colours on the ids, state colours on the states, hit: good from 93%, warn from 89%, else bad and Bold; the bar: G0..G2 inherited, the role colour for its own part")
	rows := showSketchAgents()
	d.AddText("m0, w1 and w7 at width 84", showtest.FlattenStyled(widget.AgentTable([]widget.AgentRow{rows[0], rows[1], rows[7]}, 84, 0, p), showNames(p)))
	showGolden(t, "agenttable_styled", &d)
}

func TestAgentTableColumnsGiveWayInOrder(t *testing.T) {
	p := widget.MonoPalette()
	rows := showSketchAgents()
	headers := map[int]string{}
	for w := 12; w <= 120; w++ {
		headers[w] = widget.AgentTable(rows, w, 0, p)[0].Plain()
	}
	// a column that is there at a width is there at every wider width, and they go in the order lease, cost, tok, prompt
	order := []string{"LEASE", "COST", "TOK", "PROMPT"}
	for w := 12; w <= 120; w++ {
		var gone []string
		for _, col := range order {
			if !strings.Contains(headers[w], col) {
				gone = append(gone, col)
			}
			if w > 12 && strings.Contains(headers[w-1], col) && !strings.Contains(headers[w], col) {
				t.Fatalf("%s is at width %d and gone at %d", col, w-1, w)
			}
		}
		for i, col := range gone {
			if order[i] != col {
				t.Fatalf("width %d: %v have given way, but the order is lease, cost, tok, prompt", w, gone)
			}
		}
	}
	for _, col := range []string{"LEASE", "PROMPT"} {
		if !strings.Contains(headers[100], col) {
			t.Errorf("everything fits at 100: no %s", col)
		}
	}
	for _, col := range []string{"AGENT", "STATE"} {
		if !strings.Contains(headers[30], col) {
			t.Errorf("the agent and its state always stay: no %s at 30", col)
		}
	}
}

func TestAgentBarShowsWhatWasServedFromTheCache(t *testing.T) {
	p := widget.MonoPalette()
	bar := func(hit float64) string {
		row := []widget.AgentRow{{ID: "a", Shared: 30000, Own: 30000, Hit: hit}}
		lines := widget.AgentTable(row, 56, 0, p)
		plain := lines[1].Plain()
		start := strings.IndexAny(plain, "█▓░")
		end := strings.LastIndexAny(plain, "█▓░▏")
		return string([]rune(plain)[len([]rune(plain[:start])) : len([]rune(plain[:end]))+1])
	}
	full, half, none := bar(1), bar(0.5), bar(0)
	if strings.ContainsAny(full, "░▓") || !strings.Contains(full, "█") {
		t.Errorf("all cached: %q", full)
	}
	if !strings.Contains(half, "█") || !strings.Contains(half, "░") || !strings.Contains(half, "▓") {
		t.Errorf("half cached: %q", half)
	}
	if strings.ContainsAny(none, "█▓") {
		t.Errorf("nothing cached: %q", none)
	}
	if !strings.Contains(half, "▏") {
		t.Errorf("the inherited part ends at a tick: %q", half)
	}
	if strings.Count(full, "█")+strings.Count(full, "▏") != len([]rune(full)) {
		t.Errorf("a full bar is solid but for the tick: %q", full)
	}
}

func TestAgentTableStates(t *testing.T) {
	p := widget.DefaultPalette()
	lines := widget.AgentTable(showSketchAgents(), 100, 0, p)
	text := showtest.Flatten(lines)
	for _, want := range []string{"⠋ think", "⚙ tool", "✎ edit", "✉ wait", "◌ idle", "✓ done", "⚠ stuck", "m0 manager", "w7 docs", "$0.012", "96%", "orders/*", "—"} {
		if !strings.Contains(text, want) {
			t.Errorf("the table lacks %q:\n%s", want, text)
		}
	}
	// the hit ratio's colour, and stuck stands out
	var stuck, low cell.Style
	for _, l := range lines {
		for _, sp := range l {
			if strings.Contains(sp.Text, "⚠") {
				stuck = sp.Style
			}
			if strings.Contains(sp.Text, "88%") {
				low = sp.Style
			}
		}
	}
	if !stuck.Has(cell.Bold) || !low.Has(cell.Bold) || low.FG != p.Bad {
		t.Errorf("stuck %+v, 88%% %+v: both are loud", stuck, low)
	}
}

func TestAgentTableEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.AgentTable(nil, 50, 0, p) != nil || widget.AgentTable(showSketchAgents(), 0, 0, p) != nil || widget.AgentTable(showSketchAgents(), -2, 0, p) != nil {
		t.Error("no rows or no width draws nothing")
	}
	evil := []widget.AgentRow{{ID: "a\x1b[31m", Role: "b\nc", Doing: "\x1b]0;x\x07rm", Lease: "\t"}}
	got := widget.AgentTable(evil, 60, 0, p)
	showNoControl(t, "agent table", got)
	if got := widget.AgentTable(showSketchAgents(), 100, -3, p); len(got) != 9 {
		t.Errorf("a negative frame: %d lines", len(got))
	}
}
