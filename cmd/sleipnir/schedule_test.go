package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/sched"
)

func TestDaemonTickStartsWhatIsDueOnceAndRecordsHowItEnded(t *testing.T) {
	st := sched.Store{Path: filepath.Join(t.TempDir(), "schedule.json")}
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, j := range []sched.Job{{Cron: "*/30 * * * *", Goal: "check CI"}, {Cron: "0 9 * * *", Goal: "morning summary"}} {
		if _, err := st.Add(j, t0); err != nil {
			t.Fatal(err)
		}
	}
	var started []string
	var sawRunning bool
	launch := func(_ context.Context, j sched.Job, _ time.Time) (string, string) {
		started = append(started, j.ID)
		jobs, _ := st.List()
		sawRunning = sawRunning || jobs[0].LastExit == "running" // the start is on disk before the run: a crash cannot start it twice
		return "ok", "/logs/" + j.ID + ".log"
	}
	var out bytes.Buffer
	now := t0.Add(31 * time.Minute)
	if err := daemonTick(context.Background(), st, now, launch, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(started, ",") != "j1" || !sawRunning {
		t.Fatalf("started %v (running recorded first: %v)", started, sawRunning)
	}
	if err := daemonTick(context.Background(), st, now.Add(time.Second), launch, &out); err != nil || len(started) != 1 {
		t.Fatalf("not twice for one slot: %v %v", started, err)
	}
	jobs, _ := st.List()
	if jobs[0].LastExit != "ok" || jobs[0].Log != "/logs/j1.log" || !jobs[0].LastRun.Equal(now) || !jobs[1].LastRun.IsZero() {
		t.Errorf("records: %+v", jobs)
	}
	var list bytes.Buffer
	if err := listJobs(&list, st, now); err != nil || !strings.Contains(list.String(), "check CI") || !strings.Contains(list.String(), "10-01 09:00") {
		t.Errorf("list: %v\n%s", err, list.String())
	}
	empty := sched.Store{Path: filepath.Join(t.TempDir(), "none.json")}
	list.Reset()
	if err := listJobs(&list, empty, now); err != nil || !strings.Contains(list.String(), "no scheduled jobs") {
		t.Errorf("empty list: %v %q", err, list.String())
	}
}

// Keys the process holds in memory (moved out of the environment at start, or stored by `sleipnir login`) are not in os.Environ, so a plain
// child of the daemon would have none: the job's environment gets them back.
func TestAJobsEnvironmentCarriesTheKeysTheProcessHolds(t *testing.T) {
	const name = "ACME_JOB_API_KEY"
	t.Setenv(name, "")
	harden.Provide(name, "sk-test-1234567890abcdef")
	t.Cleanup(func() { harden.Provide(name, "") })
	var found bool
	for _, e := range jobEnv(os.Environ()) {
		found = found || e == name+"=sk-test-1234567890abcdef"
	}
	if !found {
		t.Error("the job would start without its key")
	}
}
