package render

import (
	"bytes"
	"io"
	"strconv"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// The callers of the renderer flush at most about 15 times a second, so what matters is that a frame is cheap enough to be lost
// in the noise next to a model request and that an idle UI allocates little.

func benchLive(i int) []cell.Line {
	return []cell.Line{
		cell.Join(cell.Styled(cell.Style{}.Fg(cell.Hex("#bb9af7")).With(cell.Bold), "⠹ "), cell.Text("Thinking 12s  ↑1.2k ↓340  $0.012  saved ≈ $0.08")),
		cell.Join(cell.Styled(cell.Style{}.Bg(cell.Hex("#6366f1")), "  G0  "), cell.Styled(cell.Style{}.Bg(cell.Hex("#9ece6a")), " G1 "), cell.Text(" hit "+strconv.Itoa(i%100)+"%")),
		cell.Text(""),
		cell.Join(cell.Styled(cell.Style{}.Fg(cell.Hex("#9ece6a")), "> "), cell.Text("and what does it cost?")),
		cell.Styled(cell.Style{}.With(cell.Dim), "? for shortcuts                      default mode"),
	}
}

// One row of a five-row live region changes at every frame: the spinner tick.
func BenchmarkFlushOneRowChanged(b *testing.B) {
	r := NewInline(io.Discard, termCaps(100, 30))
	r.SetLive(benchLive(0))
	r.SetCursor(3, 24)
	r.Flush()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.SetLive(benchLive(i))
		r.Flush()
	}
}

// Nothing changed since the last frame: what an idle UI costs per tick.
func BenchmarkFlushNothingChanged(b *testing.B) {
	r := NewInline(io.Discard, termCaps(100, 30))
	live := benchLive(7)
	r.SetLive(live)
	r.SetCursor(3, 24)
	r.Flush()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.SetLive(live)
		r.Flush()
	}
}

// Streamed output: a line is printed and the live region is drawn again below it.
func BenchmarkPrintOneLine(b *testing.B) {
	r := NewInline(io.Discard, termCaps(100, 30))
	r.SetLive(benchLive(0))
	line := cell.Join(cell.Styled(cell.Style{}.Fg(cell.Hex("#9ece6a")), "● "), cell.Text("a line of assistant text that is long enough to be typical of what is streamed"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Print(line)
		r.Flush()
	}
}

func BenchmarkScreenFrameOneRowChanged(b *testing.B) {
	s := NewScreen(io.Discard, termCaps(120, 40))
	s.Enter()
	lines := make([]cell.Line, 40)
	for i := range lines {
		lines[i] = cell.Text("a row of the cockpit: agent be-1 backend edit 12.4k tokens 93% hit")
	}
	s.DrawLines(lines)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lines[5] = cell.Text("a row of the cockpit: agent be-1 backend edit " + strconv.Itoa(i) + " tokens")
		s.DrawLines(lines)
	}
}

// Resize arrives from the goroutine that watches SIGWINCH while the UI goroutine draws: nothing may race.
func TestResizeFromAnotherGoroutineIsRaceFree(t *testing.T) {
	var out bytes.Buffer // not safe for concurrent writes: the renderer's one write path makes it so
	c := termCaps(40, 10)
	c.SyncOutput = true
	r := NewInline(&out, c, WithBracketedPaste())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			r.Resize(30+i%20, 10+i%5)
			r.Size()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			r.SetLive(txts("status "+strconv.Itoa(i), "> input"))
			r.SetCursor(1, i%9)
			if i%7 == 0 {
				r.Print(txt("line " + strconv.Itoa(i)))
			}
			if err := r.Flush(); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Error("nothing was written")
	}
}
