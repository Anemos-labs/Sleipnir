package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Transport moves whole JSON-RPC messages between the client and one server.
// It owns framing, sessions and everything wire-level, and knows nothing about
// MCP: correlation, timeouts, cancellation and the protocol live in Client.
type Transport interface {
	// Start begins delivering incoming messages to h. It is called once,
	// before Send, and must not block.
	Start(h Handler) error
	// Send transmits one message. It may wait for flow control but returns
	// when ctx is done. For HTTP it can take as long as the server takes to
	// answer (a plain JSON response arrives only when the tool is done), so the
	// client runs it beside its timers, not before them; the answer itself always
	// arrives through Handler.Message.
	Send(ctx context.Context, msg []byte) error
	// Close ends the connection and releases everything, killing a child
	// process if there is one. It is safe to call more than once.
	Close() error
}

// Handler receives what a transport reads.
type Handler struct {
	// Message delivers one complete incoming message. The callee owns msg. The
	// stdio transports call it from a single goroutine, in order; the HTTP
	// transports may call it from several at once. It must not block: it runs
	// on the goroutine that drains the server's output.
	Message func(msg []byte)
	// Closed reports that the transport ended on its own: the server exited,
	// hung up, or (HTTP) forgot the session. It is called at most once, never
	// after a local Close, and err is never nil.
	Closed func(err error)
}

// versioned transports are told the negotiated protocol version after the
// handshake (the HTTP transports send it as a header on every request).
type versioned interface{ SetProtocolVersion(v string) }

// listener transports have a server-to-client channel that is separate from
// requests (the streamable HTTP GET stream); it is opened once the handshake
// is done.
type listener interface{ Listen() }

// ender transports can say that their connection is over before Handler.Closed
// has delivered the reason. A child process's transport waits a moment for the
// exit status so the error can say how the server died; in that interval every
// send already fails, and the manager must not hand such a connection to a new
// call as if it were live.
type ender interface{ Ended() bool }

// DefaultMaxMessageBytes bounds one incoming message. Bigger ones are not
// buffered: they stream past a skimmer that recovers the id, so the call they
// answer fails at once with ErrMessageTooLarge instead of exhausting memory or
// hanging until its timeout.
const DefaultMaxMessageBytes = 16 << 20

// codeTooLarge is the JSON-RPC error code of the synthetic response a
// transport makes for an oversized answer. It lies in the implementation
// range (-32000..-32099) and RPCError.Is maps it to ErrMessageTooLarge.
const codeTooLarge = -32090

func tooLargeResponse(id []byte, limit int) []byte {
	return marshalError(id, codeTooLarge, fmt.Sprintf("response exceeds the %d byte message limit", limit))
}

// Is lets errors.Is(err, ErrMessageTooLarge) see through the synthetic error.
func (e *RPCError) Is(target error) bool {
	return target == ErrMessageTooLarge && e.Code == codeTooLarge
}

// Flood limits: text a server writes to stdout that is not a JSON-RPC message
// (a startup banner, stray logging) is skipped, but only up to a point; a
// server that only ever prints garbage is broken or hostile, and one more line
// is never going to be the valid one.
const (
	maxGarbageLines = 10_000
	maxGarbageBytes = 16 << 20
)

var (
	errFlood      = errors.New("server wrote too much output that is not JSON-RPC")
	errWriteStuck = errors.New("server is not reading its input (a write timed out)")
)

// StreamOptions configures NewStreamTransport.
type StreamOptions struct {
	// MaxMessageBytes bounds one incoming message (default DefaultMaxMessageBytes).
	MaxMessageBytes int
	// WriteTimeout is how long a single write may block before the peer is
	// considered wedged (not reading its input) and the transport fails.
	// Default 30s.
	WriteTimeout time.Duration
	// CloseReader, when set, is closed when the transport finishes, to end a
	// blocked read.
	CloseReader io.Closer
	// OnDrop is told about input the transport discarded (oversized or not
	// JSON-RPC). Optional; must not block.
	OnDrop func(reason string)
}

// StreamTransport frames JSON-RPC as newline-delimited JSON over any pair of
// streams: the stdio transport of MCP, and in-process pipes in tests.
//
// Two goroutines serve it. Reads never wait on writes and writes never wait on
// reads, which matters: a server that fills its stdout while we are writing to
// its stdin would otherwise deadlock both ends.
type StreamTransport struct {
	r    io.Reader
	w    io.WriteCloser
	opts StreamOptions
	max  int

	out        chan *outMsg
	done       chan struct{} // closed when the transport is finished
	writerDone chan struct{}
	once       sync.Once
	cause      atomic.Pointer[error]
	h          Handler
	started    atomic.Bool
}

type outMsg struct {
	data []byte
	done chan error
}

// NewStreamTransport returns a transport reading from r and writing to w.
// Closing w signals end of input to the peer (the MCP shutdown signal).
func NewStreamTransport(r io.Reader, w io.WriteCloser, opts StreamOptions) *StreamTransport {
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 30 * time.Second
	}
	return &StreamTransport{
		r: r, w: w, opts: opts, max: opts.MaxMessageBytes,
		out: make(chan *outMsg, 64), done: make(chan struct{}), writerDone: make(chan struct{}),
	}
}

// Start implements Transport.
func (t *StreamTransport) Start(h Handler) error {
	if !t.started.CompareAndSwap(false, true) {
		return errors.New("mcp: transport already started")
	}
	t.h = h
	go t.readLoop()
	go t.writeLoop()
	return nil
}

// Send implements Transport.
func (t *StreamTransport) Send(ctx context.Context, msg []byte) error {
	if bytes.IndexByte(msg, '\n') >= 0 {
		// Framing is one message per line; a raw newline would split it.
		return errors.New("mcp: refusing to send a message containing a newline")
	}
	buf := make([]byte, len(msg)+1)
	copy(buf, msg)
	buf[len(msg)] = '\n'
	m := &outMsg{data: buf, done: make(chan error, 1)}
	select {
	case <-t.done: // never queue behind a finished transport
		return t.closedErr()
	default:
	}
	select {
	case t.out <- m:
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return t.closedErr()
	}
	select {
	case err := <-m.done:
		return err
	case <-ctx.Done():
		// The bytes may still go out; a caller that gave up follows with a
		// cancellation notification, which the FIFO queue keeps behind them.
		return ctx.Err()
	case <-t.done:
		return t.closedErr()
	}
}

// Ended reports whether the stream has finished, for any reason.
func (t *StreamTransport) Ended() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (t *StreamTransport) closedErr() error {
	if c := t.cause.Load(); c != nil {
		return &closedError{cause: *c}
	}
	return &closedError{}
}

// finish ends the transport once. A non-nil cause means it ended on its own
// and is reported to Handler.Closed; a nil cause is a local Close.
func (t *StreamTransport) finish(cause error) {
	first := false
	t.once.Do(func() {
		first = true
		if cause != nil {
			t.cause.Store(&cause)
		}
		close(t.done)
		_ = t.w.Close()
		if t.opts.CloseReader != nil {
			_ = t.opts.CloseReader.Close()
		}
	})
	// Outside the Once: the callback may call Close, which re-enters finish.
	if first && cause != nil && t.h.Closed != nil {
		t.h.Closed(cause)
	}
}

// Close implements Transport. It does not wait for the read goroutine: with no
// CloseReader that may sit in Read until the peer closes its end.
func (t *StreamTransport) Close() error {
	t.finish(nil)
	if t.started.Load() {
		select {
		case <-t.writerDone:
		case <-time.After(time.Second):
		}
	}
	return nil
}

func (t *StreamTransport) drop(reason string) {
	if t.opts.OnDrop != nil {
		t.opts.OnDrop(reason)
	}
}

func (t *StreamTransport) writeLoop() {
	defer close(t.writerDone)
	for {
		select {
		case <-t.done:
			return
		case m := <-t.out:
			// A peer that stops reading would block this write forever; the
			// watchdog turns that into a failed transport (which unblocks the
			// write by closing the pipe) rather than a hung harness.
			wd := time.AfterFunc(t.opts.WriteTimeout, func() { t.finish(errWriteStuck) })
			_, err := t.w.Write(m.data)
			wd.Stop()
			if err != nil {
				// A failed write ends the transport. The sender is told in the same
				// terms as any other send on a finished transport, so that it waits
				// for the owner's account of the end (a child's exit status and last
				// words) instead of reporting a bare "broken pipe".
				cause := fmt.Errorf("writing to server: %w", err)
				m.done <- &closedError{cause: cause}
				t.finish(cause)
				return
			}
			m.done <- nil
		}
	}
}

// readLoop splits the input into lines without ever holding more than
// MaxMessageBytes of one. A line that is over the limit is not kept: it
// streams through a skimmer so that, if it was the answer to a call, that call
// can be failed by id right away.
func (t *StreamTransport) readLoop() {
	br := bufio.NewReaderSize(t.r, 64<<10)
	var (
		acc                  []byte
		skim                 *skimmer
		garbLines, garbBytes int
	)
	deliver := func(line []byte) bool {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return true
		}
		if line[0] != '{' && line[0] != '[' {
			garbLines++
			garbBytes += len(line)
			t.drop("skipped output that is not JSON-RPC")
			return garbLines <= maxGarbageLines && garbBytes <= maxGarbageBytes
		}
		garbLines, garbBytes = 0, 0
		if t.h.Message != nil {
			t.h.Message(bytes.Clone(line))
		}
		return true
	}
	oversize := func() {
		id, hasMethod, ok := skim.result()
		skim = nil
		if ok && id != nil && !hasMethod && t.h.Message != nil {
			t.h.Message(tooLargeResponse(id, t.max))
			return
		}
		t.drop("dropped an oversized message")
	}
	for {
		chunk, err := br.ReadSlice('\n')
		complete := err == nil
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			// End of stream or a read error. Text after the last newline is a
			// final unterminated message.
			switch {
			case skim != nil:
				skim.feed(chunk)
				oversize()
			case len(acc)+len(chunk) > 0 && len(acc)+len(chunk) <= t.max:
				deliver(append(acc, chunk...))
			}
			t.finish(err)
			return
		}
		switch {
		case skim != nil:
			skim.feed(chunk)
			if complete {
				oversize()
				acc = nil
			}
		case len(acc)+len(chunk) > t.max:
			skim = &skimmer{}
			skim.feed(acc)
			skim.feed(chunk)
			acc = nil
			if complete {
				oversize()
			}
		case complete:
			line := chunk
			if len(acc) > 0 {
				acc = append(acc, chunk...)
				line = acc
			}
			if !deliver(line) {
				t.finish(errFlood)
				return
			}
			if cap(acc) > 1<<20 {
				acc = nil // do not pin a big buffer for the connection's life
			} else {
				acc = acc[:0]
			}
		default:
			acc = append(acc, chunk...)
		}
	}
}
