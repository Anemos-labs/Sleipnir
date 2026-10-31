package session

import (
	"bytes"
	"strings"
	"testing"
)

// A proof that writes far more than the host keeps has its start and its end kept, and the middle counted.
func TestProofOutputKeepsHeadAndTail(t *testing.T) {
	var w headTail
	w.Write([]byte("READY\n"))
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for range 3 * proofKeep / len(chunk) {
		w.Write(chunk)
	}
	w.Write([]byte("\nREPRODUCED\n"))
	out := w.bytes()
	if len(out) > 2*proofKeep+100 {
		t.Fatalf("kept %d bytes, want at most about %d", len(out), 2*proofKeep)
	}
	s := string(out)
	if !strings.HasPrefix(s, "READY\n") || !strings.HasSuffix(s, "REPRODUCED\n") || !strings.Contains(s, "bytes of output not kept") {
		t.Errorf("output lost its ends or the note of what was dropped: %q...%q", s[:20], s[len(s)-20:])
	}
}
