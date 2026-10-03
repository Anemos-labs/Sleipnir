package term

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT asks the console behind f to understand colour and cursor sequences (Windows 10 and later; Windows Terminal already does). It
// reports whether it does: the screen of the chat is drawn with them, and a console that does not would print them as text.
func enableVT(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
