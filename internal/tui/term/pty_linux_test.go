//go:build linux

package term

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// hangGuard is how long a test waits for something that must happen. It is a hang guard, not a timing: a loaded CI runner
// may be slow, but a minute of nothing is a bug.
const hangGuard = 2 * time.Minute

// openPTY opens a pseudo-terminal pair. master is the side a terminal emulator would hold, slave is what a program sees as
// its terminal. The test is skipped where a pty cannot be made (a sandbox without /dev/ptmx).
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty here: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("cannot unlock the pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("cannot name the pty: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Skipf("cannot open the pty's slave: %v", err)
	}
	t.Cleanup(func() { s.Close(); m.Close() })
	return m, s
}

func setWinsize(t *testing.T, master *os.File, w, h int) {
	t.Helper()
	ws := &unix.Winsize{Col: uint16(w), Row: uint16(h)}
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, ws); err != nil {
		t.Fatalf("set window size: %v", err)
	}
}

func lflags(t *testing.T, f *os.File) uint32 {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read termios: %v", err)
	}
	return tio.Lflag
}

func TestDetectOnARealTerminal(t *testing.T) {
	m, s := openPTY(t)
	setWinsize(t, m, 101, 31)
	env := envOf(map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": "en_US.UTF-8", "COLUMNS": "1", "LINES": "1"})

	got := Detect(env, s)
	want := Caps{Color: ColorTrueColor, Unicode: true, Width: 101, Height: 31, BracketedPaste: true, Anim: true}
	if got != want {
		t.Errorf("Detect on a pty:\n got  %+v\n want %+v", got, want)
	}
	if sz, err := GetSize(s); err != nil || sz != (Size{Width: 101, Height: 31}) {
		t.Errorf("GetSize = %+v, %v", sz, err)
	}

	// A terminal that reports no size (a fresh pty) falls back to the environment, then to the default.
	m2, s2 := openPTY(t)
	setWinsize(t, m2, 0, 0)
	if c := Detect(envOf(map[string]string{"TERM": "xterm", "COLUMNS": "90", "LINES": "33"}), s2); c.Width != 90 || c.Height != 33 || c.Dumb {
		t.Errorf("size unknown on a tty: %+v", c)
	}
	if c := Detect(envOf(map[string]string{"TERM": "xterm"}), s2); c.Width != DefaultWidth || c.Height != DefaultHeight {
		t.Errorf("size unknown, no environment: %+v", c)
	}
}

func TestMakeRawAndRestore(t *testing.T) {
	_, s := openPTY(t)
	const cooked = unix.ICANON | unix.ECHO
	if lflags(t, s)&cooked != cooked {
		t.Skip("the pty does not start in cooked mode")
	}
	restore, err := MakeRaw(s)
	if err != nil {
		t.Fatal(err)
	}
	if l := lflags(t, s); l&(unix.ICANON|unix.ECHO|unix.ISIG) != 0 {
		t.Fatalf("not raw: lflag %#x", l)
	}

	// One restore from the main goroutine, seven from "signal handlers" at once: every one of them must see the terminal
	// restored when it returns, and every one returns the same (nil) result.
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = restore()
			if l := lflags(t, s); l&cooked != cooked {
				t.Errorf("restore %d returned with the terminal still raw: lflag %#x", i, l)
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("restore %d: %v", i, err)
		}
	}
	if err := restore(); err != nil {
		t.Errorf("a second restore after the first is a no-op, got %v", err)
	}
	if l := lflags(t, s); l&cooked != cooked {
		t.Errorf("after restore: lflag %#x", l)
	}
}

// A restore that comes after the caller changed the terminal again must still put back what MakeRaw saw (it is a snapshot,
// not a toggle), and calling it twice must not undo somebody else's later change.
func TestRestoreIsASnapshotNotAToggle(t *testing.T) {
	_, s := openPTY(t)
	restore, err := MakeRaw(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	again, err := MakeRaw(s) // somebody else (a child process, a second program phase) makes it raw again
	if err != nil {
		t.Fatal(err)
	}
	defer again()
	if err := restore(); err != nil { // the old restore is spent: it must not touch the terminal a second time
		t.Fatal(err)
	}
	if l := lflags(t, s); l&(unix.ICANON|unix.ECHO) != 0 {
		t.Errorf("a spent restore reset the terminal a second time: lflag %#x", l)
	}
}

func TestWatchResizeDeliversTheNewSize(t *testing.T) {
	m, s := openPTY(t)
	setWinsize(t, m, 100, 30)
	sizes, stop := WatchResize(s)
	defer stop()

	want := Size{Width: 123, Height: 45}
	setWinsize(t, m, want.Width, want.Height)
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	// A real SIGWINCH from the terminal this test runs in could arrive first, carrying the old size; wait for the new one.
	timeout := time.After(hangGuard)
	for {
		select {
		case got := <-sizes:
			if got == want {
				return
			}
		case <-timeout:
			t.Fatal("no resize notification")
		}
	}
}

func TestWatchResizeLatestWins(t *testing.T) {
	m, s := openPTY(t)
	sizes, stop := WatchResize(s)
	defer stop()

	// Three resizes and three signals while nobody receives. The channel holds one value, so the receiver sees the current
	// size and not a queue of old ones: whatever it receives, the sizes never go backwards, and the last one arrives.
	for i := 1; i <= 3; i++ {
		setWinsize(t, m, 100+i, 30+i)
		if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
			t.Fatal(err)
		}
	}
	want := Size{Width: 103, Height: 33}
	last := 0
	timeout := time.After(hangGuard)
	for {
		select {
		case got := <-sizes:
			if got.Width < last {
				t.Fatalf("a stale size %+v arrived after a newer one (width %d)", got, last)
			}
			last = got.Width
			if got == want {
				return
			}
		case <-timeout:
			t.Fatal("the final size never arrived")
		}
	}
}
