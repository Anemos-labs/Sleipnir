package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/render"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// The rig of the chat program's tests. The program draws on the real inline renderer, which writes into the emulator of
// internal/tui/vt, so a test reads the screen a person would see (the scrollback and the live region), and every frame that the
// renderer wrote is checked on its way: it ends between escape sequences, it uses only the sequences the renderer composes, and it
// leaves synchronized output closed.
//
// Nothing in a test waits for a time to pass. The keys, the ticks and the size of the window are sent by the test; what the agent
// says is said by a scripted host on a goroutine the program starts; and the test waits for what it expects to be on the screen,
// frame after frame, with a hang guard that is a minute long.

// ---- the screen ----

// vtBridge is the writer the renderer writes to: it feeds the emulator and checks each frame.
type vtBridge struct {
	t   testing.TB
	mu  sync.Mutex
	v   *vt.Term
	raw []byte // everything that was written, as written
}

func (b *vtBridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.raw = append(b.raw, p...)
	b.v.Write(p)
	if !b.v.Idle() {
		b.t.Errorf("a frame ended inside an escape sequence: %q", p)
	}
	if b.v.SyncDepth() != 0 {
		b.t.Errorf("a frame left synchronized output open: %q", p)
	}
	if len(b.v.Rejected) != 0 {
		b.t.Errorf("the program emitted %q", b.v.Rejected)
		b.v.Rejected = nil
	}
	checkFrameVocabulary(b.t, p)
	return len(p), nil
}

// checkFrameVocabulary asserts that every escape sequence in a frame is one the renderer composes (colours, cursor moves, erase, the
// modes it switches) and that no other control byte is in it. Text that came from outside cannot have brought one in: the renderer
// and the widgets clean it, and this is what says so.
func checkFrameVocabulary(tb testing.TB, p []byte) {
	tb.Helper()
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == 0x1b:
			if i+1 >= len(p) || p[i+1] != '[' {
				tb.Errorf("an escape that does not start a CSI at byte %d of %q", i, p)
				return
			}
			j := i + 2
			for j < len(p) && p[j] >= 0x30 && p[j] <= 0x3f {
				j++
			}
			if j >= len(p) {
				tb.Errorf("an unterminated CSI in %q", p)
				return
			}
			params, final := string(p[i+2:j]), p[j]
			switch final {
			case 'A', 'B', 'C', 'D', 'G', 'H', 'J', 'K', 'm':
				if strings.Trim(params, "0123456789;") != "" {
					tb.Errorf("unexpected parameters %q for %c", params, final)
				}
			case 'h', 'l':
				switch params {
				case "?25", "?2026", "?2004":
				default:
					tb.Errorf("unexpected mode %q", params)
				}
			default:
				tb.Errorf("unexpected CSI final %q (params %q)", final, params)
			}
			i = j
		case c == '\r' || c == '\n':
		case c < 0x20 || c == 0x7f:
			tb.Errorf("control byte %#x in the output %q", c, p)
		case c == 0xc2 && i+1 < len(p) && p[i+1] >= 0x80 && p[i+1] < 0xa0:
			tb.Errorf("a C1 control in the output %q", p)
		}
	}
}

// testScreen is the renderer the program draws on, plus a count of the frames: Flush is called once after every event the program
// handles, which is the barrier the tests wait on.
type testScreen struct {
	*render.Inline
	frames chan struct{}
}

func (s *testScreen) Flush() error {
	err := s.Inline.Flush()
	s.frames <- struct{}{}
	return err
}

// ---- the session ----

// fakeHost is a session that does what a test scripts: a turn is a function that says what the agent does, through the sink and the
// prompter of the link, and ends when it returns.
type fakeHost struct {
	mu       sync.Mutex
	mode     string
	goals    []string
	commands []string
	turn     func(ctx context.Context, goal string) TurnResult
	command  func(ctx context.Context, line string, out io.Writer) CommandResult
}

func (h *fakeHost) Turn(ctx context.Context, goal string) TurnResult {
	h.mu.Lock()
	h.goals = append(h.goals, goal)
	f := h.turn
	h.mu.Unlock()
	if f == nil {
		return TurnResult{Steps: 1}
	}
	return f(ctx, goal)
}

func (h *fakeHost) Command(ctx context.Context, line string, out io.Writer) CommandResult {
	h.mu.Lock()
	h.commands = append(h.commands, line)
	f := h.command
	h.mu.Unlock()
	if f == nil {
		fmt.Fprintf(out, "ran %s\n", line)
		return CommandResult{}
	}
	return f(ctx, line, out)
}

func (h *fakeHost) Mode() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mode == "" {
		return "default"
	}
	return h.mode
}

func (h *fakeHost) SetMode(m string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mode = m
	return m
}

func (h *fakeHost) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.goals...)
}

// ---- the rig ----

type chatRig struct {
	t      *testing.T
	team   int // the size of the team the session is, 0 for a single agent
	bridge *vtBridge
	scr    *testScreen
	host   *fakeHost
	link   *ChatLink
	sink   *ChatSink
	prompt perm.Prompter

	keys   chan input.Key
	sizes  chan term.Size
	tickCh chan time.Time
	attach chan ChatAttach
	log    chan events.Event

	now    time.Time
	cancel context.CancelFunc
	done   chan chatExit

	cols, rows int
	cancelled  bool // CancelStart was called
	mu         sync.Mutex
	started    int
}

type chatExit struct {
	end ChatEnd
	err error
}

type rigOpts struct {
	cols, rows  int
	look        Look
	noAttach    bool // the session is not made yet: the test attaches it (or fails it) itself
	tickClock   bool // the program has no clock but the ticks the test sends
	answerAfter time.Duration
	verbose     bool
	mainAgent   string
	history     *input.History
	bell        func()
	restartTo   *[]string
	animAllowed bool
	team        int // the session is a team of this many agents, the manager included (0: a single agent)
}

func defaultLook() Look {
	return Look{Theme: widget.DefaultTheme(), Palette: widget.DefaultPalette(), Unicode: true, Anim: true}
}

// startChat runs the program on a terminal of the size asked for, with a session attached that is a fake host.
func startChat(t *testing.T, o rigOpts) *chatRig {
	t.Helper()
	if o.cols == 0 {
		o.cols = 80
	}
	if o.rows == 0 {
		o.rows = 24
	}
	if (o.look == Look{}) {
		o.look = defaultLook()
	}
	caps := term.Caps{Color: term.ColorTrueColor, Unicode: o.look.Unicode, Width: o.cols, Height: o.rows, BracketedPaste: true}
	if o.look.Theme.Mono {
		caps.Color = term.ColorANSI16 // NO_COLOR: the terminal can be drawn on, and the program asks it for no colour
	}
	r := &chatRig{t: t, host: &fakeHost{}, link: NewChatLink(), cols: o.cols, rows: o.rows,
		keys: make(chan input.Key), sizes: make(chan term.Size), tickCh: make(chan time.Time), attach: make(chan ChatAttach, 1),
		log: make(chan events.Event, 1024), done: make(chan chatExit, 1), now: time.Unix(1_700_000_000, 0)}
	r.team = o.team
	r.bridge = &vtBridge{t: t, v: vt.New(o.cols, o.rows)}
	r.scr = &testScreen{Inline: render.NewInline(r.bridge, caps, render.WithBracketedPaste()), frames: make(chan struct{}, 1<<16)}
	r.sink = r.link.Sink()
	r.prompt = r.link.Prompter()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	answerAfter := o.answerAfter
	if answerAfter == 0 {
		answerAfter = -1 // no pause: a test that wants one asks for it
	}
	var clock func() time.Time
	if !o.tickClock {
		clock = func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
	}
	cfg := ChatConfig{Screen: r.scr, Keys: r.keys, Sizes: r.sizes, Tick: r.tickCh, Attach: r.attach, Link: r.link, Look: o.look, Now: clock,
		MainAgent: o.mainAgent, Verbose: o.verbose, History: o.history, AnswerAfter: answerAfter, Bell: o.bell, RestartTo: o.restartTo, AnimAllowed: o.animAllowed,
		CancelStart: func() { r.mu.Lock(); r.cancelled = true; r.mu.Unlock() }}
	go func() {
		end, err := RunChat(ctx, cfg)
		r.done <- chatExit{end, err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(time.Minute):
			t.Error("the program did not end (a hang guard)")
		}
	})
	if !o.noAttach {
		r.attachSession()
	}
	if o.noAttach {
		r.shows("Starting")
	} else {
		mark := "◆"
		if !o.look.Unicode {
			mark = "<>"
		}
		r.shows(mark+" sleipnir", "default")
	}
	return r
}

func (r *chatRig) attachSession() {
	r.attach <- ChatAttach{Host: r.host, Events: r.log, Info: ChatInfo{Version: "0.1.0", Model: "mock/mock-1", Cwd: "/work/proj", SessionID: "20260102-030405-abcdef", Swarm: r.team > 1, Agents: r.team},
		Commands: []input.Command{{Name: "help", Description: "this text"}, {Name: "cost", Description: "tokens, cost and cache hit ratio so far"},
			{Name: "compact", Args: "[focus]", Description: "fold the older thread now"}, {Name: "exit", Description: "quit"}}}
}

// frame waits for the next frame the program draws.
func (r *chatRig) frame() {
	r.t.Helper()
	select {
	case <-r.scr.frames:
	case <-time.After(time.Minute):
		r.t.Fatalf("the program drew no frame (a hang guard); the screen:\n%s", r.screen())
	}
}

// screen is everything a person would see: the scrollback, then the screen, without the blank rows at the end.
func (r *chatRig) screen() string {
	r.bridge.mu.Lock()
	defer r.bridge.mu.Unlock()
	rows := r.bridge.v.All()
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\n")
}

// written is every byte the program has written to the terminal.
func (r *chatRig) written() []byte {
	r.bridge.mu.Lock()
	defer r.bridge.mu.Unlock()
	return append([]byte(nil), r.bridge.raw...)
}

// live is the screen only, as the terminal shows it now.
func (r *chatRig) visible() string {
	r.bridge.mu.Lock()
	defer r.bridge.mu.Unlock()
	return r.bridge.v.String()
}

// until waits for the screen to satisfy ok. It looks at each frame the program draws, and also every few milliseconds: a condition that is
// not about the screen (the session was given the goal, which another goroutine does after the frame that shows the goal was drawn) is
// not woken by a frame that never comes, and on Windows, where that goroutine ran later, such a test waited out its hang guard.
func (r *chatRig) until(what string, ok func(screen string) bool) string {
	r.t.Helper()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	guard := time.NewTimer(time.Minute)
	defer guard.Stop()
	for {
		if s := r.screen(); ok(s) {
			return s
		}
		select {
		case <-r.scr.frames:
		case <-poll.C:
		case e := <-r.done:
			r.done <- e
			r.t.Fatalf("the program ended (%v) before the screen showed %s:\n%s", e, what, r.screen())
		case <-guard.C:
			r.t.Fatalf("the screen never showed %s (a hang guard); it shows:\n%s", what, r.screen())
		}
	}
}

// shows waits for every one of the words to be on the screen.
func (r *chatRig) shows(words ...string) string {
	r.t.Helper()
	return r.until(fmt.Sprintf("%q", words), func(s string) bool {
		for _, w := range words {
			if !strings.Contains(s, w) {
				return false
			}
		}
		return true
	})
}

// ping is a key that does nothing: an empty paste, which the decoder never makes. The program handles one event at a time and
// draws after each, so when a second key has been taken, the first has been handled and drawn: sending the ping after a key is the
// barrier that says its effect is on the screen, without counting frames (which an event of the session that arrives meanwhile
// would put out of step).
var ping = input.PasteKey("")

// press sends a key, and returns when the program has handled it and drawn what it did.
func (r *chatRig) press(k input.Key) {
	r.t.Helper()
	r.send(k)
	r.send(ping)
}

func (r *chatRig) send(k input.Key) {
	r.t.Helper()
	select {
	case r.keys <- k:
	case e := <-r.done:
		r.done <- e
		r.t.Fatalf("the program ended (%+v) before it took %v; the screen:\n%s", e, k, r.screen())
	case <-time.After(time.Minute):
		r.t.Fatalf("the program did not take %v (a hang guard); the screen:\n%s", k, r.screen())
	}
}

func (r *chatRig) typeText(s string) {
	r.t.Helper()
	for _, c := range s {
		r.press(input.RuneKey(c, 0))
	}
}

func (r *chatRig) enter()                  { r.t.Helper(); r.press(input.SpecialKey(input.Enter, 0)) }
func (r *chatRig) ctrl(c rune)             { r.t.Helper(); r.press(input.RuneKey(c, input.Ctrl)) }
func (r *chatRig) special(c input.Special) { r.t.Helper(); r.press(input.SpecialKey(c, 0)) }

// submit types a line and presses enter.
func (r *chatRig) submit(line string) {
	r.t.Helper()
	r.typeText(line)
	r.enter()
}

// step moves the clock on by dt, sends the tick, and returns when the program has drawn it.
func (r *chatRig) step(dt time.Duration) {
	r.t.Helper()
	r.mu.Lock()
	r.now = r.now.Add(dt)
	now := r.now
	r.mu.Unlock()
	select {
	case r.tickCh <- now:
	case <-time.After(time.Minute):
		r.t.Fatalf("the program did not take the tick (a hang guard); the screen:\n%s", r.screen())
	}
	r.send(ping)
}

// resize changes the size of the window: the terminal is the new size first, then the program is told, as a terminal does it.
func (r *chatRig) resize(cols, rows int) {
	r.t.Helper()
	r.bridge.mu.Lock()
	r.bridge.v.Resize(cols, rows)
	r.bridge.mu.Unlock()
	r.cols, r.rows = cols, rows
	select {
	case r.sizes <- term.Size{Width: cols, Height: rows}:
	case <-time.After(time.Minute):
		r.t.Fatalf("the program did not take the new size (a hang guard); the screen:\n%s", r.screen())
	}
	r.send(ping)
}

// at sets the clock of the program.
func (r *chatRig) at(t time.Time) {
	r.mu.Lock()
	r.now = t
	r.mu.Unlock()
}

// emit adds events to the session's log, and returns when the program has taken them and drawn what they changed.
func (r *chatRig) emit(es ...events.Event) {
	r.t.Helper()
	for _, e := range es {
		select {
		case r.log <- e:
		default:
			r.t.Fatal("the log's channel is full")
		}
	}
	deadline := time.Now().Add(time.Minute)
	for len(r.log) > 0 { // the program takes them one event at a time, and draws after the last
		if time.Now().After(deadline) {
			r.t.Fatalf("the program did not take the events (a hang guard); the screen:\n%s", r.screen())
		}
		runtime.Gosched()
	}
	r.send(ping)
}

// wait returns how the program ended.
func (r *chatRig) wait() chatExit {
	r.t.Helper()
	select {
	case e := <-r.done:
		r.done <- e // the cleanup of the test waits for it too
		return e
	case <-time.After(time.Minute):
		r.t.Fatalf("the program did not end (a hang guard); the screen:\n%s", r.screen())
		return chatExit{}
	}
}

// ---- what the agent does ----

// toolCall is a call as the agent makes it.
func toolCall(id, name string, in map[string]any) core.Block {
	return core.Block{ToolID: id, ToolName: name, Input: mustJSON(in)}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// okResult is what a tool says when it did what it was asked.
func okResult(text string, meta map[string]any) *tools.Result {
	return &tools.Result{Text: text, Meta: meta}
}

// cell helpers for assertions on styles

func anyStyled(lines []cell.Line, pred func(cell.Span) bool) bool {
	for _, l := range lines {
		for _, sp := range l {
			if pred(sp) {
				return true
			}
		}
	}
	return false
}
