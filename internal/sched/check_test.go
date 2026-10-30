package sched

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestTickRunsAJobOnlyWhenItsCheckChanges: a due job with a check runs on its first look and after the checked value changes; a slot
// whose check prints the same value is recorded as "no change" without launching, and a failed run keeps the old value so that the
// next slot tries again.
func TestTickRunsAJobOnlyWhenItsCheckChanges(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "schedule.json")}
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	j, err := s.Add(Job{Cron: "@hourly", Goal: "triage new issues", Check: "count issues"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	value := "3"
	checks := 0
	defer func(f func(context.Context, Job) (string, error)) { runCheck = f }(runCheck)
	runCheck = func(_ context.Context, got Job) (string, error) {
		checks++
		if got.Check != "count issues" {
			t.Errorf("check %q", got.Check)
		}
		return "sum-" + value, nil
	}
	launches, exit := 0, "ok"
	launch := func(context.Context, Job, time.Time) (string, string) { launches++; return exit, "x.log" }
	slot := func(h int) Job {
		t.Helper()
		var log bytes.Buffer
		if err := Tick(context.Background(), s, t0.Add(time.Duration(h)*time.Hour+time.Minute), launch, &log); err != nil {
			t.Fatal(err)
		}
		jobs, _ := s.List()
		return jobs[0]
	}

	if got := slot(1); launches != 1 || got.LastExit != "ok" || got.CheckSum != "sum-3" {
		t.Fatalf("first look: launches %d, %+v", launches, got)
	}
	if got := slot(2); launches != 1 || got.LastExit != ExitNoChange || got.CheckSum != "sum-3" || got.LastRun.IsZero() {
		t.Fatalf("unchanged: launches %d, %+v", launches, got)
	}
	value, exit = "4", "exit status 1"
	if got := slot(3); launches != 2 || got.CheckSum != "sum-3" {
		t.Fatalf("a failed run must keep the old value: launches %d, %+v", launches, got)
	}
	exit = "ok"
	if got := slot(4); launches != 3 || got.CheckSum != "sum-4" {
		t.Fatalf("retry after failure: launches %d, %+v", launches, got)
	}
	if checks != 4 {
		t.Errorf("checks %d, want one per slot", checks)
	}
	// a changed check forgets the value: its first look runs the job
	if _, err := s.Update(j.ID, func(x *Job) { x.Check = "count open issues" }); err != nil {
		t.Fatal(err)
	}
	runCheck = func(context.Context, Job) (string, error) { return "sum-4", nil }
	if got := slot(5); launches != 4 || got.CheckSum != "sum-4" {
		t.Fatalf("edited check: launches %d, %+v", launches, got)
	}
}

// TestTickSkipsARunWhoseCheckFails: a check that fails stops the run and says why; nothing is launched.
func TestTickSkipsARunWhoseCheckFails(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "schedule.json")}
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	if _, err := s.Add(Job{Cron: "@hourly", Goal: "g", Check: "false"}, t0); err != nil {
		t.Fatal(err)
	}
	defer func(f func(context.Context, Job) (string, error)) { runCheck = f }(runCheck)
	runCheck = func(context.Context, Job) (string, error) { return "", context.DeadlineExceeded }
	var log bytes.Buffer
	err := Tick(context.Background(), s, t0.Add(61*time.Minute), func(context.Context, Job, time.Time) (string, string) {
		t.Error("launched although the check failed")
		return "ok", ""
	}, &log)
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	if !strings.HasPrefix(jobs[0].LastExit, "check failed: ") || !strings.Contains(log.String(), "skipped: check failed") {
		t.Fatalf("exit %q, log %q", jobs[0].LastExit, log.String())
	}
}

// TestRunCheckDigestsStdoutInTheJobsDirectory: the digest follows standard output only, the check runs in the job's folder, and a
// non-zero exit is an error naming the status and the last line of standard error.
func TestRunCheckDigestsStdoutInTheJobsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "v"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	j := Job{Dir: dir, Check: "cat v; echo noise >&2"}
	a, err := RunCheck(context.Background(), j)
	if err != nil || a == "" {
		t.Fatalf("%q %v", a, err)
	}
	b, _ := RunCheck(context.Background(), Job{Dir: dir, Check: "cat v; echo other noise >&2"})
	if a != b {
		t.Error("standard error changed the digest")
	}
	_ = os.WriteFile(filepath.Join(dir, "v"), []byte("2"), 0o600)
	if c, _ := RunCheck(context.Background(), j); c == a {
		t.Error("a changed value kept the digest")
	}
	_, err = RunCheck(context.Background(), Job{Dir: dir, Check: "echo out; echo 'no token' >&2; exit 4"})
	if err == nil || err.Error() != "exit status 4: no token" {
		t.Errorf("err %v", err)
	}
}
