//go:build !unix

package term

import (
	"os"
	"sync"
)

// WatchResize is a stub on this platform: there is no SIGWINCH, so the channel never delivers a size and stop closes it.
// A caller that needs resizes here polls GetSize. No goroutine is started.
func WatchResize(f *os.File) (sizes <-chan Size, stop func()) {
	out := make(chan Size)
	var once sync.Once
	return out, func() { once.Do(func() { close(out) }) }
}
