package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// daemon is the schedule's daemon run by this page: the loop of `sleipnir daemon` in this process, holding the daemon lock.
type daemon struct {
	every  time.Duration
	once   bool // a single look (--once), not a loop
	cancel context.CancelFunc
	kick   chan struct{}
	done   chan struct{}
}

// Bounds of the daemon's interval (PARITY.md A12).
const (
	minEvery = 5 * time.Second
	maxEvery = 24 * time.Hour
)

// daemonHere reports whether this page's daemon runs.
func (s *service) daemonHere() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.daemon != nil
}

// daemonState says who runs the schedule: this page, another process (the holder of the lock), or nobody.
func (s *service) daemonState() wire.DaemonState {
	st := wire.DaemonState{Every: s.o.Every.String(), Timeout: sched.JobTimeout.String() + " per run", Owner: "none"}
	s.mu.Lock()
	d := s.daemon
	s.mu.Unlock()
	if d != nil {
		st.Running, st.Owner, st.PID, st.Every = true, "here", os.Getpid(), d.every.String()
		st.Line = "sleipnir daemon: looking for due jobs every " + d.every.String()
		if d.once {
			st.Line = "sleipnir daemon --once: starting what is due now"
		}
		return st
	}
	if pid, held := sched.Probe(s.store().LockPath()); held {
		st.Running, st.Owner, st.PID = true, "external", pid
		st.Line = fmt.Sprintf("a daemon outside this page runs the schedule (pid %d)", pid)
		return st
	}
	st.Line = "stopped: no job starts until a daemon runs (here, or sleipnir daemon in a terminal)"
	return st
}

// daemonRequest is the body of POST /api/schedule/daemon.
type daemonRequest struct {
	Action string `json:"action"`
	Every  string `json:"every,omitempty"`
}

// handleDaemon is POST /api/schedule/daemon {action: start|stop|once, every}: start the daemon in this process (it stops with the
// server), stop it, or start what is due now once. A daemon of another process answers 409 external: it is stopped where it runs.
func (s *service) handleDaemon(w http.ResponseWriter, r *http.Request) {
	var body daemonRequest
	if !web.DecodeJSON(w, r, &body) {
		return
	}
	every := s.o.Every
	if body.Every != "" {
		d, err := time.ParseDuration(body.Every)
		if err != nil || d < minEvery || d > maxEvery {
			web.Error(w, http.StatusBadRequest, "bad_flags", "--every is a duration from 5s to 24h")
			return
		}
		every = d
	}
	var err error
	switch body.Action {
	case "start":
		err = s.startDaemon(every, false)
	case "once":
		err = s.startDaemon(every, true)
	case "stop":
		if !s.daemonHere() {
			if pid, held := sched.Probe(s.store().LockPath()); held {
				err = werrDetail(http.StatusConflict, "external", fmt.Sprintf("a daemon outside this page holds the lock (pid %d): stop it there", pid), map[string]int{"pid": pid})
			}
			break
		}
		s.stopDaemon(r.Context())
	default:
		err = werr(http.StatusBadRequest, "bad_request", "the action is start, stop or once")
	}
	if err != nil {
		web.WriteError(w, err)
		return
	}
	web.Logf(r, "daemon %s", body.Action)
	_ = web.WriteJSON(w, http.StatusOK, s.daemonState())
}

// startDaemon takes the daemon lock and starts the loop (or one look). A daemon of this page that runs already is kicked to look
// now for a once, and is left as it is for a start.
func (s *service) startDaemon(every time.Duration, once bool) error {
	s.mu.Lock()
	if d := s.daemon; d != nil {
		s.mu.Unlock()
		if once {
			select {
			case d.kick <- struct{}{}:
			default:
			}
		}
		return nil
	}
	st := s.store()
	unlock, pid, err := sched.Lock(st.LockPath())
	if errors.Is(err, sched.ErrLocked) {
		s.mu.Unlock()
		return werrDetail(http.StatusConflict, "external", fmt.Sprintf("a daemon outside this page holds the lock (pid %d): stop it there", pid), map[string]int{"pid": pid})
	}
	if err != nil {
		s.mu.Unlock()
		s.srv.Logf("the daemon lock: %v", err)
		return werr(http.StatusInternalServerError, "internal", "the daemon lock could not be taken")
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{every: every, once: once, cancel: cancel, kick: make(chan struct{}, 1), done: make(chan struct{})}
	s.daemon = d
	s.mu.Unlock()
	go s.loop(ctx, d, st, unlock)
	return nil
}

// loop is the daemon: a look for due jobs now and every d.every after (one look for a once), each due job started as
// `sleipnir daemon` starts it (sched.Tick, sched.RunJob with the held keys).
func (s *service) loop(ctx context.Context, d *daemon, st sched.Store, unlock func()) {
	defer close(d.done)
	defer unlock()
	defer func() {
		s.mu.Lock()
		if s.daemon == d {
			s.daemon = nil
		}
		s.mu.Unlock()
	}()
	env := os.Environ
	if s.o.Env != nil {
		env = s.o.Env
	}
	launch := func(ctx context.Context, j sched.Job, now time.Time) (string, string) {
		s.mu.Lock()
		s.busy[j.ID] = true
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.busy, j.ID)
			s.mu.Unlock()
		}()
		return sched.RunJob(ctx, s.o.Self, st.LogDir(), j, now, sched.JobEnv(env()), nil)
	}
	for {
		if err := sched.Tick(ctx, st, s.o.Now(), launch, logWriter{s.srv}); err != nil {
			s.srv.Logf("sleipnir daemon: %v", err)
		}
		if d.once {
			return
		}
		t := time.NewTimer(d.every)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-d.kick:
			t.Stop()
		case <-t.C:
		}
	}
}

// logWriter writes the daemon's lines to the server's log.
type logWriter struct{ srv *web.Server }

// Write logs one line per call (Tick writes whole lines).
func (l logWriter) Write(p []byte) (int, error) {
	l.srv.Logf("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// stopDaemon stops this page's daemon (a job it runs is interrupted) and waits for it, at most until ctx ends.
func (s *service) stopDaemon(ctx context.Context) {
	s.mu.Lock()
	d := s.daemon
	s.mu.Unlock()
	if d == nil {
		return
	}
	d.cancel()
	select {
	case <-d.done:
	case <-ctx.Done():
	}
}
