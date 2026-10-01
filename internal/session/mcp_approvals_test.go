package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// A project's tool server is a program that runs as the person, and which of a repository's entries may start is the person's to say, per
// project and per exact entry. The record of what was said lives in their state directory and nowhere a repository can write, and a record
// that cannot be read means nothing is approved, never everything.

func approvalsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "") // the state directory is below the home that the test gives
	return home
}

func TestMCPApprovalsAreKeptPerProjectAndPerEntry(t *testing.T) {
	home := approvalsHome(t)
	a := OpenMCPApprovals(home)
	if a.Has("/p/one", "fp1") {
		t.Fatal("nothing has been approved yet")
	}
	if err := a.Approve("/p/one", "fp1", "github"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		root, fp string
		want     bool
	}{
		{"/p/one", "fp1", true},
		{"/p/one", "fp2", false}, // another entry (an edited one has another fingerprint: it asks again)
		{"/p/two", "fp1", false}, // another project
	} {
		if got := a.Has(tc.root, tc.fp); got != tc.want {
			t.Errorf("Has(%s, %s) = %v, want %v", tc.root, tc.fp, got, tc.want)
		}
	}
	// what was approved is still approved by whoever opens the record next: a later run, the `mcp` command
	if b := OpenMCPApprovals(home); !b.Has("/p/one", "fp1") || b.Has("/p/two", "fp1") {
		t.Errorf("a reopened record: one %v, two %v", b.Has("/p/one", "fp1"), b.Has("/p/two", "fp1"))
	}
	if err := a.Revoke("/p/one", "fp1"); err != nil {
		t.Fatal(err)
	}
	if a.Has("/p/one", "fp1") || OpenMCPApprovals(home).Has("/p/one", "fp1") {
		t.Error("a revoked entry is still approved")
	}
	if err := a.Revoke("/p/one", "never approved"); err != nil { // forgetting what was not there is not an error
		t.Errorf("revoking an unknown entry: %v", err)
	}
}

func TestMCPApprovalsFileIsPrivateAtomicAndInTheStateDirectory(t *testing.T) {
	home := approvalsHome(t)
	a := OpenMCPApprovals(home)
	if err := a.Approve("/p", "fp", "docs"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".sleipnir", "mcp-approvals.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the record is not where the state directory is: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("the record is readable by others: %v", fi.Mode().Perm())
	}
	var doc struct {
		Version  int
		Approved map[string]map[string]string
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil || doc.Version != 1 || doc.Approved["/p"]["fp"] != "docs" {
		t.Errorf("the record says %s (%v)", b, err)
	}
	// written to a temp file and renamed: none is left behind
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mcp-approvals-") {
			t.Errorf("a temp file of the write is left behind: %s", e.Name())
		}
	}
	// revoking the last entry of a project takes the project out of the record
	if err := a.Revoke("/p", "fp"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), `"/p"`) {
		t.Errorf("a project with nothing approved is still in the record: %s", b)
	}
}

// What cannot be read approves nothing: a record that is damaged, from another version, or not a record at all.
func TestMCPApprovalsThatCannotBeReadApproveNothing(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":         "this is not json",
		"empty":            "",
		"another version":  `{"version":2,"approved":{"/p":{"fp":"docs"}}}`,
		"no version":       `{"approved":{"/p":{"fp":"docs"}}}`,
		"approved is null": `{"version":1,"approved":null}`,
		"a list":           `[1,2,3]`,
	} {
		t.Run(name, func(t *testing.T) {
			home := approvalsHome(t)
			path := filepath.Join(home, ".sleipnir", "mcp-approvals.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			a := OpenMCPApprovals(home)
			if a.Has("/p", "fp") {
				t.Fatal("an unreadable record approved an entry")
			}
			// and the person can approve again: the record is made anew
			if err := a.Approve("/p", "fp2", "docs"); err != nil || !OpenMCPApprovals(home).Has("/p", "fp2") {
				t.Errorf("approving into a record that was unreadable: %v", err)
			}
		})
	}
}

func TestMCPApprovalsUnderConcurrentUse(t *testing.T) {
	home := approvalsHome(t)
	a := OpenMCPApprovals(home)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			root, fp := fmt.Sprintf("/p/%d", i%4), fmt.Sprintf("fp%d", i)
			if err := a.Approve(root, fp, "s"); err != nil {
				t.Error(err)
			}
			a.Has(root, fp)
			if i%3 == 0 {
				if err := a.Revoke(root, fp); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	b := OpenMCPApprovals(home)
	for i := 0; i < 24; i++ {
		root, fp := fmt.Sprintf("/p/%d", i%4), fmt.Sprintf("fp%d", i)
		if want := i%3 != 0; b.Has(root, fp) != want {
			t.Errorf("entry %d: approved %v, want %v", i, b.Has(root, fp), want)
		}
	}
}

// What the `mcp` command lists: the user's own servers are trusted, a project's are not even read until the project is trusted, and then each
// is approved or not by the record.
func TestMCPEntriesSaysWhoIsTrustedAndWhatIsApproved(t *testing.T) {
	home := approvalsHome(t)
	root := t.TempDir()
	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(home, ".sleipnir", "config.json"), `{"mcp":{"mine":{"command":"my-server"}}}`)
	mustWrite(filepath.Join(root, ".sleipnir", "config.json"), `{"mcp":{"theirs":{"command":"their-server","args":["--x"]}}}`)

	list := func(trust bool) map[string]MCPEntry {
		t.Helper()
		es, _, err := MCPEntries(home, root, trust)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]MCPEntry{}
		for _, e := range es {
			m[e.Name] = e
		}
		return m
	}
	untrusted := list(false)
	if e, ok := untrusted["mine"]; !ok || !e.Trusted {
		t.Errorf("the user's own server: %+v (present %v)", e, ok)
	}
	if _, ok := untrusted["theirs"]; ok {
		t.Error("a project's server was read though the project is not trusted")
	}
	trusted := list(true)
	theirs, ok := trusted["theirs"]
	if !ok || theirs.Trusted || theirs.Approved || theirs.Fingerprint == "" {
		t.Fatalf("a trusted project's server: %+v (present %v), want one that is neither trusted nor approved yet", theirs, ok)
	}
	if err := OpenMCPApprovals(home).Approve(root, theirs.Fingerprint, "theirs"); err != nil {
		t.Fatal(err)
	}
	if e := list(true)["theirs"]; !e.Approved {
		t.Errorf("after the approval: %+v", e)
	}
	// editing the entry changes its fingerprint, and the approval does not carry over
	mustWrite(filepath.Join(root, ".sleipnir", "config.json"), `{"mcp":{"theirs":{"command":"their-server","args":["--x","--steal"]}}}`)
	if e := list(true)["theirs"]; e.Approved || e.Fingerprint == theirs.Fingerprint {
		t.Errorf("an edited entry kept its approval: %+v (was %s)", e, theirs.Fingerprint)
	}
}
