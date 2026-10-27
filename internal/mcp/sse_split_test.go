package mcp

import (
	"io"
	"strings"
	"testing"
)

// countingReader hands out its data a few bytes at a time.
type countingReader struct {
	s     string
	chunk int
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.s == "" {
		return 0, io.EOF
	}
	n := min(c.chunk, len(p), len(c.s))
	copy(p, c.s[:n])
	c.s = c.s[n:]
	return n, nil
}

// A long line that arrives in tiny reads is scanned once: the splitter does not look at its start again on each read.
func TestSSESplitterScansEachByteOnce(t *testing.T) {
	l := &lineSplitter{}
	data := []byte(strings.Repeat("x", 1000))
	looked := 0
	for n := 1; n <= len(data); n++ {
		before := l.seen
		if adv, _, _ := l.split(data[:n], false); adv != 0 {
			t.Fatalf("a line without an end was split at %d", adv)
		}
		looked += n - before
	}
	if looked != len(data) {
		t.Fatalf("looked at %d bytes for a %d-byte line", looked, len(data))
	}
	if adv, tok, _ := l.split(append(data, '\r', '\n'), false); adv != len(data)+2 || len(tok) != len(data) {
		t.Fatalf("adv %d tok %d", adv, len(tok))
	}
}

// Lines split across reads keep their CR, LF and CRLF endings.
func TestSSEReaderAcrossSmallReads(t *testing.T) {
	in := "data: one\r\n\r\ndata: two\r\rdata: three\n\n"
	r := newSSEReader(&countingReader{s: in, chunk: 1}, 1<<20)
	var got []string
	for {
		ev, err := r.Next()
		if err != nil {
			break
		}
		got = append(got, ev.Data)
	}
	if strings.Join(got, "|") != "one|two|three" {
		t.Fatalf("events = %q", got)
	}
}
