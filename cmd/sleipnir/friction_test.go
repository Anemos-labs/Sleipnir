package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/friction"
)

func frictionLog(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "session")
	l, err := events.Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	emit := func(typ string, data map[string]any) {
		if _, err := l.Emit("a", typ, data); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"1", "2", "3"} {
		emit(events.TypeModelResponse, map[string]any{"req": "r" + id})
		emit(events.TypeToolCall, map[string]any{"id": id, "name": "bash", "input": map[string]any{"command": "cd /workspace && go test ./..."}})
		emit(events.TypePermDecide, map[string]any{"tool": "bash", "command": "cd /workspace && go test ./...", "reason": "approval required: reads /workspace outside the workspace (/w/tree)", "allow": false, "by": "no one"})
		emit(events.TypeToolResult, map[string]any{"id": id, "name": "bash", "error": true, "meta": map[string]any{"error_kind": "permission"}})
	}
	emit(events.TypeAgentCancel, map[string]any{"phase": "tools", "cause": "canceled", "steps": 3})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFrictionCommandRanksAndExplains(t *testing.T) {
	dir := frictionLog(t)
	var out, errb bytes.Buffer
	if err := cmdFriction(context.Background(), []string{dir}, &out, &errb); err != nil {
		t.Fatalf("%v\n%s", err, errb.String())
	}
	text := out.String()
	for _, want := range []string{"friction in 1 sessions", "SCORE", "permission.refused", "bash: cd <path>", "[no one]", "outside the workspace", "agent.cancel", "canceled during tools", "seq ", "cd /workspace"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "permission.refused") > strings.Index(text, "agent.cancel") {
		t.Errorf("the pattern seen three times must rank above the one seen once:\n%s", text)
	}

	out.Reset()
	if err := cmdFriction(context.Background(), []string{"--json", "--top", "1", dir}, &out, &errb); err != nil {
		t.Fatal(err)
	}
	var rep friction.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || len(rep.Findings) != 1 || rep.Findings[0].Category != friction.PermRefused || rep.Findings[0].Count != 3 {
		t.Fatalf("json: %v %+v", err, rep)
	}

	out.Reset()
	if err := cmdFriction(context.Background(), []string{dir, "--category", "agent"}, &out, &errb); err != nil || strings.Contains(out.String(), "permission.refused") || !strings.Contains(out.String(), "agent.cancel") {
		t.Errorf("--category agent: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := cmdFriction(context.Background(), []string{dir, "--min-count", "5"}, &out, &errb); err != nil || !strings.Contains(out.String(), "nothing that cost anything") {
		t.Errorf("--min-count 5: %v\n%s", err, out.String())
	}
}

func TestFrictionCommandNeedsALog(t *testing.T) {
	var out, errb bytes.Buffer
	if err := cmdFriction(context.Background(), nil, &out, &errb); err == nil {
		t.Error("no path must be an error")
	}
	if err := cmdFriction(context.Background(), []string{t.TempDir()}, &out, &errb); err == nil || !strings.Contains(err.Error(), "no events.jsonl") {
		t.Errorf("an empty directory: %v", err)
	}
}
