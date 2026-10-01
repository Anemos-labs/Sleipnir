package session_test

// Worktree isolation through the assembled session: real tools, the real permission
// engine, the real workspace manager and merge queue, on a real git repository, with a
// scripted model behind the mock endpoint.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
)

// verifyScript is the project's verifier: the number in count.txt must be the number of
// lines in list.txt, and shared.txt must not hold conflict markers. Two changes that are
// each right on their own can be wrong together.
const verifyScript = `want=$(cat count.txt)
have=$(wc -l < list.txt | tr -d ' ')
if [ "$want" != "$have" ]; then echo "FAIL: count.txt says $want but list.txt has $have lines"; exit 1; fi
if grep -q '<<<<<<<' shared.txt; then echo "FAIL: conflict markers in shared.txt"; exit 1; fi
echo OK
`

// isoRepo makes the small project the isolation tests work on (under git, one commit).
func isoRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"README.md":  "# demo\n",
		"shared.txt": "line one\nline two\nline three\n",
		"list.txt":   "alpha\nbeta\n",
		"count.txt":  "2\n",
		"verify.sh":  verifyScript,
		"sub/keep":   "kept\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// toolResults are the results the model is answering: the tool messages after its last
// message.
func toolResults(c *mock.Call) []string {
	var out []string
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Role == "assistant" {
			break
		}
		if m.Role == "tool" {
			out = append([]string{m.Content}, out...)
		}
	}
	return out
}

func joined(ss []string) string { return strings.Join(ss, "\n") }

// isoScript is a scripted team: the manager creates a task per worker and spawns them
// all, waits, accepts everything; every worker follows the script of its task. The
// workers rendezvous after their first step, so that no merge can happen before every
// tree was made and every edit written: what they then do to each other is decided by
// git and the verifier, not by timing.
type isoScript struct {
	t     *testing.T
	repo  string
	trees string     // the directory of the session's trees
	turns [][]string // the tasks the manager creates in each of its turns, in order: "files:title"
	// files are what the workers of T1..T3 write (default d1.txt, d2.txt, d3.txt).
	files map[string]string
	// extraCalls are more calls a worker makes in its first step, by task: the probes an
	// isolated run must refuse (reaching into another tree, into the person's checkout).
	extraCalls map[string][]mock.ToolCall
	// beforeDone runs when a T1..T3 worker is about to call done (after its own edit).
	beforeDone func(agent string)
	workers    int

	mu       sync.Mutex
	arrived  map[string]bool
	gate     chan struct{}
	diag     func() string       // says where the swarm is, for a barrier that gives up (watch)
	listing  map[string]string   // agent -> what `ls` and `pwd` showed in its working directory
	extras   map[string][]string // agent -> what came back for its extra calls
	firstReq map[string]string
	systems  map[string]string
}

func newIsoScript(t *testing.T, repo, trees string, workers int) *isoScript {
	return &isoScript{t: t, repo: repo, trees: trees, workers: workers, arrived: map[string]bool{}, gate: make(chan struct{}),
		files:   map[string]string{"T1": "d1.txt", "T2": "d2.txt", "T3": "d3.txt"},
		listing: map[string]string{}, extras: map[string][]string{}, firstReq: map[string]string{}, systems: map[string]string{}}
}

// barrier blocks until every worker has finished its first step.
func (sc *isoScript) barrier(id string) {
	sc.mu.Lock()
	sc.arrived[id] = true
	if len(sc.arrived) == sc.workers {
		select {
		case <-sc.gate:
		default:
			close(sc.gate)
		}
	}
	gate := sc.gate
	sc.mu.Unlock()
	select {
	case <-gate:
	case <-time.After(60 * time.Second):
		sc.mu.Lock()
		arrived, diag := len(sc.arrived), sc.diag
		sc.mu.Unlock()
		where := ""
		if diag != nil {
			where = "\n" + diag()
		}
		sc.t.Errorf("%s waited for the other workers in vain: %d of %d arrived%s", id, arrived, sc.workers, where)
	}
}

// watch makes a barrier that gives up say where the swarm was: what each agent was doing, what each task's status was, and what the
// person had been told. A worker that never arrives is a worker that never started or that stopped early, and the first run on macOS
// showed that the failure alone does not say which.
func (sc *isoScript) watch(s *session.Session, n *notices) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.diag = func() string {
		var sb strings.Builder
		snap := s.Swarm.Board.Snapshot()
		sb.WriteString("agents:\n")
		for _, a := range snap.Agents {
			fmt.Fprintf(&sb, "  %s (%s) %s on %q: %s\n", a.ID, a.Role, a.State, a.Task, a.Line)
		}
		sb.WriteString("tasks:\n")
		for _, t := range snap.Tasks {
			fmt.Fprintf(&sb, "  %s %s owner %q attempts %d: %s\n", t.ID, t.Status, t.Owner, t.Attempts, t.Line)
		}
		if n != nil {
			n.mu.Lock()
			logs := append([]string(nil), n.logs...)
			n.mu.Unlock()
			if len(logs) > 40 {
				logs = logs[len(logs)-40:]
			}
			sb.WriteString("what the person was told:\n  " + strings.Join(logs, "\n  "))
		}
		return sb.String()
	}
}

func (sc *isoScript) respond(c *mock.Call) mock.Reply {
	id, role, tid := whoIs(c)
	n := assistantTurns(c)
	if role == "manager" {
		return sc.manager(n)
	}
	return sc.worker(c, id, tid, n)
}

// manager takes three requests per turn of the conversation: create, spawn and wait for
// that turn's tasks; accept them; answer.
func (sc *isoScript) manager(n int) mock.Reply {
	turn, step := n/3, n%3
	if turn >= len(sc.turns) {
		return mock.Reply{Text: "nothing more to do"}
	}
	first := 0 // the number of tasks created in earlier turns
	for _, earlier := range sc.turns[:turn] {
		first += len(earlier)
	}
	specs := sc.turns[turn]
	switch step {
	case 0:
		var calls []mock.ToolCall
		var ids []string
		for i, spec := range specs {
			file, title, _ := strings.Cut(spec, ":")
			tid := "T" + itoa(first+i+1)
			ids = append(ids, tid)
			calls = append(calls,
				call("c"+itoa(i), "task", map[string]any{"action": "create", "title": title, "role": "backend", "files": strings.Split(file, ",")}),
				call("s"+itoa(i), "spawn", map[string]any{"role": "backend", "task": tid}))
		}
		calls = append(calls, call("w", "wait", map[string]any{"until": ids, "timeout_sec": 120}))
		return mock.Reply{Text: "plan", ToolCalls: calls}
	case 1:
		var calls []mock.ToolCall
		for i := range specs {
			calls = append(calls, call("a"+itoa(i), "task", map[string]any{"action": "accept", "id": "T" + itoa(first+i+1)}))
		}
		return mock.Reply{Text: "accepting", ToolCalls: calls}
	}
	return mock.Reply{Text: "all tasks accepted"}
}

func (sc *isoScript) worker(c *mock.Call, id, tid string, n int) mock.Reply {
	last := joined(toolResults(c))
	switch tid {
	case "T1", "T2", "T3":
		file := sc.files[tid]
		switch n {
		case 0:
			sc.mu.Lock()
			sc.firstReq[id] = c.System + "\n" + messagesText(c)
			sc.systems[id] = c.System
			sc.mu.Unlock()
			calls := []mock.ToolCall{
				call("w1", "write", map[string]any{"path": file, "content": "made by " + id + "\n"}),
				call("w2", "bash", map[string]any{"command": "ls -1 && pwd"}),
			}
			return mock.Reply{Text: "working", ToolCalls: append(calls, sc.extraCalls[tid]...)}
		case 1:
			res := toolResults(c)
			sc.mu.Lock()
			if len(res) > 1 {
				sc.listing[id] = res[1]
			}
			if len(res) > 2 {
				sc.extras[id] = res[2:]
			}
			sc.mu.Unlock()
			if sc.beforeDone != nil {
				sc.beforeDone(id)
			}
			sc.barrier(id)
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d", "task", map[string]any{"action": "done", "id": tid, "text": "added " + file})}}
		}
		return mock.Reply{Text: "summary from " + id}

	case "T4", "T5": // both change line two of shared.txt: the second to merge conflicts
		mine := "A"
		if tid == "T5" {
			mine = "B"
		}
		readIt := call("r", "read", map[string]any{"path": "shared.txt"})
		switch n {
		case 0:
			return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{readIt}}
		case 1:
			return mock.Reply{Text: "editing", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "shared.txt", "content": "line one\nline two by " + mine + "\nline three\n"})}}
		case 2:
			sc.barrier(id)
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d", "task", map[string]any{"action": "done", "id": tid, "text": "changed line two"})}}
		case 3:
			if strings.Contains(last, "conflict") { // the markers are in its tree: read them, then resolve
				return mock.Reply{Text: "reading the conflict", ToolCalls: []mock.ToolCall{readIt}}
			}
		case 4:
			return mock.Reply{Text: "resolving", ToolCalls: []mock.ToolCall{call("w2", "write", map[string]any{"path": "shared.txt", "content": "line one\nline two by A and B\nline three\n"})}}
		case 5:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d2", "task", map[string]any{"action": "done", "id": tid, "text": "resolved together with the other edit"})}}
		}
		return mock.Reply{Text: "summary from " + id}

	case "T6", "T7": // each adds a line to list.txt and bumps count.txt: right alone, wrong together
		line, list := "gamma", "alpha\nbeta\ngamma\n"
		if tid == "T7" {
			line, list = "delta", "delta\nalpha\nbeta\n"
		}
		switch n {
		case 0:
			return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{
				call("r1", "read", map[string]any{"path": "list.txt"}), call("r2", "read", map[string]any{"path": "count.txt"})}}
		case 1:
			return mock.Reply{Text: "editing", ToolCalls: []mock.ToolCall{
				call("w1", "write", map[string]any{"path": "list.txt", "content": list}),
				call("w2", "write", map[string]any{"path": "count.txt", "content": "3\n"}),
			}}
		case 2:
			sc.barrier(id)
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d", "task", map[string]any{"action": "done", "id": tid, "text": "added " + line})}}
		case 3:
			if strings.Contains(last, "failed on the merged result") { // the failure reproduces in its tree
				return mock.Reply{Text: "reading the count", ToolCalls: []mock.ToolCall{call("r3", "read", map[string]any{"path": "count.txt"})}}
			}
		case 4:
			return mock.Reply{Text: "fixing", ToolCalls: []mock.ToolCall{call("f1", "write", map[string]any{"path": "count.txt", "content": "4\n"})}}
		case 5:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d2", "task", map[string]any{"action": "done", "id": tid, "text": "count follows the list again"})}}
		}
		return mock.Reply{Text: "summary from " + id}
	}
	return mock.Reply{Text: "nothing to do for " + id}
}

func messagesText(c *mock.Call) string {
	var sb strings.Builder
	for _, m := range c.Messages {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// isoOptions are the session options of an isolation test: a batch swarm with the
// project's verifier, its trees under a directory the test owns.
func isoOptions(t *testing.T, repo string, client *openaichat.Client, model cost.Model, id string) session.Options {
	t.Helper()
	home := t.TempDir()
	o := opts(t, repo, client, model)
	o.Home, o.ID = home, id
	o.Swarm, o.MaxAgents = true, 12
	o.Verify = "sh verify.sh"
	o.Isolation = "worktree"
	return o
}

// treesOf is the directory a session's worktrees are made in: under the cache
// directory of the session's home, never under its state directory.
func treesOf(o session.Options, sid string) string {
	return filepath.Join(o.Home, ".cache", "sleipnir", "worktrees", sid)
}

func statusLines(t *testing.T, repo string) []string {
	t.Helper()
	out := git(t, repo, "status", "--porcelain")
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	sort.Strings(lines)
	return lines
}

func eventTypes(t *testing.T, dir string) map[string]int {
	t.Helper()
	m := map[string]int{}
	for _, e := range readEvents(t, dir) {
		m[e.Type]++
	}
	return m
}

// The whole thing, once: three workers on separate files, two on the same line of one
// file, two whose changes are each fine and wrong together. Every one gets a tree of its
// own; the conflict and the verifier failure come back to the worker that was second;
// the checkout ends with the merged, verified result as uncommitted edits, exactly what
// the same team leaves in a shared tree; the trees are gone.
func TestIsolatedSwarmMergesAConflictAndAVerifierFailureAndAppliesTheResult(t *testing.T) {
	repo := isoRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	const sid = "s-iso"
	var sc *isoScript
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
	o := isoOptions(t, repo, client, model, sid)
	trees := treesOf(o, sid)
	sc = newIsoScript(t, repo, trees, 7)
	sc.extraCalls = map[string][]mock.ToolCall{
		// reach into another worker's tree
		"T1": {call("x1", "read", map[string]any{"path": filepath.Join(trees, "be-2", "README.md")})},
		// and into the person's checkout
		"T2": {call("x2", "write", map[string]any{"path": filepath.Join(repo, "intruder.txt"), "content": "not through the queue\n"})},
	}
	sc.turns = [][]string{{
		"d1.txt:Add d1.txt", "d2.txt:Add d2.txt", "d3.txt:Add d3.txt",
		"shared.txt:Edit shared line two (A)", "shared.txt:Edit shared line two (B)",
		"list.txt,count.txt:List gamma", "list.txt,count.txt:List delta",
	}}
	sink := &notices{}
	o.Sink = sink
	o.NewSink = func(string) agent.Sink { return sink }
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sc.watch(s, sink)

	res, err := s.Run(context.Background(), "make the seven changes")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "all tasks accepted" {
		t.Fatalf("manager result %q", res.Text)
	}
	for _, tid := range []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7"} {
		if tk, _ := s.Swarm.Board.Snapshot().Task(tid); tk.Status != swarm.StatusDone {
			t.Errorf("%s = %s, want done", tid, tk.Status)
		}
	}

	// The result is in the checkout, applied when the manager stopped.
	for i, want := range map[string]string{"d1.txt": "made by be-1\n", "d2.txt": "made by be-2\n", "d3.txt": "made by be-3\n"} {
		if got := readFile(t, filepath.Join(repo, i)); got != want {
			t.Errorf("%s = %q, want %q", i, got, want)
		}
	}
	if got := readFile(t, filepath.Join(repo, "shared.txt")); got != "line one\nline two by A and B\nline three\n" {
		t.Errorf("shared.txt = %q: the conflict was not resolved", got)
	}
	if got := readFile(t, filepath.Join(repo, "list.txt")); got != "delta\nalpha\nbeta\ngamma\n" {
		t.Errorf("list.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(repo, "count.txt")); got != "4\n" {
		t.Errorf("count.txt = %q: the verifier failure was not fixed", got)
	}
	// The project's own verifier agrees, run in the person's checkout.
	c := exec.Command("sh", "verify.sh")
	c.Dir = repo
	if out, err := c.CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("the verifier fails in the checkout: %v %s", err, out)
	}
	// Nothing reached it any other way: no intruder, HEAD unchanged, only these edits.
	if _, err := os.Stat(filepath.Join(repo, "intruder.txt")); err == nil {
		t.Fatal("a worker wrote into the person's checkout, past the merge queue")
	}
	if got := git(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved from %s to %s without --commit", head, got)
	}
	want := []string{" M count.txt", " M list.txt", " M shared.txt", "?? d1.txt", "?? d2.txt", "?? d3.txt"}
	if got := statusLines(t, repo); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("git status:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !sink.has("integrate: Applied 6 file(s) to your working tree as uncommitted changes") {
		t.Errorf("the person was not told what was applied: %v", sink.logs)
	}

	// No cross-visibility: a worker's listing shows the project and its own file, never a
	// sibling's uncommitted one, and reaching into another tree or into the checkout is refused.
	for id, other := range map[string][]string{"be-1": {"d2.txt", "d3.txt"}, "be-2": {"d1.txt", "d3.txt"}, "be-3": {"d1.txt", "d2.txt"}} {
		sc.mu.Lock()
		ls := sc.listing[id]
		sc.mu.Unlock()
		if !strings.Contains(ls, "README.md") || !strings.Contains(ls, "d"+id[len(id)-1:]+".txt") {
			t.Errorf("%s's listing %q shows neither the project nor its own file", id, ls)
		}
		for _, f := range other {
			if strings.Contains(ls, f) {
				t.Errorf("%s sees %s, which another worker has not merged: %q", id, f, ls)
			}
		}
		if !strings.Contains(ls, "/"+sid+"/"+id) {
			t.Errorf("%s's working directory is not its own tree: %q", id, ls)
		}
	}
	sc.mu.Lock()
	probe1, probe2 := joined(sc.extras["be-1"]), joined(sc.extras["be-2"])
	sc.mu.Unlock()
	if !strings.Contains(probe1, "works only inside its own tree") {
		t.Errorf("be-1 read another worker's tree: %q", probe1)
	}
	if !strings.Contains(probe2, "works only inside its own tree") {
		t.Errorf("be-2 wrote into the person's checkout: %q", probe2)
	}

	// The prompt bytes do not depend on the trees: every worker's system prompt is the
	// same, and no request names the directory of a tree.
	sc.mu.Lock()
	var first string
	for id, sys := range sc.systems {
		if first == "" {
			first = sys
		} else if sys != first {
			t.Errorf("%s's system prompt differs from another worker's", id)
		}
	}
	for id, req := range sc.firstReq {
		if strings.Contains(req, "worktrees") || strings.Contains(req, o.Home) {
			t.Errorf("%s's first request names a tree or the state directory", id)
		}
	}
	sc.mu.Unlock()

	// What happened is in the log, and Finish accounts for the whole run.
	et := eventTypes(t, s.Dir)
	for typ, min := range map[string]int{
		events.TypeWorkspaceCreate: 7, events.TypeMergeMerged: 7, events.TypeMergeConflict: 1, events.TypeMergeVerifyFail: 1,
		events.TypeMergeRolledBack: 1, events.TypeTaskMerge: 9, events.TypeSwarmIntegration: 1,
	} {
		if et[typ] < min {
			t.Errorf("%d %s events, want at least %d (%v)", et[typ], typ, min, et)
		}
	}
	rep := s.Finish(context.Background())
	if rep == nil || !rep.Applied || rep.Committed || len(rep.Kept) != 0 {
		t.Fatalf("finish report: %+v", rep)
	}
	if got := strings.Join(rep.Files, ","); got != "count.txt,d1.txt,d2.txt,d3.txt,list.txt,shared.txt" {
		t.Errorf("finish names %s", got)
	}
	if again := s.Finish(context.Background()); again != rep {
		t.Error("Finish is not idempotent")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Cleaned up: no trees, no branches, no registrations.
	if ents, _ := os.ReadDir(trees); len(ents) != 0 {
		t.Errorf("the session's trees were left behind: %v", ents)
	}
	if out := git(t, repo, "worktree", "list", "--porcelain"); strings.Count(out, "worktree ") != 1 {
		t.Errorf("worktrees are still registered:\n%s", out)
	}
	if out := git(t, repo, "branch", "--list", "sleipnir/*"); out != "" {
		t.Errorf("branches were left behind:\n%s", out)
	}
	if got := statusLines(t, repo); len(got) != len(want) {
		t.Errorf("closing changed the checkout: %v", got)
	}
}
