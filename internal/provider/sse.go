package provider

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// SSEEvent is one server-sent event.
type SSEEvent struct {
	Event string
	Data  []byte
}

// SSEReader parses a text/event-stream body. It is bounded (see StreamLimits): a
// stream, a line or an event that grows past its limit ends with a *Error of
// kind ErrServer instead of filling memory.
type SSEReader struct {
	r   *bufio.Reader
	lim StreamLimits

	bytes  int64  // consumed so far
	events int    // events returned so far
	acc    []byte // scratch for a line that spans read buffers
}

// NewSSEReader wraps r with the default limits. Lines longer than the read
// buffer are handled; model responses can carry very large single-line JSON
// payloads, up to the line limit.
func NewSSEReader(r io.Reader) *SSEReader { return NewSSEReaderLimits(r, StreamLimits{}) }

// NewSSEReaderLimits wraps r with explicit limits (zero fields take the defaults).
func NewSSEReaderLimits(r io.Reader, l StreamLimits) *SSEReader {
	return &SSEReader{r: bufio.NewReaderSize(r, 1<<16), lim: l.Normalized()}
}

// Bytes reports how many bytes of the stream have been consumed.
func (s *SSEReader) Bytes() int64 { return s.bytes }

// readLine returns the next line with its terminator, valid until the next call.
// The error is io.EOF (with any final unterminated line), a read error, or the
// *Error of an exceeded limit.
func (s *SSEReader) readLine() ([]byte, error) {
	s.acc = s.acc[:0]
	for {
		frag, err := s.r.ReadSlice('\n')
		s.bytes += int64(len(frag))
		if s.bytes > s.lim.MaxBytes {
			return nil, LimitExceeded("stream size", s.lim.MaxBytes)
		}
		if len(s.acc)+len(frag) > s.lim.MaxLineBytes {
			return nil, LimitExceeded("line length", int64(s.lim.MaxLineBytes))
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			s.acc = append(s.acc, frag...)
			continue
		}
		if len(s.acc) == 0 {
			return frag, err
		}
		s.acc = append(s.acc, frag...)
		return s.acc, err
	}
}

var (
	prefixEvent = []byte("event:")
	prefixData  = []byte("data:")
)

// Next returns the next event, or io.EOF at end of stream. Comment lines
// (": ping") and events without data are skipped.
func (s *SSEReader) Next() (SSEEvent, error) {
	var ev SSEEvent
	var data []byte
	have := false
	emit := func() (SSEEvent, error) {
		s.events++
		if s.events > s.lim.MaxEvents {
			return SSEEvent{}, LimitExceededCount("events", s.lim.MaxEvents)
		}
		ev.Data = data
		return ev, nil
	}
	for {
		line, err := s.readLine()
		var le *Error
		if errors.As(err, &le) {
			return SSEEvent{}, le
		}
		if len(line) > 0 {
			l := bytes.TrimRight(line, "\r\n")
			switch {
			case len(l) == 0:
				if have {
					return emit()
				}
			case l[0] == ':':
				// comment / keep-alive
			case bytes.HasPrefix(l, prefixEvent):
				ev.Event = strings.TrimSpace(string(l[len(prefixEvent):]))
			case bytes.HasPrefix(l, prefixData):
				d := l[len(prefixData):]
				if len(d) > 0 && d[0] == ' ' {
					d = d[1:]
				}
				if have {
					data = append(data, '\n')
				}
				if len(data)+len(d) > s.lim.MaxLineBytes {
					return SSEEvent{}, LimitExceeded("event size", int64(s.lim.MaxLineBytes))
				}
				data = append(data, d...)
				have = true
			}
		}
		if err != nil {
			if err == io.EOF && have {
				return emit()
			}
			return SSEEvent{}, err
		}
	}
}
