package session

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// The approval question reaches the terminal without the escape sequences a model put in its summary.
func TestApprovalQuestionIsSanitizedForTheTerminal(t *testing.T) {
	var out bytes.Buffer
	ask := TerminalPrompter(strings.NewReader("n\n"), &out)
	ask(context.Background(), perm.Request{Agent: "a", Tool: "bash", Summary: "run ls\x1b]0;owned\x07\x1b[2J"})
	if strings.ContainsRune(out.String(), 0x1b) || strings.ContainsRune(out.String(), 0x07) {
		t.Fatalf("question = %q", out.String())
	}
	if !strings.Contains(out.String(), "run ls") {
		t.Fatalf("question = %q", out.String())
	}
}
