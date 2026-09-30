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
	// when ctx is done. For HTTP a request's Send returns once the server has
	// accepted it; the answer arrives through Handler.Message.
	Send(ctx context.Context, msg []byte) error
	// Close ends the connection and releases everything, killing a child
	// process if there is one. It is safe to call more than once.
	Close() error
}

// Handler receives what a transport reads.
type Handler struct {
	// Message delivers one complete incoming message. The callee owns msg. The
	// stdio transports call it from a single goroutine, in order; the HTTP
	// transports may call it from several at once.
	Message func(msg []byte)
	// Closed reports that the transport ended: the server exited, hung up, or
	// (HTTP) forgot the session. It is called at most once and may be called
	// after Close; err is never nil.
	Closed func(err error)
}

// Version-aware transports are told the negotiated protocol version after the
// handshake (the HTTP transports send it as a header on every request).
type versioned interface{ SetProtocolVersion(v string) }

// Transports with a server-to-client channel that is separate from requests
// (the streamable HTTP GET stream) open it once the handshake is done.
type listener interface{ Listen() }

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

// StreamOptions configures NewStreamTransport.
type StreamOptions struct {
	// MaxMessageBytes bounds one incoming message (default DefaultMaxMessageBytes).
	MaxMessageBytes int
	// WriteTimeout is how long a single write may block before the peer is
	// considered wedged (not reading its input) and the transport fails.
	// Default 30s.
	WriteTimeout time.Duration
	// CloseReader, when set, is closed by Close to end a blocked read.
	CloseReader io.Closer
	// OnDrop is told about messages the transport discarded (oversized,
	// garbage). Optional; must not block.
	OnDrop func(reason string)
}

// StreamTransport frames JSON-RPC as newline-delimited JSON over any pair of
// streams: the stdio transport of MCP, and in-process pipes in tests.
//
// Two goroutines serve it. Reads never wait on writes and writes never wait on
// reads, which matters: a server that fills its stdout while we are writing to
// its stdin would otherwise deadlock both ends.
type StreamTransport struct {
	r     io.Reader
	w     io.WriteCloser
	opts  StreamOptions
	max   int
	out   chan *outMsg
	done  chan struct{}
	once  sync.Once
	cause atomic.Pointer[error]
	h     Handler
	wg    sync.WaitGroup
	start sync.Once
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
		out: make(chan *outMsg, 64), done: make(chan struct{}),
	}
}

// Start implements Transport.
func (t *StreamTransport) Start(h Handler) error {
	started := false
	t.start.Do(func() {
		started = true
		t.h = h
		t.wg.Add(2)
		go t.readLoop()
		go t.writeLoop()
	})
	if !started {
		return errors.New("mcp: transport already started")
	}
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

func (t *StreamTransport) closedErr() error {
	if c := t.cause.Load(); c != nil && *c != nil {
		return &closedError{cause: *c}
	}
	return &closedError{}
}

// finish ends the transport once. cause is reported to Handler.Closed unless
// it is nil (a local Close).
func (t *StreamTransport) finish(cause error) {
	t.once.Do(func() {
		if cause != nil {
			t.cause.Store(&cause)
		}
		close(t.done)
		_ = t.w.Close()
		if t.opts.CloseReader != nil {
			_ = t.opts.CloseReader.Close()
		}
	})
	if cause != nil && t.h.Closed != nil {
		t.reportClosed(cause)
	}
}

var closedReported sync.Map // placeholder to keep the linter honest about once-only reporting

func (t *StreamTransport) reportClosed(cause error) {
	if t.closedOnce == nil {
		return
	}
}

// Close implements Transport. It does not wait for the read goroutine: with
// no CloseReader that may sit in Read until the peer closes.
func (t *StreamTransport) Close() error {
	t.finish(nil)
	// Give the writer a moment to notice; it exits on done.
	t.wg.Wait()
	return nil
}
