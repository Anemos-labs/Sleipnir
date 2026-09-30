package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// safeEnvNames are the only variables of the harness's own environment that a
// server process inherits. Everything else, above all the provider API keys and
// tokens the harness itself runs on, stays behind: an MCP server is third-party
// code (often fetched and executed by npx or uvx a minute earlier), and it must
// hold only the credentials the user gave it in its own "env" block.
//
// The list is what a process needs to find programs, locate its home and cache,
// pick a language and a temporary directory, and trust the same certificate
// authorities. None of it is a secret. Proxy variables are absent on purpose:
// proxy URLs commonly embed credentials, so a server that needs one lists it
// in "env" (with ${HTTPS_PROXY} if the harness's map provides it).
var safeEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"LANG": true, "LANGUAGE": true, "TZ": true, "TERM": true,
	"TMPDIR": true, "TEMP": true, "TMP": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true, "REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true,
	// Windows: without these most programs cannot start at all.
	"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true, "USERPROFILE": true,
	"APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true, "PROGRAMFILES": true, "PROGRAMFILES(X86)": true,
	"HOMEDRIVE": true, "HOMEPATH": true, "OS": true, "NUMBER_OF_PROCESSORS": true, "PROCESSOR_ARCHITECTURE": true,
}

// SafeBaseEnv filters an environment ("K=V" entries, os.Environ() format) down
// to the variables a server process may inherit (see safeEnvNames, plus LC_*).
func SafeBaseEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		up := strings.ToUpper(k)
		if safeEnvNames[up] || strings.HasPrefix(up, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// childEnv is the complete environment of a server process: the safe base
// with the entries the configuration lists laid over it, sorted so the same
// configuration always produces the same environment.
func childEnv(base []string, extra map[string]string) []string {
	m := map[string]string{}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[k] = v
		}
	}
	for k, v := range extra {
		m[k] = v
	}
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, k+"="+m[k])
	}
	return out
}

// procSpec describes one server process to start.
type procSpec struct {
	Command string
	Args    []string
	Dir     string
	Env     []string // the complete environment; nothing is inherited
	Grace   time.Duration
	Redact  *redactor
	Stream  StreamOptions
}

// procTransport is a StreamTransport over a child process, plus the process
// lifecycle. The child leads its own session (see configureProc), so its pid
// is its process-group id and the whole tree, not just the leader, is signalled
// on shutdown: an npx or uvx wrapper's real server is a grandchild, and
// killing only the wrapper would leak it.
//
// The pipes are made here instead of by exec.Cmd so Wait never blocks on I/O:
// a grandchild that inherits stderr and outlives its parent would otherwise
// hang cmd.Wait.
type procTransport struct {
	st      *StreamTransport
	cmd     *exec.Cmd
	pid     int
	inW     *os.File
	outR    *os.File
	tail    *tailBuffer
	redact  *redactor
	grace   time.Duration
	done    chan struct{} // closed when the leader has been reaped
	closing atomic.Bool
	once    sync.Once
	// stderrDone closes when the stderr drain has seen EOF, so a crash report can
	// include what the server said last instead of racing the drain goroutine.
	stderrDone chan struct{}
}

func startProc(sp procSpec) (*procTransport, error) {
	path, err := resolveCommand(sp.Command, sp.Env, sp.Dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, sp.Args...)
	cmd.Dir = sp.Dir
	cmd.Env = sp.Env
	if cmd.Env == nil {
		cmd.Env = []string{} // never inherit: nil would mean "the harness's environment"
	}
	configureProc(cmd)

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
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
		return nil, fmt.Errorf("starting server: %w", unwrapExecError(err))
	}
	if sp.Grace <= 0 {
		sp.Grace = 2 * time.Second
	}
	p := &procTransport{
		cmd: cmd, pid: cmd.Process.Pid, inW: inW, outR: outR,
		tail: &tailBuffer{max: 8 << 10}, redact: sp.Redact, grace: sp.Grace,
		done: make(chan struct{}), stderrDone: make(chan struct{}),
	}
	// The stream is not given CloseReader: procTransport closes stdout itself
	// once the process is gone. Closing it early would cost the server its last
	// words and make it die of SIGPIPE in the middle of an orderly shutdown.
	p.st = NewStreamTransport(outR, inW, sp.Stream)
	go func() {
		defer close(p.stderrDone)
		_, _ = io.Copy(p.tail, errR)
		errR.Close()
	}()
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// unwrapExecError drops the path/argument context of exec errors down to the
// cause, which is what is actionable ("executable file not found in $PATH",
// "permission denied").
func unwrapExecError(err error) error {
	var ee *exec.Error
	if errors.As(err, &ee) {
		return fmt.Errorf("%s: %w", ee.Name, ee.Err)
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s %s: %w", pe.Op, pe.Path, pe.Err)
	}
	return err
}

// Start implements Transport.
func (p *procTransport) Start(h Handler) error {
	wrapped := Handler{
		Message: h.Message,
		Closed: func(err error) {
			if p.closing.Load() || h.Closed == nil {
				return
			}
			// The stream ended, which nearly always means the process is exiting;
			// wait a moment for the status so the error can say how it died.
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
			}
			h.Closed(p.describeExit(err))
		},
	}
	if err := p.st.Start(wrapped); err != nil {
		return err
	}
	go func() {
		// The leader is gone but something else still holds the pipe (a daemonised
		// grandchild): end the read instead of waiting for it forever.
		<-p.done
		select {
		case <-p.st.done:
		case <-time.After(500 * time.Millisecond):
			_ = p.outR.Close()
		}
	}()
	return nil
}

func (p *procTransport) describeExit(streamErr error) error {
	// Let the stderr drain catch up: the process's last words are the most useful
	// part of a crash report. Bounded, because a grandchild may hold stderr open.
	select {
	case <-p.stderrDone:
	case <-time.After(300 * time.Millisecond):
	}
	var msg string
	select {
	case <-p.done:
		msg = "server process exited (" + strings.TrimSpace(p.cmd.ProcessState.String()) + ")"
	default:
		msg = "server closed its output"
		if streamErr != nil && !errors.Is(streamErr, io.EOF) {
			msg = "server output failed: " + cleanText(streamErr.Error())
		}
	}
	if ex := p.tail.excerpt(p.redact); ex != "" {
		msg += "; last stderr: " + ex
	}
	return errors.New(msg)
}

// Send implements Transport.
func (p *procTransport) Send(ctx context.Context, msg []byte) error { return p.st.Send(ctx, msg) }

// Close implements Transport: the orderly MCP shutdown, then force.
//
//  1. Close stdin. EOF on stdin is the protocol's shutdown signal.
//  2. If the process is still there after the grace period, SIGTERM the group.
//  3. If it is still there after another grace period, SIGKILL the group.
//
// Afterwards any leftover member of the group (a child the server started and
// did not wait for) gets the same treatment, so no server outlives its session.
func (p *procTransport) Close() error {
	p.once.Do(func() {
		p.closing.Store(true)
		_ = p.inW.Close()
		if p.waitDone(p.grace) {
			if groupAlive(p.pid) {
				p.terminateGroup()
			}
		} else {
			p.terminateGroup()
		}
		_ = p.outR.Close()
		p.st.finish(nil)
	})
	return nil
}

func (p *procTransport) terminateGroup() {
	termGroup(p.pid)
	if p.waitGroupGone(p.grace) {
		return
	}
	killGroup(p.pid)
	p.waitGroupGone(3 * time.Second)
}

func (p *procTransport) waitDone(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	}
}

// waitGroupGone polls until the leader is reaped and no member of its process
// group is left, or d passes.
func (p *procTransport) waitGroupGone(d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			if !groupAlive(p.pid) {
				return true
			}
		default:
		}
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

// tailBuffer keeps the last bytes a server wrote to stderr, for the error
// message of a crash. It must drain stderr for the whole life of the process:
// a full pipe would block the server on its own logging.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

// excerpt returns the last few lines of stderr as one sanitised, redacted,
// bounded line of text. stderr is untrusted and may echo credentials.
func (t *tailBuffer) excerpt(r *redactor) string {
	t.mu.Lock()
	raw := string(t.buf)
	t.mu.Unlock()
	var keep []string
	for _, l := range strings.Split(cleanText(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > 4 {
		keep = keep[len(keep)-4:]
	}
	s := r.apply(strings.Join(keep, " | "))
	s, _ = truncateRunes(s, 300)
	return s
}
