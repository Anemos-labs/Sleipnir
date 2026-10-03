package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/render"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// ReadKeys and mergeInterrupts close the keys when the context is done, so a program that a signal told to stop finds two things
// ready at once: the end of its context and the end of its keys. A select chooses between ready cases at random, and the keys' end
// means "the user quit" (no error, exit status 0) where the context's means "it was told to stop" (an interruption, exit status
// 143 for SIGTERM). The first run on macOS caught `replay` exiting 0 on SIGTERM; where the loop is waiting in the select when the
// context ends the context wins, and where it is busy it is a coin toss. These tests make both ready before the loop starts, and
// run it enough times that a coin toss cannot pass them.

const raceRuns = 300

func TestRunEndedByItsContextSaysSoWhenTheKeysEndWithIt(t *testing.T) {
	src := demoReplay(t, 1)
	t.Cleanup(func() { src.Close() })
	for i := 0; i < raceRuns; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		keys := make(chan input.Key)
		close(keys)
		err := Run(ctx, Config{Src: src, Screen: newFakeScreen(100, 36), Keys: keys, Sizes: make(chan term.Size), Tick: make(chan time.Time),
			Pal: widget.DefaultPalette(), View: ViewCockpit})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: ended with %v, want the context's error: the context ended it, and the keys with it", i, err)
		}
	}
}

// The keys ending alone, with the context alive, is the terminal going away: the program ends and it is not an error.
func TestRunWhoseKeysEndWithoutTheContextEndsWithoutAnError(t *testing.T) {
	src := demoReplay(t, 1)
	t.Cleanup(func() { src.Close() })
	keys := make(chan input.Key)
	close(keys)
	err := Run(context.Background(), Config{Src: src, Screen: newFakeScreen(100, 36), Keys: keys, Sizes: make(chan term.Size), Tick: make(chan time.Time),
		Pal: widget.DefaultPalette(), View: ViewCockpit})
	if err != nil {
		t.Fatalf("the keys ended: %v", err)
	}
}

func TestChatEndedByItsContextIsInterruptedWhenTheKeysEndWithIt(t *testing.T) {
	caps := term.Caps{Color: term.ColorTrueColor, Unicode: true, Width: 80, Height: 24, BracketedPaste: true}
	for i := 0; i < raceRuns; i++ {
		bridge := &vtBridge{t: t, v: vt.New(80, 24)}
		scr := &testScreen{Inline: render.NewInline(bridge, caps, render.WithBracketedPaste(), render.WithBottomAnchor()), frames: make(chan struct{}, 1<<10)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		keys := make(chan input.Key)
		close(keys)
		end, err := RunChat(ctx, ChatConfig{Screen: scr, Keys: keys, Sizes: make(chan term.Size), Tick: make(chan time.Time),
			Attach: make(chan ChatAttach), Link: NewChatLink(), Look: defaultLook()})
		if end != ChatInterrupted || err != nil {
			t.Fatalf("run %d: the chat ended %q (%v), want %q: the context ended it, and the keys with it", i, end, err, ChatInterrupted)
		}
	}
}

func TestChatWhoseKeysEndWithoutTheContextHasQuit(t *testing.T) {
	caps := term.Caps{Color: term.ColorTrueColor, Unicode: true, Width: 80, Height: 24, BracketedPaste: true}
	bridge := &vtBridge{t: t, v: vt.New(80, 24)}
	scr := &testScreen{Inline: render.NewInline(bridge, caps, render.WithBracketedPaste(), render.WithBottomAnchor()), frames: make(chan struct{}, 1<<10)}
	keys := make(chan input.Key)
	close(keys)
	end, err := RunChat(context.Background(), ChatConfig{Screen: scr, Keys: keys, Sizes: make(chan term.Size), Tick: make(chan time.Time),
		Attach: make(chan ChatAttach), Link: NewChatLink(), Look: defaultLook()})
	if end != ChatQuit || err != nil {
		t.Fatalf("the terminal went away: the chat ended %q (%v), want %q", end, err, ChatQuit)
	}
}
