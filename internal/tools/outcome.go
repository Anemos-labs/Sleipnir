package tools

import (
	"encoding/json"
	"fmt"
)

// A command that exits with a status other than 0 is not a tool error: its output is the answer, and often the one the model
// wanted (a failing test says what to fix), so a result keeps IsError false and the model sees the same text as ever. But a
// person watching must not be shown a tick next to it, and the harness's stuck guard must count it: a run that repeats one
// failing command is as stuck as one that repeats a refused call. These read what a command tool reports in Meta.

// ExitStatus is the exit status of the command a result reports ("exit_code" in Meta), and whether it reports one.
func (r *Result) ExitStatus() (code int, ok bool) {
	if r == nil {
		return 0, false
	}
	switch v := r.Meta["exit_code"].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64: // a result read back from a log
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	}
	return 0, false
}

// TimedOut reports whether the command was stopped because it ran out of time.
func (r *Result) TimedOut() bool {
	if r == nil {
		return false
	}
	b, _ := r.Meta["timed_out"].(bool)
	return b
}

// Failed reports whether the call did not do what it was asked: the tool said so (IsError), or the command it ran exited with a
// status other than 0 or was stopped on its time limit.
func (r *Result) Failed() bool {
	if r == nil {
		return false
	}
	if r.IsError || r.TimedOut() {
		return true
	}
	code, ok := r.ExitStatus()
	return ok && code != 0
}

// Outcome says in a few words how a failed command ended ("exit 1", "timed out"); empty when the call did not fail or is not a
// command.
func (r *Result) Outcome() string {
	switch code, ok := r.ExitStatus(); {
	case r == nil:
		return ""
	case r.TimedOut():
		return "timed out"
	case ok && code != 0:
		return fmt.Sprintf("exit %d", code)
	}
	return ""
}
