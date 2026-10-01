package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

func hintText(hs []widget.Hint) string {
	var parts []string
	for _, h := range hs {
		parts = append(parts, h.Text)
	}
	return strings.Join(parts, " | ")
}

// A pipe is not a terminal: nothing is drawn, nothing is changed, and the caller is told so before it has started anything else.
func TestNoTerminalOnPipes(t *testing.T) {
	in, w1, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r2, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer w1.Close()
	defer r2.Close()
	env := func(k string) string {
		if k == "TERM" {
			return "xterm-256color"
		}
		return ""
	}
	o := TTYOptions{In: in, Out: out, Env: env}
	if HaveTerminal(o) {
		t.Error("two pipes are not a terminal")
	}
	src := OpenLive(context.Background(), os.DevNull, LiveOptions{WaitForFile: true})
	defer src.Close()
	err = RunTTY(context.Background(), src, o)
	if !errors.Is(err, ErrNoTerminal) {
		t.Errorf("RunTTY on pipes: %v, want ErrNoTerminal", err)
	}
	out.Close() // what was written to the pipe is all there will be
	buf := make([]byte, 64)
	if n, _ := r2.Read(buf); n != 0 {
		t.Errorf("a program that finds no terminal must not write: %q", buf[:n])
	}
}

// The key that leaves says what it does: a program that prints something when the screen closes, or stops the session with it,
// says that in the row of keys, and says it for the session as it stands (under way, or ended).
func TestTheQuitKeySaysWhatItDoes(t *testing.T) {
	ended := &state.Snapshot{}
	ended.Session.Ended = true
	live := &state.Snapshot{}
	for _, c := range []struct {
		name string
		snap *state.Snapshot
		mode Mode
		want string
	}{
		{"default, under way", live, Mode{}, "q quit"},
		{"default, ended", ended, Mode{}, "q quit"},
		{"labels, under way", live, Mode{QuitLive: "stop the demo", QuitEnded: "leave, then the report"}, "q stop the demo"},
		{"labels, ended", ended, Mode{QuitLive: "stop the demo", QuitEnded: "leave, then the report"}, "q leave, then the report"},
		{"only the ended label, under way", live, Mode{QuitEnded: "see the report"}, "q quit"},
		{"only the live label, ended", ended, Mode{QuitLive: "stop"}, "q quit"},
		{"no snapshot yet", nil, Mode{QuitLive: "stop"}, "q stop"},
	} {
		got := hintText(hintsFor(Scene{Snap: c.snap, Mode: c.mode}))
		if !strings.HasSuffix(got, c.want) {
			t.Errorf("%s: the keys are %q, want them to end with %q", c.name, got, c.want)
		}
	}
}
