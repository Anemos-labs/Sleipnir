package app

import (
	"os"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/term"
)

// How the chat looks is decided by the terminal's capabilities and one flag, in one place.
func TestLookForFollowsTheTerminalAndTheFlag(t *testing.T) {
	base := term.Caps{Color: term.ColorTrueColor, Unicode: true, Anim: true}
	if l := LookFor(base, false); !l.Unicode || !l.Anim || l.Theme.Mono {
		t.Errorf("a terminal with everything: %+v", l)
	}
	if l := LookFor(base, true); l.Anim || !l.Unicode || l.Theme.Mono {
		t.Errorf("--no-anim takes the motion and nothing else: %+v", l)
	}
	noMotion := base
	noMotion.Anim = false // REDUCE_MOTION, SLEIPNIR_ANIM=0, NO_COLOR: the terminal says so
	if l := LookFor(noMotion, false); l.Anim {
		t.Errorf("a terminal that does not want motion has none: %+v", l)
	}
	ascii := base
	ascii.Unicode = false
	if l := LookFor(ascii, false); l.Unicode || newChatLook(l).g != &asciiChat {
		t.Errorf("a locale without UTF-8 gets the ASCII glyphs: %+v", l)
	}
	mono := base
	mono.Color, mono.Anim = term.ColorNone, false
	l := LookFor(mono, false)
	if !l.Theme.Mono || !newChatLook(l).mono {
		t.Errorf("NO_COLOR gets attributes where colours were: %+v", l)
	}
}

// What the program draws on is a terminal that takes escape sequences, and it needs an input that is one; a pipe is neither.
func TestACharacterDeviceThatIsNotATerminalCannotBeDrawnOn(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip(err)
	}
	defer devnull.Close()
	if CanDrawChat(devnull, devnull, func(string) string { return "" }) {
		t.Error("/dev/null is not a terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if CanDrawChat(r, w, nil) {
		t.Error("a pipe is not a terminal")
	}
}
