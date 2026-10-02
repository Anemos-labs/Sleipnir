package session_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

var (
	_ agent.Resetter = (*session.TextSink)(nil)
	_ agent.Resetter = (*session.JSONSink)(nil)
)

func bashCall(cmd string) core.Block {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return core.ToolUse("c1", "bash", in)
}

// A command that exited with a status other than 0 is not a tool error, and used to be shown with the tick of a call that worked:
// a run whose tests failed scrolled past as a column of ✓.
func TestTextSinkMarksAFailedCommandAndSaysHowItEnded(t *testing.T) {
	var out, log strings.Builder
	sink := session.NewTextSink(&out, &log, "main", false)
	for _, tc := range []struct {
		name string
		res  *tools.Result
		want string
	}{
		{"success", &tools.Result{Text: "ok", Meta: map[string]any{"exit_code": 0}}, "✓ bash go test (1s)"},
		{"exit 1", &tools.Result{Text: "FAIL", Meta: map[string]any{"exit_code": 1}}, "✗ bash go test (1s, exit 1)"},
		{"a timeout", &tools.Result{Text: "stopped", Meta: map[string]any{"exit_code": -1, "timed_out": true}}, "✗ bash go test (1s, timed out)"},
		{"a tool error", &tools.Result{Text: "no such file", IsError: true}, "✗ bash go test (1s)\n    no such file"}, // a refusal says why, without --verbose
		{"no result", nil, "✓ bash go test (1s)"},
	} {
		log.Reset()
		call := bashCall("go test")
		sink.ToolStart("main", call)
		sink.ToolEnd("main", call, tc.res, time.Second)
		if got := strings.TrimSpace(log.String()); got != tc.want {
			t.Errorf("%s: the line is %q, want %q", tc.name, got, tc.want)
		}
	}
}

// With --verbose a failed command shows how its output ended: the last lines are the ones that say what failed.
func TestTextSinkVerboseShowsTheTailOfAFailedCommand(t *testing.T) {
	var out, log strings.Builder
	sink := session.NewTextSink(&out, &log, "main", true)
	call := bashCall("go test")
	sink.ToolEnd("main", call, &tools.Result{Text: "=== RUN TestA\n=== RUN TestB\n--- FAIL: TestA (0.00s)\n    a_test.go:9: got 1, want 2\nFAIL\nexit status 1\nFAIL\tx\t0.004s\n", Meta: map[string]any{"exit_code": 1}}, time.Second)
	got := log.String()
	for _, want := range []string{"✗ bash go test (1s, exit 1)", "a_test.go:9: got 1, want 2", "FAIL\tx"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "=== RUN") {
		t.Errorf("only the tail is shown:\n%s", got)
	}
}

// A retried response starts over: what was shown of the failed attempt stays (it was printed), and the new attempt begins on a line
// of its own instead of continuing the half line.
func TestTextSinkStartsARetriedAnswerOnANewLine(t *testing.T) {
	var out, log strings.Builder
	sink := session.NewTextSink(&out, &log, "main", false)
	sink.Text("main", "The bug is in the par")
	sink.Reset("main")
	sink.Text("main", "\n\nThe bug is in the parser.\n")
	if got, want := out.String(), "The bug is in the par\nThe bug is in the parser.\n"; got != want {
		t.Errorf("the answer stream is %q, want %q", got, want)
	}
	sink.Reset("main") // nothing is in progress: nothing to end
	sink.Text("main", "x\n")
	if strings.Contains(out.String(), "\n\n") {
		t.Errorf("a reset with nothing in progress wrote a blank line: %q", out.String())
	}
}

// Scripts read the JSON stream: the output of a tool is all of it (up to a bound), not its first line, and a retry is announced so
// that a consumer can drop the partial text of the attempt that failed.
func TestJSONSinkCarriesTheWholeOutputAndTheExitStatus(t *testing.T) {
	var b strings.Builder
	sink := session.NewJSONSink(&b)
	call := bashCall("make")
	text := "line one\nline two\nline three"
	sink.ToolEnd("main", call, &tools.Result{Text: text, Meta: map[string]any{"exit_code": 2}}, 1500*time.Millisecond)
	sink.Reset("main")
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2:\n%s", len(lines), b.String())
	}
	end := lines[0]
	if end["output"] != text || end["failed"] != true || end["error"] != false || end["exit_code"] != float64(2) || end["ms"] != float64(1500) {
		t.Errorf("tool_end = %v", end)
	}
	if lines[1]["type"] != "reset" || lines[1]["agent"] != "main" {
		t.Errorf("reset = %v", lines[1])
	}
	// a very long output is cut at a bound, and says so
	b.Reset()
	sink.ToolEnd("main", call, &tools.Result{Text: strings.Repeat("x", 300_000)}, time.Millisecond)
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(b.String())), &m); err != nil {
		t.Fatal(err)
	}
	if o, _ := m["output"].(string); len(o) > 70_000 || !strings.Contains(o, "truncated") {
		t.Errorf("a huge output was carried whole (%d bytes) or cut without a word", len(o))
	}
}

// A notice of the session itself (no agent) is not printed under an empty name: "[] warn: ...".
func TestTextSinkNoticeOfTheSessionHasNoEmptyBrackets(t *testing.T) {
	var out, log strings.Builder
	s := session.NewTextSink(&out, &log, "main", false)
	s.Notice("", "warn", "from the session")
	s.Notice("w1", "warn", "from an agent")
	if got, want := log.String(), "warn: from the session\n[w1] warn: from an agent\n"; got != want {
		t.Errorf("log %q, want %q", got, want)
	}
}

// A run on an endpoint whose cache keeps missing says so three times and then once that it will not say more.
func TestTextSinkSaysCacheMissesThreeTimes(t *testing.T) {
	var out, log strings.Builder
	s := session.NewTextSink(&out, &log, "main", false)
	for i := 0; i < 8; i++ {
		s.Notice("main", "warn", agent.CacheMissNoticePrefix+" ~100 tokens read from cache, got 0")
	}
	s.Notice("main", "warn", "something else")
	got := log.String()
	if n := strings.Count(got, agent.CacheMissNoticePrefix); n != 3 {
		t.Errorf("%d miss lines, want 3:\n%s", n, got)
	}
	if n := strings.Count(got, "further misses are not said here"); n != 1 || !strings.Contains(got, "something else") {
		t.Errorf("log:\n%s", got)
	}
}
