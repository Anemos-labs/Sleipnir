package runner

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Spec is one run: a child process of the runner's executable (Args), or a function run in this process (Func).
type Spec struct {
	// Path and Flags describe the run in the recent-runs list; Cmdline is its command line as a person would type it.
	Path    []string
	Flags   map[string]any
	Cmdline string
	// Args are the arguments after the program's name; Dir the working directory (empty: the runner's).
	Args []string
	Dir  string
	// Env replaces the runner's environment when not nil; Net adds the held provider keys (sched.JobEnv).
	Env []string
	Net bool
	// Func runs the command in this process instead of a child: it writes to stdout and stderr and returns the exit status.
	Func func(ctx context.Context, stdout, stderr io.Writer) int
	// Keep says that the page lets the run go on when it leaves it (PARITY.md A22); Server that the run has no time limit (a
	// server runs until it is stopped).
	Keep, Server bool
	// Timeout replaces the runner's limit when positive.
	Timeout time.Duration
	// Tee receives the raw output (both streams), before it is made safe for display: a job's log file.
	Tee io.Writer
	// Line sees each line, made safe, before it is sent: it returns a step to send in its place (the doctor's probe) and whether
	// the line itself is shown. Nil shows every line.
	Line func(stream, text string) (step *wire.DoctorStep, show bool)
	// End is called when the run has ended, before its last frame is sent: it may add a verdict, a result card and a log line.
	End func(res *wire.RunResult) *wire.DoctorVerdict
}

// RunInfo is a run in the recent-runs list: wire.RunInfo, plus whether the page keeps it going (A22).
type RunInfo struct {
	wire.RunInfo
	Keep    bool `json:"keep,omitempty"`
	started time.Time
}

// RunOutput is what GET /api/runs/{run}/output answers: the lines a run kept from a line number on, the number to ask from next,
// and the run's result once it has ended.
type RunOutput struct {
	ID      string          `json:"id"`
	Lines   []wire.RunLine  `json:"lines"`
	Next    int             `json:"next"`
	Running bool            `json:"running"`
	Result  *wire.RunResult `json:"result,omitempty"`
	Dropped int             `json:"dropped,omitempty"`
}

// Runs is the run set of one server.
type Runs struct {
	srv  *web.Server
	host seam.Host
	o    Options
	mask *masker

	mu      sync.Mutex
	runs    map[string]*run
	order   []string // ids, oldest first
	running int
	closed  bool
	wg      sync.WaitGroup
}

// newRuns makes the run set of a server.
func newRuns(srv *web.Server, h seam.Host, o Options) *Runs {
	return &Runs{srv: srv, host: h, o: o, mask: newMasker(srv), runs: map[string]*run{}}
}

// run is one run and what it kept of its output.
type run struct {
	id      string
	spec    Spec
	started time.Time
	cancel  context.CancelFunc

	mu       sync.Mutex
	lines    []wire.RunLine
	bytes    int
	dropped  int
	pending  []wire.RunLine
	result   *wire.RunResult
	canceled bool
	done     chan struct{}
}

// maxKeptBytes bounds the text a run keeps for reattaching.
const maxKeptBytes = 4 << 20

// info is the run as the list shows it.
func (x *run) info() RunInfo {
	x.mu.Lock()
	defer x.mu.Unlock()
	ri := RunInfo{RunInfo: wire.RunInfo{ID: x.id, Path: x.spec.Path, Flags: x.spec.Flags, Cmdline: x.spec.Cmdline, Running: x.result == nil}, Keep: x.spec.Keep, started: x.started}
	if x.result != nil {
		exit := x.result.Exit
		ri.Exit, ri.Ms = &exit, x.result.Ms
	} else {
		ri.Ms = time.Since(x.started).Milliseconds()
	}
	return ri
}

// newRunID returns a random run id: r_ and 16 base32 characters (80 bits).
func newRunID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "r_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}

// Start starts a run and returns its id and command line. It fails with 409 busy when the runner runs its maximum already.
func (r *Runs) Start(s Spec) (wire.RunStarted, error) {
	id, err := newRunID()
	if err != nil {
		return wire.RunStarted{}, err
	}
	timeout := r.o.Timeout
	if s.Timeout > 0 {
		timeout = s.Timeout
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if s.Server && s.Timeout <= 0 {
		ctx, cancel = context.WithCancel(context.Background())
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	x := &run{id: id, spec: s, started: time.Now(), cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	switch {
	case r.closed:
		r.mu.Unlock()
		cancel()
		return wire.RunStarted{}, &wire.Error{Status: http.StatusServiceUnavailable, Code: "shutting_down", Msg: "the server is stopping"}
	case r.running >= r.o.MaxRuns:
		r.mu.Unlock()
		cancel()
		return wire.RunStarted{}, &wire.Error{Status: http.StatusConflict, Code: "busy", Msg: fmt.Sprintf("%d commands are running already: stop one first", r.o.MaxRuns)}
	}
	r.running++
	r.runs[id] = x
	r.order = append(r.order, id)
	r.forgetLocked()
	r.wg.Add(1)
	r.mu.Unlock()
	go r.execute(ctx, x, timeout)
	return wire.RunStarted{ID: id, Cmdline: s.Cmdline}, nil
}

// forgetLocked drops the oldest finished runs beyond the number kept.
func (r *Runs) forgetLocked() {
	finished := 0
	for i := len(r.order) - 1; i >= 0; i-- {
		x := r.runs[r.order[i]]
		x.mu.Lock()
		ended := x.result != nil
		x.mu.Unlock()
		if !ended {
			continue
		}
		finished++
		if finished > r.o.Keep {
			delete(r.runs, r.order[i])
			r.order = append(r.order[:i], r.order[i+1:]...)
		}
	}
}

// Cancel stops a run: its process group is sent SIGTERM, and SIGKILL after the grace. It reports whether the run exists (a run that
// has ended already is no error).
func (r *Runs) Cancel(id string) bool {
	r.mu.Lock()
	x, ok := r.runs[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	x.mu.Lock()
	if x.result == nil {
		x.canceled = true
	}
	x.mu.Unlock()
	x.cancel()
	return true
}

// Done returns a channel closed when the run has ended (nil for an unknown run).
func (r *Runs) Done(id string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if x, ok := r.runs[id]; ok {
		return x.done
	}
	return nil
}

// Close stops every run and waits for them, at most until ctx ends; no run starts after it.
func (r *Runs) Close(ctx context.Context) {
	r.mu.Lock()
	r.closed = true
	all := make([]*run, 0, len(r.runs))
	for _, x := range r.runs {
		all = append(all, x)
	}
	r.mu.Unlock()
	for _, x := range all {
		x.mu.Lock()
		if x.result == nil {
			x.canceled = true
		}
		x.mu.Unlock()
		x.cancel()
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Output returns the lines a run kept from line from on.
func (r *Runs) Output(id string, from int) (RunOutput, bool) {
	r.mu.Lock()
	x, ok := r.runs[id]
	r.mu.Unlock()
	if !ok {
		return RunOutput{}, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	out := RunOutput{ID: id, Running: x.result == nil, Result: x.result, Dropped: x.dropped, Lines: []wire.RunLine{}}
	if from < len(x.lines) {
		out.Lines = append(out.Lines, x.lines[from:]...)
	}
	out.Next = len(x.lines)
	return out, true
}

// publish sends a run frame to every page.
func (r *Runs) publish(f wire.RunFrame, critical bool) {
	if r.host == nil {
		return
	}
	r.host.Publish(wire.Frame{Type: "run", Data: f, Critical: critical})
}

// execute runs one run to its end and sends its frames.
func (r *Runs) execute(ctx context.Context, x *run, timeout time.Duration) {
	defer r.wg.Done()
	defer x.cancel()
	stop := make(chan struct{})
	flushed := make(chan struct{})
	go func() { // the batches of lines
		defer close(flushed)
		t := time.NewTicker(r.o.Batch)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.flush(x)
			case <-stop:
				return
			}
		}
	}()
	var exit int
	if x.spec.Func != nil {
		exit = r.inProcess(ctx, x)
	} else {
		exit = r.child(ctx, x)
	}
	close(stop)
	<-flushed
	res := &wire.RunResult{Exit: exit, Ms: time.Since(x.started).Milliseconds()}
	x.mu.Lock()
	res.Canceled = x.canceled
	x.mu.Unlock()
	if !res.Canceled && ctx.Err() == context.DeadlineExceeded {
		r.line(x, "err", fmt.Sprintf("sleipnir web: the command was stopped after %s", timeout))
	}
	var verdict *wire.DoctorVerdict
	if x.spec.End != nil {
		verdict = x.spec.End(res)
	}
	x.mu.Lock()
	if x.dropped > 0 {
		x.pending = append(x.pending, wire.RunLine{K: "err", T: fmt.Sprintf("[%d more lines were not shown]", x.dropped)})
	}
	lines := x.pending
	x.pending = nil
	x.result = res
	x.mu.Unlock()
	if len(lines) > 0 {
		r.publish(wire.RunFrame{ID: x.id, Lines: lines}, false)
	}
	r.publish(wire.RunFrame{ID: x.id, Verdict: verdict, Result: res}, true)
	close(x.done)
	r.mu.Lock()
	r.running--
	r.forgetLocked()
	r.mu.Unlock()
}

// flush sends the lines waiting.
func (r *Runs) flush(x *run) {
	x.mu.Lock()
	lines := x.pending
	x.pending = nil
	x.mu.Unlock()
	if len(lines) > 0 {
		r.publish(wire.RunFrame{ID: x.id, Lines: lines}, false)
	}
}

// line adds one line of output (made safe) to a run: it is kept and sent with the next batch, unless the run's hook takes it.
func (r *Runs) line(x *run, k, text string) {
	text = r.mask.clean(text)
	if x.spec.Line != nil {
		step, show := x.spec.Line(k, text)
		if step != nil {
			r.flush(x) // the lines before it first
			r.publish(wire.RunFrame{ID: x.id, Step: step}, false)
		}
		if !show {
			return
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.lines) >= r.o.MaxLines {
		x.dropped++
		return
	}
	l := wire.RunLine{K: k, T: text}
	x.pending = append(x.pending, l)
	if x.bytes+len(text) <= maxKeptBytes {
		x.lines = append(x.lines, l)
		x.bytes += len(text)
	} else {
		x.dropped++
	}
}

// read splits one stream into lines of at most MaxLineBytes (a longer line is cut, its rest dropped) and adds them.
func (r *Runs) read(x *run, k string, src io.Reader) {
	br := bufio.NewReaderSize(src, 64<<10)
	var cur []byte
	cut := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			nl := chunk[len(chunk)-1] == '\n'
			body := chunk
			if nl {
				body = chunk[:len(chunk)-1]
			}
			if room := r.o.MaxLineBytes - len(cur); room > 0 {
				if len(body) > room {
					body, cut = body[:room], true
				}
				cur = append(cur, body...)
			} else if len(body) > 0 {
				cut = true
			}
			if nl {
				r.emit(x, k, cur, cut)
				cur, cut = cur[:0], false
			}
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if len(cur) > 0 {
				r.emit(x, k, cur, cut)
			}
			return
		}
	}
}

// emit adds one complete line.
func (r *Runs) emit(x *run, k string, b []byte, cut bool) {
	text := strings.TrimSuffix(string(b), "\r")
	if cut {
		text = strings.ToValidUTF8(text, "") + " …[line cut]"
	}
	r.line(x, k, text)
}

// teeWriter writes to the run's tee and splits the stream into lines.
type teeWriter struct {
	tee io.Writer
	w   *io.PipeWriter
}

// Write copies p to the tee (errors of the tee are ignored: the display goes on) and to the line splitter.
func (t teeWriter) Write(p []byte) (int, error) {
	if t.tee != nil {
		_, _ = t.tee.Write(p)
	}
	return t.w.Write(p)
}

// inProcess runs a Func run.
func (r *Runs) inProcess(ctx context.Context, x *run) int {
	or, ow := io.Pipe()
	er, ew := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.read(x, "out", or) }()
	go func() { defer wg.Done(); r.read(x, "err", er) }()
	var stdout, stderr io.Writer = teeWriter{x.spec.Tee, ow}, teeWriter{x.spec.Tee, ew}
	code := func() (code int) {
		defer func() {
			if p := recover(); p != nil {
				fmt.Fprintln(stderr, "sleipnir web: the command failed inside the server")
				code = 70
			}
		}()
		return x.spec.Func(ctx, stdout, stderr)
	}()
	_ = ow.Close()
	_ = ew.Close()
	wg.Wait()
	return code
}

// drainWait is how long the output of a finished process may stay open (a child it left behind holds it) before its group is
// killed and the pipes closed.
const drainWait = 2 * time.Second

// child runs an Args run as a child process and returns its exit status (128 + the signal for one a signal ended; -1 when it
// could not start).
func (r *Runs) child(ctx context.Context, x *run) int {
	env := x.spec.Env
	if env == nil {
		env = r.o.Env()
	}
	if x.spec.Net {
		env = sched.JobEnv(env)
	}
	dir := x.spec.Dir
	if dir == "" {
		dir = r.o.Cwd
	}
	cmd := exec.Command(r.o.Self, x.spec.Args...)
	cmd.Dir, cmd.Env = dir, env
	setGroup(cmd)
	orR, orW, err := os.Pipe()
	if err != nil {
		r.line(x, "err", "sleipnir web: "+err.Error())
		return -1
	}
	erR, erW, err := os.Pipe()
	if err != nil {
		orR.Close()
		orW.Close()
		r.line(x, "err", "sleipnir web: "+err.Error())
		return -1
	}
	cmd.Stdout, cmd.Stderr = orW, erW
	if err := cmd.Start(); err != nil {
		orR.Close()
		orW.Close()
		erR.Close()
		erW.Close()
		r.line(x, "err", "sleipnir web: the command could not start")
		return -1
	}
	orW.Close()
	erW.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	readOne := func(k string, f *os.File) {
		defer wg.Done()
		var src io.Reader = f
		if x.spec.Tee != nil {
			src = io.TeeReader(f, x.spec.Tee)
		}
		r.read(x, k, src)
	}
	go readOne("out", orR)
	go readOne("err", erR)
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-waited:
	case <-ctx.Done():
		terminate(cmd)
		select {
		case werr = <-waited:
		case <-time.After(r.o.Grace):
			kill(cmd)
			werr = <-waited
		}
		kill(cmd) // what of its group ignored SIGTERM goes too: a stopped run leaves nothing behind
	}
	// a child the process left behind that still holds the output is ended with the group
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainWait):
		kill(cmd)
		orR.Close()
		erR.Close()
		<-drained
	}
	orR.Close()
	erR.Close()
	return exitStatus(cmd, werr)
}
