package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// sseEvent is one dispatched server-sent event.
type sseEvent struct {
	Event string // "" means the default type, "message"
	Data  string // data lines joined with "\n"
	ID    string
	Retry int // milliseconds, or -1 when the event set none
}

// sseReader parses a text/event-stream (WHATWG HTML, section 9.2).
//
// Lines end in CRLF, LF or a lone CR (servers and proxies produce all three).
// Memory is bounded: one line, and one event's data, may not exceed the limit,
// and the reader reports ErrMessageTooLarge instead of growing.
type sseReader struct {
	sc      *bufio.Scanner
	max     int
	started bool
	event   string
	data    bytes.Buffer
	hasData bool
	id      string
	retry   int
	lastID  string
}

func newSSEReader(r io.Reader, maxEvent int) *sseReader {
	sc := bufio.NewScanner(r)
	// A line is "data: " plus payload, so allow a little over the event limit.
	sc.Buffer(make([]byte, 0, 32<<10), maxEvent+64)
	sc.Split(scanSSELines)
	return &sseReader{sc: sc, max: maxEvent, retry: -1}
}

// scanSSELines is bufio.ScanLines that also accepts a lone CR as a terminator.
func scanSSELines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if atEOF {
				return i + 1, data[:i], nil
			}
			return 0, nil, nil // a CR at the end of the buffer: is an LF next?
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// lastEventID is the id of the last event dispatched with one; a reconnecting
// GET stream sends it as Last-Event-ID.
func (r *sseReader) lastEventID() string { return r.lastID }

// Next returns the next event. It returns io.EOF at the end of the stream and
// ErrMessageTooLarge when a line or event exceeded the limit.
func (r *sseReader) Next() (*sseEvent, error) {
	for r.sc.Scan() {
		line := r.sc.Bytes()
		if !r.started {
			r.started = true
			line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf")) // byte order mark
		}
		if len(line) == 0 {
			if ev := r.dispatch(); ev != nil {
				return ev, nil
			}
			continue
		}
		if line[0] == ':' { // comment (keep-alive)
			continue
		}
		field, value, _ := strings.Cut(string(line), ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			r.event = value
		case "data":
			if r.data.Len()+len(value)+1 > r.max {
				return nil, ErrMessageTooLarge
			}
			r.data.WriteString(value)
			r.data.WriteByte('\n')
			r.hasData = true
		case "id":
			if !strings.ContainsRune(value, 0) {
				r.id = value
			}
		case "retry":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				r.retry = n
			}
		}
	}
	if err := r.sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, ErrMessageTooLarge
		}
		return nil, err
	}
	// The stream ended. By spec an event still being assembled is discarded;
	// servers that close without the final blank line are tolerated because a
	// response is far more likely complete than not.
	if ev := r.dispatch(); ev != nil {
		return ev, nil
	}
	return nil, io.EOF
}

func (r *sseReader) dispatch() *sseEvent {
	defer func() {
		r.event, r.id, r.retry, r.hasData = "", "", -1, false
		r.data.Reset()
	}()
	if r.id != "" {
		r.lastID = r.id
	}
	if !r.hasData {
		return nil
	}
	return &sseEvent{
		Event: r.event,
		Data:  strings.TrimSuffix(r.data.String(), "\n"),
		ID:    r.id,
		Retry: r.retry,
	}
}
