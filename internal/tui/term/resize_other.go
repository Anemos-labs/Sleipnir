//go:build !unix

package term

import "os"

// WatchResize reports changes of the size of the terminal behind f. There is no SIGWINCH here, so the size is read a few times a second
// (a window that is dragged wider or narrower is seen within a tenth of a second or so); see watchBySize for the rest.
func WatchResize(f *os.File) (sizes <-chan Size, stop func()) {
	return watchBySize(func() (Size, error) { return GetSize(f) }, pollInterval)
}
