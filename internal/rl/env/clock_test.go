package env

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// The clock does not run while the endpoint is down: an outage longer than the whole budget leaves the budget where it was, and
// the budget runs out once the endpoint is back and the rest of it is spent.
func TestTheWallClockStopsWhileTheEndpointIsDown(t *testing.T) {
	c := NewWallClock(150*time.Millisecond, 0, nil)
	ctx, cancel := c.Watch(context.Background())
	defer cancel()
	if !c.Down("server (http 503)") {
		t.Fatal("the first failure must begin an outage")
	}
	if c.Down("network: connection refused") {
		t.Error("a second failure during an outage began another one")
	}
	select {
	case <-ctx.Done():
		t.Fatalf("the budget ran out during an outage: %v", context.Cause(ctx))
	case <-time.After(300 * time.Millisecond):
	}
	lasted, ended := c.Up()
	if !ended || lasted < 300*time.Millisecond {
		t.Fatalf("Up = %v, %v: the outage lasted at least 300ms", lasted, ended)
	}
	if _, again := c.Up(); again {
		t.Error("an answer with no outage under way ended one")
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the budget never ran out once the endpoint was back")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrWallBudget) || !errors.Is(cause, context.DeadlineExceeded) {
		t.Errorf("cause %v, want the wall budget, which is a deadline", cause)
	}
	c.Stop()
	if a, p := c.Active(), c.Paused(); a < 150*time.Millisecond || a > 2*time.Second || p < 300*time.Millisecond {
		t.Errorf("active %v paused %v: the budget's 150ms active and the outage's 300ms paused", a, p)
	}
	if c.Outages() != 1 {
		t.Errorf("%d outages, want 1", c.Outages())
	}
}

// Outages together have a limit: past it the run ends, for the endpoint's reason and not the policy's.
func TestOutagesThatAddUpToTheLimitEndTheRun(t *testing.T) {
	var said []string
	c := NewWallClock(time.Hour, 400*time.Millisecond, func(down bool, cause string) {
		if down {
			said = append(said, "down: "+cause)
		} else {
			said = append(said, "up")
		}
	})
	ctx, cancel := c.Watch(context.Background())
	defer cancel()
	c.Down("timeout: no response from the server within 2m0s")
	time.Sleep(120 * time.Millisecond)
	c.Up()
	select {
	case <-ctx.Done():
		t.Fatalf("120ms of a 400ms limit ended the run: %v", context.Cause(ctx))
	case <-time.After(50 * time.Millisecond):
	}
	c.Down("server (http 502)")
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("outages past the limit did not end the run")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrOutageLimit) {
		t.Errorf("cause %v, want the outage limit", cause)
	}
	if p := c.Paused(); p < 400*time.Millisecond {
		t.Errorf("paused %v, want at least the limit", p)
	}
	if strings.Join(said, "; ") != "down: timeout: no response from the server within 2m0s; up; down: server (http 502)" {
		t.Errorf("notified: %q", said)
	}
}

// A stopped clock keeps what it had, and a clock the runner does not keep (nil) can be told anything.
func TestAStoppedClockIsFrozenAndANilOneIsHarmless(t *testing.T) {
	c := NewWallClock(0, 0, nil)
	c.Down("network: connection reset")
	time.Sleep(20 * time.Millisecond)
	c.Stop()
	p := c.Paused()
	time.Sleep(20 * time.Millisecond)
	if c.Paused() != p || p < 20*time.Millisecond {
		t.Errorf("paused %v then %v: a stopped clock counts the outage under way up to the stop, and no further", p, c.Paused())
	}
	if _, ended := c.Up(); ended || c.Down("again") {
		t.Error("a stopped clock changed state")
	}

	var none *WallClock
	if none.Down("x") || none.Active() != 0 || none.Paused() != 0 || none.Outages() != 0 {
		t.Error("a nil clock reported something")
	}
	if _, ended := none.Up(); ended {
		t.Error("a nil clock ended an outage")
	}
	none.Stop()
}

// What a rollout records of an outage: the wall budget of the task is spent on the active time only, so an outage longer than the
// whole budget does not end the rollout; the episode, the summary and the ledger say how long it was paused; the progress stream
// says when the endpoint went down and came back.
func TestAnOutageDoesNotCountAgainstARolloutsWallBudget(t *testing.T) {
	was := outageNotice
	outageNotice = 200 * time.Millisecond // the stream says so once an outage has lasted this long
	t.Cleanup(func() { outageNotice = was })
	f := newRunnerFixture(t, quickVerify)
	f.task.Budget.WallS = 0
	f.r.MaxWall = 500 * time.Millisecond
	f.h.Default = FakeScript{Steps: []FakeStep{
		FakeDown("network: cannot connect to gw.example: connection refused"), FakeSleep(900 * time.Millisecond), FakeUp(),
		FakeWrite("mathx.go", fixedMath),
	}, Claimed: "done"}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	r := sum.Results[0]
	if sum.Completed != 1 || r.Status != StatusOK || !r.Pass || r.Claimed != "done" {
		t.Fatalf("an outage longer than the wall budget ended the rollout: %+v", r)
	}
	for _, fl := range r.Flags {
		if fl == rl.FlagBudgetExceeded {
			t.Fatalf("the outage was charged to the budget: flags %v", r.Flags)
		}
	}
	if r.PausedMs < 900 || r.ActiveWallMs >= 500 || r.WallMs < 900 {
		t.Errorf("wall %dms active %dms paused %dms: the 900ms outage is paused, not active", r.WallMs, r.ActiveWallMs, r.PausedMs)
	}
	ep := f.episode("mathx-max", 0)
	if ep.Cost.PausedMs != r.PausedMs || ep.Cost.ActiveWallMs != r.ActiveWallMs {
		t.Errorf("episode cost %+v, result %+v", ep.Cost, r)
	}
	if sum.PausedMs != r.PausedMs {
		t.Errorf("summary paused %d, want %d", sum.PausedMs, r.PausedMs)
	}
	es := readLedger(t, f.out)
	if len(es) != 1 || es[0].PausedMs != r.PausedMs || es[0].ActiveWallMs != r.ActiveWallMs {
		t.Errorf("ledger %+v", es)
	}
	var phases []string
	f.pmu.Lock()
	for _, p := range f.progress {
		if p.Type == "rollout.outage" {
			phases = append(phases, p.Status+":"+p.Error)
			if p.Status == "up" && p.OutageMs < 900 {
				t.Errorf("the end of the outage says it lasted %dms", p.OutageMs)
			}
		}
	}
	f.pmu.Unlock()
	if strings.Join(phases, " ") != "down:network: cannot connect to gw.example: connection refused up:" {
		t.Errorf("progress of the outage: %q", phases)
	}
}

// A blip (a 503 that the next attempt gets past) stops the clock for a moment and is in the log, but the progress stream, which
// a person reads for every rollout in flight, says nothing of it.
func TestABriefOutageIsNotAnnounced(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.h.Default = FakeScript{Steps: []FakeStep{FakeDown("server (http 503)"), FakeSleep(20 * time.Millisecond), FakeUp(),
		FakeWrite("mathx.go", fixedMath)}}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if r := sum.Results[0]; r.Status != StatusOK || r.PausedMs < 20 {
		t.Fatalf("%+v", r)
	}
	f.pmu.Lock()
	defer f.pmu.Unlock()
	for _, p := range f.progress {
		if p.Type == "rollout.outage" {
			t.Errorf("a 20ms outage was announced: %+v", p)
		}
	}
}

// An endpoint that stays down past the limit ends the rollout as an infrastructure failure, at once: another attempt would sit
// the same outage out from the start.
func TestOutagesPastTheLimitEndARolloutAsInfra(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.r.MaxOutagePause = 200 * time.Millisecond
	f.r.InfraRetries = 2
	f.h.Default = FakeScript{Steps: []FakeStep{FakeWrite("mathx.go", fixedMath), FakeDown("server (http 503)"), FakeHang()}}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	r := sum.Results[0]
	if sum.Infra != 1 || r.Status != StatusInfra || r.Attempts != 1 || len(f.h.Calls()) != 1 {
		t.Fatalf("status %s after %d attempt(s), %d calls: want one infra attempt, not retried", r.Status, r.Attempts, len(f.h.Calls()))
	}
	if !strings.Contains(r.Error, ErrOutageLimit.Error()) || !strings.Contains(r.Error, "--max-outage-pause") {
		t.Errorf("error %q does not say the endpoint was down too long", r.Error)
	}
	if r.PausedMs < 200 {
		t.Errorf("paused %dms, want at least the limit", r.PausedMs)
	}
	if ep := f.episode("mathx-max", 0); !ep.Has(rl.FlagInfraError) || ep.Cost.PausedMs < 200 {
		t.Errorf("episode flags %v cost %+v", ep.Flags, ep.Cost)
	}
}

// The clock still runs while the endpoint answers: a rollout that spends its budget working ends as a budget episode, whatever it
// sat out before.
func TestActiveTimeStillSpendsTheWallBudget(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.task.Budget.WallS = 0
	f.r.MaxWall = 300 * time.Millisecond
	f.h.Default = FakeScript{Steps: []FakeStep{FakeDown("server (http 502)"), FakeSleep(400 * time.Millisecond), FakeUp(), FakeHang()}}
	start := time.Now()
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	r := sum.Results[0]
	if r.Status != StatusOK || r.Claimed != "budget" {
		t.Fatalf("status %s claimed %q: a rollout that used its active time up is a budget episode", r.Status, r.Claimed)
	}
	if !strings.Contains(strings.Join(r.Flags, ","), rl.FlagBudgetExceeded) {
		t.Errorf("flags %v lack %s", r.Flags, rl.FlagBudgetExceeded)
	}
	if r.ActiveWallMs < 300 || r.PausedMs < 400 || time.Since(start) < 700*time.Millisecond {
		t.Errorf("active %dms paused %dms after %v", r.ActiveWallMs, r.PausedMs, time.Since(start))
	}
}

// With the pause turned off, the clock runs through an outage as it always did.
func TestANegativePauseLimitKeepsThePlainWallClock(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	f.task.Budget.WallS = 0
	f.r.MaxWall = 300 * time.Millisecond
	f.r.MaxOutagePause = -1
	f.r.OutagePatience = -1
	var got RunSpec
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		got = spec
		spec.Clock.Down("ignored: there is no clock")
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	}
	sum := f.rollout([]rl.Task{f.task}, 1, f.opts())
	if r := sum.Results[0]; r.Claimed != "budget" || r.PausedMs != 0 || r.ActiveWallMs != 0 {
		t.Fatalf("%+v", r)
	}
	if got.Clock != nil || got.OutagePatience >= 0 {
		t.Errorf("clock %v patience %v: both off", got.Clock, got.OutagePatience)
	}
}

// The harness is told how patient its agents are and given the clock to report to; the manifest records both settings.
func TestTheOutageSettingsReachTheHarnessAndTheManifest(t *testing.T) {
	f := newRunnerFixture(t, quickVerify)
	var got RunSpec
	f.h.Func = func(ctx context.Context, spec RunSpec) (RunResult, error) {
		got = spec
		return RunResult{Claimed: "done"}, nil
	}
	f.rollout([]rl.Task{f.task}, 1, f.opts())
	if got.Clock == nil || got.OutagePatience != DefaultOutagePatience {
		t.Errorf("clock %v patience %v: want a clock and the default patience", got.Clock, got.OutagePatience)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.out, "manifest.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if m.Config.OutagePatienceMs != DefaultOutagePatience.Milliseconds() || m.Config.MaxOutagePauseMs != DefaultMaxOutagePause.Milliseconds() {
		t.Errorf("manifest config %+v", m.Config)
	}
}
