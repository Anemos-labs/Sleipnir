package wire

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The optional fields of the Workspace bodies (the scope of a confirmation, the runs of a verification gate, the details of an accept)
// are omitted when empty, and a body that has them round-trips.
func TestWorkspaceOptionalFieldsAreOmittedAndRoundTrip(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{AcceptRequest{Message: "m"}, `{"message":"m"}`},
		{AcceptResult{Commit: "c", Files: []string{"a"}, Branch: "main"}, `{"commit":"c","files":["a"],"branch":"main"}`},
		{VerifyOutput{Task: "T1", Cmd: "go test", ExitCode: 1, Output: "x"}, `{"task":"T1","cmd":"go test","exitCode":1,"output":"x"}`},
		{RestoreRequest{ID: "c01"}, `{"id":"c01","dryRun":false}`},
		{RevertRequest{Path: "a", Key: "1:1", From: "base", To: "live"}, `{"path":"a","key":"1:1","from":"base","to":"live"}`},
		{Hunk{OldStart: 1, Lines: []HunkLine{}}, `{"oldStart":1,"oldLines":0,"newStart":0,"newLines":0,"lines":[]}`},
	} {
		b, err := json.Marshal(c.v)
		if err != nil || string(b) != c.want {
			t.Errorf("%T = %s (%v), want %s", c.v, b, err, c.want)
		}
	}
	req := AcceptRequest{Message: "m", Mode: "edits", DryRun: true, Scope: "accept:t:0123456789abcdef"}
	res := AcceptResult{Commit: "c", Files: []string{"a"}, Branch: "main", Tasks: []string{"T1"}, Applied: true, Committed: true,
		Waiting: true, CanCommit: true, CommitBlocked: "dirty", Message: "done", DryRun: true, Scope: "accept:t:0123456789abcdef"}
	out := VerifyOutput{Task: "T1", Cmd: "go test", Runs: []VerifyRun{{Attempt: 1, Cmd: "go test", Exit: 1, Ms: 30, At: 5, Agent: "be-1",
		Gate: "done", Out: "FAIL", Truncated: true, TimedOut: true}, {Attempt: 2, Cmd: "go test", OK: true}}}
	plan := RestorePlan{ID: "c01", Files: []RestoreFile{{Path: "a"}}, Scope: "restore:t:c01:0123456789abcdef"}
	rreq := RestoreRequest{ID: "c01", Scope: plan.Scope}
	vreq := RevertRequest{Path: "a", Key: "1:1", Scope: "revert:t:0123456789abcdef"}
	diff := WsDiff{Path: "a", Hunks: []Hunk{{OldStart: 1, Lines: []HunkLine{{T: " ", S: "x"}}, Scope: vreq.Scope}}}
	for _, v := range []any{&req, &res, &out, &plan, &rreq, &vreq, &diff} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		back := reflect.New(reflect.TypeOf(v).Elem()).Interface()
		if err := json.Unmarshal(b, back); err != nil || !reflect.DeepEqual(back, v) {
			t.Errorf("%T round trip: %s -> %+v (%v)", v, b, back, err)
		}
	}
	b, _ := json.Marshal(AcceptRequest{Message: "m", Mode: "edits", DryRun: true})
	if string(b) != `{"message":"m","mode":"edits","dryRun":true}` {
		t.Errorf("request spelling: %s", b)
	}
}
