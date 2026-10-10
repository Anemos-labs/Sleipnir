package session

import (
	"path/filepath"
	"testing"
)

// A session keeps its approvals store for as long as it runs. An approval written meanwhile by another program (`sleipnir mcp approve`
// in a terminal, or the web page) is seen at once, and a change the session makes afterwards keeps it instead of writing the
// session's old copy over it.
func TestMCPApprovalReread(t *testing.T) {
	home := approvalsHome(t)
	path := filepath.Join(home, ".sleipnir", "mcp-approvals.json")
	held := openApprovals(path) // what a running session holds
	if held.has("/p", "cli") {
		t.Fatal("nothing approved yet")
	}
	if err := OpenMCPApprovals(home).Approve("/p", "cli", "from-the-cli"); err != nil { // another program
		t.Fatal(err)
	}
	if !held.has("/p", "cli") {
		t.Error("an approval written after the store was opened is not seen")
	}
	if err := held.add("/p", "sess", "from-the-session"); err != nil {
		t.Fatal(err)
	}
	if err := held.add("/q", "sess2", "other-project"); err != nil {
		t.Fatal(err)
	}
	fresh := OpenMCPApprovals(home)
	for _, fp := range []string{"cli", "sess"} {
		if !fresh.Has("/p", fp) {
			t.Errorf("approval %q was lost by a later write", fp)
		}
	}
	if err := OpenMCPApprovals(home).Revoke("/p", "cli"); err != nil {
		t.Fatal(err)
	}
	if held.has("/p", "cli") {
		t.Error("a revocation by another program is not seen")
	}
	if err := held.remove("/q", "sess2"); err != nil || !fresh.Has("/p", "sess") || fresh.Has("/p", "cli") {
		t.Errorf("after the session's own removal: sess %v cli %v (%v)", fresh.Has("/p", "sess"), fresh.Has("/p", "cli"), err)
	}
}
