package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/reee344/sleipnir/internal/tui/input"
	"github.com/reee344/sleipnir/internal/tui/render"
	"github.com/reee344/sleipnir/internal/tui/term"
	"github.com/reee344/sleipnir/internal/tui/widget"
	xterm "golang.org/x/term"
)

// ErrNoTerminal is what RunTTY returns when input or output is not a terminal that can be drawn on: a pipe, a file, TERM=dumb.
// The caller says what to do instead (a plain screen, a recording).
var ErrNoTerminal = errors.New("a full-screen view needs a terminal (standard input and output a terminal that takes escape sequences)")

// TTYOptions are what RunTTY needs from the world around the program.
type TTYOptions struct {
	// In and Out are the terminal (os.Stdin and os.Stdout when nil).
	In, Out *os.File
	// Env reads the environment, as term.Detect does (os.Getenv when nil).
	Env func(string) string
	// View, Agent and NoAnim start the program where the user asked; the animation is off as well when the terminal does not
	// want it (NO_COLOR, REDUCE_MOTION, SLEIPNIR_ANIM=0).
	View   View
	Agent  string
	NoAnim bool
}

// RunTTY shows the session on the terminal until the user quits: it puts the terminal in raw mode, draws on the alternate screen,
// reads keys, follows the window's size and ticks the animation at UIFPS, and puts everything back the way it was on every way
// out, a panic included. It returns ErrNoTerminal, having touched nothing, when there is no terminal to draw on.
func RunTTY(ctx context.Context, src Source, o TTYOptions) error {
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
		return ErrNoTerminal
	}
	restore, err := term.MakeRaw(in)
	if err != nil {
		return fmt.Errorf("the terminal would not go into raw mode: %w", err)
	}
	defer func() { _ = restore() }() // a terminal that will not be restored cannot be helped, and its error would hide why the program ended
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sizes, stopResize := term.WatchResize(out)
	defer stopResize()
	ticker := time.NewTicker(time.Second / UIFPS)
	defer ticker.Stop()

	pal := widget.DefaultPalette()
	if caps.Color == term.ColorNone {
		pal = widget.MonoPalette()
	}
	return Run(ctx, Config{
		Src: src, Screen: render.NewScreen(out, caps), Keys: ReadKeys(ctx, in), Sizes: sizes, Tick: ticker.C,
		Pal: pal, NoAnim: o.NoAnim || !caps.Anim, View: o.View, Agent: o.Agent,
	})
}

// ReadKeys reads r (a terminal in raw mode) and sends the keys it decodes. The channel closes when r ends or fails or ctx is done.
// A lone Esc is told from the start of an escape sequence by waiting for the grace time the decoder asks for; a read that blocks
// cannot be interrupted, so the goroutine that does it may outlive ctx until the next byte arrives (or the process ends, which is
// what follows a full-screen program).
func ReadKeys(ctx context.Context, r io.Reader) <-chan input.Key {
	out := make(chan input.Key, 16)
	chunks := make(chan []byte)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				c := append([]byte(nil), buf[:n]...)
				select {
				case chunks <- c:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				close(chunks)
				return
			}
		}
	}()
	go func() {
		defer close(out)
		var dec input.Decoder
		var timer *time.Timer
		var grace <-chan time.Time
		send := func(keys []input.Key) bool {
			for _, k := range keys {
				select {
				case out <- k:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-chunks:
				if !ok {
					send(dec.Flush())
					return
				}
				if !send(dec.Feed(c)) {
					return
				}
				if timer != nil {
					timer.Stop()
					timer, grace = nil, nil
				}
				if dec.Pending() {
					timer = time.NewTimer(dec.Grace())
					grace = timer.C
				}
			case <-grace:
				timer, grace = nil, nil
				if !send(dec.Flush()) {
					return
				}
			}
		}
	}()
	return out
}
