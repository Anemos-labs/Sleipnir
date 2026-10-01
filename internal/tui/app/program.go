package app

import (
	"context"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// Screen is the full-screen renderer a program draws on (render.Screen is one; a test brings a fake that keeps the frames).
type Screen interface {
	Enter() error
	Leave() error
	Resize(cols, rows int)
	Size() (cols, rows int)
	DrawLines(lines []cell.Line) error
}

// Config is what the watch and replay program is made of. Everything that touches the world is given: the session (Src), the keys,
// the terminal's size changes, the tick of the animation and the screen. The program reads no clock, starts no goroutine and does
// no I/O of its own, so a test drives it with channels it owns and looks at the frames it drew, and the real thing (RunTTY) only
// supplies the real ones.
type Config struct {
	Src    Source
	Screen Screen
	// Keys, Sizes and Tick are the events the program reacts to. A closed Keys ends the program (the terminal is gone), a closed or
	// nil Sizes or Tick is never heard from again.
	Keys  <-chan input.Key
	Sizes <-chan term.Size
	Tick  <-chan time.Time
	// Pal is the colours of the screen; NoAnim turns the animations off (the horse stands, nothing arrives).
	Pal    widget.Palette
	NoAnim bool
	// View and Agent are where the program starts.
	View  View
	Agent string
	// QuitLive and QuitEnded are what the row of keys says q does while the session is under way and after it ended ("quit" when
	// empty).
	QuitLive, QuitEnded string
}

// maxStep is the longest stretch of time one tick may move a replay by: a process that was suspended for an hour does not play the
// hour when it wakes.
const maxStep = 250 * time.Millisecond

// Run shows the session until the user quits (q, Esc on the cockpit, Ctrl-C), the keys end, or ctx is done. The screen is left
// whatever way it ends. It returns nil when the user quit, ctx.Err() when ctx ended it and the error of the screen or the source
// otherwise.
func Run(ctx context.Context, c Config) (err error) {
	if err = c.Screen.Enter(); err != nil {
		return err
	}
	defer func() {
		if lerr := c.Screen.Leave(); err == nil {
			err = lerr
		}
	}()
	m := newModel(c)
	if err = m.draw(); err != nil {
		return err
	}
	keys, sizes, tick := c.Keys, c.Sizes, c.Tick
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case k, ok := <-keys:
			if !ok {
				// ReadKeys closes the keys when ctx is done, so a program that was told to stop finds the end of its keys and the
				// end of its context ready together, and select chooses between them at random. The context ended it.
				if err := ctx.Err(); err != nil {
					return err
				}
				return nil
			}
			if m.key(k) {
				return nil
			}
		case sz, ok := <-sizes:
			if !ok {
				sizes = nil
				continue
			}
			m.resize(sz.Width, sz.Height)
		case t, ok := <-tick:
			if !ok {
				tick = nil
				continue
			}
			dt := time.Duration(0)
			if !last.IsZero() {
				dt = min(max(t.Sub(last), 0), maxStep)
			}
			last = t
			m.tick(dt)
		}
		if err = m.src.Err(); err != nil {
			return err
		}
		if err = m.draw(); err != nil {
			return err
		}
	}
}

// model is the state of the program: what is on the screen and what the keys have chosen.
type model struct {
	c      Config
	src    Source
	rep    *ReplaySource // the source when it is a replay
	view   View
	agent  string // the agent the user chose; empty follows the one that was answered last
	frame  int
	paused bool
	help   bool
	noAnim bool
	mem    *Memory
	snap   *state.Snapshot // the snapshot on the screen; a paused screen holds the one it had
	cols   int
	rows   int
}

func newModel(c Config) *model {
	m := &model{c: c, src: c.Src, view: c.View, agent: c.Agent, noAnim: c.NoAnim, mem: NewMemory()}
	m.rep, _ = c.Src.(*ReplaySource)
	m.cols, m.rows = c.Screen.Size()
	m.snap = m.snapshot()
	if m.rep == nil { // a session that is under way: what happened before we looked does not arrive again
		m.mem.Seed(m.snap)
	} else {
		m.mem.Observe(m.snap, 0)
	}
	return m
}

func (m *model) snapshot() *state.Snapshot { return m.src.State().SnapshotAt(m.src.Now()) }

// resize lays the screen out for a new size of the terminal.
func (m *model) resize(cols, rows int) {
	m.c.Screen.Resize(cols, rows)
	m.cols, m.rows = m.c.Screen.Size()
}

// tick moves the animations and the session on by one step of the clock; a paused screen stands still.
func (m *model) tick(dt time.Duration) {
	if m.paused {
		return
	}
	m.src.Advance(dt)
	m.frame++
	m.snap = m.snapshot()
	m.mem.Observe(m.snap, m.frame)
}

// key reacts to a key and reports whether it was the one that ends the program.
func (m *model) key(k input.Key) (quit bool) {
	if k.Kind == input.KindPaste {
		return false
	}
	if k.IsRune('c', input.Ctrl) || k.IsRune('q', 0) || k.IsRune('Q', 0) {
		return true
	}
	if m.help { // any other key closes the help
		m.help = false
		return false
	}
	switch {
	case k.Is(input.Esc, 0):
		m.view = ViewCockpit // the way back from the views; on the cockpit it does nothing, quitting is q
	case k.IsRune('?', 0) || k.IsRune('h', 0) || k.Is(input.F1, 0):
		m.help = true
	case k.IsRune('o', 0) || k.IsRune('1', 0):
		m.view = ViewCockpit
	case k.IsRune('c', 0) || k.IsRune('2', 0):
		m.view = ViewCache
	case k.IsRune('m', 0) || k.IsRune('3', 0):
		m.view = ViewMail
	case k.IsRune('b', 0) || k.IsRune('4', 0):
		m.view = ViewBoard
	case k.Is(input.Tab, 0):
		m.view = (m.view + 1) % numViews
	case k.Is(input.Tab, input.Shift):
		m.view = (m.view + numViews - 1) % numViews
	case k.Is(input.Down, 0) || k.IsRune('j', 0):
		m.move(+1)
	case k.Is(input.Up, 0) || k.IsRune('k', 0):
		m.move(-1)
	case k.IsRune('f', 0):
		m.agent = "" // follow the agent that is answered
	case k.IsRune(' ', 0) || k.IsRune('p', 0):
		m.paused = !m.paused
		if !m.paused {
			m.snap = m.snapshot() // a live view that was held catches up
			if m.rep == nil {
				m.mem.Seed(m.snap)
			}
		}
	case k.IsRune('a', 0):
		m.noAnim = !m.noAnim
	case k.Is(input.Left, 0) || k.IsRune('H', 0):
		m.seek(-10 * time.Second)
	case k.Is(input.Right, 0) || k.IsRune('L', 0):
		m.seek(+10 * time.Second)
	case k.Is(input.Left, input.Shift):
		m.seek(-time.Minute)
	case k.Is(input.Right, input.Shift):
		m.seek(+time.Minute)
	case k.Is(input.Home, 0) || k.IsRune('0', 0):
		m.seekTo(0)
	case k.Is(input.End, 0) || k.IsRune('$', 0):
		if m.rep != nil {
			m.seekTo(m.rep.Total())
		}
	case k.IsRune('+', 0) || k.IsRune('=', 0) || k.IsRune(']', 0):
		if m.rep != nil {
			m.rep.Faster()
		}
	case k.IsRune('-', 0) || k.IsRune('_', 0) || k.IsRune('[', 0):
		if m.rep != nil {
			m.rep.Slower()
		}
	case k.IsRune('l', input.Ctrl):
		m.resize(m.cols, m.rows) // the terminal was scribbled on: draw everything again
	}
	return false
}

// seek moves a replay by d; a live session cannot be.
func (m *model) seek(d time.Duration) {
	if m.rep != nil {
		m.seekTo(m.rep.Elapsed() + d)
	}
}

func (m *model) seekTo(to time.Duration) {
	if m.rep == nil {
		return
	}
	if err := m.rep.Seek(to); err != nil {
		return
	}
	m.snap = m.snapshot()
	m.mem.Seed(m.snap) // the screen jumps: the things that are there now are not arriving
}

// move steps the choice of agent down or up the list of agents of the session.
func (m *model) move(d int) {
	ids := Agents(m.snap)
	if len(ids) == 0 {
		return
	}
	cur := m.current()
	i := 0
	for j, id := range ids {
		if id == cur {
			i = j
		}
	}
	m.agent = ids[(i+d+len(ids))%len(ids)]
}

// current is the agent the views are about: the one chosen, else the one that was answered last, else the first.
func (m *model) current() string {
	if m.agent != "" {
		return m.agent
	}
	if id := Newest(m.snap); id != "" {
		return id
	}
	if ids := Agents(m.snap); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// scene is what is on the screen now.
func (m *model) scene() Scene {
	mode := Mode{Replay: m.rep != nil, Paused: m.paused, QuitLive: m.c.QuitLive, QuitEnded: m.c.QuitEnded}
	if m.rep != nil {
		mode.Speed, mode.At, mode.Total = m.rep.Speed(), m.rep.Elapsed(), m.rep.Total()
	}
	return Scene{Snap: m.snap, View: m.view, Agent: m.current(), Frame: m.frame, Cols: m.cols, Rows: m.rows, Pal: m.c.Pal,
		Mem: m.mem, NoAnim: m.noAnim, Mode: mode}
}

// draw puts the screen on the terminal.
func (m *model) draw() error {
	lines := Draw(m.scene())
	if m.help {
		lines = Overlay(lines, helpBox(m.c.Pal, m.rep != nil), m.cols)
	}
	return m.c.Screen.DrawLines(lines)
}
