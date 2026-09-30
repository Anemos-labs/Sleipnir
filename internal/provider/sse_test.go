package provider

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func readAll(t *testing.T, r *SSEReader) (events []SSEEvent, err error) {
	t.Helper()
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
}

func TestSSEReaderParsesTheStreamFormat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		events []SSEEvent
	}{
		{"one event", "data: hello\n\n", []SSEEvent{{Data: []byte("hello")}}},
		{"event name and data", "event: message_start\ndata: {\"a\":1}\n\n", []SSEEvent{{Event: "message_start", Data: []byte(`{"a":1}`)}}},
		{"CRLF line ends", "event: x\r\ndata: y\r\n\r\n", []SSEEvent{{Event: "x", Data: []byte("y")}}},
		{"no space after the colon", "data:tight\n\n", []SSEEvent{{Data: []byte("tight")}}},
		{"only one leading space is dropped", "data:  two\n\n", []SSEEvent{{Data: []byte(" two")}}},
		{"multi-line data joins with newlines", "data: a\ndata: b\ndata: c\n\n", []SSEEvent{{Data: []byte("a\nb\nc")}}},
		{"comments and keep-alives are skipped", ": ping\n: HEIMDALL PROCESSING\n\ndata: x\n\n: again\n", []SSEEvent{{Data: []byte("x")}}},
		{"an event without data is skipped", "event: ping\n\ndata: x\n\n", []SSEEvent{{Event: "ping", Data: []byte("x")}}},
		{"unknown fields are ignored", "id: 7\nretry: 100\ndata: x\n\n", []SSEEvent{{Data: []byte("x")}}},
		{"the last event needs no blank line", "data: a\n\ndata: b", []SSEEvent{{Data: []byte("a")}, {Data: []byte("b")}}},
		{"two events", "data: 1\n\ndata: 2\n\n", []SSEEvent{{Data: []byte("1")}, {Data: []byte("2")}}},
		{"empty input", "", nil},
		{"blank lines only", "\n\n\n", nil},
		{"data: [DONE] is just data", "data: [DONE]\n\n", []SSEEvent{{Data: []byte("[DONE]")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readAll(t, NewSSEReader(strings.NewReader(tc.in)))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.events) {
				t.Fatalf("events = %+v, want %+v", got, tc.events)
			}
			for i := range got {
				if got[i].Event != tc.events[i].Event || string(got[i].Data) != string(tc.events[i].Data) {
					t.Errorf("event %d = {%q %q}, want {%q %q}", i, got[i].Event, got[i].Data, tc.events[i].Event, tc.events[i].Data)
				}
			}
		})
	}
}

// Lines and reads of any shape: one byte at a time, and a line far longer than the
// read buffer.
func TestSSEReaderHandlesAwkwardReads(t *testing.T) {
	long := strings.Repeat("x", 300_000) // several read buffers
	in := "data: " + long + "\n\ndata: tail\n\n"
	for name, r := range map[string]io.Reader{
		"whole":        strings.NewReader(in),
		"byte by byte": iotest.OneByteReader(strings.NewReader(in)),
		"half reads":   iotest.HalfReader(strings.NewReader(in)),
		"data err":     iotest.DataErrReader(strings.NewReader(in)),
	} {
		got, err := readAll(t, NewSSEReader(r))
		if err != nil || len(got) != 2 || string(got[0].Data) != long || string(got[1].Data) != "tail" {
			t.Fatalf("%s: %v (%d events)", name, err, len(got))
		}
	}
}

func TestSSEReaderReportsReadErrors(t *testing.T) {
	boom := errors.New("connection reset")
	r := NewSSEReader(io.MultiReader(strings.NewReader("data: a\n\ndata: half"), iotest.ErrReader(boom)))
	if ev, err := r.Next(); err != nil || string(ev.Data) != "a" {
		t.Fatalf("%v %v", ev, err)
	}
	if _, err := r.Next(); !errors.Is(err, boom) {
		t.Fatalf("a read error must surface, not be taken for the end of the stream: %v", err)
	}
}

func TestSSEReaderLimits(t *testing.T) {
	requireLimit := func(t *testing.T, err error, what string) {
		t.Helper()
		pe, ok := AsError(err)
		if !ok || pe.Kind != ErrServer || !pe.Retryable() || !strings.Contains(pe.Message, what) || !strings.Contains(pe.Message, "cancelled") {
			t.Fatalf("want an ErrServer naming %q, got %v", what, err)
		}
	}
	t.Run("a line that never ends", func(t *testing.T) {
		// 10 MiB without a newline: the reader must stop at the limit, not buffer it all.
		src := &countingReader{r: strings.NewReader("data: " + strings.Repeat("x", 10<<20))}
		r := NewSSEReaderLimits(src, StreamLimits{MaxLineBytes: 1 << 20})
		_, err := r.Next()
		requireLimit(t, err, "line length")
		if src.n > 2<<20 {
			t.Errorf("read %d bytes before giving up", src.n)
		}
	})
	t.Run("an event assembled from many data lines", func(t *testing.T) {
		line := "data: " + strings.Repeat("y", 100_000) + "\n"
		r := NewSSEReaderLimits(strings.NewReader(strings.Repeat(line, 20)), StreamLimits{MaxLineBytes: 1 << 20})
		_, err := r.Next()
		requireLimit(t, err, "event size")
	})
	t.Run("total bytes", func(t *testing.T) {
		src := &countingReader{r: strings.NewReader(strings.Repeat(": keep-alive\n", 1_000_000))} // comments only
		r := NewSSEReaderLimits(src, StreamLimits{MaxBytes: 1 << 20})
		_, err := r.Next()
		requireLimit(t, err, "stream size")
		if src.n > 2<<20 {
			t.Errorf("read %d bytes past a %d byte limit", src.n, 1<<20)
		}
	})
	t.Run("events", func(t *testing.T) {
		r := NewSSEReaderLimits(strings.NewReader(strings.Repeat("data: x\n\n", 100)), StreamLimits{MaxEvents: 10})
		got, err := readAll(t, r)
		if len(got) != 10 {
			t.Errorf("delivered %d events before the limit", len(got))
		}
		requireLimit(t, err, "events")
	})
	t.Run("exactly at the limits is fine", func(t *testing.T) {
		in := "data: 12345\n\ndata: 12345\n\n"
		r := NewSSEReaderLimits(strings.NewReader(in), StreamLimits{MaxBytes: int64(len(in)), MaxEvents: 2, MaxLineBytes: len("data: 12345\n")})
		if got, err := readAll(t, r); err != nil || len(got) != 2 {
			t.Fatalf("%v %d", err, len(got))
		}
	})
	t.Run("negative switches a limit off", func(t *testing.T) {
		r := NewSSEReaderLimits(strings.NewReader(strings.Repeat("data: x\n\n", 5000)), StreamLimits{MaxEvents: -1, MaxBytes: -1, MaxLineBytes: -1})
		if got, err := readAll(t, r); err != nil || len(got) != 5000 {
			t.Fatalf("%v %d", err, len(got))
		}
	})
	t.Run("the default limits apply to NewSSEReader", func(t *testing.T) {
		r := NewSSEReader(strings.NewReader("data: " + strings.Repeat("z", DefaultMaxLineBytes+10)))
		_, err := r.Next()
		requireLimit(t, err, "line length")
	})
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestStreamLimitsNormalized(t *testing.T) {
	d := StreamLimits{}.Normalized()
	if d.MaxBytes != DefaultMaxStreamBytes || d.MaxLineBytes != DefaultMaxLineBytes || d.MaxEvents != DefaultMaxEvents ||
		d.MaxTextBytes != DefaultMaxTextBytes || d.MaxToolCalls != DefaultMaxToolCalls || d.MaxToolArgBytes != DefaultMaxToolArgBytes ||
		d.MaxBlocks != DefaultMaxBlocks || d.MaxDuration != DefaultMaxStreamElapsed {
		t.Fatalf("defaults: %+v", d)
	}
	c := StreamLimits{MaxBytes: 5, MaxEvents: 7}.Normalized()
	if c.MaxBytes != 5 || c.MaxEvents != 7 || c.MaxLineBytes != DefaultMaxLineBytes {
		t.Fatalf("explicit values must survive: %+v", c)
	}
	off := StreamLimits{MaxBytes: -1, MaxDuration: -1, MaxTextBytes: -5}.Normalized()
	if off.MaxBytes < 1<<60 || off.MaxDuration < 1<<60 || off.MaxTextBytes < 1<<30 {
		t.Fatalf("negative must mean unlimited: %+v", off)
	}
	if again := d.Normalized(); again != d {
		t.Fatalf("Normalized must be idempotent: %+v vs %+v", again, d)
	}
}

func TestReadCapped(t *testing.T) {
	if b, err := ReadCapped(strings.NewReader("abcde"), 5); err != nil || string(b) != "abcde" {
		t.Fatalf("at the limit: %q %v", b, err)
	}
	_, err := ReadCapped(strings.NewReader("abcdef"), 5)
	if pe, ok := AsError(err); !ok || pe.Kind != ErrServer || !strings.Contains(pe.Message, "response size limit (5 bytes)") {
		t.Fatalf("over the limit: %v", err)
	}
	if b, err := ReadCapped(strings.NewReader("abcdef"), -1); err != nil || string(b) != "abcdef" {
		t.Fatalf("no limit: %q %v", b, err)
	}
	if _, err := ReadCapped(iotest.ErrReader(errors.New("boom")), 10); err == nil || err.Error() != "boom" {
		t.Fatalf("read error: %v", err)
	}
}
