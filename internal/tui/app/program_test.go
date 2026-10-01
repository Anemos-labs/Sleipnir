package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// fakeScreen keeps every frame the program draws, so that a test can read the screen; Run draws once for the first frame and once
// for every event after it, which is the barrier the tests wait on.
type fakeScreen struct {
	cols, rows    int
	frames        chan string
	entered, left int
	resizes       [][2]int
	err           error
}

func newFakeScreen(cols, rows int) *fakeScreen {
	return &fakeScreen{cols: cols, rows: rows, frames: make(chan string, 4096)}
}

func (f *fakeScreen) Enter() error     { f.entered++; return nil }
func (f *fakeScreen) Leave() error     { f.left++; return nil }
func (f *fakeScreen) Size() (int, int) { return f.cols, f.rows }
func (f *fakeScreen) Resize(cols, rows int) {
	f.cols, f.rows = cols, rows
	f.resizes = append(f.resizes, [2]int{cols, rows})
}
func (f *fakeScreen) DrawLines(l []cell.Line) error {
	if f.err != nil {
		return f.err
	}
	f.frames <- plainText(l)
	return nil
}

// rig runs the program in a goroutine and hands the test the channels that drive it.
type rig struct {
	t      *testing.T
	scr    *fakeScreen
	keys   chan input.Key
	sizes  chan term.Size
	tick   chan time.Time
	done   chan error
	cancel context.CancelFunc
	now    time.Time
}

func startRig(t *testing.T, src Source, view View, cols, rows int) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{t: t, scr: newFakeScreen(cols, rows), keys: make(chan input.Key), sizes: make(chan term.Size), tick: make(chan time.Time),
		done: make(chan error, 1), cancel: cancel, now: time.Unix(1_700_000_000, 0)}
	go func() {
		r.done <- Run(ctx, Config{Src: src, Screen: r.scr, Keys: r.keys, Sizes: r.sizes, Tick: r.tick, Pal: widget.DefaultPalette(), View: view})
	}()
	t.Cleanup(func() { cancel(); src.Close() })
	return r
}

// frame waits for the next frame the program draws.
func (r *rig) frame() string {
	r.t.Helper()
	select {
	case f := <-r.scr.frames:
		return f
	case <-time.After(time.Minute):
		r.t.Fatal("the program drew no frame (a hang guard)")
		return ""
	}
}

func (r *rig) press(k input.Key) string {
	r.t.Helper()
	r.keys <- k
	return r.frame()
}

func (r *rig) rune(c rune) string { return r.press(input.RuneKey(c, 0)) }

// step sends one tick of dt and returns the frame it drew.
func (r *rig) step(dt time.Duration) string {
	r.t.Helper()
	r.now = r.now.Add(dt)
	r.tick <- r.now
	return r.frame()
}

func (r *rig) wait() error {
	r.t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(time.Minute):
		r.t.Fatal("the program did not end (a hang guard)")
		return nil
	}
}

func demoReplay(t *testing.T, speed float64) *ReplaySource {
	t.Helper()
	src, err := OpenReplay(statetest.DemoLogFile(t), speed, 0)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestRunDrawsTheCockpitAndQuitsOnQ(t *testing.T) {
	r := startRig(t, demoReplay(t, 1), ViewCockpit, 100, 36)
	first := r.frame()
	if !strings.Contains(first, "SLEIPNIR") || !strings.Contains(first, "q quit") {
		t.Fatalf("the first frame is not the cockpit with its keys:\n%s", first)
	}
	if n := strings.Count(first, "\n"); n != 36 {
		t.Errorf("the frame has %d lines, the screen has 36", n)
	}
	r.keys <- input.RuneKey('q', 0)
	if err := r.wait(); err != nil {
		t.Fatalf("quitting is not an error: %v", err)
	}
	if r.scr.entered != 1 || r.scr.left != 1 {
		t.Errorf("the screen was entered %d times and left %d: each once", r.scr.entered, r.scr.left)
	}
}

func TestEveryWayOutLeavesTheScreen(t *testing.T) {
	for name, end := range map[string]func(*rig){
		"ctrl-c":   func(r *rig) { r.keys <- input.RuneKey('c', input.Ctrl) },
		"keys end": func(r *rig) { close(r.keys) },
		"context":  func(r *rig) { r.cancel() },
	} {
		t.Run(name, func(t *testing.T) {
			r := startRig(t, demoReplay(t, 1), ViewCockpit, 100, 36)
			r.frame()
			end(r)
			err := r.wait()
			if name == "context" && !errors.Is(err, context.Canceled) {
				t.Errorf("a cancelled context ends the program with its error, got %v", err)
			}
			if name != "context" && err != nil {
				t.Errorf("%s is a way to quit, not an error: %v", name, err)
			}
			if r.scr.left != 1 {
				t.Errorf("the screen was left %d times", r.scr.left)
			}
		})
	}
}

func TestKeysChooseTheView(t *testing.T) {
	r := startRig(t, demoReplay(t, 1), ViewCockpit, 100, 36)
	r.frame()
	r.step(0)
	for _, k := range []struct {
		key  rune
		want string
	}{{'c', "cache ·"}, {'m', "mail"}, {'b', "board"}, {'o', "riders"}} {
		if f := r.rune(k.key); !strings.Contains(f, k.want) {
			t.Errorf("after %q the screen lacks %q:\n%s", k.key, k.want, f)
		}
	}
	f := r.press(input.SpecialKey(input.Tab, 0))
	if !strings.Contains(f, "cache ·") {
		t.Errorf("tab goes to the next view (the cache):\n%s", f)
	}
	f = r.press(input.SpecialKey(input.Tab, input.Shift))
	if !strings.Contains(f, "riders") {
		t.Errorf("shift-tab goes back to the cockpit:\n%s", f)
	}
	f = r.rune('3')
	if !strings.Contains(f, "mail") {
		t.Errorf("3 is the mail:\n%s", f)
	}
	f = r.press(input.SpecialKey(input.Esc, 0))
	if !strings.Contains(f, "riders") {
		t.Errorf("Esc is the way back to the cockpit:\n%s", f)
	}
	f = r.press(input.SpecialKey(input.Esc, 0))
	select {
	case err := <-r.done:
		t.Fatalf("Esc on the cockpit must not quit the program (%v):\n%s", err, f)
	default:
	}
}

func TestArrowsChooseTheAgentOfTheCacheView(t *testing.T) {
	r := startRig(t, demoReplay(t, 64), ViewCache, 100, 36)
	r.frame()
	for i := 0; i < 40; i++ { // a recording of a second, at 64 times its speed, is over in a few ticks
		r.step(250 * time.Millisecond)
	}
	f := r.press(input.SpecialKey(input.End, 0))
	if !strings.Contains(f, "cache ·") {
		t.Fatalf("the cache view is not on the screen:\n%s", f)
	}
	first := headerOf(f)
	f = r.press(input.SpecialKey(input.Down, 0))
	second := headerOf(f)
	if first == second {
		t.Errorf("Down must choose another agent: %q then %q", first, second)
	}
	f = r.press(input.SpecialKey(input.Up, 0))
	if headerOf(f) != first {
		t.Errorf("Up must choose the first agent again: %q then %q", first, headerOf(f))
	}
	if !strings.Contains(f, "›") {
		t.Errorf("the agent the view is about is marked › in the table:\n%s", f)
	}
}

// headerOf is the title line of a screen up to the stats.
func headerOf(f string) string {
	line, _, _ := strings.Cut(f, "\n")
	if i := strings.Index(line, "  "); i > 0 {
		line = line[:i]
	}
	return line
}

func TestAReplayPlaysOnTicksAndPauseHoldsTheScreen(t *testing.T) {
	r := startRig(t, demoReplay(t, 1), ViewCockpit, 100, 36)
	start := r.frame()
	if !strings.Contains(start, "▶ replay") {
		t.Errorf("a replay says so in the title bar:\n%s", start)
	}
	r.step(0)
	var last string
	for i := 0; i < 30; i++ {
		last = r.step(100 * time.Millisecond)
	}
	if last == start {
		t.Fatal("the screen did not change while the recording played")
	}
	held := r.rune(' ')
	if !strings.Contains(held, "⏸ paused") {
		t.Errorf("a paused screen says so:\n%s", held)
	}
	for i := 0; i < 5; i++ {
		if f := r.step(time.Second); f != held {
			t.Fatalf("a paused screen must not change (tick %d):\n%s", i, f)
		}
	}
	back := r.rune('p')
	if strings.Contains(back, "paused") {
		t.Errorf("p goes on:\n%s", back)
	}
}

func TestReplayKeysMoveTheClock(t *testing.T) {
	src := demoReplay(t, 1)
	r := startRig(t, src, ViewCockpit, 100, 36)
	r.frame()
	r.step(0)
	r.press(input.SpecialKey(input.End, 0))
	if !src.Done() {
		t.Fatal("End must play the recording to its end")
	}
	r.press(input.SpecialKey(input.Home, 0))
	if src.Elapsed() != 0 {
		t.Errorf("Home is the start, the clock is at %v", src.Elapsed())
	}
	sn := src.State().Snapshot()
	if len(sn.Agents) > 1 {
		t.Errorf("at the start of the recording %d agents exist", len(sn.Agents))
	}
	speed := src.Speed()
	r.rune('+')
	if src.Speed() <= speed {
		t.Errorf("+ must make it faster: %v then %v", speed, src.Speed())
	}
	r.rune('-')
	r.rune('-')
	if src.Speed() >= speed {
		t.Errorf("- must make it slower: %v then %v", speed, src.Speed())
	}
}

func TestHelpOverlayListsTheKeysAndAnyKeyClosesIt(t *testing.T) {
	r := startRig(t, demoReplay(t, 1), ViewCockpit, 100, 36)
	r.frame()
	f := r.rune('?')
	for _, want := range []string{"keys", "pause", "quit", "seek"} {
		if !strings.Contains(f, want) {
			t.Errorf("the help lacks %q:\n%s", want, f)
		}
	}
	if n := strings.Count(f, "\n"); n != 36 {
		t.Errorf("the help must not change the size of the screen: %d lines", n)
	}
	f = r.rune('x')
	if strings.Contains(f, "┤ keys") || strings.Contains(f, "╭─ keys") {
		t.Errorf("a key closes the help:\n%s", f)
	}
	r.rune('?')
	r.keys <- input.RuneKey('q', 0)
	if err := r.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestResizeLaysTheScreenOutAgain(t *testing.T) {
	r := startRig(t, demoReplay(t, 1), ViewCockpit, 100, 36)
	r.frame()
	r.sizes <- term.Size{Width: 70, Height: 20}
	f := r.frame()
	if len(r.scr.resizes) != 1 || r.scr.resizes[0] != [2]int{70, 20} {
		t.Fatalf("the screen was told %v", r.scr.resizes)
	}
	lines := strings.Split(strings.TrimSuffix(f, "\n"), "\n")
	if len(lines) != 20 {
		t.Errorf("the frame has %d lines, the terminal 20", len(lines))
	}
	for _, l := range lines {
		if w := cell.StringWidth(l); w > 70 {
			t.Errorf("a line is %d cells wide on a screen of 70: %q", w, l)
		}
	}
}

func TestALongStretchOfTimeDoesNotPlayAWholeRecordingAtOnce(t *testing.T) {
	src := demoReplay(t, 1)
	r := startRig(t, src, ViewCockpit, 100, 36)
	r.frame()
	r.step(0)
	r.step(time.Hour) // a process that was suspended: the first tick after it is an hour late
	if src.Elapsed() > time.Second {
		t.Errorf("one tick moved the recording by %v: a tick moves it by a fraction of a second at most", src.Elapsed())
	}
}

func TestALiveSessionIsFollowedAsItIsWritten(t *testing.T) {
	log, err := os.ReadFile(statetest.DemoLogFile(t))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(log), "\n")
	half := len(lines) / 2
	path := t.TempDir() + "/events.jsonl"
	if err := os.WriteFile(path, []byte(strings.Join(lines[:half], "")), 0o600); err != nil {
		t.Fatal(err)
	}
	poll := make(chan time.Time)
	polled := make(chan state.PollInfo, 16)
	clock := time.Unix(1_700_000_000, 0)
	src := OpenLive(context.Background(), path, LiveOptions{Tick: poll, OnPoll: func(p state.PollInfo) { polled <- p }, Clock: func() time.Time { return clock }})
	<-polled // the first poll, at the start
	r := startRig(t, src, ViewCockpit, 100, 36)
	part := r.frame()
	if !strings.Contains(part, "● live") {
		t.Errorf("a live view says so:\n%s", part)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines[half:], "")); err != nil {
		t.Fatal(err)
	}
	poll <- time.Now()
	<-polled
	full := r.step(66 * time.Millisecond)
	if full == part {
		t.Fatalf("the screen did not follow the log:\n%s", full)
	}
	if strings.Contains(part, "■ ended") || !strings.Contains(full, "■ ended") {
		t.Errorf("the session ends with the last event, not before it:\n%s\n%s", part, full)
	}
}

func TestAFailingScreenEndsTheProgram(t *testing.T) {
	r := startRig(t, demoReplay(t, 1), ViewCockpit, 100, 36)
	r.frame()
	r.scr.err = errors.New("the terminal is gone")
	r.keys <- input.RuneKey('m', 0)
	err := r.wait()
	if err == nil || !strings.Contains(err.Error(), "terminal is gone") {
		t.Fatalf("a screen that cannot be written ends the program with its error, got %v", err)
	}
	if r.scr.left != 1 {
		t.Errorf("the screen must be left even then")
	}
}

func TestOverlayKeepsTheSizeOfTheScreen(t *testing.T) {
	base := make([]cell.Line, 10)
	for i := range base {
		base[i] = cell.Text(strings.Repeat("·", 30))
	}
	box := []cell.Line{cell.Text("┌────┐"), cell.Text("│ 世 │"), cell.Text("└────┘")}
	out := Overlay(base, box, 30)
	if len(out) != 10 {
		t.Fatalf("%d lines", len(out))
	}
	for i, l := range out {
		if w := l.Width(); w != 30 {
			t.Errorf("line %d is %d cells wide, want 30: %q", i, w, l.Plain())
		}
	}
	if !strings.Contains(plainText(out), "│ 世 │") {
		t.Errorf("the box is not on the screen:\n%s", plainText(out))
	}
	// a box over a wide character cuts the wide character cleanly
	wide := []cell.Line{cell.Text(strings.Repeat("世", 15))}
	out = Overlay(wide, []cell.Line{cell.Text("[]")}, 30)
	if w := out[0].Width(); w != 30 {
		t.Errorf("a box over wide characters made the line %d cells wide: %q", w, out[0].Plain())
	}
}

func TestSliceLineCutsByColumns(t *testing.T) {
	l := cell.Join(cell.Styled(cell.Style{Attr: cell.Bold}, "ab世cd"), cell.Text("ef"))
	for from := 0; from <= 8; from++ {
		for to := from; to <= 8; to++ {
			got := sliceLine(l, from, to)
			if w := got.Width(); w != to-from {
				t.Errorf("slice %d:%d is %d cells wide (%q)", from, to, w, got.Plain())
			}
		}
	}
	if got := sliceLine(l, 2, 4).Plain(); got != "世" {
		t.Errorf("the wide character whole: %q", got)
	}
	if got := sliceLine(l, 3, 5).Plain(); got != " c" {
		t.Errorf("half a wide character is a space: %q", got)
	}
}
