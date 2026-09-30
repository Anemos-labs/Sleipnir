package render

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/term"
)

func TestPlainOutputForAPipeIsPlainLines(t *testing.T) {
	var out bytes.Buffer
	r := NewInline(&out, termCaps(10, 4).Plain())
	red := cell.Style{}.Fg(cell.ANSI(1)).With(cell.Bold)
	r.Print(cell.Styled(red, "hello"), txt("a\tb"), txt("a line that is much longer than the ten columns of this terminal"))
	r.SetLive(txts("status", "> input"))
	r.SetCursor(1, 2)
	if out.Len() != 0 {
		t.Fatal("nothing is written before Flush")
	}
	r.Flush()
	r.Resize(5, 2)
	r.SetLive(txts("status 2"))
	r.Print(txt(""))
	r.Flush()
	r.Close()
	want := "hello\na       b\na line that is much longer than the ten columns of this terminal\n\n"
	if out.String() != want {
		t.Errorf("\n got  %q\n want %q", out.String(), want)
	}
}

func TestPlainKeepLivePrintsTheLastRegionOnceAtClose(t *testing.T) {
	var out bytes.Buffer
	r := NewInline(&out, termCaps(40, 4).Plain(), KeepLive())
	r.Print(txt("one"))
	r.SetLive(txts("status: 1"))
	r.Flush()
	r.SetLive(txts("status: 2", "> prompt"))
	r.Flush()
	r.Close()
	if want := "one\nstatus: 2\n> prompt\n"; out.String() != want {
		t.Errorf("%q, want %q", out.String(), want)
	}
}

func TestNoColourOnATerminalKeepsCarriageReturns(t *testing.T) {
	// NO_COLOR on a real terminal: plain output, but the terminal may be in raw mode, where a bare line feed would start the
	// next line under the end of the last. A pipe gets a bare line feed: a CRLF in a log is noise.
	var tty, pipe bytes.Buffer
	c := term.Caps{Color: term.ColorNone, Width: 20, Height: 5}
	NewInlineAndPrint(&tty, c, "a", "b")
	NewInlineAndPrint(&pipe, c.Plain(), "a", "b")
	if tty.String() != "a\r\nb\r\n" {
		t.Errorf("terminal: %q", tty.String())
	}
	if pipe.String() != "a\nb\n" {
		t.Errorf("pipe: %q", pipe.String())
	}
}

// NewInlineAndPrint is a helper of this test file: a renderer that prints the lines and is closed.
func NewInlineAndPrint(w *bytes.Buffer, c term.Caps, lines ...string) {
	r := NewInline(w, c)
	r.Print(txts(lines...)...)
	r.Close()
}

// Synchronized output brackets every frame, and only frames (viii).
func TestSyncOutputBracketsEveryFrame(t *testing.T) {
	c := termCaps(30, 8)
	c.SyncOutput = true
	h := newHarness(t, c, WithBracketedPaste()) // the bridge checks every Write: one begin first, one end last, nothing else
	frames := func() int { return len(h.b.frames) }

	h.r.Print(txt("history"))
	h.r.SetLive(txts("status", "> "))
	h.r.SetCursor(1, 2)
	h.flush() // the first frame: printed text, the region, bracketed paste
	h.r.SetLive(txts("status 2", "> "))
	h.flush() // a one-row update
	h.r.SetCursor(1, 0)
	h.flush() // only the cursor moves
	h.r.SetCursor(-1, 0)
	h.flush() // the cursor is hidden
	h.r.SetLive(txts("one", "two", "three"))
	h.flush() // the region grows
	h.resize(20, 8)
	h.flush() // a resize
	h.r.Print(txt("more"))
	h.flush() // printed text over a region
	h.r.SetLive(nil)
	h.flush() // the region goes
	if n := frames(); n != 8 {
		t.Errorf("%d frames for 8 flushes that each change something", n)
	}
	if h.v.SyncBegins() != frames() || h.v.SyncEnds() != frames() || h.v.SyncDepth() != 0 {
		t.Errorf("begins %d ends %d depth %d for %d frames", h.v.SyncBegins(), h.v.SyncEnds(), h.v.SyncDepth(), frames())
	}
	before := frames()
	h.r.Close() // the last frame: paste off, cursor shown
	if frames() != before+1 {
		t.Errorf("Close writes one frame, got %d", frames()-before)
	}
	if h.v.SyncBegins() != frames() {
		t.Errorf("Close's frame is not bracketed: %d begins for %d frames", h.v.SyncBegins(), frames())
	}
	// A flush that changes nothing is not a frame: no markers either.
	h2 := newHarness(t, c)
	h2.r.SetLive(txts("x"))
	h2.flush()
	n := len(h2.b.frames)
	h2.flush()
	if len(h2.b.frames) != n {
		t.Error("an empty frame was written")
	}
}

func TestNoSynchronizedOutputWhereItIsNotKnownToWork(t *testing.T) {
	h := newHarness(t, termCaps(30, 8)) // SyncOutput false
	h.r.Print(txt("a"))
	h.r.SetLive(txts("b"))
	h.flush()
	h.r.SetLive(txts("c"))
	h.flush()
	h.r.Close()
	if h.v.SyncBegins() != 0 {
		t.Error("mode 2026 was used on a terminal that is not known to have it")
	}
	for _, f := range h.b.frames {
		if bytes.Contains(f, []byte("?2026")) {
			t.Errorf("%q", f)
		}
	}
}

func TestBracketedPaste(t *testing.T) {
	h := newHarness(t, termCaps(30, 8), WithBracketedPaste())
	if h.v.BracketedPaste() {
		t.Fatal("NewInline writes nothing")
	}
	h.r.SetLive(txts("x"))
	h.flush()
	if !h.v.BracketedPaste() {
		t.Error("the first frame turns bracketed paste on")
	}
	n := len(h.b.frames)
	h.r.SetLive(txts("y"))
	h.flush()
	if bytes.Contains(h.b.frames[n], []byte("?2004")) {
		t.Error("the mode is set once, not with every frame")
	}
	h.r.Close()
	if h.v.BracketedPaste() {
		t.Error("Close turns bracketed paste off")
	}

	// Only where the terminal has it, and never for plain output.
	c := termCaps(30, 8)
	c.BracketedPaste = false
	h = newHarness(t, c, WithBracketedPaste())
	h.r.SetLive(txts("x"))
	h.flush()
	if h.v.BracketedPaste() {
		t.Error("bracketed paste on a terminal without it")
	}
	var out bytes.Buffer
	r := NewInline(&out, termCaps(30, 8).Plain(), WithBracketedPaste())
	r.Print(txt("x"))
	r.Close()
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("plain output: %q", out.String())
	}

	// A renderer that was not asked does not touch the mode.
	h = newHarness(t, termCaps(30, 8))
	h.r.SetLive(txts("x"))
	h.flush()
	h.r.Close()
	for _, f := range h.b.frames {
		if bytes.Contains(f, []byte("?2004")) {
			t.Errorf("%q", f)
		}
	}
}

func TestCloseErasesTheRegionAndLeavesTheCursorWhereItWas(t *testing.T) {
	h := newHarness(t, termCaps(20, 8), WithBracketedPaste())
	h.r.Print(txts("history one", "history two")...)
	h.r.SetLive(txts("status", "> input"))
	h.r.SetCursor(1, 3)
	h.flush()
	if err := h.r.Close(); err != nil {
		t.Fatal(err)
	}
	h.wantAll("history one", "history two")
	if x, y, vis := h.v.Cursor(); x != 0 || y != 2 || !vis {
		t.Errorf("cursor (%d,%d) visible %v: it belongs at the start of the line the region was on, shown", x, y, vis)
	}
	if h.v.BracketedPaste() {
		t.Error("bracketed paste is left on")
	}
	// After Close nothing is written, and Close is repeatable.
	n := h.written()
	h.r.Print(txt("late"))
	h.r.SetLive(txts("late"))
	h.r.SetCursor(0, 0)
	h.r.Resize(10, 10)
	if err := h.r.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := h.r.Close(); err != nil {
		t.Fatal(err)
	}
	if h.written() != n {
		t.Errorf("a closed renderer wrote %d bytes", h.written()-n)
	}
}

func TestCloseKeepLiveLeavesTheRegionInTheScrollback(t *testing.T) {
	h := newHarness(t, termCaps(20, 8), KeepLive())
	h.r.Print(txt("history"))
	h.r.SetLive(txts("status", "> input"))
	h.r.SetCursor(0, 2) // the cursor is not on the last row: Close must still end below the region
	h.flush()
	h.r.Close()
	h.wantAll("history", "status", "> input")
	if x, y, vis := h.v.Cursor(); x != 0 || y != 3 || !vis {
		t.Errorf("cursor (%d,%d) visible %v: it belongs on a fresh line below the region", x, y, vis)
	}
}

func TestCloseKeepLiveAtTheBottomOfTheScreenScrolls(t *testing.T) {
	h := newHarness(t, termCaps(20, 4), KeepLive())
	for i := 0; i < 6; i++ {
		h.r.Print(txt(fmt.Sprintf("h%d", i)))
	}
	h.r.SetLive(txts("status", "> input"))
	h.flush()
	h.r.Close()
	h.wantAll("h0", "h1", "h2", "h3", "h4", "h5", "status", "> input")
	if _, y, _ := h.v.Cursor(); y != 3 {
		t.Errorf("the cursor is on the last screen row, on a fresh line: y=%d", y)
	}
}

func TestCloseWritesWhatIsPending(t *testing.T) {
	h := newHarness(t, termCaps(20, 8))
	h.r.Print(txt("never flushed"))
	h.r.SetLive(txts("region"))
	h.r.Close()
	h.wantAll("never flushed") // printed text is kept; the live region is erased
}

func TestCloseWithNoRegionAndNoCursorChangeWritesNothing(t *testing.T) {
	h := newHarness(t, termCaps(20, 8))
	h.r.Print(txt("only printing"))
	h.flush()
	n := h.written()
	h.r.Close()
	if h.written() != n {
		t.Errorf("Close wrote %q", h.b.frames[len(h.b.frames)-1])
	}
	if _, _, vis := h.v.Cursor(); !vis {
		t.Error("a program that only prints must leave the cursor shown")
	}
}

func TestPrintingDoesNotTouchTheCursorWithoutALiveRegion(t *testing.T) {
	h := newHarness(t, termCaps(20, 8))
	h.r.Print(txt("one"))
	h.flush()
	h.r.Print(txt("two"))
	h.flush()
	for _, f := range h.b.frames {
		if bytes.Contains(f, []byte("?25")) {
			t.Errorf("a print-only frame hid or showed the cursor: %q", f)
		}
	}
	// With a region and no input the cursor is hidden; it comes back when the region goes.
	h.r.SetLive(txts("status"))
	h.flush()
	if _, _, vis := h.v.Cursor(); vis {
		t.Error("a status line has no cursor")
	}
	h.r.SetLive(nil)
	h.flush()
	if _, _, vis := h.v.Cursor(); !vis {
		t.Error("the cursor must come back when the region goes")
	}
}

// failingWriter accepts failAfter writes and then fails every one.
type failingWriter struct {
	failAfter int
	writes    int
	err       error
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > f.failAfter {
		return 0, f.err
	}
	return len(p), nil
}

func TestAWriteErrorIsRememberedAndEndsAllOutput(t *testing.T) {
	boom := errors.New("broken pipe")
	for _, plain := range []bool{false, true} {
		w := &failingWriter{failAfter: 1, err: boom}
		c := termCaps(20, 5)
		if plain {
			c = c.Plain()
		}
		r := NewInline(w, c)
		r.Print(txt("one"))
		if err := r.Flush(); err != nil {
			t.Fatalf("plain=%v: the first write succeeds: %v", plain, err)
		}
		r.Print(txt("two"))
		if err := r.Flush(); !errors.Is(err, boom) {
			t.Fatalf("plain=%v: Flush = %v, want the write's error", plain, err)
		}
		writes := w.writes
		for i := 0; i < 5; i++ {
			r.Print(txt("three"))
			r.SetLive(txts("status"))
			if err := r.Flush(); !errors.Is(err, boom) {
				t.Errorf("plain=%v: the error is sticky: %v", plain, err)
			}
		}
		if err := r.Close(); !errors.Is(err, boom) {
			t.Errorf("plain=%v: Close = %v, want the write's error", plain, err)
		}
		if w.writes != writes {
			t.Errorf("plain=%v: %d more writes after the failure", plain, w.writes-writes)
		}
		if n := r.pendLen(); n != 0 {
			t.Errorf("plain=%v: %d lines kept waiting after an error", plain, n)
		}
	}
}

func TestEachFlushIsOneWrite(t *testing.T) {
	h := newHarness(t, termCaps(20, 5))
	h.r.Print(txts("a", "b", "c", "d", "e", "f", "g")...)
	h.r.SetLive(txts("x", "y"))
	h.r.SetCursor(1, 1)
	h.flush()
	if len(h.b.frames) != 1 {
		t.Errorf("a frame is one Write: %d", len(h.b.frames))
	}
	h.resize(10, 5)
	h.r.Print(txt("h"))
	h.r.SetLive(txts("z"))
	h.flush()
	if len(h.b.frames) != 2 {
		t.Errorf("a frame is one Write: %d", len(h.b.frames))
	}
}

// Callers on other goroutines cannot interleave a frame: each Write is a whole frame (the bridge checks that every frame leaves
// the emulator between sequences and synchronized output closed), every printed line arrives exactly once and each goroutine's
// lines in the order it printed them. Run under -race.
func TestConcurrentUse(t *testing.T) {
	c := termCaps(40, 10)
	c.SyncOutput = true
	h := newHarness(t, c)
	const goroutines, each = 8, 40
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				h.r.Print(txt(fmt.Sprintf("g%d-%02d", g, i)))
				h.r.SetLive(txts(fmt.Sprintf("live from %d at %d", g, i), "> input"))
				h.r.SetCursor(1, i%7)
				if i%5 == 0 {
					if err := h.r.Flush(); err != nil {
						t.Error(err)
					}
				}
				h.r.Size()
			}
		}()
	}
	wg.Wait()
	if err := h.r.Close(); err != nil {
		t.Fatal(err)
	}
	last := make([]int, goroutines)
	for i := range last {
		last[i] = -1
	}
	count := 0
	for _, row := range h.v.All() {
		var g, i int
		if n, _ := fmt.Sscanf(row, "g%d-%d", &g, &i); n != 2 || g >= goroutines {
			if row != "" {
				t.Errorf("a row that nobody printed: %q", row)
			}
			continue
		}
		if i != last[g]+1 {
			t.Errorf("goroutine %d: line %d after line %d", g, i, last[g])
		}
		last[g] = i
		count++
	}
	if count != goroutines*each {
		t.Errorf("%d lines arrived, want %d", count, goroutines*each)
	}
}

// waitForGoroutines waits until the process has no more goroutines than base. A goroutine that has returned may still be counted
// for a moment, so this yields until the count settles; the bound is a hang guard, not a timing.
func waitForGoroutines(t *testing.T, base int) {
	t.Helper()
	for i := 0; runtime.NumGoroutine() > base; i++ {
		if i > 1_000_000 {
			t.Fatalf("goroutines outlived the renderer: %d now, %d before", runtime.NumGoroutine(), base)
		}
		runtime.Gosched()
	}
}

// No renderer starts a goroutine, so nothing can outlive Close or Leave.
func TestNoGoroutineOutlivesClose(t *testing.T) {
	use := func() {
		var out bytes.Buffer
		c := termCaps(20, 5)
		c.SyncOutput = true
		r := NewInline(&out, c, WithBracketedPaste(), KeepLive())
		r.Print(txt("a"))
		r.SetLive(txts("b"))
		r.Flush()
		r.Resize(10, 4)
		r.Close()
		s := NewScreen(&out, c)
		s.Enter()
		s.DrawLines(txts("x"))
		s.Leave()
	}
	use()
	base := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		use()
	}
	waitForGoroutines(t, base)
}
