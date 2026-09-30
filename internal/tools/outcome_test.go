package tools

import (
	"encoding/json"
	"testing"
)

func TestFailedReadsWhatACommandReports(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     *Result
		failed  bool
		outcome string
	}{
		{"nil", nil, false, ""},
		{"a tool that is not a command", &Result{Text: "ok"}, false, ""},
		{"a tool error", &Result{IsError: true}, true, ""},
		{"a command that succeeded", &Result{Meta: map[string]any{"exit_code": 0, "timed_out": false}}, false, ""},
		{"a command that exited 1", &Result{Meta: map[string]any{"exit_code": 1}}, true, "exit 1"},
		{"an exit status read back from a log", &Result{Meta: map[string]any{"exit_code": float64(2)}}, true, "exit 2"},
		{"an exit status as json.Number", &Result{Meta: map[string]any{"exit_code": json.Number("127")}}, true, "exit 127"},
		{"int64", &Result{Meta: map[string]any{"exit_code": int64(3)}}, true, "exit 3"},
		{"a command that timed out", &Result{Meta: map[string]any{"exit_code": 0, "timed_out": true}}, true, "timed out"},
		{"a status that is not a number", &Result{Meta: map[string]any{"exit_code": "1"}}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Failed(); got != tc.failed {
				t.Errorf("Failed() = %v, want %v", got, tc.failed)
			}
			if got := tc.res.Outcome(); got != tc.outcome {
				t.Errorf("Outcome() = %q, want %q", got, tc.outcome)
			}
		})
	}
}
