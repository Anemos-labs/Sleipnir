package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitDone waits for ctx to end, failing the test if it takes longer than limit.
func waitDone(t *testing.T, ctx context.Context, limit time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(limit):
		t.Fatalf("the watchdog did not fire within %v", limit)
	}
}

func TestWatchdogFirstByteDeadlineIsArmedFromTheStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, 60*time.Millisecond, time.Hour, 0, nil)
	start := time.Now()
	wd.Start()
	defer wd.Stop()
	waitDone(t, ctx, 3*time.Second)
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("fired after %v, before its deadline", d)
	}
	if wd.Why() != WatchFirstByte || wd.Received() {
		t.Fatalf("why = %v, received = %v", wd.Why(), wd.Received())
	}
	e := wd.Failure(context.Canceled)
	if e == nil || e.Kind != ErrTimeout || !e.Retryable() || !strings.Contains(e.Message, "no response from the server within 60ms") {
		t.Fatalf("failure = %v", e)
	}
	if !errors.Is(e, context.Canceled) {
		t.Error("the cause must stay reachable")
	}
}

func TestWatchdogSwitchesToTheIdleDeadlineAtTheFirstByte(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A generous first-byte deadline and a short idle one: the stream starts late but
	// is then held to the idle deadline.
	wd := NewWatchdog(cancel, 5*time.Second, 80*time.Millisecond, 0, nil)
	wd.Start()
	defer wd.Stop()
	rd := wd.Reader(strings.NewReader("x"))
	time.Sleep(100 * time.Millisecond) // longer than idle, shorter than first: not fired yet
	if wd.Why() != WatchNone {
		t.Fatalf("the idle deadline applied before the first byte: %v", wd.Why())
	}
	buf := make([]byte, 1)
	if n, _ := rd.Read(buf); n != 1 {
		t.Fatal("read")
	}
	if !wd.Received() {
		t.Fatal("the first byte was not noticed")
	}
	waitDone(t, ctx, 3*time.Second)
	if wd.Why() != WatchIdle {
		t.Fatalf("why = %v, want idle", wd.Why())
	}
	if e := wd.Failure(nil); e == nil || !strings.Contains(e.Message, "no data from the server for 80ms") || e.NoRetry {
		t.Fatalf("failure = %v", e)
	}
}

func TestWatchdogEveryReadRestartsTheIdleDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, time.Second, 120*time.Millisecond, 0, nil)
	wd.Start()
	defer wd.Stop()
	pr, pw := io.Pipe()
	rd := wd.Reader(pr)
	go func() {
		for i := 0; i < 12; i++ { // 12 x 40ms = 480ms of trickle, four times the idle deadline
			time.Sleep(40 * time.Millisecond)
			pw.Write([]byte("."))
		}
		pw.Close()
	}()
	if _, err := io.ReadAll(rd); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || wd.Why() != WatchNone {
		t.Fatalf("a slow but live stream was cut: %v", wd.Why())
	}
}

func TestWatchdogTotalDeadlineStopsATrickle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, time.Second, time.Second, 150*time.Millisecond, nil)
	wd.Start()
	defer wd.Stop()
	pr, pw := io.Pipe()
	defer pw.Close()
	rd := wd.Reader(pr)
	go func() {
		for ctx.Err() == nil {
			pw.Write([]byte("."))
			time.Sleep(20 * time.Millisecond)
		}
	}()
	go io.Copy(io.Discard, rd)
	waitDone(t, ctx, 3*time.Second)
	if wd.Why() != WatchTotal {
		t.Fatalf("why = %v", wd.Why())
	}
	if e := wd.Failure(nil); e == nil || e.Kind != ErrTimeout || !strings.Contains(e.Message, "still running after 150ms") {
		t.Fatalf("failure = %v", e)
	}
}

func TestWatchdogStopDisarms(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, 40*time.Millisecond, 40*time.Millisecond, 40*time.Millisecond, nil)
	wd.Start()
	wd.Stop()
	wd.Stop() // idempotent
	time.Sleep(120 * time.Millisecond)
	if ctx.Err() != nil || wd.Why() != WatchNone || wd.Failure(nil) != nil {
		t.Fatal("a stopped watchdog fired")
	}
	// Start after Stop stays disarmed.
	wd.Start()
	time.Sleep(80 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("Start after Stop re-armed the watchdog")
	}
}

func TestWatchdogWithoutFailureIsNil(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, time.Hour, time.Hour, 0, nil)
	wd.Start()
	defer wd.Stop()
	if wd.Failure(errors.New("x")) != nil || wd.Why() != WatchNone {
		t.Fatal("Failure must be nil until a deadline fires")
	}
}

// A request that gets no response twice is not retried a third time; a request that
// got a byte in between starts counting again.
func TestWatchdogCountsSilentAttemptsOfOneRequest(t *testing.T) {
	fire := func(req *Request) *Error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		wd := NewWatchdog(cancel, 20*time.Millisecond, time.Hour, 0, req)
		wd.Start()
		defer wd.Stop()
		waitDone(t, ctx, 3*time.Second)
		return wd.Failure(ctx.Err())
	}
	req := &Request{}
	first := fire(req)
	if first == nil || first.NoRetry || !first.Retryable() || first.Kind != ErrTimeout {
		t.Fatalf("the first silence must be retryable: %+v", first)
	}
	second := fire(req)
	if second == nil || !second.NoRetry || second.Retryable() || second.Kind != ErrTimeout {
		t.Fatalf("the second silence must not be retried: %+v", second)
	}
	if !strings.Contains(second.Message, "2 attempts of this request got no response") {
		t.Errorf("the message must say why: %q", second.Message)
	}

	// A different request starts from zero.
	if e := fire(&Request{}); e == nil || e.NoRetry {
		t.Fatalf("a new request inherited the count: %+v", e)
	}

	// A byte in between resets the count for this request.
	req2 := &Request{}
	if e := fire(req2); e == nil || e.NoRetry {
		t.Fatalf("%+v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wd := NewWatchdog(cancel, time.Second, time.Second, 0, req2)
	wd.Start()
	if _, err := io.ReadAll(wd.Reader(strings.NewReader("a response"))); err != nil {
		t.Fatal(err)
	}
	wd.Stop() // the exchange ended normally after a byte arrived
	cancel()
	_ = ctx
	if e := fire(req2); e == nil || e.NoRetry {
		t.Fatalf("an attempt that got a response must reset the silence count: %+v", e)
	}

	// Silence after the first byte is an idle timeout, which is not counted.
	req3 := &Request{}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		wd := NewWatchdog(cancel, time.Second, 20*time.Millisecond, 0, req3)
		wd.Start()
		wd.Reader(strings.NewReader("x")).Read(make([]byte, 1))
		waitDone(t, ctx, 3*time.Second)
		e := wd.Failure(nil)
		wd.Stop()
		if e == nil || e.NoRetry || !strings.Contains(e.Message, "no data") {
			t.Fatalf("idle timeout %d: %+v", i, e)
		}
	}
}

// Progress and the timers race by design; run them against each other.
func TestWatchdogIsRaceFree(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wd := NewWatchdog(cancel, 5*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond, &Request{})
			wd.Start()
			rd := wd.Reader(&slowReader{})
			buf := make([]byte, 8)
			for ctx.Err() == nil {
				rd.Read(buf)
				wd.Why()
				wd.Received()
			}
			wd.Failure(ctx.Err())
			wd.Stop()
		}()
	}
	wg.Wait()
}

type slowReader struct{}

func (slowReader) Read(p []byte) (int, error) {
	time.Sleep(2 * time.Millisecond)
	return 1, nil
}

// keepAliveReader is a stream that sends a keep-alive comment every tick, and the lines of content in between when told to.
type keepAliveReader struct {
	mu      sync.Mutex
	content []string
	tick    time.Duration
}

func (k *keepAliveReader) Read(p []byte) (int, error) {
	time.Sleep(k.tick)
	k.mu.Lock()
	line := ": keep-alive\n"
	if len(k.content) > 0 {
		line, k.content = k.content[0], k.content[1:]
	}
	k.mu.Unlock()
	return copy(p, line), nil
}

func (k *keepAliveReader) say(lines ...string) {
	k.mu.Lock()
	k.content = append(k.content, lines...)
	k.mu.Unlock()
}

// drain reads the stream until ctx ends.
func drain(ctx context.Context, r io.Reader) {
	buf := make([]byte, 64)
	for ctx.Err() == nil {
		if _, err := r.Read(buf); err != nil {
			return
		}
	}
}

// A stream that carries nothing but keep-alive comments outlives the first-byte and idle deadlines (the connection is alive) and
// is ended by the content deadline: a gateway whose upstream hung sent ": keep-alive" for twenty minutes, and the request was
// cut only by the swarm's stuck-worker watchdog.
func TestWatchdogContentDeadlineEndsAStreamOfKeepAlives(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, time.Hour, time.Hour, 0, nil).WithContent(150 * time.Millisecond)
	start := time.Now()
	wd.Start()
	defer wd.Stop()
	go drain(ctx, wd.Reader(&keepAliveReader{tick: 10 * time.Millisecond}))
	waitDone(t, ctx, 3*time.Second)
	if d := time.Since(start); d < 140*time.Millisecond {
		t.Errorf("fired after %v, before its deadline", d)
	}
	if wd.Why() != WatchContent || !wd.Received() {
		t.Fatalf("why = %v, received = %v", wd.Why(), wd.Received())
	}
	e := wd.Failure(context.Canceled)
	if e == nil || e.Kind != ErrTimeout || !e.Retryable() || !strings.Contains(e.Message, "nothing but keep-alives from the server for 150ms") {
		t.Fatalf("failure = %v", e)
	}
}

// A content line restarts the content deadline, wherever it falls in a read, and a line that spans two reads counts once it
// begins; blank lines and comments do not.
func TestWatchdogContentLinesRestartTheContentDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, time.Hour, time.Hour, 0, nil).WithContent(150 * time.Millisecond)
	wd.Start()
	defer wd.Stop()
	k := &keepAliveReader{tick: 10 * time.Millisecond}
	go drain(ctx, wd.Reader(k))
	for i := 0; i < 4; i++ {
		time.Sleep(100 * time.Millisecond)
		switch i % 2 {
		case 0:
			k.say("data: {\"delta\":\"x\"}\n")
		default:
			k.say("data: {\"del", "ta\":\"y\"}\n\n") // one line over two reads, then a blank line
		}
	}
	if wd.Why() != WatchNone {
		t.Fatalf("the content deadline fired although content kept coming: %v", wd.Why())
	}
	waitDone(t, ctx, 3*time.Second) // nothing more is said: the deadline fires now
	if wd.Why() != WatchContent {
		t.Fatalf("why = %v", wd.Why())
	}
}

// Without WithContent (a non-streaming call) keep-alives are never cut by a content deadline.
func TestWatchdogWithoutContentDeadlineLeavesKeepAlivesAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wd := NewWatchdog(cancel, time.Hour, time.Hour, 0, nil)
	wd.Start()
	defer wd.Stop()
	go drain(ctx, wd.Reader(&keepAliveReader{tick: 10 * time.Millisecond}))
	time.Sleep(200 * time.Millisecond)
	if wd.Why() != WatchNone {
		t.Fatalf("fired: %v", wd.Why())
	}
}

func TestWatchReaderTellsContentFromComments(t *testing.T) {
	cases := []struct {
		in   []string
		want bool
	}{
		{[]string{": keep-alive\n", ": ping\n\n"}, false},
		{[]string{"\n\r\n"}, false},
		{[]string{"data: x\n"}, true},
		{[]string{": a comment\n", "d"}, true},                 // a line begins with content
		{[]string{": a long comment that ", "spans\n"}, false}, // a comment that spans two reads
		{[]string{"event: ping\n"}, true},                      // an SSE event is content to this reader; the clients decide what it means
	}
	for _, c := range cases {
		a := &watchReader{lineStart: true}
		got := false
		for _, s := range c.in {
			got = a.sawContent([]byte(s)) || got
		}
		if got != c.want {
			t.Errorf("sawContent(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
