package main

// The parts of stdinLines and interrupts that the rules in chat_input_test.go do not reach:
// what the owner does at the end of input and with a producer that does not stop, what happens
// when a question is cancelled in the instant a line arrives, and the Ctrl-C state machine with a
// clock the test turns.

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func (l *stdinLines) queued() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.queue)
}

func (l *stdinLines) asking() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asker != nil
}

// answer is Answer for a test that has no question to show.
func (l *stdinLines) answer(ctx context.Context) (string, error) {
	return l.Answer(ctx, func() {})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(inputGuard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v", what, inputGuard)
		}
		time.Sleep(time.Millisecond)
	}
}

// ttyReader is a terminal as the owner sees it: every Read waits for the next thing the person
// does, which is typing a line, or Ctrl-D (a read that returns the end of input and, unlike a
// pipe's, is not the end).
type ttyReader struct{ events chan ttyEvent }

type ttyEvent struct {
	data string
	eof  bool
	err  error
}

func newTTY() *ttyReader { return &ttyReader{events: make(chan ttyEvent)} }

func (r *ttyReader) Read(p []byte) (int, error) {
	ev := <-r.events
	switch {
	case ev.err != nil:
		return 0, ev.err
	case ev.eof:
		return 0, io.EOF
	}
	return copy(p, ev.data), nil
}

func (r *ttyReader) line(s string) { r.events <- ttyEvent{data: s + "\n"} }
func (r *ttyReader) ctrlD()        { r.events <- ttyEvent{eof: true} }

func next(t *testing.T, l *stdinLines) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), inputGuard)
	defer cancel()
	s, err := l.Next(ctx, nil)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next: nothing within %v", inputGuard)
	}
	return s, err
}

// At a terminal the end of input is Ctrl-D: news for whoever reads next, in order with the lines
// around it, and reading goes on. At a pipe or a file it is final and stays.
func TestStdinLinesEndOfInput(t *testing.T) {
	t.Run("a terminal: Ctrl-D ends one read", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, false)
		tty.line("a")
		tty.ctrlD()
		// What is typed after the end of input is not read before the end of input is taken: a
		// terminal that answers every read with the end (one that was hung up) would be read in a loop.
		sent := make(chan struct{})
		go func() { tty.line("b"); close(sent) }()
		for i, want := range []struct {
			text string
			eof  bool
		}{{"a", false}, {"", true}} {
			s, err := next(t, l)
			if (err != nil) != want.eof || s != want.text || (want.eof && !errors.Is(err, io.EOF)) {
				t.Fatalf("read %d: %q, %v; want %+v", i, s, err, want)
			}
		}
		select {
		case <-sent:
		case <-time.After(inputGuard):
			t.Fatal("the reader did not go on after the end of input was taken")
		}
		if s, err := next(t, l); err != nil || s != "b" {
			t.Fatalf("after the end of input: %q, %v", s, err)
		}
	})
	t.Run("a terminal: Ctrl-D at a question is its answer, a refusal, and does not end the chat", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, false)
		res := make(chan error, 1)
		go func() { _, err := l.answer(context.Background()); res <- err }()
		waitFor(t, "the question waiting", l.asking)
		tty.ctrlD()
		if err := <-res; !errors.Is(err, io.EOF) {
			t.Fatalf("the question got %v, want the end of input", err)
		}
		tty.line("after")
		if s, err := next(t, l); err != nil || s != "after" {
			t.Fatalf("the prompt after it: %q, %v; the end of input was taken by the question", s, err)
		}
	})
	t.Run("a pipe: the end stays", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, true)
		tty.line("a")
		tty.ctrlD()
		if s, err := next(t, l); err != nil || s != "a" {
			t.Fatalf("%q, %v", s, err)
		}
		for range 3 {
			if _, err := next(t, l); !errors.Is(err, io.EOF) {
				t.Fatalf("after the end: %v, want it to stay the end", err)
			}
		}
		if _, err := l.answer(context.Background()); !errors.Is(err, io.EOF) {
			t.Fatalf("a question after the end: %v", err)
		}
	})
	t.Run("an error is final, at a terminal too", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, false)
		boom := errors.New("input/output error")
		tty.events <- ttyEvent{err: boom}
		for range 2 {
			if _, err := next(t, l); !errors.Is(err, boom) {
				t.Fatalf("got %v, want %v", err, boom)
			}
		}
	})
	t.Run("a question that is waiting is told when the input ends", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, true)
		res := make(chan error, 1)
		go func() { _, err := l.answer(context.Background()); res <- err }()
		waitFor(t, "the question waiting", l.asking)
		tty.ctrlD()
		if err := <-res; !errors.Is(err, io.EOF) {
			t.Fatalf("got %v", err)
		}
	})
}

// endless is a producer that never stops: every Read is another line.
type endless struct{ reads atomic.Int64 }

func (e *endless) Read(p []byte) (int, error) {
	e.reads.Add(1)
	return copy(p, "line\n"), nil
}

// The owner reads ahead a bounded number of lines: `yes | sleipnir chat` waits in its pipe and
// does not fill memory. Taking a line lets it read one more.
func TestStdinLinesReadsAheadOnlyAsFarAsItMay(t *testing.T) {
	src := &endless{}
	l := newStdinLines(src, true)
	waitFor(t, "a full queue", func() bool { return l.queued() == maxQueued })
	// One Read per line, and the reader does not read when the queue is full: it cannot have
	// read more than fits. (Not a measure of how long it stays parked: that the next read
	// happens only when a line is taken is what comes next.)
	if n := src.reads.Load(); n != maxQueued {
		t.Fatalf("%d reads with %d lines queued", n, maxQueued)
	}
	if _, err := next(t, l); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the queue full again", func() bool { return l.queued() == maxQueued })
	if n := src.reads.Load(); n != maxQueued+1 {
		t.Fatalf("%d reads after one line was taken, want %d: the reader went on without room, or did not go on", n, maxQueued+1)
	}
	// A question needs the reader whatever else is waiting: its answer is read past a full queue.
	tty := newTTY()
	l = newStdinLines(tty, true)
	for i := range maxQueued {
		tty.line(fmt.Sprint("ahead ", i))
	}
	waitFor(t, "a full queue", func() bool { return l.queued() == maxQueued })
	res := make(chan string, 1)
	go func() { s, _ := l.answer(context.Background()); res <- s }()
	waitFor(t, "the question waiting", l.asking)
	tty.line("y")
	select {
	case s := <-res:
		if s != "y" {
			t.Fatalf("the answer was %q", s)
		}
	case <-time.After(inputGuard):
		t.Fatal("a question was not answered while the queue was full")
	}
}

// A question that is cancelled in the instant a line arrives, which is what Ctrl-C does to a
// person who types as it is pressed, loses nothing: the line is the answer, or it is the next
// goal, never neither and never both, and lines stay in the order they were typed.
func TestStdinLinesACancelledQuestionLosesNoLine(t *testing.T) {
	tty := newTTY()
	l := newStdinLines(tty, true)
	const rounds = 300
	answered, requeued := 0, 0
	for i := range rounds {
		ctx, cancel := context.WithCancel(context.Background())
		type result struct {
			s   string
			err error
		}
		res := make(chan result, 1)
		go func() { s, err := l.answer(ctx); res <- result{s, err} }()
		waitFor(t, "the question waiting", l.asking)

		line := fmt.Sprint("line ", i)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); tty.line(line) }()
		go func() { defer wg.Done(); cancel() }()
		r := <-res
		wg.Wait()

		got := r.s
		if r.err != nil {
			if !errors.Is(r.err, context.Canceled) {
				t.Fatalf("round %d: %v", i, r.err)
			}
			requeued++
			s, err := next(t, l) // not lost: it is the next goal
			if err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
			got = s
		} else {
			answered++
		}
		if got != line {
			t.Fatalf("round %d: the line %q came out as %q", i, line, got)
		}
		cancel()
	}
	t.Logf("%d answered, %d went back to the prompt", answered, requeued)
}

// A script that waits for the question and answers at once is not early for it: the answer is
// read after the question began to wait, however soon after it was shown. (Answering from inside
// show is the extreme of that: the answer is read before show returns. A question that began to
// wait only after it was shown would take it for a line typed ahead, and wait for another for
// ever. It did, under load, in the end-to-end tests.)
func TestStdinLinesAnAnswerTypedAsTheQuestionIsShownIsNotLost(t *testing.T) {
	tty := newTTY()
	l := newStdinLines(tty, true)
	shown := false
	show := func() {
		shown = true
		tty.line("y")
		waitFor(t, "the answer to be read", func() bool {
			l.mu.Lock()
			defer l.mu.Unlock()
			return l.seq >= 1 // read, and either given to the question or queued
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), inputGuard)
	defer cancel()
	got, err := l.Answer(ctx, show)
	if err != nil || got != "y" {
		t.Fatalf("%q, %v: the answer typed as the question was shown was taken for a line typed ahead", got, err)
	}
	if !shown {
		t.Error("the question was not shown")
	}
	if n := l.queued(); n != 0 {
		t.Errorf("%d lines queued: the answer must not also be a goal", n)
	}
}

// A question that stops waiting gives back what reached it in that instant, in its place. (The
// race that makes this happen is too narrow to hit on purpose, so the two states are made by hand.)
func TestStdinLinesAnAbandonedQuestionGivesBackWhatReachedIt(t *testing.T) {
	newOwner := func() *stdinLines {
		return &stdinLines{ready: make(chan struct{}, 1), room: make(chan struct{}, 1)}
	}
	t.Run("nothing had arrived", func(t *testing.T) {
		l := newOwner()
		a := &answerWait{done: make(chan struct{})}
		l.asker = a
		l.abandon(a)
		l.deliver(lineItem{text: "later"})
		if l.asker != nil || l.queued() != 1 {
			t.Fatalf("asker %v, %d queued: a line that arrives after the question was given up is for the prompt", l.asker, l.queued())
		}
	})
	t.Run("a line had arrived", func(t *testing.T) {
		l := newOwner()
		a := &answerWait{done: make(chan struct{})}
		l.asker = a
		l.deliver(lineItem{text: "first"})  // the question's
		l.deliver(lineItem{text: "second"}) // not: the question had its answer
		if a.item == nil || a.item.text != "first" || l.queued() != 1 {
			t.Fatalf("question got %+v, %d queued", a.item, l.queued())
		}
		l.abandon(a)
		var got []string
		for l.queued() > 0 {
			s, _ := next(t, l)
			got = append(got, s)
		}
		if strings.Join(got, ",") != "first,second" {
			t.Fatalf("the prompt got %q, want the line back in its place", got)
		}
	})
	t.Run("an end of input had arrived", func(t *testing.T) {
		l := newOwner()
		a := &answerWait{done: make(chan struct{})}
		l.asker = a
		l.deliver(lineItem{err: io.EOF})
		l.abandon(a)
		if _, err := next(t, l); !errors.Is(err, io.EOF) {
			t.Fatalf("the prompt got %v, want the end of input that reached the question", err)
		}
		if l.eofs != 0 {
			t.Fatalf("%d ends of input counted after it was taken", l.eofs)
		}
	})
}

// A line that comes back goes where it was typed, before the lines that came after it; an end of
// input that comes back is counted again.
func TestStdinLinesEnqueueKeepsArrivalOrder(t *testing.T) {
	l := &stdinLines{ready: make(chan struct{}, 1), room: make(chan struct{}, 1)}
	l.mu.Lock()
	l.enqueue(lineItem{seq: 3, text: "three"})
	l.enqueue(lineItem{seq: 5, text: "five"})
	l.enqueue(lineItem{seq: 1, text: "one"}) // comes back
	l.enqueue(lineItem{seq: 4, err: io.EOF}) // and so does an end of input
	l.enqueue(lineItem{seq: 6, text: "six"})
	var order []string
	for _, it := range l.queue {
		order = append(order, fmt.Sprintf("%d:%s", it.seq, it.text))
	}
	eofs := l.eofs
	l.mu.Unlock()
	if got, want := strings.Join(order, " "), "1:one 3:three 4: 5:five 6:six"; got != want {
		t.Errorf("the queue is %q, want %q", got, want)
	}
	if eofs != 1 {
		t.Errorf("%d ends of input counted, want 1", eofs)
	}
}

// readInput continues a line that ends in a backslash, and Ctrl-C at the prompt drops what was
// typed so far.
func TestReadInput(t *testing.T) {
	t.Run("continuation", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, true)
		tty.line(`first \`)
		tty.line(`second \`)
		tty.line("third")
		got, err := readInput(context.Background(), l, nil)
		if err != nil || got != "first \nsecond \nthird" {
			t.Fatalf("%q, %v", got, err)
		}
	})
	t.Run("Ctrl-C drops the continued line", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, true)
		abort := make(chan struct{}, 1)
		tty.line(`half of a line \`)
		waitFor(t, "the first part queued", func() bool { return l.queued() == 1 })
		res := make(chan error, 1)
		go func() { _, err := readInput(context.Background(), l, abort); res <- err }()
		// readInput has the first part and waits for the rest; Ctrl-C ends it.
		waitFor(t, "the first part taken", func() bool { return l.queued() == 0 })
		abort <- struct{}{}
		if err := <-res; !errors.Is(err, errInterrupted) {
			t.Fatalf("got %v", err)
		}
		tty.line("fresh")
		if got, err := readInput(context.Background(), l, nil); err != nil || got != "fresh" {
			t.Fatalf("the next read: %q, %v; the dropped part must not come back", got, err)
		}
	})
	t.Run("the end of input in the middle of a line", func(t *testing.T) {
		tty := newTTY()
		l := newStdinLines(tty, true)
		tty.line(`unfinished \`)
		tty.ctrlD()
		if got, err := readInput(context.Background(), l, nil); !errors.Is(err, io.EOF) {
			t.Fatalf("%q, %v; want the end of input (the unfinished line is dropped, as it always was)", got, err)
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		l := newStdinLines(newTTY(), true)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := readInput(ctx, l, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

// ---- Ctrl-C ----

// clock is a time that only moves when the test says.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestInterrupts() (*interrupts, *clock) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	i := newInterrupts()
	i.now = c.now
	return i, c
}

// Ctrl-C while something runs cancels that and nothing else.
func TestInterruptsCancelWhatRuns(t *testing.T) {
	i, _ := newTestInterrupts()
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	ran := false
	i.run(parent, func(ctx context.Context) {
		if ctx.Err() != nil {
			t.Fatal("cancelled before anything was pressed")
		}
		i.deliver()
		if ctx.Err() == nil {
			t.Error("Ctrl-C did not cancel what was running")
		}
		ran = true
	})
	if !ran {
		t.Fatal("f did not run")
	}
	if parent.Err() != nil {
		t.Error("Ctrl-C cancelled the parent: the session would have ended with the turn")
	}
	select {
	case <-i.idle:
		t.Error("a press during a run woke the prompt")
	default:
	}
	// And the prompt is the prompt's again: a press is its.
	i.deliver()
	select {
	case <-i.idle:
	default:
		t.Error("a press at the prompt did not wake it")
	}
}

// A press made at the prompt, and not yet looked at, is not meant for what starts next.
func TestInterruptsDropAStalePress(t *testing.T) {
	i, _ := newTestInterrupts()
	i.deliver()
	i.run(context.Background(), func(ctx context.Context) {
		if ctx.Err() != nil {
			t.Error("a press from before cancelled what started after it")
		}
	})
	select {
	case <-i.idle:
		t.Error("the press from before is still there")
	default:
	}
}

// At the prompt a first Ctrl-C is a reminder and a second one, soon after and with nothing typed in
// between, quits.
func TestInterruptsAtThePrompt(t *testing.T) {
	type step struct {
		do   string // press, advance, type, turn, look
		d    time.Duration
		want bool // look: does the session end?
	}
	press, look := step{do: "press"}, func(quit bool) step { return step{do: "look", want: quit} }
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"one press is a reminder", []step{press, look(false)}},
		{"two presses quit", []step{press, look(false), press, look(true)}},
		{"two presses before the prompt looked are two", []step{press, press, look(true)}},
		{"the window", []step{press, look(false), {do: "advance", d: quitWindow}, press, look(true)}},
		{"a second press too late is a first", []step{press, look(false), {do: "advance", d: quitWindow + time.Nanosecond}, press, look(false), press, look(true)}},
		{"typing in between", []step{press, look(false), {do: "type"}, press, look(false)}},
		{"a turn in between", []step{press, look(false), {do: "turn"}, press, look(false)}},
		{"after quitting, a press is a first", []step{press, press, look(true), press, look(false)}},
		{"every other press of many", []step{press, look(false), press, look(true), press, look(false), press, look(true)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, clk := newTestInterrupts()
			for n, s := range tc.steps {
				switch s.do {
				case "press":
					i.deliver()
				case "advance":
					clk.advance(s.d)
				case "type":
					i.disarm()
				case "turn":
					i.run(context.Background(), func(context.Context) {})
				case "look":
					select {
					case <-i.idle:
					default:
						t.Fatalf("step %d: the prompt was not woken", n)
					}
					if got := i.pressed(); got != s.want {
						t.Fatalf("step %d: quit = %v, want %v", n, got, s.want)
					}
				}
			}
		})
	}
}

// SIGINT is registered in one place, and it is not where the turn runs: a second registration
// (main's, for the whole process; each turn's) is what made one Ctrl-C end the session, because a
// signal goes to every channel that asked for it. This reads the source.
func TestSIGINTIsRegisteredInOnePlaceForChat(t *testing.T) {
	calls := map[string][]string{} // file -> "signal.Notify" and the like
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "signal" && strings.HasPrefix(sel.Sel.Name, "Notify") {
						calls[f] = append(calls[f], "signal."+sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
	for f, c := range calls {
		switch f {
		case "main.go": // the process's context: SIGTERM, and SIGINT for the commands that do not own it
			if len(c) != 1 || c[0] != "signal.NotifyContext" {
				t.Errorf("main.go registers %v, want one signal.NotifyContext", c)
			}
		case "chat_input.go": // chat's Ctrl-C
			if len(c) != 1 || c[0] != "signal.Notify" {
				t.Errorf("chat_input.go registers %v, want one signal.Notify", c)
			}
		default:
			t.Errorf("%s registers for signals (%v): chat has one place for Ctrl-C (interrupts), and main has the process's", f, c)
		}
	}
	if len(calls["chat_input.go"]) != 1 || len(calls["main.go"]) != 1 {
		t.Errorf("registrations found: %v", calls)
	}
	if !ownsInterrupt["chat"] {
		t.Error("chat does not own Ctrl-C: main would cancel the process with the first one")
	}
}
