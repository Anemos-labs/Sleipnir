package app

import (
	"fmt"
	"time"

	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/svg"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

// UIFPS is the rate the animations are designed for: the cockpit's frame counter advances this often a second. A recording at a
// lower rate skips frames of it, so that a gait or a spinner moves at the pace it has in a terminal.
const UIFPS = 15

// RecordOptions say how a recorded session becomes a picture.
type RecordOptions struct {
	// Cols and Rows are the size of the screen (default 100 x 36).
	Cols, Rows int
	// FPS is the frames of the recording a second (default 5, at most UIFPS).
	FPS int
	// Speed is how many seconds of the session pass in one second of the recording (default 1). The demo's scripted team is over in
	// a second or two, so its recording is slowed down (0.25) to be seen.
	Speed float64
	// Until stops the recording after the last event whose seq is not above it (0 means the whole log).
	Until uint64
	// Hold is how long the last frame stays before the loop starts again (default 4 s).
	Hold time.Duration
	// Palette and Theme are the colours of the cockpit and of the terminal it is drawn on (the defaults of each when zero).
	Palette widget.Palette
	Theme   svg.Theme
	// NoAnim draws the standing horse and no arrival animation.
	NoAnim bool
	// View is the screen that is recorded (the cockpit when zero) and Agent the agent of the cache view.
	View  View
	Agent string
	// Follow makes the cache view follow the agent that matters as the session goes: the one that got the newest answer.
	Follow bool
}

func (o *RecordOptions) fill() {
	if o.Cols <= 0 {
		o.Cols = 100
	}
	if o.Rows <= 0 {
		o.Rows = 36
	}
	if o.FPS <= 0 {
		o.FPS = 5
	}
	o.FPS = min(o.FPS, UIFPS)
	if o.Speed <= 0 {
		o.Speed = 1
	}
	if o.Hold <= 0 {
		o.Hold = 4 * time.Second
	}
	if o.Palette == (widget.Palette{}) {
		o.Palette = widget.DefaultPalette()
	}
}

// Record plays the event log at path on a virtual clock and draws the view at every step into one animated SVG. The log, the
// options and the code decide every byte: no clock is read and nothing is random, so the recording can be made again and compared
// (scripts/record-demo.sh --check).
func Record(path string, o RecordOptions) (string, error) {
	o.fill()
	frames, err := Frames(path, o)
	if err != nil {
		return "", err
	}
	return svg.Animated(frames, o.Theme, svg.Options{Hold: o.Hold}), nil
}

// Frames are the frames of the recording: one per step of the virtual clock, each the screen the view draws then.
func Frames(path string, o RecordOptions) ([]svg.Frame, error) {
	o.fill()
	pl, err := state.Replay(path, state.ReplayOptions{Speed: o.Speed, Until: o.Until})
	if err != nil {
		return nil, err
	}
	defer pl.Close()
	step := time.Second / time.Duration(o.FPS)
	mem := NewMemory()
	var frames []svg.Frame
	for f := range pl.Frames(step) {
		ui := f.Index * UIFPS / o.FPS
		sn := pl.State().SnapshotAt(pl.Now())
		mem.Observe(sn, ui)
		agent := o.Agent
		if o.Follow && agent == "" {
			agent = Newest(sn)
		}
		lines := Draw(Scene{Snap: sn, View: o.View, Agent: agent, Frame: ui, Cols: o.Cols, Rows: o.Rows, Pal: o.Palette, Mem: mem, NoAnim: o.NoAnim,
			Mode: Mode{Replay: true, Speed: o.Speed, At: pl.Elapsed(), Total: 0}})
		fr := svg.FromLines(lines, o.Cols, time.Duration(f.Index)*step)
		frames = append(frames, fr)
	}
	if err := pl.Err(); err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("replay: %s holds no events", path)
	}
	return frames, nil
}
