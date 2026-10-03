package provider

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Default deadlines for one model exchange. They are generous on purpose: a
// reasoning model may think for minutes before its first token (marketplaces that
// do not send keep-alive comments send nothing until then), and a huge cold
// prompt takes a while to prefill. A hung endpoint is still bounded, which is
// what matters: a request that never returns pins a governor slot, the warm
// gate's primer and the agent.
const (
	// DefaultFirstByteTimeout bounds the wait from sending a request to the first
	// byte of the response body.
	DefaultFirstByteTimeout = 120 * time.Second
	// DefaultStreamIdleTimeout bounds the silence between two reads once the
	// response has started. Keep-alive comments and pings are data.
	DefaultStreamIdleTimeout = 60 * time.Second
	// DefaultRequestTimeout bounds a non-streaming call (warm-ups, keep-alives) from
	// the moment it is sent until its body is read.
	DefaultRequestTimeout = 10 * time.Minute
	// MaxSilentAttempts is how many times one request may get no response at all
	// (nothing within the first-byte deadline) before it stops being retried.
	MaxSilentAttempts = 2
)

// WatchReason says which deadline of a Watchdog ended an exchange.
type WatchReason int

const (
	WatchNone      WatchReason = iota // nothing fired
	WatchFirstByte                    // no byte arrived within the first-byte deadline
	WatchIdle                         // the response went silent after it had started
	WatchTotal                        // the response outlived its overall deadline
)

// Watchdog bounds one HTTP exchange with three deadlines, all armed from the
// moment Start is called (just before the request is sent, so a server that
// accepts the connection and never answers is covered as well as one that stalls
// mid-stream):
//
//   - first: from the start until the first byte of the response body;
//   - idle: after that, between two reads that returned data;
//   - total: the whole exchange (zero: none).
//
// When one fires it records why and calls cancel, which aborts the request or the
// body read in progress. Callers ask Why (or Failure) to tell "the server went
// quiet" from "the caller cancelled": the derived context is cancelled in both
// cases, and only the first is retried as a timeout.
type Watchdog struct {
	cancel             context.CancelFunc
	first, idle, total time.Duration
	req                *Request

	mu       sync.Mutex
	timer    *time.Timer
	totalT   *time.Timer
	deadline time.Time
	seen     bool
	done     bool
	why      WatchReason
}

// NewWatchdog builds a watchdog that calls cancel when a deadline passes. req (may
// be nil) is the request being served: the watchdog counts, on it, how often the
// same request got no response at all, so that the agent's retry loop cannot
// multiply one hung endpoint into a dozen hung minutes (see MaxSilentAttempts).
func NewWatchdog(cancel context.CancelFunc, first, idle, total time.Duration, req *Request) *Watchdog {
	return &Watchdog{cancel: cancel, first: first, idle: idle, total: total, req: req}
}

// Start arms the deadlines.
func (w *Watchdog) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done || w.timer != nil {
		return
	}
	w.deadline = time.Now().Add(w.first)
	w.timer = time.AfterFunc(w.first, w.fire)
	if w.total > 0 {
		w.totalT = time.AfterFunc(w.total, w.fireTotal)
	}
}

// fire runs when the current deadline may have passed. Progress moves the
// deadline without touching the timer, so a stale firing re-arms itself for the
// time that is left.
func (w *Watchdog) fire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done || w.why != WatchNone {
		return
	}
	if rem := time.Until(w.deadline); rem > 0 {
		w.timer.Reset(rem)
		return
	}
	if w.seen {
		w.why = WatchIdle
	} else {
		w.why = WatchFirstByte
	}
	w.cancel()
}

// fireTotal records a total-deadline timeout and cancels once, preserving an earlier watchdog
// outcome.
func (w *Watchdog) fireTotal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done || w.why != WatchNone {
		return
	}
	w.why = WatchTotal
	w.cancel()
}

// progress records that data arrived: the first byte ends the first-byte wait,
// and every later one restarts the idle deadline.
func (w *Watchdog) progress() {
	w.mu.Lock()
	w.deadline = time.Now().Add(w.idle)
	if !w.seen {
		w.seen = true
		// The idle deadline is usually shorter than the first-byte deadline the timer
		// was armed with, so the timer moves up; later progress only pushes the
		// deadline back, which fire notices by itself.
		if w.timer != nil && !w.done && w.why == WatchNone && w.idle < w.first {
			w.timer.Reset(w.idle)
		}
	}
	w.mu.Unlock()
}

// Stop disarms the watchdog. It is safe to call more than once. A request that
// got any response byte is no longer counted as silent.
func (w *Watchdog) Stop() {
	w.mu.Lock()
	w.done = true
	if w.timer != nil {
		w.timer.Stop()
	}
	if w.totalT != nil {
		w.totalT.Stop()
	}
	seen := w.seen
	w.mu.Unlock()
	if seen && w.req != nil {
		atomic.StoreInt32(&w.req.silent, 0)
	}
}

// Reader wraps the response body so that every read that returns data counts as
// activity.
func (w *Watchdog) Reader(r io.Reader) io.Reader { return &watchReader{r: r, w: w} }

type watchReader struct {
	r io.Reader
	w *Watchdog
}

// Read forwards data and reports watchdog progress only when bytes were received.
func (a *watchReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.w.progress()
	}
	return n, err
}

// Why reports which deadline fired, WatchNone if none did.
func (w *Watchdog) Why() WatchReason {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.why
}

// Received reports whether any response byte arrived.
func (w *Watchdog) Received() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

// Failure builds the provider error for a fired deadline (cause is what the
// transport reported, usually context.Canceled), or returns nil when none fired.
// The error is ErrTimeout, which the agent retries; a request that has now been
// met with silence MaxSilentAttempts times is marked NoRetry, because a server
// that swallows the same request twice is down, and a third wait of the whole
// first-byte deadline only delays the failure.
func (w *Watchdog) Failure(cause error) *Error {
	why := w.Why()
	if why == WatchNone {
		return nil
	}
	e := &Error{Kind: ErrTimeout, Err: cause}
	switch why {
	case WatchFirstByte:
		e.Message = "no response from the server within " + w.first.String()
		if w.req != nil {
			if n := int(atomic.AddInt32(&w.req.silent, 1)); n >= MaxSilentAttempts {
				e.NoRetry = true
				e.Message += fmt.Sprintf(" (%d attempts of this request got no response; not retrying)", n)
			}
		}
	case WatchIdle:
		e.Message = "no data from the server for " + w.idle.String()
	default:
		e.Message = "the response was still running after " + w.total.String()
	}
	return e
}
