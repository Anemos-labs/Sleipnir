package session_test

// More worktree isolation through the assembled session: the same team without
// isolation, commit mode, what is refused, what a crashed session leaves, subdirectories,
// interactive sessions, /rewind, a result that cannot be applied, and the project's
// permission rules inside the trees.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/checkpoint"
	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/gitx"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
	"github.com/reee344/sleipnir/internal/workspace"
)

// threeFiles is the team that needs no isolation to work: three workers, three files.
var threeFiles = [][]string{{"d1.txt:Add d1.txt", "d2.txt:Add d2.txt", "d3.txt:Add d3.txt"}}

// The same team, run without isolation (asked for, and by default): it works exactly as
// it did before the feature existed. Every worker writes into the one checkout, nothing
// is created outside it, no workspace or merge event is logged, and there is nothing to
// finish.
func TestTheSameTeamWithoutIsolationStillWorks(t *testing.T) {
	for _, mode := range []string{"none", ""} {
		name := mode
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			repo := isoRepo(t)
			head := git(t, repo, "rev-parse", "HEAD")
			var sc *isoScript
			client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
			o := isoOptions(t, repo, client, model, "s-plain")
			o.Isolation = mode
			sc = newIsoScript(t, repo, treesOf(o, "s-plain"), 3)
			sc.turns = threeFiles
			s, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			sc.watch(s, nil)
			res, err := s.Run(context.Background(), "add three files")
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != "all tasks accepted" {
				t.Fatalf("manager result %q", res.Text)
			}
			for i, tid := range []string{"T1", "T2", "T3"} {
				if tk, _ := s.Swarm.Board.Snapshot().Task(tid); tk.Status != swarm.StatusDone {
					t.Errorf("%s = %s", tid, tk.Status)
				}
				f := "d" + itoa(i+1) + ".txt"
				if got := readFile(t, filepath.Join(repo, f)); got != "made by be-"+itoa(i+1)+"\n" {
					t.Errorf("%s = %q", f, got)
				}
			}
			if rep := s.Finish(context.Background()); rep != nil {
				t.Errorf("a shared-tree run has nothing to finish: %+v", rep)
			}
			if got := git(t, repo, "rev-parse", "HEAD"); got != head {
				t.Error("HEAD moved")
			}
			if _, err := os.Stat(filepath.Join(o.Home, ".cache")); err == nil {
				t.Error("a shared-tree run made worktrees")
			}
			for typ, n := range eventTypes(t, s.Dir) {
				if strings.HasPrefix(typ, "workspace.") || strings.HasPrefix(typ, "merge.") || typ == events.TypeTaskMerge || typ == events.TypeSwarmIntegration {
					t.Errorf("%d %s events in a run without isolation", n, typ)
				}
			}
			if out := git(t, repo, "branch", "--list", "sleipnir/*"); out != "" {
				t.Errorf("branches were made:\n%s", out)
			}
		})
	}
}

// With Commit the verified result lands on the person's branch as commits (a
// fast-forward), the checkout is clean, and the integration branch is gone.
func TestCommitOptionCommitsTheVerifiedResultOntoTheBranch(t *testing.T) {
	repo := isoRepo(t)
	base := git(t, repo, "rev-parse", "HEAD")
	branch := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	var sc *isoScript
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
	o := isoOptions(t, repo, client, model, "s-commit")
	o.Commit = true
	sc = newIsoScript(t, repo, treesOf(o, "s-commit"), 3)
	sc.turns = threeFiles
	sink := &notices{}
	o.Sink = sink
	o.NewSink = func(string) agent.Sink { return sink }
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sc.watch(s, sink)
	if _, err := s.Run(context.Background(), "add three files"); err != nil {
		t.Fatal(err)
	}
	head := git(t, repo, "rev-parse", "HEAD")
	if head == base {
		t.Fatal("the branch did not move")
	}
	if git(t, repo, "merge-base", "--is-ancestor", base, head) != "" {
		t.Error("the new head is not a descendant of the old one")
	}
	if got := statusLines(t, repo); len(got) != 0 {
		t.Errorf("the checkout is not clean after a commit-mode run: %v", got)
	}
	for _, f := range []string{"d1.txt", "d2.txt", "d3.txt"} {
		if got := git(t, repo, "show", "HEAD:"+f); !strings.HasPrefix(got, "made by be-") {
			t.Errorf("%s at HEAD = %q", f, got)
		}
	}
	if !sink.has("Moved your branch " + branch) {
		t.Errorf("the person was not told the branch moved: %v", sink.logs)
	}
	rep := s.Finish(context.Background())
	if rep == nil || !rep.Applied || !rep.Committed || len(rep.Files) != 3 {
		t.Fatalf("finish: %+v", rep)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if out := git(t, repo, "branch", "--list", "sleipnir/*"); out != "" {
		t.Errorf("branches were left behind:\n%s", out)
	}
	if got := git(t, repo, "rev-parse", "HEAD"); got != head {
		t.Error("closing moved the branch")
	}
}

// What cannot be isolated is refused up front, with the reason, before anything is built
// or created.
func TestIsolationRefusesWhatItCannotDo(t *testing.T) {
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "unused"} })
	newSession := func(t *testing.T, repo string, tweak func(*session.Options)) (*session.Session, error) {
		o := isoOptions(t, repo, client, model, "s-refuse")
		tweak(&o)
		return session.New(context.Background(), o)
	}
	repo := isoRepo(t)
	noGit := t.TempDir()
	noCommits := t.TempDir()
	git(t, noCommits, "init", "-q")
	dirty := isoRepo(t)
	if err := os.WriteFile(filepath.Join(dirty, "README.md"), []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := isoRepo(t)
	if err := os.MkdirAll(filepath.Join(nested, "pkg", ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		repo  string
		tweak func(*session.Options)
		want  string
	}{
		{"not a repository", noGit, func(*session.Options) {}, "git repository"},
		{"no commit yet", noCommits, func(*session.Options) {}, "commit the project first"},
		{"commit with uncommitted changes", dirty, func(o *session.Options) { o.Commit = true }, "clean checkout"},
		{"commit without isolation", repo, func(o *session.Options) { o.Commit, o.Isolation = true, "none" }, "commit needs worktree isolation"},
		{"an unknown mode", repo, func(o *session.Options) { o.Isolation = "bogus" }, `isolation "bogus"`},
		{"a single agent that asks for it", repo, func(o *session.Options) { o.Swarm = false }, "swarm sessions"},
		{"a single agent that asks for a commit", repo, func(o *session.Options) { o.Swarm, o.Isolation, o.Commit = false, "", true }, "commit needs worktree isolation"},
		{"a project root that is not the repository's", nested, func(o *session.Options) {
			o.Root, o.Cwd = filepath.Join(nested, "pkg"), filepath.Join(nested, "pkg")
		}, "project root to be the repository's root"},
		{"trees that would land inside the repository", repo, func(o *session.Options) { o.Home = repo }, "inside the repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newSession(t, tc.repo, tc.tweak)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	// The configuration is not a request: a single agent under a configuration that
	// isolates swarms just runs.
	cfg := config.Defaults()
	cfg.Swarm.Isolation = config.IsolationWorktree
	s, err := newSession(t, repo, func(o *session.Options) { o.Swarm, o.Isolation, o.Config = false, "", cfg })
	if err != nil {
		t.Fatalf("a single agent under swarm.isolation = worktree: %v", err)
	}
	if s.Agent == nil || s.Finish(context.Background()) != nil {
		t.Error("a single-agent session is not isolated")
	}
	s.Close()
	// And the configuration isolates a swarm that did not ask.
	s, err = newSession(t, repo, func(o *session.Options) { o.Isolation, o.Config = "", cfg })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Swarm == nil {
		t.Fatal("no swarm")
	}
	if got := git(t, repo, "branch", "--list", "sleipnir/*"); !strings.Contains(got, "sleipnir/s-refuse/_integration") {
		t.Errorf("swarm.isolation = worktree did not isolate the swarm: branches %q", got)
	}
}

// deadPID is the pid of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("sh", "-c", "exit 0")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// crash makes the marker of the tree at path say its owner is gone.
func crash(t *testing.T, repo, path string) {
	t.Helper()
	markers, _ := filepath.Glob(filepath.Join(repo, ".git", "worktrees", "*", "sleipnir-workspace.json"))
	for _, mk := range markers {
		var doc map[string]any
		if err := json.Unmarshal([]byte(readFile(t, mk)), &doc); err != nil {
			t.Fatal(err)
		}
		if doc["path"] != path {
			continue
		}
		doc["pid"] = deadPID(t)
		delete(doc, "start")
		b, _ := json.Marshal(doc)
		if err := os.WriteFile(mk, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("no marker for %s", path)
}

// A session that died leaves its trees behind. The next isolated session of the
// repository cleans them up at its start: uncommitted work is committed onto the tree's
// own branch first (nothing is lost, and the person is told where it is), and a tree
// whose owner is still running is not touched.
func TestTreesOfACrashedSessionAreCleanedUpAtTheNextStart(t *testing.T) {
	repo := isoRepo(t)
	home := t.TempDir()
	top := filepath.Join(home, ".cache", "sleipnir", "worktrees")
	ctx := context.Background()
	r, err := gitx.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	makeTree := func(sid string) *workspace.Tree {
		m := &workspace.Manager{Repo: r, Dir: filepath.Join(top, sid), Prefix: "sleipnir/" + sid}
		tr, err := m.Create(ctx, "be-1", workspace.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	dead, live := makeTree("dead-1"), makeTree("live-1")
	if err := os.WriteFile(filepath.Join(dead.Path, "wip.txt"), []byte("unsaved work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	crash(t, repo, dead.Path)

	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "unused"} })
	o := isoOptions(t, repo, client, model, "s-next")
	o.Home = home
	sink := &notices{}
	o.Sink = sink
	s, err := session.New(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := os.Stat(dead.Path); err == nil {
		t.Error("the crashed session's tree is still there")
	}
	if _, err := os.Stat(live.Path); err != nil {
		t.Errorf("a tree of a running session was removed: %v", err)
	}
	if got := git(t, repo, "show", "sleipnir/dead-1/be-1:wip.txt"); got != "unsaved work" {
		t.Errorf("the unsaved work was not kept on its branch: %q", got)
	}
	if !sink.has("cleaned up 1 worktree(s)") || !sink.has("sleipnir/dead-1/be-1") {
		t.Errorf("the person was not told what was cleaned up and what was kept: %v", sink.logs)
	}
	if got := statusLines(t, repo); len(got) != 0 {
		t.Errorf("the checkout changed: %v", got)
	}
	if out := git(t, repo, "branch", "--list", "sleipnir/live-1/*"); !strings.Contains(out, "be-1") {
		t.Errorf("the running session's branch was removed: %q", out)
	}
	if got := eventTypes(t, s.Dir); got[events.TypeWorkspacePrune] < 1 {
		t.Errorf("the cleanup is not in the log: %v", got)
	}
}

// A session started in a subdirectory of the repository keeps its writers in the same
// subdirectory of their trees: relative paths mean the same thing as in a shared run.
func TestIsolatedSessionInASubdirectoryKeepsItsWritersThere(t *testing.T) {
	repo := isoRepo(t)
	var sc *isoScript
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
	o := isoOptions(t, repo, client, model, "s-sub")
	o.Cwd = filepath.Join(repo, "sub")
	o.Verify = "" // the verifier lives at the root
	sc = newIsoScript(t, repo, treesOf(o, "s-sub"), 1)
	sc.files = map[string]string{"T1": "x.txt"}
	sc.turns = [][]string{{"sub/x.txt:Add sub/x.txt"}}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "add x.txt here"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(repo, "sub", "x.txt")); got != "made by be-1\n" {
		t.Errorf("sub/x.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "x.txt")); err == nil {
		t.Error("the file landed at the repository's root")
	}
	sc.mu.Lock()
	ls := sc.listing["be-1"]
	sc.mu.Unlock()
	if !strings.Contains(ls, "keep") || strings.Contains(ls, "README.md") || !strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ls), "[exit code 0]")), "/be-1/sub") {
		t.Errorf("the worker's directory is not the same subdirectory of its tree: %q", ls)
	}
}

// An interactive session applies what has been merged when the manager stops, at the end
// of every turn, and only what is new each time; the end of the session accounts for all
// of it.
func TestInteractiveIsolatedSessionAppliesAtTheEndOfEveryTurn(t *testing.T) {
	repo := isoRepo(t)
	var sc *isoScript
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
	o := isoOptions(t, repo, client, model, "s-chat")
	o.Interactive = true
	sc = newIsoScript(t, repo, treesOf(o, "s-chat"), 1)
	sc.turns = [][]string{{"d1.txt:Add d1.txt"}, {"d2.txt:Add d2.txt"}}
	sink := &notices{}
	o.Sink = sink
	o.NewSink = func(string) agent.Sink { return sink }
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(repo, "d1.txt")); got != "made by be-1\n" {
		t.Fatalf("after the first turn d1.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "d2.txt")); err == nil {
		t.Fatal("d2.txt exists before its task was started")
	}
	if !sink.has("integrate: Applied 1 file(s) to your working tree as uncommitted changes: d1.txt.") {
		t.Fatalf("the first application was not reported: %v", sink.logs)
	}

	if _, err := s.Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(repo, "d2.txt")); got != "made by be-2\n" {
		t.Fatalf("after the second turn d2.txt = %q", got)
	}
	if !sink.has("integrate: Applied 1 file(s) to your working tree as uncommitted changes: d2.txt.") {
		t.Fatalf("the second application should name only what is new: %v", sink.logs)
	}
	rep := s.Finish(context.Background())
	if rep == nil || !rep.Applied || strings.Join(rep.Files, ",") != "d1.txt,d2.txt" || !strings.Contains(rep.Message, "Applied 2 file(s)") {
		t.Fatalf("the account of the whole session: %+v", rep)
	}
}

// /rewind can undo what an isolated run applied to the checkout, as it can undo what a
// shared run wrote there, and the end of the session does not apply it again.
func TestRewindUndoesWhatAnIsolatedRunApplied(t *testing.T) {
	repo := isoRepo(t)
	var sc *isoScript
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
	o := isoOptions(t, repo, client, model, "s-rewind")
	sc = newIsoScript(t, repo, treesOf(o, "s-rewind"), 1)
	sc.turns = [][]string{{"d1.txt:Add d1.txt"}}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "add d1.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "d1.txt")); err != nil {
		t.Fatal(err)
	}
	var id string
	for _, cp := range s.Ckpt.List() {
		for _, f := range cp.Files {
			if f == "d1.txt" {
				id = cp.ID
			}
		}
	}
	if id == "" {
		t.Fatalf("the applied file is in no checkpoint: %+v", s.Ckpt.List())
	}
	rep, err := s.Ckpt.Restore(id, checkpoint.RestoreOpts{})
	if err != nil || !rep.OK() {
		t.Fatalf("rewind: %v %+v", err, rep)
	}
	if got := statusLines(t, repo); len(got) != 0 {
		t.Fatalf("the rewind left changes in the checkout: %v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "d1.txt")); err == nil {
		t.Fatal("the end of the session applied the result again after the person undid it")
	}
}

// When the checkout changed under the run (the person made the same file), nothing is
// overwritten: the result stays on its branch, the person is told which and how to get
// it, and closing the session keeps the branch.
func TestResultThatCannotBeAppliedStaysOnItsBranch(t *testing.T) {
	repo := isoRepo(t)
	var sc *isoScript
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
	o := isoOptions(t, repo, client, model, "s-late")
	sc = newIsoScript(t, repo, treesOf(o, "s-late"), 1)
	sc.turns = [][]string{{"d1.txt:Add d1.txt"}}
	sc.beforeDone = func(string) { // the person gets to the file first
		_ = os.WriteFile(filepath.Join(repo, "d1.txt"), []byte("the person's own d1\n"), 0o644)
	}
	sink := &notices{}
	o.Sink = sink
	o.NewSink = func(string) agent.Sink { return sink }
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "add d1.txt"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(repo, "d1.txt")); got != "the person's own d1\n" {
		t.Fatalf("the person's file was overwritten: %q", got)
	}
	if !sink.has("warn: The result was NOT applied to your checkout") {
		t.Fatalf("the person was not told: %v", sink.logs)
	}
	rep := s.Finish(context.Background())
	if rep == nil || rep.Applied || !rep.BranchKept || !strings.Contains(rep.Hint, "git diff --binary") || !strings.Contains(rep.Hint, "sleipnir/s-late/_integration") {
		t.Fatalf("finish: %+v", rep)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "show", "sleipnir/s-late/_integration:d1.txt"); got != "made by be-1" {
		t.Errorf("the result is not on its branch: %q", got)
	}
	if ents, _ := os.ReadDir(treesOf(o, "s-late")); len(ents) != 0 {
		t.Errorf("trees were left behind: %v", ents)
	}
}

// The project's own permission rules hold inside a worker's tree as they do in the
// checkout: a directory the configuration denies cannot be written there, and writing
// the configuration directory asks (an unattended run refuses). Without this a worker
// could put in its copy what the rules keep out of the original.
func TestProjectPermissionRulesHoldInsideTheTrees(t *testing.T) {
	repo := isoRepo(t)
	var sc *isoScript
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
	o := isoOptions(t, repo, client, model, "s-rules")
	cfg := config.Defaults()
	cfg.Permissions.Deny = []string{"Edit(./migrations/**)"}
	o.Config = cfg
	sc = newIsoScript(t, repo, treesOf(o, "s-rules"), 1)
	sc.extraCalls = map[string][]mock.ToolCall{"T1": {
		call("x1", "write", map[string]any{"path": "migrations/001.sql", "content": "drop table users;\n"}),
		call("x2", "write", map[string]any{"path": ".sleipnir/config.json", "content": "{}\n"}),
		call("x3", "bash", map[string]any{"command": "echo 'drop table users;' > migrations/002.sql"}),
	}}
	sc.turns = [][]string{{"d1.txt:Add d1.txt"}}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "add d1.txt"); err != nil {
		t.Fatal(err)
	}
	sc.mu.Lock()
	got := sc.extras["be-1"]
	sc.mu.Unlock()
	if len(got) != 3 || !strings.Contains(got[0], "denied by rule Edit(./migrations/**)") || !strings.Contains(got[1], "approval required") || !strings.Contains(got[2], "denied by rule Edit(./migrations/**)") {
		t.Fatalf("what the worker got back: %q", got)
	}
	for _, f := range []string{"migrations", ".sleipnir"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err == nil {
			t.Errorf("%s reached the checkout", f)
		}
	}
	if got := readFile(t, filepath.Join(repo, "d1.txt")); got != "made by be-1\n" {
		t.Errorf("d1.txt = %q: the allowed part of the work must still arrive", got)
	}
}

// The event names the session log carries for the workspace layer are the ones that
// layer emits: a rename on either side would otherwise silently split them.
func TestEventTypesAreTheWorkspaceLayersOwn(t *testing.T) {
	for _, p := range [][2]string{
		{events.TypeWorkspaceCreate, workspace.EventCreate}, {events.TypeWorkspaceRemove, workspace.EventRemove},
		{events.TypeWorkspacePrune, workspace.EventPrune}, {events.TypeWorkspaceCommit, workspace.EventCommit},
		{events.TypeWorkspaceReset, workspace.EventReset}, {events.TypeMergeQueued, workspace.EventQueued},
		{events.TypeMergeMerged, workspace.EventMerged}, {events.TypeMergeConflict, workspace.EventConflict},
		{events.TypeMergeVerifyFail, workspace.EventVerifyFailed}, {events.TypeMergeRolledBack, workspace.EventRolledBack},
		{events.TypeMergeRejected, workspace.EventRejected}, {events.TypeMergeFastFwd, workspace.EventFastForward},
	} {
		if p[0] != p[1] {
			t.Errorf("events has %q, workspace emits %q", p[0], p[1])
		}
	}
}
