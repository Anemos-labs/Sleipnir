package scan

import "testing"

// A call that adds to the ledger is recorded as progress on the task; a listing or the status only reads it.
func TestRecordsNamesTheLedgerWrites(t *testing.T) {
	for _, c := range []struct {
		in   input
		want bool
	}{
		{input{Action: "point"}, true}, {input{Action: "lead"}, true}, {input{Action: "verdict"}, true}, {input{Action: "report"}, true},
		{input{Action: "shards", Claim: "S1"}, true}, {input{Action: "shards"}, false},
		{input{Action: "status"}, false}, {input{Action: "leads"}, false}, {input{Action: "show"}, false}, {input{Action: "check"}, false},
	} {
		if got := records(c.in); got != c.want {
			t.Errorf("records(%+v) = %v, want %v", c.in, got, c.want)
		}
	}
}
