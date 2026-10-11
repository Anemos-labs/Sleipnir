package env

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// The argv hides, masks and re-exposes in a fixed order: parents before their
// children, protected directories never re-exposed through PATH or a cache
// variable, the command's own directories always.
func TestConfinementArgv(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) string {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hide, work, blobs := mk("hide"), mk("hide/work"), mk("hide/blobs")
	tool, cache, tree := mk("hide/toolchain/bin"), mk("hide/cache"), mk("hide/work/ws/a/tree")
	c := Confinement{Available: true, Bwrap: "/usr/bin/bwrap", Hide: []string{hide, work, blobs}, protected: []string{work, blobs}}
	env := []string{"PATH=" + tool + ":/usr/bin:" + blobs, "GOCACHE=" + cache, "GOMODCACHE=" + work}
	argv := c.argv(Mounts{RW: []string{tree}}, false, env, []string{"sh", "-c", "true"})
	line := strings.Join(argv, " ")
	for _, want := range []string{"--unshare-net", "--unshare-pid", "--ro-bind / /", "--tmpfs " + hide, "--tmpfs " + blobs,
		"--ro-bind " + tool + " " + tool, "--bind " + cache + " " + cache, "--bind " + tree + " " + tree, "-- sh -c true"} {
		if !strings.Contains(line, want) {
			t.Errorf("argv lacks %q:\n%s", want, line)
		}
	}
	for _, bad := range []string{"--ro-bind " + blobs + " ", "--bind " + work + " ", "--ro-bind " + work + " "} {
		if strings.Contains(line, bad) {
			t.Errorf("argv re-exposes a protected directory (%q):\n%s", bad, line)
		}
	}
	if i, j := strings.Index(line, "--tmpfs "+hide), strings.Index(line, "--bind "+tree); i < 0 || j < i {
		t.Errorf("the workspace must be bound after the directory hiding it:\n%s", line)
	}
	if net := strings.Join(c.argv(Mounts{}, true, nil, nil), " "); strings.Contains(net, "--unshare-net") || !strings.HasSuffix(net, " --") {
		t.Errorf("a network task keeps the network, and a prefix ends in --: %s", net)
	}
}

// Asking for confinement where bubblewrap is missing or broken fails the
// sandbox's construction instead of running unconfined.
func TestConfinementRequiredButUnavailable(t *testing.T) {
	for name, o := range map[string]LocalSandboxOptions{
		"missing": {Confine: ConfineBwrap, LookPath: func(string) (string, error) { return "", exec.ErrNotFound }},
		"broken": {Confine: ConfineBwrap, LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
			Probe: func(context.Context, string, ...string) error { return errors.New("no user namespaces") }},
		"unknown": {Confine: "docker"},
	} {
		if _, err := NewLocalSandbox(o); err == nil {
			t.Errorf("%s: confinement was required, construction must fail", name)
		} else if name != "unknown" && !errors.Is(err, ErrConfinementUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// confinedSandbox returns a sandbox confining commands, skipping where the host
// has no working bubblewrap.
func confinedSandbox(t *testing.T, protect ...string) *LocalSandbox {
	t.Helper()
	s, err := NewLocalSandbox(LocalSandboxOptions{Confine: ConfineBwrap, ConfineOptions: ConfineOptions{Protect: protect}})
	if errors.Is(err, ErrConfinementUnavailable) {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// What a confined command can and cannot reach, run for real.
func TestConfinedCommand(t *testing.T) {
	root := t.TempDir()
	blobs := filepath.Join(root, "blobs")
	ws := filepath.Join(root, "work", "ws", "t-s0")
	tree, home, tmp := filepath.Join(ws, "tree"), filepath.Join(ws, "home"), filepath.Join(ws, "tmp")
	for _, d := range []string{blobs, tree, home, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(blobs, "hidden_test.py")
	if err := os.WriteFile(secret, []byte("the answer"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := confinedSandbox(t, blobs, filepath.Join(root, "work"))
	pol := ExecPolicy{Mounts: &Mounts{RW: []string{tree, home, tmp}}}
	run := func(cmd string) ExecResult {
		t.Helper()
		env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + tmp}
		res, err := s.Exec(WithExecPolicy(context.Background(), pol), tree, env, cmd, time.Minute)
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		return res
	}
	if res := run("cat " + secret); res.ExitCode == 0 || strings.Contains(res.Output, "the answer") {
		t.Errorf("the blob store is readable: %+v", res)
	}
	if res := run("echo made > f && cat f && echo x > \"$HOME/h\" && echo y > \"$TMPDIR/t\""); res.ExitCode != 0 || !strings.Contains(res.Stdout, "made") {
		t.Errorf("the workspace must be writable: %+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(tree, "f")); err != nil || string(b) != "made\n" {
		t.Errorf("a write in the workspace reaches the real tree: %q %v", b, err)
	}
	// Outside its workspace a command writes at most into an empty private
	// tmpfs (here: the test's root lies in the hidden /tmp); nothing reaches the
	// host, and the read-only system directories refuse writes.
	run("echo x > " + filepath.Join(root, "escape"))
	if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
		t.Error("a write outside the workspace reached the host")
	}
	if res := run("echo x > /usr/sleipnir-escape"); res.ExitCode == 0 {
		t.Error("/usr is writable")
	}
	if res := run("ls /proc | grep -c '^[0-9]'"); res.ExitCode != 0 {
		t.Errorf("counting processes: %+v", res)
	} else if n, err := strconv.Atoi(strings.TrimSpace(res.Stdout)); err != nil || n > 8 {
		t.Errorf("the command sees %q processes: not only its own (its PID namespace)", res.Stdout)
	}
	if res := run("grep -v -e Inter -e face /proc/net/dev | cut -d: -f1 | tr -d ' '"); strings.TrimSpace(res.Stdout) != "lo" {
		t.Errorf("a network-less command sees interfaces %q", res.Stdout)
	}
	// Masked: replaced by the null device, which reads empty (or, where the
	// sandbox's mounts are nodev, cannot be opened at all); never the content.
	if _, err := os.Stat("/etc/shadow"); err == nil {
		if res := run("ls -l /etc/shadow; cat /etc/shadow"); !strings.HasPrefix(res.Stdout, "c") || strings.Contains(res.Stdout, "root:") {
			t.Errorf("/etc/shadow is not masked: %q %q", res.Stdout, res.Stderr)
		}
	}
	// Unconfined (no Mounts in the policy), the same sandbox runs as before.
	res, err := s.Exec(context.Background(), tree, []string{"PATH=/usr/bin:/bin"}, "cat "+secret, time.Minute)
	if err != nil || res.ExitCode != 0 {
		t.Errorf("without Mounts a command is not confined: %+v %v", res, err)
	}
}

// End to end through Workspaces: the agent's shell (CommandPrefix) and the
// verifier both work in their own trees and cannot read a protected store.
func TestConfinedWorkspaceAndVerifier(t *testing.T) {
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "gold.patch")
	if err := os.WriteFile(secret, []byte("the reference solution"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newFixtureRepo(t)
	r.write("app.txt", "broken\n")
	base := r.commit("start")
	m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), DisableNetIsolation: true, FailureTTL: -1,
		Confine: ConfineBwrap, ConfineProtect: []string{secretDir}})
	if errors.Is(err, ErrConfinementUnavailable) {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	task := rl.Task{
		ID: "confined", Kind: rl.TaskFix, Repo: rl.RepoSpec{Path: r.Dir, Commit: base}, Prompt: "fix app.txt",
		// Passes only if app.txt was fixed AND the protected store is unreadable from the verifier.
		Verifier: rl.Verifier{Cmd: "grep -q fixed app.txt && ! cat " + secret + " 2>/dev/null", TimeoutS: 60},
		Budget:   rl.Budget{WallS: 300},
	}
	ws, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Cleanup() })
	prefix := m.CommandPrefix(task, ws)
	if !m.Confined() || len(prefix) == 0 || prefix[len(prefix)-1] != "--" {
		t.Fatalf("CommandPrefix: %v", prefix)
	}
	shell := func(cmd string) (string, error) {
		argv := append(append([]string(nil), prefix...), "/bin/sh", "-c", cmd)
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir, c.Env = ws.Dir, ws.Env()
		out, err := c.CombinedOutput()
		return string(out), err
	}
	if out, err := shell("cat " + secret); err == nil || strings.Contains(out, "reference solution") {
		t.Errorf("the agent's shell read the protected store: %q", out)
	}
	if out, err := shell("git status --short >/dev/null && echo fixed > app.txt"); err != nil {
		t.Fatalf("the agent's shell cannot work in its tree (git must read the snapshot objects): %v %s", err, out)
	}
	res, err := Verify(ctxT(t), task, ws, VerifyOptions{})
	if err != nil || !res.Pass {
		t.Fatalf("verify: %v %+v\n%s", err, res, res.Log)
	}
}
