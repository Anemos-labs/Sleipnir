package session_test

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestRuntimeShellContextReachesSoloAndWorkers(t *testing.T) {
	candidates := []string{"bash", "sh"}
	if runtime.GOOS == "windows" {
		candidates = []string{"pwsh", "powershell", "cmd"}
	}
	selected := ""
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate); err == nil {
			selected = candidate
			break
		}
	}
	if selected == "" {
		t.Skip("no native shell available")
	}
	command := "printf runtime-shell-probe"
	switch selected {
	case "pwsh", "powershell":
		command = "Write-Output runtime-shell-probe"
	case "cmd":
		command = "echo runtime-shell-probe"
	}
	for _, team := range []bool{false, true} {
		name := "solo"
		if team {
			name = "team"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var shared []string
			probes := 0
			client, model := startMock(t, func(c *mock.Call) mock.Reply {
				mu.Lock()
				defer mu.Unlock()
				manager := false
				for _, msg := range c.Messages {
					if msg.Role == "user" {
						manager = manager || strings.Contains(msg.Content, "you: mgr (manager)")
						if _, tail, ok := strings.Cut(msg.Content, "<shared-context>"); ok {
							text, _, _ := strings.Cut(tail, "</shared-context>")
							shared = append(shared, text)
						}
					}
					if msg.Role == "tool" && strings.Contains(msg.Content, "runtime-shell-probe") && strings.Contains(msg.Content, "[exit code 0]") {
						probes++
					}
				}
				n := assistantTurns(c)
				if manager {
					switch n {
					case 0:
						return mock.Reply{ToolCalls: []mock.ToolCall{
							call("m1", "task", map[string]any{"action": "create", "title": "Check the runtime shell", "role": "backend", "files": []string{"main.go"}}),
							call("m2", "spawn", map[string]any{"role": "backend", "task": "T1"}),
							call("m3", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20}),
						}}
					case 1:
						return mock.Reply{ToolCalls: []mock.ToolCall{call("m4", "task", map[string]any{"action": "accept", "id": "T1"})}}
					}
				} else {
					if n == 0 {
						return mock.Reply{ToolCalls: []mock.ToolCall{call("probe", "bash", map[string]any{"command": command})}}
					}
					if team && n == 1 {
						return mock.Reply{ToolCalls: []mock.ToolCall{call("done", "task", map[string]any{"action": "done", "id": "T1", "text": "Native shell probe passed."})}}
					}
				}
				return mock.Reply{Text: "Runtime checked."}
			})
			o := opts(t, newRepo(t), client, model)
			o.NoRecon, o.Swarm = true, team
			o.NoMCP, o.Offline = true, true
			o.Isolation = "none"
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s, err := session.New(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Run(ctx, "Check the runtime shell."); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			o.Resume, o.Dir = s.Dir, ""
			s, err = session.New(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Run(ctx, "Acknowledge the same runtime after resume."); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(shared) < 3 || probes == 0 {
				t.Fatalf("runtime missing from requests or native command did not run: shared=%d probes=%d", len(shared), probes)
			}
			for _, text := range shared {
				if text != shared[0] || !strings.Contains(text, "Operating system: "+runtime.GOOS) || !strings.Contains(text, "Command shell:") || !strings.Contains(text, selected) {
					t.Fatalf("shared runtime missing, incorrect, or changed across turns, agents, or resume: %q", text)
				}
			}
		})
	}
}

func TestRuntimeShellUnavailableStillAllowsFileTools(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	var sawContext, readFile bool
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		for _, msg := range c.Messages {
			if msg.Role == "user" && strings.Contains(msg.Content, "Command shell: unavailable.") {
				sawContext = true
			}
			if msg.Role == "tool" && strings.Contains(msg.Content, "package main") {
				readFile = true
			}
		}
		if assistantTurns(c) == 0 {
			return mock.Reply{ToolCalls: []mock.ToolCall{call("read", "read", map[string]any{"path": "main.go"})}}
		}
		return mock.Reply{Text: "Read the file without a command shell."}
	})
	t.Setenv("PATH", t.TempDir())
	o := opts(t, repo, client, model)
	o.NoRecon, o.NoMCP, o.Offline = true, true, true
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "Read main.go."); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawContext || !readFile {
		t.Fatalf("unavailable runtime context=%v, file read=%v", sawContext, readFile)
	}
}
