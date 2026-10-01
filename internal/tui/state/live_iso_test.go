package state

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The isolated session is a team whose writers each work in a git worktree of their own and whose finished work goes through the
// verifying merge queue (docs/SWARM-PROTOCOL.md): the log of it holds the workspace and merge events that no other session writes.
// Seven workers: three that change files of their own, two that change the same line of one file (the second to merge gets the
// conflict back and resolves it), and two whose changes are right alone and wrong together (the verifier fails on the second to
// merge, which fixes its part). The script is the one of internal/session's isolation test, cut to what this package needs; which
// of a pair merges first is decided by the merge queue, so the tests that fold the log count what it says.

var isoRecording recording

// isoLog is the log of the isolated session; a machine without git skips the tests that need it.
func isoLog(t testing.TB) []byte {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the isolated session needs git")
	}
	return isoRecording.get(t, "isolated", recordIsolated)
}

// isoVerify is the project's verifier: the number in count.txt must be the number of lines in list.txt, and shared.txt must not
// hold conflict markers.
const isoVerify = `want=$(cat count.txt)
have=$(wc -l < list.txt | tr -d ' ')
if [ "$want" != "$have" ]; then echo "FAIL: count.txt says $want but list.txt has $have lines"; exit 1; fi
if grep -q '<<<<<<<' shared.txt; then echo "FAIL: conflict markers in shared.txt"; exit 1; fi
echo OK
`

const isoGoal = "make the seven changes"

func makeIsoRepo(dir string) error {
	files := map[string]string{
		"README.md":  "# demo\n",
		"shared.txt": "line one\nline two\nline three\n",
		"list.txt":   "alpha\nbeta\n",
		"count.txt":  "2\n",
		"verify.sh":  isoVerify,
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func recordIsolated() ([]byte, error) {
	root, cleanup, err := tempRoot("state-iso-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return nil, err
	}
	if err := makeIsoRepo(repo); err != nil {
		return nil, err
	}
	sc := newIsoScript(7)
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, sc.respond)
	ts := srv.Start()
	defer ts.Close()
	prof := openaichat.DefaultProfile("iso", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "iso", BaseURL: ts.URL, Profile: &prof,
		Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	model := cost.Model{ID: "iso-model", ContextTokens: 1_000_000, MaxOutput: 8192, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 2, OutputPerM: 8, CacheReadPerM: 0.5, CacheWrite5mPerM: 2, CacheWrite1hPerM: 2}}

	ctx, cancel := context.WithTimeout(context.Background(), recordingGuard)
	defer cancel()
	sdir := filepath.Join(root, "session")
	s, err := session.New(ctx, session.Options{
		Cwd: repo, Root: repo, Home: filepath.Join(root, "home"), ID: "s-iso", Dir: sdir,
		Provider: client, ModelInfo: &model, Model: model.ID,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true, NoMCP: true,
		Swarm: true, MaxAgents: 12, Verify: "sh verify.sh", Isolation: "worktree",
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if _, err := s.Run(ctx, isoGoal); err != nil {
		return nil, fmt.Errorf("the isolated session: %w", err)
	}
	s.Finish(ctx)
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(sdir, "events.jsonl"))
}

// isoTasks are the titles and the files of the seven tasks, in the order of T1 to T7.
var isoTasks = []struct{ files, title string }{
	{"d1.txt", "Add d1.txt"}, {"d2.txt", "Add d2.txt"}, {"d3.txt", "Add d3.txt"},
	{"shared.txt", "Edit shared line two (A)"}, {"shared.txt", "Edit shared line two (B)"},
	{"list.txt,count.txt", "List gamma"}, {"list.txt,count.txt", "List delta"},
}

// isoScript plays every agent of the isolated session. The workers meet at a barrier after their edits, so that no merge begins
// before every tree exists and every edit is written: what they then do to each other is decided by git and the verifier.
type isoScript struct {
	workers int

	mu       sync.Mutex
	turn     map[string]int
	arrived  map[string]bool
	gate     chan struct{}
	accepted bool
}

func newIsoScript(workers int) *isoScript {
	return &isoScript{workers: workers, turn: map[string]int{}, arrived: map[string]bool{}, gate: make(chan struct{})}
}

// barrier blocks until every worker has made its edit. The wait is a guard against a hang, not a timing: a worker that never comes
// is an error of the script, and the recording fails with it.
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
	case <-time.After(recordingGuard):
	}
}

func (sc *isoScript) respond(c *mock.Call) mock.Reply {
	id, role, tid := whoIs(c)
	sc.mu.Lock()
	k := sc.turn[id]
	sc.turn[id]++
	sc.mu.Unlock()
	if role == "manager" {
		return sc.manager(c, k)
	}
	return sc.worker(c, id, tid, k)
}

func (sc *isoScript) manager(c *mock.Call, k int) mock.Reply {
	var ids []string
	for i := range isoTasks {
		ids = append(ids, fmt.Sprintf("T%d", i+1))
	}
	if k == 0 {
		var calls []mock.ToolCall
		for i, t := range isoTasks {
			calls = append(calls,
				call(fmt.Sprintf("c%d", i), "task", map[string]any{"action": "create", "title": t.title, "role": "backend", "files": strings.Split(t.files, ",")}),
				call(fmt.Sprintf("s%d", i), "spawn", map[string]any{"role": "backend", "task": ids[i]}))
		}
		return mock.Reply{Text: "plan", ToolCalls: append(calls, call("w", "wait", map[string]any{"until": ids, "timeout_sec": 120}))}
	}
	settled := strings.Contains(toolResults(c), "all awaited tasks settled")
	sc.mu.Lock()
	done := sc.accepted
	if settled && !done {
		sc.accepted = true
	}
	sc.mu.Unlock()
	switch {
	case settled && !done:
		var calls []mock.ToolCall
		for i, id := range ids {
			calls = append(calls, call(fmt.Sprintf("a%d", i), "task", map[string]any{"action": "accept", "id": id}))
		}
		return mock.Reply{Text: "accepting", ToolCalls: calls}
	case !done && k < 40:
		return mock.Reply{Text: "waiting", ToolCalls: []mock.ToolCall{call(fmt.Sprintf("w%d", k), "wait", map[string]any{"until": ids, "timeout_sec": 120})}}
	}
	return mock.Reply{Text: "all tasks accepted"}
}

func (sc *isoScript) worker(c *mock.Call, id, tid string, k int) mock.Reply {
	last := toolResults(c)
	summary := mock.Reply{Text: "summary from " + id}
	switch tid {
	case "T1", "T2", "T3":
		file := "d" + tid[1:] + ".txt"
		switch k {
		case 0:
			return mock.Reply{Text: "working", ToolCalls: []mock.ToolCall{
				call("w1", "write", map[string]any{"path": file, "content": "made by " + id + "\n"}),
				call("w2", "bash", map[string]any{"command": "ls -1"}),
			}}
		case 1:
			sc.barrier(id)
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d", "task", map[string]any{"action": "done", "id": tid, "text": "added " + file})}}
		}
	case "T4", "T5": // both change line two of shared.txt: the second to merge conflicts
		mine := map[string]string{"T4": "A", "T5": "B"}[tid]
		readIt := call("r", "read", map[string]any{"path": "shared.txt"})
		switch k {
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
	case "T6", "T7": // each adds a line to list.txt and bumps count.txt: right alone, wrong together
		line, list := "gamma", "alpha\nbeta\ngamma\n"
		if tid == "T7" {
			line, list = "delta", "delta\nalpha\nbeta\n"
		}
		switch k {
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
	}
	return summary
}
