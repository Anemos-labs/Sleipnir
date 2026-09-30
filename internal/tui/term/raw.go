package term

import (
	"errors"
	"os"
	"sync"

	xterm "golang.org/x/term"
)

// Size is the dimensions of a terminal in cells.
type Size struct{ Width, Height int }

// GetSize returns the size of the terminal behind f. It fails (and returns zeros) when f is nil or not a terminal, or when
// the terminal reports no size; callers that need a number anyway use Detect, which falls back to a default.
func GetSize(f *os.File) (Size, error) {
	if f == nil {
		return Size{}, errors.New("term: no file")
	}
	w, h, err := xterm.GetSize(int(f.Fd()))
	if err != nil {
		return Size{}, err
	}
	if w <= 0 || h <= 0 {
		return Size{}, errors.New("term: the terminal reports no size")
	}
	return Size{Width: w, Height: h}, nil
}

// noRestore is what MakeRaw returns when it failed, so that a deferred restore is always safe to call.
func noRestore() error { return nil }

// MakeRaw puts the terminal behind f (normally os.Stdin) into raw mode: no echo, no line editing, no signal keys (Ctrl-C
// arrives as the byte 3), no input or output translation, so a program that does its own key decoding sees every byte, and
// a program that draws must write "\r\n" where it means a new line.
//
// It returns a function that puts the terminal back as it was. That function is idempotent and safe to call from any
// goroutine, including one that handles a signal while the main goroutine's deferred call is running: the first call does the
// work, the others wait for it and return the same result (nil, or the error of the failed restore). It holds f, so the
// descriptor cannot be recycled under it.
//
// When f is not a terminal, or the terminal refuses, MakeRaw returns the error and a restore that does nothing, so
// "defer restore()" is correct on every path and the terminal is left as it was.
func MakeRaw(f *os.File) (restore func() error, err error) {
	if f == nil {
		return noRestore, errors.New("term: no file")
	}
	state, err := xterm.MakeRaw(int(f.Fd()))
	if err != nil {
		return noRestore, err
	}
	var (
		once sync.Once
		rerr error
	)
	return func() error {
		once.Do(func() { rerr = xterm.Restore(int(f.Fd()), state) })
		return rerr
	}, nil
}
