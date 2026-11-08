package shell

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// pkill without -f matches the kernel's name for a process, which is not argv[0] when a process is started under another
// name: the harness's own name is read from /proc/self/comm. The test runs itself again with a different argv[0].
func TestTheHarnessNameIsTheKernelsNotArgvZero(t *testing.T) {
	if os.Getenv("SLEIPNIR_SELFNAME_CHILD") == "1" {
		b, err := os.ReadFile("/proc/self/comm")
		if err != nil {
			t.Fatal(err)
		}
		comm := strings.TrimSpace(string(b))
		if what := endsHarness("pkill -0 "+comm, os.Getpid(), selfLine(), selfName()); what == "" {
			t.Fatalf("pkill -0 %s is not named (selfName %q, argv[0] %q)", comm, selfName(), os.Args[0])
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestTheHarnessNameIsTheKernelsNotArgvZero$")
	cmd.Args[0] = "another-name"
	cmd.Env = append(os.Environ(), "SLEIPNIR_SELFNAME_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
