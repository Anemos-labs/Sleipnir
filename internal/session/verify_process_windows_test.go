package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/workspace"
	"golang.org/x/sys/windows"
)

func TestWindowsVerificationCancellationStopsDescendants(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("TEST_VERIFY_PROCESS_DIRECTORY", dir)
	t.Setenv("TEST_VERIFY_PROCESS_MODE", "launcher")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(done)
		_, _, err := runVerify(ctx, dir, `"`+exe+`" -test.run=^TestWindowsVerifierProcessHelper$`)
		result <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("verification caller did not stop")
		}
	})
	handles := make(map[string]windows.Handle)
	for _, name := range []string{"launcher", "leaf"} {
		handle := waitForVerifierFixture(t, filepath.Join(dir, name+".pid"))
		handles[name] = handle
		t.Cleanup(func() {
			_ = windows.TerminateProcess(handle, 1)
			_, _ = windows.WaitForSingleObject(handle, 2000)
			_ = windows.CloseHandle(handle)
		})
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled verification returned %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("verification did not return after cancellation")
	}
	for name, handle := range handles {
		if state, err := windows.WaitForSingleObject(handle, 1000); err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("verification %s survived cancellation: state=%d err=%v", name, state, err)
		}
	}
}

func TestWindowsVerificationTimeoutAndExitStopDescendants(t *testing.T) {
	for _, earlyExit := range []bool{false, true} {
		name := "timeout"
		if earlyExit {
			name = "successful exit"
		}
		t.Run(name, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			t.Setenv("TEST_VERIFY_PROCESS_DIRECTORY", dir)
			t.Setenv("TEST_VERIFY_PROCESS_MODE", "launcher")
			if earlyExit {
				t.Setenv("TEST_VERIFY_PROCESS_EARLY_EXIT", "1")
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			result := make(chan workspace.VerifyResult, 1)
			go func() {
				defer close(done)
				result <- workspace.RunShell(ctx, workspace.VerifyRequest{Dir: dir, Cmd: `"` + exe + `" -test.run=^TestWindowsVerifierProcessHelper$`, Timeout: 10 * time.Second})
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Error("verification caller did not stop")
				}
			})
			handle := waitForVerifierFixture(t, filepath.Join(dir, "leaf.pid"))
			t.Cleanup(func() {
				_ = windows.TerminateProcess(handle, 1)
				_, _ = windows.WaitForSingleObject(handle, 2000)
				_ = windows.CloseHandle(handle)
			})
			select {
			case res := <-result:
				if earlyExit && !res.OK() {
					t.Fatalf("successful verifier: %+v", res)
				}
				if !earlyExit && (!res.TimedOut || res.Err != nil || res.ExitCode != -1) {
					t.Fatalf("timed-out verifier: %+v", res)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("verification did not return")
			}
			if state, err := windows.WaitForSingleObject(handle, 1000); err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("verification child survived %s: state=%d err=%v", name, state, err)
			}
		})
	}
}

func waitForVerifierFixture(t *testing.T, path string) windows.Handle {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
				if err != nil {
					t.Fatal(err)
				}
				return handle
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture did not report its process: %s", path)
	return 0
}

func TestWindowsVerifierProcessHelper(t *testing.T) {
	mode := os.Getenv("TEST_VERIFY_PROCESS_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("TEST_VERIFY_PROCESS_DIRECTORY")
	if err := os.WriteFile(filepath.Join(dir, mode+".pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if mode == "launcher" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(exe, "-test.run=^TestWindowsVerifierProcessHelper$")
		child.Env = append(os.Environ(), "TEST_VERIFY_PROCESS_MODE=leaf")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("TEST_VERIFY_PROCESS_EARLY_EXIT") == "1" {
			_ = child.Process.Release()
			os.Exit(0)
		}
		if err := child.Wait(); err != nil {
			os.Exit(1)
		}
	} else if mode == "leaf" {
		time.Sleep(30 * time.Second) // expiry is a backstop if the parent test crashes
	} else {
		t.Fatal("invalid fixture mode")
	}
	os.Exit(0)
}
