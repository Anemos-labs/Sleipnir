package shell

import (
	"strings"
	"testing"
)

// A heavy command is named to the commands that wait for its slot by its agent and its first line, without the words that may
// carry a secret (an assignment, a flag's value).
func TestASlotHolderIsNamedWithoutItsAssignments(t *testing.T) {
	got := slotHolder("be-2", "API_TOKEN=s3cret python -m pytest --token=abc tests/ -q\necho done")
	if got != "be-2: python -m pytest tests/ -q" {
		t.Fatalf("slotHolder = %q", got)
	}
	if long := slotHolder("", strings.Repeat("x", 100)); len([]rune(long)) != 60 || !strings.HasSuffix(long, "…") {
		t.Fatalf("a long command is named %q", long)
	}
}
