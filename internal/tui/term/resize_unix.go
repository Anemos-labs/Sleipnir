//go:build unix

package term

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// WatchResize reports changes of the size of the terminal behind f: on every SIGWINCH it reads the new size and sends it.
// The channel holds at most one size and the latest wins, so a slow receiver sees the current size, never a backlog; a size
// that cannot be read (f is not a terminal) sends nothing.
//
// stop ends the watch: it stops the signal delivery, waits for the watcher's goroutine to finish (it is the only goroutine
// this function starts, and none outlives stop) and closes the channel, so a "for s := range sizes" loop ends. stop is
// idempotent and safe from any goroutine. On platforms without SIGWINCH, WatchResize returns a channel that never
// delivers anything and is closed by stop.
func WatchResize(f *os.File) (sizes <-chan Size, stop func()) {
	out := make(chan Size, 1)
	sig := make(chan os.Signal, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		defer close(finished)
		defer close(out)
		for {
			select {
			case <-done:
				return
			case <-sig:
				s, err := GetSize(f)
				if err != nil {
					continue
				}
				select { // latest wins: this goroutine is the only sender, so the send below cannot block
				case <-out:
				default:
				}
				out <- s
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			signal.Stop(sig)
			close(done)
			<-finished
		})
	}
}
