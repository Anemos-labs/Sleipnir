package mcp

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func readEvents(t *testing.T, in string, max int) ([]sseEvent, error) {
	t.Helper()
	r := newSSEReader(strings.NewReader(in), max)
	var out []sseEvent
	for {
		ev, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, *ev)
	}
}

func TestSSEParser(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []sseEvent
	}{
		{"one event", "data: hello\n\n", []sseEvent{{Data: "hello", Retry: -1}}},
		{"crlf", "data: a\r\n\r\ndata: b\r\n\r\n", []sseEvent{{Data: "a", Retry: -1}, {Data: "b", Retry: -1}}},
		{"lone cr", "data: a\r\rdata: b\r\r", []sseEvent{{Data: "a", Retry: -1}, {Data: "b", Retry: -1}}},
		{"mixed endings", "data: a\r\n\ndata: b\n\r\ndata: c\r\r", []sseEvent{{Data: "a", Retry: -1}, {Data: "b", Retry: -1}, {Data: "c", Retry: -1}}},
		{"multi-line data", "data: one\ndata: two\ndata:three\n\n", []sseEvent{{Data: "one\ntwo\nthree", Retry: -1}}},
		{"event type and id", "event: endpoint\nid: 7\ndata: /messages\n\n", []sseEvent{{Event: "endpoint", ID: "7", Data: "/messages", Retry: -1}}},
		{"retry", "retry: 1500\ndata: x\n\n", []sseEvent{{Data: "x", Retry: 1500}}},
		{"bad retry ignored", "retry: soon\ndata: x\n\n", []sseEvent{{Data: "x", Retry: -1}}},
		{"comments and keepalives", ": keep-alive\n\n:another\ndata: x\n\n: trailing\n\n", []sseEvent{{Data: "x", Retry: -1}}},
		{"bom", "\xef\xbb\xbfdata: x\n\n", []sseEvent{{Data: "x", Retry: -1}}},
		{"field without colon is an empty value", "data\n\ndata: y\n\n", []sseEvent{{Data: "", Retry: -1}, {Data: "y", Retry: -1}}},
		{"only one leading space stripped", "data:  two spaces\n\n", []sseEvent{{Data: " two spaces", Retry: -1}}},
		{"unknown fields ignored", "foo: bar\ndata: x\n\n", []sseEvent{{Data: "x", Retry: -1}}},
		{"event without data is not dispatched", "event: ping\n\ndata: x\n\n", []sseEvent{{Data: "x", Retry: -1}}},
		{"unterminated final event is tolerated", "data: last", []sseEvent{{Data: "last", Retry: -1}}},
		{"id with nul ignored", "id: a\x00b\ndata: x\n\n", []sseEvent{{Data: "x", Retry: -1}}},
		{"event type resets", "event: a\ndata: 1\n\ndata: 2\n\n", []sseEvent{{Event: "a", Data: "1", Retry: -1}, {Data: "2", Retry: -1}}},
		{"empty stream", "", nil},
		{"json payload", "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n", []sseEvent{{Event: "message", Data: `{"jsonrpc":"2.0","id":1,"result":{}}`, Retry: -1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readEvents(t, tt.in, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d events %+v, want %d %+v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("event %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSSEParserByteAtATime(t *testing.T) {
	// A CR at the end of a read must wait for a possible LF, whatever the chunking.
	in := "event: message\r\ndata: a\r\n\r\ndata: b\rdata: c\r\r"
	r := newSSEReader(iotest.OneByteReader(strings.NewReader(in)), 1<<20)
	var got []string
	for {
		ev, err := r.Next()
		if err != nil {
			break
		}
		got = append(got, ev.Data)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b\nc" {
		t.Errorf("got %q", got)
	}
}

func TestSSEParserLimits(t *testing.T) {
	if _, err := readEvents(t, "data: "+strings.Repeat("x", 5000)+"\n\n", 1000); !errors.Is(err, ErrMessageTooLarge) {
		t.Errorf("one huge line: %v", err)
	}
	// Many small lines that add up past the limit.
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("data: " + strings.Repeat("y", 50) + "\n")
	}
	sb.WriteString("\n")
	if _, err := readEvents(t, sb.String(), 1000); !errors.Is(err, ErrMessageTooLarge) {
		t.Errorf("event assembled from many lines: %v", err)
	}
	// Below the limit is fine, and the limit applies per event, not per stream.
	var ok strings.Builder
	for i := 0; i < 50; i++ {
		ok.WriteString("data: " + strings.Repeat("z", 400) + "\n\n")
	}
	evs, err := readEvents(t, ok.String(), 1000)
	if err != nil || len(evs) != 50 {
		t.Errorf("%d events, %v", len(evs), err)
	}
}

func TestSSEParserLastEventID(t *testing.T) {
	r := newSSEReader(strings.NewReader("id: 1\ndata: a\n\ndata: b\n\nid: 3\ndata: c\n\n"), 1<<20)
	for i := 0; i < 3; i++ {
		if _, err := r.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if r.lastEventID() != "3" {
		t.Errorf("lastEventID = %q", r.lastEventID())
	}
}

func FuzzSSEParser(f *testing.F) {
	for _, s := range []string{"data: x\n\n", "\r\r\r", "data\n", ":\n", "id:\x00\n", "retry:99999999999999999999\n\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := newSSEReader(strings.NewReader(s), 4096)
		for i := 0; i < len(s)+2; i++ {
			if _, err := r.Next(); err != nil {
				return
			}
		}
		t.Fatal("parser did not terminate")
	})
}
