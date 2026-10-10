package wsvc

import (
	"context"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

var (
	whoRE  = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	taskRE = regexp.MustCompile(`task (T\d+)`)
)

// whoIs reads which agent a request is for, and its task.
func whoIs(c *mock.Call) (id, role, tid string) {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role != "user" {
			continue
		}
		if m := whoRE.FindStringSubmatch(c.Messages[i].Content); m != nil && id == "" {
			id, role = m[1], m[2]
		}
		if m := taskRE.FindStringSubmatch(c.Messages[i].Content); m != nil && tid == "" {
			tid = m[1]
		}
	}
	return
}

// isoEnv is an isolated team (worktree isolation, a verifier) whose one worker added
// d1.txt in its own tree; the merge queue verified and merged it, and the end of the
// manager's run applied it to the checkout as uncommitted edits.
func isoEnv(t *testing.T) *env {
	t.Helper()
	root := project(t, map[string]string{
		"README.md": "# demo\n",
		"verify.sh": "test -f d1.txt || { echo 'FAIL: no d1.txt'; exit 1; }\necho OK verified\n",
	})
	var mu sync.Mutex
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		_, role, tid := whoIs(c)
		n := turns(c)
		if role == "manager" {
			switch n {
			case 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("c1", "task", map[string]any{"action": "create", "title": "Add d1.txt", "role": "backend", "files": []string{"d1.txt"}}),
					call("s1", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("w1", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 120}),
				}}
			case 1:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("a1", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "all tasks accepted"}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "working", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "d1.txt", "content": "made in a tree\nsecond line\n"})}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d1", "task", map[string]any{"action": "done", "id": tid, "text": "added d1.txt"})}}
		}
		return mock.Reply{Text: "done"}
	})
	o := options(t, root, client, model)
	o.ID = "s-wsvc-iso"
	o.Swarm, o.Workers = true, 2
	o.Verify = "sh verify.sh"
	o.Isolation = "worktree"
	e := newEnv(t, root, o)
	if _, err := e.sess.Run(context.Background(), "add d1.txt"); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(root, "d1.txt")) != "made in a tree\nsecond line\n" {
		t.Fatal("the merged result was not applied to the checkout")
	}
	return e
}

func TestIsolatedTeamQueueTreesVerifyBlameAndAccept(t *testing.T) {
	e := isoEnv(t)

	var q wire.MergeQueueStatus
	expect(t, e.get("/ws/queue", &q), http.StatusOK, "")
	if !q.Healthy || q.Verify != "sh verify.sh" || len(q.Landed) != 1 || q.Landed[0].Task != "T1" || q.Landed[0].Agent != "be-1" ||
		strings.Join(q.Landed[0].Files, ",") != "d1.txt" || !strings.HasSuffix(q.Branch, "_integration") {
		t.Fatalf("queue = %+v", q)
	}

	var trees struct{ Worktrees []wire.Worktree }
	expect(t, e.get("/ws/worktrees", &trees), http.StatusOK, "")
	if len(trees.Worktrees) != 1 || trees.Worktrees[0].Agent != "be-1" || trees.Worktrees[0].Branch != "sleipnir/s-wsvc-iso/be-1" || trees.Worktrees[0].Head == "" {
		t.Fatalf("worktrees = %+v", trees.Worktrees)
	}

	var v wire.VerifyOutput
	expect(t, e.get("/ws/verify/T1", &v), http.StatusOK, "")
	gates := map[string]bool{}
	for i, r := range v.Runs {
		gates[r.Gate] = true
		if r.Attempt != i+1 || !r.OK || !strings.Contains(r.Out, "OK verified") || r.Cmd == "" {
			t.Fatalf("run %d = %+v", i, r)
		}
	}
	if !gates["done"] || !gates["merge"] || v.Task != "T1" || v.ExitCode != 0 {
		t.Fatalf("verify = %+v", v)
	}

	// who wrote d1.txt: git blame on the integration branch names the agent and its task
	var f wire.WsContent
	expect(t, e.get("/ws/file?path=d1.txt", &f), http.StatusOK, "")
	if len(f.Blame) != 1 || f.Blame[0].Ag != "be-1" || f.Blame[0].Count != 2 || f.Blame[0].Task != "T1" || !f.Exact {
		t.Fatalf("blame = %+v exact %v", f.Blame, f.Exact)
	}
	var idx wire.WsIndex
	expect(t, e.get("/ws/index", &idx), http.StatusOK, "")
	found := false
	for _, r := range idx.Tree {
		if r.Path == "d1.txt" {
			found = true
			if r.Owner != "be-1" || r.Task != "T1" || r.Status != "A" {
				t.Fatalf("d1.txt row = %+v", r)
			}
		}
	}
	if idx.Isolation != "worktree" || !found {
		t.Fatalf("index: isolation %q, d1.txt listed %v", idx.Isolation, found)
	}

	// Apply the verified work as a commit: the dry run first, which needs no confirmation.
	var plan wire.AcceptResult
	expect(t, e.post("/ws/accept", wire.AcceptRequest{DryRun: true}, "", &plan), http.StatusOK, "")
	if !plan.DryRun || !plan.Waiting || !plan.CanCommit || strings.Join(plan.Files, ",") != "d1.txt" || plan.Commit != "" {
		t.Fatalf("dry run = %+v", plan)
	}
	if st := git(t, e.root, "status", "--porcelain"); !strings.Contains(st, "d1.txt") {
		t.Fatalf("a dry run changes nothing: %q", st)
	}
	expect(t, e.post("/ws/accept", wire.AcceptRequest{Message: "Add d1.txt (verified)"}, "", nil), http.StatusPreconditionRequired, "confirm_required")
	var res wire.AcceptResult
	expect(t, e.post("/ws/accept", wire.AcceptRequest{Message: "Add d1.txt (verified)"}, "accept:"+e.tab.TabID(), &res), http.StatusOK, "")
	if !res.Committed || res.Commit == "" || res.Branch == "" {
		t.Fatalf("accept = %+v", res)
	}
	if head := git(t, e.root, "rev-parse", "HEAD"); head != res.Commit {
		t.Fatalf("HEAD %s, commit %s", head, res.Commit)
	}
	if msg := git(t, e.root, "log", "-1", "--format=%s"); msg != "Add d1.txt (verified)" {
		t.Fatalf("commit message %q", msg)
	}
	if st := git(t, e.root, "status", "--porcelain"); st != "" {
		t.Fatalf("the checkout is clean afterwards: %q", st)
	}
	if !sawSys(e, "Committed 1 file(s)") {
		t.Fatal("no acknowledgement row")
	}
	expect(t, e.post("/ws/accept", wire.AcceptRequest{}, "accept:"+e.tab.TabID(), nil), http.StatusConflict, "nothing")

	// A change of the person's own blocks commits, and the dry run says why.
	writeFile(t, filepath.Join(e.root, "README.md"), "# mine\n")
	var dirty wire.AcceptResult
	expect(t, e.post("/ws/accept", wire.AcceptRequest{DryRun: true}, "", &dirty), http.StatusOK, "")
	if dirty.CanCommit || !strings.Contains(dirty.CommitBlocked, "README.md") || dirty.Waiting {
		t.Fatalf("dirty dry run = %+v", dirty)
	}
	expect(t, e.post("/ws/accept", wire.AcceptRequest{Mode: "edits"}, "accept:"+e.tab.TabID(), nil), http.StatusConflict, "nothing")
}

// A team in the shared checkout: the index names the worker and its task for the file it
// wrote (from the log's assignments), blame comes from the journal, and the worker's
// gate runs are its verify output.
func TestSharedTreeTeamOwnersTasksAndGateRuns(t *testing.T) {
	root := project(t, map[string]string{
		"README.md": "# demo\n",
		"verify.sh": "test -f d1.txt || { echo 'FAIL: no d1.txt'; exit 1; }\necho OK verified\n",
	})
	var mu sync.Mutex
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		_, role, tid := whoIs(c)
		n := turns(c)
		if role == "manager" {
			switch n {
			case 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("c1", "task", map[string]any{"action": "create", "title": "Add d1.txt", "role": "backend", "files": []string{"d1.txt"}}),
					call("s1", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("w1", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 120}),
				}}
			case 1:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("a1", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "all tasks accepted"}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "working", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "d1.txt", "content": "made by a worker\n"})}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d1", "task", map[string]any{"action": "done", "id": tid, "text": "added d1.txt"})}}
		}
		return mock.Reply{Text: "done"}
	})
	o := options(t, root, client, model)
	o.Swarm, o.Workers = true, 2
	o.Verify = "sh verify.sh"
	o.Isolation = "none"
	e := newEnv(t, root, o)
	if _, err := e.sess.Run(context.Background(), "add d1.txt"); err != nil {
		t.Fatal(err)
	}
	var idx wire.WsIndex
	expect(t, e.get("/ws/index", &idx), http.StatusOK, "")
	var row wire.WsFile
	for _, r := range idx.Tree {
		if r.Path == "d1.txt" {
			row = r
		}
	}
	if row.Owner != "be-1" || row.Task != "T1" || row.Status != "A" || row.Cp != "c01" {
		t.Fatalf("d1.txt row = %+v", row)
	}
	if c := idx.Cps[0]; len(c.Tasks) != 1 || c.Tasks[0] != "T1" || c.Changes[0].Task != "T1" {
		t.Fatalf("c01 = %+v", c)
	}
	var f wire.WsContent
	expect(t, e.get("/ws/file?path=d1.txt", &f), http.StatusOK, "")
	if len(f.Blame) != 1 || f.Blame[0].Ag != "be-1" || f.Blame[0].Task != "T1" || f.Blame[0].ID != "c01" || !f.Exact {
		t.Fatalf("blame = %+v exact %v", f.Blame, f.Exact)
	}
	var v wire.VerifyOutput
	expect(t, e.get("/ws/verify/T1", &v), http.StatusOK, "")
	if len(v.Runs) < 1 || v.Runs[0].Gate != "done" || !v.Runs[0].OK || !strings.Contains(v.Runs[0].Out, "OK verified") || v.Runs[0].Agent != "be-1" {
		t.Fatalf("verify = %+v", v)
	}
	expect(t, e.get("/ws/queue", nil), http.StatusConflict, "not_isolated")
}
