// Package ptytest runs a command on a pseudo-terminal, so that a test can drive it
// the way a person does: type a line, press Ctrl-C or Ctrl-D, resize the window,
// and wait for what the program prints.
//
// Why a terminal and not a pipe: what a program does with Ctrl-C is decided by the
// terminal, not by the program. The tty line discipline turns the byte 0x03 into a
// SIGINT for the foreground process group, flushes what was typed but not yet read,
// echoes "^C", and Ctrl-D becomes end of input only at the start of a line. A test
// that sends SIGINT with os.Process.Signal skips all of that; this package writes the
// byte and lets the kernel do the rest, so the program under test sees exactly the
// signals and input a person at a terminal would cause.
//
// The command is started as a session leader with the terminal as its controlling
// terminal and as its standard input, output and error. Everything it writes is
// collected into one transcript, which every failure message includes: a test that
// waited for text that never came says what the program printed instead.
//
// Matching works like expect(1): ExpectString and ExpectRegexp look at the output
// that has not been matched yet, and a match consumes the output up to its end, so
// "expect the prompt" after "expect (cancelled)" finds the prompt that followed the
// cancellation, not one printed earlier. Line ends are matched as "\n" ("\r\n", as a
// terminal prints them, is folded). The terminal echoes what is typed, as a real one
// does: choose the text to wait for so that it is not also what was typed.
//
// Linux and macOS are supported. Elsewhere Start skips the test with the reason.
//
// Timeouts in this package are hang guards: they say how long to wait for something
// that must happen, and a test never asserts that it took less. Use Guard unless a
// test is about the timeout itself.
package ptytest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Guard is the hang guard for waits that must succeed: generous enough for a loaded
// machine running several race-instrumented test binaries at once, short enough that
// a hung program fails a test instead of the whole job.
const Guard = 60 * time.Second

// drainLimit is the longest the end of a session waits for its output to be read (a hang
// guard: the reader takes it in microseconds unless it is gone).
const drainLimit = 10 * time.Second

// Default window size of a new terminal.
const (
	DefaultRows = 24
	DefaultCols = 80
)

var (
	// ErrEOF is returned (wrapped) by the wait functions when the output ended (the
	// program exited and nothing holds the terminal any more) before the wait was
	// satisfied. Use errors.Is: the error also carries the transcript.
	ErrEOF = errors.New("ptytest: the output ended")

	// ErrTimeout is returned (wrapped) when a wait ran out of time. The error also
	// carries the transcript.
	ErrTimeout = errors.New("ptytest: timed out")

	// ErrUnsupported is returned (wrapped) on a system without a pseudo-terminal that
	// this package can open.
	ErrUnsupported = errors.New("ptytest: pseudo-terminals are not supported here")
)

// Option adjusts Start.
type Option func(*config)

type config struct{ rows, cols uint16 }

// Size sets the window size the program starts with (default 24 by 80).
func Size(rows, cols uint16) Option {
	return func(c *config) { c.rows, c.cols = rows, cols }
}

// Session is a command running on a pseudo-terminal.
type Session struct {
	cmd    *exec.Cmd
	master *os.File
	// slave is the test's own copy of the terminal's other end. It stays open until the
	// command has exited, so that WaitInputRead can ask how much of what was typed the
	// program has not read yet; closing it then lets the output end.
	slave *os.File

	mu      sync.Mutex
	raw     []byte        // everything the program wrote, as written
	norm    []byte        // the same with "\r\n" folded to "\n": what the Expect functions match against
	pendCR  bool          // the last byte of raw was "\r": held back from norm until the next byte says whether "\n" follows
	pos     int           // norm[:pos] has been matched by an Expect
	eof     bool          // the output ended
	readErr error         // why, when it was not the ordinary end
	changed chan struct{} // closed and replaced whenever norm or eof changes

	exited   chan struct{} // closed when the command has been waited for
	state    *os.ProcessState
	unread   int // typed bytes nothing had read when the command exited (-1: could not be asked)
	readDone chan struct{}

	// ask is how much typed input the terminal holds that nothing has read (inputPending on the
	// slave end; a test of the waiting replaces it).
	ask func() (int, error)

	closeOnce sync.Once
}

// Start runs cmd on a new pseudo-terminal. It sets cmd's standard streams and
// SysProcAttr (the command becomes a session leader with the terminal as its
// controlling terminal); everything else in cmd is the caller's. The command is killed,
// with its whole process group, when the test ends.
func Start(t testing.TB, cmd *exec.Cmd, opts ...Option) *Session {
	t.Helper()
	c := config{rows: DefaultRows, cols: DefaultCols}
	for _, o := range opts {
		o(&c)
	}
	s, err := start(cmd, c)
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			t.Skip(err)
		}
		t.Fatalf("ptytest: starting %v: %v", cmd.Args, err)
	}
	t.Cleanup(s.Close)
	return s
}

func start(cmd *exec.Cmd, c config) (*Session, error) {
	master, slave, err := openPty()
	if err != nil {
		return nil, err
	}
	if err := setWinsize(master, c.rows, c.cols); err != nil {
		master.Close()
		slave.Close()
		return nil, fmt.Errorf("setting the window size: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = ctty()
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	s := &Session{
		cmd: cmd, master: master, slave: slave,
		changed: make(chan struct{}), exited: make(chan struct{}), readDone: make(chan struct{}),
	}
	s.ask = func() (int, error) { return inputPending(s.slave) }
	go s.readLoop()
	go func() {
		_ = cmd.Wait()
		// Some systems take the terminal from every holder when the session leader exits (macOS
		// revokes it), so after the exit the question may have no answer: -1.
		unread, err := s.ask()
		if err != nil {
			unread = -1
		}
		s.mu.Lock()
		s.state, s.unread = cmd.ProcessState, unread
		s.mu.Unlock()
		s.drain()
		// Let go of the terminal: the output ends when the last holder of the other end
		// does (the command, and anything it started that is still running).
		s.slave.Close()
		close(s.exited)
	}()
	return s, nil
}

// drain waits for the reader to take what the command wrote just before it exited. Closing the
// last descriptor of the slave end discards what is still queued on some systems (macOS), and
// the test holds one until now. It stops waiting when nothing is queued, when the queue has
// stopped shrinking (what the system reports is then not something the reader is about to
// take), or at drainLimit.
func (s *Session) drain() {
	deadline := time.Now().Add(drainLimit)
	last, stuck := -1, 0
	for time.Now().Before(deadline) {
		n, err := outputPending(s.master)
		if err != nil || n == 0 {
			return
		}
		if n == last {
			if stuck++; stuck >= 20 {
				return
			}
		} else {
			stuck = 0
		}
		last = n
		time.Sleep(time.Millisecond)
	}
}

// readLoop copies the terminal's output into the transcript until it ends.
func (s *Session) readLoop() {
	defer close(s.readDone)
	buf := make([]byte, 4096)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.append(buf[:n])
		}
		if err != nil {
			s.finish(err)
			return
		}
	}
}

func (s *Session) append(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = append(s.raw, p...)
	for _, b := range p {
		if s.pendCR {
			s.pendCR = false
			if b == '\n' {
				s.norm = append(s.norm, '\n')
				continue
			}
			s.norm = append(s.norm, '\r')
		}
		if b == '\r' {
			s.pendCR = true
			continue
		}
		s.norm = append(s.norm, b)
	}
	s.broadcast()
}

// finish records the end of the output. Reading a terminal whose other end is closed
// fails with EIO on Linux and returns end of file on macOS; both are the ordinary end.
func (s *Session) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendCR {
		s.norm = append(s.norm, '\r')
		s.pendCR = false
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EIO) {
		s.readErr = err
	}
	s.eof = true
	s.broadcast()
}

// broadcast wakes every waiter; s.mu is held.
func (s *Session) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Write types p at the terminal. The line discipline sees it as input from a
// keyboard: it is echoed, edited (backspace) and collected into lines.
func (s *Session) Write(p []byte) (int, error) { return s.master.Write(p) }

// WriteString types str at the terminal; "\n" is the Enter key.
func (s *Session) WriteString(str string) (int, error) { return s.master.Write([]byte(str)) }

// SendCtrlC types Ctrl-C (0x03). The terminal turns it into SIGINT for the
// foreground process group and discards the input that was typed but not yet read.
func (s *Session) SendCtrlC() error {
	_, err := s.master.Write([]byte{0x03})
	return err
}

// SendCtrlD types Ctrl-D (0x04): at the start of a line, end of input for the
// program's next read; in the middle of one, it hands over the part typed so far.
func (s *Session) SendCtrlD() error {
	_, err := s.master.Write([]byte{0x04})
	return err
}

// Resize sets the window size; the program gets SIGWINCH.
func (s *Session) Resize(rows, cols uint16) error { return setWinsize(s.master, rows, cols) }

// Signal sends sig to the command itself (not to its group, and not through the
// terminal): for signals the keyboard cannot type, such as SIGTERM.
func (s *Session) Signal(sig os.Signal) error { return s.cmd.Process.Signal(sig) }

// PID is the command's process id.
func (s *Session) PID() int { return s.cmd.Process.Pid }

// Transcript is everything the program has written so far, as written (terminal line
// ends included).
func (s *Session) Transcript() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.raw)
}

// ExpectString waits until the program prints want, and consumes the output up to the
// end of it.
func (s *Session) ExpectString(want string, timeout time.Duration) error {
	_, err := s.expect(timeout, fmt.Sprintf("%q", want), func(b []byte) []int {
		i := bytes.Index(b, []byte(want))
		if i < 0 {
			return nil
		}
		return []int{i, i + len(want)}
	})
	return err
}

// ExpectRegexp waits until the program prints text that matches pattern (a Go regular
// expression; (?s) lets . match a newline) and consumes the output up to the end of the
// match. It returns the match and its submatches, as FindStringSubmatch does.
func (s *Session) ExpectRegexp(pattern string, timeout time.Duration) ([]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("ptytest: %w", err)
	}
	return s.expect(timeout, "/"+pattern+"/", re.FindSubmatchIndex)
}

// expect is the wait behind ExpectString and ExpectRegexp. find looks at the output not
// matched yet and returns the indexes of the match and its submatches, or nil.
func (s *Session) expect(timeout time.Duration, what string, find func([]byte) []int) ([]string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		rest := s.norm[s.pos:]
		if loc := find(rest); loc != nil {
			got := make([]string, 0, len(loc)/2)
			for i := 0; i+1 < len(loc); i += 2 {
				if loc[i] < 0 {
					got = append(got, "")
					continue
				}
				got = append(got, string(rest[loc[i]:loc[i+1]]))
			}
			s.pos += loc[1]
			s.mu.Unlock()
			return got, nil
		}
		eof, ch := s.eof, s.changed
		s.mu.Unlock()
		if eof {
			return nil, s.failure(ErrEOF, "while waiting for "+what+": the program printed nothing more")
		}
		select {
		case <-ch:
		case <-timer.C:
			return nil, s.failure(ErrTimeout, fmt.Sprintf("after %v waiting for %s", timeout, what))
		}
	}
}

// ExpectEOF waits until the output ends: the program has exited and nothing it started
// holds the terminal any more.
func (s *Session) ExpectEOF(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		eof, ch := s.eof, s.changed
		s.mu.Unlock()
		if eof {
			return nil
		}
		select {
		case <-ch:
		case <-timer.C:
			return s.failure(ErrTimeout, fmt.Sprintf("after %v waiting for the output to end", timeout))
		}
	}
}

// Wait waits for the command to exit and returns its status: ProcessState.ExitCode is -1
// for a command that a signal killed (ProcessState.Sys says which). The error is a
// timeout; a command that exited with a failure is not an error here.
func (s *Session) Wait(timeout time.Duration) (*os.ProcessState, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.exited:
	case <-timer.C:
		return nil, s.failure(ErrTimeout, fmt.Sprintf("after %v waiting for the program to exit", timeout))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

// WaitInputRead waits until the program has read everything typed so far. It is the
// barrier between "I typed a line" and "I do something that must happen after the
// program has had the line": input sits in the terminal until a read takes it, and a
// write to the terminal returns before anything has read. It says the program has the
// line, not what it did with it.
//
// Type the line, wait for its echo (the terminal echoes when it has accepted the input,
// so the echo proves that the bytes are queued), then call this.
//
// A program that has exited is past reading. Where the terminal can still be asked, bytes
// it left unread are an error (it never took the line); where it cannot (macOS revokes the
// terminal from every holder when the program that leads its session exits) the exit is
// all there is to say, and the later steps of the test say what became of the line.
func (s *Session) WaitInputRead(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-s.exited:
			// The test's copy of the terminal is closed now; what it held then was noted.
			s.mu.Lock()
			unread := s.unread
			s.mu.Unlock()
			if unread <= 0 {
				return nil
			}
			return s.failure(ErrEOF, fmt.Sprintf("while waiting for the program to read typed input: it exited with %d bytes unread", unread))
		default:
		}
		n, err := s.ask()
		if err != nil {
			// The program may have exited between the check above and the question, and taken
			// the terminal with it: the exit is noted in a moment. An error that is not that
			// outlasts the wait.
			if time.Now().After(deadline) {
				return fmt.Errorf("ptytest: asking how much typed input is unread: %w", err)
			}
			time.Sleep(2 * time.Millisecond)
			continue
		}
		if n == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return s.failure(ErrTimeout, fmt.Sprintf("after %v waiting for the program to read %d typed bytes", timeout, n))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// failure is an error that is kind (for errors.Is) and says what was being waited for,
// followed by the transcript with the point the matching has got to marked.
func (s *Session) failure(kind error, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sb strings.Builder
	sb.WriteString(detail)
	// A long transcript is no more useful than its end.
	const keep = 6000
	text, pos, from := s.norm, s.pos, 0
	if len(text) > keep {
		from = len(text) - keep
		sb.WriteString("\n--- transcript (earlier output cut) ---\n")
	} else {
		sb.WriteString("\n--- transcript ---\n")
	}
	if pos < from {
		pos = from
	}
	sb.WriteString(visible(text[from:pos]))
	sb.WriteString("⟦matched up to here⟧")
	sb.WriteString(visible(text[pos:]))
	sb.WriteString("\n--- end of transcript ---")
	if s.readErr != nil {
		fmt.Fprintf(&sb, "\n(reading the terminal failed: %v)", s.readErr)
	}
	return &failed{kind: kind, msg: sb.String()}
}

// failed is an error that is kind for errors.Is and prints as kind, a colon and msg.
type failed struct {
	kind error
	msg  string
}

// Error prefixes the PTY failure detail with its error category.
func (f *failed) Error() string { return f.kind.Error() + ": " + f.msg }

// Unwrap exposes the PTY failure category for errors.Is comparisons.
func (f *failed) Unwrap() error { return f.kind }

// visible makes control characters readable in a failure message: a terminal's output
// is full of them, and the ones that matter (an escape sequence, a stray carriage
// return) must not be hidden by the terminal that displays the failure.
func visible(b []byte) string {
	var sb strings.Builder
	for _, r := range string(b) {
		switch {
		case r == '\n' || r == '\t':
			sb.WriteRune(r)
		case r < 0x20:
			fmt.Fprintf(&sb, "^%c", r+0x40)
		case r == 0x7f:
			sb.WriteString("^?")
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// Close ends the session: the command and everything in its process group are killed
// if they are still running, and the terminal is closed. Start arranges for it to run
// when the test ends; it is safe to call more than once.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		killGroup(s.cmd.Process.Pid)
		waitFor(s.exited, 30*time.Second)
		s.slave.Close()
		s.master.Close()
		waitFor(s.readDone, 30*time.Second)
	})
}

// waitFor waits until a channel is ready or the duration elapses without distinguishing the
// outcome.
func waitFor(ch <-chan struct{}, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	}
}
