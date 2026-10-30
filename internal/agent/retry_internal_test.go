package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipErrorCutsAtARune(t *testing.T) {
	long := strings.Repeat("a", 299) + "é" + strings.Repeat("b", 50)
	got := clipError(long)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > 300+len("…") {
		t.Fatalf("clipError = %q", got)
	}
	if clipError("short") != "short" {
		t.Fatal("a short error was changed")
	}
}
