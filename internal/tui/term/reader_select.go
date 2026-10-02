//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package term

import (
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// selectReader waits in select(2) for the terminal or for the pipe Cancel writes to, and reads only what select says is there. (select
// and not poll or the runtime's own poller: macOS's poll and kqueue do not work with terminals.)
type selectReader struct {
	f            *os.File // held, so that the descriptor is not recycled under a read
	fd, wake     int
	wakeR, wakeW *os.File
	mu           sync.Mutex // held for the length of a Read
	stopped      bool
	once         sync.Once
}

func newReader(f *os.File) Reader {
	fd := int(f.Fd())
	r, w, err := os.Pipe()
	if err != nil || fd >= unix.FD_SETSIZE || int(r.Fd()) >= unix.FD_SETSIZE {
		if err == nil {
			r.Close()
			w.Close()
		}
		return plainReader{f}
	}
	return &selectReader{f: f, fd: fd, wake: int(r.Fd()), wakeR: r, wakeW: w}
}

func (r *selectReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for !r.stopped {
		var set unix.FdSet
		set.Set(r.fd)
		set.Set(r.wake)
		if _, err := unix.Select(max(r.fd, r.wake)+1, &set, nil, nil, nil); err != nil {
			if err == unix.EINTR {
				continue
			}
			return 0, err
		}
		if set.IsSet(r.wake) {
			break
		}
		n, err := unix.Read(r.fd, p)
		switch {
		case err == unix.EINTR || err == unix.EAGAIN:
			continue
		case err != nil:
			return 0, err
		case n == 0:
			return 0, io.EOF
		}
		return n, nil
	}
	return 0, os.ErrClosed
}

// Cancel wakes a read that waits, lets it leave, and closes the pipe.
func (r *selectReader) Cancel() {
	r.once.Do(func() {
		_, _ = r.wakeW.Write([]byte{0}) // a read that waits in select sees the pipe ready and returns
		r.mu.Lock()                     // and a read that is in flight has left before Cancel does
		r.stopped = true
		r.mu.Unlock()
		r.wakeR.Close()
		r.wakeW.Close()
	})
}
