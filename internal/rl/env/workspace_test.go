package env

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/rl"
)

func TestPrepareCreatesIsolatedHistoryFreeWorkspace(t *testing.T) {
	r, base := mathxRepo(t)
	// A later commit (the "future"): must never be reachable from the workspace.
	r.write("mathx.go", fixedMath)
	r.commit("fix Max")
	m := newManager(t)
	task := mathxTask(r, base)
	w, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()

	if got := treeFiles(t, w.Root); !reflect.DeepEqual(got, []string{".gitignore", "README.md", "go.mod", "mathx.go", "mathx_test.go"}) {
		t.Fatalf("files = %v", got)
	}
	if got := mustRead(t, filepath.Join(w.Root, "mathx.go")); got != buggyMath {
		t.Fatalf("workspace does not hold the starting commit's content:\n%s", got)
	}
	if !strings.HasPrefix(w.Root, m.Root()) {
		t.Fatalf("workspace %s is outside the root %s", w.Root, m.Root())
	}
	if w.Commit != base {
		t.Errorf("Commit = %s, want %s", w.Commit, base)
	}

	// The agent can use git normally, on a single-commit history.
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = w.Dir
		cmd.Env = w.Env()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if n := run("rev-list", "--all", "--count"); n != "1" {
		t.Errorf("workspace history has %s commits, want 1", n)
	}
	if out := run("status", "--porcelain"); out != "" {
		t.Errorf("fresh workspace is dirty:\n%s", out)
	}
}

func TestWorkspaceHasNoFutureObjects(t *testing.T) {
	r, base := mathxRepo(t)
	r.write("mathx.go", fixedMath)
	future := r.commit("fix Max")
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	cmd := exec.Command("git", "cat-file", "-e", future)
	cmd.Dir = w.Dir
	cmd.Env = w.Env()
	if err := cmd.Run(); err == nil {
		t.Fatalf("the future commit %s exists in the workspace repository", future)
	}
	cmd = exec.Command("git", "log", "--all", "--oneline")
	cmd.Dir = w.Dir
	cmd.Env = w.Env()
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Fatalf("history: %q %v", out, err)
	}
}

func TestPrepareCloneModeKeepsHistory(t *testing.T) {
	r, base := mathxRepo(t)
	r.write("mathx.go", fixedMath)
	r.commit("fix Max")
	m := newManager(t, func(o *WorkspaceOptions) { o.Mode = ModeClone })
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "c0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	if got := mustRead(t, filepath.Join(w.Root, "mathx.go")); got != buggyMath {
		t.Fatalf("clone is not at the requested commit")
	}
	cmd := exec.Command("git", "rev-list", "--all", "--count")
	cmd.Dir = w.Dir
	cmd.Env = w.Env()
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "2" {
		t.Fatalf("clone mode should include the future: %q %v", out, err)
	}
	// Diffs work in clone mode too.
	mustWrite(t, filepath.Join(w.Dir, "mathx.go"), fixedMath)
	d, err := w.Diff(ctxT(t), 0)
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "mathx.go" {
		t.Fatalf("diff: %+v %v", d, err)
	}
}

func TestSetupRunsOnceAndIsSharedByAllSamples(t *testing.T) {
	r, base := mathxRepo(t)
	counter := filepath.Join(t.TempDir(), "setup-count")
	task := mathxTask(r, base)
	// The setup appends to a counter file outside the workspace and leaves a
	// generated file plus a cache entry in the tool home.
	task.Setup = []string{
		fmt.Sprintf("echo run >> %s", counter),
		"echo generated > generated.txt",
		`mkdir -p "$HOME/.cache" && echo warm > "$HOME/.cache/marker"`,
		"sleep 0.3", // widen the window in which concurrent callers must wait for one build
	}
	m := newManager(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wss []*Workspace
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, err := m.Prepare(ctxT(t), task, fmt.Sprintf("s%d", i))
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			wss = append(wss, w)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := strings.Count(mustRead(t, counter), "run"); got != 1 {
		t.Fatalf("setup ran %d times for 8 concurrent samples, want 1", got)
	}
	seen := map[string]bool{}
	for _, w := range wss {
		if seen[w.Root] {
			t.Fatalf("two samples share %s", w.Root)
		}
		seen[w.Root] = true
		if got := mustRead(t, filepath.Join(w.Root, "generated.txt")); got != "generated\n" {
			t.Errorf("setup output missing from %s", w.Root)
		}
		if got := mustRead(t, filepath.Join(w.Home, ".cache", "marker")); got != "warm\n" {
			t.Errorf("tool home cache missing from %s", w.Home)
		}
		// Setup's changes are part of the baseline, not of the agent's diff.
		d, err := w.Diff(ctxT(t), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Patch) != 0 || len(d.Files) != 0 {
			t.Errorf("setup output shows up in the agent's diff:\n%s", d.Patch)
		}
	}
	// Editing one sample leaves the others (and the cache) alone.
	mustWrite(t, filepath.Join(wss[0].Root, "mathx.go"), "package mathx\n")
	for _, w := range wss[1:] {
		if mustRead(t, filepath.Join(w.Root, "mathx.go")) != buggyMath {
			t.Fatal("samples are not isolated from each other")
		}
	}
	// A later Prepare reuses the cache: still one setup run.
	w, err := m.Prepare(ctxT(t), task, "later")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(mustRead(t, counter), "run"); got != 1 {
		t.Fatalf("setup re-ran for a later sample (%d runs)", got)
	}
	if mustRead(t, filepath.Join(w.Root, "mathx.go")) != buggyMath {
		t.Fatal("the cached snapshot was modified through a workspace")
	}
	// A different setup is a different snapshot.
	task2 := task
	task2.Setup = []string{fmt.Sprintf("echo run2 >> %s", counter)}
	if _, err := m.Prepare(ctxT(t), task2, "x"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(mustRead(t, counter), "run"); got != 2 {
		t.Fatalf("changed setup should build a new snapshot: %d runs", got)
	}
}

func TestSetupFailureIsInfraWithOutput(t *testing.T) {
	r, base := mathxRepo(t)
	task := mathxTask(r, base)
	task.Setup = []string{"echo compiling >&2; echo dependency-missing >&2; exit 3"}
	m := newManager(t)
	_, err := m.Prepare(ctxT(t), task, "s0")
	if err == nil || !IsInfra(err) {
		t.Fatalf("want an infra error, got %v", err)
	}
	var se *SetupError
	if !asSetupError(err, &se) || se.ExitCode != 3 || !strings.Contains(se.Output, "dependency-missing") {
		t.Fatalf("setup error lacks details: %v", err)
	}
	// Nothing half-built is left behind in the snapshot area.
	ents, _ := os.ReadDir(filepath.Join(m.Root(), "snaps"))
	if len(ents) != 0 {
		t.Errorf("failed setup left %d entries in snaps/", len(ents))
	}
}

func asSetupError(err error, target **SetupError) bool {
	for err != nil {
		if se, ok := err.(*SetupError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestSetupTimeout(t *testing.T) {
	r, base := mathxRepo(t)
	task := mathxTask(r, base)
	task.Setup = []string{"sleep 30"}
	m := newManager(t, func(o *WorkspaceOptions) {
		o.SetupTimeout = 300 * time.Millisecond
		o.KillGrace = 100 * time.Millisecond
	})
	start := time.Now()
	_, err := m.Prepare(ctxT(t), task, "s0")
	var se *SetupError
	if err == nil || !asSetupError(err, &se) || !se.TimedOut {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("setup timeout was not enforced")
	}
}

func TestFailedSetupIsRememberedBriefly(t *testing.T) {
	r, base := mathxRepo(t)
	counter := filepath.Join(t.TempDir(), "runs")
	task := mathxTask(r, base)
	task.Setup = []string{fmt.Sprintf("echo x >> %s; exit 1", counter)}
	m := newManager(t, func(o *WorkspaceOptions) { o.FailureTTL = 5 * time.Second })
	for i := 0; i < 4; i++ {
		if _, err := m.Prepare(ctxT(t), task, "s"); err == nil {
			t.Fatal("expected failure")
		}
	}
	if got := strings.Count(mustRead(t, counter), "x"); got != 1 {
		t.Fatalf("a broken setup ran %d times for 4 samples, want 1", got)
	}
}

func TestNonGitDirectoryIsCopied(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "a.txt"), "alpha")
	mustWrite(t, filepath.Join(src, "sub", "b.txt"), "beta")
	mustWrite(t, filepath.Join(src, ".git", "config"), "stray") // not a repository: must not be copied
	task := rl.Task{ID: "dir", Kind: rl.TaskFix, Repo: rl.RepoSpec{Path: src, Commit: "n/a"}, Prompt: "p", Verifier: rl.Verifier{Cmd: "true"}}
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	if got := treeFiles(t, w.Root); !reflect.DeepEqual(got, []string{"a.txt", "sub/b.txt"}) {
		t.Fatalf("files = %v", got)
	}
	mustWrite(t, filepath.Join(w.Root, "a.txt"), "changed")
	d, err := w.Diff(ctxT(t), 0)
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "a.txt" {
		t.Fatalf("diff %+v %v", d, err)
	}
	// Changing the directory makes a new snapshot.
	mustWrite(t, filepath.Join(src, "a.txt"), "alpha v2")
	w2, err := m.Prepare(ctxT(t), task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Cleanup() }()
	if got := mustRead(t, filepath.Join(w2.Root, "a.txt")); got != "alpha v2" {
		t.Fatalf("stale snapshot: %q", got)
	}
}

func TestSubdirWorkspace(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("svc/api/main.go", "package main\n")
	r.write("lib/x.go", "package lib\n")
	c := r.commit("mono")
	task := mathxTask(r, c)
	task.Repo.Subdir = "svc/api"
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	if w.Dir != filepath.Join(w.Root, "svc", "api") {
		t.Fatalf("Dir = %s", w.Dir)
	}
	if !exists(filepath.Join(w.Root, "lib", "x.go")) {
		t.Fatal("the whole repository must be present, only the working directory moves")
	}
	task.Repo.Subdir = "does/not/exist"
	if _, err := m.Prepare(ctxT(t), task, "s1"); err == nil || !IsInfra(err) {
		t.Fatalf("missing subdir: %v", err)
	}
}

func TestPrepareErrors(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	tests := []struct {
		name   string
		mutate func(*rl.Task)
	}{
		{"unknown commit", func(t *rl.Task) { t.Repo.Commit = strings.Repeat("0", 40) }},
		{"unknown ref", func(t *rl.Task) { t.Repo.Commit = "no-such-branch" }},
		{"missing repo path", func(t *rl.Task) { t.Repo.Path = filepath.Join(t.Repo.Path, "nope") }},
		{"invalid task", func(t *rl.Task) { t.Verifier.Cmd = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := mathxTask(r, base)
			tc.mutate(&task)
			_, err := m.Prepare(ctxT(t), task, "x")
			if err == nil || !IsInfra(err) {
				t.Fatalf("want an infra error, got %v", err)
			}
		})
	}
	// No workspace directories survive failures.
	ents, _ := os.ReadDir(filepath.Join(m.Root(), "ws"))
	if len(ents) != 0 {
		t.Errorf("failed Prepare left %d workspaces behind", len(ents))
	}
}

func TestPrepareRespectsCancellation(t *testing.T) {
	r, base := mathxRepo(t)
	task := mathxTask(r, base)
	task.Setup = []string{"sleep 20"}
	m := newManager(t, func(o *WorkspaceOptions) { o.KillGrace = 100 * time.Millisecond })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := m.Prepare(ctx, task, "s")
	if err == nil || IsInfra(err) {
		t.Fatalf("a cancelled caller is not an infra error: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Prepare ignored the context")
	}
}

func TestCleanupNeverFollowsSymlinks(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	victim := t.TempDir()
	mustWrite(t, filepath.Join(victim, "precious.txt"), "keep me")
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	// The agent leaves links pointing out of its workspace and read-only trees.
	if err := os.Symlink(victim, filepath.Join(w.Root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(w.Home, "escape-home")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(w.Root, "ro", "deep", "f"), "x")
	if err := os.Chmod(filepath.Join(w.Root, "ro", "deep"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(w.Root, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := w.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Dir(w.Root)) {
		t.Fatal("workspace directory still exists")
	}
	if got := mustRead(t, filepath.Join(victim, "precious.txt")); got != "keep me" {
		t.Fatal("cleanup followed a symlink out of the workspace")
	}
	// Idempotent and concurrent-safe.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Cleanup() }()
	}
	wg.Wait()
}

func TestCleanupRefusesForeignDirectories(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	precious := t.TempDir()
	mustWrite(t, filepath.Join(precious, "f"), "x")
	forged := &Workspace{ID: "forged", mgr: w.mgr, dir: precious}
	if err := forged.Cleanup(); err == nil {
		t.Fatal("Cleanup removed a directory outside the workspace area")
	}
	if !exists(filepath.Join(precious, "f")) {
		t.Fatal("foreign directory was damaged")
	}
}

func TestCleanupKillsLeftoverProcesses(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("needs /proc")
	}
	r, base := mathxRepo(t)
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	// A daemon the agent left behind, started with the workspace's environment.
	cmd := exec.Command("sh", "-c", fmt.Sprintf("sleep 100 & echo $! > %s; wait", pidFile))
	cmd.Dir = w.Dir
	cmd.Env = w.Env()
	configureProc(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	pid := readPID(t, pidFile)
	if !alive(pid) {
		t.Fatal("test process did not start")
	}
	if err := w.Cleanup(); err != nil {
		t.Fatal(err)
	}
	waitDead(t, pid)
}

func TestSnapshotTamperingIsDetectedAndRebuilt(t *testing.T) {
	r, base := mathxRepo(t)
	counter := filepath.Join(t.TempDir(), "runs")
	task := mathxTask(r, base)
	task.Setup = []string{fmt.Sprintf("echo x >> %s", counter)}
	m := newManager(t)
	w1, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w1.Cleanup() }()
	// An agent (or a stray process) reaches into the shared snapshot, which is
	// where every later verification checkout comes from.
	snapTree := w1.snap.tree
	mustWrite(t, filepath.Join(snapTree, "mathx.go"), "package mathx // tampered\n")
	w2, err := m.Prepare(ctxT(t), task, "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Cleanup() }()
	if got := mustRead(t, filepath.Join(w2.Root, "mathx.go")); got != buggyMath {
		t.Fatalf("a workspace was built from a tampered snapshot: %q", got)
	}
	if got := strings.Count(mustRead(t, counter), "x"); got != 2 {
		t.Fatalf("expected a rebuild (2 setup runs), got %d", got)
	}
	if w2.SnapshotKey() == w1.SnapshotKey() {
		t.Fatal("rebuilt snapshot reuses the damaged directory")
	}
	// The first workspace keeps working: its diff base lives in the old snapshot.
	mustWrite(t, filepath.Join(w1.Root, "mathx.go"), fixedMath)
	if d, err := w1.Diff(ctxT(t), 0); err != nil || len(d.Files) != 1 {
		t.Fatalf("diff after tamper: %+v %v", d, err)
	}
	foundWarn := false
	for _, w := range m.Warnings() {
		if strings.Contains(w, "modified after it was built") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("no warning recorded: %v", m.Warnings())
	}
}

func TestSnapshotSurvivesManagerRestart(t *testing.T) {
	r, base := mathxRepo(t)
	counter := filepath.Join(t.TempDir(), "runs")
	task := mathxTask(r, base)
	task.Setup = []string{fmt.Sprintf("echo x >> %s", counter)}
	root := filepath.Join(t.TempDir(), "root")
	mk := func() *Workspaces {
		m, err := NewWorkspaces(WorkspaceOptions{Root: root, DisableNetIsolation: true, SetEnv: map[string]string{"GOCACHE": sharedGoCache}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		return m
	}
	w, err := mk().Prepare(ctxT(t), task, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Cleanup(); err != nil {
		t.Fatal(err)
	}
	w, err = mk().Prepare(ctxT(t), task, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(mustRead(t, counter), "x"); got != 1 {
		t.Fatalf("setup re-ran after restart: %d", got)
	}
}

func TestPruneStale(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	w, err := m.Prepare(ctxT(t), mathxTask(r, base), "s0")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Dir(w.Root), old, old); err != nil {
		t.Fatal(err)
	}
	fresh, err := m.Prepare(ctxT(t), mathxTask(r, base), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Cleanup() }()
	n, err := m.PruneStale(24 * time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if exists(filepath.Dir(w.Root)) || !exists(filepath.Dir(fresh.Root)) {
		t.Fatal("wrong directories pruned")
	}
	// Snapshots are never pruned.
	ents, _ := os.ReadDir(filepath.Join(m.Root(), "snaps"))
	if len(ents) != 1 {
		t.Fatalf("snapshot cache was pruned: %d entries", len(ents))
	}
}

func TestAttach(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	dir := t.TempDir()
	// Recreate the tree by hand, as some other tool would.
	mustWrite(t, filepath.Join(dir, "go.mod"), goMod)
	mustWrite(t, filepath.Join(dir, "mathx.go"), fixedMath)
	mustWrite(t, filepath.Join(dir, "mathx_test.go"), visibleTest)
	mustWrite(t, filepath.Join(dir, ".gitignore"), "*.log\nbuild/\n")
	mustWrite(t, filepath.Join(dir, "README.md"), "# mathx\n")
	w, err := m.Attach(ctxT(t), mathxTask(r, base), dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.Diff(ctxT(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "mathx.go" || d.Files[0].Status != "M" {
		t.Fatalf("diff: %+v", d.Files)
	}
	if err := w.Cleanup(); err != nil || !exists(dir) {
		t.Fatalf("Cleanup of an attached workspace must not delete it: %v", err)
	}
}

func TestConcurrentPrepareAcrossTasks(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	var wg sync.WaitGroup
	var fail atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task := mathxTask(r, base)
			task.ID = fmt.Sprintf("t%d", i%2)
			task.Setup = []string{fmt.Sprintf("echo %d > variant.txt", i%2)}
			w, err := m.Prepare(ctxT(t), task, fmt.Sprintf("s%d", i))
			if err != nil {
				fail.Add(1)
				t.Error(err)
				return
			}
			defer func() { _ = w.Cleanup() }()
			if got := strings.TrimSpace(mustRead(t, filepath.Join(w.Root, "variant.txt"))); got != fmt.Sprint(i%2) {
				fail.Add(1)
				t.Errorf("task t%d got variant %q", i%2, got)
			}
		}(i)
	}
	wg.Wait()
}

func TestUrlRepositoriesAreMirroredOnce(t *testing.T) {
	r, base := mathxRepo(t)
	m := newManager(t)
	task := mathxTask(r, base)
	task.Repo.Path = ""
	task.Repo.URL = "file://" + r.Dir
	w, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	if got := mustRead(t, filepath.Join(w.Root, "mathx.go")); got != buggyMath {
		t.Fatalf("workspace content: %q", got)
	}
	mirrors, _ := os.ReadDir(filepath.Join(m.Root(), "repos"))
	if len(mirrors) != 1 {
		t.Fatalf("expected one mirror, got %d", len(mirrors))
	}
	// A second task on the same URL reuses the mirror, and a commit added upstream
	// after mirroring is fetched on demand.
	r.write("mathx.go", fixedMath)
	newCommit := r.commit("fix")
	task2 := task
	task2.ID = "second"
	task2.Repo.Commit = newCommit
	w2, err := m.Prepare(ctxT(t), task2, "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Cleanup() }()
	if got := mustRead(t, filepath.Join(w2.Root, "mathx.go")); got != fixedMath {
		t.Fatalf("new commit not fetched: %q", got)
	}
	if mirrors, _ = os.ReadDir(filepath.Join(m.Root(), "repos")); len(mirrors) != 1 {
		t.Fatalf("mirror duplicated: %d", len(mirrors))
	}
	// A path that does not exist falls back to the URL.
	task3 := task
	task3.ID = "third"
	task3.Repo.Path = filepath.Join(r.Dir, "nope")
	w3, err := m.Prepare(ctxT(t), task3, "s2")
	if err != nil {
		t.Fatalf("fallback to URL: %v", err)
	}
	if err := w3.Cleanup(); err != nil {
		t.Fatal(err)
	}
	// An unreachable URL is an infra error, and no half-made mirror is left.
	task4 := task
	task4.ID = "fourth"
	task4.Repo.URL = "file://" + filepath.Join(t.TempDir(), "missing.git")
	if _, err := m.Prepare(ctxT(t), task4, "s3"); err == nil || !IsInfra(err) {
		t.Fatalf("unreachable URL: %v", err)
	}
	if mirrors, _ = os.ReadDir(filepath.Join(m.Root(), "repos")); len(mirrors) != 1 {
		t.Fatalf("failed clone left debris: %d entries", len(mirrors))
	}
}

func TestNetworkIsolationFallbackIsRecordedOrRefused(t *testing.T) {
	noHost := func(string) (string, error) { return "", os.ErrNotExist }
	m, err := NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root"), LookPath: func(n string) (string, error) {
		if n == "sh" {
			return "/bin/sh", nil
		}
		return noHost(n)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	found := false
	for _, w := range m.Warnings() {
		if strings.Contains(w, "network isolation unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no fallback warning recorded: %v", m.Warnings())
	}
	if prefix := m.IsolationPrefix(rl.Task{}); prefix != nil {
		t.Errorf("isolation prefix without isolation: %v", prefix)
	}
	// Strict mode refuses to start instead.
	_, err = NewWorkspaces(WorkspaceOptions{Root: filepath.Join(t.TempDir(), "root2"), RequireNetIsolation: true, LookPath: noHost})
	if err == nil || !strings.Contains(err.Error(), "network isolation is required") {
		t.Fatalf("got %v", err)
	}
	// The manifest of a run carries the warning.
	f := newRunnerFixture(t)
	f.m.warn("network isolation unavailable: test")
	f.rollout([]rl.Task{f.task}, 1, f.opts())
	var mf Manifest
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.out, "manifest.json"))), &mf); err != nil {
		t.Fatal(err)
	}
	if len(mf.Warnings) == 0 || !strings.Contains(mf.Warnings[0], "network isolation unavailable") {
		t.Fatalf("manifest warnings: %v", mf.Warnings)
	}
}

func TestResourceLimitsApplyToVerifierCommands(t *testing.T) {
	if _, err := exec.LookPath("prlimit"); err != nil {
		t.Skip("prlimit not installed")
	}
	r, base := mathxRepo(t)
	m := newManager(t, func(o *WorkspaceOptions) { o.Limits = Limits{FileSize: 1 << 20} })
	task := mathxTask(r, base)
	// The command tries to write a 5 MB file: the limit stops it (SIGXFSZ) and
	// the verdict is a failure, not an infra error.
	task.Verifier.Cmd = "head -c 5000000 /dev/zero > big.bin && echo wrote-it"
	w, err := m.Prepare(ctxT(t), task, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Cleanup() }()
	res, err := Verify(ctxT(t), task, w, VerifyOptions{})
	if err != nil || res.Pass || res.ExitCode != 128+25 { // SIGXFSZ
		t.Fatalf("file size limit not enforced: %v pass=%v\n%s", err, res.Pass, res.Log)
	}
}
