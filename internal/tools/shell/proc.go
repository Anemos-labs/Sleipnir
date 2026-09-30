package shell

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// killReason records why the harness (rather than the command itself) ended a
// process.
type killReason int32

const (
	killNone      killReason = iota
	killTimeout              // the command ran past its deadline
	killCancel               // the calling context was cancelled (user interrupt)
	killShutdown             // Manager.Shutdown
	killRequested            // bash_kill
	killOutput               // the command produced more output than allowed
)

// exitStatus is how a process ended. code follows the shell convention: the
// exit code, or 128+N when the process died from signal N.
type exitStatus struct {
	code   int
	signal int
	err    error // a wait error that is not just a non-zero exit
}

// procSpec describes one process to start.
type procSpec struct {
	path   string
	args   []string
	dir    string
	env    []string
	sink   sink
	maxOut int64 // kill the command past this many raw output bytes; 0 = unlimited
}

// proc is one running shell process and the pumps draining its output.
//
// The process leads its own session (see configureCmd), so its pid is also its
// process-group id and the whole tree can be signalled at once. The pipes are
// created here rather than by exec.Cmd so that Wait never blocks on I/O: a
// backgrounded grandchild (`server &`) inherits the pipes and would otherwise
// keep a finished command hanging until it exits.
type proc struct {
	m    *Manager
	cmd  *exec.Cmd
	pid  int
	sink sink

	done      chan struct{} // closed once the leader has been reaped
	exit      exitStatus    // valid after done is closed
	pumpsDone chan struct{} // closed once stdout and stderr both reached EOF
	rfiles    [2]*os.File
	killCh    chan killReason

	raw    atomic.Int64 // raw bytes read
	ctrl   atomic.Int64 // control bytes dropped by the sanitizers
	maxOut int64
	grace  time.Duration
}

// startProc launches the process and its pumps. The caller must have called
// m.begin and is responsible for m.end.
func (m *Manager) startProc(sp procSpec) (*proc, error) {
	cmd := exec.Command(sp.path, sp.args...)
	cmd.Dir = sp.dir
	cmd.Env = sp.env
	configureCmd(cmd)

	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW // stdin stays nil: /dev/null
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return nil, err
	}

	p := &proc{
		m:         m,
		cmd:       cmd,
		pid:       cmd.Process.Pid,
		sink:      sp.sink,
		done:      make(chan struct{}),
		pumpsDone: make(chan struct{}),
		rfiles:    [2]*os.File{outR, errR},
		killCh:    make(chan killReason, 1),
		maxOut:    sp.maxOut,
		grace:     m.opts.KillGrace,
	}
	m.track(p)

	var pumps sync.WaitGroup
	pumps.Add(2)
	go p.pump("stdout", outR, &pumps)
	go p.pump("stderr", errR, &pumps)
	go func() {
		err := cmd.Wait()
		p.exit = exitFrom(cmd.ProcessState, err)
		close(p.done)
	}()
	go func() {
		pumps.Wait()
		close(p.pumpsDone)
		<-p.done
		m.untrack(p)
	}()
	return p, nil
}

// pump drains one pipe, sanitizing as it goes. It runs until EOF, which only
// arrives once every process holding the write end (including strays that
// outlive the command) has exited, or until closeReaders.
func (p *proc) pump(stream string, r *os.File, wg *sync.WaitGroup) {
	defer wg.Done()
	defer r.Close()
	var san sanitizer
	buf := make([]byte, 32<<10)
	var out []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			before := san.ctrl
			out = san.feed(out[:0], buf[:n])
			p.ctrl.Add(san.ctrl - before)
			total := p.raw.Add(int64(n))
			if len(out) > 0 {
				p.sink.write(stream, out)
			}
			if p.maxOut > 0 && total > p.maxOut {
				p.requestKill(killOutput)
			}
		}
		if err != nil {
			break
		}
	}
	if out = san.flush(out[:0]); len(out) > 0 {
		p.sink.write(stream, out)
	}
}

// requestKill asks the supervisor to terminate the process tree. It never
// blocks and is harmless once the process has ended.
func (p *proc) requestKill(r killReason) {
	select {
	case p.killCh <- r:
	default:
	}
}

// supervise waits for the leader to exit, ending it first if the deadline
// passes, ctx is cancelled, the Manager shuts down, or a kill is requested.
// timeout <= 0 means no deadline.
func (p *proc) supervise(ctx context.Context, timeout time.Duration) killReason {
	var expire <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expire = t.C
	}
	reason := killNone
	select {
	case <-p.done:
	case <-expire:
		reason = killTimeout
	case <-ctx.Done():
		reason = killCancel
	case <-p.m.stop:
		reason = killShutdown
	case r := <-p.killCh:
		reason = r
	}
	if reason != killNone {
		p.killTree()
	}
	<-p.done
	return reason
}

// killTree stops the whole process group: SIGTERM for an orderly exit, then
// SIGKILL after the grace period for whatever ignored it. It waits for the
// group, not just the leader: a child that ignores SIGTERM outlives a leader
// that obeyed it, and must not be left running.
func (p *proc) killTree() {
	termGroup(p.pid)
	grace := time.NewTimer(p.grace)
	defer grace.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if p.leaderDone() && !groupAlive(p.pid) {
			return
		}
		select {
		case <-grace.C:
			killGroup(p.pid)
			select { // SIGKILL cannot be caught; wait for the kernel to finish
			case <-p.done:
			case <-time.After(5 * time.Second):
			}
			return
		case <-tick.C:
		}
	}
}

func (p *proc) leaderDone() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *proc) pumpsFinished() bool {
	select {
	case <-p.pumpsDone:
		return true
	default:
		return false
	}
}

// status reports how the leader ended; ok is false while it is still running
// (or could not be reaped after SIGKILL).
func (p *proc) status() (exitStatus, bool) {
	select {
	case <-p.done:
		return p.exit, true
	default:
		return exitStatus{code: -1}, false
	}
}

// closeReaders force-closes the read ends of the pipes, ending the pumps even
// if a stray process still holds the write ends.
func (p *proc) closeReaders() {
	for _, f := range p.rfiles {
		f.Close()
	}
}

// waitPumps gives the pumps a moment to drain what the process wrote just
// before exiting. Returns false if they are still running (a background
// process kept the pipes open).
func (p *proc) waitPumps(grace time.Duration) bool {
	if p.pumpsFinished() {
		return true
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-p.pumpsDone:
		return true
	case <-t.C:
		return false
	}
}
