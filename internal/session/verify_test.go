package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func verificationOutputCommand(t *testing.T) (string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "'"
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		command = `.\` + filepath.Base(exe)
		dir = filepath.Dir(exe)
	}
	command += " -test.run=^TestVerificationOutputProcessHelper$"
	return dir, command
}

func TestVerificationCombinesConcurrentOutputWithinItsCap(t *testing.T) {
	dir, command := verificationOutputCommand(t)
	for _, lines := range []int{2048, 16384} {
		t.Run(strconv.Itoa(lines), func(t *testing.T) {
			t.Setenv("TEST_VERIFY_OUTPUT_LINES", strconv.Itoa(lines))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			out, code, err := runVerify(ctx, dir, command)
			if err != nil || code != 7 {
				t.Fatalf("verification exit: code=%d err=%v output=%q", code, err, out)
			}
			total := 2 * lines * len("stdout fixture\n")
			if total <= 256<<10 {
				if len(out) != total {
					t.Fatalf("combined output size=%d, want %d", len(out), total)
				}
				if a, b := strings.Count(out, "stdout fixture\n"), strings.Count(out, "stderr fixture\n"); a != lines || b != lines {
					t.Fatalf("lost output: stdout=%d stderr=%d, want %d of each", a, b, lines)
				}
			} else {
				marker := fmt.Sprintf("\n[... %d bytes omitted ...]\n", total-(256<<10))
				if !strings.Contains(out, marker) || len(out) != (256<<10)+len(marker) {
					t.Fatalf("combined output did not retain 256 KiB plus the omission marker (%d bytes)", len(out))
				}
			}
		})
	}
}

func TestVerificationRetainsFinalFailureDiagnostic(t *testing.T) {
	dir, command := verificationOutputCommand(t)
	t.Setenv("TEST_VERIFY_OUTPUT_LINES", "16384")
	t.Setenv("TEST_VERIFY_OUTPUT_DIAGNOSTIC", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, code, err := runVerify(ctx, dir, command)
	if err != nil || code != 7 {
		t.Fatalf("verification exit: code=%d err=%v", code, err)
	}
	if !strings.HasPrefix(out, "verification started\n") || !strings.HasSuffix(out, "--- FAIL: TestRequirement: expected 10, got 11\n") {
		t.Fatalf("verification lost its command context or final failure diagnostic (%d captured bytes)", len(out))
	}
	if !strings.Contains(out, "bytes omitted") || len(out) > (256<<10)+64 {
		t.Fatalf("long verifier output is not bounded with an omission marker (%d bytes)", len(out))
	}
}

func TestVerificationOutputProcessHelper(t *testing.T) {
	value := os.Getenv("TEST_VERIFY_OUTPUT_LINES")
	if value == "" {
		return
	}
	lines, err := strconv.Atoi(value)
	if err != nil || lines < 1 || lines > 16384 {
		t.Fatal("invalid fixture line count")
	}
	if os.Getenv("TEST_VERIFY_OUTPUT_DIAGNOSTIC") == "1" {
		fmt.Fprintln(os.Stdout, "verification started")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < lines; i++ {
			fmt.Fprintln(os.Stdout, "stdout fixture")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < lines; i++ {
			fmt.Fprintln(os.Stderr, "stderr fixture")
		}
	}()
	wg.Wait()
	if os.Getenv("TEST_VERIFY_OUTPUT_DIAGNOSTIC") == "1" {
		fmt.Fprintln(os.Stderr, "--- FAIL: TestRequirement: expected 10, got 11")
	}
	os.Exit(7)
}
