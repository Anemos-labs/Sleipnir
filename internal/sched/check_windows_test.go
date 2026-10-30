package sched

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestACheckThatTimesOutEndsItsProcessTree: when the deadline ends a check, the processes the shell started go with it (taskkill /T),
// not only cmd.exe.
func TestACheckThatTimesOutEndsItsProcessTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	// powershell, a child of cmd, starts ping as its own child and records its id, then waits; nothing ends on its own in time
	script := `powershell -NoProfile -NonInteractive -Command "$p = Start-Process -PassThru -WindowStyle Hidden ping -ArgumentList '-n','300','127.0.0.1'; Set-Content -Path pid -Value $p.Id; Start-Sleep 300"`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := RunCheck(ctx, Job{Dir: dir, Check: script}); err == nil {
		t.Fatal("a check that outlived its deadline succeeded")
	}
	if d := time.Since(start); d > 45*time.Second {
		t.Fatalf("RunCheck returned after %s", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Skipf("the check did not record its child in time: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(b), "\ufeff")))
	if err != nil {
		t.Fatalf("pid file %q: %v", b, err)
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return // already gone
	}
	done := make(chan struct{})
	go func() { _, _ = p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = p.Kill()
		t.Fatalf("the check's grandchild %d outlived the timeout", pid)
	}
}
