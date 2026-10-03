// What an agent is doing, as one glyph and one colour, and the heatmap that shows a whole swarm at once: one cell per agent
// (docs/UX.md: at more than about 16 agents the table becomes a heatmap; the legend of swarm.png).

package widget

import (
	"strconv"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// AgentState is what an agent is doing right now.
type AgentState uint8

const (
	// StateIdle waits for work (◌).
	StateIdle AgentState = iota
	// StateThinking waits for the model (⠹).
	StateThinking
	// StateTool runs a tool (⚙).
	StateTool
	// StateEdit edits a file (✎).
	StateEdit
	// StateWait waits for mail or for another agent (✉).
	StateWait
	// StateStuck has tripped the repeat guard (⚠).
	StateStuck
	// StateDone has finished (✓).
	StateDone
)

const heatStateCount = 7

// heatOrder is the order of the legend.
var heatOrder = [heatStateCount]AgentState{StateThinking, StateTool, StateEdit, StateWait, StateIdle, StateDone, StateStuck}

// valid maps unsupported agent-state values to idle.
func (s AgentState) valid() AgentState {
	if s >= heatStateCount {
		return StateIdle
	}
	return s
}

// Glyph is the state as one cell: what the colour says is also said by the shape. An unknown state is idle.
func (s AgentState) Glyph() string {
	switch s.valid() {
	case StateThinking:
		return "⠹"
	case StateTool:
		return "⚙"
	case StateEdit:
		return "✎"
	case StateWait:
		return "✉"
	case StateStuck:
		return "⚠"
	case StateDone:
		return "✓"
	}
	return "◌"
}

// Word is the state as a short word (think, tool, edit, wait, idle, done, stuck).
func (s AgentState) Word() string {
	switch s.valid() {
	case StateThinking:
		return "think"
	case StateTool:
		return "tool"
	case StateEdit:
		return "edit"
	case StateWait:
		return "wait"
	case StateStuck:
		return "stuck"
	case StateDone:
		return "done"
	}
	return "idle"
}

// style colours the state: violet thinking, green tool, blue edit, amber waiting, dim idle, teal done, red and Bold stuck. Only
// stuck is Bold and only idle is Dim, so a screenshot without colour still shows what needs attention and what does not.
func (s AgentState) style(p Palette) cell.Style {
	switch s.valid() {
	case StateThinking:
		return p.accentSt()
	case StateTool:
		return p.goodSt()
	case StateEdit:
		return p.layerSt(1)
	case StateWait:
		return p.warnSt()
	case StateStuck:
		return p.badSt()
	case StateDone:
		return p.layerSt(2)
	}
	return p.dimSt()
}

// heatCounts is how many agents are in each state.
func heatCounts(states []AgentState) [heatStateCount]int {
	var n [heatStateCount]int
	for _, s := range states {
		n[s.valid()]++
	}
	return n
}

// Heatmap draws one cell per agent, cols to a row, coloured and shaped by its state, with a legend that counts the states:
//
//	⠹ ⚙ ⚙ ✎ ⠹ ◌ ◌ ✓ ⚠ ⚙
//	⚙ ⠹ ✉ ✉ ◌ ⚙ ⚙ ⠹ ✓ ◌
//
//	⠹ think 12   ⚙ tool 18
//	✎ edit   3   ✉ wait  7
//	◌ idle   6   ✓ done  3
//	⚠ stuck  1
//
// The agents are in the order given, row by row. Every state has its own glyph (⠹ ⚙ ✎ ✉ ◌ ✓ ⚠), so nothing depends on colour;
// stuck is Bold, idle is Dim. The legend is in two columns when the grid is wide enough for them and in one otherwise.
//
// cols below 1 is 10, and more columns than agents is one row. No agents draws nothing. A state that is not one of the seven
// is idle.
func Heatmap(states []AgentState, cols int, p Palette) []cell.Line {
	n := len(states)
	if n == 0 {
		return nil
	}
	if cols < 1 {
		cols = 10
	}
	if cols > n {
		cols = n
	}
	var out []cell.Line
	for i := 0; i < n; i += cols {
		var b showRowBuf
		for j := i; j < i+cols && j < n; j++ {
			if j > i {
				b.space(1)
			}
			b.add(states[j].style(p), states[j].Glyph())
		}
		out = append(out, b.line())
	}
	out = append(out, nil)

	counts := heatCounts(states)
	numW := len(strconv.Itoa(n))
	entry := func(s AgentState) cell.Line {
		var b showRowBuf
		b.add(s.style(p), s.Glyph()+" ").add(p.dimSt(), showPadR(s.Word(), 5)).add(cell.Style{}, " "+showPadL(strconv.Itoa(counts[s]), numW))
		return b.line()
	}
	entryW := 2 + 5 + 1 + numW
	gridW := 2*cols - 1
	perRow := 1
	if gridW >= 2*entryW+1 {
		perRow = 2
	}
	for i := 0; i < heatStateCount; i += perRow {
		var b showRowBuf
		for k := 0; k < perRow && i+k < heatStateCount; k++ {
			if k > 0 {
				b.space(showClamp(gridW-2*entryW, 1, 3))
			}
			b.addLine(entry(heatOrder[i+k]))
		}
		out = append(out, b.line())
	}
	return out
}
