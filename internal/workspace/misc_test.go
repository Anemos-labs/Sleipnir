package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/gitx"
)

func TestTailBuffer(t *testing.T) {
	// exactly at the cap: nothing elided
	b := newTailBuffer(1000)
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 10; i++ {
		b.Write([]byte(line))
	}
	out, trunc := b.String()
	if trunc || len(out) != 1000 || strings.Contains(out, "omitted") {
		t.Fatalf("at the cap: %d bytes truncated=%v", len(out), trunc)
	}
	// over the cap: head and tail kept, the count of the middle is exact
	b = newTailBuffer(1000)
	total := 0
	for i := 0; i < 500; i++ {
		l := fmt.Sprintf("line %04d\n", i)
		total += len(l)
		b.Write([]byte(l))
	}
	out, trunc = b.String()
	if !trunc || !strings.HasPrefix(out, "line 0000\n") || !strings.HasSuffix(out, "line 0499\n") {
		t.Fatalf("head/tail: %.60q ... %q", out, out[len(out)-20:])
	}
	var omitted int
	if _, err := fmt.Sscanf(out[strings.Index(out, "[... "):], "[... %d bytes omitted ...]", &omitted); err != nil {
		t.Fatalf("no omitted marker in %q", out)
	}
	kept := len(out) - len(fmt.Sprintf("\n[... %d bytes omitted ...]\n", omitted))
	if kept+omitted != total {
		t.Fatalf("kept %d + omitted %d != written %d", kept, omitted, total)
	}
	if len(out) > 1000+60 {
		t.Fatalf("output %d bytes for a cap of 1000", len(out))
	}
	// one huge write, and writes that split multi-byte characters
	b = newTailBuffer(64)
	b.Write([]byte(strings.Repeat("日本語", 1000)))
	out, _ = b.String()
	if !utf8.ValidString(out) {
		t.Fatalf("invalid UTF-8 in %q", out)
	}
	// concurrent writers (stdout and stderr share one buffer)
	b = newTailBuffer(4096)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				b.Write([]byte("goroutine output line\n"))
			}
		}()
	}
	wg.Wait()
	if out, trunc := b.String(); !trunc || len(out) > 4096+80 {
		t.Fatalf("concurrent: %d bytes truncated=%v", len(out), trunc)
	}
}

func TestVerifyResultSummaries(t *testing.T) {
	for _, c := range []struct {
		r    VerifyResult
		ok   bool
		want string
	}{
		{VerifyResult{Cmd: "make test"}, true, "passed"},
		{VerifyResult{Cmd: "make test", ExitCode: 2}, false, "failed (exit 2)"},
		{VerifyResult{Cmd: "make test", TimedOut: true, ExitCode: -1}, false, "timed out"},
		{VerifyResult{Cmd: "make test", Err: errors.New("boom")}, false, "could not run"},
	} {
		if c.r.OK() != c.ok || !strings.Contains(c.r.Summary(), c.want) {
			t.Errorf("%+v: OK=%v summary=%q", c.r, c.r.OK(), c.r.Summary())
		}
	}
}

func TestRunShellBasics(t *testing.T) {
	skipWithoutUnix(t)
	dir := t.TempDir()
	res := RunShell(tctx(t), VerifyRequest{Dir: dir, Cmd: "echo out; echo err >&2; exit 5", Agent: "a", Task: "t"})
	if res.ExitCode != 5 || res.Err != nil || res.TimedOut || !strings.Contains(res.Output, "out") || !strings.Contains(res.Output, "err") {
		t.Fatalf("%+v", res)
	}
	if res := RunShell(tctx(t), VerifyRequest{Dir: dir, Cmd: "   "}); res.Err == nil || res.OK() {
		t.Fatalf("empty command: %+v", res)
	}
	if res := RunShell(tctx(t), VerifyRequest{Dir: filepath.Join(dir, "missing"), Cmd: "true"}); res.Err == nil || res.OK() {
		t.Fatalf("missing directory: %+v", res)
	}
	if res := RunShell(tctx(t), VerifyRequest{Dir: dir, Cmd: "pwd"}); !res.OK() || strings.TrimSpace(res.Output) != dir {
		t.Fatalf("pwd: %+v", res)
	}
	// stdin is closed: a command that reads it does not hang
	if res := RunShell(tctx(t), VerifyRequest{Dir: dir, Cmd: "cat >/dev/null; echo done"}); !res.OK() {
		t.Fatalf("stdin: %+v", res)
	}
}

func TestEmitToAdaptsAnEventLog(t *testing.T) {
	log := events.NewMemLog()
	fn := EmitTo(log)
	fn.emit(EventMerged, "be-1", "T-9", map[string]any{"tip": "abc"})
	fn.emit(EventPrune, "", "", nil)
	evs := log.All()
	if len(evs) != 2 || evs[0].Type != EventMerged || evs[0].Agent != "be-1" || !bytes.Contains(evs[0].Data, []byte(`"task":"T-9"`)) || !bytes.Contains(evs[0].Data, []byte(`"tip":"abc"`)) {
		t.Fatalf("events: %+v", evs)
	}
	if EmitTo(nil) != nil {
		t.Fatal("EmitTo(nil) should be nil")
	}
	var nilFn EventFunc
	nilFn.emit("x", "", "", nil) // a nil sink is a no-op, not a panic
}

func TestProcStatParsing(t *testing.T) {
	if runtimeGOOS() == "windows" {
		t.Skip()
	}
	// our own process: alive, non-zero start time where /proc exists
	if !pidExists(os.Getpid()) {
		t.Fatal("own pid reported dead")
	}
	if pidExists(deadPID(t)) {
		t.Fatal("dead pid reported alive")
	}
	if pidExists(0) || pidExists(-1) {
		t.Fatal("invalid pids")
	}
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		if procStart(os.Getpid()) == 0 {
			t.Fatal("no start time for our own process")
		}
	}
	// a marker of a live process with matching start is alive; reused pid (start differs) is dead
	mk := &marker{PID: os.Getpid(), Start: procStart(os.Getpid()), BootID: bootID()}
	if mk.owner() != ownerAlive {
		t.Fatal("own marker not alive")
	}
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		mk.Start++
		if mk.owner() != ownerDead {
			t.Fatal("reused pid must read as dead")
		}
	}
	if (&marker{PID: 0}).owner() != ownerDead {
		t.Fatal("pid 0")
	}
	if (&marker{PID: os.Getpid(), BootID: "elsewhere"}).owner() != ownerUnknown {
		t.Fatal("other boot")
	}
}

func TestCorruptMarkersMakeATreeForeign(t *testing.T) {
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	a := mustCreate(t, m, "a")
	admin := a.repo.GitDir()
	orphan(t, a)
	for name, content := range map[string]string{
		"garbage":       "not json at all",
		"empty":         "",
		"wrong version": `{"v":99,"prefix":"sleipnir/s1","agent":"a","path":"` + a.Path + `"}`,
		"no agent":      `{"v":1,"prefix":"sleipnir/s1","path":"` + a.Path + `"}`,
		"other prefix":  `{"v":1,"prefix":"someone/else","agent":"a","path":"` + a.Path + `","pid":1}`,
		"other path":    `{"v":1,"prefix":"sleipnir/s1","agent":"a","path":"/somewhere/else","pid":1}`,
	} {
		must(t, os.WriteFile(filepath.Join(admin, markerFile), []byte(content), 0o600))
		if err := a.Remove(tctx(t), true); !errors.Is(err, ErrForeign) {
			t.Errorf("%s: Remove = %v, want ErrForeign", name, err)
		}
		fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
		rep, err := fresh.Prune(tctx(t), PruneOptions{Force: true})
		if err != nil || len(rep.Removed) != 0 || !exists(a.Path) {
			t.Errorf("%s: Prune removed a tree with a bad marker: %+v %v", name, rep, err)
		}
		if list, _ := fresh.List(tctx(t)); len(list) != 0 {
			t.Errorf("%s: List shows %+v", name, list)
		}
	}
}

func TestManagerOverALinkedWorktree(t *testing.T) {
	// the user runs the harness from a linked worktree of their own repository
	dir := newRepo(t)
	userWT := filepath.Join(t.TempDir(), "user-worktree")
	rawGit(t, dir, "worktree", "add", "-q", "-b", "feature", userWT)
	repo := openRepo(t, userWT)
	if !repo.IsLinked() {
		t.Fatal("not linked")
	}
	m := newManager(t, repo)
	q1 := mustQueue(t, m, QueueOptions{})
	a := mustCreate(t, m, "a", CreateOptions{Base: q1.Tip()})
	edit(t, a, "README.md", "# from a linked worktree\n")
	if r := mustSubmit(t, q1, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("%+v", r)
	}
	ff, err := q1.FastForward(tctx(t))
	if err != nil || ff.Branch != "feature" {
		t.Fatalf("FastForward: %+v, %v", ff, err)
	}
	if readFile(t, filepath.Join(userWT, "README.md")) != "# from a linked worktree\n" {
		t.Fatal("the user's worktree did not follow")
	}
	if readFile(t, filepath.Join(dir, "README.md")) != "# project\n" {
		t.Fatal("the main checkout was touched")
	}
	must(t, a.Remove(tctx(t), false))
	list, _ := m.List(tctx(t))
	for _, i := range list {
		if strings.Contains(i.Path, "user-worktree") {
			t.Fatal("the user's worktree was listed as ours")
		}
	}
}

// TestManagersInSeveralProcessesShareARepository re-executes the test binary as
// child processes that each create trees, commit and remove them in the same
// repository at the same time: git's own lock files are what stands between them.
func TestManagersInSeveralProcessesShareARepository(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	skipWithoutUnix(t)
	if os.Getenv("SLEIPNIR_WORKSPACE_CHILD") != "" {
		return
	}
	dir := newRepo(t)
	trees := t.TempDir()
	const procs, perProc = 4, 6
	var wg sync.WaitGroup
	outs := make([][]byte, procs)
	errs := make([]error, procs)
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestWorkspaceChildProcess$", "-test.count=1")
			cmd.Env = append(fixtureEnv(t.TempDir()),
				"SLEIPNIR_WORKSPACE_CHILD=1", "SLEIPNIR_CHILD_REPO="+dir, "SLEIPNIR_CHILD_DIR="+filepath.Join(trees, fmt.Sprintf("p%d", i)),
				fmt.Sprintf("SLEIPNIR_CHILD_ID=%d", i), fmt.Sprintf("SLEIPNIR_CHILD_N=%d", perProc))
			outs[i], errs[i] = cmd.CombinedOutput()
		}()
	}
	wg.Wait()
	for i := range outs {
		if errs[i] != nil {
			t.Fatalf("child %d: %v\n%s", i, errs[i], outs[i])
		}
	}
	// the repository is consistent and nothing was left behind
	rawGit(t, dir, "fsck", "--no-dangling")
	repo := openRepo(t, dir)
	wts, _ := repo.Worktrees(tctx(t))
	if len(wts) != 1 {
		t.Fatalf("worktrees left: %+v", wts)
	}
	if br, _ := repo.Branches(tctx(t), "sleipnir/"); len(br) != 0 {
		t.Fatalf("branches left: %+v", br)
	}
}

func TestWorkspaceChildProcess(t *testing.T) {
	if os.Getenv("SLEIPNIR_WORKSPACE_CHILD") == "" {
		t.Skip("helper for TestManagersInSeveralProcessesShareARepository")
	}
	repo, err := gitx.Open(os.Getenv("SLEIPNIR_CHILD_REPO"), gitx.WithHermeticConfig(), gitx.WithLockWait(30_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	id := os.Getenv("SLEIPNIR_CHILD_ID")
	var n int
	fmt.Sscanf(os.Getenv("SLEIPNIR_CHILD_N"), "%d", &n)
	m := &Manager{Repo: repo, Dir: os.Getenv("SLEIPNIR_CHILD_DIR"), Prefix: "sleipnir/proc" + id, Clock: testClock()}
	for i := 0; i < n; i++ {
		tr, err := m.Create(tctx(t), fmt.Sprintf("w-%d", i), CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		edit(t, tr, "work.txt", fmt.Sprintf("proc %s worker %d\n", id, i))
		if _, err := tr.Commit(tctx(t), "work"); err != nil {
			t.Fatal(err)
		}
		if err := tr.Remove(tctx(t), true); err != nil {
			t.Fatal(err)
		}
	}
}
