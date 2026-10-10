package sched

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// TestMain lets the test binary stand in for a second process: a writer of the job file, or the `sleipnir run` of a job.
func TestMain(m *testing.M) {
	switch os.Getenv("SCHED_TEST_CHILD") {
	case "add":
		st := Store{Path: os.Getenv("SCHED_TEST_STORE")}
		n, _ := strconv.Atoi(os.Getenv("SCHED_TEST_N"))
		for i := 0; i < n; i++ {
			if _, err := st.Add(Job{Cron: "@daily", Goal: fmt.Sprintf("child %d", i)}, time.Now()); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		os.Exit(0)
	case "lock":
		unlock, pid, err := Lock(os.Getenv("SCHED_TEST_STORE"))
		fmt.Printf("%d %v\n", pid, err)
		if err == nil {
			unlock()
		}
		os.Exit(0)
	case "run":
		fmt.Println("args:", strings.Join(os.Args[1:], "|"))
		fmt.Fprintln(os.Stderr, "key:", os.Getenv("SCHED_TEST_API_KEY") != "")
		if os.Getenv("SCHED_TEST_FAIL") != "" {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestUpdatePauseAndDue(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "schedule.json")}
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	j, err := s.Add(Job{Cron: "*/30 * * * *", Goal: "check CI"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Update(j.ID, func(x *Job) { x.Paused, x.ID, x.Goal, x.Model = true, "j99", "check CI again", "m/x" })
	if err != nil || u.ID != j.ID || !u.Paused || u.Goal != "check CI again" || u.Model != "m/x" || !u.Created.Equal(t0) {
		t.Fatalf("update: %+v %v", u, err)
	}
	jobs, _ := s.List()
	if due := Due(jobs, t0.Add(time.Hour)); len(due) != 0 {
		t.Errorf("a paused job is not due: %v", due)
	}
	if _, ok := Next(jobs[0], t0); ok {
		t.Error("a paused job has no next run")
	}
	if _, err := s.Update(j.ID, func(x *Job) { x.Cron = "nonsense" }); err == nil {
		t.Error("a bad cron was stored by an edit")
	}
	if _, err := s.Update(j.ID, func(x *Job) { x.Goal = " " }); err == nil {
		t.Error("an edit removed the goal")
	}
	if _, err := s.Update("j7", func(*Job) {}); !errors.Is(err, ErrNoJob) {
		t.Errorf("no such job: %v", err)
	}
	if _, err := s.Update(j.ID, func(x *Job) { x.Paused = false }); err != nil {
		t.Fatal(err)
	}
	jobs, _ = s.List()
	if due := Due(jobs, t0.Add(time.Hour)); len(due) != 1 {
		t.Errorf("resumed, it is due again: %v", due)
	}
	if jobs[0].Cron != "*/30 * * * *" {
		t.Errorf("a refused edit changed the job: %+v", jobs[0])
	}
}

// Writers in this process and in another do not lose each other's jobs.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "schedule.json")}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SCHED_TEST_CHILD=add", "SCHED_TEST_STORE="+s.Path, "SCHED_TEST_N=25")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 10 {
				if _, err := s.Add(Job{Cron: "@hourly", Goal: fmt.Sprintf("parent %d %d", g, i)}, time.Now()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v %s", err, stderr.String())
	}
	jobs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, j := range jobs {
		ids[j.ID] = true
	}
	if len(jobs) != 65 || len(ids) != 65 {
		t.Fatalf("%d jobs, %d distinct ids; want 65", len(jobs), len(ids))
	}
}

// The daemon lock refuses a second daemon, in this process or another, and names the holder.
func TestDaemonLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "schedule.lock")
	if pid, held := Probe(path); held || pid != 0 {
		t.Fatal("nothing holds a lock that does not exist")
	}
	unlock, _, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, pid, err := Lock(path); !errors.Is(err, ErrLocked) || pid != os.Getpid() {
		t.Errorf("a second daemon here: pid %d, %v", pid, err)
	}
	if pid, held := Probe(path); !held || pid != os.Getpid() {
		t.Errorf("probe: %d %v", pid, held)
	}
	out, err := childLock(t, path)
	if err != nil || out != fmt.Sprintf("%d %v", os.Getpid(), ErrLocked) {
		t.Errorf("another process: %q %v", out, err)
	}
	unlock()
	unlock() // twice is harmless
	if _, held := Probe(path); held {
		t.Error("released, nothing holds it")
	}
	if out, _ := childLock(t, path); out != "0 <nil>" {
		t.Errorf("free, another process takes it: %q", out)
	}
}

// childLock runs Lock in another process and returns what it printed.
func childLock(t *testing.T, path string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SCHED_TEST_CHILD=lock", "SCHED_TEST_STORE="+path)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func TestTickSkipsPausedJobsAndToleratesRemoval(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "schedule.json")}
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	a, _ := s.Add(Job{Cron: "*/30 * * * *", Goal: "a"}, t0)
	b, _ := s.Add(Job{Cron: "*/30 * * * *", Goal: "b"}, t0)
	if _, err := s.Update(b.ID, func(j *Job) { j.Paused = true }); err != nil {
		t.Fatal(err)
	}
	var started []string
	var log bytes.Buffer
	err := Tick(context.Background(), s, t0.Add(31*time.Minute), func(_ context.Context, j Job, _ time.Time) (string, string) {
		started = append(started, j.ID)
		if err := s.Remove(j.ID); err != nil { // removed while it ran
			t.Error(err)
		}
		return "ok", "x.log"
	}, &log)
	if err != nil || strings.Join(started, ",") != a.ID {
		t.Fatalf("started %v: %v", started, err)
	}
	if !strings.Contains(log.String(), a.ID+" starts: a") || !strings.Contains(log.String(), a.ID+" ended: ok") {
		t.Errorf("log: %q", log.String())
	}
}

func TestRunJobRunsTheJobWithItsKeysAndLogsIt(t *testing.T) {
	const name = "SCHED_TEST_API_KEY"
	t.Setenv(name, "")
	harden.Provide(name, "sk-test-0123456789abcdef")
	t.Cleanup(func() { harden.Provide(name, "") })
	logs := t.TempDir()
	j := Job{ID: "j4", Goal: "do it", Model: "m/x", Dir: "/w", Mode: "plan", BudgetUSD: 2}
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	env := append(JobEnv(os.Environ()), "SCHED_TEST_CHILD=run")
	var out bytes.Buffer
	exit, log := RunJob(context.Background(), os.Args[0], logs, j, now, env, &out)
	if exit != "ok" || log != filepath.Join(logs, "j4-20261001-080000.log") {
		t.Fatalf("%q %q", exit, log)
	}
	b, _ := os.ReadFile(log)
	want := "args: run|--quiet|--model|m/x|--cwd|/w|--mode|plan|--budget-usd|2|--|do it"
	for _, got := range []string{string(b), out.String()} {
		if !strings.Contains(got, want) || !strings.Contains(got, "key: true") {
			t.Errorf("output %q", got)
		}
	}
	if strings.Contains(strings.Join(Args(j), " "), "sk-test") {
		t.Error("a key is in the arguments")
	}
	env = append(env, "SCHED_TEST_FAIL=1")
	if exit, _ := RunJob(context.Background(), os.Args[0], logs, j, now.Add(time.Second), env, nil); exit != "exit status 3" {
		t.Errorf("a failed run: %q", exit)
	}
}
