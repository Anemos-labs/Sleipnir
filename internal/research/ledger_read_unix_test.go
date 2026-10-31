//go:build unix

package research

import (
	"os"
	"path/filepath"
	"testing"
)

// A run's ledger is read only from a regular run.json of a bounded size, in a run directory reached through no link.
func TestLoadRefusesALedgerItCannotTrust(t *testing.T) {
	_, _, r, root := newTestTool(t, "quick", "")
	dir := RunDir(root, r.Slug)
	ledger := filepath.Join(dir, "run.json")
	good, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, r.Slug); err != nil {
		t.Fatalf("the ledger as written: %v", err)
	}

	// the run directory a link to a copy elsewhere
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "run.json"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, r.Slug); err == nil {
		t.Error("a run directory that is a link was read")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".real", dir); err != nil {
		t.Fatal(err)
	}

	// run.json not a regular file
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledger, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, r.Slug); err == nil {
		t.Error("a run.json that is a directory was read")
	}
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}

	// run.json over the limit (sparse: no disk is used)
	f, err := os.Create(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxLedgerFile + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Load(root, r.Slug); err == nil {
		t.Error("a run.json over the limit was read")
	}
}
