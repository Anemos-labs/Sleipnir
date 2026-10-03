package term

import (
	"sync"
	"time"
)

// pollInterval is how often a platform without a resize signal looks at the size of the terminal.
const pollInterval = 120 * time.Millisecond

// watchBySize is WatchResize for a platform that has no SIGWINCH (Windows): it reads the size every interval and sends it when it is not
// the one it saw last. The first size read is the one the program started with and is not sent. The channel holds at most one size, the
// latest wins, and stop ends the goroutine, waits for it and closes the channel; it is idempotent.
func watchBySize(read func() (Size, error), every time.Duration) (sizes <-chan Size, stop func()) {
	out := make(chan Size, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer close(out)
		last, haveLast := Size{}, false
		if s, err := read(); err == nil {
			last, haveLast = s, true
		}
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				s, err := read()
				if err != nil || (haveLast && s == last) {
					continue
				}
				last, haveLast = s, true
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
			close(done)
			<-finished
		})
	}
}
