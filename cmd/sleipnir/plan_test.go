package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestPlanCommandSubmitsPromptInPlanMode(t *testing.T) {
	for _, team := range []bool{false, true} {
		name := "solo"
		if team {
			name = "team"
		}
		t.Run(name, func(t *testing.T) {
			s := chatSessionWith(t, false, nil, func(o *session.Options) {
				o.Mode = perm.ModeYolo
				o.Swarm, o.MaxAgents = team, 8
			}, nil)
			h := &sessionHost{s: s}
			prompt := "Redo the website animations.\nUse 8 experts and one  shared design."
			var out strings.Builder
			res := h.Command(context.Background(), "/plan "+prompt, &out)
			if !strings.HasSuffix(res.Send, prompt) || strings.Count(res.Send, prompt) != 1 {
				t.Fatalf("planning prompt was lost or changed: %+v", res)
			}
			if !strings.Contains(res.Send, "read-only") || !strings.Contains(res.Send, "do not implement") {
				t.Fatalf("model was not told to plan: %q", res.Send)
			}
			if s.Perm.Mode() != perm.ModePlan || res.Quit || len(res.Restart) != 0 {
				t.Fatalf("mode=%s result=%+v", s.Perm.Mode(), res)
			}
			if !strings.Contains(out.String(), "plan mode: read-only") {
				t.Fatalf("no mode confirmation: %q", out.String())
			}
			for _, bare := range []string{"/plan", "/plan \t"} {
				if got := h.Command(context.Background(), bare, &out); got.Send != "" || s.Perm.Mode() != perm.ModePlan {
					t.Fatalf("bare command started work or left plan mode: %+v", got)
				}
			}
		})
	}
}

func TestPlanPromptRunsFromPipeAndTerminalWithoutWriting(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "pipe"
		if terminal {
			name = "terminal"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var prompts, results []string
			srv := mock.New(mock.Config{}, func(c *mock.Call) mock.Reply {
				mu.Lock()
				defer mu.Unlock()
				// Team requests append a live board after the tool result.
				for _, msg := range c.Messages {
					if msg.Role == "tool" {
						results = append(results, msg.Content)
						return mock.Reply{Text: "Planning remains read-only."}
					}
				}
				prompts = append(prompts, c.LastUser())
				return mock.Reply{ToolCalls: []mock.ToolCall{*writes("implementation.txt").call}}
			}).Start()
			t.Cleanup(srv.Close)
			w := newWorld(t, srv.URL+"/v1")
			w.extra = append(w.extra, "USERPROFILE="+w.home)
			if terminal {
				u := startUI(t, w, "--swarm", "8", "--mode", "yolo")
				u.send("/plan @plan")
				u.expect("plan mode: read-only", "read-only")
				u.wait("model receives the denied write", func(_, _ string) bool {
					mu.Lock()
					defer mu.Unlock()
					return len(results) == 1
				})
				u.ready()
				u.quitWithCtrlC()
			} else {
				r := w.run("/plan @plan\n", "chat", "--swarm", "8", "--mode", "yolo")
				if r.code != 0 || !strings.Contains(r.stderr, "plan mode: read-only") {
					t.Fatalf("exit=%d stdout=%s stderr=%s", r.code, r.stdout, r.stderr)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(prompts) != 1 || strings.Count(prompts[0], "@plan") != 1 || !strings.Contains(prompts[0], "do not implement") {
				t.Fatalf("prompt did not run exactly once in plan mode: %q", prompts)
			}
			if exists(filepath.Join(w.project, "implementation.txt")) {
				t.Fatal("planning request wrote an implementation file")
			}
			if len(results) != 1 || !strings.Contains(results[0], "read-only") {
				t.Fatalf("model did not receive the plan-mode refusal: %q", results)
			}
		})
	}
}
