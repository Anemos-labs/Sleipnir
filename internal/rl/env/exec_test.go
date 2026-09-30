package env

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

var lookPathOS = exec.LookPath

func testSandbox(t testing.TB) *LocalSandbox {
	t.Helper()
	s, err := NewLocalSandbox(LocalSandboxOptions{DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func minimalEnv(extra ...string) []string {
	return append([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C"}, extra...)
}

func run(t testing.TB, s Sandbox, dir, cmd string, timeout time.Duration, env ...string) ExecResult {
	t.Helper()
	res, err := s.Exec(context.Background(), dir, minimalEnv(env...), cmd, timeout)
	if err != nil {
		t.Fatalf("exec %q: %v", cmd, err)
	}
	return res
}

// alive reports whether a process exists (signal 0).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil {
		return err == syscall.EPERM
	}
	// A zombie still answers signal 0; check /proc where available.
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i > 0 && i+2 < len(s) {
			return s[i+2] != 'Z'
		}
	}
	return true
}

func waitDead(t testing.TB, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("process %d is still alive", pid)
}

func readPID(t testing.TB, p string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(p); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return n
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", p)
	return 0
}

func TestExecBasics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell required")
	}
	s := testSandbox(t)
	dir := t.TempDir()
	tests := []struct {
		name     string
		cmd      string
		wantExit int
		wantSig  int
		stdout   string
		stderr   string
	}{
		{"success", "echo hello", 0, 0, "hello\n", ""},
		{"stderr separate", "echo out; echo err >&2", 0, 0, "out\n", "err\n"},
		{"exit code", "exit 7", 7, 0, "", ""},
		{"command not found", "definitely-not-a-command-xyz", 127, 0, "", ""},
		{"killed by signal", "kill -9 $$", 137, 9, "", ""},
		{"cwd", "pwd", 0, 0, dir + "\n", ""},
		{"stdin is /dev/null", "cat; echo done", 0, 0, "done\n", ""},
		{"no controlling terminal", "test -t 0 || echo notty", 0, 0, "notty\n", ""},
		{"multi line", "a=1\nb=2\necho $a$b", 0, 0, "12\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := run(t, s, dir, tc.cmd, 30*time.Second)
			if res.ExitCode != tc.wantExit || res.Signal != tc.wantSig {
				t.Errorf("exit=%d signal=%d, want %d/%d (stderr %q)", res.ExitCode, res.Signal, tc.wantExit, tc.wantSig, res.Stderr)
			}
			if tc.name == "cwd" {
				// /tmp may be a symlink on some hosts; compare resolved paths.
				want, _ := filepath.EvalSymlinks(dir)
				got, _ := filepath.EvalSymlinks(strings.TrimSpace(res.Stdout))
				if got != want {
					t.Errorf("cwd = %q want %q", got, want)
				}
			} else if res.Stdout != tc.stdout {
				t.Errorf("stdout = %q, want %q", res.Stdout, tc.stdout)
			}
			if tc.stderr != "" && res.Stderr != tc.stderr {
				t.Errorf("stderr = %q, want %q", res.Stderr, tc.stderr)
			}
			if res.TimedOut || res.Canceled || res.OutputKilled || res.Truncated {
				t.Errorf("unexpected flags: %+v", res)
			}
		})
	}
}

func TestExecInterleavesOutput(t *testing.T) {
	s := testSandbox(t)
	res := run(t, s, t.TempDir(), "echo one; sleep 0.05; echo two >&2; sleep 0.05; echo three", 10*time.Second)
	if res.Output != "one\ntwo\nthree\n" {
		t.Fatalf("Output = %q", res.Output)
	}
}

func TestExecEnvironmentIsExact(t *testing.T) {
	s := testSandbox(t)
	// A variable of the harness's own environment must not reach the command,
	// even though exec would inherit it for a nil Env.
	t.Setenv("HARNESS_ONLY_API_KEY", "harness-secret-value")
	res := run(t, s, t.TempDir(), "env | sort", 10*time.Second, "FOO=bar")
	if strings.Contains(res.Stdout, "harness-secret-value") || strings.Contains(res.Stdout, "HARNESS_ONLY_API_KEY") {
		t.Fatalf("harness environment leaked:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "FOO=bar") {
		t.Fatalf("requested variable missing:\n%s", res.Stdout)
	}
	// An empty environment stays empty rather than falling back to the parent's.
	res2, err := s.Exec(context.Background(), t.TempDir(), nil, "echo ${HARNESS_ONLY_API_KEY:-unset}", 10*time.Second)
	if err != nil || strings.TrimSpace(res2.Stdout) != "unset" {
		t.Fatalf("nil env inherited the parent's: %q %v", res2.Stdout, err)
	}
}

func TestExecTimeoutKillsProcessGroup(t *testing.T) {
	s := testSandbox(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	// The child would outlive a plain kill of the shell.
	cmd := fmt.Sprintf("sleep 60 & echo $! > %s; wait", pidFile)
	start := time.Now()
	res := run(t, s, dir, cmd, 300*time.Millisecond)
	if !res.TimedOut {
		t.Fatalf("expected a timeout: %+v", res)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("timeout took %v", d)
	}
	waitDead(t, readPID(t, pidFile))
}

func TestExecKillsBackgroundStragglersAfterNormalExit(t *testing.T) {
	s := testSandbox(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "daemon.pid")
	// The shell exits at once; the background process holds stdout open.
	start := time.Now()
	res := run(t, s, dir, fmt.Sprintf("sleep 60 & echo $! > %s; echo done", pidFile), 30*time.Second)
	if res.ExitCode != 0 || res.Stdout != "done\n" || res.TimedOut {
		t.Fatalf("got %+v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Exec waited %v for a background process holding the pipe", d)
	}
	waitDead(t, readPID(t, pidFile))
}

func TestExecIgnoresSIGTERMButIsKilled(t *testing.T) {
	s := testSandbox(t)
	ctx := WithExecPolicy(context.Background(), ExecPolicy{Grace: 200 * time.Millisecond})
	start := time.Now()
	res, err := s.Exec(ctx, t.TempDir(), minimalEnv(), `trap '' TERM; while :; do sleep 0.05; done`, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Signal != 9 {
		t.Fatalf("expected SIGKILL after the grace period: %+v", res)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestExecContextCancel(t *testing.T) {
	s := testSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	res, err := s.Exec(ctx, t.TempDir(), minimalEnv(), "sleep 60", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Canceled || res.TimedOut {
		t.Fatalf("got %+v", res)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("cancel was not prompt")
	}
	// Already-cancelled contexts never start anything.
	if _, err := s.Exec(ctx, t.TempDir(), minimalEnv(), "touch should-not-exist", time.Minute); err == nil {
		t.Fatal("cancelled context started a command")
	}
}

func TestExecHugeOutputIsCapped(t *testing.T) {
	s := testSandbox(t)
	ctx := WithExecPolicy(context.Background(), ExecPolicy{MaxOutput: 4096})
	cmd := `echo START; head -c 20000000 /dev/zero | tr '\0' 'x'; echo; echo END`
	res, err := s.Exec(ctx, t.TempDir(), minimalEnv(), cmd, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("Truncated not set")
	}
	if len(res.Stdout) > 4096+200 || len(res.Output) > 4096+200 {
		t.Fatalf("output not capped: stdout %d output %d", len(res.Stdout), len(res.Output))
	}
	if !strings.HasPrefix(res.Stdout, "START\n") || !strings.HasSuffix(res.Stdout, "END\n") {
		t.Fatalf("head and tail must both survive: %q ... %q", res.Stdout[:10], res.Stdout[len(res.Stdout)-10:])
	}
	if !strings.Contains(res.Stdout, "bytes of output dropped") {
		t.Fatal("no truncation marker")
	}
}

func TestExecRunawayOutputIsKilled(t *testing.T) {
	s := testSandbox(t)
	ctx := WithExecPolicy(context.Background(), ExecPolicy{MaxOutput: 1024, KillOutput: 1 << 20, Grace: 100 * time.Millisecond})
	start := time.Now()
	res, err := s.Exec(ctx, t.TempDir(), minimalEnv(), "yes", 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OutputKilled || res.TimedOut {
		t.Fatalf("got %+v", res)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("output kill was slow")
	}
	if len(res.Output) > 2048 {
		t.Fatalf("kept %d bytes", len(res.Output))
	}
}

func TestExecStartErrors(t *testing.T) {
	s := testSandbox(t)
	if _, err := s.Exec(context.Background(), filepath.Join(t.TempDir(), "missing"), minimalEnv(), "true", time.Second); err == nil {
		t.Error("nonexistent directory accepted")
	}
	if _, err := s.Exec(context.Background(), t.TempDir(), minimalEnv(), "true\x00rm -rf /", time.Second); err == nil {
		t.Error("NUL byte in command accepted")
	}
}

func TestExecSweepsEscapedDaemonsByMarker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("marker sweeping needs /proc")
	}
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		if _, err := os.Stat("/bin/setsid"); err != nil {
			t.Skip("setsid not installed")
		}
	}
	s := testSandbox(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "escaped.pid")
	marker := fmt.Sprintf("test-%d-%d", os.Getpid(), time.Now().UnixNano())
	ctx := WithExecPolicy(context.Background(), ExecPolicy{Marker: marker, Grace: 100 * time.Millisecond})
	// setsid moves the daemon into a session (and group) of its own, so
	// killing the command's process group cannot reach it.
	cmd := fmt.Sprintf(`setsid sh -c 'echo $$ > %s; exec sleep 120' >/dev/null 2>&1 & sleep 30`, pidFile)
	res, err := s.Exec(ctx, dir, minimalEnv(MarkerEnv+"="+marker), cmd, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("expected timeout: %+v", res)
	}
	waitDead(t, readPID(t, pidFile))
}

func TestArgvWrapping(t *testing.T) {
	iso := NetIsolation{Available: true, Prefix: []string{"/usr/bin/unshare", "-n", "--"}, Loopback: []string{"/sbin/ip", "link", "set", "lo", "up"}}
	s := &LocalSandbox{shell: "/bin/sh", prlimit: "/usr/bin/prlimit", netIso: iso}

	argv, warns := s.argv("go test ./...", ExecPolicy{}.withDefaults())
	joined := strings.Join(argv, " ")
	for _, want := range []string{"/usr/bin/unshare -n --", "/bin/sh -c", "/sbin/ip link set lo up", "exec \"$@\"", "/usr/bin/prlimit --core=0 --fsize=1073741824 --", "go test ./..."} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q:\n%s", want, joined)
		}
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings %v", warns)
	}
	if argv[len(argv)-1] != "go test ./..." || argv[len(argv)-2] != "-c" {
		t.Errorf("the command must reach the inner shell unmodified as one argument: %q", argv)
	}

	// Network allowed: no namespace.
	argv, _ = s.argv("curl x", ExecPolicy{Network: true}.withDefaults())
	if strings.Contains(strings.Join(argv, " "), "unshare") {
		t.Errorf("network task was isolated: %q", argv)
	}

	// Limits are configurable and the default file size can be removed.
	argv, _ = s.argv("x", ExecPolicy{Limits: Limits{FileSize: -1, OpenFiles: 64, Procs: 32, CPU: 90 * time.Second, AddressSpace: 1 << 30, Core: true}}.withDefaults())
	joined = strings.Join(argv, " ")
	for _, want := range []string{"--nofile=64", "--nproc=32", "--cpu=90", "--as=1073741824"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: %s", want, joined)
		}
	}
	for _, bad := range []string{"--fsize", "--core=0"} {
		if strings.Contains(joined, bad) {
			t.Errorf("argv unexpectedly has %q: %s", bad, joined)
		}
	}

	// Without isolation the fallback is a recorded warning, not a silent gap.
	s2 := &LocalSandbox{shell: "/bin/sh", netIso: NetIsolation{Reason: "no unshare"}}
	argv, warns = s2.argv("echo hi", ExecPolicy{}.withDefaults())
	if len(warns) != 1 || !strings.Contains(warns[0], "no unshare") {
		t.Errorf("warnings = %v", warns)
	}
	if !strings.Contains(strings.Join(argv, " "), "ulimit -c 0") {
		t.Errorf("shell fallback should disable core dumps: %q", argv)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"abc": "abc", "": "''", "a b": "'a b'", "it's": `'it'\''s'`, "$(x)": "'$(x)'", "/sbin/ip": "/sbin/ip",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectNetIsolationWithFakeHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("detection is Linux only")
	}
	found := map[string]string{"unshare": "/usr/bin/unshare", "ip": "/usr/sbin/ip"}
	look := func(n string) (string, error) {
		if p, ok := found[n]; ok {
			return p, nil
		}
		return "", os.ErrNotExist
	}
	var probed [][]string
	okProbe := func(ctx context.Context, name string, args ...string) error {
		probed = append(probed, append([]string{name}, args...))
		return nil
	}
	iso := detectNetIsolation(look, okProbe)
	if !iso.Available || iso.Prefix[1] != "-n" || len(probed) != 1 {
		t.Fatalf("got %+v probes %v", iso, probed)
	}

	// Root without CAP_SYS_ADMIN falls back to a user namespace.
	probed = nil
	calls := 0
	iso = detectNetIsolation(look, func(ctx context.Context, name string, args ...string) error {
		calls++
		if calls == 1 {
			return fmt.Errorf("operation not permitted")
		}
		return nil
	})
	if !iso.Available || iso.Prefix[1] != "-r" {
		t.Fatalf("expected the -r -n fallback: %+v", iso)
	}

	// Nothing works: a reason, not an error.
	iso = detectNetIsolation(look, func(context.Context, string, ...string) error { return fmt.Errorf("EPERM") })
	if iso.Available || !strings.Contains(iso.Reason, "EPERM") {
		t.Fatalf("got %+v", iso)
	}
	delete(found, "unshare")
	if iso = detectNetIsolation(look, okProbe); iso.Available || !strings.Contains(iso.Reason, "unshare") {
		t.Fatalf("got %+v", iso)
	}
	found["unshare"] = "/usr/bin/unshare"
	delete(found, "ip")
	iso = detectNetIsolation(look, okProbe)
	// /sbin/ip etc. may exist on the host running the test, in which case
	// detection legitimately succeeds.
	if !iso.Available && !strings.Contains(iso.Reason, "loopback") {
		t.Fatalf("got %+v", iso)
	}
}

// TestNetIsolationReal runs a command in a real network namespace when the host
// allows it. A fake "ip" stands in for iproute2 so the test does not depend on
// it being installed; what is asserted is that the command sees no interface
// but loopback, which no amount of host connectivity can fake.
func TestNetIsolationReal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "ip"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewLocalSandbox(LocalSandboxOptions{
		LookPath: func(n string) (string, error) {
			if n == "ip" {
				return filepath.Join(fakeBin, "ip"), nil
			}
			return lookPathOS(n)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.NetIsolation().Available {
		t.Skipf("no network namespaces on this host: %s", s.NetIsolation().Reason)
	}
	count := `tail -n +3 /proc/net/dev | wc -l`
	isolated, err := s.Exec(WithExecPolicy(context.Background(), ExecPolicy{Network: false}), t.TempDir(), minimalEnv(), count, 30*time.Second)
	if err != nil || isolated.ExitCode != 0 {
		t.Fatalf("isolated run: %+v %v", isolated, err)
	}
	open, err := s.Exec(WithExecPolicy(context.Background(), ExecPolicy{Network: true}), t.TempDir(), minimalEnv(), count, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(isolated.Stdout) != "1" {
		t.Errorf("isolated command sees %s interfaces, want only lo", strings.TrimSpace(isolated.Stdout))
	}
	if strings.TrimSpace(open.Stdout) == "1" {
		t.Skip("host has only a loopback interface, cannot tell the difference")
	}
	// The wrapped command keeps its exit status, cwd and quoting.
	res, err := s.Exec(context.Background(), t.TempDir(), minimalEnv(), `echo "a  b" '$HOME'; exit 3`, 30*time.Second)
	if err != nil || res.ExitCode != 3 || res.Stdout != "a  b $HOME\n" {
		t.Fatalf("wrapped command misbehaved: %+v %v", res, err)
	}
}

func TestCapBufMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 300; iter++ {
		max := int64(2 + rng.Intn(60))
		b := newCapBuf(max)
		var all []byte
		for w := 0; w < rng.Intn(20); w++ {
			chunk := make([]byte, rng.Intn(50))
			for i := range chunk {
				chunk[i] = byte('a' + rng.Intn(26))
			}
			b.Write(chunk)
			all = append(all, chunk...)
		}
		half := int(max / 2)
		if half < 1 {
			half = 1
		}
		var want string
		if len(all) <= 2*half {
			want = string(all)
		} else {
			want = string(all[:half]) + fmt.Sprintf("\n[... %d bytes of output dropped ...]\n", len(all)-2*half) + string(all[len(all)-half:])
		}
		if got := b.String(); got != want {
			t.Fatalf("iter %d (max %d, %d bytes written):\n got %q\nwant %q", iter, max, len(all), got, want)
		}
		if b.truncated() != (len(all) > 2*half) {
			t.Fatalf("iter %d: truncated=%v for %d bytes", iter, b.truncated(), len(all))
		}
	}
}

func TestCapBufReplacesInvalidUTF8(t *testing.T) {
	b := newCapBuf(100)
	b.Write([]byte("ok \xff\xfe bad"))
	if got := b.String(); !utf8.ValidString(got) || strings.Contains(got, "\xff") || !bytes.Contains([]byte(got), []byte("ok ")) {
		t.Fatalf("got %q", got)
	}
}

func TestExecConcurrent(t *testing.T) {
	s := testSandbox(t)
	errs := make(chan string, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			res, err := s.Exec(context.Background(), t.TempDir(), minimalEnv(), fmt.Sprintf("echo %d; exit %d", i, i%3), 30*time.Second)
			switch {
			case err != nil:
				errs <- err.Error()
			case strings.TrimSpace(res.Stdout) != strconv.Itoa(i) || res.ExitCode != i%3:
				errs <- fmt.Sprintf("run %d: %+v", i, res)
			default:
				errs <- ""
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		if e := <-errs; e != "" {
			t.Error(e)
		}
	}
}
