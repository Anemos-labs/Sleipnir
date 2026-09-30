package hooks

import (
	"context"
	"os"
	"sync"
	"time"
)

// outcome is what happened to one hook process.
type outcome struct {
	stdout, stderr       []byte
	stdoutCut, stderrCut bool
	// exit is the exit status: 128+N when signal N ended the process, -1 when it
	// never started.
	exit int
	// timedOut and canceled say the runner ended the process; overflow says it did
	// so because the hook printed more than it may.
	timedOut, canceled, overflow bool
	// strays says background processes were still holding the hook's output when
	// the hook itself was done, and were ended.
	strays   bool
	startErr error
	dur      time.Duration
}

// sink keeps the head of a stream and counts the rest, so that a hook that
// prints without end costs a bounded amount of memory.
type sink struct {
	mu    sync.Mutex
	buf   []byte
	max   int
	total int64
	cut   bool
}

func (s *sink) write(p []byte) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total += int64(len(p))
	if room := s.max - len(s.buf); room > 0 {
		take := min(room, len(p))
		s.buf = append(s.buf, p[:take]...)
		if take < len(p) {
			s.cut = true
		}
	} else if len(p) > 0 {
		s.cut = true
	}
	return s.total
}

func (s *sink) snapshot() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf...), s.cut
}

// pump copies a pipe into a sink until EOF or until the reader is closed.
// limit > 0 asks for overflow to be signalled once the stream has carried more
// than that many bytes in total.
func pump(f *os.File, s *sink, limit int64, overflow chan<- struct{}) {
	defer f.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if total := s.write(buf[:n]); limit > 0 && total > limit {
				select {
				case overflow <- struct{}{}:
				default:
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// execCommand runs one command hook.
//
// The command runs in a session of its own, so that its pid is also its
// process-group id and the whole tree can be signalled: a hook that spawns
// children (a test runner, a watcher, "sleep 1000 &") must not outlive its
// timeout. The pipes are created here rather than by exec.Cmd, because with
// os.Pipe files Wait never blocks on I/O: a backgrounded grandchild that
// inherited stdout would otherwise keep a finished hook "running" forever.
//
// stdin carries the payload and is written concurrently, so a hook that never
// reads it cannot wedge the runner, and stdout and stderr are captured up to
// their caps and drained beyond them, so a hook that prints without end cannot
// exhaust memory or block on a full pipe. Past MaxRawOutput the hook is ended.
func (r *Runner) execCommand(ctx context.Context, command string, payload []byte, dir string, env []string, timeout time.Duration) outcome {
	start := time.Now()
	res := outcome{exit: -1}

	cmd := shellCommand(command)
	cmd.Dir = dir
	if env == nil {
		env = []string{}
	}
	cmd.Env = env
	configureCmd(cmd)

	inR, inW, err := os.Pipe()
	if err != nil {
		res.startErr = err
		return res
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		res.startErr = err
		return res
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW} {
			f.Close()
		}
		res.startErr = err
		return res
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	err = cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		res.startErr = err
		res.dur = time.Since(start)
		return res
	}
	pid := cmd.Process.Pid

	go func() {
		_, _ = inW.Write(payload)
		_ = inW.Close()
	}()

	stdout := &sink{max: r.maxStdout()}
	stderr := &sink{max: r.maxStderr()}
	overflow := make(chan struct{}, 1)
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() { defer pumps.Done(); pump(outR, stdout, r.maxRaw(), overflow) }()
	go func() { defer pumps.Done(); pump(errR, stderr, r.maxRaw(), overflow) }()
	pumpsDone := make(chan struct{})
	go func() { pumps.Wait(); close(pumpsDone) }()

	waitCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitCh)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	killed := false
	select {
	case <-waitCh:
	case <-timer.C:
		res.timedOut, killed = true, true
	case <-ctx.Done():
		res.canceled, killed = true, true
	case <-overflow:
		res.overflow, killed = true, true
	}
	if killed {
		termGroup(pid)
		select {
		case <-waitCh:
		case <-time.After(r.killGrace()):
		}
		killGroup(pid) // anything that ignored the polite signal, the leader included
		<-waitCh
	}

	// The leader is gone. Give its last output a moment to arrive; if something
	// else still holds the pipes, end it: a hook that leaves processes attached
	// to the harness's pipes has not finished.
	select {
	case <-pumpsDone:
	case <-time.After(r.pipeGrace()):
		res.strays = true
		killGroup(pid)
		outR.Close()
		errR.Close()
		<-pumpsDone
	}
	_ = inW.Close()

	res.exit = exitStatus(cmd.ProcessState)
	res.stdout, res.stdoutCut = stdout.snapshot()
	res.stderr, res.stderrCut = stderr.snapshot()
	res.dur = time.Since(start)
	return res
}
