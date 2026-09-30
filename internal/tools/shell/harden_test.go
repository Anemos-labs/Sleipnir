//go:build linux

package shell

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestHardenProcessHelper runs inside a child copy of the test binary: making
// the process non-dumpable changes how it can read its own /proc files, which
// must not leak into the other tests.
func TestHardenProcessHelper(t *testing.T) {
	if os.Getenv("SLEIPNIR_HARDEN_HELPER") != "1" {
		t.Skip("helper process for TestHardenProcess")
	}
	before, err := dumpable()
	if err != nil {
		t.Fatal(err)
	}
	if err := HardenProcess(); err != nil {
		t.Fatal(err)
	}
	after, err := dumpable()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("dumpable before=%d after=%d\n", before, after)

	// Commands started afterwards still work: the flag is reset by execve.
	h := newHarness(t)
	res := h.bash(h.env("a"), "echo child-ok; cat /proc/self/status | grep -c ^Name")
	fmt.Printf("child: %q\n", res.Text)
}

func TestHardenProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardenProcessHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "SLEIPNIR_HARDEN_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "dumpable before=1 after=0") && !strings.Contains(string(out), "after=0") {
		t.Errorf("HardenProcess did not clear the dumpable flag:\n%s", out)
	}
	if !strings.Contains(string(out), `child: "child-ok\n1\n[exit code 0]"`) {
		t.Errorf("commands broke after hardening:\n%s", out)
	}
}

func TestHardenProcessIsIdempotent(t *testing.T) {
	// Idempotence is checked in the helper process too: two calls, same result.
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardenTwiceHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "SLEIPNIR_HARDEN_HELPER=2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
}

func TestHardenTwiceHelper(t *testing.T) {
	if os.Getenv("SLEIPNIR_HARDEN_HELPER") != "2" {
		t.Skip("helper process")
	}
	for i := 0; i < 3; i++ {
		if err := HardenProcess(); err != nil {
			t.Fatal(err)
		}
	}
	if d, err := dumpable(); err != nil || d != 0 {
		t.Fatalf("dumpable = %d, %v", d, err)
	}
}
