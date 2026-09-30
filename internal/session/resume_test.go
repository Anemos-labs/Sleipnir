package session_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
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
	// Swarms are not resumable yet, and say so.
	o = opts(t, repo, client, model)
	o.Swarm, o.Resume = true, empty
	if _, err := session.New(context.Background(), o); err == nil || !strings.Contains(err.Error(), "swarm") {
		t.Errorf("resuming a swarm: %v", err)
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

// A swarm session cannot be resumed yet. "latest" must not pick one (its
// snapshots are the manager's), and naming one says why it cannot be continued.
func TestResumeSkipsAndExplainsSwarmSessions(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	sessions := filepath.Join(home, ".sleipnir", "sessions")
	forge := func(id string, swarm bool) string {
		dir := filepath.Join(sessions, id)
		l, err := events.Open(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		l.Emit("", events.TypeSessionStart, map[string]any{"swarm": swarm, "root": root})
		l.Emit("mgr", events.TypeAgentSnapshot, map[string]any{"blob": "x"})
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	single := forge("20260101-000000-aaaaaa", false)
	swarm := forge("20260102-000000-bbbbbb", true) // newer, but not resumable

	got, err := session.ResolveResume(home, root, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if got != single {
		t.Errorf("latest = %s, want the newest resumable session %s", got, single)
	}
	if !session.Resumable(single) || session.Resumable(swarm) {
		t.Errorf("Resumable: single=%v swarm=%v", session.Resumable(single), session.Resumable(swarm))
	}
	for _, spec := range []string{"20260102-000000-bbbbbb", swarm} {
		if _, err := session.ResolveResume(home, root, spec); err == nil || !strings.Contains(err.Error(), "swarm session") {
			t.Errorf("resume %q: %v, want an explanation that swarm sessions cannot be resumed", spec, err)
		}
	}
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
