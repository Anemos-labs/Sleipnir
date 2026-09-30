//go:build unix

package skills

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO named SKILL.md must not hang discovery.
func TestFIFOSkillDoesNotHangDiscovery(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(w.root, ".claude", "skills", "pipe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "SKILL.md"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	w.proj(".claude", "fine", skillText("fine", "d"))
	done := make(chan struct{})
	var c *Catalog
	var warns []Warning
	go func() {
		c, warns = w.discover(true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery blocked on a FIFO")
	}
	if names(c) != "fine" || len(warns) != 1 || !warns[0].Skipped || !strings.Contains(warns[0].Msg, "not a regular file") {
		t.Fatalf("skills %s, warnings %s", names(c), warnText(warns))
	}
}
