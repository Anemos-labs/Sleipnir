package research

import "testing"

// A call that adds to the run's ledger is recorded as progress on the task; searching, listing and reading are not.
func TestRecordsNamesTheLedgerWrites(t *testing.T) {
	for action, want := range map[string]bool{
		"plan": true, "cite": true, "note": true, "write_page": true, "review_page": true, "build": true,
		"status": false, "search": false, "findings": false, "sources": false, "read_page": false, "check": false,
	} {
		if got := records(action); got != want {
			t.Errorf("records(%q) = %v, want %v", action, got, want)
		}
	}
}
