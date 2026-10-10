package wire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The fields PARITY A9 and A10 added to the Workspace bodies are optional: a body
// without them encodes as before, and a body with them round-trips.
func TestWorkspaceParityFieldsAreOptionalAndRoundTrip(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{AcceptRequest{Message: "m"}, `{"message":"m"}`},
		{AcceptResult{Commit: "c", Files: []string{"a"}, Branch: "main"}, `{"commit":"c","files":["a"],"branch":"main"}`},
		{VerifyOutput{Task: "T1", Cmd: "go test", ExitCode: 1, Output: "x"}, `{"task":"T1","cmd":"go test","exitCode":1,"output":"x"}`},
	} {
		b, err := json.Marshal(c.v)
		if err != nil || string(b) != c.want {
			t.Errorf("%T = %s (%v), want %s", c.v, b, err, c.want)
		}
	}
	req := AcceptRequest{Message: "m", Mode: "edits", DryRun: true}
	res := AcceptResult{Commit: "c", Files: []string{"a"}, Branch: "main", Tasks: []string{"T1"}, Applied: true, Committed: true,
		Waiting: true, CanCommit: true, CommitBlocked: "dirty", Message: "done", DryRun: true}
	out := VerifyOutput{Task: "T1", Cmd: "go test", Runs: []VerifyRun{{Attempt: 1, Cmd: "go test", Exit: 1, Ms: 30, At: 5, Agent: "be-1",
		Gate: "done", Out: "FAIL", Truncated: true, TimedOut: true}, {Attempt: 2, Cmd: "go test", OK: true}}}
	for _, v := range []any{&req, &res, &out} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		back := reflect.New(reflect.TypeOf(v).Elem()).Interface()
		if err := json.Unmarshal(b, back); err != nil || !reflect.DeepEqual(back, v) {
			t.Errorf("%T round trip: %s -> %+v (%v)", v, b, back, err)
		}
	}
	b, _ := json.Marshal(req)
	if string(b) != `{"message":"m","mode":"edits","dryRun":true}` {
		t.Errorf("request spelling: %s", b)
	}
}
