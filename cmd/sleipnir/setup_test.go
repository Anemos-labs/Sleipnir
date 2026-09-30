package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// projectDir makes an empty git project and an empty HOME, and runs the test in
// the project.
func projectDir(t *testing.T) (proj, home string) {
	t.Helper()
	proj, home = t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "-C", proj, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, k := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Chdir(proj)
	return proj, home
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return m
}

func sub(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func TestInitUserWritesTheProvidersTheModelAndTheMode(t *testing.T) {
	_, home := projectDir(t)
	t.Setenv("HEIMDALL_API_KEY", "test-key-value")
	if err := cmdInit(context.Background(), []string{"--user"}); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, filepath.Join(home, ".sleipnir", "config.json"))
	if got := sub(cfg, "models", "default"); got != "heimdall/deepseek/deepseek-v4.1-flash" {
		t.Errorf("the default model detected from the key was not written: %v", got)
	}
	if got := sub(cfg, "providers", "local", "base_url"); got != "http://127.0.0.1:8000/v1" {
		t.Errorf("local provider: %v", got)
	}
	if got := sub(cfg, "providers", "local", "options", "capture_tokens"); got != true {
		t.Errorf("token capture is what makes a local policy RL-ready: %v", got)
	}
	if got := sub(cfg, "permissions", "mode"); got != "default" {
		t.Errorf("permission mode: %v", got)
	}
}

func TestInitUserWithExplicitModelWinsOverDetection(t *testing.T) {
	_, home := projectDir(t)
	t.Setenv("OPENAI_API_KEY", "test-key-value")
	if err := cmdInit(context.Background(), []string{"--user", "--model", "local/my-policy"}); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, filepath.Join(home, ".sleipnir", "config.json"))
	if got := sub(cfg, "models", "default"); got != "local/my-policy" {
		t.Errorf("models.default = %v", got)
	}
}

func TestInitProjectWritesOnlyShareableSettings(t *testing.T) {
	proj, home := projectDir(t)
	t.Setenv("HEIMDALL_API_KEY", "test-key-value")
	if err := cmdInit(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, filepath.Join(proj, ".sleipnir", "config.json"))
	for _, path := range [][]string{{"providers"}, {"permissions", "mode"}, {"permissions", "allow"}, {"hooks"}, {"mcp"}, {"models"}} {
		if v := sub(cfg, path...); v != nil {
			t.Errorf("the project config must not carry %s (ignored unless trusted, and the detected model depends on whose keys are set): %v", strings.Join(path, "."), v)
		}
	}
	if sub(cfg, "permissions", "deny") == nil || sub(cfg, "swarm", "max_agents") == nil {
		t.Errorf("shareable settings missing: %v", cfg)
	}
	if _, err := os.Stat(filepath.Join(proj, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".gitignore")); !strings.Contains(string(b), ".sleipnir/config.local.json") {
		t.Errorf(".gitignore: %q", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".sleipnir", "config.json")); err == nil {
		t.Error("a project init must not touch the user's config")
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	projectDir(t)
	if err := cmdInit(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := cmdInit(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second init must refuse: %v", err)
	}
}
