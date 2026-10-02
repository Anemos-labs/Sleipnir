package term

import (
	"io"
	"os"
)

// A Reader is a terminal's input that can be stopped. A plain Read of os.Stdin blocks in the kernel until a key is typed, and cannot be
// called off: a program that ends while the process lives on (the chat, which starts itself again after /restart, or `sleipnir login`
// after /login) leaves that read behind, and it takes the first thing typed for the next program on the terminal, a line of a key
// included. Cancel is the end of it: it returns when no read is in flight, and after it none starts, so that every key is the next
// program's. Reads after Cancel fail with os.ErrClosed.
type Reader interface {
	io.Reader
	Cancel()
}

// NewReader returns a Reader of the terminal behind f. Where a read of a terminal cannot be woken (Windows) it is a plain one, and
// Cancel does nothing.
func NewReader(f *os.File) Reader { return newReader(f) }

// plainReader is a Reader that cannot be stopped: a read of the file, and a Cancel that does nothing.
type plainReader struct{ *os.File }

func (plainReader) Cancel() {}
