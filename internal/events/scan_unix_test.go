//go:build unix

package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A log that leads to something other than a regular file (here a link to a device that never ends) is refused, not read.
func TestScanReadsOnlyARegularFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.Symlink("/dev/zero", p); err != nil {
		t.Fatal(err)
	}
	if err := Scan(p, func(Event) error { return nil }); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v", err)
	}
}
