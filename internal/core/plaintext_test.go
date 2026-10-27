package core

import (
	"strings"
	"testing"
	"time"
)

// Deeply nested tool results flatten in one pass, in order.
func TestPlainTextOfNestedResults(t *testing.T) {
	b := ToolResult("t", false, Text("a"), ToolResult("u", false, Text("b"), ToolUse("x", "y", nil)), Text("c"))
	if got := b.PlainText(); got != "abc" {
		t.Fatalf("%q", got)
	}
	deep := Text(strings.Repeat("x", 1<<16))
	for i := 0; i < 5000; i++ {
		deep = ToolResult("t", false, deep)
	}
	start := time.Now()
	if got := deep.PlainText(); len(got) != 1<<16 {
		t.Fatalf("%d bytes", len(got))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
}
