package provider

import (
	"fmt"
	"io"
	"math"
	"time"
)

// StreamLimits bounds what one response may cost the harness. A streamed reply is
// read from an endpoint the harness does not control (a marketplace, a gateway, or
// whatever a base-URL override points at) and folded into memory, the event log
// and the next prompt, so a stream that never ends, a line that never ends or a
// tool call with a gigabyte of arguments must end the request instead of the
// process.
//
// The zero value of every field means the default below; a negative value turns
// that limit off. The defaults are several times what a legitimate response of
// the largest models can be (128k output tokens are about half a megabyte of
// text), so they never fire on real traffic.
type StreamLimits struct {
	// MaxBytes bounds everything read from the response body, framing included.
	MaxBytes int64
	// MaxLineBytes bounds one line of a stream and the data of one event. A data line
	// carries a whole JSON event.
	MaxLineBytes int
	// MaxEvents bounds the number of events (frames) in one response.
	MaxEvents int
	// MaxTextBytes bounds the answer text of one response and, separately, its
	// reasoning text.
	MaxTextBytes int
	// MaxToolCalls bounds the tool calls in one response.
	MaxToolCalls int
	// MaxToolArgBytes bounds the arguments of one tool call.
	MaxToolArgBytes int
	// MaxBlocks bounds the content blocks (and reasoning items) of one response.
	MaxBlocks int
	// MaxDuration bounds the wall time of one response, from the moment the request
	// is sent. Keep-alive comments reset the idle timer, so this is what stops a
	// server that trickles a byte a minute for ever.
	MaxDuration time.Duration
}

// Default stream limits.
const (
	DefaultMaxStreamBytes = 64 << 20 // the cap the non-streaming path always had
	DefaultMaxLineBytes   = 8 << 20
	DefaultMaxEvents      = 1 << 20
	DefaultMaxTextBytes   = 8 << 20
	// DefaultMaxToolCalls sits well above the number of calls the agent runs in one
	// turn (agent.DefaultMaxToolCalls, 128): a model that asks for more gets its extra
	// calls refused with an error it can act on (S24), which needs the whole response.
	// This bound only ends a stream that never stops calling tools.
	DefaultMaxToolCalls     = 512
	DefaultMaxToolArgBytes  = 4 << 20
	DefaultMaxBlocks        = 1024
	DefaultMaxStreamElapsed = 30 * time.Minute
)

// Normalized fills in the defaults and turns "off" into a bound no response
// reaches, so callers compare against the fields without special cases.
func (l StreamLimits) Normalized() StreamLimits {
	i64 := func(v, def int64) int64 {
		switch {
		case v == 0:
			return def
		case v < 0:
			return math.MaxInt64
		}
		return v
	}
	i := func(v, def int) int { return int(i64(int64(v), int64(def))) }
	l.MaxBytes = i64(l.MaxBytes, DefaultMaxStreamBytes)
	l.MaxLineBytes = i(l.MaxLineBytes, DefaultMaxLineBytes)
	l.MaxEvents = i(l.MaxEvents, DefaultMaxEvents)
	l.MaxTextBytes = i(l.MaxTextBytes, DefaultMaxTextBytes)
	l.MaxToolCalls = i(l.MaxToolCalls, DefaultMaxToolCalls)
	l.MaxToolArgBytes = i(l.MaxToolArgBytes, DefaultMaxToolArgBytes)
	l.MaxBlocks = i(l.MaxBlocks, DefaultMaxBlocks)
	l.MaxDuration = time.Duration(i64(int64(l.MaxDuration), int64(DefaultMaxStreamElapsed)))
	return l
}

// LimitExceeded is the error for a response that outgrew one of its limits. what
// names the limit ("stream size", "tool call arguments"). The kind is ErrServer:
// a server that overruns its budget is misbehaving, and the agent retries such a
// request a bounded number of times (with the stream's partial output discarded)
// before giving up. Every attempt is capped by the same limits, so a hostile
// server cannot make the retries any more expensive than the first try.
func LimitExceeded(what string, limit int64) *Error {
	return &Error{
		Kind:    ErrServer,
		Message: fmt.Sprintf("response exceeded the %s limit (%s); the stream was cancelled", what, humanSize(limit)),
	}
}

// LimitExceededCount is LimitExceeded for a limit that counts things.
func LimitExceededCount(what string, limit int) *Error {
	return &Error{
		Kind:    ErrServer,
		Message: fmt.Sprintf("response exceeded the limit of %d %s; the stream was cancelled", limit, what),
	}
}

// ReadCapped reads a whole body of at most max bytes. A longer one is a provider
// error (LimitExceeded), not a silently truncated document that then fails to
// parse with a confusing message.
func ReadCapped(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 || max == math.MaxInt64 {
		return io.ReadAll(r)
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, LimitExceeded("response size", max)
	}
	return b, nil
}

// humanSize uses whole MiB or KiB for exact multiples and otherwise reports bytes.
func humanSize(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
