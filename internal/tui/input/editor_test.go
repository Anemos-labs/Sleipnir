package input

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/tui/cell"
)

// Byte strings for the keys the tests press.
const (
	kLeft, kRight, kUp, kDown = "\x1b[D", "\x1b[C", "\x1b[A", "\x1b[B"
	kHome, kEnd               = "\x1b[H", "\x1b[F"
	kCtrlHome, kCtrlEnd       = "\x1b[1;5H", "\x1b[1;5F"
	kCtrlLeft, kCtrlRight     = "\x1b[1;5D", "\x1b[1;5C"
	kEnter, kBS, kDel         = "\r", "\x7f", "\x1b[3~"
	kAltEnter, kShiftEnter    = "\x1b\r", "\x1b[13;2u"
	kEsc, kTab, kShiftTab     = "\x1b", "\t", "\x1b[Z"
	kUndo, kRedo              = "\x1f", "\x1bz"
	kPgUp, kPgDn              = "\x1b[5~", "\x1b[6~"
)

func ctrl(c byte) string    { return string(rune(c - 'a' + 1)) }
func alt(s string) string   { return "\x1b" + s }
func paste(s string) string { return "\x1b[200~" + s + "\x1b[201~" }

// rig drives an Editor with raw terminal bytes, through a Decoder, the way the real input loop does.
type rig struct {
	t      *testing.T
	d      Decoder
	ed     *Editor
	events []Event // everything since the last call to take
}

func newRig(t *testing.T, o Options) *rig {
	t.Helper()
	return &rig{t: t, ed: NewEditor(o)}
}

// send feeds bytes (and a Flush, so a lone Esc is resolved) and returns the events they produced.
func (r *rig) send(parts ...string) []Event {
	r.t.Helper()
	var evs []Event
	for _, s := range parts {
		keys := append(r.d.Feed([]byte(s)), r.d.Flush()...)
		for _, k := range keys {
			evs = append(evs, r.ed.Handle(k)...)
		}
		r.check()
	}
	r.events = append(r.events, evs...)
	return evs
}

// check asserts the invariants that must hold after every key.
func (r *rig) check() {
	r.t.Helper()
	ed := r.ed
	if ed.cur < 0 || ed.cur > len(ed.buf) {
		r.t.Fatalf("cursor %d outside the buffer of %d runes", ed.cur, len(ed.buf))
	}
	if !isBoundary(ed.buf, ed.cur) {
		r.t.Fatalf("cursor %d is inside a cluster of %q", ed.cur, string(ed.buf))
	}
	if !utf8.ValidString(string(ed.buf)) {
		r.t.Fatalf("buffer is not valid UTF-8: %q", string(ed.buf))
	}
	for _, c := range ed.buf {
		if (c < 0x20 && c != '\n' && c != '\t') || (c >= 0x7f && c < 0xa0) {
			r.t.Fatalf("control character %U in the buffer %q", c, string(ed.buf))
		}
	}
}

// state shows the buffer with the cursor as "|" (a chip is its label).
func (r *rig) state() string {
	var b strings.Builder
	for i, c := range r.ed.buf {
		if i == r.ed.cur {
			b.WriteByte('|')
		}
		if isChip(c) {
			b.WriteString(r.ed.chipLabel(c))
		} else {
			b.WriteRune(c)
		}
	}
	if r.ed.cur == len(r.ed.buf) {
		b.WriteByte('|')
	}
	return b.String()
}

func withoutChanged(evs []Event) []Event {
	var out []Event
	for _, e := range evs {
		if _, ok := e.(Changed); !ok {
			out = append(out, e)
		}
	}
	return out
}

func TestEditing(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{"typing", []string{"hello"}, "hello|"},
		{"left and right", []string{"abc", kLeft, kLeft}, "a|bc"},
		{"left then right", []string{"abc", kLeft, kLeft, kRight}, "ab|c"},
		{"left at the start stays", []string{"a", kLeft, kLeft, kLeft}, "|a"},
		{"right at the end stays", []string{"a", kRight, kRight}, "a|"},
		{"ctrl+b and ctrl+f", []string{"abc", ctrl('b'), ctrl('b'), ctrl('f')}, "ab|c"},
		{"backspace", []string{"abc", kBS}, "ab|"},
		{"backspace in the middle", []string{"abc", kLeft, kBS}, "a|c"},
		{"backspace at the start does nothing", []string{"abc", kHome, kBS}, "|abc"},
		{"delete", []string{"abc", kHome, kDel}, "|bc"},
		{"delete at the end does nothing", []string{"abc", kDel}, "abc|"},
		{"ctrl+d deletes forward when there is text", []string{"abc", kHome, ctrl('d')}, "|bc"},
		{"backspace joins lines", []string{"ab", kAltEnter, "cd", kHome, kBS}, "ab|cd"},
		{"delete joins lines", []string{"ab", kAltEnter, "cd", kUp, kEnd, kDel}, "ab|cd"},
		{"home and end are per line", []string{"ab", kAltEnter, "cd", kHome}, "ab\n|cd"},
		{"end is per line", []string{"ab", kAltEnter, "cd", kUp, kEnd}, "ab|\ncd"},
		{"ctrl+a and ctrl+e", []string{"ab", kAltEnter, "cd", ctrl('a')}, "ab\n|cd"},
		{"ctrl+e", []string{"ab", kAltEnter, "cd", kUp, ctrl('e')}, "ab|\ncd"},
		{"ctrl+home and ctrl+end are per buffer", []string{"ab", kAltEnter, "cd", kCtrlHome}, "|ab\ncd"},
		{"ctrl+end", []string{"ab", kAltEnter, "cd", kCtrlHome, kCtrlEnd}, "ab\ncd|"},
		{"alt+b", []string{"foo bar  baz", alt("b")}, "foo bar  |baz"},
		{"alt+b twice", []string{"foo bar  baz", alt("b"), alt("b")}, "foo |bar  baz"},
		{"alt+b to the start", []string{"foo", alt("b"), alt("b")}, "|foo"},
		{"alt+f", []string{"foo bar", kHome, alt("f")}, "foo| bar"},
		{"alt+f twice", []string{"foo bar", kHome, alt("f"), alt("f")}, "foo bar|"},
		{"ctrl+left and ctrl+right", []string{"foo bar", kCtrlLeft, kCtrlLeft, kCtrlRight}, "foo| bar"},
		{"punctuation separates words", []string{"foo.bar", alt("b")}, "foo.|bar"},
		{"underscore and digits are word characters", []string{"a_b1 c", alt("b"), alt("b")}, "|a_b1 c"},
		{"words cross lines", []string{"foo", kAltEnter, "bar", kHome, alt("b")}, "|foo\nbar"},
		{"alt+< and alt+>", []string{"ab", kAltEnter, "cd", alt("<")}, "|ab\ncd"},
		{"newline with alt+enter", []string{"ab", kAltEnter, "cd"}, "ab\ncd|"},
		{"newline with shift+enter", []string{"ab", kShiftEnter, "cd"}, "ab\ncd|"},
		{"newline with ctrl+j", []string{"ab", ctrl('j'), "cd"}, "ab\ncd|"},
		{"newline in the middle", []string{"abcd", kLeft, kLeft, kAltEnter}, "ab\n|cd"},
		{"trailing backslash then enter is a newline", []string{"ab\\", kEnter, "cd"}, "ab\ncd|"},
		{"a backslash inside the line is text", []string{"a\\b", kHome, kRight, kRight}, "a\\|b"},
		{"tab does not insert anything without a completer", []string{"ab", kTab}, "ab|"},
		{"F-keys and PgUp change nothing", []string{"ab", "\x1bOP", kPgUp, kPgDn, "\x1b[2~"}, "ab|"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send(c.keys...)
			if got := r.state(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestKillAndYank(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{"ctrl+k kills to the end of the line", []string{"hello", kHome, kRight, ctrl('k')}, "h|"},
		{"ctrl+k then ctrl+y restores", []string{"hello", kHome, kRight, ctrl('k'), ctrl('y')}, "hello|"},
		{"ctrl+k at the end of a line joins the next", []string{"ab", kAltEnter, "cd", kUp, kEnd, ctrl('k')}, "ab|cd"},
		{"ctrl+k at the end of the buffer does nothing", []string{"ab", ctrl('k')}, "ab|"},
		{"ctrl+u kills to the start of the line", []string{"hello world", ctrl('u')}, "|"},
		{"ctrl+u in the middle", []string{"hello world", kLeft, kLeft, kLeft, kLeft, kLeft, ctrl('u')}, "|world"},
		{"ctrl+u only takes the current line", []string{"ab", kAltEnter, "cd", ctrl('u')}, "ab\n|"},
		{"ctrl+u at the start of a line joins it to the one above", []string{"ab", kAltEnter, "cd", kHome, ctrl('u')}, "ab|cd"},
		{"ctrl+w kills the previous word", []string{"hello world", ctrl('w')}, "hello |"},
		{"ctrl+w twice", []string{"hello world", ctrl('w'), ctrl('w')}, "|"},
		{"ctrl+w skips trailing space", []string{"hello   ", ctrl('w')}, "|"},
		{"alt+d kills the next word", []string{"foo bar baz", kHome, alt("d")}, "| bar baz"},
		{"alt+backspace kills the previous word", []string{"foo bar", alt(kBS)}, "foo |"},
		{"successive ctrl+w yank back as one piece", []string{"one two three", ctrl('w'), ctrl('w'), ctrl('y')}, "one two three|"},
		{"successive alt+d yank back as one piece", []string{"a b c", kHome, alt("d"), alt("d"), ctrl('y')}, "a b| c"},
		{"a move between kills starts a new entry", []string{"one two", ctrl('w'), kLeft, ctrl('w'), ctrl('y')}, "one| "},
		{"yank with an empty ring does nothing", []string{"ab", ctrl('y')}, "ab|"},
		{"alt+y replaces the yank with the previous kill", []string{"abc", ctrl('a'), ctrl('k'), "x", ctrl('a'), ctrl('k'), ctrl('y'), alt("y")}, "abc|"},
		{"alt+y without a yank does nothing", []string{"ab", ctrl('k'), alt("y")}, "ab|"},
		{"yank into the middle", []string{"abcd", kHome, kRight, kRight, ctrl('k'), kHome, ctrl('y')}, "cd|ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send(c.keys...)
			if got := r.state(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestKillRingIsSmall(t *testing.T) {
	r := newRig(t, Options{})
	for i := 0; i < 20; i++ {
		r.send(fmt.Sprintf("w%d", i), ctrl('u'), "z") // a move (typing) between kills keeps them apart
	}
	if len(r.ed.kills) != maxKills {
		t.Errorf("the ring holds %d entries, want at most %d", len(r.ed.kills), maxKills)
	}
}

func TestTranspose(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{"at the end swaps the last two", []string{"abc", ctrl('t')}, "acb|"},
		{"in the middle swaps around the cursor and moves on", []string{"abcd", kLeft, kLeft, ctrl('t')}, "acb|d"},
		{"at the start does nothing", []string{"ab", kHome, ctrl('t')}, "|ab"},
		{"one character does nothing", []string{"a", ctrl('t')}, "a|"},
		{"not across a line break", []string{"ab", kAltEnter, "c", kHome, ctrl('t')}, "ab\n|c"},
		{"at the end of a line before a newline", []string{"abc", kAltEnter, "d", kUp, kEnd, ctrl('t')}, "acb|\nd"},
		{"wide characters move whole", []string{"中a", ctrl('t')}, "a中|"},
		{"a character with a combining mark moves whole", []string{"e\U00000301x", ctrl('t')}, "xe\U00000301|"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send(c.keys...)
			if got := r.state(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestUndoRedo(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{"a run of typing is one step", []string{"hello", kUndo}, "|"},
		{"two runs separated by a move are two steps", []string{"ab", kLeft, "X", kUndo}, "a|b"},
		{"undo twice", []string{"ab", kLeft, "X", kUndo, kUndo}, "|"},
		{"redo", []string{"hello", kUndo, kRedo}, "hello|"},
		{"redo with alt+ctrl+_", []string{"hello", kUndo, alt(kUndo)}, "hello|"},
		{"redo with kitty ctrl+shift+z", []string{"hello", kUndo, "\x1b[122;6u"}, "hello|"},
		{"ctrl+z is undo as well", []string{"hello", ctrl('z')}, "|"},
		{"a new edit clears redo", []string{"ab", kUndo, "c", kRedo}, "c|"},
		{"a run of backspaces is one step", []string{"hello", kBS, kBS, kBS, kUndo}, "hello|"},
		{"a run of deletes is one step", []string{"hello", kHome, kDel, kDel, kUndo}, "|hello"},
		{"a kill is a step", []string{"hello world", ctrl('w'), kUndo}, "hello world|"},
		{"undo a yank", []string{"abc", ctrl('a'), ctrl('k'), ctrl('y'), kUndo}, "|"},
		{"newline is its own step", []string{"ab", kAltEnter, "cd", kUndo}, "ab\n|"},
		{"undo the newline too", []string{"ab", kAltEnter, "cd", kUndo, kUndo}, "ab|"},
		{"undo on nothing does nothing", []string{kUndo, kRedo}, "|"},
		{"undo restores the cursor", []string{"abc", kLeft, kLeft, kBS, kUndo}, "a|bc"},
		{"typing after a backspace run starts a new step", []string{"abc", kBS, kBS, "x", kUndo}, "a|"},
		{"undo a clear by Esc Esc", []string{"abc", kEsc, kEsc, kUndo}, "abc|"},
		{"undo a clear by Ctrl+C", []string{"abc", ctrl('c'), kUndo}, "abc|"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send(c.keys...)
			if got := r.state(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestUndoStackIsBounded(t *testing.T) {
	r := newRig(t, Options{})
	for i := 0; i < maxUndo*2; i++ {
		r.send("x", kLeft)
	}
	if n := len(r.ed.undo); n > maxUndo+maxUndo/8 || n < maxUndo {
		t.Errorf("undo holds %d steps, want %d to %d", n, maxUndo, maxUndo+maxUndo/8)
	}
}

func TestUndoIsBoundedBySize(t *testing.T) {
	e := NewEditor(Options{})
	big := []rune(strings.Repeat("x", 60_000)) // just under the size of the buffer
	for i := 0; i < 30; i++ {
		e.replace(0, len(e.buf), big, len(big), grpNone) // fill it, then clear it: each is a step holding the whole buffer
		e.clearBuffer()
	}
	total := 0
	for _, rec := range e.undo {
		total += len(rec.old) + len(rec.new)
	}
	if total > maxUndoRunes+2*len(big) || len(e.undo) == 0 {
		t.Errorf("undo holds %d steps of %d runes in all", len(e.undo), total)
	}
	e.undoCmd() // the newest step is always there
	if string(e.buf) != string(big) {
		t.Error("undoing the last clear brings the text back")
	}
}

func TestSubmit(t *testing.T) {
	r := newRig(t, Options{})
	evs := withoutChanged(r.send("hello world  \n", kEnter))
	if !reflect.DeepEqual(evs, []Event{Submit{Text: "hello world"}}) {
		t.Fatalf("events %v", evs)
	}
	if !r.ed.Empty() || r.ed.Cursor() != 0 {
		t.Error("submit clears the editor")
	}
	if got := r.ed.History().Entries(); !reflect.DeepEqual(got, []string{"hello world"}) {
		t.Errorf("history %q", got)
	}
	// an empty or whitespace-only prompt is not sent
	if evs := withoutChanged(r.send(kEnter, "   ", kEnter)); len(evs) != 0 {
		t.Errorf("empty submit: %v", evs)
	}
	// multi-line, cursor in the middle: the whole buffer is sent
	r.ed.Reset()
	r.send("one", kAltEnter, "two", kUp, kHome)
	evs = withoutChanged(r.send(kEnter))
	if !reflect.DeepEqual(evs, []Event{Submit{Text: "one\ntwo"}}) {
		t.Errorf("multi-line submit: %v", evs)
	}
	// Submit comes before Changed
	r.send("x")
	all := r.send(kEnter)
	if _, ok := all[0].(Submit); !ok {
		t.Errorf("Submit should come first: %v", all)
	}
	if _, ok := all[len(all)-1].(Changed); !ok {
		t.Errorf("Changed should come last: %v", all)
	}
}

func TestEscClearsOnTheSecondPress(t *testing.T) {
	r := newRig(t, Options{})
	r.send("abc")
	evs := r.send(kEsc)
	if !r.ed.EscArmed() || r.state() != "abc|" {
		t.Fatalf("first Esc only arms: armed=%v %q", r.ed.EscArmed(), r.state())
	}
	if !reflect.DeepEqual(evs, []Event{Changed{}}) {
		t.Errorf("arming is a change the view shows: %v", evs)
	}
	if lines := r.ed.View(40).Plain(); lines[len(lines)-1] != "  "+EscHint {
		t.Errorf("the hint is shown: %q", lines)
	}
	r.send(kEsc)
	if r.ed.EscArmed() || r.state() != "|" {
		t.Fatalf("second Esc clears: armed=%v %q", r.ed.EscArmed(), r.state())
	}
	// another key in between disarms
	r.send("abc", kEsc, kLeft, kEsc)
	if r.state() != "ab|c" || !r.ed.EscArmed() {
		t.Errorf("a key between two Esc presses disarms: %q armed=%v", r.state(), r.ed.EscArmed())
	}
	// Esc on an empty buffer is not ours
	r.send(kEsc, kEsc) // disarm and clear
	evs = withoutChanged(r.send(kEsc))
	if !reflect.DeepEqual(evs, []Event{Unhandled{Key: ks(Esc, 0)}}) {
		t.Errorf("Esc on an empty buffer: %v", evs)
	}
}

func TestInterruptEOFAndRedraw(t *testing.T) {
	r := newRig(t, Options{})
	if evs := withoutChanged(r.send(ctrl('c'))); !reflect.DeepEqual(evs, []Event{Interrupt{Cleared: false}}) {
		t.Errorf("Ctrl+C on an empty buffer: %v", evs)
	}
	r.send("abc")
	if evs := withoutChanged(r.send(ctrl('c'))); !reflect.DeepEqual(evs, []Event{Interrupt{Cleared: true}}) || !r.ed.Empty() {
		t.Errorf("Ctrl+C with text clears it first: %v %q", evs, r.state())
	}
	if evs := withoutChanged(r.send(ctrl('d'))); !reflect.DeepEqual(evs, []Event{EOF{}}) {
		t.Errorf("Ctrl+D on an empty buffer: %v", evs)
	}
	r.send("ab")
	if evs := withoutChanged(r.send(ctrl('d'))); len(evs) != 0 {
		t.Errorf("Ctrl+D with text is a delete: %v", evs)
	}
	if evs := withoutChanged(r.send(ctrl('l'))); !reflect.DeepEqual(evs, []Event{Redraw{}}) {
		t.Errorf("Ctrl+L: %v", evs)
	}
}

func TestUnhandledKeysAreReported(t *testing.T) {
	r := newRig(t, Options{})
	for in, want := range map[string]Key{
		kShiftTab:    ks(Tab, Shift),
		"\x1bOP":     ks(F1, 0),
		kPgUp:        ks(PgUp, 0),
		"\x1b[2~":    ks(Insert, 0),
		ctrl('o'):    kr('o', Ctrl),
		"\x1b[1;5A":  ks(Up, Ctrl),
		alt("x"):     kr('x', Alt),
		"\x1b[1;3P":  ks(F1, Alt),
		"\x1b[27;5u": ks(Esc, Ctrl),
	} {
		evs := withoutChanged(r.send(in))
		if !reflect.DeepEqual(evs, []Event{Unhandled{Key: want}}) {
			t.Errorf("%q: %v, want Unhandled{%v}", in, evs, want)
		}
	}
}

func TestChangedIsReportedWhenAndOnlyWhenTheViewChanges(t *testing.T) {
	r := newRig(t, Options{})
	if evs := r.send("a"); !reflect.DeepEqual(evs, []Event{Changed{}}) {
		t.Errorf("typing: %v", evs)
	}
	if evs := r.send(kLeft); !reflect.DeepEqual(evs, []Event{Changed{}}) {
		t.Errorf("moving: %v", evs)
	}
	if evs := r.send(kLeft); len(evs) != 0 {
		t.Errorf("a move that goes nowhere changes nothing: %v", evs)
	}
	if evs := r.send(kBS); len(evs) != 0 {
		t.Errorf("a delete that deletes nothing changes nothing: %v", evs)
	}
}

func TestWideCombiningAndEmoji(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want string
	}{
		{"wide rune is one step", []string{"a中b", kLeft, kLeft}, "a|中b"},
		{"backspace over a wide rune", []string{"a中", kBS}, "a|"},
		{"combining mark stays with its base on Left", []string{"e\U00000301x", kLeft, kLeft}, "|e\U00000301x"},
		{"backspace removes base and mark together", []string{"ae\U00000301", kBS}, "a|"},
		{"delete removes base and mark together", []string{"e\U00000301x", kHome, kDel}, "|x"},
		{"right skips base and mark together", []string{"e\U00000301x", kHome, kRight}, "e\U00000301|x"},
		{"a mark typed after its base joins it", []string{"e", "\U00000301", kLeft}, "|e\U00000301"},
		{"emoji with a skin tone", []string{"a👍🏽b", kLeft, kLeft}, "a|👍🏽b"},
		{"zero-width-joiner sequence is one cluster", []string{"x👨\U0000200d👩\U0000200d👧y", kLeft, kLeft}, "x|👨\U0000200d👩\U0000200d👧y"},
		{"backspace over a joiner sequence", []string{"x👨\U0000200d👩\U0000200d👧", kBS}, "x|"},
		{"emoji with a variation selector", []string{"❤\U0000fe0fx", kHome, kRight}, "❤\U0000fe0f|x"},
		{"hangul and CJK", []string{"한글中文", kLeft, kBS}, "한글|文"},
		{"alt+b over CJK letters", []string{"中文 abc", alt("b"), alt("b")}, "|中文 abc"},
		{"kill a word of wide runes", []string{"ab 中文", ctrl('w')}, "ab |"},
		{"a leading orphan mark is its own cluster", []string{"\U00000301a", kHome, kRight}, "\U00000301|a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Options{})
			r.send(c.keys...)
			if got := r.state(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestVerticalMovementKeepsTheColumn(t *testing.T) {
	r := newRig(t, Options{})
	r.send("abcdef", kAltEnter, "ab", kAltEnter, "abcdef") // cursor at the end of line 3, column 6
	r.send(kUp)
	if got := r.state(); got != "abcdef\nab|\nabcdef" {
		t.Fatalf("up to the short line: %q", got)
	}
	r.send(kUp)
	if got := r.state(); got != "abcdef|\nab\nabcdef" {
		t.Fatalf("up again finds the preferred column 6 again: %q", got)
	}
	r.send(kDown, kDown)
	if got := r.state(); got != "abcdef\nab\nabcdef|" {
		t.Fatalf("down twice: %q", got)
	}
	// a horizontal move forgets the preferred column
	r.send(kUp, kLeft, kUp)
	if got := r.state(); got != "a|bcdef\nab\nabcdef" {
		t.Fatalf("after a horizontal move: %q", got)
	}
	// Ctrl+P and Ctrl+N are the same
	r.send(ctrl('n'), ctrl('n'))
	if got := r.state(); got != "abcdef\nab\na|bcdef" {
		t.Fatalf("ctrl+n: %q", got)
	}
	r.send(ctrl('p'))
	if got := r.state(); got != "abcdef\na|b\nabcdef" {
		t.Fatalf("ctrl+p: %q", got)
	}
}

func TestVerticalMovementIsByDisplayColumn(t *testing.T) {
	r := newRig(t, Options{})
	r.send("中中中", kAltEnter, "abcdefgh")
	r.send(kUp) // column 8 on a line 6 cells wide: the end
	if got := r.state(); got != "中中中|\nabcdefgh" {
		t.Fatalf("got %q", got)
	}
	r.send(kHome, kRight, kDown) // after one wide rune: column 2
	if got := r.state(); got != "中中中\nab|cdefgh" {
		t.Fatalf("got %q", got)
	}
	r.send(kUp)
	if got := r.state(); got != "中|中中\nabcdefgh" {
		t.Fatalf("back up: %q", got)
	}
	r.send(kDown, kRight, kUp) // column 3 is in the middle of the second wide rune: stays before it
	if got := r.state(); got != "中|中中\nabcdefgh" {
		t.Fatalf("a column inside a wide rune: %q", got)
	}
}

func TestTabsInPastedTextAreKeptAndMeasured(t *testing.T) {
	r := newRig(t, Options{})
	r.send(paste("a\tb"))
	if got := r.ed.Text(); got != "a\tb" {
		t.Fatalf("tabs survive a paste: %q", got)
	}
	if got := r.ed.colOf(len(r.ed.buf)); got != 5 {
		t.Errorf("a tab after one character reaches column 4, then b: column %d", got)
	}
	v := r.ed.View(40)
	if got := v.Plain()[0]; got != "❯ a   b" {
		t.Errorf("a tab is drawn as spaces: %q", got)
	}
	if v.CursorCol != 2+5 {
		t.Errorf("cursor column %d", v.CursorCol)
	}
	_ = cell.StringWidth
}
