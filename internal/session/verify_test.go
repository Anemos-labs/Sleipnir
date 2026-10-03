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

func TestVerificationCombinesConcurrentOutputWithinItsCap(t *testing.T) {
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
	for _, lines := range []int{2048, 16384} {
		t.Run(strconv.Itoa(lines), func(t *testing.T) {
			t.Setenv("SLEIPNIR_VERIFY_OUTPUT_LINES", strconv.Itoa(lines))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			out, code, err := runVerify(ctx, dir, command)
			if err != nil || code != 7 {
				t.Fatalf("verification exit: code=%d err=%v output=%q", code, err, out)
			}
			want := min(2*lines*len("stdout fixture\n"), 256<<10)
			if len(out) != want {
				t.Fatalf("combined output size=%d, want %d", len(out), want)
			}
			if want < 256<<10 {
				if a, b := strings.Count(out, "stdout fixture\n"), strings.Count(out, "stderr fixture\n"); a != lines || b != lines {
					t.Fatalf("lost output: stdout=%d stderr=%d, want %d of each", a, b, lines)
				}
			}
		})
	}
}

func TestVerificationOutputProcessHelper(t *testing.T) {
	value := os.Getenv("SLEIPNIR_VERIFY_OUTPUT_LINES")
	if value == "" {
		return
	}
	lines, err := strconv.Atoi(value)
	if err != nil || lines < 1 || lines > 16384 {
		t.Fatal("invalid fixture line count")
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
	os.Exit(7)
}
