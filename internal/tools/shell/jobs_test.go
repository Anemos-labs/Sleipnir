//go:build unix

package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// lastLine returns the final line of a tool result: the status trailer.
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[strings.LastIndexByte(s, '\n')+1:]
}

func jobEvents(h *harness) []JobEvent {
	var out []JobEvent
	for _, e := range h.log.OfType(EventJob) {
		var je JobEvent
		if err := json.Unmarshal(e.Data, &je); err != nil {
			h.t.Fatalf("bad %s payload %s: %v", EventJob, e.Data, err)
		}
		out = append(out, je)
	}
	return out
}

func TestBackgroundJobLifecycle(t *testing.T) {
	h := newHarness(t, Options{KillGrace: 500 * time.Millisecond})
	env := h.env("a")

	res := h.bash(env, "echo hi; sleep 30", map[string]any{"run_in_background": true, "description": "greet"})
	if res.IsError || res.Text != "job job_1 started" {
		t.Fatalf("start result = %+v", res)
	}
	pid := metaInt(t, res, "pid")
	if !pidAlive(pid) {
		t.Fatal("job is not running")
	}

	waitFor(t, "first output", 10*time.Second, func() bool {
		return strings.HasPrefix(h.output(env, "job_1").Text, "hi\n")
	})
	// The read above consumed "hi"; nothing new since.
	if got := h.output(env, "job_1").Text; got != "(no new output)\n[job_1 running; next offset 3]" {
		t.Errorf("second read = %q", got)
	}
	for since, want := range map[int]string{
		0:   "hi\n[job_1 running; next offset 3]",
		1:   "i\n[job_1 running; next offset 3]",
		3:   "(no new output)\n[job_1 running; next offset 3]",
		999: "(no new output)\n[job_1 running; next offset 3]",
	} {
		if got := h.output(env, "job_1", map[string]any{"since": since}).Text; got != want {
			t.Errorf("since=%d: %q, want %q", since, got, want)
		}
	}
	// An explicit since also moves the cursor for later reads.
	h.output(env, "job_1", map[string]any{"since": 1})
	if got := h.output(env, "job_1").Text; got != "(no new output)\n[job_1 running; next offset 3]" {
		t.Errorf("read after explicit since = %q", got)
	}

	kres := h.kill(env, "job_1")
	if kres.IsError || kres.Text != "job job_1 killed (exit code 143)" {
		t.Fatalf("kill result = %+v", kres)
	}
	waitDead(t, pid, 5*time.Second)
	if got := h.output(env, "job_1", map[string]any{"since": 0}).Text; got != "hi\n[job_1 killed; next offset 3]" {
		t.Errorf("read after kill = %q", got)
	}
	// Killing again is idempotent.
	for i := 0; i < 2; i++ {
		if r := h.kill(env, "job_1"); r.IsError || r.Text != "job job_1 already killed (exit code 143)" {
			t.Errorf("repeat kill %d = %+v", i, r)
		}
	}

	evs := jobEvents(h)
	if len(evs) != 2 {
		t.Fatalf("%d tool.job events, want 2: %+v", len(evs), evs)
	}
	if e := evs[0]; e.ID != "job_1" || e.Agent != "a" || e.Command != "echo hi; sleep 30" || e.Status != "started" || e.Exit != nil {
		t.Errorf("start event = %+v", e)
	}
	if e := evs[1]; e.ID != "job_1" || e.Agent != "a" || e.Status != "killed" || e.Exit == nil || *e.Exit != 143 || e.DurationMS < 0 || e.Duration == "" {
		t.Errorf("end event = %+v", e)
	}
	if got := h.log.OfType(EventJob)[0].Agent; got != "a" {
		t.Errorf("envelope agent = %q", got)
	}
}

func TestJobExitsOnItsOwn(t *testing.T) {
	h := newHarness(t)
	env := h.env("a")
	id := h.startJob(env, "for i in 1 2 3; do echo line$i; sleep 0.1; done; echo oops >&2; exit 4")
	waitFor(t, "job to exit", 10*time.Second, func() bool {
		return strings.HasSuffix(h.output(env, id, map[string]any{"since": 0}).Text, "exited 4; next offset 23]")
	})
	got := h.output(env, id, map[string]any{"since": 0}).Text
	if want := "line1\nline2\nline3\noops\n[job_1 exited 4; next offset 23]"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	res := h.output(env, id)
	if res.Meta["running"] != false || metaInt(t, res, "exit_code") != 4 {
		t.Errorf("meta = %v", res.Meta)
	}
	if r := h.kill(env, id); r.IsError || r.Text != "job job_1 already exited (exit code 4)" {
		t.Errorf("kill after exit = %+v", r)
	}
	evs := jobEvents(h)
	if len(evs) != 2 || evs[1].Status != "exited" || evs[1].Exit == nil || *evs[1].Exit != 4 {
		t.Errorf("events = %+v", evs)
	}
}

func TestJobsAreSessionWide(t *testing.T) {
	h := newHarness(t)
	a, b := h.env("a"), h.env("b")
	id := h.startJob(a, "echo from-a; sleep 30")
	waitFor(t, "output visible to another agent", 10*time.Second, func() bool {
		return strings.HasPrefix(h.output(b, id).Text, "from-a\n")
	})
	// Any agent may kill any job.
	if r := h.kill(b, id); r.IsError || !strings.HasPrefix(r.Text, "job job_1 killed") {
		t.Errorf("b killed a's job: %+v", r)
	}
	// The end event still names the agent that started the job.
	evs := jobEvents(h)
	if len(evs) != 2 || evs[1].Agent != "a" || evs[1].Status != "killed" {
		t.Errorf("events = %+v", evs)
	}
}

// Each reader has its own cursor: one agent draining a job must not hide the
// output from another.
func TestPerAgentCursors(t *testing.T) {
	h := newHarness(t)
	a, b := h.env("a"), h.env("b")
	gate := filepath.Join(h.root, "go")
	id := h.startJob(a, fmt.Sprintf("echo first; while [ ! -e %s ]; do sleep 0.02; done; echo second; sleep 30", gate))
	waitFor(t, "first", 10*time.Second, func() bool {
		return strings.Contains(h.output(a, id, map[string]any{"since": 0}).Text, "first")
	})
	// a has read everything so far; a fresh reader b still gets it all.
	if got := h.output(b, id).Text; got != "first\n[job_1 running; next offset 6]" {
		t.Errorf("b's first read = %q", got)
	}
	if got := h.output(a, id).Text; got != "(no new output)\n[job_1 running; next offset 6]" && !strings.HasPrefix(got, "first\n") {
		t.Errorf("a's cursor read = %q", got)
	}
	os.WriteFile(gate, nil, 0o644)
	waitFor(t, "second", 10*time.Second, func() bool {
		return strings.Contains(h.output(a, id, map[string]any{"since": 0}).Text, "second")
	})
	// b continues from where b left off, unaffected by a's reads.
	if got := h.output(b, id).Text; got != "second\n[job_1 running; next offset 13]" {
		t.Errorf("b's second read = %q", got)
	}
}

func TestJobKillReachesStubbornChildren(t *testing.T) {
	h := newHarness(t, Options{KillGrace: 400 * time.Millisecond})
	env := h.env("a")
	pidfile := filepath.Join(h.root, "child.pid")
	id := h.startJob(env, fmt.Sprintf(`sh -c 'trap "" TERM; echo $$ > %s; while :; do sleep 0.1; done' & wait`, pidfile))
	child := readPid(t, pidfile)
	start := time.Now()
	r := h.kill(env, id)
	if r.IsError || !strings.HasPrefix(r.Text, "job job_1 killed") {
		t.Fatalf("kill = %+v", r)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("kill took %v", d)
	}
	// The kill signals the whole group; a child that is being torn down is not gone the very
	// instant the call returns on a loaded machine, so give it a moment (it does not need more).
	waitDead(t, child, 5*time.Second)
}

func TestJobTimeout(t *testing.T) {
	h := newHarness(t, Options{KillGrace: 300 * time.Millisecond})
	env := h.env("a")
	id := h.startJob(env, "sleep 30", map[string]any{"timeout": 0.3})
	waitFor(t, "timeout kill", 10*time.Second, func() bool {
		return strings.Contains(h.output(env, id).Text, "killed (timed out)")
	})
	// Without a timeout a job has no deadline at all, even past the foreground default.
	id2 := h.startJob(env, "sleep 30")
	time.Sleep(200 * time.Millisecond)
	if !strings.Contains(h.output(env, id2).Text, "running") {
		t.Error("job without timeout stopped")
	}
}

func TestJobBufferRolls(t *testing.T) {
	h := newHarness(t, Options{JobBuffer: 2048})
	env := h.env("a")
	id := h.startJob(env, "seq 1 5000; echo THE-END; sleep 30") // 24,000+ bytes of output
	const total = 23893 + len("THE-END\n")
	waitFor(t, "all output", 10*time.Second, func() bool {
		return strings.Contains(h.output(env, id, map[string]any{"since": 1 << 40}).Text, fmt.Sprintf("next offset %d]", total))
	})
	res := h.output(env, id, map[string]any{"since": 0})
	if !strings.HasPrefix(res.Text, "[") || !strings.Contains(res.Text, "bytes of earlier output were dropped") {
		t.Fatalf("no drop notice: %.120q", res.Text)
	}
	if !strings.Contains(res.Text, "5000\nTHE-END\n[job_1 running; next offset") {
		t.Errorf("newest output missing: %q", res.Text[len(res.Text)-100:])
	}
	if strings.Contains(res.Text, "\n1\n2\n3\n") {
		t.Error("the oldest output should have been discarded")
	}
	if n := len(res.Text); n > 4096 {
		t.Errorf("read returned %d bytes from a 2 KiB buffer", n)
	}
	// Reading from a live offset works.
	tail := h.output(env, id, map[string]any{"since": total - 8})
	if want := fmt.Sprintf("THE-END\n[job_1 running; next offset %d]", total); tail.Text != want {
		t.Errorf("tail read = %q, want %q", tail.Text, want)
	}
}

func TestJobOutputIsTruncatedWithHandle(t *testing.T) {
	h := newHarness(t) // 5 MiB job buffer
	env := h.env("a")
	id := h.startJob(env, "seq 1 200000; sleep 30")
	waitFor(t, "output", 20*time.Second, func() bool {
		return strings.Contains(h.output(env, id, map[string]any{"since": 1 << 40}).Text, "next offset 1288895]")
	})
	res := h.output(env, id, map[string]any{"since": 0})
	if !res.Truncated || res.Handle == "" {
		t.Fatalf("expected truncation with a handle, got %d bytes", len(res.Text))
	}
	if !strings.Contains(res.Text, "[job_1 running; next offset 1288895]") {
		t.Errorf("status trailer lost in truncation: %q", res.Text[len(res.Text)-200:])
	}
	full, err := h.blobs.Get(res.FullRef)
	if err != nil || !strings.HasPrefix(string(full), "1\n2\n") || !strings.Contains(string(full), "200000\n[job_1") {
		t.Errorf("blob wrong: %v %.20q", err, full)
	}
}

func TestJobOutputIsSanitized(t *testing.T) {
	h := newHarness(t)
	env := h.env("a")
	id := h.startJob(env, `printf '\033[31mred\033[0m\n10%%\r50%%\rdone\n\000nul\n'; printf 'caf\351\n'; sleep 30`)
	waitFor(t, "output", 10*time.Second, func() bool {
		return strings.Contains(h.output(env, id, map[string]any{"since": 0}).Text, "caf")
	})
	got := h.output(env, id, map[string]any{"since": 0}).Text
	if !strings.HasPrefix(got, "red\ndone\nnul\ncaf�\n[job_1 running") {
		t.Errorf("text = %q", got)
	}
}

func TestJobStartsInAgentDirectoryWithoutMovingIt(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(filepath.Join(h.root, "sub", "inner"), 0o755)
	env := h.env("a")
	h.bash(env, "cd sub")
	id := h.startJob(env, "pwd; cd inner; pwd; echo agent=$SLEIPNIR_AGENT")
	waitFor(t, "job", 10*time.Second, func() bool {
		return strings.Contains(h.output(env, id, map[string]any{"since": 0}).Text, "exited 0")
	})
	want := filepath.Join(h.root, "sub") + "\n" + filepath.Join(h.root, "sub", "inner") + "\nagent=a\n"
	if got := h.output(env, id, map[string]any{"since": 0}).Text; !strings.HasPrefix(got, want) {
		t.Errorf("job output = %q, want prefix %q", got, want)
	}
	// The job's own `cd` does not move the agent.
	if got := h.bash(env, "pwd").Text; got != filepath.Join(h.root, "sub")+"\n[exit code 0]" {
		t.Errorf("agent moved by a background job: %q", got)
	}
}

func TestUnknownJob(t *testing.T) {
	h := newHarness(t)
	env := h.env("a")
	for _, name := range []string{"bash_output", "bash_kill"} {
		res := h.call(context.Background(), env, name, map[string]any{"id": "job_9"})
		if !res.IsError || !strings.Contains(res.Text, `no job "job_9"`) || !strings.Contains(res.Text, "no background jobs have been started") {
			t.Errorf("%s: %+v", name, res)
		}
	}
	h.startJob(env, "sleep 30")
	h.startJob(env, "sleep 30")
	res := h.output(env, "job_9")
	if !res.IsError || !strings.Contains(res.Text, "known jobs: job_1, job_2") {
		t.Errorf("result = %+v", res)
	}
	res = h.output(env, "  job_1  ") // ids are trimmed
	if res.IsError {
		t.Errorf("padded id rejected: %s", res.Text)
	}
	long := strings.Repeat("x", 500)
	if res := h.output(env, long); !res.IsError || len(res.Text) > 400 {
		t.Errorf("long id echoed: %d bytes", len(res.Text))
	}
}

func TestMaxJobs(t *testing.T) {
	h := newHarness(t, Options{MaxJobs: 2, KillGrace: 300 * time.Millisecond})
	env := h.env("a")
	h.startJob(env, "sleep 30")
	h.startJob(env, "sleep 30")
	res := h.bash(env, "sleep 30", map[string]any{"run_in_background": true})
	if !res.IsError || !strings.Contains(res.Text, "2 background jobs are already running") {
		t.Fatalf("third job = %+v", res)
	}
	h.kill(env, "job_1")
	waitFor(t, "job_1 to finish", 5*time.Second, func() bool { return strings.Contains(h.output(env, "job_1").Text, "killed") })
	id := h.startJob(env, "sleep 30")
	if id != "job_3" {
		t.Errorf("id = %s", id)
	}
	// The finished job made room by being forgotten.
	if res := h.output(env, "job_1"); !res.IsError {
		t.Errorf("job_1 should have been evicted: %s", res.Text)
	}
	if res := h.output(env, "job_2"); res.IsError {
		t.Errorf("running job_2 must survive eviction: %s", res.Text)
	}
}

func TestBackgroundLeaderExitsButStrayHoldsPipes(t *testing.T) {
	h := newHarness(t, Options{DrainGrace: 100 * time.Millisecond, KillGrace: 300 * time.Millisecond})
	env := h.env("a")
	id := h.startJob(env, "sleep 60 & echo stray=$!")
	waitFor(t, "leader exit", 10*time.Second, func() bool {
		return strings.Contains(h.output(env, id, map[string]any{"since": 0}).Text, "exited 0")
	})
	var pid int
	if _, err := fmt.Sscanf(h.output(env, id, map[string]any{"since": 0}).Text, "stray=%d", &pid); err != nil {
		t.Fatal(err)
	}
	if !pidAlive(pid) {
		t.Fatal("the stray was killed by job completion")
	}
	r := h.kill(env, id)
	if r.IsError || !strings.Contains(r.Text, "already exited (exit code 0)") || !strings.Contains(r.Text, "stopped leftover processes") {
		t.Errorf("kill = %+v", r)
	}
	waitDead(t, pid, 5*time.Second)
}

func TestShutdown(t *testing.T) {
	h := newHarness(t, Options{KillGrace: 400 * time.Millisecond})
	env := h.env("a")
	stubborn := filepath.Join(h.root, "stubborn.pid")
	stray := filepath.Join(h.root, "stray.pid")
	ids := []string{
		h.startJob(env, "sleep 30"),
		h.startJob(env, fmt.Sprintf(`sh -c 'trap "" TERM; echo $$ > %s; while :; do sleep 0.1; done' & wait`, stubborn)),
		h.startJob(env, "trap '' TERM; sleep 30"),
	}
	var pids []int
	for _, id := range ids {
		pids = append(pids, metaIntFromJob(t, h, id))
	}
	pids = append(pids, readPid(t, stubborn))

	// A foreground command in flight and a stray left behind by another one.
	fg := make(chan *tools.Result, 1)
	go func() {
		res, _ := h.tryCall(context.Background(), h.env("b"), "bash", map[string]any{"command": "echo fg-start; sleep 30"})
		fg <- res
	}()
	h.bash(h.env("c"), fmt.Sprintf(`sh -c 'echo $$ > %s; sleep 60' & sleep 0.1`, stray))
	pids = append(pids, readPid(t, stray))
	time.Sleep(200 * time.Millisecond) // let the foreground command start

	start := time.Now()
	done := make(chan struct{})
	go func() { h.m.Shutdown(); close(done) }()
	go func() { h.m.Shutdown() }() // concurrent and repeated calls are safe
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Shutdown did not return")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Shutdown took %v", d)
	}
	for _, pid := range pids {
		if pidAlive(pid) {
			t.Errorf("pid %d survived Shutdown", pid)
		}
	}
	select {
	case res := <-fg:
		if !res.IsError || !strings.Contains(res.Text, "fg-start") || !strings.Contains(res.Text, "[cancelled: the harness is shutting down]") {
			t.Errorf("foreground result = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Error("foreground command still blocked after Shutdown")
	}
	for _, id := range ids {
		if got := h.output(env, id).Text; !strings.Contains(got, "killed") {
			t.Errorf("%s after shutdown: %q", id, got)
		}
	}
	// New work is refused; reading old output is not.
	if res := h.bash(env, "echo hi"); !res.IsError || !strings.Contains(res.Text, "shut down") {
		t.Errorf("bash after Shutdown = %+v", res)
	}
	if res := h.bash(env, "echo hi", map[string]any{"run_in_background": true}); !res.IsError {
		t.Errorf("job after Shutdown = %+v", res)
	}
	h.m.Shutdown() // idempotent
}

func metaIntFromJob(t *testing.T, h *harness, id string) int {
	t.Helper()
	j := h.m.job(id)
	if j == nil {
		t.Fatalf("no job %s", id)
	}
	return j.p.pid
}

func TestShutdownWithNothingRunning(t *testing.T) {
	m := NewManager()
	m.Shutdown()
	m.Shutdown()
}

// Shutdown racing with new commands must neither deadlock nor panic (a
// WaitGroup Add after Wait would).
func TestShutdownRacesWithStarts(t *testing.T) {
	h := newHarness(t, Options{KillGrace: 200 * time.Millisecond})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := h.env(fmt.Sprintf("r%d", i))
			for k := 0; k < 5; k++ {
				res, err := h.tryCall(context.Background(), env, "bash", map[string]any{"command": "sleep 5", "run_in_background": k%2 == 0, "timeout": 5})
				if err != nil {
					t.Errorf("harness error: %v", err)
					return
				}
				_ = res
			}
		}()
	}
	time.Sleep(30 * time.Millisecond)
	done := make(chan struct{})
	go func() { h.m.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Shutdown deadlocked")
	}
	wg.Wait()
}

// Events must not be emitted from a nil emitter, and a panicking emitter is
// the harness's bug; but a closed log returning errors must not disturb jobs.
type failingEmitter struct{}

func (failingEmitter) Emit(string, string, any, ...events.Opt) (uint64, error) {
	return 0, fmt.Errorf("log closed")
}

func TestJobsSurviveEmitterErrors(t *testing.T) {
	h := newHarness(t)
	env := h.env("a")
	env.Emit = failingEmitter{}
	id := h.startJob(env, "echo ok")
	waitFor(t, "job to exit", 10*time.Second, func() bool {
		return strings.Contains(h.output(env, id, map[string]any{"since": 0}).Text, "exited 0")
	})
}

// Jobs are session-wide, but that must not put them outside the permission
// engine: a read-only role must not read a writer's output or kill its runs.
func TestCrossAgentJobAccessIsPermissionGated(t *testing.T) {
	h := newHarness(t, Options{KillGrace: 300 * time.Millisecond})
	owner := h.env("be-1")
	id := h.startJob(owner, "echo secret-from-be-1; sleep 30")
	waitFor(t, "output", 10*time.Second, func() bool {
		return strings.Contains(h.output(owner, id, map[string]any{"since": 0}).Text, "secret-from-be-1")
	})

	t.Run("denied", func(t *testing.T) {
		deny := &recordingPerm{allow: false, why: "read-only role"}
		rv := h.env("rv-1")
		rv.Role = "reviewer"
		rv.Perm = deny

		out := h.output(rv, id)
		if !out.IsError || out.Text != "permission denied: read-only role" || strings.Contains(out.Text, "secret") {
			t.Errorf("bash_output = %+v", out)
		}
		kill := h.kill(rv, id)
		if !kill.IsError || kill.Text != "permission denied: read-only role" {
			t.Errorf("bash_kill = %+v", kill)
		}
		if state, _, _ := h.m.job(id).snapshot(); state != jobRunning {
			t.Error("a denied kill stopped the job")
		}
		reqs := deny.seen()
		if len(reqs) != 2 {
			t.Fatalf("%d permission requests, want 2", len(reqs))
		}
		for i, want := range []struct {
			tool   string
			writes bool
		}{{"bash_output", false}, {"bash_kill", true}} {
			r := reqs[i]
			if r.Tool != want.tool || r.Writes != want.writes || r.Agent != "rv-1" || r.Role != "reviewer" || r.Command != "" || r.Paths != nil || r.Network {
				t.Errorf("request %d = %+v", i, r)
			}
			for _, s := range []string{"job_1", "be-1", "echo secret-from-be-1"} {
				if !strings.Contains(r.Summary, s) {
					t.Errorf("request %d summary %q lacks %q", i, r.Summary, s)
				}
			}
			if !json.Valid(r.Input) {
				t.Errorf("request %d input = %q", i, r.Input)
			}
		}
	})
	t.Run("denied without reason", func(t *testing.T) {
		rv := h.env("rv-2")
		rv.Perm = &recordingPerm{}
		if out := h.output(rv, id); out.Text != "permission denied" {
			t.Errorf("text = %q", out.Text)
		}
	})
	t.Run("allowed", func(t *testing.T) {
		allow := &recordingPerm{allow: true}
		mgr := h.env("mgr")
		mgr.Perm = allow
		if out := h.output(mgr, id, map[string]any{"since": 0}); out.IsError || !strings.HasPrefix(out.Text, "secret-from-be-1\n") {
			t.Errorf("bash_output = %+v", out)
		}
		if n := len(allow.seen()); n != 1 {
			t.Errorf("%d requests", n)
		}
	})
	t.Run("an agent's own jobs need no approval", func(t *testing.T) {
		strict := &recordingPerm{allow: false, why: "no"}
		owner.Perm = strict
		if out := h.output(owner, id, map[string]any{"since": 0}); out.IsError {
			t.Errorf("owner denied its own job: %+v", out)
		}
		if n := len(strict.seen()); n != 0 {
			t.Errorf("the permission engine was asked %d times about the owner's own job", n)
		}
	})
	t.Run("unknown ids are not a permission matter", func(t *testing.T) {
		rv := h.env("rv-3")
		rv.Perm = &recordingPerm{}
		if out := h.output(rv, "job_99"); !strings.Contains(out.Text, `no job "job_99"`) {
			t.Errorf("text = %q", out.Text)
		}
	})
	t.Run("permitted kill works", func(t *testing.T) {
		mgr := h.env("mgr2")
		mgr.Perm = &recordingPerm{allow: true}
		if r := h.kill(mgr, id); r.IsError || !strings.HasPrefix(r.Text, "job job_1 killed") {
			t.Errorf("kill = %+v", r)
		}
	})
}
