package session_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

func TestIsolatedTeamCrashHelper(t *testing.T) {
	if os.Getenv("SLEIPNIR_ISOLATED_CRASH_HELPER") != "1" {
		return
	}
	repo, home := os.Getenv("SLEIPNIR_CRASH_REPO"), os.Getenv("SLEIPNIR_CRASH_HOME")
	var s *session.Session
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		_, role, _ := whoIs(c)
		if role == "manager" {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				call("spawn", "spawn", map[string]any{"role": "backend", "task": "Complete preserved work", "files": []string{"unfinished.txt"}}),
				call("wait", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 120}),
			}}
		}
		if assistantTurns(c) == 0 {
			return mock.Reply{ToolCalls: []mock.ToolCall{call("write", "write", map[string]any{"path": "unfinished.txt", "content": "UNFINISHED_WORK\n"})}}
		}
		if err := s.Log.Flush(); err != nil {
			t.Error(err)
		}
		fmt.Println("resume-ready")
		select {} // The parent kills this process while its model call is outstanding.
	})
	o := isoOptions(t, repo, client, model, "crashed-team")
	o.Home, o.Dir = home, filepath.Join(home, ".sleipnir", "sessions", o.ID)
	var err error
	s, err = session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "complete preserved work"); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash helper unexpectedly returned")
}

func TestIsolatedTeamResumeAfterProcessDeath(t *testing.T) {
	t.Run("continue editing", func(t *testing.T) { isolatedTeamResumeAfterProcessDeath(t, true) })
	t.Run("submit preserved edits", func(t *testing.T) { isolatedTeamResumeAfterProcessDeath(t, false) })
}

func isolatedTeamResumeAfterProcessDeath(t *testing.T, editAgain bool) {
	repo, home := isoRepo(t), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIsolatedTeamCrashHelper$")
	child.Env = append(os.Environ(), "SLEIPNIR_ISOLATED_CRASH_HELPER=1", "SLEIPNIR_CRASH_REPO="+repo, "SLEIPNIR_CRASH_HOME="+home)
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	reader := bufio.NewReader(output)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "resume-ready" {
		tail, _ := io.ReadAll(reader)
		_ = child.Process.Kill()
		_ = child.Wait()
		waited = true
		t.Fatalf("child did not reach interruption point: %q %v\n%s\n%s", line, err, tail, &stderr)
	}
	dir := filepath.Join(home, ".sleipnir", "sessions", "crashed-team")
	managerStep, workerStep := 0, 0
	history := false
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		_, role, _ := whoIs(c)
		if role == "manager" {
			step := managerStep
			managerStep++
			switch step {
			case 0:
				return mock.Reply{ToolCalls: []mock.ToolCall{
					call("resume", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("wait", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 120}),
				}}
			case 1:
				return mock.Reply{ToolCalls: []mock.ToolCall{call("accept", "task", map[string]any{"action": "accept", "id": "T1"})}}
			default:
				return mock.Reply{Text: "recovered work accepted"}
			}
		}
		step := workerStep
		workerStep++
		switch step {
		case 0:
			messages, _ := json.Marshal(c.Messages)
			history = strings.Contains(string(messages), "UNFINISHED_WORK")
			return mock.Reply{ToolCalls: []mock.ToolCall{call("reread", "read", map[string]any{"path": "unfinished.txt"})}}
		case 1:
			if editAgain {
				return mock.Reply{ToolCalls: []mock.ToolCall{call("finish", "write", map[string]any{"path": "unfinished.txt", "content": "UNFINISHED_WORK\ncompleted after recovery\n"})}}
			}
			fallthrough
		case 2:
			return mock.Reply{ToolCalls: []mock.ToolCall{call("done", "task", map[string]any{"action": "done", "id": "T1", "text": "continued preserved edits"})}}
		default:
			return mock.Reply{Text: "worker finished"}
		}
	})
	o := isoOptions(t, repo, client, model, "crashed-team")
	o.Home, o.Resume = home, dir
	if competing, err := session.New(context.Background(), o); err == nil {
		competing.Close()
		t.Fatal("took over a live session")
	} else if !strings.Contains(err.Error(), "in use") {
		t.Fatalf("live session error: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	waited = true
	tree := filepath.Join(treesOf(o, o.ID), "be-1", "unfinished.txt")
	if got := readFile(t, tree); got != "UNFINISHED_WORK\n" {
		t.Fatalf("crashed worker state: %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "unfinished.txt")); !os.IsNotExist(err) {
		t.Fatal("unfinished work bypassed integration")
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if task, _ := s.Swarm.Board.Snapshot().Task("T1"); task.Status != swarm.StatusTodo || task.Owner != "be-1" {
		t.Fatalf("lost recovered ownership: %+v", task)
	}
	if info, _ := s.Swarm.Board.Snapshot().Agent("be-1"); info.State != "idle" {
		t.Fatalf("recovered worker is not idle: %+v", info)
	}
	if _, err := s.Run(context.Background(), "continue the unfinished task"); err != nil {
		t.Fatal(err)
	}
	if !history {
		t.Fatal("worker did not recover its pre-crash context")
	}
	want := "UNFINISHED_WORK\n"
	if editAgain {
		want += "completed after recovery\n"
	}
	if got := readFile(t, filepath.Join(repo, "unfinished.txt")); got != want {
		t.Fatalf("recovered result: %q", got)
	}
	if task, _ := s.Swarm.Board.Snapshot().Task("T1"); task.Status != swarm.StatusDone || task.Owner != "be-1" {
		t.Fatalf("recovered task did not finish: %+v", task)
	}
}

func TestIsolatedTeamResumeKeepsBaseCursorAndWorkerIDs(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "patch"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			repo := isoRepo(t)
			if !commit {
				if err := os.WriteFile(filepath.Join(repo, "original.txt"), []byte("user's initial changes\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var sc *isoScript
			client, model := startMock(t, func(c *mock.Call) mock.Reply { return sc.respond(c) })
			o := isoOptions(t, repo, client, model, "resumable-team")
			o.Commit = commit
			sc = newIsoScript(t, repo, treesOf(o, o.ID), 1)
			sc.turns = [][]string{{"d1.txt:First change"}, {"d2.txt:Second change"}}
			s1, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s1.Run(context.Background(), "make the first change"); err != nil {
				t.Fatal(err)
			}
			if err := s1.Close(); err != nil {
				t.Fatal(err)
			}
			spent := s1.Swarm.TotalCost()
			originalBase := git(t, repo, "rev-parse", "sleipnir/"+s1.ID+"/_resume")
			if !session.Resumable(s1.Dir) {
				t.Fatal("isolated session not listed as resumable")
			}
			if !commit {
				// An unrelated edit made between runs must not become the new base.
				if err := os.WriteFile(filepath.Join(repo, "later.txt"), []byte("human edit between runs\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			o.Resume, o.Isolation, o.Commit = s1.Dir, "", false
			s2, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			if got := s2.Swarm.TotalCost(); got != spent {
				t.Fatalf("resume changed spending: %g -> %g", spent, got)
			}
			if s2.ID != s1.ID {
				t.Fatalf("session identity changed: %s -> %s", s1.ID, s2.ID)
			}
			if task, _ := s2.Swarm.Board.Snapshot().Task("T1"); task.Status != swarm.StatusDone {
				t.Fatalf("completed task reset: %+v", task)
			}
			if _, err := s2.Run(context.Background(), "make the second change"); err != nil {
				t.Fatal(err)
			}
			for file, want := range map[string]string{"d1.txt": "made by be-1\n", "d2.txt": "made by be-2\n"} {
				if got := readFile(t, filepath.Join(repo, file)); got != want {
					t.Fatalf("%s: %q", file, got)
				}
			}
			if rep := s2.Finish(context.Background()); rep == nil || !rep.Applied || rep.Committed != commit || rep.Base != originalBase {
				t.Fatalf("resumed integration: %+v", rep)
			}
			if !commit {
				if got := readFile(t, filepath.Join(repo, "original.txt")); got != "user's initial changes\n" {
					t.Fatal("original user edits lost")
				}
				if got := readFile(t, filepath.Join(repo, "later.txt")); got != "human edit between runs\n" {
					t.Fatal("later user edits lost")
				}
			}
			if err := s2.Close(); err != nil {
				t.Fatal(err)
			}
			var workerRestored bool
			for _, e := range readEvents(t, s2.Dir) {
				if e.Type == events.TypeAgentRestore && e.Agent == "be-1" {
					workerRestored = true
				}
			}
			if !workerRestored {
				t.Fatal("worker conversation was not restored")
			}
		})
	}
}

func TestIsolatedTeamResumeRejectsChangedLocationOrModeWithoutDeletingWork(t *testing.T) {
	repo := isoRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ready"} })
	o := isoOptions(t, repo, client, model, "resume-contract")
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "remember this team"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*session.Options){
		func(o *session.Options) { o.Isolation = "none" },
		func(o *session.Options) { o.Swarm = false },
		func(o *session.Options) { o.Commit = true },
		func(o *session.Options) { o.Cwd = filepath.Join(repo, "sub") },
		func(o *session.Options) { o.Root, o.Cwd = isoRepo(t), ""; o.Cwd = o.Root },
		func(o *session.Options) { o.Home = t.TempDir() },
	} {
		bad := o
		bad.Resume = s.Dir
		change(&bad)
		if resumed, err := session.New(context.Background(), bad); err == nil {
			resumed.Close()
			t.Fatal("incompatible resume succeeded")
		}
		if got := git(t, repo, "branch", "--list", "sleipnir/"+s.ID+"/*"); !strings.Contains(got, "_integration") || !strings.Contains(got, "_resume") {
			t.Fatalf("failed resume deleted recovery refs: %s", got)
		}
	}
	o.Resume = s.Dir
	resumed, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatalf("valid resume after failures: %v", err)
	}
	resumed.Close()
}

func TestIsolatedTeamResumeRetainsServiceSpending(t *testing.T) {
	repo := isoRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ready"} })
	o := isoOptions(t, repo, client, model, "service-spending")
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "remember this team"); err != nil {
		t.Fatal(err)
	}
	spent := s.Swarm.TotalCost()
	// Service agents do not become workers on resume, but their ledger survives.
	blobs, err := events.NewDirBlobs(filepath.Join(s.Dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(agent.Snapshot{Version: 1, Agent: "mail-1", Role: "mailman", CostUSD: 2})
	hash, err := blobs.Put(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.Log.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": "mail-1", "role": "mailman", "service": true})
	s.Log.Emit("mail-1", events.TypeAgentSnapshot, map[string]any{"blob": hash})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	o.Resume = s.Dir
	s, err = session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Swarm.TotalCost(); got != spent+2 {
		t.Fatalf("service spending lost: got %g, want %g", got, spent+2)
	}
	if _, ok := s.Swarm.Board.Snapshot().Agent("mail-1"); ok {
		t.Fatal("service agent became a recovered worker")
	}
}

func TestIsolatedTeamResumeDoesNotResurrectRetiredWorkers(t *testing.T) {
	for _, preparedTree := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared tree=%t", preparedTree), func(t *testing.T) {
			repo := isoRepo(t)
			client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ready"} })
			o := isoOptions(t, repo, client, model, "retired-workers")
			o.MaxAgents = 3 // manager, one existing worker, and one interrupted spawn
			s, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			if _, err := s.Run(context.Background(), "remember this team"); err != nil {
				t.Fatal(err)
			}
			spent := s.Swarm.TotalCost()
			r, err := gitx.Open(repo)
			if err != nil {
				t.Fatal(err)
			}
			mgr := &workspace.Manager{Repo: r, Dir: treesOf(o, s.ID), Prefix: "sleipnir/" + s.ID, Base: "sleipnir/" + s.ID + "/_resume"}
			blobs, err := events.NewDirBlobs(filepath.Join(s.Dir, "blobs"))
			if err != nil {
				t.Fatal(err)
			}
			var retained []string
			// Record retirement and spawn interruption boundaries with real board
			// operations and released Git trees. Retired trees contain unmerged work;
			// one retired task remains in review, while another was requeued.
			for i := 1; i <= 6; i++ {
				id := fmt.Sprintf("be-%d", i)
				if i == 6 {
					id = "orphan-1" // a leftover tree is not evidence of roster membership
				}
				if i != 4 || preparedTree {
					tree, err := mgr.Create(context.Background(), id, workspace.CreateOptions{})
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(mgr.Dir, id, "unfinished.txt")
					if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					if err := tree.Release(context.Background()); err != nil {
						t.Fatal(err)
					}
					retained = append(retained, path)
				}
				if i == 6 {
					continue
				}
				task, err := s.Swarm.Board.CreateTask("mgr", swarm.TaskSpec{Title: id + " work", Role: "backend"})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Swarm.Board.Assign("mgr", id, task.ID); err != nil {
					t.Fatal(err)
				}
				s.Log.Emit(id, "agent.prepare", map[string]any{"id": id, "role": "backend", "task": task.ID})
				if i <= 3 {
					s.Swarm.Board.SetAgent(swarm.AgentInfo{ID: id, Role: "backend", State: "idle", Task: task.ID})
					s.Log.Emit("swarm", events.TypeAgentSpawn, map[string]any{"id": id, "role": "backend", "task": task.ID})
				}
				if i == 1 || i == 3 {
					if err := s.Swarm.Board.Submit(id, task.ID, "ready for review", ""); err != nil {
						t.Fatal(err)
					}
				}
				if i <= 2 {
					raw, _ := json.Marshal(agent.Snapshot{Version: 1, Agent: id, Role: "backend", CostUSD: 2})
					hash, err := blobs.Put(raw)
					if err != nil {
						t.Fatal(err)
					}
					s.Log.Emit(id, events.TypeAgentSnapshot, map[string]any{"blob": hash})
					s.Swarm.Board.RequeueOwned(id, "its worker was retired")
					s.Swarm.Board.RemoveAgent(id)
				}
				if i == 5 {
					s.Swarm.Board.RequeueOwned(id, "the worker could not be started")
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			o.Resume = s.Dir
			resumed, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close()
			snap := resumed.Swarm.Board.Snapshot()
			if len(snap.Agents) != 3 {
				t.Fatalf("unexpected recovered roster: %+v", snap.Agents)
			}
			for _, id := range []string{"be-3", "be-4"} {
				if a, ok := snap.Agent(id); !ok || a.State != "idle" {
					t.Fatalf("worker %s was not recovered idle: %+v", id, a)
				}
			}
			for _, id := range []string{"T1", "T2", "T5"} {
				if task, ok := snap.Task(id); !ok || task.Status != swarm.StatusTodo || task.Owner != "" {
					t.Fatalf("retired or rolled-back task stayed reserved: %+v", task)
				}
			}
			if task, _ := snap.Task("T4"); task.Owner != "be-4" || task.Status != swarm.StatusTodo {
				t.Fatalf("interrupted spawn lost its assignment: %+v", task)
			}
			if got := resumed.Swarm.TotalCost(); got != spent+4 {
				t.Fatalf("retired spending lost: got %g, want %g", got, spent+4)
			}
			for _, path := range retained {
				if got := readFile(t, path); got != filepath.Base(filepath.Dir(path))+"\n" {
					t.Fatalf("preserved tree changed: %s: %q", path, got)
				}
			}
			if err := resumed.Swarm.Retire("be-3"); err != nil {
				t.Fatal(err)
			}
			resumed.Swarm.Start(context.Background())
			if id, err := resumed.Swarm.Spawn(swarm.SpawnReq{By: "mgr", Role: "backend", Title: "new assignment"}); err != nil || id != "be-6" {
				t.Fatalf("worker IDs reused after recovery: %q, %v", id, err)
			}
		})
	}
}

func TestResumeCorruptionPolicyPreservesIsolatedRecoveryState(t *testing.T) {
	for _, name := range []string{"solo", "shared", "isolated"} {
		t.Run(name, func(t *testing.T) {
			repo := isoRepo(t)
			client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ready"} })
			o := isoOptions(t, repo, client, model, "corrupt-log")
			o.Swarm = name != "solo"
			if name != "isolated" {
				o.Isolation = "none"
			}
			s, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Run(context.Background(), "remember this"); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(filepath.Join(s.Dir, "events.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.WriteString("malformed complete event\n")
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("append damaged line: %v, %v", writeErr, closeErr)
			}
			o.Resume = s.Dir
			resumed, err := session.New(context.Background(), o)
			if name == "isolated" {
				if err == nil {
					resumed.Close()
					t.Fatal("isolated recovery guessed across a lost event")
				}
				if refs := git(t, repo, "branch", "--list", "sleipnir/"+s.ID+"/*"); !strings.Contains(refs, "_resume") || !strings.Contains(refs, "_integration") {
					t.Fatalf("recovery refs lost: %s", refs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ordinary resume lost its corruption tolerance: %v", err)
			}
			if err := resumed.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
