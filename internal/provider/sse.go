package provider

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// SSEEvent is one server-sent event.
type SSEEvent struct {
	Event string
	Data  []byte
}

// SSEReader parses a text/event-stream body.
type SSEReader struct {
	r *bufio.Reader
}

// NewSSEReader wraps r. Lines longer than the buffer are handled; model
// responses can carry very large single-line JSON payloads.
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{r: bufio.NewReaderSize(r, 1<<16)}
}

// Next returns the next event, or io.EOF at end of stream. Comment lines
// (": ping") and events without data are skipped.
func (s *SSEReader) Next() (SSEEvent, error) {
	var ev SSEEvent
	var data bytes.Buffer
	have := false
	for {
		line, err := s.r.ReadBytes('\n')
		if len(line) > 0 {
			l := strings.TrimRight(string(line), "\r\n")
			switch {
			case l == "":
				if have {
					ev.Data = data.Bytes()
					return ev, nil
				}
			case strings.HasPrefix(l, ":"):
				// comment / keep-alive
			case strings.HasPrefix(l, "event:"):
				ev.Event = strings.TrimSpace(l[len("event:"):])
			case strings.HasPrefix(l, "data:"):
				d := l[len("data:"):]
				d = strings.TrimPrefix(d, " ")
				if have {
					data.WriteByte('\n')
				}
				data.WriteString(d)
				have = true
			}
		}
		if err != nil {
			if err == io.EOF && have {
				ev.Data = data.Bytes()
				return ev, nil
			}
			return SSEEvent{}, err
		}
	}
}
