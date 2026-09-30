package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
)

// verifyFixture is a manager, a task on the mathx repository and a workspace.
type verifyFixture struct {
	t    *testing.T
	m    *Workspaces
	r    *fixtureRepo
	base string
	task rl.Task
}

func newVerifyFixture(t *testing.T, mut ...func(*rl.Task)) *verifyFixture {
	t.Helper()
	r, base := mathxRepo(t)
	m := newManager(t)
	task := mathxTask(r, base)
	for _, f := range mut {
		f(&task)
	}
	return &verifyFixture{t: t, m: m, r: r, base: base, task: task}
}

func (f *verifyFixture) workspace(label string) *Workspace {
	f.t.Helper()
	w, err := f.m.Prepare(ctxT(f.t), f.task, label)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = w.Cleanup() })
	return w
}

func (f *verifyFixture) verify(w *Workspace, opts ...func(*VerifyOptions)) Result {
	f.t.Helper()
	res, err := f.verifyErr(w, opts...)
	if err != nil {
		f.t.Fatalf("Verify: %v", err)
	}
	return res
}

func (f *verifyFixture) verifyErr(w *Workspace, opts ...func(*VerifyOptions)) (Result, error) {
	f.t.Helper()
	o := VerifyOptions{}
	for _, mut := range opts {
		mut(&o)
	}
	return Verify(ctxT(f.t), f.task, w, o)
}

func fix(t *testing.T, w *Workspace) {
	t.Helper()
	mustWrite(t, filepath.Join(w.Root, "mathx.go"), fixedMath)
}

func TestVerifyPassesWithARealFix(t *testing.T) {
	f := newVerifyFixture(t)
	w := f.workspace("s0")
	fix(t, w)
	out := t.TempDir()
	store := events.NewMemBlobs()
	res := f.verify(w, func(o *VerifyOptions) { o.OutDir = out; o.Store = store })
	if !res.Pass || res.Score != 1 || res.ExitCode != 0 || res.Rejected != "" {
		t.Fatalf("result: %+v\nlog:\n%s", res, res.Log)
	}
	if res.Mode != "exit0" || !strings.HasPrefix(res.Version, "v1-") {
		t.Errorf("mode/version: %q %q", res.Mode, res.Version)
	}
	if !reflect.DeepEqual(res.HiddenWritten, []string{"mathx_hidden_test.go"}) {
		t.Errorf("hidden written: %v", res.HiddenWritten)
	}
	if res.FilesChanged != 1 || res.LinesAdded != 2 || res.LinesDeleted != 2 {
		t.Errorf("diff stats: %d files +%d -%d", res.FilesChanged, res.LinesAdded, res.LinesDeleted)
	}
	if len(res.ProtectedTouched) != 0 {
		t.Errorf("protected touched: %v", res.ProtectedTouched)
	}
	// Artifacts.
	if got := mustRead(t, filepath.Join(out, "diff.patch")); got != string(res.Diff) || !strings.Contains(got, "mathx.go") {
		t.Errorf("diff.patch: %q", got)
	}
	log := mustRead(t, filepath.Join(out, "verifier.log"))
	if log != res.Log || !strings.Contains(log, "ok") || !strings.Contains(log, "pass=true") {
		t.Errorf("verifier.log: %q", log)
	}
	if b, err := store.Get(res.LogBlob); err != nil || string(b) != res.Log {
		t.Errorf("log blob: %v", err)
	}
	if b, err := store.Get(res.DiffBlob); err != nil || string(b) != string(res.Diff) {
		t.Errorf("diff blob: %v", err)
	}
	if v := res.Verdict(); !v.Pass || v.Kind != "verifier" || v.Detail != res.LogBlob || v.Version != res.Version {
		t.Errorf("verdict: %+v", v)
	}
	// The hidden test never existed in the agent's workspace or in the cache.
	if exists(filepath.Join(w.Root, "mathx_hidden_test.go")) || exists(filepath.Join(w.snap.tree, "mathx_hidden_test.go")) {
		t.Fatal("hidden file leaked into the workspace or the cached snapshot")
	}
	// Verification checkouts are removed.
	if ents, _ := os.ReadDir(filepath.Join(f.m.Root(), "verify")); len(ents) != 0 {
		t.Errorf("verify/ not cleaned: %d entries", len(ents))
	}
}

func TestVerifyFailsWithoutFix(t *testing.T) {
	f := newVerifyFixture(t)
	w := f.workspace("s0")
	res := f.verify(w)
	if res.Pass || res.Score != 0 || res.ExitCode == 0 {
		t.Fatalf("result: %+v", res)
	}
	if !strings.Contains(res.Log, "TestMax") || !strings.Contains(res.Log, "FAIL") {
		t.Errorf("log lacks the failing test:\n%s", res.Log)
	}
	if len(res.Diff) != 0 || res.FilesChanged != 0 {
		t.Errorf("no diff expected: %d bytes", len(res.Diff))
	}
}

// ---- tampering ----

func TestVerifyTamperingWithTestsHasNoEffect(t *testing.T) {
	f := newVerifyFixture(t)

	t.Run("weakening the visible test does not help", func(t *testing.T) {
		w := f.workspace("weaken")
		mustWrite(t, filepath.Join(w.Root, "mathx_test.go"), "package mathx\n// all tests deleted\n")
		res := f.verify(w)
		if res.Pass {
			t.Fatal("the agent passed by editing a protected test")
		}
		if !reflect.DeepEqual(res.ProtectedTouched, []string{"mathx_test.go"}) {
			t.Fatalf("ProtectedTouched = %v", res.ProtectedTouched)
		}
	})
	t.Run("deleting tests is discarded and flagged, a real fix still passes", func(t *testing.T) {
		w := f.workspace("delete")
		fix(t, w)
		if err := os.Remove(filepath.Join(w.Root, "mathx_test.go")); err != nil {
			t.Fatal(err)
		}
		res := f.verify(w)
		if !res.Pass {
			t.Fatalf("legit fix rejected: %s", res.Log)
		}
		if !reflect.DeepEqual(res.ProtectedTouched, []string{"mathx_test.go"}) {
			t.Fatalf("ProtectedTouched = %v", res.ProtectedTouched)
		}
	})
	t.Run("pre-empting the hidden test file", func(t *testing.T) {
		w := f.workspace("preempt")
		mustWrite(t, filepath.Join(w.Root, "mathx_hidden_test.go"), "package mathx\nimport \"testing\"\nfunc TestMax(t *testing.T) {}\n")
		res := f.verify(w)
		if res.Pass {
			t.Fatal("a stub at the hidden path made the verifier pass")
		}
		if !reflect.DeepEqual(res.ProtectedTouched, []string{"mathx_hidden_test.go"}) || !reflect.DeepEqual(res.VerifierTouched, []string{"mathx_hidden_test.go"}) {
			t.Fatalf("touched %v verifier %v", res.ProtectedTouched, res.VerifierTouched)
		}
	})
	t.Run("go.mod changes are discarded", func(t *testing.T) {
		w := f.workspace("gomod")
		fix(t, w)
		mustWrite(t, filepath.Join(w.Root, "go.mod"), "module example.com/other\n\ngo 1.24\n\nreplace example.com/x => ./evil\n")
		res := f.verify(w)
		if !res.Pass || !reflect.DeepEqual(res.ProtectedTouched, []string{"go.mod"}) {
			t.Fatalf("pass=%v touched=%v\n%s", res.Pass, res.ProtectedTouched, res.Log)
		}
	})
	t.Run("case games on protected names", func(t *testing.T) {
		w := f.workspace("case")
		mustWrite(t, filepath.Join(w.Root, "MATHX_TEST.GO"), "package mathx\n")
		res := f.verify(w)
		if !reflect.DeepEqual(res.ProtectedTouched, []string{"MATHX_TEST.GO"}) {
			t.Fatalf("ProtectedTouched = %v", res.ProtectedTouched)
		}
	})
}

func TestVerifyAgentThatDeletesOrReplacesTheVerifier(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("go.mod", goMod)
	r.write("mathx.go", buggyMath)
	r.write("mathx_test.go", visibleTest)
	r.write("verify.sh", "#!/bin/sh\nexec go test -count=1 ./...\n")
	if err := os.Chmod(filepath.Join(r.Dir, "verify.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := r.commit("start")
	task := mathxTask(r, base)
	task.Verifier.Cmd = "sh ./verify.sh"
	task.Verifier.Protected = []string{"verify.sh", "*_test.go", "go.mod"}
	m := newManager(t)
	newWS := func(label string) *Workspace {
		w, err := m.Prepare(ctxT(t), task, label)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = w.Cleanup() })
		return w
	}
	t.Run("deleted", func(t *testing.T) {
		w := newWS("del")
		if err := os.Remove(filepath.Join(w.Root, "verify.sh")); err != nil {
			t.Fatal(err)
		}
		res, err := Verify(ctxT(t), task, w, VerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// The script still exists in the clean checkout, so the real verdict is
		// produced: the bug is still there.
		if res.Pass || res.ExitCode == 127 || res.ExitCode == 126 {
			t.Fatalf("the verifier vanished with the agent's deletion: exit %d\n%s", res.ExitCode, res.Log)
		}
		if !reflect.DeepEqual(res.VerifierTouched, []string{"verify.sh"}) {
			t.Fatalf("VerifierTouched = %v", res.VerifierTouched)
		}
	})
	t.Run("replaced by exit 0", func(t *testing.T) {
		w := newWS("shim")
		mustWrite(t, filepath.Join(w.Root, "verify.sh"), "#!/bin/sh\nexit 0\n")
		res, err := Verify(ctxT(t), task, w, VerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Pass {
			t.Fatal("an `exit 0` shim replaced the verifier")
		}
		if !reflect.DeepEqual(res.ProtectedTouched, []string{"verify.sh"}) {
			t.Fatalf("ProtectedTouched = %v", res.ProtectedTouched)
		}
	})
	t.Run("made non-executable", func(t *testing.T) {
		w := newWS("chmod")
		if err := os.Chmod(filepath.Join(w.Root, "verify.sh"), 0o644); err != nil {
			t.Fatal(err)
		}
		fixIn(t, w)
		res, err := Verify(ctxT(t), task, w, VerifyOptions{})
		if err != nil || !res.Pass {
			t.Fatalf("%v %+v", err, res)
		}
	})
}

func fixIn(t *testing.T, w *Workspace) { mustWrite(t, filepath.Join(w.Root, "mathx.go"), fixedMath) }

func TestVerifyHiddenWritesCannotEscapeThroughAgentSymlinks(t *testing.T) {
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "hidden_test.go"), "OUTSIDE ORIGINAL")
	f := newVerifyFixture(t, func(tk *rl.Task) {
		tk.Verifier.Hidden = map[string]string{
			"mathx_hidden_test.go":  "text:" + hiddenTest,
			"linkdir/extra_test.go": "text:package mathx\n",
		}
	})
	w := f.workspace("s0")
	fix(t, w)
	// The agent plants a symlinked directory where a hidden file will go, and a
	// symlink at the hidden test's own path pointing at a file it wants
	// overwritten.
	if err := os.Symlink(outside, filepath.Join(w.Root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "hidden_test.go"), filepath.Join(w.Root, "mathx_hidden_test.go")); err != nil {
		t.Fatal(err)
	}
	res := f.verify(w)
	if got := mustRead(t, filepath.Join(outside, "hidden_test.go")); got != "OUTSIDE ORIGINAL" {
		t.Fatalf("a hidden write followed the agent's symlink and clobbered a file outside: %q", got)
	}
	if exists(filepath.Join(outside, "extra_test.go")) {
		t.Fatal("hidden file was written through a symlinked directory")
	}
	if !res.Pass {
		t.Fatalf("legit fix should still pass:\n%s", res.Log)
	}
}

// ---- machinery failures vs verdicts ----

func TestVerifyTimeoutIsAVerdictAndKillsEverything(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	f := newVerifyFixture(t, func(tk *rl.Task) {
		tk.Verifier.Cmd = fmt.Sprintf("sleep 100 & echo $! > %s; wait", pidFile)
		tk.Verifier.TimeoutS = 1
	})
	w := f.workspace("s0")
	start := time.Now()
	res := f.verify(w)
	if res.Pass || res.Score != 0 || !res.TimedOut {
		t.Fatalf("a hanging verifier must be a failed verdict: %+v", res)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("took %v", d)
	}
	waitDead(t, readPID(t, pidFile))
	if !strings.Contains(res.Log, "timed out") {
		t.Errorf("log: %s", res.Log)
	}
}

func TestVerifyHugeOutputIsBounded(t *testing.T) {
	f := newVerifyFixture(t, func(tk *rl.Task) {
		tk.Verifier.Cmd = `echo BEGIN; head -c 30000000 /dev/zero | tr '\0' 'x'; echo; echo END-MARKER; exit 1`
	})
	w := f.workspace("s0")
	res := f.verify(w, func(o *VerifyOptions) { o.MaxOutput = 64 << 10 })
	if res.Pass || !res.Truncated {
		t.Fatalf("%+v", res)
	}
	if len(res.Log) > 400<<10 {
		t.Fatalf("log is %d bytes", len(res.Log))
	}
	if !strings.Contains(res.Log, "END-MARKER") || !strings.Contains(res.Log, "BEGIN") {
		t.Fatal("head or tail of the output was lost")
	}
}

func TestVerifyDiffTooLargeIsRejectedNotInfra(t *testing.T) {
	f := newVerifyFixture(t)
	w := f.workspace("s0")
	mustWrite(t, filepath.Join(w.Root, "huge.txt"), strings.Repeat("x\n", 300_000))
	res, err := f.verifyErr(w, func(o *VerifyOptions) { o.MaxDiffBytes = 100 << 10 })
	if err != nil {
		t.Fatalf("an oversized diff is the agent's doing, not an infra error: %v", err)
	}
	if res.Pass || res.Rejected == "" || res.Score != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestVerifyPatchThatDoesNotApplyIsRejectedNotInfra(t *testing.T) {
	f := newVerifyFixture(t)
	bad := "diff --git a/mathx.go b/mathx.go\n--- a/mathx.go\n+++ b/mathx.go\n@@ -1,3 +1,3 @@\n nothing like the file\n-at all\n+really\n context\n"
	res, err := VerifyPatch(ctxT(t), f.task, []byte(bad), VerifyOptions{Workspaces: f.m})
	if err != nil {
		t.Fatalf("a patch that does not apply must be a verdict: %v", err)
	}
	if res.Pass || res.Rejected == "" || !strings.Contains(res.Rejected, "does not apply") {
		t.Fatalf("%+v", res)
	}
}

func TestVerifyInfraFailures(t *testing.T) {
	blobStore := events.NewMemBlobs()
	goodHash, _ := blobStore.Put([]byte(hiddenTest))
	tests := []struct {
		name   string
		mutate func(*rl.Task)
		opts   func(*VerifyOptions)
		want   string
	}{
		{"missing blob", func(tk *rl.Task) {
			tk.Verifier.Hidden = map[string]string{"h_test.go": "blob:" + strings.Repeat("ab", 32)}
		}, func(o *VerifyOptions) { o.HiddenBlobs = blobStore }, "blob not found"},
		{"no blob store", func(tk *rl.Task) {
			tk.Verifier.Hidden = map[string]string{"h_test.go": "blob:" + string(goodHash)}
		}, nil, "no hidden blob store"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixture(t, tc.mutate)
			w := f.workspace("s0")
			var opts []func(*VerifyOptions)
			if tc.opts != nil {
				opts = append(opts, tc.opts)
			}
			_, err := f.verifyErr(w, opts...)
			if err == nil || !IsInfra(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an infra error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// A blob store that was edited (say by an agent that found it) must not feed
// altered tests to the verifier.
type corruptBlobs struct {
	events.Blobs
	replacement []byte
}

func (c corruptBlobs) Get(h core.Hash) ([]byte, error) { return c.replacement, nil }

func TestVerifyDetectsTamperedHiddenBlob(t *testing.T) {
	store := events.NewMemBlobs()
	h, _ := store.Put([]byte(hiddenTest))
	f := newVerifyFixture(t, func(tk *rl.Task) {
		tk.Verifier.Hidden = map[string]string{"mathx_hidden_test.go": "blob:" + string(h)}
	})
	w := f.workspace("s0")
	fix(t, w)
	// Untampered store works.
	if res := f.verify(w, func(o *VerifyOptions) { o.HiddenBlobs = store }); !res.Pass {
		t.Fatalf("blob-backed hidden file failed:\n%s", res.Log)
	}
	evil := corruptBlobs{Blobs: store, replacement: []byte("package mathx\n")}
	_, err := f.verifyErr(w, func(o *VerifyOptions) { o.HiddenBlobs = evil })
	if err == nil || !IsInfra(err) || !strings.Contains(err.Error(), "does not match its hash") {
		t.Fatalf("got %v", err)
	}
}

type failingSandbox struct{}

func (failingSandbox) Exec(context.Context, string, []string, string, time.Duration) (ExecResult, error) {
	return ExecResult{}, errors.New("container runtime is down")
}

func TestVerifySandboxFailureIsInfra(t *testing.T) {
	r, base := mathxRepo(t)
	// The sandbox only fails for the verifier command, not for setup (none here).
	m := newManager(t, func(o *WorkspaceOptions) { o.Sandbox = failingSandbox{} })
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	_, err = Verify(ctxT(t), mathxTask(r, base), w, VerifyOptions{})
	if err == nil || !IsInfra(err) || !strings.Contains(err.Error(), "container runtime is down") {
		t.Fatalf("got %v", err)
	}
}

// recordingSandbox wraps LocalSandbox and records what it was asked to run.
type recordingSandbox struct {
	inner Sandbox
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	dir, cmd string
	env      []string
	policy   ExecPolicy
}

func (r *recordingSandbox) Exec(ctx context.Context, dir string, env []string, cmd string, timeout time.Duration) (ExecResult, error) {
	pol, _ := ExecPolicyFrom(ctx)
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{dir, cmd, append([]string(nil), env...), pol})
	r.mu.Unlock()
	return r.inner.Exec(ctx, dir, env, cmd, timeout)
}

func TestVerifyUsesTheSandboxHookWithScrubbedEnvAndPolicy(t *testing.T) {
	t.Setenv("HARNESS_SECRET_TOKEN", "placeholder-not-a-secret")
	local, err := NewLocalSandbox(LocalSandboxOptions{DisableNetIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSandbox{inner: local}
	r, base := mathxRepo(t)
	m := newManager(t, func(o *WorkspaceOptions) { o.Sandbox = rec })
	task := mathxTask(r, base)
	task.Verifier.Cmd = `env > "$TMPDIR/env.txt"; cat "$TMPDIR/env.txt"; go test -count=1 ./...`
	w, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	fixIn(t, w)
	res, err := Verify(ctxT(t), task, w, VerifyOptions{})
	if err != nil || !res.Pass {
		t.Fatalf("%v %+v", err, res.Log)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls: %d", len(rec.calls))
	}
	c := rec.calls[0]
	if c.policy.Network || c.policy.Marker == "" {
		t.Errorf("policy: %+v", c.policy)
	}
	if !strings.HasPrefix(c.dir, filepath.Join(m.Root(), "verify")) {
		t.Errorf("verifier ran outside a clean checkout: %s", c.dir)
	}
	envm := EnvMap(c.env)
	if _, ok := envm["HARNESS_SECRET_TOKEN"]; ok {
		t.Error("harness secret reached the verifier")
	}
	if !strings.HasPrefix(envm["HOME"], filepath.Join(m.Root(), "verify")) || envm["GOPROXY"] != "off" {
		t.Errorf("env: HOME=%s GOPROXY=%s", envm["HOME"], envm["GOPROXY"])
	}
	if strings.Contains(res.Log, "HARNESS_SECRET_TOKEN") {
		t.Error("secret name shows up in the verifier's own environment dump")
	}
}

func TestVerifyCancellationStopsTheVerifier(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	f := newVerifyFixture(t, func(tk *rl.Task) {
		tk.Verifier.Cmd = fmt.Sprintf("sleep 100 & echo $! > %s; wait", pidFile)
	})
	w := f.workspace("s0")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		readPID(t, pidFile)
		cancel()
	}()
	start := time.Now()
	_, err := Verify(ctx, f.task, w, VerifyOptions{})
	if !errors.Is(err, context.Canceled) || IsInfra(err) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("cancel was slow")
	}
	waitDead(t, readPID(t, pidFile))
}

// ---- pass modes ----

func TestVerifyPassModes(t *testing.T) {
	tests := []struct {
		name      string
		pass, cmd string
		wantPass  bool
		wantScore float64
	}{
		{"exit0 ok", "", "true", true, 1},
		{"exit0 explicit fail", "exit0", "exit 3", false, 0},
		{"regex match", "regex:^RESULT: all \\d+ passed$", "echo noise; echo 'RESULT: all 12 passed'; exit 5", true, 1},
		{"regex no match", "regex:^RESULT: ok$", "echo 'RESULT: failed'", false, 0},
		{"regex anchors per line", "regex:^ok\\s", "echo header; echo 'ok  pkg'", true, 1},
		{"json full", "json-score", `echo log; echo '{"score": 1}'`, true, 1},
		{"json partial", "json-score", `echo '{"score": 0.5}'`, false, 0.5},
		{"json zero", "json-score", `echo '{"score": 0}'`, false, 0},
		{"json clamps high", "json-score", `echo '{"score": 7}'`, true, 1},
		{"json clamps negative", "json-score", `echo '{"score": -2}'`, false, 0},
		{"json extra keys", "json-score", `echo '{"score": 1, "detail": "x"}'`, true, 1},
		{"json trailing blank lines", "json-score", `printf '{"score": 1}\n\n\n'`, true, 1},
		{"json garbage", "json-score", "echo hello", false, 0},
		{"json missing key", "json-score", `echo '{"pass": true}'`, false, 0},
		{"json score must be the last line", "json-score", `echo '{"score": 1}'; echo 'atexit noise'`, false, 0},
		{"json crash", "json-score", "exit 2", false, 0},
		{"exit 127", "", "definitely-not-a-command", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixture(t, func(tk *rl.Task) { tk.Verifier.Pass = tc.pass; tk.Verifier.Cmd = tc.cmd })
			w := f.workspace("s0")
			res := f.verify(w)
			if res.Pass != tc.wantPass || res.Score != tc.wantScore {
				t.Fatalf("pass=%v score=%v, want %v/%v\n%s", res.Pass, res.Score, tc.wantPass, tc.wantScore, res.Log)
			}
		})
	}
}

func TestVerifyExitCommandNotFoundIsAFailureNotInfra(t *testing.T) {
	// The agent can make a command vanish (delete a script it names); treating
	// that as infra would be an escape hatch from a bad reward.
	f := newVerifyFixture(t, func(tk *rl.Task) { tk.Verifier.Cmd = "./no-such-script.sh" })
	w := f.workspace("s0")
	res := f.verify(w)
	if res.Pass || res.ExitCode != 127 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Log, "not found or not executable") {
		t.Errorf("log lacks the hint:\n%s", res.Log)
	}
}

func TestVerifyExpectForRecallTasks(t *testing.T) {
	mk := func(expect string, cmd string) rl.Task {
		return rl.Task{
			ID: "recall-1", Kind: rl.TaskRecall, Prompt: "what was the constant?",
			Repo:     rl.RepoSpec{Path: "/unused", Commit: "abc"},
			Verifier: rl.Verifier{Cmd: cmd, Expect: json.RawMessage(expect)},
		}
	}
	tests := []struct {
		name      string
		expect    string
		answer    string
		wantPass  bool
		wantScore float64
	}{
		{"all present", `{"contains":["0xDEADBEEF","retry_limit"]}`, "It was retry_limit = 0xDEADBEEF.", true, 1},
		{"partial", `{"contains":["alpha","bravo","charlie","delta"]}`, "alpha then charlie", false, 0.5},
		{"none", `{"contains":["needle"]}`, "haystack", false, 0},
		{"case sensitive by default", `{"contains":["Needle"]}`, "needle", false, 0},
		{"fold", `{"contains":["Needle"],"fold":true}`, "a NEEDLE here", true, 1},
		{"empty answer", `{"contains":["x"]}`, "", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Verify(ctxT(t), mk(tc.expect, ""), nil, VerifyOptions{Answer: tc.answer})
			if err != nil {
				t.Fatal(err)
			}
			if res.Pass != tc.wantPass || res.Score != tc.wantScore || res.Mode != "expect" {
				t.Fatalf("%+v", res)
			}
		})
	}
	t.Run("command and answer must both pass", func(t *testing.T) {
		f := newVerifyFixture(t, func(tk *rl.Task) {
			tk.Verifier.Cmd = "true"
			tk.Verifier.Expect = json.RawMessage(`{"contains":["ok"]}`)
		})
		w := f.workspace("s0")
		if res := f.verify(w, func(o *VerifyOptions) { o.Answer = "all ok" }); !res.Pass {
			t.Fatalf("%+v", res)
		}
		if res := f.verify(w, func(o *VerifyOptions) { o.Answer = "no" }); res.Pass || res.Score != 0 {
			t.Fatalf("%+v", res)
		}
	})
}

// ---- flakiness ----

func TestVerifyRepeatsAndPassPolicies(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "n")
	// Fails on even runs. `state.txt` proves each repeat gets a fresh checkout.
	cmd := fmt.Sprintf(`test ! -e state.txt || exit 9; touch state.txt; n=$(cat %[1]s 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]s; [ $((n %% 2)) -eq 1 ]`, counter)
	f := newVerifyFixture(t, func(tk *rl.Task) { tk.Verifier.Cmd = cmd })
	w := f.workspace("s0")
	tests := []struct {
		policy    string
		wantPass  bool
		wantScore float64
		flaky     bool
	}{
		{"", false, 2.0 / 3, true},      // runs pass, fail, pass under unanimity
		{PassAll, false, 2.0 / 3, true}, // ditto
		{PassMajority, true, 2.0 / 3, true},
		{PassAny, true, 2.0 / 3, true},
	}
	for _, tc := range tests {
		if err := os.RemoveAll(counter); err != nil {
			t.Fatal(err)
		}
		res := f.verify(w, func(o *VerifyOptions) { o.Repeats = 3; o.PassPolicy = tc.policy })
		if len(res.Runs) != 3 {
			t.Fatalf("policy %q: %d runs\n%s", tc.policy, len(res.Runs), res.Log)
		}
		for i, r := range res.Runs {
			if r.ExitCode == 9 {
				t.Fatalf("run %d reused a checkout", i)
			}
		}
		if res.Pass != tc.wantPass || res.Flaky != tc.flaky || diff(res.Score, tc.wantScore) > 1e-9 {
			t.Fatalf("policy %q: pass=%v flaky=%v score=%v", tc.policy, res.Pass, res.Flaky, res.Score)
		}
	}
	// A stable verifier is not flaky.
	f2 := newVerifyFixture(t, func(tk *rl.Task) { tk.Verifier.Cmd = "true" })
	if res := f2.verify(f2.workspace("s"), func(o *VerifyOptions) { o.Repeats = 3 }); !res.Pass || res.Flaky || res.Score != 1 {
		t.Fatalf("%+v", res)
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// ---- baseline and gold ----

func TestVerifyBaseline(t *testing.T) {
	f := newVerifyFixture(t)
	opts := VerifyOptions{Workspaces: f.m}
	res, err := VerifyBaseline(ctxT(t), f.task, opts)
	if err != nil || res.Pass {
		t.Fatalf("a sound task fails on the starting commit: %v %+v", err, res)
	}
	if res.HiddenWritten == nil {
		t.Error("hidden files must be in place for the baseline run")
	}

	passing := f.task
	passing.Verifier.Cmd = "true"
	if _, err := VerifyBaseline(ctxT(t), passing, opts); !errors.Is(err, ErrBaselinePasses) {
		t.Fatalf("got %v", err)
	}
	notFound := f.task
	notFound.Verifier.Cmd = "no-such-tool-anywhere"
	if _, err := VerifyBaseline(ctxT(t), notFound, opts); !errors.Is(err, ErrBaselineBroken) {
		t.Fatalf("got %v", err)
	}
	hang := f.task
	hang.Verifier.Cmd = "sleep 60"
	hang.Verifier.TimeoutS = 1
	if _, err := VerifyBaseline(ctxT(t), hang, opts); !errors.Is(err, ErrBaselineBroken) {
		t.Fatalf("got %v", err)
	}
	if _, err := VerifyBaseline(ctxT(t), f.task, VerifyOptions{}); err == nil || !IsInfra(err) {
		t.Fatalf("missing manager should be an error: %v", err)
	}
}

func TestCheckTaskWithGoldPatch(t *testing.T) {
	f := newVerifyFixture(t)
	f.r.write("mathx.go", fixedMath)
	fixCommit := f.r.commit("fix")
	gold := f.r.git("diff", "--binary", "--full-index", "--no-renames", f.base, fixCommit, "--", "mathx.go") + "\n"
	rep, err := CheckTask(ctxT(t), f.task, []byte(gold), VerifyOptions{Workspaces: f.m})
	if err != nil {
		t.Fatalf("%v\nbaseline: %s\ngold: %s", err, rep.Baseline.Log, rep.Gold.Log)
	}
	if rep.Baseline.Pass || !rep.Gold.Pass {
		t.Fatalf("baseline %v gold %v", rep.Baseline.Pass, rep.Gold.Pass)
	}
	// A reference solution that does not solve the task is reported.
	wrong := strings.Replace(gold, "return a\n", "return b\n", 1)
	if _, err := CheckTask(ctxT(t), f.task, []byte(wrong), VerifyOptions{Workspaces: f.m}); err == nil {
		t.Fatal("bogus gold accepted")
	}
	if _, err := CheckTask(ctxT(t), f.task, nil, VerifyOptions{Workspaces: f.m}); !errors.Is(err, ErrGoldFails) {
		t.Fatalf("an empty gold patch must fail: %v", err)
	}
}

func TestVerifyPatchMatchesLiveDiff(t *testing.T) {
	f := newVerifyFixture(t)
	w := f.workspace("s0")
	fix(t, w)
	mustWrite(t, filepath.Join(w.Root, "mathx_test.go"), "package mathx\n") // protected edit
	live := f.verify(w)
	// Re-verifying the stored diff.patch gives the same verdict: runs are
	// reproducible from their artifacts.
	again, err := VerifyPatch(ctxT(t), f.task, live.Diff, VerifyOptions{Workspaces: f.m})
	if err != nil {
		t.Fatal(err)
	}
	if again.Pass != live.Pass || again.Score != live.Score || !reflect.DeepEqual(again.ProtectedTouched, live.ProtectedTouched) {
		t.Fatalf("live %+v\nagain %+v", live, again)
	}
}

func TestVerifyAfterSnapshotTamperingUsesAFreshSnapshot(t *testing.T) {
	f := newVerifyFixture(t)
	w := f.workspace("s0")
	fix(t, w)
	// While the agent worked, something rewrote the cached go.mod: verification
	// checkouts come from the snapshot, so a verdict must not be built on it.
	mustWrite(t, filepath.Join(w.snap.tree, "mathx.go"), "package mathx // poisoned\n")
	res := f.verify(w)
	if !res.Pass {
		t.Fatalf("verification should have rebuilt a clean snapshot:\n%s", res.Log)
	}
}

func TestVerifyConcurrent(t *testing.T) {
	f := newVerifyFixture(t)
	const n = 5
	var wg sync.WaitGroup
	results := make([]Result, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		w := f.workspace(fmt.Sprintf("s%d", i))
		if i%2 == 0 {
			fix(t, w)
		}
		wg.Add(1)
		go func(i int, w *Workspace) {
			defer wg.Done()
			results[i], errs[i] = Verify(ctxT(t), f.task, w, VerifyOptions{})
		}(i, w)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if want := i%2 == 0; results[i].Pass != want {
			t.Errorf("sample %d: pass=%v want %v", i, results[i].Pass, want)
		}
	}
}

func TestVerifyExecutableHiddenScript(t *testing.T) {
	f := newVerifyFixture(t, func(tk *rl.Task) {
		tk.Verifier.Cmd = "./check.sh"
		tk.Verifier.Hidden = map[string]string{"check.sh": "text:#!/bin/sh\nsed -n 6p mathx.go | grep -q 'return a'\n"}
	})
	w := f.workspace("s0")
	if res := f.verify(w); res.Pass {
		t.Fatal("bug still present")
	}
	fix(t, w)
	if res := f.verify(w); !res.Pass {
		t.Fatalf("%s", res.Log)
	}
}

func TestVerifyHiddenFilesAreNotInTheAgentsDiffOrRunDir(t *testing.T) {
	f := newVerifyFixture(t)
	w := f.workspace("s0")
	fix(t, w)
	out := t.TempDir()
	res := f.verify(w, func(o *VerifyOptions) { o.OutDir = out })
	if strings.Contains(string(res.Diff), "TestMax") {
		t.Fatal("hidden test content appears in diff.patch")
	}
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		if strings.Contains(e.Name(), "hidden") {
			t.Fatalf("hidden file copied into the output dir: %s", e.Name())
		}
	}
}

func TestVerifierVersion(t *testing.T) {
	a := mathxTask(&fixtureRepo{Dir: "/x"}, "abc")
	b := a
	if VerifierVersion(a) != VerifierVersion(b) {
		t.Fatal("not stable")
	}
	mods := map[string]func(*rl.Task){
		"cmd":       func(t *rl.Task) { t.Verifier.Cmd += " -v" },
		"pass":      func(t *rl.Task) { t.Verifier.Pass = "json-score" },
		"timeout":   func(t *rl.Task) { t.Verifier.TimeoutS++ },
		"hidden":    func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"mathx_hidden_test.go": "text:other"} },
		"protected": func(t *rl.Task) { t.Verifier.Protected = append([]string{"x"}, t.Verifier.Protected...) },
		"expect":    func(t *rl.Task) { t.Verifier.Expect = json.RawMessage(`{"contains":["x"]}`) },
	}
	for name, mod := range mods {
		c := a
		c.Verifier.Hidden = map[string]string{"mathx_hidden_test.go": "text:" + hiddenTest}
		base := VerifierVersion(c)
		mod(&c)
		if VerifierVersion(c) == base {
			t.Errorf("changing %s does not change the version", name)
		}
	}
	// Map order and unrelated fields do not matter.
	c := a
	c.Prompt = "different"
	c.Tags = []string{"x"}
	if VerifierVersion(c) != VerifierVersion(a) {
		t.Error("version depends on the prompt or tags")
	}
}

func TestVerifierFilesTouched(t *testing.T) {
	task := rl.Task{Verifier: rl.Verifier{
		Cmd:    `cd sub && ./run_tests.sh --flag "check.py" ; make test`,
		Hidden: map[string]string{"h/hidden_test.go": "text:x"},
	}}
	got := verifierFilesTouched(task, []string{"run_tests.sh", "check.py", "h/HIDDEN_test.go", "Makefile", "other_test.go"})
	want := []string{"run_tests.sh", "check.py", "h/HIDDEN_test.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if verifierFilesTouched(task, nil) != nil {
		t.Fatal("nothing touched")
	}
}

func TestVerifyRejectsAnInvalidTaskAsInfra(t *testing.T) {
	f := newVerifyFixture(t)
	w := f.workspace("s0")
	bad := f.task
	bad.Verifier.Pass = "bogus"
	if _, err := Verify(ctxT(t), bad, w, VerifyOptions{}); err == nil || !IsInfra(err) || !strings.Contains(err.Error(), "verifier.pass") {
		t.Fatalf("got %v", err)
	}
}
