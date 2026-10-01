package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// capture runs f with os.Stdout redirected and returns what it wrote.
func capture(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestRunCommandEndToEnd(t *testing.T) {
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.LastUser(), "list files") {
			for _, m := range c.Messages {
				if m.Role == "tool" {
					return mock.Reply{Text: "the repo has main.go"}
				}
			}
			return mock.Reply{Text: "listing", ToolCalls: []mock.ToolCall{{ID: "c1", Name: "ls", Args: `{"path":"."}`}}}
		}
		return mock.Reply{Text: "the repo has main.go"}
	})
	ts := srv.Start()
	defer ts.Close()

	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"providers":{"mock":{"base_url":%q}},"models":{"default":"mock/mock-1"}}`, ts.URL)
	if err := os.WriteFile(filepath.Join(repo, ".sleipnir", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEIPNIR_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	var err error
	out := capture(t, func() {
		err = cmdRun(context.Background(), []string{"--cwd", repo, "--mode", "bypass", "--no-web", "--trust-project", "--json", "list files"})
	})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var sawTool, sawResult bool
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			t.Fatalf("stdout must be JSON lines only, got %q", sc.Text())
		}
		switch ev["type"] {
		case "tool_end":
			sawTool = sawTool || ev["name"] == "ls"
		case "result":
			sawResult = true
			if !strings.Contains(fmt.Sprint(ev["text"]), "main.go") || ev["steps"].(float64) != 2 {
				t.Fatalf("result event: %v", ev)
			}
		}
	}
	if !sawTool || !sawResult {
		t.Fatalf("expected a tool_end for ls and a final result:\n%s", out)
	}

	// A missing prompt is a usage error, not a hang.
	if err := cmdRun(context.Background(), []string{"--cwd", repo, "--no-web", "--trust-project", "--quiet", ""}); err == nil {
		// stdin is not a terminal under `go test`, so it may read (empty) stdin; either way an error is required.
		t.Fatal("empty prompt must fail")
	}
}

// The README says `sleipnir swarm 8 "goal" --verify "make test"`: flags after the
// prompt must be flags, not more goal.
func TestRunTakesFlagsAfterThePrompt(t *testing.T) {
	var seen string
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		seen = c.LastUser()
		return mock.Reply{Text: "ok"}
	})
	ts := srv.Start()
	defer ts.Close()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"providers":{"mock":{"base_url":%q}},"models":{"default":"mock/mock-1"}}`, ts.URL)
	if err := os.WriteFile(filepath.Join(repo, ".sleipnir", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEIPNIR_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var err error
	out := capture(t, func() {
		err = cmdRun(context.Background(), []string{"say", "hello", "--cwd", repo, "--mode", "bypass", "--no-web", "--trust-project", "--quiet"})
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// The pins and the prompt share the first user message; the prompt is its tail.
	if !strings.HasSuffix(strings.TrimSpace(seen), "say hello") || strings.Contains(seen, "--cwd") {
		t.Fatalf("the model was sent %q: flags after the prompt were swallowed into it", seen)
	}
}

func TestSimAndRoleModelFlags(t *testing.T) {
	kf := kvFlags{}
	if err := kf.Set("manager=heimdall/x"); err != nil || kf["manager"] != "heimdall/x" {
		t.Fatalf("role-model parse: %v %v", err, kf)
	}
	for _, bad := range []string{"", "manager", "=x", "manager="} {
		if err := kf.Set(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}
