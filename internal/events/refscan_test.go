package events

import (
	"bytes"
	"strings"
	"testing"
)

// refScan is scanLog written the plain way, over the whole file in memory: split it at
// newlines, judge each line, add up. It has no buffers, so none of the reading logic
// (lines longer than the read buffer, lines over the limit that are skipped unread, an
// unterminated last line) can hide in it.
func refScan(data []byte, limit int) logScan {
	var sc logScan
	unrecorded, lineNo := 0, 0
	for pos := 0; pos < len(data); {
		lineNo++
		n := len(data) - pos
		complete := false
		if i := bytes.IndexByte(data[pos:], '\n'); i >= 0 {
			n, complete = i+1, true
		}
		line := data[pos : pos+n]
		var (
			seq uint64
			typ string
			ok  bool
		)
		if n <= limit {
			seq, typ, ok = parseLine(line)
		}
		if !complete {
			if ok {
				sc.last = max(sc.last, seq)
				sc.needNewline = true
				sc.size += int64(n)
			} else {
				sc.TornBytes = int64(n)
			}
			break
		}
		if ok {
			sc.last = max(sc.last, seq)
			if typ == TypeLogCorrupt {
				unrecorded = 0
			}
		} else {
			if unrecorded++; unrecorded == 1 {
				sc.FirstCorruptLine = lineNo
			}
		}
		sc.size += int64(n)
		pos += n
	}
	sc.CorruptLines = unrecorded
	if unrecorded == 0 {
		sc.FirstCorruptLine = 0
	}
	return sc
}

// FuzzScanLog checks the reader Open is built on against refScan for arbitrary bytes
// and an arbitrary line limit: the same events counted, the same damage found, the
// same torn bytes, the same place to resume.
func FuzzScanLog(f *testing.F) {
	good := evLine(1, "a") + evLine(2, "b")
	for _, s := range []struct {
		data  string
		limit uint32
	}{
		{"", 100}, {"\n", 100}, {good, 1000}, {good + `{"seq":3,"ty`, 1000}, {good + "garbage\n" + evLine(3, "c"), 1000},
		{good + strings.Repeat("x", 200) + "\n" + evLine(3, "c"), 150}, {good + strings.Repeat("x", 70000) + "\n" + evLine(3, "c"), 100000},
		{good + strings.Repeat("y", 70000), 100000}, {good + strings.Repeat("y", 70000) + "\n", 69999},
		{strings.Repeat(good, 3000), 1 << 20}, {`{"seq":1,"type":"log.corrupt"}` + "\n" + "bad\n" + good, 100},
		{"\x00\x00\n\x00", 10}, {strings.Repeat("\n", 300), 1},
	} {
		f.Add([]byte(s.data), s.limit)
	}
	f.Fuzz(func(t *testing.T, data []byte, limit uint32) {
		if len(data) > 1<<21 {
			t.Skip("large inputs only slow the target down")
		}
		old := maxLineBytes
		maxLineBytes = 1 + int(limit%(1<<19)) // from one byte to half a megabyte: the skip-unread and the buffer-full paths both
		defer func() { maxLineBytes = old }()

		got, err := scanLog(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("scanLog: %v", err)
		}
		if want := refScan(data, maxLineBytes); got != want {
			t.Fatalf("scanLog = %+v\nrefScan = %+v\nlimit %d", got, want, maxLineBytes)
		}
		if got.size < 0 || got.size > int64(len(data)) || got.TornBytes < 0 || got.size+got.TornBytes > int64(len(data)) {
			t.Fatalf("impossible sizes: %+v for %d bytes", got, len(data))
		}
	})
}
