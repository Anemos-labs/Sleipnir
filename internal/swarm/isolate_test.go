package swarm

// Tests for worktree isolation (isolate.go): a real git repository, real worktrees and
// the real merge queue, with scripted models and small file tools that write into the
// agent's own working directory through the same guard the fs tools use. Verifiers are
// Go functions, so nothing depends on a shell.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/gitx"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/workspace"
)

// gitOut runs the git binary in dir with a hermetic environment.
func gitOut(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

// newProject makes a small repository with a few files committed (plus extra).
func newProject(t testing.TB, extra map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "init", "-q", "-b", "main", ".")
	files := map[string]string{
		"a.txt":      "alpha\n",
		"b.txt":      "bravo\n",
		"c.txt":      "charlie\n",
		"shared.txt": "line one\nline two\nline three\n",
		"src/x.go":   "package src\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func readText(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileExists(path string) bool { _, err := os.Lstat(path); return err == nil }

func writeFileString(path, s string) error { return os.WriteFile(path, []byte(s), 0o644) }

// dirEntries lists a directory's entries (nil when it does not exist).
func dirEntries(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// verifyDir is the verifier the tests use, in the worker's tree and in the merge
// queue's integration tree alike: no file may contain BROKEN, and when limit.txt
// exists at most that many part-*.txt files may.
func verifyDir(dir string) (string, int) {
	var problems []string
	ents, _ := os.ReadDir(dir)
	parts := 0
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && strings.Contains(string(b), "BROKEN") {
			problems = append(problems, "FAIL: "+e.Name()+" contains BROKEN")
		}
		if strings.HasPrefix(e.Name(), "part-") {
			parts++
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "limit.txt")); err == nil {
		var max int
		fmt.Sscanf(strings.TrimSpace(string(b)), "max=%d", &max)
		if parts > max {
			problems = append(problems, fmt.Sprintf("FAIL: %d part files, at most %d allowed", parts, max))
		}
	}
	if len(problems) > 0 {
		return strings.Join(problems, "\n"), 1
	}
	return "ok", 0
}

// isoRig is a swarm over a real repository with isolation on.
type isoRig struct {
	*rvRig
	repo, trees string
	mgr         *workspace.Manager
	q           *workspace.Queue
	iso         *Isolation

	mu   sync.Mutex
	cwds map[string]string // agent -> the working directory its tools ran in
	saw  map[string]string // "agent:path" -> what cat found there ("(missing)" if nothing)
}

type isoOpts struct {
	cfg    Config
	commit bool
	verify bool              // run the test verifier in trees and in the merge queue
	files  map[string]string // extra files in the initial commit
	// snapshot makes the user's uncommitted state part of the base (what the session does
	// for patch mode); dirty is written to the checkout before the manager is made.
	snapshot bool
	dirty    map[string]string
	// queueVerify replaces the merge queue's verifier (the trees keep verifyDir).
	queueVerify func(dir string) (string, int)
	// dirsVerify makes the verify command "check-dirs {dirs}": every directory it names must hold
	// done.txt, in the trees and in the merge queue alike (see checkDirs).
	dirsVerify bool
	tweak      func(*Deps)
}

// checkDirs is the verifier of dirsVerify: the words of cmd after its first are directories
// (./a ./b, or ./... for every top-level directory), and each must hold done.txt in dir.
func checkDirs(dir, cmd string) (string, int) {
	words := strings.Fields(cmd)
	if len(words) < 2 {
		return "FAIL: no directories to check in " + cmd, 1
	}
	var want []string
	for _, w := range words[1:] {
		if w == "./..." {
			for _, e := range dirEntries(dir) {
				if st, err := os.Stat(filepath.Join(dir, e)); err == nil && st.IsDir() && !strings.HasPrefix(e, ".") {
					want = append(want, e)
				}
			}
			continue
		}
		want = append(want, strings.TrimPrefix(w, "./"))
	}
	var problems []string
	for _, d := range want {
		if !fileExists(filepath.Join(dir, filepath.FromSlash(d), "done.txt")) {
			problems = append(problems, "FAIL: "+d+"/done.txt is missing")
		}
	}
	if len(problems) > 0 {
		return strings.Join(problems, "\n"), 1
	}
	return "ok", 0
}

// newIsoRig builds the repository, the workspace manager, the queue and the swarm.
func newIsoRig(t *testing.T, o isoOpts, fn func(ctx context.Context, c *rvCall) rvReply) *isoRig {
	t.Helper()
	repo := newProject(t, o.files)
	for name, body := range o.dirty {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var is /private/var, and the trees are reported by their real path
	if err != nil {
		t.Fatal(err)
	}
	trees := filepath.Join(base, "trees")
	log := events.NewMemLog()
	git, err := gitx.Open(repo, gitx.WithHermeticConfig())
	if err != nil {
		t.Fatal(err)
	}
	mgr := &workspace.Manager{Repo: git, Dir: trees, Prefix: "sleipnir/t", Snapshot: o.snapshot, OnEvent: workspace.EmitTo(log)}
	qo := workspace.QueueOptions{}
	if o.verify || o.queueVerify != nil || o.dirsVerify {
		check := verifyDir
		if o.queueVerify != nil {
			check = o.queueVerify
		}
		qo.VerifyCmd = "verify-dir"
		qo.Verify = func(ctx context.Context, req workspace.VerifyRequest) workspace.VerifyResult {
			out, code := check(req.Dir)
			if o.dirsVerify {
				out, code = checkDirs(req.Dir, req.Cmd)
			}
			return workspace.VerifyResult{Cmd: req.Cmd, ExitCode: code, Output: out}
		}
	}
	q, err := workspace.NewQueue(context.Background(), mgr, qo)
	if err != nil {
		t.Fatal(err)
	}
	iso := &Isolation{Manager: mgr, Queue: q, Commit: o.commit}
	cfg := o.cfg
	if o.verify || o.queueVerify != nil || o.dirsVerify {
		cfg.VerifyCmd = "verify-dir"
		cfg.Verify = func(ctx context.Context, dir, cmd string) (string, int, error) {
			out, code := verifyDir(dir)
			return out, code, nil
		}
		if o.dirsVerify {
			cfg.VerifyCmd = "check-dirs {dirs}"
			cfg.Verify = func(ctx context.Context, dir, cmd string) (string, int, error) {
				out, code := checkDirs(dir, cmd)
				return out, code, nil
			}
		}
	}
	r := &isoRig{repo: repo, trees: trees, mgr: mgr, q: q, iso: iso, cwds: map[string]string{}, saw: map[string]string{}}
	r.rvRig = newRVRigWith(t, cfg, fn, func(d *Deps) {
		d.Workdir, d.Root = repo, repo
		d.Isolation = iso
		d.Events = log
		if o.tweak != nil {
			o.tweak(d)
		}
	})
	r.log = log
	// Small file tools in place of the rig's fakes: they write where the agent works
	// and go through the guard the fs tools use.
	reg := r.sw.deps.Registry
	for _, ft := range r.fileTools() {
		reg.Register(ft)
	}
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	r.sw.SetToolset(reg, specs)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		r.sw.Finish(ctx)
	})
	return r
}

func (r *isoRig) noteCwd(agentID, cwd string) {
	r.mu.Lock()
	r.cwds[agentID] = cwd
	r.mu.Unlock()
}

// fileTools are write (through the guard), sneak and sneakrm (what a shell command
// could do: no guard), cat and rm (guarded).
func (r *isoRig) fileTools() []rvFakeTool {
	abs := func(c *tools.Call, p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(c.Env.Cwd, p)
	}
	return []rvFakeTool{
		{name: "write", run: func(ctx context.Context, c *tools.Call) *tools.Result {
			var in struct{ Path, Content string }
			_ = json.Unmarshal(c.Input, &in)
			r.noteCwd(c.Env.Agent, c.Env.Cwd)
			p := abs(c, in.Path)
			if err := c.Env.Guard.BeforeWrite(c.Env.Agent, p); err != nil {
				return tools.Errorf("%v", err)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return tools.Errorf("%v", err)
			}
			if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
				return tools.Errorf("%v", err)
			}
			c.Env.Guard.AfterWrite(c.Env.Agent, p)
			return &tools.Result{Text: "wrote " + in.Path}
		}},
		{name: "sneak", run: func(ctx context.Context, c *tools.Call) *tools.Result {
			var in struct{ Path, Content string }
			_ = json.Unmarshal(c.Input, &in)
			if err := os.WriteFile(abs(c, in.Path), []byte(in.Content), 0o644); err != nil {
				return tools.Errorf("%v", err)
			}
			return &tools.Result{Text: "wrote " + in.Path + " without the guard"}
		}},
		{name: "sneakrm", run: func(ctx context.Context, c *tools.Call) *tools.Result {
			var in struct{ Path string }
			_ = json.Unmarshal(c.Input, &in)
			if err := os.Remove(abs(c, in.Path)); err != nil {
				return tools.Errorf("%v", err)
			}
			return &tools.Result{Text: "removed " + in.Path + " without the guard"}
		}},
		{name: "cat", ro: true, run: func(ctx context.Context, c *tools.Call) *tools.Result {
			var in struct{ Path string }
			_ = json.Unmarshal(c.Input, &in)
			r.noteCwd(c.Env.Agent, c.Env.Cwd)
			b, err := os.ReadFile(abs(c, in.Path))
			r.mu.Lock()
			if err != nil {
				r.saw[c.Env.Agent+":"+in.Path] = "(missing)"
			} else {
				r.saw[c.Env.Agent+":"+in.Path] = string(b)
			}
			r.mu.Unlock()
			if err != nil {
				return tools.Errorf("cannot read %s: no such file", in.Path)
			}
			return &tools.Result{Text: string(b)}
		}},
		{name: "rm", run: func(ctx context.Context, c *tools.Call) *tools.Result {
			var in struct{ Path string }
			_ = json.Unmarshal(c.Input, &in)
			p := abs(c, in.Path)
			if err := c.Env.Guard.BeforeWrite(c.Env.Agent, p); err != nil {
				return tools.Errorf("%v", err)
			}
			if err := os.Remove(p); err != nil {
				return tools.Errorf("%v", err)
			}
			c.Env.Guard.AfterWrite(c.Env.Agent, p)
			return &tools.Result{Text: "removed " + in.Path}
		}},
	}
}

func (r *isoRig) sawText(agentID, path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saw[agentID+":"+path]
}

func (r *isoRig) cwd(agentID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cwds[agentID]
}

func (r *isoRig) task(id string) Task {
	tk, _ := r.sw.Board.Snapshot().Task(id)
	return tk
}

func (r *isoRig) waitStatus(id string, st TaskStatus) {
	r.t.Helper()
	rvWait(r.t, fmt.Sprintf("%s to be %s", id, st), func() bool { return r.task(id).Status == st })
}

// accept runs the manager's accept.
func (r *isoRig) accept(id string) *tools.Result {
	return r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "accept", "id": id})
}

func (r *isoRig) mustSpawn(role, title string, files ...string) string {
	r.t.Helper()
	id, err := r.sw.Spawn(SpawnReq{Role: role, Title: title, Files: files, By: "mgr"})
	if err != nil {
		r.t.Fatalf("spawn %s (%s): %v", role, title, err)
	}
	return id
}

func (r *isoRig) finish() *IntegrationReport {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rep := r.sw.Finish(ctx)
	if rep == nil {
		r.t.Fatal("Finish returned no report for an isolated swarm")
	}
	return rep
}

func doneCall(id, text string) rvToolCall {
	return rvToolCall{"task", map[string]any{"action": "done", "id": id, "text": text}}
}

func writeCall(path, content string) rvToolCall {
	return rvToolCall{"write", map[string]any{"path": path, "content": content}}
}

func catCall(path string) rvToolCall { return rvToolCall{"cat", map[string]any{"path": path}} }

func act(calls ...rvToolCall) func(*rvCall) rvReply {
	return func(*rvCall) rvReply { return rvReply{Tools: calls} }
}

// script gives each agent its replies, in order: the reply to a request is the entry at
// the number of assistant turns already in the thread. Past the end an agent answers.
type script map[string][]func(c *rvCall) rvReply

func (s script) fn() func(ctx context.Context, c *rvCall) rvReply {
	return func(ctx context.Context, c *rvCall) rvReply {
		if list, ok := s[c.Agent]; ok && c.Assistants < len(list) {
			return list[c.Assistants](c)
		}
		return rvReply{Text: "ok from " + c.Agent}
	}
}

// ---- the tests -----------------------------------------------------------------------

// Three workers edit disjoint files in trees of their own; nobody sees anyone's
// uncommitted files; every task is merged before review; the user's checkout ends
// with the three edits as uncommitted changes and no tree, branch or registration is
// left behind.
func TestIsolatedWorkersEditDisjointFilesAndTheirWorkIsMerged(t *testing.T) {
	var barrier sync.WaitGroup
	barrier.Add(3)
	release := make(chan struct{})
	go func() { barrier.Wait(); close(release) }()
	worker := func(id, file, content string, look ...string) []func(*rvCall) rvReply {
		var cats []rvToolCall
		for _, p := range look {
			cats = append(cats, catCall(p))
		}
		return []func(*rvCall) rvReply{
			act(writeCall(file, content)),
			func(*rvCall) rvReply { // every worker has written: now look at the others' files
				barrier.Done()
				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}
				return rvReply{Tools: cats}
			},
			act(doneCall(id, "edited "+file)),
		}
	}
	sc := script{
		"be-1": worker("T1", "a.txt", "alpha edited by be-1\n", "b.txt.new", "c.txt.new"),
		"fe-1": worker("T2", "b.txt.new", "bravo by fe-1\n", "a.txt", "c.txt.new"),
		"fs-1": worker("T3", "c.txt.new", "charlie by fs-1\n", "a.txt", "b.txt.new"),
	}
	// MaxWriters 1: an isolated run has no writer cap, three writers run at once.
	r := newIsoRig(t, isoOpts{cfg: Config{MaxWriters: 1}, verify: true}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a", "a.txt")
	r.mustSpawn("frontend", "add b", "b.txt.new")
	r.mustSpawn("fullstack", "add c", "c.txt.new")
	for _, id := range []string{"T1", "T2", "T3"} {
		r.waitStatus(id, StatusReview)
	}

	// Each worker's tools ran in a tree of its own under the trees directory.
	seen := map[string]bool{}
	for _, id := range []string{"be-1", "fe-1", "fs-1"} {
		cwd := r.cwd(id)
		if cwd == "" || cwd == r.repo || !strings.HasPrefix(cwd, r.trees+string(filepath.Separator)) || filepath.Base(cwd) != id {
			t.Fatalf("%s worked in %q, want a tree under %s named %s", id, cwd, r.trees, id)
		}
		if seen[cwd] {
			t.Fatalf("two workers share the tree %s", cwd)
		}
		seen[cwd] = true
	}
	// Nobody saw anybody else's uncommitted work: the base version of a file another
	// worker edited, and nothing at all of the files they created.
	for _, c := range []struct{ who, file, want string }{
		{"be-1", "b.txt.new", "(missing)"}, {"be-1", "c.txt.new", "(missing)"},
		{"fe-1", "a.txt", "alpha\n"}, {"fe-1", "c.txt.new", "(missing)"},
		{"fs-1", "a.txt", "alpha\n"}, {"fs-1", "b.txt.new", "(missing)"},
	} {
		if got := r.sawText(c.who, c.file); got != c.want {
			t.Fatalf("%s saw %q in %s, want %q: another worker's uncommitted work leaked", c.who, got, c.file, c.want)
		}
	}
	// The user's checkout is untouched while the work is only in review.
	if got := readText(t, filepath.Join(r.repo, "a.txt")); got != "alpha\n" {
		t.Fatalf("the user's checkout changed before the end of the run: %q", got)
	}
	// Merged before review, with the merge in the evidence; accept needs nothing more.
	for _, id := range []string{"T1", "T2", "T3"} {
		tk := r.task(id)
		if !strings.Contains(tk.Evidence, "merged into integration") || !strings.Contains(tk.Evidence, "(verified)") {
			t.Fatalf("%s evidence %q lacks the merge", id, tk.Evidence)
		}
		if res := r.accept(id); res.IsError {
			t.Fatalf("accept %s: %s", id, res.Text)
		}
	}
	if st := r.q.Status(); st.Merged != 3 || st.Conflicts != 0 || st.VerifyFailures != 0 {
		t.Fatalf("queue: %+v", st)
	}

	rep := r.finish()
	if !rep.Applied || rep.Committed {
		t.Fatalf("report: %+v", rep)
	}
	for file, want := range map[string]string{"a.txt": "alpha edited by be-1\n", "b.txt.new": "bravo by fe-1\n", "c.txt.new": "charlie by fs-1\n"} {
		if got := readText(t, filepath.Join(r.repo, file)); got != want {
			t.Fatalf("%s in the user's checkout = %q, want %q", file, got, want)
		}
	}
	status := gitOut(t, r.repo, "status", "--porcelain")
	if !strings.Contains(status, " M a.txt") || !strings.Contains(status, "?? b.txt.new") || !strings.Contains(status, "?? c.txt.new") {
		t.Fatalf("the result must be uncommitted edits, as a shared-tree run leaves them:\n%s", status)
	}
	if log := gitOut(t, r.repo, "log", "--oneline"); strings.Contains(log, "\n") {
		t.Fatalf("the user's history changed:\n%s", log)
	}
	if ents, err := os.ReadDir(r.trees); err == nil && len(ents) > 0 {
		t.Fatalf("trees left behind: %v", ents)
	}
	if b := gitOut(t, r.repo, "branch", "--list", "sleipnir/*"); b != "" {
		t.Fatalf("branches left behind:\n%s", b)
	}
	if wt := gitOut(t, r.repo, "worktree", "list", "--porcelain"); strings.Count(wt, "worktree ") != 1 {
		t.Fatalf("worktrees left registered:\n%s", wt)
	}
	for _, typ := range []string{events.TypeWorkspaceCreate, events.TypeMergeMerged, events.TypeTaskMerge, events.TypeSwarmIntegration, events.TypeWorkspaceRemove} {
		if len(r.log.OfType(typ)) == 0 {
			t.Errorf("no %s events were logged", typ)
		}
	}
}

// A decomposed task set can be verified task by task in isolated trees when the verify command
// names the task's own directories ({dirs}). Without it each worker's `go test ./...`-like command
// sees only its own change and fails on the others' directories until they merge, which they cannot
// do before they pass: a real swarm of three workers deadlocked that way.
func TestScopedVerifyLetsDecomposedIsolatedTasksEachPassOnTheirOwnWork(t *testing.T) {
	sc := script{
		"be-1": {act(writeCall("a/done.txt", "ok\n")), act(doneCall("T1", "made a"))},
		"fe-1": {act(writeCall("b/done.txt", "ok\n")), act(doneCall("T2", "made b"))},
	}
	r := newIsoRig(t, isoOpts{cfg: Config{MaxWriters: 1}, dirsVerify: true,
		files: map[string]string{"a/keep.txt": "a\n", "b/keep.txt": "b\n"}}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "make a", "a/**")
	r.mustSpawn("frontend", "make b", "b/**")
	for _, id := range []string{"T1", "T2"} {
		r.waitStatus(id, StatusReview)
	}
	for _, id := range []string{"T1", "T2"} {
		if tk := r.task(id); !strings.Contains(tk.Evidence, "merged into integration") {
			t.Fatalf("%s evidence %q lacks the merge", id, tk.Evidence)
		}
	}
	// The same swarm with a command over the whole repository never gets there: each worker's tree
	// lacks the other's done.txt.
	if out, code := checkDirs(r.cwd("be-1"), "check-dirs ./..."); code == 0 {
		t.Fatalf("the whole-repository command passed in a worker's tree (%s): the scenario proves nothing", out)
	}
}

// The manager of an isolated run is told, in its private notes, that the checkout does not show the
// work until the run ends and that the harness applies it then: a first real run's manager told the
// user the verified work "lives on _integration; merge it into master" when the harness was about to
// apply it. A manager in an ordinary run is told nothing about isolation.
func TestTheManagerOfAnIsolatedRunKnowsWhereTheWorkGoes(t *testing.T) {
	iso := newIsoRig(t, isoOpts{cfg: Config{MaxWriters: 1}, verify: true}, func(ctx context.Context, c *rvCall) rvReply {
		return rvReply{Text: "nothing to do"}
	})
	iso.sw.StartManager()
	m := iso.sw.get(iso.sw.ManagerID())
	if _, err := m.a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !iso.prov.sawEver("Isolation: every writer works in a private git worktree") || !iso.prov.sawEver("never tell the user to merge anything") {
		t.Fatal("the manager of an isolated run was not told what happens to the merged work")
	}
	if strings.Contains(managerIsolationCard, "/") {
		t.Fatalf("the card names a path: the bytes must not vary: %q", managerIsolationCard)
	}

	plain := newRVRig(t, Config{MaxWriters: 1}, func(ctx context.Context, c *rvCall) rvReply { return rvReply{Text: "nothing to do"} })
	plain.sw.StartManager()
	pm := plain.sw.get(plain.sw.ManagerID())
	if _, err := pm.a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if plain.prov.sawEver("Isolation:") {
		t.Fatal("a manager in an ordinary run was told about isolation")
	}
}
