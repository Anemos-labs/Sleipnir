package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
	"github.com/anemos-labs/sleipnir/internal/tui/widget/showtest"
)

var showAllStates = []widget.AgentState{widget.StateIdle, widget.StateThinking, widget.StateTool, widget.StateEdit, widget.StateWait, widget.StateStuck, widget.StateDone}

func TestHeatmapGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("Heatmap: one cell per agent, a glyph per state (⠹ thinking ⚙ tool ✎ edit ✉ wait ◌ idle ✓ done ⚠ stuck) and a legend with counts")
	eight := showStates(showSwarm(8, 7))
	d.Add("the sketch's eight agents, 8 columns", widget.Heatmap(eight, 8, p), 15)
	fifty := showStates(showSwarm(50, 7))
	d.Add("fifty agents, 10 columns", widget.Heatmap(fifty, 10, p), 19)
	d.Add("fifty agents, 7 columns (a one-column legend)", widget.Heatmap(fifty, 7, p), 13)
	d.Add("fifty agents, 25 columns", widget.Heatmap(fifty, 25, p), 49)
	d.Add("three agents, more columns than agents", widget.Heatmap(eight[:3], 10, p), 5)
	d.Add("default columns", widget.Heatmap(fifty[:23], 0, p), 19)
	showGolden(t, "heatmap", &d)
}

func TestHeatmapStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("think accent, tool good, edit G1, wait warn, idle dim, done G2, stuck bad and Bold")
	d.AddText("one of each", showtest.FlattenStyled(widget.Heatmap(showAllStates, 7, p), showNames(p)))
	showGolden(t, "heatmap_styled", &d)
}

func TestHeatmapStatesHaveTheirOwnGlyph(t *testing.T) {
	seen := map[string]widget.AgentState{}
	words := map[string]widget.AgentState{}
	for _, s := range showAllStates {
		if prev, dup := seen[s.Glyph()]; dup {
			t.Errorf("states %v and %v share the glyph %s: colour would be the only difference", prev, s, s.Glyph())
		}
		seen[s.Glyph()] = s
		if prev, dup := words[s.Word()]; dup {
			t.Errorf("states %v and %v share the word %s", prev, s, s.Word())
		}
		words[s.Word()] = s
		if cell.StringWidth(s.Glyph()) != 1 {
			t.Errorf("the glyph of %v is %d cells wide", s, cell.StringWidth(s.Glyph()))
		}
	}
	if widget.AgentState(99).Glyph() != widget.StateIdle.Glyph() || widget.AgentState(99).Word() != "idle" {
		t.Error("an unknown state is idle")
	}
}

func TestHeatmapCellsAndLegendAgree(t *testing.T) {
	p := widget.MonoPalette()
	states := showStates(showSwarm(50, 11))
	for _, cols := range []int{1, 3, 10, 13, 50} {
		lines := widget.Heatmap(states, cols, p)
		var grid []string
		i := 0
		for ; i < len(lines) && lines[i].Plain() != ""; i++ {
			grid = append(grid, lines[i].Plain())
		}
		if want := (len(states) + cols - 1) / cols; len(grid) != want {
			t.Fatalf("cols %d: %d grid rows, want %d", cols, len(grid), want)
		}
		// the n-th glyph is the n-th agent's state
		n := 0
		for _, row := range grid {
			for _, g := range strings.Fields(row) {
				if g != states[n].Glyph() {
					t.Fatalf("cols %d: cell %d is %s, agent %d is %v (%s)", cols, n, g, n, states[n], states[n].Glyph())
				}
				n++
			}
		}
		if n != len(states) {
			t.Fatalf("cols %d: %d cells for %d agents", cols, n, len(states))
		}
		// the legend counts add up to the agents
		legend := strings.Join(func() []string {
			var s []string
			for _, l := range lines[i:] {
				s = append(s, l.Plain())
			}
			return s
		}(), "\n")
		sum := 0
		for _, s := range showAllStates {
			var count int
			idx := strings.Index(legend, s.Glyph()+" "+s.Word())
			if idx < 0 {
				t.Fatalf("cols %d: the legend has no %s %s:\n%s", cols, s.Glyph(), s.Word(), legend)
			}
			if _, err := fmt.Sscanf(strings.TrimLeft(legend[idx+len(s.Glyph()+" "+s.Word()):], " "), "%d", &count); err != nil {
				t.Fatalf("cols %d: no count after %s: %v", cols, s.Word(), err)
			}
			sum += count
		}
		if sum != len(states) {
			t.Errorf("cols %d: the legend counts %d agents, there are %d", cols, sum, len(states))
		}
	}
}

func TestHeatmapStuckIsBoldAndIdleIsDim(t *testing.T) {
	p := widget.DefaultPalette()
	lines := widget.Heatmap([]widget.AgentState{widget.StateStuck, widget.StateIdle, widget.StateTool}, 3, p)
	row := lines[0]
	find := func(g string) cell.Style {
		for _, sp := range row {
			if strings.Contains(sp.Text, g) {
				return sp.Style
			}
		}
		t.Fatalf("no %s in %q", g, row.Plain())
		return cell.Style{}
	}
	if !find("⚠").Has(cell.Bold) {
		t.Error("stuck is Bold")
	}
	if find("⚙").Has(cell.Bold) {
		t.Error("only stuck is Bold")
	}
	m := widget.Heatmap([]widget.AgentState{widget.StateIdle}, 1, widget.MonoPalette())
	if !m[0][0].Style.Has(cell.Dim) {
		t.Errorf("idle is Dim without colour: %+v", m[0][0].Style)
	}
}

func TestHeatmapEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.Heatmap(nil, 10, p) != nil || widget.Heatmap([]widget.AgentState{}, 10, p) != nil {
		t.Error("no agents draws nothing")
	}
	if got := widget.Heatmap([]widget.AgentState{widget.StateTool}, -4, p); len(got) == 0 || got[0].Plain() != "⚙" {
		t.Errorf("negative columns: %v", showtest.Flatten(got))
	}
	if got := widget.Heatmap([]widget.AgentState{99, 200}, 2, p); got[0].Plain() != "◌ ◌" {
		t.Errorf("unknown states are idle: %q", got[0].Plain())
	}
}
