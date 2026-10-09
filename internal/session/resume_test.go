package session_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

func TestResumeContinuesTheConversationAndTheLog(t *testing.T) {
	repo := newRepo(t)
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-abcdef")
	var prompts []string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		var all []string
		for _, m := range c.Messages {
			all = append(all, m.Content)
		}
		prompts = append(prompts, strings.Join(all, "\n"))
		return mock.Reply{Text: "noted"}
	})

	o := opts(t, repo, client, model)
	o.Dir = dir
	s1, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Run(context.Background(), "remember the codeword PINEAPPLE"); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	o2 := opts(t, repo, client, model)
	o2.Resume = dir
	s2, err := session.New(context.Background(), o2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.ID != "20260101-000000-abcdef" || s2.Dir != dir {
		t.Fatalf("a resumed session continues under its own id and directory: %s %s", s2.ID, s2.Dir)
	}
	if _, err := s2.Run(context.Background(), "what was the codeword?"); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil { // the log group-commits; close it before reading the file
		t.Fatal(err)
	}
	last := prompts[len(prompts)-1]
	for _, want := range []string{"remember the codeword PINEAPPLE", "noted", "what was the codeword?"} {
		if !strings.Contains(last, want) {
			t.Errorf("the resumed model does not see %q", want)
		}
	}

	// One log, continued: two session.start events, the second marked resumed, and a restore record.
	var starts, resumed, restores int
	var lastSeq uint64
	for _, e := range readEvents(t, dir) {
		if e.Seq <= lastSeq {
			t.Fatalf("sequence numbers must keep increasing across a resume: %d after %d", e.Seq, lastSeq)
		}
		lastSeq = e.Seq
		switch e.Type {
		case events.TypeSessionStart:
			starts++
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			if d["resumed"] == true {
				resumed++
			}
		case events.TypeAgentRestore:
			restores++
		}
	}
	if starts != 2 || resumed != 1 || restores != 1 {
		t.Errorf("session.start=%d resumed=%d agent.restore=%d", starts, resumed, restores)
	}
}

func TestResumeLatestPicksTheNewestSessionOfThisProject(t *testing.T) {
	repo, other := newRepo(t), newRepo(t)
	state := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", state)
	var last string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		var all []string
		for _, m := range c.Messages {
			all = append(all, m.Content)
		}
		last = strings.Join(all, "\n")
		return mock.Reply{Text: "ok"}
	})
	run := func(root, id, goal string) {
		o := opts(t, root, client, model)
		o.Home = t.TempDir()
		o.Dir = filepath.Join(state, "sessions", id)
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(context.Background(), goal); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	run(repo, "20260101-000000-aaaaaa", "first goal of the project")
	run(repo, "20260102-000000-bbbbbb", "second goal of the project")
	run(other, "20260103-000000-cccccc", "a goal in another project")

	o := opts(t, repo, client, model)
	o.Home = t.TempDir()
	o.Resume = "latest"
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.ID != "20260102-000000-bbbbbb" {
		t.Fatalf("latest resumed %s, want the newest session of this project", s.ID)
	}
	if _, err := s.Run(context.Background(), "carry on"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last, "second goal of the project") || strings.Contains(last, "another project") || strings.Contains(last, "first goal") {
		t.Errorf("the wrong conversation was resumed:\n%s", last)
	}
}

func TestResumeErrorsAreClear(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	for spec, want := range map[string]string{
		"no-such-id": "no session",
		"latest":     "no session",
		t.TempDir():  "not a session directory",
	} {
		o := opts(t, repo, client, model)
		o.Home = t.TempDir()
		o.Resume = spec
		_, err := session.New(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("resume %q: %v, want mention of %q", spec, err, want)
		}
	}
	// A session that never finished a turn has no snapshot.
	empty := filepath.Join(t.TempDir(), "s")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empty, "events.jsonl"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	o := opts(t, repo, client, model)
	o.Resume = empty
	if _, err := session.New(context.Background(), o); err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Errorf("resuming a session without a snapshot: %v", err)
	}
	// A team's session that never finished a turn has none either.
	o = opts(t, repo, client, model)
	o.Swarm, o.Resume = true, empty
	if _, err := session.New(context.Background(), o); err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Errorf("resuming a team's session without a snapshot: %v", err)
	}
}

func TestCompactThroughTheSession(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.LastUser(), "<compactor-task>") {
			n := 0
			for _, m := range c.Messages {
				if m.Role == "assistant" {
					n++
				}
			}
			return mock.Reply{Text: `{"keep_from":"t` + itoa(2*n-3) + `","spine":[{"turns":"t1-t` + itoa(2*n-4) + `","line":"listed and read files"}],"mask":[],"notes":[],"promote":[]}`}
		}
		if assistantTurns(c) < 6 {
			return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{call("c"+itoa(assistantTurns(c)), "read", map[string]any{"path": "main.go"})}}
		}
		return mock.Reply{Text: "done"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Nothing has run yet: compacting is a no-op, reported as such, not an error.
	if rep, err := s.Compact(context.Background(), ""); err != nil || rep.Mode != "none" {
		t.Errorf("compacting before any goal: %+v, %v", rep, err)
	}
	if _, err := s.Run(context.Background(), "read the code"); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Compact(context.Background(), "the entry point")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mode != "model" || rep.FoldedTurns == 0 {
		t.Fatalf("report: %+v", rep)
	}
	if !strings.Contains(s.Agent.Stack().Spine.Text(), "listed and read files") {
		t.Error("the spine does not carry the digest")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// Legacy isolated sessions lack the original base and integration-mode metadata.
// "latest" reports that limitation instead of silently choosing an older session.
func TestResumeOfTeamSessionsAndOfIsolatedOnes(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	sessions := filepath.Join(home, ".sleipnir", "sessions")
	forge := func(id, agent string, swarm bool, isolation string) string {
		dir := filepath.Join(sessions, id)
		l, err := events.Open(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		start := map[string]any{"swarm": swarm, "root": root}
		if isolation != "" {
			start["isolation"] = isolation
		}
		l.Emit("", events.TypeSessionStart, start)
		l.Emit(agent, events.TypeAgentSnapshot, map[string]any{"blob": "x"})
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	single := forge("20260101-000000-aaaaaa", "main", false, "")
	team := forge("20260102-000000-bbbbbb", "mgr", true, "")
	isolated := forge("20260103-000000-cccccc", "mgr", true, "worktree") // the newest, but not resumable
	workersOnly := forge("20260104-000000-dddddd", "be-1", true, "")     // a worker saved a snapshot, the manager never did

	// "latest" is the newest session of the project: when that is a team in worktrees, which cannot be resumed, it says so and does not
	// resume an older session in its place (a trial's --continue did, and brought back a "say hi" instead of the stalled team).
	if d, err := session.ResolveResume(home, root, "latest"); err == nil || !strings.Contains(err.Error(), "20260103-000000-cccccc") || !strings.Contains(err.Error(), "recovery metadata") {
		t.Errorf("latest with a team in worktrees as the newest session: %s %v", d, err)
	}
	for name, c := range map[string]struct {
		dir  string
		want bool
	}{"single": {single, true}, "team": {team, true}, "isolated": {isolated, false}, "workers only": {workersOnly, false}} {
		if got := session.Resumable(c.dir); got != c.want {
			t.Errorf("Resumable(%s) = %v, want %v", name, got, c.want)
		}
	}
	if _, err := session.ResolveResume(home, root, "20260103-000000-cccccc"); err == nil || !strings.Contains(err.Error(), "recovery metadata") {
		t.Errorf("an isolated team's session: %v, want the reason it cannot be resumed", err)
	}
	if _, err := session.ResolveResume(home, root, "20260104-000000-dddddd"); err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Errorf("a team whose manager never finished a turn: %v", err)
	}
	if d, err := session.ResolveResume(home, root, "20260102-000000-bbbbbb"); err != nil || d != team {
		t.Errorf("a team's session by its id: %s %v", d, err)
	}
	// a resumed session keeps its shape unless the person says another: a single agent comes back as one, a team as a team
	if isTeam, ok := session.ResumedAsTeam(home, root, "20260101-000000-aaaaaa"); !ok || isTeam {
		t.Errorf("ResumedAsTeam(single) = %v, %v; want false, true", isTeam, ok)
	}
	if isTeam, ok := session.ResumedAsTeam(home, root, "20260102-000000-bbbbbb"); !ok || !isTeam {
		t.Errorf("ResumedAsTeam(team) = %v, %v; want true, true", isTeam, ok)
	}
	if _, ok := session.ResumedAsTeam(home, root, "nosuch"); ok {
		t.Error("ResumedAsTeam of a session that does not exist is known")
	}
	// without the worktree session, "latest" passes by the session in which no manager finished a turn, and finds the team
	if err := os.RemoveAll(isolated); err != nil {
		t.Fatal(err)
	}
	if got, err := session.ResolveResume(home, root, "latest"); err != nil || got != team {
		t.Errorf("latest = %s %v, want the newest session that can be resumed, the team's %s", got, err, team)
	}
}

// The default chat is a team, so it has to be resumable: the manager comes back with what it was told and what it did (its conversation, its
// notes), and the board with it, the tasks that were somebody's todo again because nothing of the workers is running. A session of a single
// agent can be resumed as a team and the other way round.
func TestResumeATeamBringsBackTheManagerAndTheBoard(t *testing.T) {
	repo := newRepo(t)
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", home)
	var prompts []string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		var all []string
		for _, m := range c.Messages {
			all = append(all, m.Content)
		}
		prompts = append(prompts, strings.Join(all, "\n"))
		if strings.Contains(c.LastUser(), "plan the work") && assistantTurns(c) == 0 {
			return mock.Reply{Text: "planning", ToolCalls: []mock.ToolCall{
				call("t1", "task", map[string]any{"action": "create", "title": "write the handler", "description": "the HTTP handler"}),
				call("t2", "task", map[string]any{"action": "create", "title": "write the docs", "description": "the README section"}),
			}}
		}
		return mock.Reply{Text: "noted"}
	})
	o := opts(t, repo, client, model)
	o.Dir, o.Home = "", home
	o.Swarm, o.Workers, o.Interactive = true, 3, true
	s1, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Run(context.Background(), "plan the work: the handler and the docs, codeword PINEAPPLE"); err != nil {
		t.Fatal(err)
	}
	id, dir := s1.ID, s1.Dir
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if !session.Resumable(dir) {
		t.Fatal("a team's session that finished a turn can be resumed")
	}

	o2 := opts(t, repo, client, model)
	o2.Dir, o2.Home = "", home
	o2.Swarm, o2.Workers, o2.Interactive, o2.Resume = true, 3, true, "latest"
	s2, err := session.New(context.Background(), o2)
	if err != nil {
		t.Fatalf("a team's session is resumed as a team: %v", err)
	}
	defer s2.Close()
	if s2.ID != id || s2.Dir != dir {
		t.Errorf("a resumed team continues under its own session: %s %s, want %s %s", s2.ID, s2.Dir, id, dir)
	}
	if m := s2.Main(); m == nil || len(m.Stack().Thread.Turns) < 2 {
		t.Fatalf("the manager comes back with its thread: %+v", m)
	}
	snap := s2.Swarm.Board.Snapshot()
	if len(snap.Tasks) != 2 || snap.Tasks[0].Title != "write the handler" || snap.Tasks[1].Title != "write the docs" {
		t.Fatalf("the board comes back: %+v", snap.Tasks)
	}
	if _, err := s2.Run(context.Background(), "what was the codeword?"); err != nil {
		t.Fatal(err)
	}
	last := prompts[len(prompts)-1]
	for _, want := range []string{"codeword PINEAPPLE", "write the handler", "what was the codeword?"} {
		if !strings.Contains(last, want) {
			t.Errorf("the resumed manager does not see %q", want)
		}
	}
	// tasks go on from where the old board stopped
	if next, err := s2.Swarm.Board.CreateTask("mgr", swarm.TaskSpec{Title: "one more"}); err != nil || next.ID != "T3" {
		t.Errorf("the next task is %q (%v), want T3", next.ID, err)
	}
}

// A session of a team can be resumed as a single agent (the manager's thread is handed to the one agent), and a single agent's as a team.
func TestResumeAcrossTheShapeOfASession(t *testing.T) {
	repo := newRepo(t)
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", home)
	var prompts []string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		var all []string
		for _, m := range c.Messages {
			all = append(all, m.Content)
		}
		prompts = append(prompts, strings.Join(all, "\n"))
		return mock.Reply{Text: "noted"}
	})
	run := func(label string, shape func(*session.Options), goal string) {
		t.Helper()
		o := opts(t, repo, client, model)
		o.Dir, o.Home = "", home
		shape(&o)
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		defer s.Close()
		if _, err := s.Run(context.Background(), goal); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		last := prompts[len(prompts)-1]
		if strings.Contains(goal, "CODEWORD") {
			return
		}
		for _, want := range []string{"codeword PINEAPPLE", "reminds"} {
			if !strings.Contains(last, want) {
				t.Errorf("%s: the resumed agent does not see %q", label, want)
			}
		}
	}
	team := func(o *session.Options) { o.Swarm, o.Workers, o.Interactive = true, 3, true }
	solo := func(o *session.Options) {}
	run("team", team, "the codeword PINEAPPLE, CODEWORD")
	run("resumed as a single agent", func(o *session.Options) { solo(o); o.Resume = "latest" }, "what reminds you of it?")
	run("resumed again as a team", func(o *session.Options) { team(o); o.Resume = "latest" }, "and what reminds you of it now?")
}

// The model is a property of the run, not of the conversation: a session can be
// continued on a different model (a cheaper one, or after the first ran out of
// budget), and the conversation comes with it.
func TestResumeOnAnotherModel(t *testing.T) {
	repo := newRepo(t)
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-abcdef")
	var models []string
	var last string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		models = append(models, c.Model)
		var all []string
		for _, m := range c.Messages {
			all = append(all, m.Content)
		}
		last = strings.Join(all, "\n")
		return mock.Reply{Text: "noted"}
	})
	o := opts(t, repo, client, model)
	o.Dir = dir
	s1, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Run(context.Background(), "the codeword is PINEAPPLE"); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	other := model
	other.ID = "mock-2"
	o2 := opts(t, repo, client, other)
	o2.Model = "mock-2"
	o2.Resume = dir
	s2, err := session.New(context.Background(), o2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.Run(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "mock-1" || models[1] != "mock-2" {
		t.Errorf("models used: %v", models)
	}
	if !strings.Contains(last, "PINEAPPLE") {
		t.Error("the conversation did not come along to the other model")
	}
}

// Two processes appending to one event log would interleave two histories under
// one sequence; a resume of a session that is still running elsewhere must fail.
func TestASessionDirectoryHasOneWriter(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-abcdef")
	o := opts(t, repo, client, model)
	o.Dir = dir
	s1, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	o2 := opts(t, repo, client, model)
	o2.Dir = dir
	if _, err := session.New(context.Background(), o2); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a second writer on one session directory: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := session.New(context.Background(), o2)
	if err != nil {
		t.Fatalf("the directory must be free once its session closed: %v", err)
	}
	s2.Close()
}

func TestSessionDirectoryLockReleasedAfterFailedStart(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Dir = filepath.Join(t.TempDir(), "session")
	o.Isolation = "invalid"
	if s, err := session.New(context.Background(), o); err == nil {
		s.Close()
		t.Fatal("invalid isolation mode unexpectedly started a session")
	} else if !strings.Contains(err.Error(), "isolation") {
		t.Fatalf("unexpected construction failure: %v", err)
	}
	o.Isolation = ""
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatalf("a failed start left the session locked: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// Compaction is acceptable because it is reversible: recall brings folded turns
// and truncated output back. A resumed session must keep that promise for what
// happened before the resume.
func TestRecallStillWorksForFoldedTurnsAndHandlesAfterAResume(t *testing.T) {
	repo := newRepo(t)
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-abcdef")
	handleRe := regexp.MustCompile(`out_[0-9a-f]{16,}`)
	var handle string

	// First session: read a file, produce output too long to show, finish, fold the early turns.
	step := 0
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.LastUser(), "<compactor-task>") {
			return mock.Reply{Text: `{"keep_from":"t4","spine":[{"turns":"t1-t3","line":"read main.go"}],"mask":[],"notes":[],"promote":[]}`}
		}
		for _, m := range c.Messages {
			if m.Role == "tool" {
				if h := handleRe.FindString(m.Content); h != "" {
					handle = h
				}
			}
		}
		step++
		switch step {
		case 1:
			return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{call("c1", "read", map[string]any{"path": "main.go"})}}
		case 2:
			return mock.Reply{Text: "producing output", ToolCalls: []mock.ToolCall{call("c2", "bash", map[string]any{"command": "yes 'a long line of output' | head -n 4000"})}}
		}
		return mock.Reply{Text: "done"}
	})
	o := opts(t, repo, client, model)
	o.Dir = dir
	s1, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Run(context.Background(), "read the code and run the noisy command"); err != nil {
		t.Fatal(err)
	}
	if rep, err := s1.Compact(context.Background(), ""); err != nil || rep.Mode == "none" {
		t.Fatalf("setup: compaction %+v %v", rep, err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if handle == "" {
		t.Fatal("setup: the long output did not get a recall handle")
	}

	// Second session: recall a folded turn range and the handle.
	var recalled []string
	calls := 0
	client2, model2 := startMock(t, func(c *mock.Call) mock.Reply {
		calls++
		switch calls {
		case 1:
			return mock.Reply{Text: "recalling", ToolCalls: []mock.ToolCall{call("r1", "recall", map[string]any{"turns": "t1-t3"})}}
		case 2:
			return mock.Reply{Text: "recalling output", ToolCalls: []mock.ToolCall{call("r2", "recall", map[string]any{"handle": handle, "limit": 200})}}
		}
		for _, m := range c.Messages {
			if m.Role == "tool" {
				recalled = append(recalled, m.Content)
			}
		}
		return mock.Reply{Text: "done"}
	})
	o2 := opts(t, repo, client2, model2)
	o2.Resume = dir
	s2, err := session.New(context.Background(), o2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.Run(context.Background(), "what did you read before?"); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(recalled, "\n---\n")
	if !strings.Contains(all, "package main") {
		t.Errorf("recall of the folded turns found nothing after the resume:\n%s", all)
	}
	if !strings.Contains(all, "a long line of output") {
		t.Errorf("recall of the handle %s found nothing after the resume:\n%s", handle, all)
	}
}

// --continue goes on under the rules the session had: the allow rules a person gave ("don't ask again", /allow) and a mode they chose are read back
// from the log, so that the same go test is not asked again. A mode on the new command line wins, and bypass is never brought back.
func TestResumeKeepsThePermissionsTheSessionHad(t *testing.T) {
	repo := newRepo(t)
	dir := filepath.Join(t.TempDir(), "sessions", "20260101-000000-abcdef")
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })

	o := opts(t, repo, client, model)
	o.Dir, o.Mode = dir, perm.ModeDefault
	s1, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	rule, _ := perm.ParseRule(perm.Allow, "Bash(go test:*)")
	s1.Perm.AddRule(perm.ScopeSession, rule)
	s1.Perm.SetMode(perm.ModeAcceptEdits)
	if _, err := s1.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	resume := func(mode perm.Mode) *session.Session {
		o2 := opts(t, repo, client, model)
		o2.Resume, o2.Mode = dir, mode
		s2, err := session.New(context.Background(), o2)
		if err != nil {
			t.Fatal(err)
		}
		return s2
	}
	s2 := resume(perm.ModeDefault)
	if got := s2.Perm.Granted(); len(got) != 1 || got[0] != "Bash(go test:*)" {
		t.Errorf("the rule given before --continue: %v", got)
	}
	if s2.Perm.Mode() != perm.ModeAcceptEdits {
		t.Errorf("the mode before --continue: %v", s2.Perm.Mode())
	}
	if m, n := s2.RestoredPermissions(); m != "accept-edits" || n != 1 {
		t.Errorf("RestoredPermissions: %q %d", m, n)
	}
	s2.Close() // one process at a time has a session
	// a mode asked for now wins, and bypass is not carried over
	s3 := resume(perm.ModePlan)
	defer s3.Close()
	if s3.Perm.Mode() != perm.ModePlan {
		t.Errorf("a mode on the new command line: %v", s3.Perm.Mode())
	}
}
