package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The benchmark runner (scripts/bench.sh) is what spends real money unattended for hours, so its loop, its caps and its
// handling of the key are tested here against a stand-in for the binary: a shell script that records how it was called and
// answers from a plan ("75,infra,ok": exit 75 the first time, then one infrastructure failure, then everything done).

const benchKey = "sk-bench-test-0123456789abcdef0123456789abcdef"

const stubSleipnir = `#!/bin/sh
state=$STUB_STATE
n=$(cat "$state/n" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$state/n"
{ echo "--- $STUB_NAME call $n cwd=$(pwd -P)"; printf '%s\n' "$@"; [ -n "${HEIMDALL_API_KEY:-}" ] && echo "key-in-env"; } >> "$state/calls"
out=; prev=; for a in "$@"; do [ "$prev" = --out ] && out=$a; prev=$a; done
seed=; prev=; for a in "$@"; do [ "$prev" = --seed ] && seed=$a; prev=$a; done
echo "$STUB_NAME seed=$seed" >> "$state/order"
mkdir -p "$out"
plan=$(echo "$STUB_PLAN" | cut -d, -f"$n"); [ -n "$plan" ] || plan=$(echo "$STUB_PLAN" | awk -F, '{print $NF}')
summary() { printf '{"rollouts":4,"completed":%s,"infra":%s,"cancelled":0,"capped":%s,"pass_rate":0.5,"spent_usd":0.0123}\n' "$1" "$2" "$3" > "$out/summary.json"; }
case "$plan" in
  75) exit 75 ;;
  fail) echo "sleipnir: boom" >&2; exit 1 ;;
  infra) summary 3 1 0; exit 0 ;;
  capped) summary 2 0 2; exit 0 ;;
  ok) summary 4 0 0; exit 0 ;;
esac
exit 9
`

type benchEnv struct {
	t      *testing.T
	home   string
	suite  string
	state  string
	stub   string
	script string
}

func newBenchEnv(t *testing.T) *benchEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script")
	}
	for _, tool := range []string{"sh", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
	e := &benchEnv{t: t, home: t.TempDir(), suite: filepath.Join(t.TempDir(), "suite"), state: t.TempDir()}
	for _, d := range []string{filepath.Join(e.suite, "blobs"), filepath.Join(e.home, "runs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.suite, "tasks.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.stub = e.makeStub("stub")
	abs, err := filepath.Abs(filepath.Join("..", "..", "scripts", "bench.sh"))
	if err != nil {
		t.Fatal(err)
	}
	e.script = abs
	return e
}

func (e *benchEnv) makeStub(name string) string {
	p := filepath.Join(e.t.TempDir(), name)
	if err := os.WriteFile(p, []byte(strings.Replace(stubSleipnir, `$STUB_NAME`, name, -1)), 0o755); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// run calls the script; it returns everything the script printed, the exit status, and what the stub recorded.
func (e *benchEnv) run(plan string, args ...string) (out string, status int, calls string) {
	e.t.Helper()
	full := append([]string{e.script}, args...)
	cmd := exec.Command("sh", full...)
	cmd.Env = append(os.Environ(), "BENCH_HOME="+e.home, "BENCH_PAUSE=0", "STUB_STATE="+e.state, "STUB_PLAN="+plan, "HEIMDALL_API_KEY="+benchKey)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	status = 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(e.state, "calls"))
	return buf.String(), status, string(b)
}

func (e *benchEnv) base(extra ...string) []string {
	return append([]string{"run", "--model", "heimdall/test/model", "--suite", e.suite, "--bin", e.stub, "--min-free-gb", "0"}, extra...)
}

func noKey(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, benchKey) {
		t.Errorf("the API key appears in %s", where)
	}
}

func TestBenchScriptResumesUntilEverythingHasAVerdict(t *testing.T) {
	e := newBenchEnv(t)
	run := filepath.Join(t.TempDir(), "run")
	out, status, calls := e.run("75,infra,ok", e.base("--out", run, "--tag", "smoke,!long", "--concurrency", "3")...)
	if status != 0 {
		t.Fatalf("exit %d\n%s", status, out)
	}
	if got := strings.Count(calls, "call "); got != 3 {
		t.Fatalf("%d invocations, want 3 (exit 75, one infrastructure failure, done)\n%s", got, calls)
	}
	for _, want := range []string{"rl\nrollout\n", "--tasks\ntasks.jsonl\n", "--blobs\nblobs\n", "--model\nheimdall/test/model\n", "--group\n1\n",
		"--tag\nsmoke,!long\n", "--concurrency\n3\n", "--rpm\n120\n", "--budget-usd\n0.25\n", "--max-spend-usd\n2\n", "--set-env\nGOFLAGS=-mod=mod\n", "key-in-env"} {
		if !strings.Contains(calls, want) {
			t.Errorf("the stub was not called with %q:\n%s", strings.ReplaceAll(want, "\n", " "), calls)
		}
	}
	// Every invocation resumes the same run directory, from the suite directory (its repositories are relative to it).
	if got := strings.Count(calls, "--out\n"+run+"\n"); got != 3 {
		t.Errorf("%d invocations used --out %s, want 3", got, run)
	}
	wantCwd, _ := filepath.EvalSymlinks(e.suite)
	if !strings.Contains(calls, "cwd="+wantCwd) {
		t.Errorf("the run did not start from the suite directory %s:\n%s", wantCwd, calls)
	}
	noKey(t, "the script's output", out)
	noKey(t, "the arguments", calls)
	if b, err := os.ReadFile(filepath.Join(run, "bench.log")); err == nil {
		noKey(t, "bench.log", string(b))
	}
	if !strings.Contains(out, "4/4 done") {
		t.Errorf("the last status line should say everything is done:\n%s", out)
	}
}

func TestBenchScriptStopsWhenTheSpendCapIsReached(t *testing.T) {
	e := newBenchEnv(t)
	out, status, calls := e.run("capped", e.base("--out", filepath.Join(t.TempDir(), "run"))...)
	if status != 3 {
		t.Fatalf("exit %d, want 3\n%s", status, out)
	}
	if strings.Count(calls, "call ") != 1 {
		t.Errorf("a capped run must not be retried:\n%s", calls)
	}
}

func TestBenchScriptGivesUpAfterTheAttempts(t *testing.T) {
	e := newBenchEnv(t)
	out, status, calls := e.run("75", e.base("--out", filepath.Join(t.TempDir(), "run"), "--attempts", "3")...)
	if status != 75 || strings.Count(calls, "call ") != 3 {
		t.Fatalf("exit %d after %d invocations, want 75 after 3\n%s", status, strings.Count(calls, "call "), out)
	}
}

func TestBenchScriptDoesNotRetryARunThatCannotStart(t *testing.T) {
	e := newBenchEnv(t)
	out, status, calls := e.run("fail", e.base("--out", filepath.Join(t.TempDir(), "run"))...)
	if status != 2 || strings.Count(calls, "call ") != 1 {
		t.Fatalf("exit %d after %d invocations, want 2 after 1\n%s", status, strings.Count(calls, "call "), out)
	}
}

func TestBenchScriptRefusesAKeyFileOthersCanRead(t *testing.T) {
	e := newBenchEnv(t)
	keyFile := filepath.Join(t.TempDir(), "key.env")
	if err := os.WriteFile(keyFile, []byte("export HEIMDALL_API_KEY="+benchKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, status, calls := e.run("ok", e.base("--key-file", keyFile)...)
	if status != 2 || !strings.Contains(out, "0600") || calls != "" {
		t.Fatalf("exit %d, calls %q\n%s", status, calls, out)
	}
	noKey(t, "the refusal", out)

	if err := os.Chmod(keyFile, 0o600); err != nil {
		t.Fatal(err)
	}
	// With the right mode the key reaches the stub through the environment, and only there.
	cmd := exec.Command("sh", append([]string{e.script}, e.base("--key-file", keyFile, "--out", filepath.Join(t.TempDir(), "run"))...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "BENCH_HOME=" + e.home, "BENCH_PAUSE=0", "STUB_STATE=" + e.state, "STUB_PLAN=ok"}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	b, _ := os.ReadFile(filepath.Join(e.state, "calls"))
	if !strings.Contains(string(b), "key-in-env") {
		t.Errorf("the key from --key-file did not reach the stub's environment:\n%s", b)
	}
	noKey(t, "the output", buf.String())
	noKey(t, "the arguments", string(b))
}

func TestBenchScriptNeedsAKey(t *testing.T) {
	e := newBenchEnv(t)
	cmd := exec.Command("sh", append([]string{e.script}, e.base()...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "BENCH_HOME=" + e.home, "STUB_STATE=" + e.state}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || !strings.Contains(buf.String(), "no API key") {
		t.Fatalf("%v\n%s", err, buf.String())
	}
}

func TestBenchScriptABAlternatesTheBinariesAndPairsTheSeeds(t *testing.T) {
	e := newBenchEnv(t)
	a, b := e.makeStub("A"), e.makeStub("B")
	out, status, _ := e.run("ok", "ab", "--model", "heimdall/test/model", "--suite", e.suite, "--bin-a", a, "--bin-b", b, "--min-free-gb", "0",
		"--group", "3", "--out", filepath.Join(t.TempDir(), "ab"))
	if status != 0 {
		t.Fatalf("exit %d\n%s", status, out)
	}
	order, _ := os.ReadFile(filepath.Join(e.state, "order"))
	// Round 1 runs A then B, round 2 B then A, round 3 A then B; both arms of a round share its seed.
	want := "A seed=1\nB seed=1\nB seed=2\nA seed=2\nA seed=3\nB seed=3\n"
	if string(order) != want {
		t.Errorf("order of runs:\n%s\nwant:\n%s", order, want)
	}
}

func TestBenchScriptStatus(t *testing.T) {
	e := newBenchEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"rollouts":8,"completed":6,"infra":2,"capped":0,"pass_rate":0.5,"spent_usd":0.0123}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, status, _ := e.run("ok", "status", dir, t.TempDir())
	if status != 0 || !strings.Contains(out, "6/8 done, 2 infra, 0 capped, pass 50%, spent $0.0123") || !strings.Contains(out, "no summary yet") {
		t.Fatalf("exit %d\n%s", status, out)
	}
}
