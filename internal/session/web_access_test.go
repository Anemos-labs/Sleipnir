package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A session hands out the options it was built with, so that a host can build it again in the same process: the copy has no sink,
// prompter or per-agent sink (they belong to the generation that is ending), and changing the copy changes nothing of the session.
func TestOptionsIsACopyWithoutTheHooksOfTheHost(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	on := true
	o.Mailman = &on
	o.RoleModels = map[string]string{"backend": "mock/mock-1"}
	o.Allow = []string{"Bash(go test:*)"}
	o.Sink = agent.NopSink{}
	o.NewSink = func(string) agent.Sink { return agent.NopSink{} }
	o.Prompter = func(context.Context, perm.Request) perm.Decision { return perm.Decision{} }
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := s.Options()
	if got.Sink != nil || got.NewSink != nil || got.Prompter != nil {
		t.Errorf("the copy keeps a hook of the host: sink %v newsink %v prompter %v", got.Sink != nil, got.NewSink != nil, got.Prompter != nil)
	}
	if got.Cwd != repo || got.Root != repo || got.Model != "mock-1" || got.Provider == nil || got.ModelInfo == nil || !got.TrustProject {
		t.Errorf("the copy lost what the session was built with: %+v", got)
	}
	got.RoleModels["backend"] = "other/x"
	got.Allow[0] = "Bash(rm:*)"
	*got.Mailman = false
	again := s.Options()
	if again.RoleModels["backend"] != "mock/mock-1" || again.Allow[0] != "Bash(go test:*)" || !*again.Mailman {
		t.Errorf("changing the copy changed the session: %+v", again)
	}
	if again.ShellEnv != nil {
		t.Error("an inherited environment became an empty one in the copy")
	}
	if s.Root() != repo {
		t.Errorf("Root = %q, want %q", s.Root(), repo)
	}
	if s.Turn() != 0 {
		t.Errorf("Turn before any run = %d", s.Turn())
	}
	if _, err := s.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	if s.Turn() != 2 {
		t.Errorf("Turn after two runs = %d", s.Turn())
	}
}

// The judge's verdict is in the log (goal.judge), with its kind, its reason and what it found missing, and GoalJudged hands the host
// the same verdict; a turn that was cancelled asks no judge.
func TestTheJudgesVerdictIsLoggedAndHandedToTheHost(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether a coding agent") {
			return mock.Reply{Text: `{"verdict":"continue","reason":"no test ran","left":["run go test","write the README"]}`}
		}
		return mock.Reply{Text: "done, I think"}
	})
	o := opts(t, repo, client, model)
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("make the tests pass")
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	note, next, j := s.GoalJudged(context.Background(), g, nil)
	if !j.Judged || j.Verdict.Kind != goal.Continue || j.Verdict.Reason != "no test ran" || len(j.Verdict.Left) != 2 || j.Err != nil {
		t.Fatalf("judgment %+v", j)
	}
	if !strings.Contains(note, "goal not met yet: no test ran") || !strings.Contains(next, "missing: run go test") {
		t.Errorf("note %q next %q", note, next)
	}
	if _, _, j := s.GoalJudged(context.Background(), g, context.Canceled); j.Judged || g.Paused != "you interrupted it" {
		t.Errorf("a cancelled turn: judged %v paused %q", j.Judged, g.Paused)
	}
	if err := s.Log.Flush(); err != nil {
		t.Fatal(err)
	}
	var found []map[string]any
	if err := events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Type == "goal.judge" {
			var d map[string]any
			if err := json.Unmarshal(e.Data, &d); err != nil {
				return err
			}
			found = append(found, d)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("%d goal.judge events, want 1 (the cancelled turn asked no judge)", len(found))
	}
	d := found[0]
	left, _ := d["left"].([]any)
	if d["verdict"] != "continue" || d["reason"] != "no test ran" || len(left) != 2 || left[0] != "run go test" {
		t.Errorf("goal.judge = %v", d)
	}
}

// A judge that cannot be asked pauses the goal and says why; the judgment carries the error and no verdict.
func TestAJudgeThatFailsIsReportedAsNotJudged(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether a coding agent") {
			return mock.Reply{Fault: &mock.Fault{Status: 400, Message: "bad request"}}
		}
		return mock.Reply{Text: "ok"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := goal.New("x")
	if _, err := s.Run(context.Background(), goal.Start(g)); err != nil {
		t.Fatal(err)
	}
	note, next, j := s.GoalJudged(context.Background(), g, nil)
	if j.Judged || j.Err == nil || next != "" || !strings.Contains(note, "the judge could not be asked") || !strings.HasPrefix(g.Paused, "the judge could not be asked") {
		t.Errorf("note %q next %q judgment %+v paused %q", note, next, j, g.Paused)
	}
	if errors.Is(j.Err, context.Canceled) {
		t.Errorf("the error is a cancellation: %v", j.Err)
	}
}

// When the checkpoint store reports its changes, a turn that writes a file leaves a "checkpoint" event in the log with the checkpoint's
// id, label and files.
func TestCheckpointsAreLoggedWhenTheStoreReportsThem(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if c.Messages[len(c.Messages)-1].Role == "tool" {
			return mock.Reply{Text: "written"}
		}
		return mock.Reply{ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "notes.txt", "content": "hello\n"})}}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := any(s.Ckpt).(interface{ OnChange(func(checkpoint.Info)) }); !ok {
		t.Skip("this checkpoint store does not report its changes yet")
	}
	if _, err := s.Run(context.Background(), "write the notes"); err != nil {
		t.Fatal(err)
	}
	_ = s.Log.Flush()
	var files []string
	_ = events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Type == "checkpoint" {
			var d struct {
				ID, Label string
				File      string   // the file the event reports as new
				Files     []string // logs that listed every file
			}
			if json.Unmarshal(e.Data, &d) == nil && d.ID != "" && d.Label != "" {
				files = append(append(files, d.Files...), d.File)
			}
		}
		return nil
	})
	if !strings.Contains(strings.Join(files, " "), "notes.txt") {
		t.Errorf("no checkpoint event names notes.txt: %v", files)
	}
}
