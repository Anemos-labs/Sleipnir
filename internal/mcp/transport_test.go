package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// collector receives what a transport delivers.
type collector struct {
	mu     sync.Mutex
	msgs   []string
	drops  []string
	closed chan error
	got    chan struct{}
}

func newCollector() *collector {
	return &collector{closed: make(chan error, 4), got: make(chan struct{}, 1<<16)}
}

func (c *collector) handler() Handler {
	return Handler{
		Message: func(m []byte) {
			c.mu.Lock()
			c.msgs = append(c.msgs, string(m))
			c.mu.Unlock()
			select {
			case c.got <- struct{}{}:
			default:
			}
		},
		Closed: func(err error) { c.closed <- err },
	}
}

func (c *collector) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		if len(c.msgs) >= n {
			out := append([]string(nil), c.msgs...)
			c.mu.Unlock()
			return out
		}
		c.mu.Unlock()
		select {
		case <-c.got:
		case <-deadline:
			c.mu.Lock()
			defer c.mu.Unlock()
			t.Fatalf("got %d messages, want %d: %v", len(c.msgs), n, c.msgs)
		}
	}
}

func (c *collector) dropped() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.drops...)
}

func startStream(t *testing.T, opts StreamOptions) (*StreamTransport, *peer, *collector) {
	t.Helper()
	col := newCollector()
	opts.OnDrop = func(r string) {
		col.mu.Lock()
		col.drops = append(col.drops, r)
		col.mu.Unlock()
	}
	tr, p := pair(t, opts)
	if err := tr.Start(col.handler()); err != nil {
		t.Fatal(err)
	}
	return tr, p, col
}

func TestStreamFraming(t *testing.T) {
	_, p, col := startStream(t, StreamOptions{})
	p.send(`{"a":1}`)
	p.send(``)
	p.send(`   `)
	p.send("{\"b\":2}\r")
	p.send(`  {"c":3}  `)
	p.send(`[{"d":4}]`)
	// A final message with no newline is delivered when the stream ends.
	p.wmu.Lock()
	io.WriteString(p.out, `{"e":5}`)
	p.wmu.Unlock()
	p.close()
	got := col.wait(t, 5)
	want := []string{`{"a":1}`, `{"b":2}`, `{"c":3}`, `[{"d":4}]`, `{"e":5}`}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("msg %d = %q, want %q", i, got[i], want[i])
		}
	}
	select {
	case err := <-col.closed:
		if !errors.Is(err, io.EOF) {
			t.Errorf("Closed(%v), want EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Closed not called at EOF")
	}
}

func TestStreamSendRejectsNewlinesAndFramesOnce(t *testing.T) {
	tr, p, _ := startStream(t, StreamOptions{})
	if err := tr.Send(context.Background(), []byte("{\"a\":\n1}")); err == nil {
		t.Error("a message with a raw newline must be refused: it would split into two frames")
	}
	if err := tr.Send(context.Background(), []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case l := <-p.lines:
		if l != `{"ok":true}` {
			t.Errorf("wire = %q", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing written")
	}
}

func TestStreamConcurrentSendsNeverInterleave(t *testing.T) {
	tr, p, _ := startStream(t, StreamOptions{})
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := fmt.Sprintf(`{"i":%d,"pad":%q}`, i, strings.Repeat("x", 3000))
			if err := tr.Send(context.Background(), []byte(msg)); err != nil {
				t.Errorf("send %d: %v", i, err)
			}
		}(i)
	}
	seen := map[int]bool{}
	for i := 0; i < n; i++ {
		m := p.next()
		var idx int
		if err := json.Unmarshal(m["i"], &idx); err != nil {
			t.Fatalf("interleaved or corrupt frame: %v", m)
		}
		seen[idx] = true
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("got %d distinct frames, want %d", len(seen), n)
	}
}

func TestStreamGarbageIsSkippedButBounded(t *testing.T) {
	_, p, col := startStream(t, StreamOptions{})
	p.send("Server starting on port 8080...")
	p.send("WARNING: something")
	p.send(`{"jsonrpc":"2.0","method":"notifications/x"}`)
	col.wait(t, 1)
	if d := col.dropped(); len(d) != 2 {
		t.Errorf("dropped %d garbage lines, want 2", len(d))
	}

	// A server that only ever prints garbage is cut off.
	go func() {
		for i := 0; i < maxGarbageLines+50; i++ {
			if p.send("still not json") != nil {
				return // cut off, as intended
			}
		}
	}()
	select {
	case err := <-col.closed:
		if !errors.Is(err, errFlood) {
			t.Errorf("Closed(%v), want the flood error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a garbage flood must end the connection")
	}
}

func TestStreamValidMessageResetsGarbageCount(t *testing.T) {
	_, p, col := startStream(t, StreamOptions{})
	for round := 0; round < 3; round++ {
		for i := 0; i < maxGarbageLines-10; i++ {
			p.send("noise")
		}
		p.send(`{"jsonrpc":"2.0","method":"notifications/x"}`)
	}
	col.wait(t, 3)
	select {
	case err := <-col.closed:
		t.Fatalf("closed despite valid messages between the noise: %v", err)
	default:
	}
}

func TestStreamOversizedResponseFailsThatCallByID(t *testing.T) {
	const limit = 64 << 10
	tests := []struct {
		name string
		msg  func(size int) string
		id   string
	}{
		{"id first", func(n int) string {
			return `{"jsonrpc":"2.0","id":5,"result":{"text":"` + strings.Repeat("x", n) + `"}}`
		}, "5"},
		{"id last", func(n int) string {
			return `{"result":{"text":"` + strings.Repeat("x", n) + `"},"jsonrpc":"2.0","id":6}`
		}, "6"},
		{"string id", func(n int) string { return `{"result":{"t":"` + strings.Repeat("x", n) + `"},"id":"seven"}` }, `"seven"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, p, col := startStream(t, StreamOptions{MaxMessageBytes: limit})
			p.send(tt.msg(4 * limit))
			p.send(`{"jsonrpc":"2.0","method":"notifications/after"}`)
			msgs := col.wait(t, 2)
			var env envelope
			if err := json.Unmarshal([]byte(msgs[0]), &env); err != nil || env.Error == nil || strings.TrimSpace(string(env.ID)) != tt.id {
				t.Fatalf("want a synthetic error response for id %s, got %s", tt.id, msgs[0])
			}
			e := &RPCError{Code: env.Error.Code, Message: env.Error.Message}
			if !errors.Is(e, ErrMessageTooLarge) {
				t.Errorf("error %v is not ErrMessageTooLarge", e)
			}
			if !strings.Contains(msgs[1], "notifications/after") {
				t.Errorf("the message after the oversized one was lost: %s", msgs[1])
			}
		})
	}
}

func TestStreamOversizedNonResponsesAreDropped(t *testing.T) {
	const limit = 32 << 10
	_, p, col := startStream(t, StreamOptions{MaxMessageBytes: limit})
	p.send(`{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"` + strings.Repeat("x", 3*limit) + `"}}`)
	p.send(`{"jsonrpc":"2.0","id":9,"method":"sampling/createMessage","params":{"x":"` + strings.Repeat("y", 3*limit) + `"}}`)
	p.send(`[` + strings.Repeat(`{"id":1},`, limit) + `{"id":2}]`)
	p.send(`{"jsonrpc":"2.0","method":"notifications/ok"}`)
	msgs := col.wait(t, 1)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "notifications/ok") {
		t.Fatalf("oversized non-responses must vanish, got %v", msgs)
	}
	if d := col.dropped(); len(d) != 3 {
		t.Errorf("dropped %d, want 3: %v", len(d), d)
	}
}

// A hostile server cannot make the transport buffer what it sends: a 16 MiB
// line against a 64 KiB limit must cost far less than 16 MiB of allocation.
func TestStreamOversizedLineIsNotBuffered(t *testing.T) {
	_, p, col := startStream(t, StreamOptions{MaxMessageBytes: 64 << 10})
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	chunk := []byte(strings.Repeat("y", 1<<20))
	p.wmu.Lock()
	io.WriteString(p.out, `{"result":{"text":"`)
	for i := 0; i < 16; i++ {
		p.out.Write(chunk)
	}
	io.WriteString(p.out, `"},"id":77}`+"\n")
	p.wmu.Unlock()

	msgs := col.wait(t, 1)
	runtime.ReadMemStats(&after)
	if !strings.Contains(msgs[0], `"id":77`) {
		t.Fatalf("no synthetic response for the oversized call: %s", msgs[0])
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 6<<20 {
		t.Errorf("allocated %d MiB while discarding a 16 MiB line", grew>>20)
	}
}

func TestStreamCloseSemantics(t *testing.T) {
	tr, _, col := startStream(t, StreamOptions{})
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	select {
	case err := <-col.closed:
		t.Errorf("Closed must not fire for a local Close, got %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	err := tr.Send(context.Background(), []byte(`{}`))
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close = %v, want ErrClosed", err)
	}
	if err := tr.Start(Handler{}); err == nil {
		t.Error("second Start must fail")
	}
}

func TestStreamPeerHangupIsReportedOnce(t *testing.T) {
	tr, p, col := startStream(t, StreamOptions{})
	p.close()
	select {
	case err := <-col.closed:
		if !errors.Is(err, io.EOF) {
			t.Errorf("cause = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hang-up not reported")
	}
	select {
	case err := <-col.closed:
		t.Errorf("Closed fired twice (second: %v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tr.Send(context.Background(), []byte(`{}`)); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after hang-up = %v", err)
	}
}

// A peer that stops reading its input must not hang the harness: the write
// watchdog fails the transport, which also releases the blocked writer.
func TestStreamWedgedPeerTripsWriteWatchdog(t *testing.T) {
	rr, rw := io.Pipe() // the peer's output: never ends
	defer rw.Close()
	pr, pw := io.Pipe() // the peer's input: nobody reads, so writes block
	defer pr.Close()
	wedged := NewStreamTransport(rr, pw, StreamOptions{WriteTimeout: 150 * time.Millisecond})
	col := newCollector()
	if err := wedged.Start(col.handler()); err != nil {
		t.Fatal(err)
	}
	defer wedged.Close()
	start := time.Now()
	if err := wedged.Send(context.Background(), []byte(`{"x":1}`)); err == nil {
		t.Fatal("Send to a wedged peer must fail")
	}
	select {
	case cause := <-col.closed:
		if !errors.Is(cause, errWriteStuck) {
			t.Errorf("cause = %v, want the write-stuck error", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not fire")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("watchdog too slow")
	}
}

func TestStreamFailedWriteIsAFinishedTransport(t *testing.T) {
	// The peer's input is closed, so the write fails at once. That must read as a
	// transport that ended (errors.Is ErrClosed, with the write error as its
	// cause), like every other send on a finished transport, not as a bare pipe
	// error: the caller then waits for the owner's account of the end.
	rr, rw := io.Pipe()
	defer rw.Close()
	pr, pw := io.Pipe()
	_ = pr.Close() // nobody will ever read: writes fail with io.ErrClosedPipe
	tr := NewStreamTransport(rr, pw, StreamOptions{})
	col := newCollector()
	if err := tr.Start(col.handler()); err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	err := tr.Send(context.Background(), []byte(`{"x":1}`))
	if !errors.Is(err, ErrClosed) || !errors.Is(err, io.ErrClosedPipe) || !strings.Contains(err.Error(), "writing to server") {
		t.Fatalf("Send err = %v", err)
	}
	select {
	case cause := <-col.closed:
		if !errors.Is(cause, io.ErrClosedPipe) {
			t.Errorf("Closed cause = %v", cause)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a failed write must end the transport")
	}
	if !tr.Ended() {
		t.Error("Ended() is false after the transport finished")
	}
}

func TestStreamSendHonoursContext(t *testing.T) {
	rr, rw := io.Pipe()
	defer rw.Close()
	pr, pw := io.Pipe() // nobody reads: writes block
	defer pr.Close()
	tr := NewStreamTransport(rr, pw, StreamOptions{WriteTimeout: time.Minute})
	if err := tr.Start(Handler{}); err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := tr.Send(ctx, []byte(`{"x":1}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Send ignored its context")
	}
}

func TestStreamMessageIsOwnedByTheHandler(t *testing.T) {
	// The handler may keep the slice: later reads must not overwrite it.
	var mu sync.Mutex
	var kept [][]byte
	var n atomic.Int32
	rr, rw := io.Pipe()
	tr := NewStreamTransport(rr, nopWriteCloser{io.Discard}, StreamOptions{})
	if err := tr.Start(Handler{Message: func(m []byte) {
		mu.Lock()
		kept = append(kept, m)
		mu.Unlock()
		n.Add(1)
	}}); err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	for i := 0; i < 50; i++ {
		fmt.Fprintf(rw, "{\"n\":%d,\"pad\":%q}\n", i, strings.Repeat("z", 100))
	}
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 50 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, m := range kept {
		if !strings.HasPrefix(string(m), fmt.Sprintf(`{"n":%d,`, i)) {
			t.Fatalf("message %d was overwritten: %s", i, m)
		}
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
