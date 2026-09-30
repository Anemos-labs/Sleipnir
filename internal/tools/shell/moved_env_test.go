//go:build unix

package shell

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/harden"
)

// harden.MoveKeys takes provider keys out of the process environment, so os.Environ no longer
// has them. An operator who lists one in Options.PassEnv still means to give it to commands:
// Manager.baseEnv reads it back from the vault. A key that is not listed stays withheld. The
// process-wide state this changes is why the scenario runs in a child copy of the test binary.

const movedHelper = "SLEIPNIR_MOVED_ENV_HELPER"

type movedReport struct {
	Passed    string `json:"passed"`     // commands of a Manager with PassEnv listing the key
	Withheld  string `json:"withheld"`   // commands of a Manager without it
	Getenv    string `json:"getenv"`     // os.Getenv of the key in the harness
	Secret    string `json:"secret"`     // harden.Secret of the same
	PassedAll string `json:"passed_all"` // wildcard PassEnv
}

func TestMovedKeysHarness(t *testing.T) {
	if os.Getenv(movedHelper) == "" {
		t.Skip("helper process for TestMovedKeysReachCommandsOnlyThroughPassEnv")
	}
	harden.Process(harden.MoveKeys())
	echo := `echo "[$MOVED_T_API_KEY][$MOVED_T_OTHER_API_KEY]"`
	var r movedReport
	{
		h := newHarness(t, Options{PassEnv: []string{"moved_t_api_key"}})
		r.Passed = outLine(h.bash(h.env("a"), echo).Text)
	}
	{
		h := newHarness(t)
		r.Withheld = outLine(h.bash(h.env("a"), echo).Text)
	}
	{
		h := newHarness(t, Options{PassEnv: []string{"MOVED_T_*"}})
		r.PassedAll = outLine(h.bash(h.env("a"), echo).Text)
	}
	r.Getenv, r.Secret = os.Getenv("MOVED_T_API_KEY"), harden.Secret("MOVED_T_API_KEY")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("\nMOVED-REPORT " + string(b) + "\n")
}

func TestMovedKeysReachCommandsOnlyThroughPassEnv(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMovedKeysHarness$", "-test.v")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), movedHelper + "=1",
		"MOVED_T_API_KEY=first-value", "MOVED_T_OTHER_API_KEY=second-value"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness failed: %v\n%s", err, out)
	}
	_, line, ok := strings.Cut(string(out), "\nMOVED-REPORT ")
	if !ok {
		t.Fatalf("no report (bash missing?):\n%s", out)
	}
	var r movedReport
	if err := json.Unmarshal([]byte(strings.SplitN(line, "\n", 2)[0]), &r); err != nil {
		t.Fatal(err)
	}
	if r.Getenv != "" || r.Secret != "first-value" {
		t.Errorf("os.Getenv = %q, Secret = %q: the key should be out of the environment and in the vault", r.Getenv, r.Secret)
	}
	if r.Passed != "[first-value][]" {
		t.Errorf("a command with PassEnv=[moved_t_api_key] saw %q, want only the listed key", r.Passed)
	}
	if r.Withheld != "[][]" {
		t.Errorf("a command without PassEnv saw %q: moved keys must stay withheld", r.Withheld)
	}
	if r.PassedAll != "[first-value][second-value]" {
		t.Errorf("a command with a wildcard PassEnv saw %q", r.PassedAll)
	}
}
