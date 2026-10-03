package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/render"
	"github.com/anemos-labs/sleipnir/internal/tui/svg"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
)

// Playing a chat transcript (chat_transcript.go) is running the chat program, the real one, against a terminal emulator and a clock
// that nothing but the player moves. The player is the only thing that talks to the program: it hands it a key, a piece of the
// answer, an event of the log or a tick of the clock, and waits for the program to have handled it and drawn the screen, which it
// says by flushing the renderer once (the program draws after every event it handles, and that is what the tests of the program
// wait on too). Only one thing is ever pending, so the program never has two inputs to choose between and the order in which it
// handles them is the transcript's, and the screens are the same on every run and every machine. Nothing in a playback waits for a
// time to pass: the virtual clock is a number the player sets, and the program is ticked at the rate it ticks at on a terminal
// (UIFPS) for as long as the transcript lasts.
//
// A frame of the picture is the emulator's screen at a tick: what a terminal of that size would show, the scrollback that has run off
// the top left out and the live region in place.

// ChatPlayOptions say how a chat transcript becomes a picture.
type ChatPlayOptions struct {
	// Cols and Rows are the size of the terminal (default 100 x 38).
	Cols, Rows int
	// FPS is the frames of the picture a second (default 5, at most UIFPS). The program is ticked UIFPS times a second whatever this
	// is, so that its animations run at the pace they have on a terminal, and the picture takes the screen of the first tick of each
	// 1/FPS of a second (with an FPS that UIFPS does not divide, the frames are not evenly apart; each has its own time).
	FPS int
	// From and Length choose the stretch of the session that is in the picture, in the time of the session: the playback always starts
	// at the beginning (the state at From is what the session had come to), the frames only from From, to From+Length (zero: to the
	// end of the transcript and a moment more, Tail).
	From, Length time.Duration
	// Hold is how long the last frame stays before the loop starts again (default 4 s).
	Hold time.Duration
	// Theme is the colours of the terminal the screen is drawn on (the default one when zero).
	Theme svg.Theme
	// NoAnim plays the program with its animations off.
	NoAnim bool
	// Shell is the line the terminal shows above the program, as the shell left it: the command that started it (default
	// "$ sleipnir chat"; "-" for none).
	Shell string
	// Tail is how long the playback goes on after the last record, so that what that record started can finish (default 1.5 s).
	Tail time.Duration
	// Guard is how long the player waits for the program to answer before it gives up: a hang guard, never a timing (default 2 minutes).
	Guard time.Duration
}

func (o *ChatPlayOptions) fill() {
	if o.Cols <= 0 {
		o.Cols = 100
	}
	if o.Rows <= 0 {
		o.Rows = 38
	}
	if o.FPS <= 0 {
		o.FPS = 5
	}
	o.FPS = min(o.FPS, UIFPS)
	if o.Hold <= 0 {
		o.Hold = 4 * time.Second
	}
	if o.Shell == "" {
		o.Shell = "$ sleipnir chat"
	}
	if o.Tail <= 0 {
		o.Tail = 1500 * time.Millisecond
	}
	if o.Guard <= 0 {
		o.Guard = 2 * time.Minute
	}
}

// recordChat plays the transcript at path and draws the screens into one animated SVG (scripts/record-demo.sh --check compares it with
// the committed one, so nothing here may read a clock or a random number).
func recordChat(path string, o ChatPlayOptions) (string, error) {
	tr, err := loadChatTranscript(path)
	if err != nil {
		return "", err
	}
	frames, err := PlayChat(tr, o)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	o.fill()
	return svg.Animated(frames, o.Theme, svg.Options{Hold: o.Hold}), nil
}

// PlayChat runs the chat program through the transcript and returns the screens it drew, one for every 1/FPS of the session.
func PlayChat(tr *ChatTranscript, o ChatPlayOptions) ([]svg.Frame, error) {
	o.fill()
	p := newChatPlayer(tr, o)
	defer p.close()
	return p.play()
}

// ---- the pieces the program is given ----

// playBridge is where the renderer writes: the emulator.
type playBridge struct {
	mu sync.Mutex
	t  *vt.Term
}

func (b *playBridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.t.Write(p)
}

// playScreen is the renderer the program draws on, and a count of the frames: the program flushes once after every input it handles.
type playScreen struct {
	*render.Inline
	frames chan struct{}
}

func (s *playScreen) Flush() error {
	err := s.Inline.Flush()
	s.frames <- struct{}{}
	return err
}

// playHost is the session the program is in front of: it does nothing of its own. A turn lasts until the player says it is over (the
// transcript says when), whatever the program does with its context, so that the end of a turn is one more input that the player
// hands over at its time and not a race between the program and a goroutine.
type playHost struct {
	mu      sync.Mutex
	ctx     context.Context
	mode    string
	goals   chan string
	release chan TurnResult
	quit    chan struct{}
}

func newPlayHost(mode string) *playHost {
	if mode == "" {
		mode = "default"
	}
	return &playHost{mode: mode, goals: make(chan string, 64), release: make(chan TurnResult, 1), quit: make(chan struct{})}
}

func (h *playHost) Turn(ctx context.Context, goal string) TurnResult {
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
	h.goals <- goal
	select {
	case r := <-h.release:
		return r
	case <-h.quit:
		return TurnResult{Err: context.Canceled}
	}
}

func (h *playHost) Command(context.Context, string, io.Writer) CommandResult { return CommandResult{} }

func (h *playHost) Mode() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mode
}

func (h *playHost) SetMode(m string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mode = m
	return m
}

// turnContext is the context of the turn that runs (nil before the first).
func (h *playHost) turnContext() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ctx
}

// ---- the player ----

type chatPlayer struct {
	tr  *ChatTranscript
	o   ChatPlayOptions
	vt  *vt.Term
	br  *playBridge
	scr *playScreen

	link *ChatLink
	sink *ChatSink
	host *playHost

	keys   chan input.Key
	tick   chan time.Time
	attach chan ChatAttach
	log    chan events.Event
	done   chan playExit

	nowNS  atomic.Int64 // the virtual clock, in nanoseconds after the epoch
	cancel context.CancelFunc
	asking *playAsk // the question that is on the screen and has not been answered
}

// playAsk is a question the player has put to the program, and what the transcript says the keys answer to it.
type playAsk struct {
	q    *question
	want ChatAsk
}

func newChatPlayer(tr *ChatTranscript, o ChatPlayOptions) *chatPlayer {
	p := &chatPlayer{tr: tr, o: o, link: NewChatLink(), keys: make(chan input.Key), tick: make(chan time.Time), attach: make(chan ChatAttach, 1),
		log: make(chan events.Event), done: make(chan playExit, 1)}
	p.sink = p.link.Sink()
	p.vt = vt.New(o.Cols, o.Rows)
	p.br = &playBridge{t: p.vt}
	caps := term.Caps{Color: term.ColorTrueColor, Unicode: true, Anim: true, Width: o.Cols, Height: o.Rows, BracketedPaste: true}
	p.scr = &playScreen{Inline: render.NewInline(p.br, caps, render.WithBracketedPaste(), render.WithBottomAnchor(), render.WithStartRow(playStartRow(o))), frames: make(chan struct{}, 1<<16)}
	return p
}

// playExit is how the program ended.
type playExit struct {
	end ChatEnd
	err error
}

func (e playExit) String() string { return fmt.Sprintf("%s, %v", e.end, e.err) }

// clock is the virtual time: the epoch and what has passed since.
func (p *chatPlayer) clock() time.Time {
	return p.tr.Epoch.Add(time.Duration(p.nowNS.Load()))
}

func (p *chatPlayer) setClock(d time.Duration) { p.nowNS.Store(int64(d)) }

func (p *chatPlayer) close() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.host != nil {
		close(p.host.quit)
	}
	select {
	case <-p.done:
	case <-time.After(p.o.Guard):
	}
}

// await waits for the program to have drawn once.
func (p *chatPlayer) await(what string) error {
	select {
	case <-p.scr.frames:
		return nil
	case e := <-p.done:
		p.done <- e
		return fmt.Errorf("the program ended (%s) before it drew what %s made it draw", e, what)
	case <-time.After(p.o.Guard):
		return fmt.Errorf("the program drew nothing for %v after %s (a hang guard)", p.o.Guard, what)
	}
}

// send gives the program one input on a channel it reads and waits for the frame.
func send[T any](p *chatPlayer, ch chan T, v T, what string) error {
	select {
	case ch <- v:
	case e := <-p.done:
		p.done <- e
		return fmt.Errorf("the program ended (%s) before it took %s", e, what)
	case <-time.After(p.o.Guard):
		return fmt.Errorf("the program did not take %s within %v (a hang guard)", what, p.o.Guard)
	}
	return p.await(what)
}

func (p *chatPlayer) play() ([]svg.Frame, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	if p.o.Shell != "-" {
		line := p.o.Shell
		if rest, ok := strings.CutPrefix(line, "$ "); ok {
			line = "\x1b[90m$\x1b[0m " + rest // the prompt is dim, as a shell's usually is
		}
		_, _ = p.br.Write([]byte(line + "\r\n"))
	}
	look := LookFor(term.Caps{Color: term.ColorTrueColor, Unicode: true, Anim: true}, p.o.NoAnim)
	cfg := ChatConfig{Screen: p.scr, Keys: p.keys, Tick: p.tick, Attach: p.attach, Link: p.link, Now: p.clock, Look: look, MainAgent: "main"}
	go func() {
		end, err := RunChat(ctx, cfg)
		p.done <- playExit{end, err}
	}()
	if err := p.await("starting"); err != nil { // the first screen: the program draws before it reads anything
		return nil, err
	}

	period := time.Second / UIFPS

	last := time.Duration(0)
	if n := len(p.tr.Records); n > 0 {
		last = time.Duration(p.tr.Records[n-1].T) * time.Millisecond
	}
	end := last + p.o.Tail
	if p.o.Length > 0 {
		end = p.o.From + p.o.Length
	}
	var frames []svg.Frame
	next := 0 // the next record to hand over
	for i := 0; ; i++ {
		at := time.Duration(i) * period
		if at > end {
			break
		}
		for next < len(p.tr.Records) && time.Duration(p.tr.Records[next].T)*time.Millisecond <= at {
			rec := p.tr.Records[next]
			next++
			p.setClock(time.Duration(rec.T) * time.Millisecond)
			if err := p.deliver(rec); err != nil {
				return nil, fmt.Errorf("at %d ms, %s: %w", rec.T, rec.Kind, err)
			}
		}
		p.setClock(at)
		if err := send(p, p.tick, p.clock(), "a tick"); err != nil {
			return nil, err
		}
		if (i == 0 || i*p.o.FPS/UIFPS != (i-1)*p.o.FPS/UIFPS) && at >= p.o.From { // the ticks that start a new 1/FPS
			p.br.mu.Lock()
			frames = append(frames, svg.Capture(p.vt, max(at-p.o.From, 0)))
			p.br.mu.Unlock()
			if len(frames) == 1 {
				frames[0].At = 0 // the picture starts with its first screen, not a blank one
			}
		}
	}
	if next < len(p.tr.Records) && p.o.Length == 0 {
		return nil, fmt.Errorf("%d records were not played", len(p.tr.Records)-next)
	}
	if p.asking != nil && p.o.Length == 0 {
		return nil, errors.New("a question was put and no key answered it")
	}
	p.br.mu.Lock()
	rejected := p.vt.Rejected
	p.br.mu.Unlock()
	if len(rejected) > 0 {
		return nil, fmt.Errorf("the program emitted %q", rejected)
	}
	if len(frames) == 0 {
		return nil, errors.New("no frames: the transcript ends before the picture starts")
	}
	return frames, nil
}

// deliver hands one record to the program, and returns when the program has drawn what it made it draw.
func (p *chatPlayer) deliver(r ChatRecord) error {
	switch r.Kind {
	case RecAttach:
		a := r.Attach
		p.host = newPlayHost(a.Mode)
		return send(p, p.attach, ChatAttach{Host: p.host, Events: p.log,
			Info: ChatInfo{Version: a.Version, Model: a.Model, Cwd: a.Cwd, SessionID: a.Session}}, "the session")
	case RecKey:
		k, err := parseKey(r.Key)
		if err != nil {
			return err
		}
		if err := send(p, p.keys, k, "the key "+r.Key); err != nil {
			return err
		}
		return p.checkAnswer(r.Key)
	case RecTurn:
		if p.host == nil {
			return errors.New("a goal before the session")
		}
		select {
		case g := <-p.host.goals:
			if g != r.Text {
				return fmt.Errorf("the program sent the goal %q, the transcript says %q", g, r.Text)
			}
		case <-time.After(p.o.Guard):
			return fmt.Errorf("the program sent no goal (the transcript says %q)", r.Text)
		}
		return nil
	case RecEvent:
		e := *r.Event
		e.TS = p.clock()
		return send(p, p.log, e, "an event")
	case RecText:
		p.sink.Text(r.Agent, r.Text)
	case RecReset:
		p.sink.Reset(r.Agent)
	case RecToolStart:
		p.sink.ToolStart(r.Agent, r.Call.block())
	case RecToolEnd:
		p.sink.ToolEnd(r.Agent, r.Call.block(), r.Result.result(), time.Duration(r.TookMS)*time.Millisecond)
	case RecResponse:
		p.sink.Response(r.Agent, nil, r.Hit)
	case RecNotice:
		p.sink.Notice(r.Agent, r.Level, r.Text)
	case RecAsk:
		// What the session's prompter does (ChatLink.Prompter): put the question to the program, and have the answer come back on a
		// channel of its own. The player does not wait on that channel in a goroutine: the program has answered when the key that
		// answers it has been handled, and whether it has is looked at then (checkAnswer), at a moment that does not depend on a
		// scheduler.
		if p.asking != nil {
			return errors.New("a question while another waits")
		}
		q := &question{req: r.Ask.Request, ans: make(chan perm.Decision, 1)}
		p.asking = &playAsk{q: q, want: *r.Ask}
		p.link.send(context.Background(), chatMsg{kind: mQuestion, q: q})
	case RecTurnEnd:
		return p.endTurn(r.End)
	default:
		return fmt.Errorf("a record of kind %q cannot be played", r.Kind)
	}
	return p.awaitLink(r.Kind)
}

// awaitLink waits for the program to have taken what was put into the link's channel, and to have drawn it. The link is buffered, so
// the program takes the message at a moment of its own: the player waits until it has, and then until the program has come back to
// read its next input, which a no-draw channel cannot say; the frame it draws after the message is that.
func (p *chatPlayer) awaitLink(what string) error {
	deadline := time.Now().Add(p.o.Guard)
	for len(p.link.msgs) > 0 {
		if time.Now().After(deadline) {
			return fmt.Errorf("the program did not take the %s within %v (a hang guard)", what, p.o.Guard)
		}
		runtime.Gosched()
	}
	return p.await("the " + what)
}

// checkAnswer sees whether the key that was just handled answered the question that waits, and whether it answered as the transcript
// says it did.
func (p *chatPlayer) checkAnswer(key string) error {
	if p.asking == nil {
		return nil
	}
	select {
	case d := <-p.asking.q.ans:
		w := p.asking.want
		p.asking = nil
		if d.Allow != w.Allow || d.Remember != w.Remember {
			return fmt.Errorf("the key %s answered the question with allow=%v remember=%q, the transcript says allow=%v remember=%q", key, d.Allow, d.Remember, w.Allow, w.Remember)
		}
	default:
	}
	return nil
}

// endTurn lets the turn that runs end, the way the transcript says it ended.
func (p *chatPlayer) endTurn(e *ChatTurnEnd) error {
	if p.host == nil {
		return errors.New("a turn ended before the session")
	}
	if p.asking != nil {
		return errors.New("the turn ended with a question that no key had answered")
	}
	res := TurnResult{Steps: e.Steps, CostUSD: e.CostUSD, HitRatio: e.HitRatio, Message: e.Message}
	switch e.Err {
	case "":
	case ErrTranscriptCanceled:
		// The program is what cancels a turn (Ctrl-C, Esc): the transcript's keys must have done it.
		if ctx := p.host.turnContext(); ctx == nil || ctx.Err() == nil {
			return errors.New("the transcript says the turn was cancelled, and the program did not cancel it")
		}
		res.Err = context.Canceled
	default:
		res.Err = errors.New(e.Err)
	}
	p.host.release <- res
	return p.await("the end of the turn")
}

// playStartRow is the row the program's cursor is on when it starts: under the shell's line, or the top.
func playStartRow(o ChatPlayOptions) int {
	if o.Shell == "-" {
		return 0
	}
	return 1
}
