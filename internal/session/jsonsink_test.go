package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// `run --json` is what a script reads, and docs/CLI.md names its events and their members. These are the lines, as a script gets them: one
// object each, with the members the page names and with the types it gives.

func jsonLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a line of the stream is not a JSON object: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestJSONSinkEventsHaveTheMembersTheDocumentationNames(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)
	call := core.Block{Kind: core.BlockToolUse, ToolID: "c1", ToolName: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}

	s.Text("main", "hello")
	s.Thinking("main", "hmm")
	s.Reset("main")
	s.ToolStart("main", call)
	s.ToolEnd("main", call, &tools.Result{Text: "ok\n"}, 1500*time.Millisecond)
	s.ToolEnd("main", call, &tools.Result{Text: "--- FAIL", Meta: map[string]any{"exit_code": 2}}, 40*time.Millisecond)
	s.ToolEnd("main", call, &tools.Result{Text: "partial", Meta: map[string]any{"timed_out": true}}, 2*time.Minute)
	s.ToolEnd("main", call, &tools.Result{Text: "no such file", IsError: true}, time.Millisecond)
	s.ToolEnd("main", call, nil, 3*time.Millisecond)
	s.Response("main", &provider.Response{Usage: core.Usage{InputTokens: 10, CacheReadTokens: 90, OutputTokens: 5}, Stop: core.StopEnd}, 0.9)
	s.Notice("mgr", "warn", "cache miss")

	got := jsonLines(t, &buf)
	if len(got) != 11 {
		t.Fatalf("%d lines, want 11: %v", len(got), got)
	}
	has := func(i int, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if _, ok := got[i][k]; !ok {
				t.Errorf("line %d (%v) lacks %q", i, got[i]["type"], k)
			}
		}
		if len(got[i]) != len(keys) {
			t.Errorf("line %d (%v) has %d members %v, want exactly %v", i, got[i]["type"], len(got[i]), got[i], keys)
		}
	}
	has(0, "type", "agent", "text")
	has(1, "type", "agent", "text")
	has(2, "type", "agent")
	has(3, "type", "agent", "id", "name", "input")
	has(4, "type", "agent", "id", "name", "ms", "error", "failed", "output")
	has(5, "type", "agent", "id", "name", "ms", "error", "failed", "output", "exit_code")
	has(6, "type", "agent", "id", "name", "ms", "error", "failed", "output")
	has(7, "type", "agent", "id", "name", "ms", "error", "failed", "output")
	has(8, "type", "agent", "id", "name", "ms") // a call that has no result yet says no more than that it ended
	has(9, "type", "agent", "usage", "hit_ratio", "stop")
	has(10, "type", "agent", "level", "message")

	if in, ok := got[3]["input"].(map[string]any); !ok || in["command"] != "go test ./..." {
		t.Errorf("the input of a call is the object the model sent, not a string: %v", got[3]["input"])
	}
	for i, want := range map[int]struct {
		ms     float64
		errv   bool
		failed bool
	}{4: {1500, false, false}, 5: {40, false, true}, 6: {120000, false, true}, 7: {1, true, true}} {
		if got[i]["ms"] != want.ms || got[i]["error"] != want.errv || got[i]["failed"] != want.failed {
			t.Errorf("line %d: ms %v error %v failed %v, want %+v", i, got[i]["ms"], got[i]["error"], got[i]["failed"], want)
		}
	}
	if got[5]["exit_code"] != float64(2) {
		t.Errorf("exit_code = %v, want 2", got[5]["exit_code"])
	}
	if u, ok := got[9]["usage"].(map[string]any); !ok || u["input_tokens"] != float64(10) || u["cache_read_tokens"] != float64(90) || got[9]["stop"] != string(core.StopEnd) {
		t.Errorf("response: %v", got[9])
	}
}

// A tool that prints more than the stream carries is cut, and says so; what is cut is never in the middle of a character.
func TestJSONSinkClipsHugeOutputOnACharacterBoundary(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)
	call := core.Block{Kind: core.BlockToolUse, ToolID: "c1", ToolName: "bash"}
	big := strings.Repeat("é", maxJSONOutput) // two bytes each: the cut falls in the middle of one if it is made carelessly
	s.ToolEnd("main", call, &tools.Result{Text: big}, time.Second)
	out := jsonLines(t, &buf)[0]["output"].(string)
	if len(out) > maxJSONOutput+80 || !strings.Contains(out, "[output truncated:") {
		t.Fatalf("a %d byte output came out as %d bytes: …%q", len(big), len(out), out[max(0, len(out)-90):])
	}
	if strings.ContainsRune(out, '�') {
		t.Error("the cut is in the middle of a character")
	}
}

// Agents of a swarm write at once: every line is whole.
func TestJSONSinkLinesAreWholeWhenAgentsWriteAtOnce(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)
	var wg sync.WaitGroup
	for a := 0; a < 8; a++ {
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				s.Text(agent, fmt.Sprintf("line %d with \"quotes\" and \nnewlines", i))
			}
		}(fmt.Sprintf("a%d", a))
	}
	wg.Wait()
	if n := len(jsonLines(t, &buf)); n != 800 {
		t.Errorf("%d whole lines, want 800", n)
	}
}
