package input

import (
	"strings"
	"testing"
)

func benchEditor(text string) *Editor {
	e := NewEditor(Options{})
	e.SetText(text)
	return e
}

func BenchmarkViewShort(b *testing.B) {
	e := benchEditor("How do I make the retry logic in the provider adapter respect the Retry-After header?")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.View(80)
	}
}

func BenchmarkViewLongMultiline(b *testing.B) {
	e := benchEditor(strings.Repeat("func main() { fmt.Println(\"hello, world\") } // a line of code\n", 150)) // about 10 KB
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.View(100)
	}
}

func BenchmarkHandleTyping(b *testing.B) {
	e := benchEditor(strings.Repeat("some text ", 100))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.Handle(RuneKey('x', 0))
		e.Handle(SpecialKey(Backspace, 0))
	}
}

// Typing at the end of a long line must cost what it costs in a short one: every key looks only a few runes back.
func BenchmarkHandleTypingInALongLine(b *testing.B) {
	e := benchEditor(strings.Repeat("some text ", 6000)) // 60,000 runes on one line
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.Handle(RuneKey('x', 0))
		e.Handle(SpecialKey(Backspace, 0))
	}
}

func BenchmarkDecode(b *testing.B) {
	in := []byte(strings.Repeat("hello \x1b[1;5C world \x1b[A\x1b[200~pasted\x1b[201~ é中", 50))
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	var d Decoder
	for i := 0; i < b.N; i++ {
		d.Feed(in)
	}
}
