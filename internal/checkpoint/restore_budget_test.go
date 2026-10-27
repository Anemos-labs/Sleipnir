package checkpoint

import (
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// The rewind's budget pays for what is read and verified, not only for what is written: records naming one valid blob under
// checksums that do not match it end once the budget is spent, instead of a read and a hash each for as many as there are.
func TestRestoreSpendsItsBudgetOnFailedVerification(t *testing.T) {
	f := newForge(t)
	old := maxRestoreBytes
	maxRestoreBytes = 300
	t.Cleanup(func() { maxRestoreBytes = old })
	h, size := f.blob(strings.Repeat("x", 100))
	s := f.open()
	s.mu.Lock()
	cp := s.beginLocked("p")
	for i := 0; i < 10; i++ {
		wrong := core.HashBytes([]byte("other " + strconv.Itoa(i)))
		cp.add(&fileRec{Path: "f" + strconv.Itoa(i) + ".txt", Pre: state{Kind: kFile, Blob: h, Sum: wrong, Size: int64(size), Mode: 0o644}})
	}
	s.mu.Unlock()
	rep := mustRestore(t, s, cp.ID, RestoreOpts{})
	mismatched, spent := 0, 0
	for _, r := range rep.Files {
		switch {
		case strings.Contains(r.Detail, "checksum mismatch"):
			mismatched++
		case strings.Contains(r.Detail, "limit"):
			spent++
		default:
			t.Errorf("%+v", r)
		}
	}
	if mismatched != 3 || spent != 7 {
		t.Fatalf("a 300-byte budget pays for three 100-byte reads: %d verified and refused, %d refused for the budget: %+v", mismatched, spent, rep.Files)
	}
}
