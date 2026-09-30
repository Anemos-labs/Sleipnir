package shell

import (
	"fmt"
	"sync"
)

// sink receives sanitized output as it is read. Implementations must be safe
// for concurrent use: stdout and stderr are pumped by separate goroutines.
type sink interface {
	write(stream string, p []byte)
	// detach stops the sink from taking further output (and from calling back
	// into the caller). It is used when a command has finished but a leftover
	// background process still holds the output pipes open.
	detach()
}

// capture keeps the head and the tail of a foreground command's output and
// counts what fell out of the middle. Errors and summaries live at the end of
// build and test logs, invocation echoes at the start, so those are the ends
// worth keeping; a 2 GB log must not cost 2 GB of RAM.
type capture struct {
	headCap int
	tailCap int
	head    []byte
	ring    []byte // allocated lazily: most commands never overflow the head
	rStart  int
	rLen    int
	dropped int64
}

func newCapture(total int) *capture {
	head := total / 4
	return &capture{headCap: head, tailCap: total - head}
}

func (c *capture) write(p []byte) {
	if len(c.head) < c.headCap {
		n := min(c.headCap-len(c.head), len(p))
		c.head = append(c.head, p[:n]...)
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	if c.ring == nil {
		c.ring = make([]byte, c.tailCap)
	}
	if len(p) >= c.tailCap {
		c.dropped += int64(c.rLen) + int64(len(p)-c.tailCap)
		copy(c.ring, p[len(p)-c.tailCap:])
		c.rStart, c.rLen = 0, c.tailCap
		return
	}
	end := (c.rStart + c.rLen) % c.tailCap
	n := copy(c.ring[end:], p)
	copy(c.ring, p[n:])
	if over := c.rLen + len(p) - c.tailCap; over > 0 {
		c.dropped += int64(over)
		c.rStart = (c.rStart + over) % c.tailCap
		c.rLen = c.tailCap
	} else {
		c.rLen += len(p)
	}
}

// text assembles head, an elision marker and tail.
func (c *capture) text() string {
	buf := make([]byte, 0, len(c.head)+c.rLen+64)
	buf = append(buf, c.head...)
	if c.dropped > 0 {
		buf = fmt.Appendf(buf, "\n… [%d bytes of output elided] …\n", c.dropped)
	}
	if c.rLen > 0 {
		first := min(c.rStart+c.rLen, len(c.ring))
		buf = append(buf, c.ring[c.rStart:first]...)
		buf = append(buf, c.ring[:c.rLen-(first-c.rStart)]...)
	}
	return string(buf)
}

// fgSink is the sink of a foreground command: it captures for the final result
// and streams live to the UI callback.
type fgSink struct {
	mu  sync.Mutex
	cap *capture
	out func(stream, text string)
	off bool
}

func newFGSink(out func(stream, text string), capBytes int) *fgSink {
	return &fgSink{cap: newCapture(capBytes), out: out}
}

func (s *fgSink) write(stream string, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.off || len(p) == 0 {
		return
	}
	s.cap.write(p)
	if s.out != nil {
		s.callOut(stream, string(p))
	}
}

// callOut shields the process from a panicking UI callback: it runs on a pump
// goroutine, where a panic would take the whole harness down.
func (s *fgSink) callOut(stream, text string) {
	defer func() {
		if recover() != nil {
			s.out = nil
		}
	}()
	s.out(stream, text)
}

func (s *fgSink) detach() {
	s.mu.Lock()
	s.off = true
	s.out = nil
	s.mu.Unlock()
}

// snapshot returns the captured text; call after detach for a stable view.
func (s *fgSink) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cap.text()
}

// rolling is a background job's output buffer: the most recent max bytes, with
// absolute offsets so readers can ask for "everything since byte N" even after
// older output has been discarded.
type rolling struct {
	mu   sync.Mutex
	buf  []byte
	base int64 // absolute offset of buf[0]
	max  int
}

func newRolling(max int) *rolling { return &rolling{max: max} }

func (r *rolling) write(_ string, p []byte) {
	if len(p) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	// Trim in batches so a steady stream costs an amortised O(1) per byte.
	if len(r.buf) > r.max+r.max/4 {
		drop := len(r.buf) - r.max
		n := copy(r.buf, r.buf[drop:])
		r.buf = r.buf[:n]
		r.base += int64(drop)
	}
}

func (r *rolling) detach() {}

// read returns the bytes at or after absolute offset since, how many bytes
// between since and the returned data were already discarded, and the offset to
// pass next time.
func (r *rolling) read(since int64) (data []byte, dropped int64, next int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.base + int64(len(r.buf))
	if since < 0 {
		since = 0
	}
	if since < r.base {
		dropped = r.base - since
		since = r.base
	}
	if since > end {
		since = end
	}
	data = append([]byte(nil), r.buf[since-r.base:]...)
	return data, dropped, end
}

// total is the number of bytes ever written.
func (r *rolling) total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base + int64(len(r.buf))
}
