//go:build unix || windows

package session

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionDirectoryLockExclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	if second, err := lockDir(dir); err == nil {
		second()
		t.Fatal("a second writer acquired the same session directory")
	} else if !strings.Contains(err.Error(), "in use") {
		t.Fatalf("lock contention must explain that the session is in use: %v", err)
	}
	other, err := lockDir(t.TempDir())
	if err != nil {
		t.Fatalf("an unrelated session was blocked: %v", err)
	}
	other()
	unlock()
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err != nil {
		t.Fatalf("the lock file must remain in place across releases: %v", err)
	}
	next, err := lockDir(dir)
	if err != nil {
		t.Fatalf("the closed session remained locked: %v", err)
	}
	t.Cleanup(next)
	unlock() // Releasing the old lock again cannot affect the new owner.
	if third, err := lockDir(dir); err == nil {
		third()
		t.Fatal("releasing an old lock released the new owner's lock")
	}
}

func TestSessionDirectoryLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := sessionLockChild(ctx, dir, "blocked")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("another process did not observe the lock: %v\n%s", err, output)
	}
	unlock()
	child = sessionLockChild(ctx, dir, "acquire")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("another process could not acquire the released lock: %v\n%s", err, output)
	}
}

func TestSessionDirectoryLockReleasedAfterProcessDeath(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := sessionLockChild(ctx, dir, "hold")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	line, readErr := bufio.NewReader(output).ReadString('\n')
	if readErr != nil || strings.TrimSpace(line) != "locked" {
		_ = child.Process.Kill()
		_ = child.Wait()
		waited = true
		t.Fatalf("child did not acquire its lock: %q, %v\n%s", line, readErr, &stderr)
	}
	if competing, err := lockDir(dir); err == nil {
		competing()
		t.Fatal("the child process's lock did not exclude another writer")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	waited = true
	// Windows can release outstanding byte-range locks asynchronously after
	// termination. Retry contention within a hang guard, without deleting .lock.
	deadline := time.Now().Add(5 * time.Second)
	for {
		unlock, err := lockDir(dir)
		if err == nil {
			unlock()
			break
		}
		if !strings.Contains(err.Error(), "in use") || time.Now().After(deadline) {
			t.Fatalf("the dead process left the session locked: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionDirectoryLockOpenFailure(t *testing.T) {
	unlock, err := lockDir(filepath.Join(t.TempDir(), "missing", "session"))
	if err == nil || unlock != nil {
		t.Fatalf("opening a missing directory returned unlock=%v, err=%v", unlock != nil, err)
	}
}

func sessionLockChild(ctx context.Context, dir, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionDirectoryLockProcessHelper$")
	cmd.Env = append(os.Environ(), "SLEIPNIR_TEST_LOCK_DIR="+dir, "SLEIPNIR_TEST_LOCK_MODE="+mode)
	cmd.WaitDelay = time.Second
	return cmd
}

func TestSessionDirectoryLockProcessHelper(t *testing.T) {
	dir, mode := os.Getenv("SLEIPNIR_TEST_LOCK_DIR"), os.Getenv("SLEIPNIR_TEST_LOCK_MODE")
	if dir == "" || mode == "" {
		return
	}
	unlock, err := lockDir(dir)
	if mode == "blocked" {
		if err == nil {
			unlock()
			t.Fatal("acquired a lock already held by the parent")
		}
		if !strings.Contains(err.Error(), "in use") {
			t.Fatalf("unexpected lock error: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if mode == "hold" {
		fmt.Println("locked")
		var release [1]byte
		_, _ = os.Stdin.Read(release[:])
	}
}
