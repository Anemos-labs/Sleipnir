//go:build unix

package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// realTemp is a symlink-free temporary directory.
func realTemp(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// testEnv is the environment hooks inherit in tests: a PATH that finds sh's
// tools, and nothing else the developer's shell happens to export.
func testEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent-home", "LANG=C"}
}

// hookSpec is one hook object of a test configuration.
type hookSpec map[string]any

func cmdHook(command string) hookSpec { return hookSpec{"type": "command", "command": command} }

func (h hookSpec) timeout(sec float64) hookSpec { h["timeout"] = sec; return h }

func (h hookSpec) failClosed(b bool) hookSpec { h["failClosed"] = b; return h }

func (h hookSpec) when(cond string) hookSpec { h["if"] = cond; return h }

// group is one matcher group of a test configuration.
type group struct {
	matcher string
	hooks   []hookSpec
}

// settings encodes {event: [groups]} as the "hooks" object of a settings file.
func settings(t testing.TB, event string, groups ...group) map[string]json.RawMessage {
	t.Helper()
	var gs []map[string]any
	for _, g := range groups {
		m := map[string]any{"hooks": g.hooks}
		if g.matcher != "" {
			m["matcher"] = g.matcher
		}
		gs = append(gs, m)
	}
	raw, err := json.Marshal(gs)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]json.RawMessage{event: raw}
}

// newRunner returns a trusted Runner over the given settings, with a scratch
// project directory and a short kill grace so timeout tests are quick.
func newRunner(t testing.TB, m map[string]json.RawMessage) *Runner {
	t.Helper()
	s, err := ParseAs(OriginUser, "test", m)
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Set: s, Dir: realTemp(t), Trusted: true, Env: testEnv(), KillGrace: 200 * time.Millisecond, SessionID: "sess-1"}
}

// one is newRunner for a single command hook on one event.
func one(t testing.TB, event, matcher, command string) *Runner {
	t.Helper()
	return newRunner(t, settings(t, event, group{matcher: matcher, hooks: []hookSpec{cmdHook(command)}}))
}

// run runs an event and fails the test on a harness error.
func run(t testing.TB, r *Runner, ev Event) Result {
	t.Helper()
	res, err := r.Run(context.Background(), ev)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// heredoc is a shell command that prints text exactly, whatever it contains.
func heredoc(text string) string { return "cat <<'HEREDOC_EOF'\n" + text + "\nHEREDOC_EOF" }

// alive reports whether a process exists (and is not a zombie we could not reap).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// A zombie still answers kill(pid, 0); /proc tells them apart where it exists.
	if b, err := os.ReadFile("/proc/" + itoa(pid) + "/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) && s[i+2] == 'Z' {
			return false
		}
	}
	return true
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// readPID reads a process id a hook wrote to a file.
func readPID(t testing.TB, path string) int {
	t.Helper()
	var pid int
	waitFor(t, 5*time.Second, "pid file "+path, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return false
		}
		n, err := json.Number(s).Int64()
		if err != nil {
			return false
		}
		pid = int(n)
		return true
	})
	return pid
}

// killLater makes sure a process a test deliberately leaves running is gone.
func killLater(t testing.TB, pid int) {
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
}
