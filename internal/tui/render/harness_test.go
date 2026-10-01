package render

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
)

// The tests of this package run the renderer against the emulator of internal/tui/vt: everything the renderer writes goes into a
// vt.Term, and the assertions are about the screen that results (plain text, or cells and styles), never about the bytes,
// except where the bytes are the point (how many there are, what they may contain). A golden screen is a plain text file.

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// termCaps is a truecolor terminal of the given size.
func termCaps(cols, rows int) term.Caps {
	return term.Caps{Color: term.ColorTrueColor, Unicode: true, Width: cols, Height: rows, BracketedPaste: true}
}

// bridge is the io.Writer the renderer writes to: it keeps every Write as a frame, feeds the emulator, and checks what must hold
// after every frame.
type bridge struct {
	tb     testing.TB
	v      *vt.Term
	caps   term.Caps
	frames [][]byte
	total  int
}

func (b *bridge) Write(p []byte) (int, error) {
	b.tb.Helper()
	b.frames = append(b.frames, append([]byte(nil), p...))
	b.total += len(p)
	b.v.Write(p)
	if !b.v.Idle() {
		b.tb.Errorf("a frame ended inside an escape sequence: %q", p)
	}
	if b.v.SyncDepth() != 0 {
		b.tb.Errorf("a frame left synchronized output open (depth %d): %q", b.v.SyncDepth(), p)
	}
	if len(b.v.Rejected) != 0 {
		b.tb.Errorf("the renderer emitted %q", b.v.Rejected)
		b.v.Rejected = nil
	}
	if b.caps.SyncOutput {
		if !bytes.HasPrefix(p, []byte(syncBegin)) || !bytes.HasSuffix(p, []byte(syncEnd)) || bytes.Count(p, []byte(syncBegin)) != 1 || bytes.Count(p, []byte(syncEnd)) != 1 {
			b.tb.Errorf("a frame that is not one synchronized-output frame: %q", p)
		}
	}
	checkVocabulary(b.tb, p)
	return len(p), nil
}

// checkVocabulary asserts that every escape sequence in p is one the renderer composes and every control byte is one it may
// write: ESC, CR and LF. It is what "no byte of the output is a sequence the renderer did not write" means in a test: it cannot
// prove an escape came from the renderer, but a sequence that is outside the grammar, or a stray control byte, is caught.
func checkVocabulary(tb testing.TB, p []byte) {
	tb.Helper()
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == 0x1b:
			if i+1 >= len(p) || p[i+1] != '[' {
				tb.Errorf("an escape that does not start a CSI at byte %d of %q", i, p)
				return
			}
			j := i + 2
			for j < len(p) && (p[j] >= 0x30 && p[j] <= 0x3f) {
				j++
			}
			if j >= len(p) {
				tb.Errorf("an unterminated CSI in %q", p)
				return
			}
			params, final := string(p[i+2:j]), p[j]
			switch final {
			case 'A', 'B', 'C', 'D', 'G', 'H', 'J', 'K':
				if strings.Trim(params, "0123456789;") != "" {
					tb.Errorf("unexpected parameters %q for %c", params, final)
				}
			case 'm':
				if strings.Trim(params, "0123456789;") != "" {
					tb.Errorf("unexpected SGR parameters %q", params)
				}
			case 'h', 'l':
				switch params {
				case "?25", "?2026", "?2004", "?1049":
				default:
					tb.Errorf("unexpected mode %q", params)
				}
			default:
				tb.Errorf("unexpected CSI final %q (params %q)", final, params)
			}
			i = j
		case c == '\r' || c == '\n':
		case c < 0x20 || c == 0x7f:
			tb.Errorf("control byte %#x in the output %q", c, p)
		case c == 0xc2 && i+1 < len(p) && p[i+1] >= 0x80 && p[i+1] < 0xa0:
			tb.Errorf("a C1 control (U+%04X) in the output %q", 0x80|rune(p[i+1]&0x1f)|rune(p[i+1]&0x20), p)
		}
	}
}

// harness is a renderer wired to an emulator of the same size.
type harness struct {
	tb testing.TB
	b  *bridge
	v  *vt.Term
	r  *Inline
}

func newHarness(tb testing.TB, caps term.Caps, opts ...InlineOption) *harness {
	tb.Helper()
	v := vt.New(caps.Width, caps.Height)
	b := &bridge{tb: tb, v: v, caps: caps}
	return &harness{tb: tb, b: b, v: v, r: NewInline(b, caps, opts...)}
}

func (h *harness) flush() {
	h.tb.Helper()
	if err := h.r.Flush(); err != nil {
		h.tb.Fatalf("flush: %v", err)
	}
}

// resize changes the size of the terminal and tells the renderer, as a window resize and SIGWINCH do.
func (h *harness) resize(cols, rows int) {
	h.v.Resize(cols, rows)
	h.r.Resize(cols, rows)
}

// written is how many bytes the renderer has written so far.
func (h *harness) written() int { return h.b.total }

// all is everything the terminal has shown, scrollback and screen, without the empty rows at the end.
func (h *harness) all() []string { return trimTail(h.v.All()) }

func (h *harness) wantAll(want ...string) {
	h.tb.Helper()
	if got := h.all(); !reflect.DeepEqual(got, trimTail(want)) {
		h.tb.Errorf("screen and scrollback:\n got  %q\n want %q", got, want)
	}
}

func trimTail(rows []string) []string {
	n := len(rows)
	for n > 0 && rows[n-1] == "" {
		n--
	}
	return append([]string{}, rows[:n]...)
}

func txt(s string) cell.Line { return cell.Text(s) }

func txts(ss ...string) []cell.Line {
	out := make([]cell.Line, len(ss))
	for i, s := range ss {
		out[i] = cell.Text(s)
	}
	return out
}

// snapshot is a screen as a golden file: the size, the cursor, the scrollback and the visible rows, as plain text. A hidden cursor
// has no position worth pinning.
func snapshot(v *vt.Term) string {
	cols, rows := v.Size()
	x, y, vis := v.Cursor()
	var b strings.Builder
	if vis {
		fmt.Fprintf(&b, "%dx%d cursor (%d,%d)\n", cols, rows, x, y)
	} else {
		fmt.Fprintf(&b, "%dx%d cursor hidden\n", cols, rows)
	}
	if sb := v.Scrollback(); len(sb) > 0 {
		b.WriteString("--- scrollback ---\n")
		for _, l := range sb {
			b.WriteString(l + "|\n")
		}
	}
	b.WriteString("--- screen ---\n")
	for _, l := range v.Rows() {
		b.WriteString(l + "|\n")
	}
	return b.String()
}

// golden compares got with testdata/<name>.screen, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".screen")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden screen %s (create it with: go test ./internal/tui/render -run Golden -update): %v", path, err)
	}
	if string(want) != got {
		t.Errorf("the screen changed: %s\n--- want\n%s--- got\n%s\nIf this is intended, run: go test ./internal/tui/render -run Golden -update", path, want, got)
	}
}
