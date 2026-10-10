// Package chaos puts the failures of a real endpoint in front of any provider that speaks HTTP: a handler that wraps another one
// (the mock provider, or a recorder in front of a real service) and, request by request, serves it as it is or breaks it the
// way endpoints do. The benchmark and the dogfood runs on a marketplace endpoint met all of these within a day: a 503 with a
// JSON body that says the database is down, a 502 with an HTML page, a 429 with a Retry-After, a connection that dies after
// half a response, a stream that stops without its last frame, a server that accepts the request and says nothing. A test of
// the agent against each of them, and against a random mixture of them, is how "the run survives a flaky endpoint" is known
// rather than hoped.
//
// A Plan decides what happens to the n-th request. Script and Random are the two that tests use: the first names the fault of
// each request in order, the second faults each request with a probability and picks the fault at random, deterministically for
// a seed and a request number, so that a failing run is a failing run on the next machine.
package chaos

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Kind is what happens to a request.
type Kind int

const (
	// Pass serves the request as the wrapped handler does.
	Pass Kind = iota
	// Status answers with Fault.Status and Fault.Body without asking the wrapped handler.
	Status
	// Reset accepts the request and drops the connection with a reset before any byte of an answer.
	Reset
	// Abort serves the response and drops the connection (no end of the body) after Fault.After bytes of it: the client reads an
	// unexpected end of file.
	Abort
	// Truncate serves the response and ends it cleanly after Fault.After bytes: a body that is complete as HTTP and not as a stream
	// (no last frame, no [DONE]).
	Truncate
	// Stall says nothing for Fault.For before the answer, or until the client gives up.
	Stall
	// StallMidStream serves Fault.After bytes of the response, says nothing for Fault.For (or until the client gives up), and goes on.
	StallMidStream
	// Garbage answers 200 with a body that is not what was asked for (an HTML page): what a proxy in front of the service says
	// when the service is not there.
	Garbage
	// Trickle serves the response a byte at a time, Fault.For apart: a slow-loris of a server.
	Trickle
)

func (k Kind) String() string {
	switch k {
	case Pass:
		return "pass"
	case Status:
		return "status"
	case Reset:
		return "reset"
	case Abort:
		return "abort"
	case Truncate:
		return "truncate"
	case Stall:
		return "stall"
	case StallMidStream:
		return "stall-mid-stream"
	case Garbage:
		return "garbage"
	case Trickle:
		return "trickle"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// Fault is one thing that can happen to a request.
type Fault struct {
	Kind Kind
	// Status, Body and RetryAfter are for Status (RetryAfter also sets the header of that name, in whole seconds, rounded up).
	Status     int
	Body       string
	RetryAfter time.Duration
	// After is the number of bytes of the response that are served before an Abort, a Truncate or a StallMidStream.
	After int
	// For is how long a Stall or a StallMidStream is silent, and the gap between the bytes of a Trickle.
	For time.Duration
}

// String formats a chaos fault with the status, byte cutoff, or duration relevant to its kind.
func (f Fault) String() string {
	switch f.Kind {
	case Status:
		return fmt.Sprintf("status %d", f.Status)
	case Abort, Truncate:
		return fmt.Sprintf("%s after %d bytes", f.Kind, f.After)
	case StallMidStream:
		return fmt.Sprintf("%s after %d bytes for %v", f.Kind, f.After, f.For)
	case Stall, Trickle:
		return fmt.Sprintf("%s %v", f.Kind, f.For)
	}
	return f.Kind.String()
}

// The failures the real endpoint was seen to have, as faults.

// Down is the 503 that the marketplace endpoint answered when its database was away.
func Down() Fault {
	return Fault{Kind: Status, Status: 503, Body: `{"error":{"message":"Database is temporarily unavailable","type":"server_error"}}`}
}

// Lost is the 503 that says a request's ownership was lost: the completion may be incomplete.
func Lost() Fault {
	return Fault{Kind: Status, Status: 503, Body: `{"error":{"message":"Request ownership was lost. Completion and usage may be incomplete."}}`}
}

// Limited is a 429 that says when to come back.
func Limited(retryAfter time.Duration) Fault {
	return Fault{Kind: Status, Status: 429, Body: `{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`, RetryAfter: retryAfter}
}

// BadGateway is the 502 of a proxy in front of a service that is not there, with the HTML page such a proxy serves.
func BadGateway() Fault {
	return Fault{Kind: Status, Status: 502, Body: "<html><head><title>502 Bad Gateway</title></head><body><center><h1>502 Bad Gateway</h1></center><hr><center>nginx</center></body></html>"}
}

// Internal is a 500 with a plain message.
func Internal() Fault {
	return Fault{Kind: Status, Status: 500, Body: `{"error":{"message":"Internal server error"}}`}
}

// Refused is a 401: a fault that no waiting cures.
func Refused() Fault {
	return Fault{Kind: Status, Status: 401, Body: `{"error":{"message":"Invalid API key"}}`}
}

// Plan decides what happens to the n-th request (counted from 1).
type Plan interface {
	Next(n int, r *http.Request) Fault
}

// PlanFunc is a Plan in a function.
type PlanFunc func(n int, r *http.Request) Fault

// Next implements Plan.
func (f PlanFunc) Next(n int, r *http.Request) Fault { return f(n, r) }

// Script faults the requests in order, one fault each; the requests after the last are served as they are.
func Script(faults ...Fault) Plan {
	return PlanFunc(func(n int, _ *http.Request) Fault {
		if n >= 1 && n <= len(faults) {
			return faults[n-1]
		}
		return Fault{}
	})
}

// Random faults each request with probability rate, with a fault chosen at random from faults. What happens to a request depends
// on the seed and the request's number alone, not on the order in which concurrent requests arrive.
func Random(seed int64, rate float64, faults ...Fault) Plan {
	return PlanFunc(func(n int, _ *http.Request) Fault {
		if len(faults) == 0 {
			return Fault{}
		}
		rng := rand.New(rand.NewSource(seed*1_000_003 + int64(n)))
		if rng.Float64() >= rate {
			return Fault{}
		}
		return faults[rng.Intn(len(faults))]
	})
}

// Event is what was done to one request.
type Event struct {
	N     int
	Path  string
	Fault Fault
}

// Handler wraps another handler in a Plan.
type Handler struct {
	next http.Handler
	plan Plan

	mu     sync.Mutex
	n      int
	events []Event
}

// New wraps next in plan.
func New(next http.Handler, plan Plan) *Handler { return &Handler{next: next, plan: plan} }

// Events returns what was done to each request so far, in the order the requests arrived.
func (h *Handler) Events() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Event(nil), h.events...)
}

// Faulted counts the requests that were not served as they are.
func (h *Handler) Faulted() int {
	n := 0
	for _, e := range h.Events() {
		if e.Fault.Kind != Pass {
			n++
		}
	}
	return n
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.n++
	n := h.n
	h.mu.Unlock()
	f := h.plan.Next(n, r)
	h.mu.Lock()
	h.events = append(h.events, Event{N: n, Path: r.URL.Path, Fault: f})
	h.mu.Unlock()

	switch f.Kind {
	case Pass:
		h.next.ServeHTTP(w, r)
	case Status, Garbage, Stall:
		// A server reads what it was sent before it answers, and the server library learns that the client has gone (the request's
		// context) only once the body has been read: a stall that never reads would never see the client give up.
		drain(r)
		h.answer(w, r, f)
	case Reset:
		reset(w)
	case Abort, Truncate, StallMidStream, Trickle:
		cw := &cutWriter{ResponseWriter: w, r: r, fault: f, left: f.After}
		if f.Kind == Trickle {
			cw.left = 0
		}
		h.next.ServeHTTP(cw, r)
		if f.Kind == Abort && !cw.cut {
			// The response was shorter than the point of the cut: it ends there, and the connection with it.
			panic(http.ErrAbortHandler)
		}
	}
}

// drain reads the request's body to its end and puts it back for whoever reads it next (a stall serves the request after the silence).
func drain(r *http.Request) {
	if r.Body == nil {
		return
	}
	b, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(b))
}

// answer serves the faults that do not involve the wrapped handler's response (Stall serves it after the silence).
func (h *Handler) answer(w http.ResponseWriter, r *http.Request, f Fault) {
	switch f.Kind {
	case Status:
		status := f.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		if f.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int((f.RetryAfter+time.Second-1)/time.Second)))
		}
		body := f.Body
		if body == "" {
			body = http.StatusText(status)
		}
		if len(body) > 0 && body[0] == '{' {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/html")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	case Garbage:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>The service is not available. Please try again later.</body></html>"))
	case Stall:
		wait(r, f.For)
		h.next.ServeHTTP(w, r)
	}
}

// reset drops the connection with a reset, before anything was written.
func reset(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic(http.ErrAbortHandler)
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0) // close sends a reset, not an orderly end
	}
	_ = conn.Close()
}

// wait is silent for d, or until the client has gone.
func wait(r *http.Request, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-r.Context().Done():
	}
}

// cutWriter passes the first left bytes of a response and then does what the fault says.
type cutWriter struct {
	http.ResponseWriter
	r     *http.Request
	fault Fault
	left  int
	cut   bool // the point of the cut was reached
	stop  bool // nothing more is passed (Truncate)
}

// Unwrap exposes the underlying HTTP response writer to response-controller utilities.
func (c *cutWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Flush forwards flushing only when the wrapped HTTP writer supports it.
func (c *cutWriter) Flush() {
	if fl, ok := c.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (c *cutWriter) Write(p []byte) (int, error) {
	if h := c.ResponseWriter.Header(); h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/octet-stream") // never left to Go's sniffing of the body
	}
	if c.stop {
		return len(p), nil // the handler thinks it wrote; the client never sees it
	}
	if c.fault.Kind == Trickle {
		for i := range p {
			if _, err := c.ResponseWriter.Write(p[i : i+1]); err != nil {
				return i, err
			}
			c.Flush()
			wait(c.r, c.fault.For)
		}
		return len(p), nil
	}
	if c.cut || len(p) <= c.left {
		c.left -= len(p)
		return c.ResponseWriter.Write(p)
	}
	// This write crosses the point of the cut.
	head := p[:c.left]
	if len(head) > 0 {
		if _, err := c.ResponseWriter.Write(head); err != nil {
			return 0, err
		}
	}
	c.Flush()
	c.left = 0
	switch c.fault.Kind {
	case Abort:
		c.cut = true
		panic(http.ErrAbortHandler)
	case Truncate:
		c.cut, c.stop = true, true
		return len(p), nil
	case StallMidStream:
		c.cut = true
		wait(c.r, c.fault.For)
		_, err := c.ResponseWriter.Write(p[len(head):])
		return len(p), err
	}
	return c.ResponseWriter.Write(p[len(head):])
}
