package main

// What chat owns that is not the loop: the one reader of its input (stdinLines) and the one
// handler of its Ctrl-C (interrupts).
//
// Both exist because the loop used to share them. Every read of the input was a goroutine
// that read a line from one bufio.Reader and was abandoned when its context ended; the prompt
// and the approval question each had their own. A question cancelled by Ctrl-C left its
// goroutine reading, and that goroutine took the next line the person typed (and raced with
// the prompt's goroutine on the reader). And Ctrl-C was registered twice, by main for the whole
// process and by each turn, so the signal that cancelled the turn cancelled the process too.

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- the input ----

// stdinLines is the one owner of a chat session's input. A single goroutine, started once,
// reads it line by line, and everything that wants a line takes it from here:
//
//   - Next is the prompt's read: the oldest line nobody has taken.
//   - Answer is an approval question's read: the first line that arrives after it begins.
//
// The rules that follow, and what they are for:
//
//   - A line typed while nobody is reading (while a turn runs) is kept, in order, for Next. It
//     is never an answer: a question takes only a line that arrives after it began, so a "y"
//     typed ahead of a question cannot approve it. A line that arrives while a question is
//     waiting is the question's, whichever read asked first: the person is answering what is on
//     the screen. (Answer begins before its question is shown, not after: see Answer.)
//   - A read that ends (its context is cancelled, or Ctrl-C at the prompt) has taken nothing:
//     the goroutine that reads the input is not the read's and is never abandoned, so the next
//     line goes to the next read. A line that arrives in the instant a question is cancelled
//     goes back where it was typed, in order.
//   - The end of the input is a line's worth of news, in order after the lines before it. On a
//     terminal it is Ctrl-D: it ends one read, and reading goes on. On a pipe or a file it is
//     final, and stays: Next keeps returning it.
//   - The owner reads ahead only a bounded number of lines (maxQueued): a producer that is
//     faster than the prompt (yes | sleipnir chat) waits in its pipe instead of in memory.
//
// One boundary is a choice, because nothing can see it exactly: the line that the owner reads
// in the instant a question begins. It is the question's if it is read after Answer registered,
// and typed ahead if before. Registering before the question is shown makes the choice safe in
// the direction that matters: an answer typed once the question is on the screen, by a person or
// by a script that waited for it, is always read after the registration, so it is never lost; a
// line typed ahead is taken for the answer only if it reached the owner in the microseconds
// between the registration and the screen, which no one has seen the question to type.
type stdinLines struct {
	final bool // the end of the input is permanent (a pipe or a file, not a terminal)

	mu    sync.Mutex
	seq   uint64      // the last item's number: arrival order
	queue []lineItem  // lines (and end-of-input events) nobody has taken, in arrival order
	eofs  int         // end-of-input events in queue
	asker *answerWait // the question waiting for its answer, if any
	dead  error       // the input has ended for good: why

	ready chan struct{} // poked when queue gains an item or the input ends
	room  chan struct{} // poked when the reader may go on
}

// maxQueued is how many lines are read ahead of the prompt.
const maxQueued = 256

// errInterrupted is what Next returns when Ctrl-C is pressed at the prompt.
var errInterrupted = errors.New("interrupted")

// lineItem is a line, or (err set) the end of the input, with its place in arrival order.
type lineItem struct {
	seq  uint64
	text string
	err  error
}

// answerWait is a question waiting for its answer. item is set, under stdinLines.mu, by the
// first item that arrives afterwards, and done is closed.
type answerWait struct {
	item *lineItem
	done chan struct{}
}

// newStdinLines starts reading r. endIsFinal says that the end of r is permanent (r is not a
// terminal).
func newStdinLines(r io.Reader, endIsFinal bool) *stdinLines {
	l := &stdinLines{final: endIsFinal, ready: make(chan struct{}, 1), room: make(chan struct{}, 1)}
	go l.read(bufio.NewReader(r))
	return l
}

// poke wakes one waiter, or leaves a note for the next one; a waiter re-checks what it waits for.
func poke(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// read is the goroutine that owns the input.
func (l *stdinLines) read(rd *bufio.Reader) {
	for {
		l.waitForRoom()
		s, err := rd.ReadString('\n')
		if s != "" { // the last line may have no newline
			l.deliver(lineItem{text: strings.TrimRight(s, "\r\n")})
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF) && !l.final:
			l.deliver(lineItem{err: err}) // Ctrl-D at a terminal
		default:
			l.end(err)
			return
		}
	}
}

// waitForRoom holds the reader back while enough is waiting: the queue is full, or an end of
// input has not been taken yet (a terminal whose input is gone, such as a hung-up one, answers
// every read with the end, and reading it ahead would spin). A question waiting for its
// answer needs the reader whatever else is waiting.
func (l *stdinLines) waitForRoom() {
	for {
		l.mu.Lock()
		room := l.asker != nil || (len(l.queue) < maxQueued && l.eofs == 0)
		l.mu.Unlock()
		if room {
			return
		}
		<-l.room
	}
}

// deliver hands a new item to the question that is waiting, or queues it.
func (l *stdinLines) deliver(it lineItem) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	it.seq = l.seq
	if a := l.asker; a != nil {
		l.asker = nil
		a.item = &it
		close(a.done)
		return
	}
	l.enqueue(it)
}

// end records that the input is over for good, and tells the question that is waiting.
func (l *stdinLines) end(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dead = err
	if a := l.asker; a != nil {
		l.asker = nil
		l.seq++
		a.item = &lineItem{seq: l.seq, err: err}
		close(a.done)
	}
	poke(l.ready)
}

// enqueue puts an item in the queue by arrival order (l.mu held): at the end for a new one, and
// before the later ones for one that comes back.
func (l *stdinLines) enqueue(it lineItem) {
	i := sort.Search(len(l.queue), func(i int) bool { return l.queue[i].seq > it.seq })
	l.queue = append(l.queue, lineItem{})
	copy(l.queue[i+1:], l.queue[i:])
	l.queue[i] = it
	if it.err != nil {
		l.eofs++
	}
	poke(l.ready)
}

// Next is the prompt's read: the oldest line that nobody has taken, waiting for one if there
// is none. It ends with ctx's error, or errInterrupted when abort (Ctrl-C at the prompt) is
// signalled, having taken nothing. At the end of the input it returns the error, after the
// lines before it.
func (l *stdinLines) Next(ctx context.Context, abort <-chan struct{}) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for {
		l.mu.Lock()
		if len(l.queue) > 0 {
			it := l.queue[0]
			l.queue = l.queue[1:]
			if it.err != nil {
				l.eofs--
			}
			l.mu.Unlock()
			poke(l.room)
			return it.text, it.err
		}
		if err := l.dead; err != nil {
			l.mu.Unlock()
			return "", err
		}
		l.mu.Unlock()
		select {
		case <-l.ready:
		case <-ctx.Done():
			return "", ctx.Err()
		case <-abort:
			return "", errInterrupted
		}
	}
}

// Answer is an approval question's read (session.LinePrompter's answer function): the first line
// that arrives after the call. Lines that were already waiting are not its business (they are
// typed ahead, for the prompt). It starts taking the lines that arrive, and only then calls show,
// which puts the question on the screen: a person, or a script that waits for the question and
// answers at once, types the moment the question is visible, and that line has to find the
// question waiting for it. (The other order loses it: a line read between the question and the
// start of the wait is a line typed ahead, and the question waits for another for ever.) It ends
// with ctx's error, having taken nothing, and with the error of an input that has ended; in both
// cases without having shown the question if it did not get as far.
func (l *stdinLines) Answer(ctx context.Context, show func()) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a := &answerWait{done: make(chan struct{})}
	l.mu.Lock()
	if l.asker != nil {
		l.mu.Unlock()
		return "", errors.New("chat: a question is already waiting for its answer")
	}
	if l.dead != nil {
		err := l.dead
		l.mu.Unlock()
		return "", err
	}
	l.asker = a
	l.mu.Unlock()
	poke(l.room)
	show()
	select {
	case <-a.done:
		return a.item.text, a.item.err
	case <-ctx.Done():
		l.abandon(a)
		return "", ctx.Err()
	}
}

// abandon is what a question does when its context ends before it is answered: it stops waiting.
// If an item arrived in that instant it is not lost: it goes back in the queue, where it was
// typed, for the prompt.
func (l *stdinLines) abandon(a *answerWait) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.asker == a {
		l.asker = nil
		return
	}
	l.enqueue(*a.item) // set under l.mu before l.asker was cleared: it is there
}

// readInput reads one logical line from the prompt: a line that ends in a backslash goes on to
// the next. Ctrl-C at the prompt (abort) drops what has been typed so far.
func readInput(ctx context.Context, in *stdinLines, abort <-chan struct{}) (string, error) {
	var sb strings.Builder
	for {
		ln, err := in.Next(ctx, abort)
		if err != nil {
			return "", err
		}
		if strings.HasSuffix(ln, "\\") {
			sb.WriteString(strings.TrimSuffix(ln, "\\"))
			sb.WriteString("\n")
			os.Stderr.WriteString("… ")
			continue
		}
		sb.WriteString(ln)
		return sb.String(), nil
	}
}

// ---- Ctrl-C ----

// quitWindow is how soon after a first Ctrl-C at the prompt a second one quits.
const quitWindow = 2 * time.Second

// interrupts is the one place chat handles Ctrl-C (SIGINT). It is in one of two states:
//
//   - something is running (the start of the session, a slash command, a turn, and so any
//     question the turn is asking): Ctrl-C cancels that, and only that. The session goes on.
//   - the prompt is waiting: Ctrl-C does not quit, it says how to. A second one, within
//     quitWindow and with nothing typed in between, does.
//
// main does not register for SIGINT in this command (ownsInterrupt), so this is the only
// registration, and cancelling a turn cannot cancel the process. SIGTERM is not handled
// here: it cancels the process's context, and with it the turn and the session.
//
// The presses are counted where the signal arrives, not where the prompt looks at them: two
// presses that come before the prompt has woken up are two.
type interrupts struct {
	now func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc // cancels what is running; nil while the prompt waits
	armed  time.Time          // when the last Ctrl-C at the prompt was pressed; zero if the next one does not count as a second
	quit   bool               // a second press came within the window
	idle   chan struct{}      // poked when Ctrl-C is pressed at the prompt
}

// newInterrupts creates interrupt state with wall-clock timing and a buffered idle notification.
func newInterrupts() *interrupts {
	return &interrupts{now: time.Now, idle: make(chan struct{}, 1)}
}

// watch routes SIGINT to the interrupts until the returned function is called. It is the only
// place in chat that registers for SIGINT.
func (i *interrupts) watch() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				i.deliver()
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// deliver is one Ctrl-C.
func (i *interrupts) deliver() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cancel != nil {
		i.cancel()
		return
	}
	t := i.now()
	if !i.armed.IsZero() && t.Sub(i.armed) <= quitWindow {
		i.quit = true
	} else {
		i.armed = t
	}
	poke(i.idle)
}

// begin says that something is running which Ctrl-C cancels by calling cancel, until end.
func (i *interrupts) begin(cancel context.CancelFunc) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cancel = cancel
	i.armed, i.quit = time.Time{}, false
	select { // a press made before this began was made at the prompt, and is not for this
	case <-i.idle:
	default:
	}
}

// end says that what ran is over: the prompt is waiting again.
func (i *interrupts) end() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cancel = nil
}

// run runs f with a context that Ctrl-C cancels, and that is over when f returns.
func (i *interrupts) run(ctx context.Context, f func(context.Context)) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	i.begin(cancel)
	defer i.end()
	f(cctx)
}

// pressed is asked by the prompt after a Ctrl-C woke it: did a second one follow the first, so
// that the session should end?
func (i *interrupts) pressed() (quit bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	quit = i.quit
	if quit {
		i.armed, i.quit = time.Time{}, false
	}
	return quit
}

// disarm is called when the person typed something: a Ctrl-C after that is a first one again.
func (i *interrupts) disarm() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.armed, i.quit = time.Time{}, false
}
