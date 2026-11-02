//go:build unix

package shell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/machine"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestHeavyCommandsTakeTurnsForTheMachinesSlots(t *testing.T) {
	b := machine.Fixed(machine.Settings{CPUs: 2, HeavySlots: 1}, t.TempDir(), 4, 0, nil)
	h := newHarness(t, Options{Machine: b})
	if err := os.WriteFile(filepath.Join(h.root, "build.sh"), []byte("#!/bin/sh\nsleep 0.6\necho built\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := make(chan *tools.Result, 1)
	go func() {
		res, _ := h.tryCall(context.Background(), h.env("a"), "bash", map[string]any{"command": "./build.sh"})
		first <- res
	}()
	time.Sleep(150 * time.Millisecond)

	// a light command does not wait for the build
	start := time.Now()
	if txt := mustText(t, h.bash(h.env("c"), "echo light")); strings.Contains(txt, "waited") || time.Since(start) > 400*time.Millisecond {
		t.Errorf("a light command waited for the slot: %q after %v", txt, time.Since(start))
	}

	var live outLog
	env := h.env("b")
	env.Out = live.fn
	second := mustText(t, h.call(context.Background(), env, "bash", map[string]any{"command": "./build.sh"}))
	if !strings.Contains(second, "built") || !strings.Contains(second, "[waited ") || !strings.Contains(second, "waiting for a free slot") {
		t.Errorf("the second build did not wait its turn:\n%s", second)
	}
	if _, stderr, _ := live.get(); !strings.Contains(stderr, "[sleipnir: waiting for a free slot") {
		t.Errorf("the wait was not shown live: %q", stderr)
	}
	if txt := mustText(t, <-first); strings.Contains(txt, "waited") {
		t.Errorf("the first build waited:\n%s", txt)
	}
}

func TestAWaitingCommandDoesNotHoldShutdown(t *testing.T) {
	b := machine.Fixed(machine.Settings{CPUs: 1, HeavySlots: 1}, t.TempDir(), 1, 0, nil)
	h := newHarness(t, Options{Machine: b})
	hold, err := b.Admit(context.Background(), true, nil) // another process's build
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	done := make(chan string, 1)
	go func() {
		res, _ := h.tryCall(context.Background(), h.env("a"), "bash", map[string]any{"command": "go build ./..."})
		done <- res.Text
	}()
	time.Sleep(100 * time.Millisecond)
	shut := make(chan struct{})
	go func() { h.m.Shutdown(); close(shut) }()
	select {
	case <-shut:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown waited for a command that was waiting for a slot")
	}
	if txt := <-done; !strings.Contains(txt, "shutting down") {
		t.Errorf("result %q", txt)
	}
}

func TestCommandsAreToldHowManyWorkersTheyMayStart(t *testing.T) {
	b := machine.Fixed(machine.Settings{CPUs: 6, HeavySlots: 3}, "", 8, 0, nil)
	h := newHarness(t, Options{Machine: b})
	txt := mustText(t, h.bash(h.env("a"), "echo jobs=$SLEIPNIR_JOBS go=$GOMAXPROCS cargo=$CARGO_BUILD_JOBS flags=$GOFLAGS"))
	if !strings.Contains(txt, "jobs=2 go=2 cargo=2 flags=-p=2") {
		t.Errorf("worker counts: %q", txt)
	}
	plain := newHarness(t)
	if txt := mustText(t, plain.bash(plain.env("a"), "echo jobs=$SLEIPNIR_JOBS")); !strings.Contains(txt, "jobs=\n") && !strings.HasPrefix(txt, "jobs=\n") {
		t.Errorf("a Manager without a budget set worker counts: %q", txt)
	}
}

func TestTheMemoryGuardStopsTheCommandUsingTheMost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("memory is read from /proc")
	}
	b := machine.Fixed(machine.Settings{MemoryReserveMB: 1024}, "", 4, 16<<30, func() (int64, bool) { return 100 << 20, true })
	h := newHarness(t, Options{Machine: b, MemoryCheck: 50 * time.Millisecond})
	small := make(chan string, 1)
	go func() {
		res, _ := h.tryCall(context.Background(), h.env("b"), "bash", map[string]any{"command": "sleep 2; echo small done"})
		small <- res.Text
	}()
	start := time.Now()
	txt := mustText(t, h.bash(h.env("a"), `x=$(head -c 150000000 /dev/zero | tr '\0' a); sleep 30; echo ${#x}`, map[string]any{"timeout": 60}))
	if !strings.Contains(txt, "[stopped by Sleipnir: the machine was running out of memory") {
		t.Fatalf("the hungry command was not stopped:\n%s", txt)
	}
	if time.Since(start) > 25*time.Second {
		t.Errorf("stopped only after %v", time.Since(start))
	}
	if got := <-small; !strings.Contains(got, "small done") {
		t.Errorf("the small command was stopped too:\n%s", got)
	}
}
