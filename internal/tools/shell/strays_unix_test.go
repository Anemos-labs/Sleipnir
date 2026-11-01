//go:build unix

package shell

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What an agent's finished command left running is listed with the agent, the command and its live processes, whether it still
// holds the command's pipes or not, and a person stops it by its id through StopJob.
func TestStraysAreListedAndStoppedByTheirID(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Options{DrainGrace: 100 * time.Millisecond, KillGrace: 300 * time.Millisecond})
	t.Cleanup(h.m.Shutdown)
	start := func(agent, command string) (pid, pgid int) {
		t.Helper()
		res := h.bash(h.env(agent), command+` & echo "pid=$!"`)
		if _, err := fmt.Sscanf(res.Text, "pid=%d", &pid); err != nil {
			t.Fatalf("text = %q (%v)", res.Text, err)
		}
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		g, err := syscall.Getpgid(pid)
		if err != nil {
			t.Fatal(err)
		}
		return pid, g
	}
	detached, dg := start("w1", "nohup sleep 601 >/dev/null 2>&1 </dev/null")
	holding, hg := start("w2", "sleep 602")
	h.startJob(h.env("w1"), "sleep 603") // a running job is a job, not a stray

	var got []StrayInfo
	waitFor(t, "both strays to be listed", 5*time.Second, func() bool {
		got = h.m.Strays()
		return len(got) == 2
	})
	byID := map[string]StrayInfo{}
	for _, s := range got {
		byID[s.ID] = s
	}
	d, ok := byID[StrayPrefix+strconv.Itoa(dg)]
	if !ok || d.Agent != "w1" || !strings.Contains(d.Command, "nohup sleep 601") || d.Pipes || d.FromJob || d.Started.IsZero() {
		t.Fatalf("the detached stray: %+v (all: %+v)", d, got)
	}
	hs, ok := byID[StrayPrefix+strconv.Itoa(hg)]
	if !ok || hs.Agent != "w2" || !hs.Pipes {
		t.Fatalf("the stray holding its pipes: %+v (all: %+v)", hs, got)
	}
	if _, err := os.Stat("/proc/self/stat"); err == nil && (!memberOf(d.Procs, detached, "sleep 601") || !memberOf(hs.Procs, holding, "sleep 602")) { // the processes are listed where /proc is
		t.Errorf("the live processes are not listed: %+v / %+v", d.Procs, hs.Procs)
	}

	for _, s := range got {
		if err := h.m.StopJob(s.ID); err != nil {
			t.Fatalf("StopJob(%s): %v", s.ID, err)
		}
	}
	for _, pid := range []int{detached, holding} {
		waitFor(t, "the stopped strays to end", 5*time.Second, func() bool { return !pidAlive(pid) })
	}
	waitFor(t, "the list to empty", 5*time.Second, func() bool { return len(h.m.Strays()) == 0 })
	if err := h.m.StopJob(StrayPrefix + strconv.Itoa(dg)); err == nil {
		t.Error("stopping a stray that is gone did not say so")
	}
}

// memberOf reports whether ms lists pid with a command line that holds cmd.
func memberOf(ms []Member, pid int, cmd string) bool {
	for _, m := range ms {
		if m.PID == pid && strings.Contains(m.Cmd, cmd) {
			return true
		}
	}
	return false
}

// A person reads the end of a job's output and its whole command without moving the agent's read position.
func TestJobOutputReadsTheTailWithoutMovingTheAgentsCursor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	t.Cleanup(h.m.Shutdown)
	command := "for i in 1 2 3; do echo line$i; done\n# a second line of the command"
	id := h.startJob(h.env("w1"), command)
	var text string
	waitFor(t, "the job's output", 5*time.Second, func() bool {
		text, _, _ = h.m.JobOutput(id, 0)
		return strings.Contains(text, "line3")
	})
	if !strings.Contains(text, "line1") {
		t.Errorf("JobOutput(all) = %q", text)
	}
	tail, omitted, err := h.m.JobOutput(id, 6)
	if err != nil || tail != "line3\n" || omitted == 0 {
		t.Errorf("JobOutput(6) = %q, %d, %v", tail, omitted, err)
	}
	if full, err := h.m.JobCommand(id); err != nil || full != command {
		t.Errorf("JobCommand = %q, %v", full, err)
	}
	if out := h.output(h.env("w1"), id).Text; !strings.Contains(out, "line1") {
		t.Errorf("the agent's next read lost output a person read: %q", out)
	}
	if _, _, err := h.m.JobOutput("p4242", 0); err == nil || !strings.Contains(err.Error(), "not a job") {
		t.Errorf("a stray's output: %v", err)
	}
	if _, _, err := h.m.JobOutput("job_999", 0); err == nil {
		t.Error("an unknown job has no error")
	}
}
