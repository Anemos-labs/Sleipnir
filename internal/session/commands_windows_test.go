package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/hooks"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tools/shell"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

func TestWindowsQuotedCommandsAcrossRunners(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "command fixture.cmd")
	const marker = "windows command fixture"
	if err := os.WriteFile(path, []byte("@echo off\r\necho "+marker+"\r\nexit /b 0\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := `"` + path + `"`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Run("task verification", func(t *testing.T) {
		out, code, err := runVerify(ctx, dir, command)
		if err != nil || code != 0 || !strings.Contains(out, marker) {
			t.Fatalf("quoted verifier: code=%d err=%v output=%q", code, err, out)
		}
	})
	t.Run("integration verification", func(t *testing.T) {
		res := workspace.RunShell(ctx, workspace.VerifyRequest{Dir: dir, Cmd: command})
		if !res.OK() || !strings.Contains(res.Output, marker) {
			t.Fatalf("quoted integration verifier: %+v", res)
		}
	})
	t.Run("hook", func(t *testing.T) {
		raw, err := json.Marshal([]map[string]any{{"hooks": []map[string]any{{"type": "command", "command": command}}}})
		if err != nil {
			t.Fatal(err)
		}
		set, err := hooks.ParseAs(hooks.OriginUser, "fixture", map[string]json.RawMessage{hooks.SessionStart: raw})
		if err != nil {
			t.Fatal(err)
		}
		r := &hooks.Runner{Set: set, Dir: dir}
		res, err := r.Run(ctx, hooks.Event{Name: hooks.SessionStart, Cwd: dir})
		if err != nil || len(res.Errors) != 0 || !strings.Contains(res.AdditionalContext, marker) {
			t.Fatalf("quoted hook: result=%+v err=%v", res, err)
		}
	})
	t.Run("shell tool", func(t *testing.T) {
		m := shell.NewManager(shell.Options{Shell: "cmd"})
		t.Cleanup(m.Shutdown)
		registry := tools.NewRegistry()
		shell.Register(registry, m)
		tool, ok := registry.Get("bash")
		if !ok {
			t.Fatal("shell tool was not registered")
		}
		raw, err := json.Marshal(map[string]any{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		res, err := tool.Run(ctx, &tools.Call{Name: "bash", Input: raw, Env: &tools.Env{Root: dir, Cwd: dir, Perm: perm.AllowAll{}}})
		if err != nil || res.IsError || res.Meta["exit_code"] != 0 || !strings.Contains(res.Text, marker) {
			t.Fatalf("quoted shell tool: result=%+v err=%v", res, err)
		}
	})
}
