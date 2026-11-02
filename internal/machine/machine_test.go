package machine

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDefaultsLeaveTheMachineHeadroom(t *testing.T) {
	cases := []struct {
		total, cpus, slots, jobs int
	}{
		{1, 1, 1, 1},
		{2, 1, 1, 1},
		{4, 2, 1, 2},
		{8, 6, 1, 6},
		{24, 18, 4, 4},
		{64, 48, 12, 4},
	}
	for _, c := range cases {
		b := newBudget(Settings{}, "", c.total, 32<<30, nil)
		if b.CPUs() != c.cpus || b.Slots() != c.slots || b.Jobs() != c.jobs {
			t.Errorf("%d cores: cpus %d slots %d jobs %d, want %d %d %d", c.total, b.CPUs(), b.Slots(), b.Jobs(), c.cpus, c.slots, c.jobs)
		}
	}
	if b := newBudget(Settings{CPUs: 100, HeavySlots: 3}, "", 8, 0, nil); b.CPUs() != 8 || b.Slots() != 3 || b.Jobs() != 2 {
		t.Errorf("explicit settings: cpus %d slots %d jobs %d", b.CPUs(), b.Slots(), b.Jobs())
	}
	if New(Settings{Unlimited: true}, "") != nil {
		t.Error("an unlimited budget is not nil")
	}
}

func TestMemoryReserve(t *testing.T) {
	for _, c := range []struct {
		memory, want int64
		mb           int
	}{
		{4 << 30, 1 << 30, 0}, // the floor
		{32 << 30, 32 << 30 * 15 / 100, 0},
		{256 << 30, 8 << 30, 0},   // the ceiling
		{1 << 30, 512 << 20, 0},   // never more than half
		{32 << 30, 3 << 30, 3072}, // the person's
		{0, 0, 2048},              // unknown memory is not watched
	} {
		if got := newBudget(Settings{MemoryReserveMB: c.mb}, "", 8, c.memory, nil).Reserve(); got != c.want {
			t.Errorf("memory %s reserve_mb %d: reserve %d, want %d", Bytes(c.memory), c.mb, got, c.want)
		}
	}
}

func TestEnvCapsWorkersWithoutOverridingThePersonsChoice(t *testing.T) {
	b := newBudget(Settings{CPUs: 8, HeavySlots: 2}, "", 16, 0, nil)
	env := b.Env([]string{"PATH=/bin", "CARGO_BUILD_JOBS=1", "GOFLAGS=-mod=mod"})
	get := func(name string) string {
		v := ""
		for _, kv := range env {
			if k, val, _ := strings.Cut(kv, "="); k == name {
				v = val
			}
		}
		return v
	}
	if get("SLEIPNIR_JOBS") != "4" || get("GOMAXPROCS") != "4" || get("OMP_NUM_THREADS") != "4" {
		t.Errorf("worker counts not set: %v", env)
	}
	if get("CARGO_BUILD_JOBS") != "1" {
		t.Errorf("CARGO_BUILD_JOBS the person set was replaced: %q", get("CARGO_BUILD_JOBS"))
	}
	if get("GOFLAGS") != "-mod=mod -p=4" {
		t.Errorf("GOFLAGS = %q", get("GOFLAGS"))
	}
	if get("MAKEFLAGS") != "" {
		t.Error("MAKEFLAGS set: a serial Makefile would run in parallel")
	}
	if got := b.Env([]string{"GOFLAGS=-p=2"}); !slices.Contains(got, "GOFLAGS=-p=2") {
		t.Errorf("GOFLAGS with -p changed: %v", got)
	}
	var nilBudget *Budget
	if got := nilBudget.Env([]string{"A=1"}); len(got) != 1 {
		t.Errorf("a nil budget changed the environment: %v", got)
	}
}

func TestSlotsAreSharedBetweenBudgetsOfOneDirectory(t *testing.T) {
	dir := t.TempDir()
	a := newBudget(Settings{CPUs: 2, HeavySlots: 1}, dir, 2, 0, nil)
	b := newBudget(Settings{CPUs: 2, HeavySlots: 1}, dir, 2, 0, nil)
	a.poll, b.poll = 5*time.Millisecond, 5*time.Millisecond
	first, err := a.Admit(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if light, err := b.Admit(context.Background(), false, nil); err != nil || light.Note != "" {
		t.Fatalf("a light command waited: %+v %v", light, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var told []string
	if _, err := b.Admit(ctx, true, func(s string) { told = append(told, s) }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second heavy command got the only slot: %v", err)
	}
	if len(told) != 1 || !strings.Contains(told[0], "waiting for a free slot") {
		t.Errorf("waiting told %q", told)
	}

	got := make(chan Admission, 1)
	go func() {
		ad, _ := b.Admit(context.Background(), true, nil)
		got <- ad
	}()
	time.Sleep(20 * time.Millisecond)
	first.Release()
	first.Release() // twice is harmless
	select {
	case ad := <-got:
		if !strings.HasPrefix(ad.Note, "[waited ") {
			t.Errorf("note %q", ad.Note)
		}
		ad.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("the released slot was not taken")
	}
}

func TestHeavyWaitsForMemoryButNotForever(t *testing.T) {
	free := int64(100 << 20)
	b := newBudget(Settings{MemoryReserveMB: 1024}, "", 4, 16<<30, func() (int64, bool) { return free, true })
	b.poll, b.memWait = 5*time.Millisecond, 40*time.Millisecond
	start := time.Now()
	ad, err := b.Admit(context.Background(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ad.Release()
	if time.Since(start) < 40*time.Millisecond || !strings.Contains(ad.Note, "waiting for memory") {
		t.Errorf("did not wait for memory: %v %q", time.Since(start), ad.Note)
	}
	if _, low := b.MemoryLow(); !low {
		t.Error("100 MiB free of a 1 GiB reserve is not low")
	}
	free = 600 << 20
	if _, low := b.MemoryLow(); low {
		t.Error("600 MiB free is above half the reserve")
	}
}

func TestStartLowersThePriorityOfTheCommandAndItsChildren(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("nice values are read from ps on unix")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	b := newBudget(Settings{}, "", 4, 0, nil)
	// the child of the shell reads its own nice value
	cmd := exec.Command("sh", "-c", "sh -c 'ps -o nice= -p $$'")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(cmd); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _ := out.Read(buf)
	_ = cmd.Wait()
	nice, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Skipf("ps gave %q", buf[:n])
	}
	if nice < lowNice {
		t.Errorf("the command's child runs at nice %d, want at least %d", nice, lowNice)
	}
	normal := newBudget(Settings{Priority: "normal"}, "", 4, 0, nil)
	if normal.low {
		t.Error("priority normal still lowers")
	}
}

func TestSummary(t *testing.T) {
	b := newBudget(Settings{}, "", 24, 32<<30, nil)
	want := "agent commands use 18 of 24 CPUs: 4 heavy at once, 4 workers each; 4.8 GiB of 32 GiB memory kept free; low priority"
	if got := b.Summary(); got != want {
		t.Errorf("summary\n got %q\nwant %q", got, want)
	}
}
