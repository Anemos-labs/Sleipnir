package app

// The team's cockpit as a page of the chat: the screen of `sleipnir watch` (the horse, the agents, the gantt, the board, the merge queue,
// the mail, the governor), drawn on the alternate screen from what the chat already knows, with the prompt under it so that a message can be
// sent to the manager meanwhile. It opens by itself when the first worker of a turn starts, closes by itself when the turn ends (the answer
// is written into the scrollback, which the page hides) or when a question needs the person, and ctrl+g opens and closes it.

import (
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// cockpitHints are the keys the page has, under the cockpit.
var cockpitHints = []widget.Hint{{Text: "ctrl+g back to the chat", Rank: 1}, {Text: "type to message the manager", Rank: 2}, {Text: "esc stops the turn", Rank: 3}, {Text: "ctrl+t stats", Rank: 4}}

// minCockpit is the smallest terminal the page is shown on: below it the chat is what fits.
const minCockpitCols, minCockpitRows = 60, 16

// toggleCockpit is ctrl+g.
func (m *chatModel) toggleCockpit() {
	if m.cols < minCockpitCols || m.rows < minCockpitRows {
		m.setHint("the terminal is too small for the cockpit")
		return
	}
	m.cockpit = !m.cockpit
	m.cockpitTurn = true // the person has chosen for this turn
}

// workersBusy reports whether an agent that is not the one the person talks to is working.
func (m *chatModel) workersBusy(sn *state.Snapshot) bool {
	for _, a := range sn.Agents {
		if m.isMain(a.ID) {
			continue
		}
		switch a.Status {
		case state.StatusThinking, state.StatusTool, state.StatusEditing:
			return true
		}
	}
	return false
}

// autoCockpit opens the page when a worker starts, once for each turn, unless the person chose already.
func (m *chatModel) autoCockpit() {
	if m.cockpit || m.cockpitTurn || !m.info.Swarm || m.running == nil || m.cols < minCockpitCols || m.rows < minCockpitRows {
		return
	}
	if m.workersBusy(m.snapshot()) {
		m.cockpit, m.cockpitTurn = true, true
	}
}

// drawCockpit draws the page, when it is up, and reports whether it did. A question closes it: the dialog is in the live region, and the
// request it asks about may have been written into the scrollback.
func (m *chatModel) drawCockpit() bool {
	switch {
	case m.question() != nil:
		if m.cockpit {
			m.cockpit, m.cockpitBack = false, true
		}
	case m.cockpitBack:
		m.cockpit, m.cockpitBack = m.running != nil, false // the question is answered: the page comes back while the turn goes on
	}
	if m.cockpit && (m.cols < minCockpitCols || m.rows < minCockpitRows || !m.info.Swarm) {
		m.cockpit = false
	}
	if !m.cockpit {
		m.scr.SetFull(nil)
		return false
	}
	v := m.liveView(nil)
	v.tools, v.fold, v.tail, v.status = nil, nil, nil, statusView{} // the cockpit shows them
	bottom := m.k.liveLines(&v)
	height := max(m.rows-len(bottom.lines), 8)
	status := "● live"
	if m.running == nil {
		status = "○ idle"
	}
	top := Cockpit(m.snapshot(), m.cols, height, m.frame, m.k.Palette, CockpitOptions{NoAnim: !m.k.Anim, G0: m.mem.g0est(), Hints: cockpitHints, Status: status})
	page := append(top, bottom.lines...)
	m.scr.SetFull(page)
	if bottom.curRow >= 0 {
		m.scr.SetCursor(len(top)+bottom.curRow, bottom.curCol)
	} else {
		m.scr.SetCursor(-1, 0)
	}
	return true
}
