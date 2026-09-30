package inspect

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

const (
	maxEventData = 4096     // payload bytes returned per event before it is elided
	maxScanBytes = 64 << 20 // bytes one /api/events call may scan while filtering
)

// Events returns raw log events for the live view: those after Since (or the
// newest Limit with Tail), optionally filtered by agent and type. It seeks with
// the sparse offset index instead of scanning from the start, reads only bytes
// the model has already consumed (never a torn tail), and elides large payloads.
func (s *Session) Events(q EventQuery) (EventPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 2000)

	s.mu.RLock()
	idx := append([]indexEntry(nil), s.index...)
	last, consumed, path := s.lastSeq, s.off, s.path
	s.mu.RUnlock()
	page := EventPage{Events: []EventRow{}, LastSeq: last, Next: q.Since}
	if path == "" {
		return page, errors.New("inspect: no log file behind this session")
	}

	since := q.Since
	if q.Tail {
		since = 0
		if last > uint64(limit) {
			since = last - uint64(limit)
		}
		page.Next = since
	}
	// Last index entry at or before since: everything earlier is skipped by seeking.
	i := sort.Search(len(idx), func(i int) bool { return idx[i].seq > since }) - 1
	var off int64
	if i >= 0 {
		off = idx[i].off
	}
	if off >= consumed {
		return page, nil
	}
	f, err := openRegular(path)
	if err != nil {
		return page, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return page, err
	}
	lr := newLineReader(bufio.NewReaderSize(io.LimitReader(f, consumed-off), 256<<10), off, s.opts.MaxLineBytes)
	scanned := int64(0)
	for {
		line, _, _, rerr := lr.next()
		if rerr != nil && rerr != io.EOF && !errors.Is(rerr, errLineTooLong) {
			return page, rerr
		}
		scanned = lr.off - off
		if len(line) > 0 {
			if ev, ok := parseEvent(line); ok && ev.Seq > since {
				if len(page.Events) >= limit {
					page.More = true
					break
				}
				page.Next = ev.Seq
				if (q.Agent == "" || ev.Agent == q.Agent) && (q.Type == "" || ev.Type == q.Type) {
					page.Events = append(page.Events, EventRow{Seq: ev.Seq, TS: ev.TS, Agent: ev.Agent, Type: ev.Type, Cause: ev.Cause, Data: elide(ev.Data)})
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if scanned > maxScanBytes {
			page.More = true
			break
		}
	}
	return page, nil
}

// elide replaces an oversized payload with a marker and a preview. The preview
// is a JSON string, so a truncated payload can never produce invalid JSON.
func elide(data json.RawMessage) json.RawMessage {
	if len(data) <= maxEventData {
		return data
	}
	b, _ := json.Marshal(map[string]any{"_truncated": true, "_bytes": len(data), "_preview": strings.ToValidUTF8(string(data[:512]), "")})
	return b
}
