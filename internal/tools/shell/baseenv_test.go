//go:build unix

package shell

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestBaseEnvReplacesTheProcessEnvironment(t *testing.T) {
	t.Setenv("SLEIPNIR_T_HOSTVAR", "from-the-host")
	h := newHarness(t, Options{BaseEnv: []string{"PATH=" + os.Getenv("PATH"), "HOME=/private/home", "FOO=bar"}})
	res := h.bash(h.env("a"), `echo "$HOME|$FOO|${SLEIPNIR_T_HOSTVAR:-unset}"`)
	if got := outLine(res.Text); got != "/private/home|bar|unset" {
		t.Fatalf("environment = %q, want the private one only", got)
	}
}

func TestBaseEnvIsStillScrubbed(t *testing.T) {
	h := newHarness(t, Options{BaseEnv: []string{"PATH=" + os.Getenv("PATH"), "SLEIPNIR_T_API_KEY=hunter2", "KEEP=1"}})
	res := h.bash(h.env("a"), `echo "[${SLEIPNIR_T_API_KEY:-}][$KEEP]"`)
	if got := outLine(res.Text); got != "[][1]" {
		t.Fatalf("got %q: a secret-looking variable in BaseEnv must still be withheld", got)
	}
}

func TestBaseEnvEmptyMeansEmpty(t *testing.T) {
	t.Setenv("SLEIPNIR_T_HOSTVAR", "from-the-host")
	h := newHarness(t, Options{BaseEnv: []string{}})
	res := h.bash(h.env("a"), `echo "${SLEIPNIR_T_HOSTVAR:-unset}"`)
	if got := outLine(res.Text); got != "unset" {
		t.Fatalf("an empty, non-nil BaseEnv must not fall back to the process environment: %q", got)
	}
}

func TestWrapRunsForegroundAndBackgroundCommandsUnderThePrefix(t *testing.T) {
	h := newHarness(t, Options{Wrap: []string{"env", "SLEIPNIR_WRAPPED=yes"}})
	env := h.env("a")
	if got := outLine(h.bash(env, `echo "$SLEIPNIR_WRAPPED"`).Text); got != "yes" {
		t.Fatalf("foreground command not wrapped: %q", got)
	}
	id := h.startJob(env, `echo "job-$SLEIPNIR_WRAPPED"`)
	waitFor(t, "job output", 10*time.Second, func() bool {
		return strings.Contains(h.output(env, id, map[string]any{"since": 0}).Text, "job-yes")
	})
}

func TestWrapDoesNotBreakTheExitCode(t *testing.T) {
	h := newHarness(t, Options{Wrap: []string{"env"}})
	res := h.bash(h.env("a"), `exit 3`)
	if !strings.Contains(res.Text, "3") {
		t.Fatalf("exit status lost under a wrapper: %q", res.Text)
	}
}

// firstLine is the command's own output: results end with an "[exit code N]" line.
func outLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
