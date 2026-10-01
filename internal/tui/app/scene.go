package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// View is one of the screens of the watch and replay programs.
type View uint8

// The views. The cockpit is the swarm at a glance, the others are the same session looked at closer: the cache of one agent (the
// prompt it sent, how much of it the provider had kept, what happened to the hit ratio, the compactions, the misses), the mail and
// the task board with the merge queue.
const (
	ViewCockpit View = iota
	ViewCache
	ViewMail
	ViewBoard
	numViews
)

var viewNames = [numViews]string{"cockpit", "cache", "mail", "board"}

// String is the name of the view, as --view takes it.
func (v View) String() string {
	if v < numViews {
		return viewNames[v]
	}
	return "view" + fmt.Sprint(uint8(v))
}

// ParseView is the view a name stands for: its name, or the letter of the key that chooses it (o for the cockpit, which is also
// called overview); the empty name is the cockpit.
func ParseView(s string) (View, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "o", "overview", "cockpit":
		return ViewCockpit, nil
	case "c", "cache":
		return ViewCache, nil
	case "m", "mail":
		return ViewMail, nil
	case "b", "board":
		return ViewBoard, nil
	}
	return ViewCockpit, fmt.Errorf("unknown view %q (one of: %s)", s, strings.Join(viewNames[:], ", "))
}

// Mode is how the screen stands to the session it shows.
type Mode struct {
	// Replay says that the session is a recording being played, not one being written now.
	Replay bool
	// Paused says that the screen is held: a replay does not advance, a live view does not change.
	Paused bool
	// Speed is how many seconds of the recording pass in a second of the screen's time (replay).
	Speed float64
	// At is how far into the recording the screen is and Total how long it is (zero when it is not known), replay only.
	At, Total time.Duration
	// Final says that the screen is one still picture of a session, its last (replay --final).
	Final bool
	// QuitLive and QuitEnded say what q does, in the row of keys, while the session is under way and after it ended; "quit" when
	// empty. A program that prints something when the screen closes says so.
	QuitLive, QuitEnded string
}

// label is the first of the title bar's stats: what the screen is doing with the session.
func (m Mode) label(ended bool) string {
	switch {
	case m.Final && ended:
		return "■ ended"
	case m.Final:
		return "■ stopped"
	case m.Paused:
		return "⏸ paused"
	case m.Replay:
		s := "▶ replay"
		if m.Speed > 0 && m.Speed != 1 {
			s += " " + speedLabel(m.Speed)
		}
		if m.Total > 0 {
			s += " " + widget.Duration(m.At.Round(time.Second)) + "/" + widget.Duration(m.Total.Round(time.Second))
		}
		return s
	case ended:
		return "■ ended"
	}
	return "● live"
}

// speedLabel is a replay speed as a person says it: 4×, 0.5×, 1.5×.
func speedLabel(v float64) string {
	if v == float64(int(v)) {
		return fmt.Sprintf("%d×", int(v))
	}
	return fmt.Sprintf("%.2g×", v)
}

// Scene is everything one screen of the program is made of: the session as it stands, the view, the animation frame and the size.
// Draw makes the lines of it; it reads nothing else and draws the same lines for the same scene.
type Scene struct {
	Snap *state.Snapshot
	View View
	// Agent is the agent the cache view is about; empty or unknown is the first one.
	Agent string
	// Frame is the frame of the animations (UIFPS a second).
	Frame int
	// Cols and Rows are the size of the screen.
	Cols, Rows int
	Pal        widget.Palette
	// Mem is what the program remembers between frames (when things arrived, G0); NoAnim turns the animations off.
	Mem    *Memory
	NoAnim bool
	Mode   Mode
}

// Draw is the screen of the scene: exactly Rows lines of at most Cols cells.
func Draw(s Scene) []cell.Line {
	if s.Cols <= 0 || s.Rows <= 0 {
		return nil
	}
	return exact(draw(s), s.Rows)
}

// exact makes a screen exactly rows lines: what a widget draws in a list that is shorter than the screen is padded with blank
// lines, and anything beyond the screen is cut.
func exact(lines []cell.Line, rows int) []cell.Line {
	if len(lines) > rows {
		return lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, nil)
	}
	return lines
}

func draw(s Scene) []cell.Line {
	sn := s.Snap
	if sn == nil || sn.Stats.Events == 0 {
		return waiting(s)
	}
	switch s.View {
	case ViewCache:
		return cacheView(s)
	case ViewMail:
		return mailView(s)
	case ViewBoard:
		return boardView(s)
	}
	return Cockpit(sn, s.Cols, s.Rows, s.Frame, s.Pal, CockpitOptions{
		NoAnim: s.NoAnim, Born: s.Mem.born(), G0: s.Mem.g0est(), Hints: hintsFor(s), Status: s.Mode.label(sn.Session.Ended),
	})
}

// hintsFor are the keys of the screen: the ones the program has, in the order a person looks for them; when the width is short
// the ones at the end of the list of ranks go first and the way out last.
func hintsFor(s Scene) []widget.Hint {
	h := []widget.Hint{}
	if s.Mode.Replay {
		h = append(h, widget.Hint{Text: "space pause", Rank: 3}, widget.Hint{Text: "←→ seek", Rank: 5}, widget.Hint{Text: "+/- speed", Rank: 6})
	} else {
		h = append(h, widget.Hint{Text: "space pause", Rank: 3})
	}
	quit := "quit"
	if s.Snap != nil && s.Snap.Session.Ended && s.Mode.QuitEnded != "" {
		quit = s.Mode.QuitEnded
	} else if (s.Snap == nil || !s.Snap.Session.Ended) && s.Mode.QuitLive != "" {
		quit = s.Mode.QuitLive
	}
	h = append(h,
		widget.Hint{Text: "o cockpit", Rank: 7}, widget.Hint{Text: "c cache", Rank: 8}, widget.Hint{Text: "m mail", Rank: 9},
		widget.Hint{Text: "b board", Rank: 10}, widget.Hint{Text: "↑↓ agent", Rank: 4},
		widget.Hint{Text: "? help", Rank: 2}, widget.Hint{Text: "q " + quit, Rank: 1})
	return h
}

// stats are the right-hand side of the title bar of the views other than the cockpit.
func stats(s Scene) []string {
	sn := s.Snap
	agents := 0
	for _, a := range sn.Agents {
		if !a.Service {
			agents++
		}
	}
	noun := " agents"
	if agents == 1 {
		noun = " agent"
	}
	return []string{
		s.Mode.label(sn.Session.Ended),
		"◷ " + widget.Duration(elapsed(sn).Round(time.Second)),
		fmt.Sprint(agents) + noun,
		widget.USD(sn.Totals.CostUSD),
		"⛁ " + widget.Percent(sn.Totals.HitRatio()),
	}
}

// frame puts bands in the box of the cockpit, under the name of the view.
func frame(s Scene, title string, bands []widget.ViewBand) []cell.Line {
	task := title
	if g := clean(s.Snap.Session.Goal); g != "" {
		task += " · " + g
	}
	return widget.ViewFrame(task, stats(s), bands, hintsFor(s), s.Cols, s.Rows, s.Pal)
}

// waiting is the screen of a session that has not written its first event.
func waiting(s Scene) []cell.Line {
	st := stylesOf(s.Pal)
	msg := cell.Styled(st.dim, "waiting for the first event of the session…")
	if s.Mode.Replay {
		msg = cell.Styled(st.dim, "this recording holds no events")
	}
	body := []cell.Line{nil, nil, msg}
	out := make([]cell.Line, 0, s.Rows)
	out = append(out, fit(cell.Join(cell.Styled(st.accent, "SLEIPNIR "), cell.Styled(st.dim, s.Mode.label(false))), s.Cols))
	for _, l := range body {
		out = append(out, fit(l, s.Cols))
	}
	for len(out) < s.Rows {
		out = append(out, nil)
	}
	return out[:s.Rows]
}
