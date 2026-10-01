package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/render"
	"github.com/reee344/sleipnir/internal/tui/term"
	xterm "golang.org/x/term"
)

// ChatTTYOptions are what RunChatTTY needs from the world around the program.
type ChatTTYOptions struct {
	// In and Out are the terminal (os.Stdin and os.Stdout when nil).
	In, Out *os.File
	// Env reads the environment, as term.Detect does (os.Getenv when nil).
	Env func(string) string
	// NoAnim turns the motion off, as --no-anim does; it is off as well when the terminal does not want it (NO_COLOR, REDUCE_MOTION,
	// SLEIPNIR_ANIM=0).
	NoAnim bool
	// Verbose, MainAgent, History, CancelStart are ChatConfig's.
	Verbose     bool
	MainAgent   string
	History     *input.History
	CancelStart func()
}

// CanDrawChat reports whether the chat can be drawn on the terminal behind in and out: both are terminals, and the output is one that
// takes escape sequences (not TERM=dumb). It is what decides between this program and the plain line chat, which every other
// case gets. NO_COLOR does not decide it: a terminal without colour is still a terminal.
func CanDrawChat(in, out *os.File, env func(string) string) bool {
	if env == nil {
		env = os.Getenv
	}
	return xterm.IsTerminal(int(in.Fd())) && !term.Detect(env, out).Dumb
}

// RunChatTTY runs the chat on the terminal until the person leaves it: it puts the terminal in raw mode (so that Ctrl-C is a key and
// not a signal), draws inline on the terminal's own screen, reads keys, follows the window's size, ticks the clock, and puts
// everything back the way it found it on every way out, a panic included. It returns ErrNoTerminal, having touched nothing, when
// there is no terminal to draw on.
//
// A SIGINT that arrives anyway (kill -INT, or the instant before raw mode) is taken for the key it would have been.
func RunChatTTY(ctx context.Context, link *ChatLink, attach <-chan ChatAttach, o ChatTTYOptions) (ChatEnd, error) {
	in, out := o.In, o.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	env := o.Env
	if env == nil {
		env = os.Getenv
	}
	caps := term.Detect(env, out)
	if caps.Dumb || !xterm.IsTerminal(int(in.Fd())) {
		return ChatFailed, ErrNoTerminal
	}
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt)
	defer signal.Stop(sigint)
	restore, err := term.MakeRaw(in)
	if err != nil {
		return ChatFailed, fmt.Errorf("the terminal would not go into raw mode: %w", err)
	}
	defer func() { _ = restore() }() // a terminal that will not be restored cannot be helped, and its error would hide why the program ended
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sizes, stopResize := term.WatchResize(out)
	defer stopResize()

	look := LookFor(caps, o.NoAnim)
	period := time.Second / UIFPS
	if !look.Anim {
		period = 250 * time.Millisecond // the clocks that count seconds still count; nothing moves between them
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	// The renderer is told the terminal can be drawn on even when it shows no colour: what NO_COLOR asks for is that the program
	// not add colour, which the look takes care of, and not that it give up its status line and its prompt.
	rcaps := caps
	if rcaps.Color == term.ColorNone {
		rcaps.Color = term.ColorANSI16
	}
	scr := render.NewInline(out, rcaps, render.WithBracketedPaste())

	keys := mergeInterrupts(ctx, ReadKeys(ctx, in), sigint)
	return RunChat(ctx, ChatConfig{
		Screen: scr, Keys: keys, Sizes: sizes, Tick: ticker.C, Attach: attach, Link: link, Now: time.Now,
		Look: look, MainAgent: o.MainAgent, Verbose: o.Verbose, History: o.History, CancelStart: o.CancelStart,
	})
}

// mergeInterrupts is keys with a Ctrl-C added for every SIGINT. The channel closes when keys does or ctx is done.
func mergeInterrupts(ctx context.Context, keys <-chan input.Key, sigint <-chan os.Signal) <-chan input.Key {
	out := make(chan input.Key, 16)
	go func() {
		defer close(out)
		send := func(k input.Key) bool {
			select {
			case out <- k:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case k, ok := <-keys:
				if !ok || !send(k) {
					return
				}
			case <-sigint:
				if !send(input.RuneKey('c', input.Ctrl)) {
					return
				}
			}
		}
	}()
	return out
}
