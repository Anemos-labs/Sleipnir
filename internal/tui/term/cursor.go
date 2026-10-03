package term

import (
	"os"
	"runtime"
	"strconv"
	"time"
)

// CursorRow asks the terminal on in/out where its cursor is and returns the row, counted from 0 at the top. The terminal must be in raw
// mode, or the answer is echoed and held back until a newline. ok is false when no answer comes within the timeout (a terminal that
// does not take the question), when it cannot be asked, and always on Windows, where a read that waits cannot be called off: the caller
// then does without. A key typed before the answer comes is lost.
func CursorRow(in, out *os.File, timeout time.Duration) (row int, ok bool) {
	if runtime.GOOS == "windows" || in == nil || out == nil {
		return 0, false
	}
	r := NewReader(in)
	defer r.Cancel()
	if _, err := out.WriteString("\x1b[6n"); err != nil {
		return 0, false
	}
	type answer struct {
		row int
		ok  bool
	}
	got := make(chan answer, 1)
	go func() {
		var seen []byte
		buf := make([]byte, 64)
		for {
			n, err := r.Read(buf)
			seen = append(seen, buf[:n]...)
			if row, ok := parseCursorReport(seen); ok {
				got <- answer{row, true}
				return
			}
			if err != nil || len(seen) > 256 {
				got <- answer{}
				return
			}
		}
	}()
	select {
	case a := <-got:
		return a.row, a.ok
	case <-time.After(timeout):
		r.Cancel() // wakes the read; its answer goes to a channel nobody reads
		return 0, false
	}
}

// parseCursorReport finds ESC [ row ; col R in b and returns the row from 0.
func parseCursorReport(b []byte) (row int, ok bool) {
	for i := 0; i+2 < len(b); i++ {
		if b[i] != 0x1b || b[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(b) && (b[j] >= '0' && b[j] <= '9' || b[j] == ';') {
			j++
		}
		if j >= len(b) || b[j] != 'R' {
			continue
		}
		var r, c int
		parts := b[i+2 : j]
		semi := -1
		for k, ch := range parts {
			if ch == ';' {
				semi = k
			}
		}
		if semi < 1 || semi == len(parts)-1 {
			continue
		}
		r, err1 := strconv.Atoi(string(parts[:semi]))
		c, err2 := strconv.Atoi(string(parts[semi+1:]))
		if err1 != nil || err2 != nil || r < 1 || c < 1 {
			continue
		}
		return r - 1, true
	}
	return 0, false
}
