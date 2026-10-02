package state

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// clean makes untrusted text fit for one cell of a terminal: control characters and line or paragraph separators become one
// space (runs of white space collapse, the ends are trimmed), format characters (bidirectional overrides, zero-width joiners,
// the byte order mark) are dropped, invalid UTF-8 becomes U+FFFD, and the result is at most max runes, a cut one ending in "…".
// It never returns an escape sequence: the ESC byte is a control character.
func clean(s string, max int) string {
	if max <= 0 || s == "" {
		return ""
	}
	if plain(s, max) {
		return s
	}
	// Collect at most max+1 runes: one more than fits says the text was cut.
	out := make([]rune, 0, min(len(s), max+1))
	space := false
	for i := 0; i < len(s) && len(out) <= max; {
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		switch {
		case r == utf8.RuneError && w == 1:
			r = '�'
		case unicode.IsControl(r) || unicode.IsSpace(r) || r == ' ' || r == ' ':
			space = true
			continue
		case unicode.Is(unicode.Cf, r):
			continue
		}
		if space && len(out) > 0 {
			out = append(out, ' ')
			if len(out) > max {
				break
			}
		}
		space = false
		out = append(out, r)
	}
	if len(out) > max {
		out = out[:max]
		if max == 1 {
			return "…"
		}
		// Do not end on the space a cut left behind.
		out = out[:max-1]
		for len(out) > 0 && out[len(out)-1] == ' ' {
			out = out[:len(out)-1]
		}
		return string(out) + "…"
	}
	return string(out)
}

// plain reports whether s is already what clean would return: printable ASCII with single inner spaces, no more than max runes.
func plain(s string, max int) bool {
	if len(s) > max {
		return false
	}
	prev := byte(' ')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || (c == ' ' && prev == ' ') {
			return false
		}
		prev = c
	}
	return prev != ' '
}

// clip is clean for identifiers: no separators are added, only the controls removed and the length bounded.
func clip(s string, max int) string { return clean(s, max) }

// short keeps the head of a hash or a key: 12 characters are enough to tell two apart, and the result cannot be long.
func short(s string) string {
	s = clean(s, 64)
	if r := []rune(s); len(r) > 12 {
		return string(r[:12])
	}
	return s
}

// clampTokens bounds a token count that came from a payload to what an int holds on every platform.
func clampTokens(v int64) int {
	switch {
	case v < 0:
		return 0
	case v > smallCount:
		return smallCount
	}
	return int(v)
}

// clampInt bounds an int that came from a payload to [0, smallCount].
func clampInt(v int) int { return clampTokens(int64(v)) }

// usd turns a dollar figure from a payload into one that can be added: not a number, infinite or negative is nothing.
func usd(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return min(f, 1e12)
}

// fmtDur writes a duration in milliseconds the way the feed shows it: "340 ms", "1.4 s", "12 s", "2m05s", "1h02m".
func fmtDur(ms int64) string {
	switch {
	case ms < 0:
		ms = 0
		fallthrough
	case ms < 1000:
		return fmt.Sprintf("%d ms", ms)
	case ms < 10_000:
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	case ms < 60_000:
		return fmt.Sprintf("%d s", ms/1000)
	case ms < 3_600_000:
		return fmt.Sprintf("%dm%02ds", ms/60_000, ms%60_000/1000)
	}
	return fmt.Sprintf("%dh%02dm", ms/3_600_000, ms%3_600_000/60_000)
}

// fmtTok writes a token count the way the feed shows it: "312", "2.4k", "31k", "1.2M".
func fmtTok(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

// fmtUSD writes dollars with as many decimals as make the figure legible: "$0.0004", "$0.31", "$12.40".
func fmtUSD(v float64) string {
	switch {
	case v == 0:
		return "$0"
	case v < 0.01:
		return fmt.Sprintf("$%.4f", v)
	case v < 10:
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.1f", v)
}

// idLess orders ids so that be-2 comes before be-10: runs of digits compare as numbers, everything else byte by byte, and two
// ids that differ only in leading zeros fall back to plain string order, so the order is total.
func idLess(a, b string) bool {
	if natLess(a, b) {
		return true
	}
	return !natLess(b, a) && a < b
}

func natLess(a, b string) bool {
	for len(a) > 0 && len(b) > 0 {
		da, db := isDigit(a[0]), isDigit(b[0])
		if da && db {
			na, ra := digits(a)
			nb, rb := digits(b)
			if c := cmpDigits(na, nb); c != 0 {
				return c < 0
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// digits splits the leading run of digits off s, without its leading zeros.
func digits(s string) (run, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	run, rest = s[:i], s[i:]
	run = strings.TrimLeft(run, "0")
	return run, rest
}

// cmpDigits compares two digit runs without leading zeros as numbers: the longer is the larger.
func cmpDigits(a, b string) int {
	switch {
	case len(a) != len(b):
		if len(a) < len(b) {
			return -1
		}
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// taskNum is the number in a task id ("T12" is 12); ids that are not of that form sort after the others.
func taskNum(id string) int {
	if len(id) < 2 || id[0] != 'T' {
		return math.MaxInt32
	}
	n := 0
	for i := 1; i < len(id); i++ {
		if !isDigit(id[i]) || n > 1<<24 {
			return math.MaxInt32
		}
		n = n*10 + int(id[i]-'0')
	}
	return n
}

// ring is a fixed-capacity queue that overwrites its oldest element: the shape of every history the State keeps. The zero value
// has capacity zero; use newRing. It allocates as it grows, so an idle ring of a large capacity costs nothing.
type ring[T any] struct {
	buf   []T
	head  int // index of the oldest element once the ring is full
	cap   int
	total int // elements ever pushed
}

func newRing[T any](capacity int) ring[T] { return ring[T]{cap: capacity} }

func (r *ring[T]) len() int { return len(r.buf) }

// push appends v, overwriting the oldest element when the ring is full.
func (r *ring[T]) push(v T) {
	r.total++
	if r.cap <= 0 {
		return
	}
	if len(r.buf) < r.cap {
		r.buf = append(r.buf, v)
		return
	}
	r.buf[r.head] = v
	r.head = (r.head + 1) % r.cap
}

// at is the i-th oldest element (0 is the oldest).
func (r *ring[T]) at(i int) *T {
	if len(r.buf) < r.cap {
		return &r.buf[i]
	}
	return &r.buf[(r.head+i)%r.cap]
}

// byAbs is the element that was pushed as number abs (counting from zero), if it is still held.
func (r *ring[T]) byAbs(abs int) (*T, bool) {
	i := abs - (r.total - len(r.buf))
	if i < 0 || i >= len(r.buf) {
		return nil, false
	}
	return r.at(i), true
}

// slice copies the elements, oldest first.
func (r *ring[T]) slice() []T {
	if len(r.buf) == 0 {
		return nil
	}
	out := make([]T, len(r.buf))
	for i := range out {
		out[i] = *r.at(i)
	}
	return out
}

func (r *ring[T]) reset() { r.buf, r.head, r.total = nil, 0, 0 }

// revise edits the newest element that match accepts, if the ring still holds one: a later event can explain an earlier one, and
// the line that was written for it is then corrected, not left to say what the State no longer believes.
func (r *ring[T]) revise(match func(*T) bool, edit func(*T)) {
	for i := len(r.buf) - 1; i >= 0; i-- {
		if v := r.at(i); match(v) {
			edit(v)
			return
		}
	}
}
