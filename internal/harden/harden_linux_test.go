//go:build linux

package harden

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// Process changes the state of the whole process (it makes it non-dumpable and erases
// values from its environment block), so these tests run it in a child copy of the
// test binary, the helper below, and look at the outcome from the outside, the way a
// command run by a model would: as a child process of the harness that reads
// /proc/<parent>/environ.
//
// When the tests run as root the helper is switched to an unprivileged uid first:
// root (with CAP_SYS_PTRACE) reads every process's /proc files whatever the dumpable
// flag says, so only an unprivileged helper shows what the flag does.

const helperEnv = "SLEIPNIR_HARDEN_HELPER"

// The canary is assembled at run time: nothing in the source is a credential shape.
var canary = "sk" + "-harden-canary-" + strings.Repeat("c", 12)

type childRead struct {
	Out  string `json:"out"`
	Err  string `json:"err"`
	Exit int    `json:"exit"`
}

type report struct {
	Dumpable     int               `json:"dumpable"`
	Status       Status            `json:"status"`
	Child        childRead         `json:"child"`      // sh -c 'tr ... < /proc/$PPID/environ'
	ChildOwn     childRead         `json:"child_own"`  // sh -c 'cat /proc/self/environ > /dev/null && echo readable'
	Getenv       string            `json:"getenv"`     // os.Getenv of the canary variable
	Secret       string            `json:"secret"`     // harden.Secret of the same
	ChildEnv     string            `json:"child_env"`  // what a child started with the inherited environment sees
	EnvBefore    []byte            `json:"env_before"` // /proc/self/environ before Process (when readable)
	EnvAfter     []byte            `json:"env_after"`  // and after
	Held         []string          `json:"held"`
	SecondStatus Status            `json:"second_status"`
	Self         map[string]string `json:"self"` // what the process can still do with its own /proc files ("ok" or the error)
}

// selfAccess tries the /proc/self files a Go program or its dependencies may use.
func selfAccess() map[string]string {
	out := map[string]string{}
	try := func(name string, err error) {
		if err != nil {
			out[name] = err.Error()
		} else {
			out[name] = "ok"
		}
	}
	for _, name := range []string{"stat", "status", "maps", "cgroup", "limits", "environ", "mem"} {
		f, err := os.Open("/proc/self/" + name)
		if err == nil {
			buf := make([]byte, 16)
			_, err = f.Read(buf)
			if err == io.EOF {
				err = nil
			}
			f.Close()
		}
		try(name, err)
	}
	_, err := os.Readlink("/proc/self/exe")
	try("exe", err)
	_, err = os.Readlink("/proc/self/ns/pid")
	try("ns/pid", err)
	_, err = os.ReadDir("/proc/self/fd")
	try("fd", err)
	_, err = os.Executable()
	try("os.Executable", err)
	return out
}

func sh(script string) childRead {
	cmd := exec.Command("sh", "-c", script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := childRead{Out: out.String(), Err: errb.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.Exit = ee.ExitCode()
	} else if err != nil {
		r.Exit, r.Err = -1, r.Err+err.Error()
	}
	return r
}

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("helper process for the tests in this file")
	}
	var r report
	r.EnvBefore, _ = os.ReadFile("/proc/self/environ")
	switch mode {
	case "control":
		// no Process: what a harness that forgot to call it looks like
	case "hardened":
		r.Status = Process()
		r.SecondStatus = Process() // idempotent
		Process()
	case "moving":
		r.Status = Process(MoveKeys())
	}
	r.EnvAfter, _ = os.ReadFile("/proc/self/environ")
	r.Dumpable, _ = unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	r.Getenv = os.Getenv("HARDEN_T_API_KEY")
	r.Secret = Secret("HARDEN_T_API_KEY")
	r.Held = Held()
	r.Self = selfAccess()
	r.Child = sh(`tr '\0' '\n' < /proc/$PPID/environ`)
	r.ChildOwn = sh(`cat /proc/self/environ > /dev/null && echo readable`)
	r.ChildEnv = sh(`env`).Out
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("\nREPORT " + string(b) + "\n")
}

// TestHelperEmptyEnvironment is the helper of TestProcessWithNothingToEraseIsQuiet: it only runs in a
// process whose environment is empty (which no normal test run has).
func TestHelperEmptyEnvironment(t *testing.T) {
	if len(os.Environ()) != 0 {
		t.Skip("helper process: runs only with an empty environment")
	}
	var r report
	r.Status = Process()
	r.Dumpable, _ = unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("\nREPORT " + string(b) + "\n")
}

// A process with no environment, or none worth erasing, is hardened without complaint: an empty
// range and "nothing to erase" are not problems to report.
func TestProcessWithNothingToEraseIsQuiet(t *testing.T) {
	needProc(t)
	path, attr := unprivileged(t)
	run := func(name string, env []string) report {
		cmd := exec.Command(path, "-test.run=^"+name+"$", "-test.v")
		cmd.Env = env
		cmd.SysProcAttr = attr
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", name, err, out)
		}
		_, line, ok := strings.Cut(string(out), "\nREPORT ")
		if !ok {
			t.Fatalf("%s printed no report:\n%s", name, out)
		}
		var r report
		if err := json.Unmarshal([]byte(strings.SplitN(line, "\n", 2)[0]), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	empty := run("TestHelperEmptyEnvironment", []string{})
	if !empty.Status.Protected || empty.Dumpable != 0 || empty.Status.EnvErased != 0 || len(empty.Status.Notes) != 0 {
		t.Errorf("empty environment: status %+v, dumpable %d", empty.Status, empty.Dumpable)
	}
	plain := run("TestHelperProcess", []string{helperEnv + "=hardened", "PATH=" + os.Getenv("PATH"), "HARDEN_T_PLAIN=visible-value"})
	if !plain.Status.Protected || plain.Status.EnvErased != 0 || len(plain.Status.Notes) != 0 {
		t.Errorf("an environment with no credentials: status %+v", plain.Status)
	}
}

// The copy of the test binary that an unprivileged user can run is made once per test process and
// removed by TestMain.
var (
	copyOnce sync.Once
	copyDir  string
	copyPath string
	copyErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if copyDir != "" {
		os.RemoveAll(copyDir)
	}
	os.Exit(code)
}

func makeUnprivilegedCopy() (dir, path string, err error) {
	dir, err = os.MkdirTemp("", "harden-helper-")
	if err != nil {
		return "", "", err
	}
	if err = os.Chmod(dir, 0o755); err != nil {
		return dir, "", err
	}
	src, err := os.Open(os.Args[0])
	if err != nil {
		return dir, "", err
	}
	defer src.Close()
	path = filepath.Join(dir, "harden.test")
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		return dir, "", err
	}
	if _, err = io.Copy(dst, src); err != nil {
		dst.Close()
		return dir, "", err
	}
	return dir, path, dst.Close()
}

// unprivileged returns what runs the helper as a user without capabilities: the
// test binary itself when the tests already are one, otherwise a copy of it in a
// directory anyone can read, run as uid 65534.
func unprivileged(t *testing.T) (path string, attr *syscall.SysProcAttr) {
	t.Helper()
	if os.Geteuid() != 0 {
		return os.Args[0], nil
	}
	copyOnce.Do(func() { copyDir, copyPath, copyErr = makeUnprivilegedCopy() })
	if copyErr != nil {
		t.Skipf("cannot make a copy of the test binary that an unprivileged user can run: %v", copyErr)
	}
	return copyPath, &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
}

func runHelper(t *testing.T, path string, attr *syscall.SysProcAttr, mode string, env ...string) report {
	t.Helper()
	cmd := exec.Command(path, "-test.run=^TestHelperProcess$", "-test.v")
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), helperEnv + "=" + mode, "HARDEN_T_API_KEY=" + canary, "HARDEN_T_PLAIN=visible-value"}, env...)
	cmd.SysProcAttr = attr
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper (%s) failed: %v\n%s", mode, err, out)
	}
	_, line, ok := strings.Cut(string(out), "\nREPORT ")
	if !ok {
		t.Fatalf("helper (%s) printed no report:\n%s", mode, out)
	}
	var r report
	if err := json.Unmarshal([]byte(strings.SplitN(line, "\n", 2)[0]), &r); err != nil {
		t.Fatalf("bad report: %v\n%s", err, line)
	}
	return r
}

func needProc(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
}

// The finding (S40): a command run by the model reads the harness's key from
// /proc/$PPID/environ. Without Process it does (control: proves the test can see the
// leak); after Process it cannot, and the harness itself, and the children it starts
// afterwards, work as before.
func TestProcessKeepsASameUserChildFromReadingTheParentsEnviron(t *testing.T) {
	needProc(t)
	path, attr := unprivileged(t)

	control := runHelper(t, path, attr, "control")
	if !strings.Contains(control.Child.Out, canary) {
		t.Skipf("the control run could not read its parent's environment (exit %d: %q %q); this sandbox hides /proc/<pid>/environ from same-user children, so the effect of Process cannot be shown here",
			control.Child.Exit, control.Child.Out, control.Child.Err)
	}
	if control.Dumpable != 1 {
		t.Fatalf("control helper is not dumpable to begin with: %d", control.Dumpable)
	}

	h := runHelper(t, path, attr, "hardened")
	if strings.Contains(h.Child.Out, canary) {
		t.Fatalf("a child of the hardened harness read the key from /proc/$PPID/environ:\n%s", h.Child.Out)
	}
	if strings.Contains(h.Child.Err, canary) {
		t.Fatalf("the key leaked into an error message: %q", h.Child.Err)
	}
	// The helper is unprivileged, so the dumpable flag itself must have stopped the read
	// (without the erasure the file would be refused, not merely blank).
	if h.Child.Exit == 0 || !strings.Contains(strings.ToLower(h.Child.Err), "permission denied") {
		t.Fatalf("the child could still open the hardened parent's /proc/$PPID/environ (exit %d, stderr %q); the flag should refuse it", h.Child.Exit, h.Child.Err)
	}
	if h.Dumpable != 0 {
		t.Fatalf("PR_GET_DUMPABLE = %d after Process, want 0", h.Dumpable)
	}
	st := h.Status
	if st.Platform != "linux" || !st.Protected || st.OptedOut {
		t.Errorf("status = %+v, want protected on linux", st)
	}
	if len(st.Notes) != 0 {
		t.Errorf("hardening reported problems: %v", st.Notes)
	}
	if st.EnvErased < 1 {
		t.Errorf("EnvErased = %d, want at least the canary variable", st.EnvErased)
	}
	// Idempotent: a second and third call change nothing.
	if h.SecondStatus.Protected != st.Protected || h.SecondStatus.EnvErased != st.EnvErased || h.Dumpable != 0 {
		t.Errorf("second Process call: %+v vs %+v", h.SecondStatus, st)
	}
	// The harness's own environment is intact: erasing touches the kernel's copy only.
	if h.Getenv != canary || h.Secret != canary {
		t.Errorf("os.Getenv = %q, Secret = %q: the harness lost its own key", h.Getenv, h.Secret)
	}
	if !strings.Contains(h.ChildEnv, "HARDEN_T_PLAIN=visible-value") {
		t.Errorf("a child started after Process does not see the environment: %q", h.ChildEnv)
	}
	// Children are not affected by the flag (execve resets it): they read their own /proc files.
	if h.ChildOwn.Exit != 0 || !strings.Contains(h.ChildOwn.Out, "readable") {
		t.Errorf("a child of the hardened process is itself not dumpable: %+v", h.ChildOwn)
	}
	// What a non-dumpable process can still do with its own /proc files (docs/SECURITY.md lists it).
	t.Logf("own /proc files after Process (unprivileged): %v", h.Self)
	for _, name := range []string{"stat", "status", "maps", "cgroup", "limits", "exe", "ns/pid", "fd", "os.Executable"} {
		if h.Self[name] != "ok" {
			t.Errorf("a hardened process can no longer use /proc/self/%s: %s", name, h.Self[name])
		}
	}
	for _, name := range []string{"environ", "mem"} {
		if h.Self[name] == "ok" {
			t.Errorf("a hardened unprivileged process can still open /proc/self/%s: the flag did not take effect", name)
		}
	}
}

// SLEIPNIR_DUMPABLE=1 leaves the process dumpable for debugging, but the values are
// still gone from the environment block: the file is readable and shows NAME= only.
func TestOptOutKeepsTheProcessDumpableButStillErasesTheValues(t *testing.T) {
	needProc(t)
	path, attr := unprivileged(t)
	if c := runHelper(t, path, attr, "control"); !strings.Contains(c.Child.Out, canary) {
		t.Skip("the control run could not read its parent's environment in this sandbox")
	}
	h := runHelper(t, path, attr, "hardened", "SLEIPNIR_DUMPABLE=1")
	if h.Dumpable != 1 || h.Status.Protected || !h.Status.OptedOut {
		t.Fatalf("with SLEIPNIR_DUMPABLE=1: dumpable=%d status=%+v", h.Dumpable, h.Status)
	}
	if h.Child.Exit != 0 {
		t.Fatalf("the opt-out must leave /proc/$PPID/environ readable: exit %d, %q", h.Child.Exit, h.Child.Err)
	}
	if strings.Contains(h.Child.Out, canary) {
		t.Fatalf("the key is readable although the environment was erased:\n%s", h.Child.Out)
	}
	if !strings.Contains(h.Child.Out, "HARDEN_T_API_KEY=\n") {
		t.Errorf("the erased variable should still show its name, empty:\n%s", h.Child.Out)
	}
	if !strings.Contains(h.Child.Out, "HARDEN_T_PLAIN=visible-value\n") {
		t.Errorf("a plain variable was touched:\n%s", h.Child.Out)
	}
	if h.Getenv != canary {
		t.Errorf("os.Getenv = %q", h.Getenv)
	}
}

// The erasure changes the values of credential-looking entries and nothing else: same
// size, same layout, every other byte identical.
func TestEraseChangesNothingButTheSecretValues(t *testing.T) {
	needProc(t)
	path, attr := unprivileged(t)
	extra := []string{
		"HARDEN_T_DB=" + "postgres://app:" + "pw" + "@db/prod", // a value with a password, whatever the name
		"HARDEN_T_EMPTY_TOKEN=",
		"HARDEN_T_LONG_SECRET=" + strings.Repeat("s", 5000),
		"HARDEN_T_UNICODE=héllo wörld ✓",
	}
	h := runHelper(t, path, attr, "hardened", append([]string{"SLEIPNIR_DUMPABLE=1"}, extra...)...)
	if len(h.EnvBefore) == 0 || len(h.EnvBefore) != len(h.EnvAfter) {
		t.Fatalf("environment block size %d -> %d", len(h.EnvBefore), len(h.EnvAfter))
	}
	want := append([]byte(nil), h.EnvBefore...)
	for _, s := range secretSpans(h.EnvBefore, shouldErase) {
		clear(want[s.off : s.off+s.n])
	}
	if !bytes.Equal(want, h.EnvAfter) {
		t.Fatalf("the block changed somewhere other than the secret values")
	}
	after := string(h.EnvAfter)
	for _, gone := range []string{canary, "pw@db", strings.Repeat("s", 40)} {
		if strings.Contains(after, gone) {
			t.Errorf("%q survived in the environment block", gone[:min(len(gone), 12)])
		}
	}
	for _, kept := range []string{"HARDEN_T_PLAIN=visible-value\x00", "HARDEN_T_UNICODE=héllo wörld ✓\x00", "HARDEN_T_EMPTY_TOKEN=\x00", "PATH="} {
		if !strings.Contains(after, kept) {
			t.Errorf("%q is missing from the block after erasing", kept)
		}
	}
	if h.Status.EnvErased < 3 {
		t.Errorf("EnvErased = %d, want at least 3 (key, database url, long secret)", h.Status.EnvErased)
	}
}

// MoveKeys: the key leaves the environment (no os.Getenv, no inherited child sees it) and
// stays readable through Secret.
func TestMoveKeysInARealProcess(t *testing.T) {
	needProc(t)
	path, attr := unprivileged(t)
	h := runHelper(t, path, attr, "moving")
	if h.Getenv != "" {
		t.Errorf("os.Getenv still returns the key: %q", h.Getenv)
	}
	if h.Secret != canary {
		t.Errorf("Secret = %q, want the key", h.Secret)
	}
	if strings.Contains(h.ChildEnv, "HARDEN_T_API_KEY") || strings.Contains(h.ChildEnv, canary) {
		t.Errorf("a child started with the inherited environment received the key:\n%s", h.ChildEnv)
	}
	if !strings.Contains(h.ChildEnv, "HARDEN_T_PLAIN=visible-value") {
		t.Errorf("the rest of the environment was lost:\n%s", h.ChildEnv)
	}
	if got := h.Status.Moved; len(got) != 1 || got[0] != "HARDEN_T_API_KEY" {
		t.Errorf("Status.Moved = %v", got)
	}
	if len(h.Held) != 1 || h.Held[0] != "HARDEN_T_API_KEY" {
		t.Errorf("Held = %v", h.Held)
	}
}

// eraseEnviron in the test's own process, but with a predicate that erases nothing: the
// no-op path must not write and must report zero.
func TestEraseEnvironWithNothingToEraseIsANoop(t *testing.T) {
	needProc(t)
	before, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		t.Skip(err)
	}
	n, err := eraseEnviron(func(string, string) bool { return false })
	if err != nil || n != 0 {
		t.Fatalf("eraseEnviron = %d, %v", n, err)
	}
	after, _ := os.ReadFile("/proc/self/environ")
	if !bytes.Equal(before, after) {
		t.Fatal("the environment block changed")
	}
}

// The range the kernel reports parses to exactly what /proc/self/environ serves.
func TestParseEnvRangeOnTheRealStat(t *testing.T) {
	needProc(t)
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip(err)
	}
	start, end, err := parseEnvRange(stat)
	if err != nil {
		t.Skipf("this kernel does not report an environment range: %v", err)
	}
	served, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		t.Skip(err)
	}
	if end-start != uint64(len(served)) {
		t.Fatalf("stat says the environment is %d bytes, /proc/self/environ serves %d", end-start, len(served))
	}
}
