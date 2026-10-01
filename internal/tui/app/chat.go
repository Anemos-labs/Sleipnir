package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

// RunChat is the inline chat: the program behind `sleipnir chat` on a terminal.
//
// It is the only writer to the terminal and the only owner of its input. Everything else that takes part reaches it through a
// channel: the keys, the window's size, the tick of the clock, the session itself (ChatConfig.Attach), what the session says
// while it works (the sink and the prompter of a ChatLink) and the session's log (ChatAttach.Events). That is what makes Ctrl-C,
// an approval and a line typed ahead coherent: they are all settled here, one event at a time, in the order they arrive, and no
// other goroutine prints. The turn itself runs on a goroutine the program starts and can cancel, and so does a slash command.
//
// Like the program of `watch`, it reads no clock (time is the tick, or ChatConfig.Now) and draws once after every event it
// handles, which is what a test waits on. It returns how the chat ended and, for ChatFailed, why. The screen is closed and the
// link ended whatever way it ends.
func RunChat(ctx context.Context, c ChatConfig) (end ChatEnd, err error) {
	m := newChatModel(ctx, c)
	defer func() {
		m.finish()
		if cerr := m.scr.Close(); err == nil {
			err = cerr
		}
	}()
	if err = m.draw(); err != nil {
		return ChatFailed, err
	}
	keys, sizes, tick, attach := c.Keys, c.Sizes, c.Tick, c.Attach
	msgs := m.c.Link.msgs
	ctxDone := ctx.Done()
	for !m.over {
		logCh := m.events
		select {
		case <-ctxDone:
			ctxDone = nil
			m.quit(ChatInterrupted)
		case k, ok := <-keys:
			if !ok {
				keys = nil
				m.quit(ChatQuit) // the terminal is gone
				break
			}
			m.key(k)
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
			m.tick(t)
		case a, ok := <-attach:
			attach = nil
			if ok {
				m.attach(a)
			}
		case e, ok := <-logCh:
			if !ok {
				m.events = nil
				continue
			}
			m.event(e)
			m.drainEvents()
		case msg := <-msgs:
			m.msg(msg)
			m.drainMsgs()
		case e := <-m.runDone:
			m.runEnded(e)
		}
		if m.over {
			break
		}
		if err = m.draw(); err != nil {
			m.end = ChatFailed
			break
		}
	}
	if m.failure != nil && err == nil {
		err = m.failure
	}
	return m.end, err
}

// runKind is what the program has running.
type runKind uint8

const (
	runTurn runKind = iota
	runCommand
)

// run is a turn or a slash command that is running on a goroutine of the program's.
type run struct {
	kind    runKind
	cancel  context.CancelFunc
	started time.Time
	label   string // the slash command's name
	base    totalsBase
	n       int
}

// runEnd is what a run's goroutine reports when it is over.
type runEnd struct {
	run  *run
	turn TurnResult
	cmd  CommandResult
	out  string
}

// totalsBase is what the session had used when a turn began, so that the status line can say what this turn has used.
type totalsBase struct {
	prompt, output int64
	cost, saved    float64
}

// queued is something typed ahead: a line, or a Ctrl-D (the end of the input), in the order it was typed.
type queued struct {
	text string
	eof  bool
}

// blockKind is what the last thing printed into the scrollback was, which decides the blank line before the next.
type blockKind uint8

const (
	bkNone blockKind = iota
	bkBanner
	bkPrompt
	bkText
	bkTool
	bkToolBody
	bkNote
	bkSummary
)

// toolRun is a tool call that is in flight.
type toolRun struct {
	key    string
	id     string
	agent  string
	name   string
	input  json.RawMessage
	since  time.Time
	asking bool
}

// foldAnim is a compaction that is being folded, in the live region, before it is written into the scrollback.
type foldAnim struct {
	comp  state.Compaction
	who   string
	start int
}

// answerStream is the assistant's words of one message as they arrive: the markdown that is not final yet is in the live region,
// and what is final is printed once.
type answerStream struct {
	md      widget.MDStream
	printed int // lines of the message already in the scrollback
}

type chatModel struct {
	c   ChatConfig
	ctx context.Context
	scr ChatScreen
	k   *chatLook

	cols, rows int
	now        time.Time
	frame      int

	ed      *input.Editor
	edWidth int

	st     *state.State
	snap   *state.Snapshot
	snapOK bool
	mem    *Memory
	events <-chan events.Event

	host     ChatHost
	info     ChatInfo
	attached bool
	complete input.Completer

	running *run
	runDone chan runEnd
	queue   []queued
	turns   int

	last    blockKind
	stream  answerStream
	tools   map[string]*toolRun
	toolSeq []string
	dialogs []*dialog
	folds   []*foldAnim
	expand  []*expandable

	compactSeen uint64
	anomalySeen uint64
	flashUntil  int

	quitArmed bool
	quitAt    time.Time
	hint      string
	hintUntil time.Time

	exiting ChatEnd // the chat is ending: it waits for the run that is still going
	over    bool
	end     ChatEnd
	failure error
}

func newChatModel(ctx context.Context, c ChatConfig) *chatModel {
	if c.Link == nil {
		c.Link = NewChatLink()
	}
	if c.QuitWindow <= 0 {
		c.QuitWindow = 2 * time.Second
	}
	if c.AnswerAfter == 0 {
		c.AnswerAfter = defaultAnswerAfter
	} else if c.AnswerAfter < 0 {
		c.AnswerAfter = 0
	}
	m := &chatModel{c: c, ctx: ctx, scr: c.Screen, k: newChatLook(c.Look), st: state.New(), mem: NewMemory(),
		tools: map[string]*toolRun{}, runDone: make(chan runEnd, 1)}
	m.cols, m.rows = c.Screen.Size()
	m.now = m.clock()
	m.snap = m.st.SnapshotAt(m.now)
	prompt := m.k.g.prompt + " "
	m.ed = input.NewEditor(input.Options{History: c.History, Prompt: prompt, Placeholder: "Type a goal, / for commands, @ for files", Theme: themePtr(m.k.inputTheme()),
		Completer: input.CompleterFunc(func(line string, cursor int) (int, []input.Candidate) {
			if m.complete == nil {
				return 0, nil
			}
			return m.complete.Complete(line, cursor)
		})})
	return m
}

func themePtr(t input.Theme) *input.Theme { return &t }

// clock is the time the program goes by: the clock it was given, else the latest tick.
func (m *chatModel) clock() time.Time {
	if m.c.Now != nil {
		return m.c.Now()
	}
	return m.now
}

// finish ends the link, so that an agent that outlives the program never waits on it, and gives the last answer to a question
// that is still open.
func (m *chatModel) finish() {
	m.endStream()
	m.flushFolds()
	for _, d := range m.dialogs {
		d.answer(noAnswer)
	}
	m.dialogs = nil
	if m.running != nil {
		m.running.cancel()
	}
	m.c.Link.Close()
}

func (d *dialog) answer(dec perm.Decision) {
	select {
	case d.q.ans <- dec:
	default:
	}
}

// ---- the session ----

// attach is the session arriving, or failing to.
func (m *chatModel) attach(a ChatAttach) {
	if a.Err != nil {
		m.failure = a.Err
		m.end = ChatFailed
		m.over = true
		return
	}
	m.host, m.info, m.attached, m.events = a.Host, a.Info, true, a.Events
	var cs []input.Completer
	if len(a.Commands) > 0 {
		cs = append(cs, input.SlashCommands(a.Commands))
	}
	if a.Root != "" {
		cs = append(cs, input.Paths(a.Root))
	}
	m.complete = firstCompleter(cs)
	m.block(bkBanner, m.k.bannerLines(a.Info, m.cols))
	if m.exiting != "" {
		m.over = true
		m.end = m.exiting
		return
	}
	m.drainQueue()
}

// firstCompleter asks each completer in turn and takes the first that has candidates: the slash commands answer a word that starts
// a line with "/", the paths one that starts with "@", and neither answers the other's.
func firstCompleter(cs []input.Completer) input.Completer {
	if len(cs) == 0 {
		return nil
	}
	return input.CompleterFunc(func(line string, cursor int) (int, []input.Candidate) {
		for _, c := range cs {
			if from, cands := c.Complete(line, cursor); len(cands) > 0 {
				return from, cands
			}
		}
		return 0, nil
	})
}

// ---- keys ----

func (m *chatModel) key(k input.Key) {
	if k.Kind == input.KindPaste && k.Text == "" {
		return // the decoder makes no empty paste: this is a test's way of asking whether the key before it has been handled
	}
	m.now = m.clock()
	if !k.IsRune('c', input.Ctrl) {
		m.quitArmed = false // a second Ctrl-C quits only when nothing was typed since the first
	}
	if d := m.question(); d != nil && m.dialogKey(d, k) {
		return
	}
	m.editorKey(k)
}

// question is the question that has the keys: the oldest one that waits.
func (m *chatModel) question() *dialog {
	if len(m.dialogs) == 0 {
		return nil
	}
	return m.dialogs[0]
}

// dialogKey gives a key to a question, if the question is to have it (see the notes at the top of chat_dialog.go), and reports
// whether it did. A key the question does not take goes on to the prompt, and starts the wait for a quiet keyboard again.
//
// The keys that answer are the digits 1 to 3, and (while the prompt is empty) enter on the choice the arrows made, esc for no and
// Ctrl-D for no answer. Letters never answer, and neither does enter when the person has typed something into the prompt: that
// enter is sending what they typed.
func (m *chatModel) dialogKey(d *dialog, k input.Key) bool {
	if k.IsRune('c', input.Ctrl) {
		return false // Ctrl-C can only cancel, and is the editor's to report
	}
	now := m.clock()
	if !d.armed(now) {
		d.armAt = now.Add(m.c.AnswerAfter)
		return false
	}
	switch {
	case isChoice(k, len(d.opts)):
	case m.ed.Empty() && (k.Is(input.Esc, 0) || k.Is(input.Enter, 0) || isNavigation(k) || k.IsRune('d', input.Ctrl)):
	default:
		d.armAt = now.Add(m.c.AnswerAfter)
		return false
	}
	if k.IsRune('d', input.Ctrl) {
		m.answerQuestion(d, noAnswer)
		return true
	}
	if i, ok := d.choose(k); ok {
		m.answerQuestion(d, d.decision(i))
	}
	return true // a key that moved the choice
}

// isChoice reports whether k is the number of one of the n options. Any other digit is part of what is being typed.
func isChoice(k input.Key, n int) bool {
	return k.Kind == input.KindRune && k.Mod == 0 && k.R >= '1' && k.R <= '9' && int(k.R-'0') <= n
}

// isNavigation reports the keys that move the choice of a question and do nothing else.
func isNavigation(k input.Key) bool {
	return k.Is(input.Up, 0) || k.Is(input.Down, 0) || k.Is(input.Tab, 0) || k.Is(input.Tab, input.Shift) ||
		k.IsRune('p', input.Ctrl) || k.IsRune('n', input.Ctrl)
}

// answerQuestion gives the answer to whoever asked, and takes the question off the screen.
func (m *chatModel) answerQuestion(d *dialog, dec perm.Decision) {
	d.answer(dec)
	m.dropQuestion(d.q)
}

func (m *chatModel) dropQuestion(q *question) {
	for i, d := range m.dialogs {
		if d.q == q {
			m.dialogs = append(m.dialogs[:i:i], m.dialogs[i+1:]...)
			break
		}
	}
	for _, t := range m.tools {
		t.asking = false
	}
	for _, d := range m.dialogs { // the tool of a question that still waits is still asking
		if t := m.callOf(d.q.req); t != nil {
			t.asking = true
		}
	}
}

func (m *chatModel) editorKey(k input.Key) {
	m.syncEditorWidth()
	if k.IsRune('t', input.Ctrl) {
		// The stack panel (docs/UX.md). The editor would transpose two characters with it, and a person who wants that has the arrows.
		m.printStack()
		return
	}
	if k.Is(input.Enter, 0) && m.menuIsExact() {
		// A command typed out in full is not completed, it is sent: enter would accept the one candidate (which is what was typed,
		// and a space), and the person would have to press it again.
		m.handle(k)
	}
	m.handle(k)
}

// menuIsExact reports whether the completion menu is open on a candidate that is exactly the word that was typed.
func (m *chatModel) menuIsExact() bool {
	ms := m.ed.Completion()
	if !ms.Open || ms.Selected < 0 || ms.Selected >= len(ms.Candidates) {
		return false
	}
	cand := ms.Candidates[ms.Selected].Text
	if !strings.HasSuffix(cand, " ") { // a directory keeps the menu open on what is inside it
		return false
	}
	rs, cur := []rune(m.ed.Text()), m.ed.Cursor()
	if ms.From < 0 || ms.From > cur || cur > len(rs) {
		return false
	}
	typed := string(rs[ms.From:cur])
	return typed != "" && typed == strings.TrimRight(cand, " ")
}

// handle gives a key to the editor and acts on what it reports.
func (m *chatModel) handle(k input.Key) {
	for _, ev := range m.ed.Handle(k) {
		switch ev := ev.(type) {
		case input.Submit:
			m.submit(ev.Text)
		case input.Interrupt:
			m.interrupt()
		case input.EOF:
			m.eof()
		case input.Redraw:
			m.scr.SetLive(nil) // the terminal was scribbled on: take the live region off, and it is drawn again at once
			_ = m.scr.Flush()
		case input.Unhandled:
			m.unhandled(ev.Key)
		}
	}
}

// syncEditorWidth tells the editor how wide its text area is: the box's inner width.
func (m *chatModel) syncEditorWidth() {
	w := widget.BoxInnerWidth(m.cols)
	if w < 10 {
		w = m.cols
	}
	if w != m.edWidth {
		m.edWidth = w
		m.ed.SetWidth(w)
	}
}

// busy says whether something is running that Esc and Ctrl-C cancel: a turn, a slash command, or the making of the session.
func (m *chatModel) busy() bool { return m.running != nil || !m.attached }

// interrupt is Ctrl-C. While something runs it cancels that and nothing else: the turn (and so the question it asks, which takes no
// input after), a slash command, the making of the session. At the prompt the first press says how to quit, and a second within
// the window, with nothing typed between, quits.
func (m *chatModel) interrupt() {
	if m.busy() {
		m.cancelRun()
		return
	}
	now := m.clock()
	if m.quitArmed && now.Sub(m.quitAt) <= m.c.QuitWindow {
		m.quitArmed = false
		m.quit(ChatInterrupted)
		return
	}
	m.quitArmed, m.quitAt = true, now
	m.block(bkNote, []cell.Line{cell.Styled(m.k.st.dim, QuitHint)})
}

// cancelRun cancels what runs, and with it the questions it asks: the turn's, or the session's while it is being made (a tool server
// of the project asks whether it may start, and the start waits for the answer).
func (m *chatModel) cancelRun() {
	switch {
	case m.running != nil:
		m.running.cancel()
	case !m.attached && m.c.CancelStart != nil:
		m.c.CancelStart()
	default:
		return
	}
	for _, d := range m.dialogs {
		d.answer(noAnswer)
	}
	m.dialogs = nil
	for _, t := range m.tools {
		t.asking = false
	}
}

// eof is Ctrl-D on an empty prompt: quit, or (while something runs) quit when it is over, like a line typed ahead.
func (m *chatModel) eof() {
	if m.busy() || len(m.queue) > 0 {
		m.queue = append(m.queue, queued{eof: true})
		return
	}
	m.quit(ChatQuit)
}

// unhandled is a key the editor has no use for.
func (m *chatModel) unhandled(k input.Key) {
	switch {
	case k.Is(input.Esc, 0):
		if m.busy() && m.question() == nil {
			m.cancelRun()
		}
	case k.Is(input.Tab, input.Shift):
		m.cycleMode()
	case k.IsRune('o', input.Ctrl):
		m.expandLast()
	}
}

// modeCycle is the order shift+tab steps through the permission modes. bypass is never stepped into: it is asked for by name.
var modeCycle = map[string]string{"default": "accept-edits", "accept-edits": "plan", "plan": "default", "bypass": "default"}

func (m *chatModel) cycleMode() {
	if m.host == nil {
		return
	}
	next, ok := modeCycle[m.host.Mode()]
	if !ok {
		next = "default"
	}
	got := m.host.SetMode(next)
	m.setHint("mode: " + got)
}

// hintFor is how long a line of the program's own stays in the footer.
const hintFor = 2 * time.Second

func (m *chatModel) setHint(s string) {
	m.hint, m.hintUntil = s, m.clock().Add(hintFor)
}

// ---- submit, dispatch, the queue ----

// submit is Enter on a prompt that has something in it.
func (m *chatModel) submit(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if m.busy() || len(m.queue) > 0 {
		m.queue = append(m.queue, queued{text: text}) // typed ahead: sent when the turn ends, and never the answer to a question
		return
	}
	m.dispatch(text)
}

// dispatch sends a line: a slash command runs, anything else is a goal.
func (m *chatModel) dispatch(text string) {
	m.block(bkPrompt, m.k.promptLines(text, m.cols))
	if strings.HasPrefix(text, "/") {
		m.startCommand(text)
		return
	}
	m.startTurn(text)
}

// drainQueue sends what was typed ahead, in order, for as long as nothing runs.
func (m *chatModel) drainQueue() {
	for m.running == nil && m.attached && len(m.queue) > 0 && m.exiting == "" {
		q := m.queue[0]
		m.queue = m.queue[1:]
		if q.eof {
			m.quit(ChatQuit)
			return
		}
		m.dispatch(q.text)
	}
}

// quit ends the chat: at once when nothing runs, else when what runs has been cancelled and has ended.
func (m *chatModel) quit(reason ChatEnd) {
	if m.exiting != "" {
		return
	}
	m.exiting = reason
	if m.running != nil {
		m.cancelRun()
		return
	}
	if !m.attached && m.c.CancelStart != nil {
		m.c.CancelStart()
	}
	m.end, m.over = reason, true
}

// ---- runs ----

func (m *chatModel) startTurn(goal string) {
	m.turns++
	ctx, cancel := context.WithCancel(m.ctx)
	r := &run{kind: runTurn, cancel: cancel, started: m.clock(), base: m.totalsNow(), n: m.turns}
	m.running = r
	host := m.host
	go func() {
		var res TurnResult
		defer func() {
			if p := recover(); p != nil {
				res = TurnResult{Err: fmt.Errorf("the turn panicked: %v", p)}
			}
			cancel()
			m.runDone <- runEnd{run: r, turn: res}
		}()
		res = host.Turn(ctx, goal)
	}()
}

func (m *chatModel) startCommand(line string) {
	ctx, cancel := context.WithCancel(m.ctx)
	name := strings.Fields(line)[0]
	r := &run{kind: runCommand, cancel: cancel, started: m.clock(), label: name}
	m.running = r
	host := m.host
	go func() {
		var res CommandResult
		var out lockedBuffer
		defer func() {
			if p := recover(); p != nil {
				fmt.Fprintf(&out, "%s: panicked: %v\n", name, p)
			}
			cancel()
			m.runDone <- runEnd{run: r, cmd: res, out: out.String()}
		}()
		res = host.Command(ctx, line, &out)
	}()
}

// lockedBuffer collects what a slash command writes; it may write from more than one goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// runEnded is a turn or a command coming back.
func (m *chatModel) runEnded(e runEnd) {
	r := e.run
	m.settle()
	m.snapshot() // may bring a fold
	m.running = nil
	m.dialogs = nil
	m.endStream()
	m.flushFolds()
	m.flushTools(e.turn.Err != nil)
	switch r.kind {
	case runTurn:
		m.turnEnded(r, e.turn)
	case runCommand:
		m.commandEnded(e)
	}
	if m.exiting != "" {
		m.end, m.over = m.exiting, true
		return
	}
	m.drainQueue()
}

func (m *chatModel) turnEnded(r *run, res TurnResult) {
	snap := m.snapshot()
	saved := snap.Totals.Savings.SavedUSD - r.base.saved
	if res.Steps > 0 || res.CostUSD > 0 { // a turn that was cancelled before the model answered did nothing worth a record
		m.block(bkSummary, []cell.Line{m.k.turnSummary(m.since(r.started), res, saved, m.cols)})
	}
	switch {
	case res.Err == nil:
	case errors.Is(res.Err, context.Canceled):
		m.block(bkNote, []cell.Line{cell.Styled(m.k.st.dim, "(cancelled)")})
	case res.Message != "":
		m.block(bkNote, m.k.noticeLines("", "warn", res.Message, m.cols, true))
	default:
		m.block(bkNote, m.k.noticeLines("", "error", "error: "+res.Err.Error(), m.cols, true))
	}
}

func (m *chatModel) commandEnded(e runEnd) {
	if e.out != "" {
		var lines []cell.Line
		for _, l := range textLines(e.out) {
			lines = append(lines, cell.Text(l))
		}
		m.block(bkNote, lines)
	}
	if e.cmd.Quit {
		m.quit(ChatQuit)
		return
	}
	if e.cmd.Send != "" {
		m.startTurn(e.cmd.Send) // a custom command or a skill, expanded into a prompt
	}
}

// since is how long ago t was by the program's clock: nothing for a moment the clock had not started yet.
func (m *chatModel) since(t time.Time) time.Duration {
	now := m.clock()
	if t.IsZero() || now.IsZero() || now.Before(t) {
		return 0
	}
	return now.Sub(t)
}

// totalsNow is what the session has used so far.
func (m *chatModel) totalsNow() totalsBase {
	t := m.snapshot().Totals
	return totalsBase{prompt: t.Tokens.Prompt(), output: t.Tokens.Output, cost: t.CostUSD, saved: t.Savings.SavedUSD}
}

// ---- what the session says while it works ----

func (m *chatModel) isMain(agent string) bool { return m.c.MainAgent == "" || agent == m.c.MainAgent }

func (m *chatModel) msg(x chatMsg) {
	switch x.kind {
	case mText:
		if m.isMain(x.agent) {
			m.stream.md.Append(x.text)
		}
	case mReset:
		if m.isMain(x.agent) {
			m.stream.md.Reset() // what was final stays in the scrollback; the attempt that failed leaves no more
			m.stream.printed = 0
		}
	case mResponse:
		if m.isMain(x.agent) {
			m.endStream()
		}
	case mToolStart:
		m.toolStart(x)
	case mToolEnd:
		m.toolEnd(x)
	case mNotice:
		m.notice(x)
	case mQuestion:
		m.ask(x.q)
	case mQuestionGone:
		m.dropQuestion(x.q)
	}
}

// settle takes everything the run said and logged before it returned. The run's goroutine sends a message, or emits an event,
// before it reports that it is over, and both are in their channels by then; but the program chooses among the channels that are
// ready, and could take the end first, and show a tool that has finished as one that never did.
func (m *chatModel) settle() {
	// What the run sent is in the channel already, and at most as much as it holds: a bound keeps a producer that never stops (an
	// agent of a swarm that is still talking) from keeping the program here.
	for i := 0; i < 2*linkBuffer; i++ {
		select {
		case x := <-m.c.Link.msgs:
			m.msg(x)
			continue
		default:
		}
		break
	}
	for i := 0; i < 2*eventBuffer && m.events != nil; i++ {
		select {
		case e, ok := <-m.events:
			if !ok {
				m.events = nil
				return
			}
			m.event(e)
			continue
		default:
		}
		break
	}
}

// eventBuffer is how many events the session's log holds for a subscriber before it drops one (cmd/sleipnir subscribes with it).
const eventBuffer = 4096

// drainMsgs takes what the session has said that is already waiting, so that a burst of it costs one frame.
func (m *chatModel) drainMsgs() {
	for i := 0; i < 128 && !m.over; i++ {
		select {
		case x := <-m.c.Link.msgs:
			m.msg(x)
		default:
			return
		}
	}
}

func (m *chatModel) drainEvents() {
	for i := 0; i < 512; i++ {
		select {
		case e, ok := <-m.events:
			if !ok {
				m.events = nil
				return
			}
			m.event(e)
		default:
			return
		}
	}
}

func (m *chatModel) notice(x chatMsg) {
	if strings.EqualFold(x.level, "info") && !m.c.Verbose {
		return
	}
	m.syncStream()
	m.block(bkNote, m.k.noticeLines(x.agent, x.level, x.text, m.cols, m.isMain(x.agent) || x.agent == ""))
}

func toolKey(agent, id string) string { return agent + "\x00" + id }

func (m *chatModel) toolStart(x chatMsg) {
	key := toolKey(x.agent, x.call.ToolID)
	t := &toolRun{key: key, id: x.call.ToolID, agent: x.agent, name: x.call.ToolName, input: x.call.Input, since: m.clock()}
	if _, dup := m.tools[key]; !dup {
		m.toolSeq = append(m.toolSeq, key)
	}
	m.tools[key] = t
}

func (m *chatModel) toolEnd(x chatMsg) {
	key := toolKey(x.agent, x.call.ToolID)
	if _, ok := m.tools[key]; ok {
		delete(m.tools, key)
		for i, k := range m.toolSeq {
			if k == key {
				m.toolSeq = append(m.toolSeq[:i:i], m.toolSeq[i+1:]...)
				break
			}
		}
	}
	m.printTool(doneTool{agent: x.agent, call: x.call, res: x.res, took: x.took, cwd: m.info.Cwd, worker: !m.isMain(x.agent)})
}

// printTool writes a finished call into the scrollback.
func (m *chatModel) printTool(t doneTool) {
	m.syncStream()
	lines, exp := m.k.toolLines(t, m.cols)
	if exp != nil {
		m.expand = append(m.expand, exp)
		if len(m.expand) > 16 {
			m.expand = m.expand[1:]
		}
	}
	kind := bkTool
	if len(lines) > 1 {
		kind = bkToolBody
	}
	m.block(kind, lines)
}

// flushTools writes the calls that never ended (the turn was cancelled under them) as cancelled, so that the scrollback does not
// lose them.
func (m *chatModel) flushTools(cancelled bool) {
	for _, key := range m.toolSeq {
		t := m.tools[key]
		if t == nil {
			continue
		}
		res := toolResult{Failed: true, IsError: true, Text: "cancelled"}
		if !cancelled {
			res = toolResult{Text: ""}
		}
		m.printTool(doneTool{agent: t.agent, call: callOf(t), res: res, took: m.since(t.since), cwd: m.info.Cwd, worker: !m.isMain(t.agent)})
	}
	m.tools = map[string]*toolRun{}
	m.toolSeq = nil
}

// ---- the answer ----

// syncStream prints what of the answer is final and returns the rest, to be drawn in the live region.
func (m *chatModel) syncStream() []cell.Line {
	if m.stream.md.Len() == 0 {
		return nil
	}
	stable, tail := m.stream.md.Render(max(m.cols-2, 1), m.k.Theme)
	if len(stable) > m.stream.printed {
		m.printAnswer(m.k.answerLines(stable, m.stream.printed))
		m.stream.printed = len(stable)
	}
	if len(tail) == 0 {
		return nil
	}
	all := append(stable[:len(stable):len(stable)], tail...)
	return m.k.answerLines(all, m.stream.printed)
}

// endStream prints the rest of the answer: the message is over.
func (m *chatModel) endStream() {
	if m.stream.md.Len() == 0 {
		m.stream.printed = 0
		return
	}
	stable, tail := m.stream.md.Render(max(m.cols-2, 1), m.k.Theme)
	all := append(stable[:len(stable):len(stable)], tail...)
	lines := m.k.answerLines(all, m.stream.printed)
	for len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	m.printAnswer(lines)
	m.stream.md.Reset()
	m.stream.printed = 0
}

// printAnswer prints lines of the answer: a blank line before the first of a message, none inside it.
func (m *chatModel) printAnswer(lines []cell.Line) {
	if m.stream.printed > 0 {
		m.print(bkText, lines)
		return
	}
	m.block(bkText, lines)
}

// ---- the scrollback ----

// block prints a block into the scrollback, with the blank line it needs after what came before.
func (m *chatModel) block(kind blockKind, lines []cell.Line) {
	if len(lines) == 0 {
		return
	}
	if m.gapBefore(kind) {
		m.scr.Print(nil)
	}
	m.print(kind, lines)
}

func (m *chatModel) print(kind blockKind, lines []cell.Line) {
	m.scr.Print(lines...)
	m.last = kind
}

// gapBefore says whether a block of this kind is set off from the last one by a blank line. A one-line tool call follows another
// without one; anything with a body, a prompt and the words of the answer stand apart.
func (m *chatModel) gapBefore(kind blockKind) bool {
	switch m.last {
	case bkNone:
		return false
	}
	switch kind {
	case bkPrompt, bkToolBody, bkSummary, bkBanner:
		return true
	case bkText:
		return m.last != bkText
	case bkTool:
		return m.last != bkTool && m.last != bkNote
	}
	return false
}

// ---- size and time ----

func (m *chatModel) resize(cols, rows int) {
	if cols == m.cols && rows == m.rows {
		return
	}
	if cols != m.cols {
		m.endStream() // what is final was laid out for the old width: the message goes on in pieces, at the new one
	}
	m.scr.Resize(cols, rows)
	m.cols, m.rows = m.scr.Size()
	m.snapOK = false
}

func (m *chatModel) tick(t time.Time) {
	m.now = t
	if m.c.Now != nil {
		m.now = m.c.Now()
	}
	if m.k.Anim {
		m.frame++
	}
	if m.hint != "" && !m.now.Before(m.hintUntil) {
		m.hint = ""
	}
	m.snapOK = false
}

// ---- the log ----

func (m *chatModel) event(e events.Event) {
	m.st.Apply(e)
	m.snapOK = false
}

// snapshot is the state as of now, taken once per change.
func (m *chatModel) snapshot() *state.Snapshot {
	if !m.snapOK {
		m.snap = m.st.SnapshotAt(m.clock())
		m.snapOK = true
		m.mem.Observe(m.snap, m.frame)
		m.scanSnapshot()
	}
	return m.snap
}

// scanSnapshot finds what the log has added that the scrollback should say: a compaction, a cache break.
func (m *chatModel) scanSnapshot() {
	sn := m.snap
	type found struct {
		seq  uint64
		fold *state.Compaction
		anom *state.Anomaly
		who  string
	}
	var news []found
	main := sn.Main()
	for i := range sn.Agents {
		a := &sn.Agents[i]
		who := ""
		if a.ID != main {
			who = a.ID
		}
		for j := range a.Compacts {
			if c := &a.Compacts[j]; c.Seq > m.compactSeen {
				news = append(news, found{seq: c.Seq, fold: c, who: who})
			}
		}
		for j := range a.Anomalies {
			if an := &a.Anomalies[j]; an.Seq > m.anomalySeen {
				news = append(news, found{seq: an.Seq, anom: an, who: who})
			}
		}
	}
	if len(news) == 0 {
		return
	}
	sortFound := func() {
		for i := 1; i < len(news); i++ {
			for j := i; j > 0 && news[j].seq < news[j-1].seq; j-- {
				news[j], news[j-1] = news[j-1], news[j]
			}
		}
	}
	sortFound()
	for _, f := range news {
		switch {
		case f.fold != nil:
			m.compactSeen = max(m.compactSeen, f.seq)
			if m.k.Anim {
				m.folds = append(m.folds, &foldAnim{comp: *f.fold, who: f.who, start: m.frame})
			} else {
				m.printFold(*f.fold, f.who)
			}
		case f.anom != nil:
			m.anomalySeen = max(m.anomalySeen, f.seq)
			m.syncStream()
			m.block(bkNote, m.k.anomalyLines(*f.anom, f.who, m.cols))
			if m.k.Anim {
				m.flashUntil = m.frame + flashFrames
			}
		}
	}
}

func (m *chatModel) printFold(c state.Compaction, who string) {
	m.syncStream()
	m.block(bkNote, m.k.foldRecord(c, who, m.cols))
}

// flushFolds writes the records of the folds that are still playing: the turn is over, or the program is.
func (m *chatModel) flushFolds() {
	for _, f := range m.folds {
		m.printFold(f.comp, f.who)
	}
	m.folds = nil
}

// stepFolds finishes the folds whose animation is over: each leaves its record in the scrollback.
func (m *chatModel) stepFolds() {
	kept := m.folds[:0]
	for _, f := range m.folds {
		if m.frame-f.start >= foldFrames {
			m.printFold(f.comp, f.who)
			continue
		}
		kept = append(kept, f)
	}
	m.folds = kept
}

// ---- a question ----

// ask puts a question on the screen.
func (m *chatModel) ask(q *question) {
	opts, mcp := dialogOptions(q.req)
	d := &dialog{q: q, opts: opts, mcp: mcp, armAt: m.clock().Add(m.c.AnswerAfter)}
	m.dialogs = append(m.dialogs, d)
	if t := m.callOf(q.req); t != nil {
		t.asking = true
	}
	// A request that is too tall for the live region is written into the scrollback whole, so that what was approved is on record
	// and can be read; the dialog shows its beginning and its end.
	title, body := m.k.requestBody(q.req, m.callOf(q.req), widget.BoxInnerWidth(m.cols, widget.BoxHardWrap()), m.info.Cwd)
	if _, cut := m.k.fitBody(body, m.dialogRows()); cut > 0 {
		m.syncStream()
		rec := []cell.Line{cell.Styled(m.k.st.warn, m.k.g.ask+" "+title+": needs your answer")}
		m.block(bkNote, append(rec, indentLines(body, cell.Text("  "))...))
	}
}

// dialogRows is how many rows of the live region a question's body may take: what is left of the terminal when the box's
// edges, its three options, the blank row before them, the hint, the status line and the footer are taken out.
func (m *chatModel) dialogRows() int { return max(m.rows-12, 3) }

// callOf is the call in flight that a question is about: the same agent, the same tool. Nothing says which call of a tool asked,
// so of several the oldest that has not been asked about is taken.
func (m *chatModel) callOf(r perm.Request) *toolRun {
	var best *toolRun
	for _, key := range m.toolSeq {
		t := m.tools[key]
		if t == nil || t.agent != r.Agent || !strings.EqualFold(t.name, r.Tool) {
			continue
		}
		if best == nil || (best.asking && !t.asking) {
			best = t
		}
	}
	return best
}

func callOf(t *toolRun) core.Block {
	return core.Block{ToolID: t.id, ToolName: t.name, Input: t.input}
}

// ---- panels on demand ----

// printStack is ctrl+t: the prompt stack layer by layer, written into the scrollback.
func (m *chatModel) printStack() {
	sn := m.snapshot()
	a := stackAgent(&liveView{snap: sn})
	if a == nil {
		m.block(bkNote, []cell.Line{cell.Styled(m.k.st.dim, "  no prompt has been sent yet")})
		return
	}
	layers := promptLayers(a, m.mem.g0est())
	o := widget.NewStackOpts(max(m.cols-2, 20), a.Stack.Read)
	if !a.Stack.Answered {
		o.CachedTokens = 0
	}
	o.BreakAt = breakLayer(a, layers)
	table := widget.StackTable(layers, o, m.k.Palette)
	if left, total, ok := ttlOf(sn, "agent", a.ID); ok {
		table = append(table, m.k.ttlLine(left, total, max(m.cols-2, 8)))
	}
	if !m.k.Unicode {
		for i := range table {
			table[i] = asciiLine(table[i])
		}
	}
	lines := indentLines(table, cell.Text("  "))
	m.syncStream()
	m.block(bkToolBody, append([]cell.Line{cell.Styled(m.k.st.dim, "  "+m.k.g.compact+" the prompt stack")}, lines...))
}

// expandLast is ctrl+o: the whole of the newest output that was collapsed, written into the scrollback.
func (m *chatModel) expandLast() {
	if len(m.expand) == 0 {
		m.setHint("nothing is collapsed")
		return
	}
	e := m.expand[len(m.expand)-1]
	m.expand = m.expand[:len(m.expand)-1]
	out := []cell.Line{cell.Styled(m.k.st.dim, "  "+m.k.g.result+" "+clean(e.title)+" (whole output)")}
	for _, l := range e.lines {
		out = append(out, cell.Join(cell.Text("    "), cell.Text(cutCells(l, max(m.cols-4, 1), m.k.g.ellipsis))))
	}
	m.syncStream()
	m.block(bkToolBody, out)
}

// ---- drawing ----

// draw puts the screen on the terminal: the scrollback that has become final, then the live region, then the cursor.
func (m *chatModel) draw() error {
	m.now = m.clock()
	m.syncEditorWidth()
	m.snapshot() // may print what the log has added; before the tail is taken, which depends on what is printed
	m.stepFolds()
	tail := m.syncStream()
	v := m.liveView(tail)
	out := m.k.liveLines(&v)
	m.scr.SetLive(out.lines)
	m.scr.SetCursor(out.curRow, out.curCol)
	return m.scr.Flush()
}

// liveView gathers what the live region shows.
func (m *chatModel) liveView(tail []cell.Line) liveView {
	sn := m.snapshot()
	v := liveView{cols: m.cols, rows: m.rows, frame: m.frame, tail: tail, snap: sn, agent: "", mem: m.mem,
		ed: m.ed.View(m.edWidthOr()), queue: m.queueTexts(), hint: m.hint}
	if m.host != nil {
		v.mode = m.host.Mode()
	}
	v.model, v.session = m.info.Model, m.info.SessionID
	v.status = m.statusView(sn)
	for _, key := range m.toolSeq {
		if t := m.tools[key]; t != nil {
			v.tools = append(v.tools, toolView{agent: t.agent, title: toolTitle(t.name), summary: toolSummary(t.name, t.input, m.info.Cwd),
				elapsed: m.since(t.since), asking: t.asking, worker: !m.isMain(t.agent)})
		}
	}
	if len(m.folds) > 0 {
		f := m.folds[0]
		v.fold = &foldView{who: f.who, before: f.comp.Before, after: f.comp.After, progress: float64(m.frame-f.start) / foldFrames}
	}
	if d := m.question(); d != nil {
		v.dlg = m.dialogView(d)
	}
	return v
}

func (m *chatModel) edWidthOr() int {
	if m.edWidth > 0 {
		return m.edWidth
	}
	return m.cols
}

func (m *chatModel) queueTexts() []string {
	var out []string
	for _, q := range m.queue {
		if q.eof {
			out = append(out, "(quit when this is done)")
			continue
		}
		out = append(out, q.text)
	}
	return out
}

func (m *chatModel) dialogView(d *dialog) *dialogView {
	title, body := m.k.requestBody(d.q.req, m.callOf(d.q.req), widget.BoxInnerWidth(m.cols, widget.BoxHardWrap()), m.info.Cwd)
	body, _ = m.k.fitBody(body, m.dialogRows())
	return &dialogView{title: title, body: body, options: d.opts, sel: d.sel, armed: d.armed(m.clock())}
}

// statusView is what the status line says: what the agent is doing, for how long, and what this turn has used.
func (m *chatModel) statusView(sn *state.Snapshot) statusView {
	var s statusView
	switch {
	case !m.attached:
		s.kind = statusStarting
		return s
	case m.running == nil:
		return s
	}
	r := m.running
	s.seed = r.n
	s.elapsed = m.since(r.started)
	s.flash = m.frame < m.flashUntil
	if r.kind == runCommand {
		s.kind, s.detail = statusCommand, r.label
		return s
	}
	t := sn.Totals
	s.tokIn, s.tokOut = max(t.Tokens.Prompt()-r.base.prompt, 0), max(t.Tokens.Output-r.base.output, 0)
	s.cost, s.saved = max(t.CostUSD-r.base.cost, 0), max(t.Savings.SavedUSD-r.base.saved, 0)
	s.kind = statusThinking
	if a, ok := sn.Focused(""); ok {
		switch {
		case a.Stuck.Active:
			s.kind = statusStuck
		case a.Compacting:
			s.kind = statusCompacting
		case a.Status == state.StatusWaiting:
			s.kind = statusWaiting
		case a.Status == state.StatusTool || a.Status == state.StatusEditing:
			s.kind = statusTool
		}
	}
	if len(m.tools) > 0 {
		s.kind = statusTool
		s.detail = m.toolDetail()
	}
	if m.question() != nil || len(sn.Perms.Pending) > 0 {
		s.kind = statusAsking
	}
	return s
}

// toolDetail names what is running: the tool, or how many.
func (m *chatModel) toolDetail() string {
	var main []*toolRun
	for _, key := range m.toolSeq {
		if t := m.tools[key]; t != nil && m.isMain(t.agent) {
			main = append(main, t)
		}
	}
	switch len(main) {
	case 0:
		return ""
	case 1:
		return toolTitle(main[0].name)
	}
	return fmt.Sprintf("%d tools", len(main))
}
