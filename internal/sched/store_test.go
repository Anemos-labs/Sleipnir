package sched

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStoreAddListRemoveAndDue(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "state", "schedule.json")}
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	if jobs, err := s.List(); err != nil || len(jobs) != 0 {
		t.Fatalf("a missing file holds no jobs: %v %v", jobs, err)
	}
	a, err := s.Add(Job{Cron: "0 9 * * *", Goal: "summarize the open issues"}, t0)
	if err != nil || a.ID != "j1" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := s.Add(Job{Cron: "*/30 * * * *", Goal: "check CI"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Job{Cron: "nonsense", Goal: "x"}, t0); err == nil {
		t.Error("a bad cron was stored")
	}
	if _, err := s.Add(Job{Cron: "* * * * *", Goal: "  "}, t0); err == nil {
		t.Error("a job without a goal was stored")
	}
	if fi, err := os.Stat(s.Path); err != nil {
		t.Error(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows keeps no POSIX modes
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	jobs, _ := s.List()
	if due := Due(jobs, t0.Add(10*time.Minute)); len(due) != 0 {
		t.Errorf("nothing is due at 08:10: %v", due)
	}
	due := Due(jobs, t0.Add(31*time.Minute))
	if len(due) != 1 || due[0].ID != "j2" {
		t.Errorf("08:31: the half-hourly job is due: %v", due)
	}
	// A daemon that was down for a day starts each job once; after LastRun is recorded, it is not due again.
	late := t0.Add(26 * time.Hour)
	due = Due(jobs, late)
	if len(due) != 2 {
		t.Fatalf("both are overdue: %v", due)
	}
	for i := range jobs {
		jobs[i].LastRun = late
	}
	if due := Due(jobs, late.Add(time.Second)); len(due) != 0 {
		t.Errorf("run once, not once per missed slot: %v", due)
	}
	if n, ok := Next(jobs[0], late); !ok || n.Hour() != 9 {
		t.Errorf("next: %v %v", n, ok)
	}
	if err := s.Remove("j1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("j9"); err == nil {
		t.Error("removing nothing is an error")
	}
	if jobs, _ := s.List(); len(jobs) != 1 || jobs[0].ID != "j2" {
		t.Errorf("after remove: %v", jobs)
	}
	if a, err := s.Add(Job{Cron: "@daily", Goal: "g"}, t0); err != nil || a.ID != "j3" {
		t.Errorf("IDs are not reused: %+v %v", a, err)
	}
}
