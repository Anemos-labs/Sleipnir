package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/mcp/mcptest"
)

func skipNotUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process groups and signals are Unix-only")
	}
}

// processAlive reports whether pid is a live (non-zombie) process. Zombies do
// not count: an orphaned, killed grandchild lingers until init reaps it, which
// in a container may be never.
func processAlive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z' && s[i+2] != 'X'
}

func needProc(t *testing.T) {
	t.Helper()
	skipNotUnix(t)
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
}

func dialProcTest(t *testing.T, cfg ServerConfig, o DialOptions) (*Client, error) {
	t.Helper()
	if o.BaseEnv == nil {
		o.BaseEnv = SafeBaseEnv(os.Environ())
	}
	if o.ShutdownGrace == 0 {
		o.ShutdownGrace = 300 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Dial(ctx, "child", cfg, o)
	if c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

func TestProcHandshakeAndCalls(t *testing.T) {
	skipNotUnix(t)
	c, err := dialProcTest(t, helperCfg("", nil), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := callText(t, c, "echo", `{"message":"from a real child process"}`); got != "from a real child process" {
		t.Fatal(got)
	}
	if info := c.Initialized(); info.ServerInfo.Name != "mcptest" || info.ProtocolVersion != LatestProtocolVersion {
		t.Errorf("%+v", info)
	}
}

func TestProcEnvironmentIsScrubbed(t *testing.T) {
	skipNotUnix(t)
	// The harness's own credentials, in every shape they come in.
	secrets := map[string]string{
		"OPENAI_API_KEY":         placeholder + "-openai",
		"ANTHROPIC_API_KEY":      placeholder + "-anthropic",
		"AWS_SECRET_ACCESS_KEY":  placeholder + "-aws",
		"GITHUB_TOKEN":           placeholder + "-github",
		"DATABASE_URL":           "postgres://user:" + placeholder + "@db/x",
		"HTTPS_PROXY":            "http://user:" + placeholder + "@proxy.example:3128",
		"SSH_AUTH_SOCK":          "/tmp/agent." + placeholder,
		"SLEIPNIR_SESSION_TOKEN": placeholder + "-session",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("MCPTEST_UNRELATED", "kept-out")

	cfg := helperCfg("", map[string]string{"EXTRA": "configured", "TOKEN": "${MY_TOKEN}"})
	c, err := dialProcTest(t, cfg, DialOptions{BaseEnv: nil, Env: map[string]string{"MY_TOKEN": placeholder + "-explicit"}})
	if err != nil {
		t.Fatal(err)
	}
	out := callText(t, c, "env", `{}`)
	for k, v := range secrets {
		if strings.Contains(out, v) || strings.Contains(out, k+"=") {
			t.Errorf("the server inherited %s from the harness", k)
		}
	}
	if strings.Contains(out, "MCPTEST_UNRELATED") {
		t.Error("an arbitrary variable of the harness leaked")
	}
	env := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			env[k] = v
		}
	}
	if env["EXTRA"] != "configured" {
		t.Errorf("configured env missing: EXTRA=%q", env["EXTRA"])
	}
	if env["TOKEN"] != placeholder+"-explicit" {
		t.Errorf("${MY_TOKEN} was not expanded from the explicit map: %q", env["TOKEN"])
	}
	if env["PATH"] == "" || env["LC_ALL"] != "C.UTF-8" {
		t.Errorf("the minimal safe base is missing: PATH=%q LC_ALL=%q", env["PATH"], env["LC_ALL"])
	}
	if env["HOME"] == "" && os.Getenv("HOME") != "" {
		t.Error("HOME missing from the safe base")
	}
}

func TestProcEnvExpansionNeverReadsTheProcessEnvironment(t *testing.T) {
	skipNotUnix(t)
	t.Setenv("ONLY_IN_PROCESS", "leak")
	cfg := helperCfg("", map[string]string{"X": "${ONLY_IN_PROCESS}"})
	_, err := dialProcTest(t, cfg, DialOptions{Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "ONLY_IN_PROCESS") || !strings.Contains(err.Error(), "env.X") {
		t.Fatalf("err = %v, want a named unset variable", err)
	}
	if !errors.Is(err, errConfig) {
		t.Errorf("an unset variable is a configuration error: %v", err)
	}
	if strings.Contains(err.Error(), "leak") {
		t.Errorf("value leaked into the error: %v", err)
	}
}

func TestProcConfiguredEnvOverridesTheBase(t *testing.T) {
	skipNotUnix(t)
	cfg := helperCfg("", map[string]string{"LANG": "xx_XX.UTF-8"})
	c, err := dialProcTest(t, cfg, DialOptions{BaseEnv: []string{"LANG=en_US.UTF-8", "PATH=" + os.Getenv("PATH")}})
	if err != nil {
		t.Fatal(err)
	}
	if out := callText(t, c, "env", `{}`); !strings.Contains(out, "LANG=xx_XX.UTF-8") || strings.Contains(out, "en_US") {
		t.Errorf("env:\n%s", out)
	}
}

func TestProcCrashMidCallReportsTheExit(t *testing.T) {
	skipNotUnix(t)
	c, err := dialProcTest(t, helperCfg("", nil), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CallTool(context.Background(), "crash", nil, CallOptions{Timeout: 10 * time.Second})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("the error should say how the process died: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed")
	}
	if _, err := c.CallTool(context.Background(), "echo", nil, CallOptions{}); !errors.Is(err, ErrClosed) {
		t.Errorf("call after crash = %v", err)
	}
}

func TestProcStartupFailureCarriesStderr(t *testing.T) {
	skipNotUnix(t)
	_, err := dialProcTest(t, helperCfg("exit", nil), DialOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "fatal: cannot start: missing configuration") {
		t.Errorf("err = %v", err)
	}
}

func TestProcStderrIsRedacted(t *testing.T) {
	skipNotUnix(t)
	cfg := helperCfg("secret-stderr", map[string]string{mcptest.EnvSecret: placeholder + "-secret"})
	_, err := dialProcTest(t, cfg, DialOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), placeholder) {
		t.Errorf("the server logged its own credential and it reached the error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") || !strings.Contains(err.Error(), "starting with token") {
		t.Errorf("redaction should mask, not delete: %v", err)
	}
}

func TestProcHangInInitializeTimesOutAndTheChildDies(t *testing.T) {
	needProc(t)
	env := childEnv(SafeBaseEnv(os.Environ()), helperCfg("hang", nil).Env)
	p, err := startProc(procSpec{Command: os.Args[0], Args: []string{"-test.run=^$"}, Env: env, Grace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.pid
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Connect(ctx, p, ClientOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the child of a failed connection must be reaped")
	}
	if processAlive(pid) {
		t.Error("child still running")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("teardown too slow")
	}
}

func TestProcOrderlyShutdownIsFastAndClean(t *testing.T) {
	needProc(t)
	env := childEnv(SafeBaseEnv(os.Environ()), helperCfg("", nil).Env)
	p, err := startProc(procSpec{Command: os.Args[0], Args: []string{"-test.run=^$"}, Env: env, Grace: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Connect(context.Background(), p, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Close took %v: stdin EOF should be enough for a well-behaved server", d)
	}
	<-p.done
	if st := p.cmd.ProcessState; st == nil || st.ExitCode() != 0 {
		t.Errorf("exit = %v, want a clean exit", st)
	}
}

func TestProcEscalatesToKillForAServerThatIgnoresEverything(t *testing.T) {
	needProc(t)
	env := childEnv(SafeBaseEnv(os.Environ()), helperCfg("ignoreterm", nil).Env)
	p, err := startProc(procSpec{Command: os.Args[0], Args: []string{"-test.run=^$"}, Env: env, Grace: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Connect(context.Background(), p, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c.Close()
	d := time.Since(start)
	if d < 100*time.Millisecond {
		t.Errorf("Close returned in %v: it should have waited for the grace period before escalating", d)
	}
	if d > 5*time.Second {
		t.Errorf("Close took %v", d)
	}
	select {
	case <-p.done:
	default:
		t.Fatal("process not reaped")
	}
	if processAlive(p.pid) {
		t.Error("process survived SIGKILL")
	}
	if ws := p.cmd.ProcessState.String(); !strings.Contains(ws, "killed") {
		t.Errorf("state = %q, want it killed (SIGTERM was ignored)", ws)
	}
}

func TestProcKillsTheWholeProcessGroup(t *testing.T) {
	needProc(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cfg := helperCfg("grandchild", map[string]string{mcptest.EnvPidFile: pidFile})
	c, err := dialProcTest(t, cfg, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gpid int
	waitFor(t, "the grandchild pid", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		gpid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return gpid > 0
	})
	if !processAlive(gpid) {
		t.Fatal("grandchild is not running before Close")
	}
	c.Close()
	waitFor(t, "the grandchild to die", func() bool { return !processAlive(gpid) })
}

func TestProcGarbageOnStdoutIsSkipped(t *testing.T) {
	skipNotUnix(t)
	var logs []string
	var mu sync.Mutex
	c, err := dialProcTest(t, helperCfg("garbage", nil), DialOptions{Client: ClientOptions{Logf: func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, f)
	}}})
	if err != nil {
		t.Fatalf("banner text on stdout must not break the handshake: %v", err)
	}
	if got := callText(t, c, "echo", `{"message":"ok"}`); got != "ok" {
		t.Fatal(got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) == 0 || len(logs) > 3 {
		t.Errorf("garbage should be logged once, not per line: %d log lines", len(logs))
	}
}

func TestProcNotificationFloodDoesNotStarveCalls(t *testing.T) {
	skipNotUnix(t)
	c, err := dialProcTest(t, helperCfg("flood", nil), DialOptions{Client: ClientOptions{OnLog: func(LogMessage) { time.Sleep(time.Millisecond) }}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		start := time.Now()
		if got := callText(t, c, "echo", `{"message":"through"}`); got != "through" {
			t.Fatal(got)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("call %d took %v under a flood", i, d)
		}
	}
	if c.Err() != nil {
		t.Errorf("a notification flood is not a reason to disconnect: %v", c.Err())
	}
}

func TestProcRootsAreOnlyGivenToLocalServers(t *testing.T) {
	skipNotUnix(t)
	roots := []Root{{Path: "/work/project", Name: "project"}}
	c, err := dialProcTest(t, helperCfg("", nil), DialOptions{Client: ClientOptions{Roots: roots}})
	if err != nil {
		t.Fatal(err)
	}
	if got := callText(t, c, "roots", `{}`); !strings.Contains(got, "file:///work/project") || !strings.Contains(got, `"project"`) {
		t.Errorf("stdio server should see the roots: %q", got)
	}

	ts, _ := httpServer(t, mcptest.New(), mcptest.HTTPOptions{})
	cfg := httpCfg(ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := Dial(ctx, "remote", cfg, DialOptions{Client: ClientOptions{Roots: roots}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got := callText(t, rc, "roots", `{}`)
	if strings.Contains(got, "/work/project") || !strings.Contains(got, "client error") {
		t.Errorf("a remote server must learn nothing about local paths: %q", got)
	}
}

func TestProcWorkingDirectory(t *testing.T) {
	skipNotUnix(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	realSub, _ := filepath.EvalSymlinks(sub)
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"default is the manager's directory", "", realRoot},
		{"relative to the manager's directory", "sub", realSub},
		{"absolute", sub, realSub},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := helperCfg("", nil)
			cfg.Cwd = tt.cwd
			c, err := dialProcTest(t, cfg, DialOptions{Cwd: root})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := filepath.EvalSymlinks(callText(t, c, "cwd", `{}`))
			if got != tt.want {
				t.Errorf("cwd = %q, want %q", got, tt.want)
			}
		})
	}
	cfg := helperCfg("", nil)
	cfg.Cwd = filepath.Join(root, "missing")
	if _, err := dialProcTest(t, cfg, DialOptions{Cwd: root}); err == nil || !strings.Contains(err.Error(), "cwd") {
		t.Errorf("a missing working directory must be a clear error: %v", err)
	}
}

func TestProcCommandNotFound(t *testing.T) {
	skipNotUnix(t)
	cfg := ServerConfig{Type: TypeStdio, Command: "definitely-not-a-real-binary-xyz", Trust: true}
	_, err := dialProcTest(t, cfg, DialOptions{})
	if !errors.Is(err, exec.ErrNotFound) || !isFatalDial(err) {
		t.Errorf("err = %v (fatal=%v): retrying cannot help", err, isFatalDial(err))
	}
	cfg.Command = filepath.Join(t.TempDir(), "missing")
	if _, err := dialProcTest(t, cfg, DialOptions{}); !errors.Is(err, os.ErrNotExist) || !isFatalDial(err) {
		t.Errorf("err = %v", err)
	}
}

func TestResolveCommandIgnoresRelativePathEntries(t *testing.T) {
	skipNotUnix(t)
	dir := t.TempDir()
	evil := filepath.Join(dir, "npx")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := t.TempDir()
	if err := os.WriteFile(filepath.Join(good, "tool"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A repository can plant an executable, and a PATH with "", "." or a relative
	// directory would find it just because the working directory is the repo.
	for _, path := range []string{"", ".", ":", "::" + good + "::", "relative/dir", dir + "/../" + filepath.Base(dir)[:0] + "."} {
		t.Run("path="+path, func(t *testing.T) {
			prev, _ := os.Getwd()
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(prev)
			got, err := resolveCommand("npx", []string{"PATH=" + path}, dir)
			if err == nil {
				t.Errorf("resolved %q from PATH=%q", got, path)
			}
		})
	}
	if got, err := resolveCommand("tool", []string{"PATH=" + good}, ""); err != nil || got != filepath.Join(good, "tool") {
		t.Errorf("absolute PATH entry: %q %v", got, err)
	}
	if got, err := resolveCommand("./x/y", []string{"PATH=" + good}, ""); err != nil || got != "./x/y" {
		t.Errorf("explicit relative path is passed to exec: %q %v", got, err)
	}
	// Non-executable files and directories do not resolve.
	if err := os.WriteFile(filepath.Join(good, "data"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCommand("data", []string{"PATH=" + good}, ""); err == nil {
		t.Error("a non-executable file resolved")
	}
}

func TestSafeBaseEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/home/u", "USER=u", "LANG=C", "LC_ALL=C", "LC_MESSAGES=C", "TZ=UTC", "TMPDIR=/tmp", "TERM=xterm",
		"SSL_CERT_FILE=/etc/ssl/ca.pem", "XDG_CONFIG_HOME=/x", "SystemRoot=C:\\Windows", "path=/lower",
		"OPENAI_API_KEY=x", "ANTHROPIC_API_KEY=x", "GITHUB_TOKEN=x", "HTTP_PROXY=x", "HTTPS_PROXY=x", "NO_PROXY=x",
		"AWS_ACCESS_KEY_ID=x", "SSH_AUTH_SOCK=x", "DATABASE_URL=x", "NODE_OPTIONS=--require=/x", "LD_PRELOAD=/x",
		"NPM_CONFIG__AUTH=x", "novalue", "=bad", "SLEIPNIR_TOKEN=x",
	}
	got := SafeBaseEnv(in)
	joined := "\n" + strings.Join(got, "\n") + "\n"
	for _, keep := range []string{"PATH=", "HOME=", "USER=", "LANG=", "LC_ALL=", "LC_MESSAGES=", "TZ=", "TMPDIR=", "SSL_CERT_FILE=", "XDG_CONFIG_HOME=", "SystemRoot=", "path="} {
		if !strings.Contains(joined, "\n"+keep) {
			t.Errorf("%s should be kept", keep)
		}
	}
	for _, drop := range []string{"OPENAI", "ANTHROPIC", "GITHUB", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "AWS_", "SSH_AUTH", "DATABASE", "NODE_OPTIONS", "LD_PRELOAD", "NPM_", "SLEIPNIR", "novalue", "=bad"} {
		if strings.Contains(joined, drop) {
			t.Errorf("%s must not be inherited", drop)
		}
	}
}

func TestChildEnvIsDeterministic(t *testing.T) {
	a := childEnv([]string{"B=1", "A=2"}, map[string]string{"C": "3", "A": "override"})
	b := childEnv([]string{"A=2", "B=1"}, map[string]string{"A": "override", "C": "3"})
	if strings.Join(a, ",") != strings.Join(b, ",") || strings.Join(a, ",") != "A=override,B=1,C=3" {
		t.Errorf("%v %v", a, b)
	}
	if got := childEnv(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("an empty environment must be empty, not nil (nil means inherit): %#v", got)
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 100}
	for i := 0; i < 50; i++ {
		tb.Write([]byte("line " + strconv.Itoa(i) + "\n"))
	}
	if len(tb.buf) > 100 {
		t.Errorf("kept %d bytes", len(tb.buf))
	}
	ex := tb.excerpt(nil)
	if !strings.Contains(ex, "line 49") || strings.Contains(ex, "line 1 ") {
		t.Errorf("excerpt = %q", ex)
	}
	tb2 := &tailBuffer{max: 4096}
	tb2.Write([]byte("\x1b[31mred\x1b[0m token=" + placeholder + " \x1b]0;evil\x07ok\n"))
	ex = tb2.excerpt(newRedactor([]string{placeholder}))
	if strings.Contains(ex, "\x1b") || strings.Contains(ex, placeholder) || !strings.Contains(ex, "red") {
		t.Errorf("excerpt = %q", ex)
	}
	if got := (&tailBuffer{max: 10}).excerpt(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
	long := &tailBuffer{max: 1 << 20}
	long.Write([]byte(strings.Repeat("y", 5000)))
	if ex := long.excerpt(nil); len([]rune(ex)) > 300 {
		t.Errorf("excerpt not bounded: %d", len(ex))
	}
}

func TestProcConcurrentCallsAreRaceFree(t *testing.T) {
	skipNotUnix(t)
	c, err := dialProcTest(t, helperCfg("", nil), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				msg, _ := json.Marshal(map[string]any{"message": strconv.Itoa(g*100 + i)})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				r, err := c.CallTool(ctx, "echo", msg, CallOptions{})
				cancel()
				if err != nil || r.Content[0].Text != strconv.Itoa(g*100+i) {
					t.Errorf("g%d i%d: %v %v", g, i, r, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestProcOversizedResultOverStdio(t *testing.T) {
	skipNotUnix(t)
	c, err := dialProcTest(t, helperCfg("", nil), DialOptions{MaxMessageBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CallTool(context.Background(), "big", json.RawMessage(`{"bytes":2000000}`), CallOptions{})
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if got := callText(t, c, "echo", `{"message":"after"}`); got != "after" {
		t.Errorf("client unusable after an oversized result: %q", got)
	}
}

func TestProcSlowButWithinBudgetStartup(t *testing.T) {
	skipNotUnix(t)
	c, err := dialProcTest(t, helperCfg("slow-init", map[string]string{mcptest.EnvDelay: "400ms"}), DialOptions{StartupTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	callText(t, c, "echo", `{"message":"x"}`)
}
