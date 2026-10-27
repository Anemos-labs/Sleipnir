package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/friction"
)

// The text report prints what came from session logs without escape sequences and without extra lines.
func TestFrictionReportIsTerminalSafe(t *testing.T) {
	rep := friction.Report{Sessions: 1, Findings: []friction.Finding{{
		Category: "refusal", Key: "k\x1b]52;c;ZXZpbA==\x07", Hint: "h\nforged: line",
		Examples: []friction.Example{{Session: "s\x1b[2J", Seq: 3, Detail: "d\r\nwarn: forged"}},
	}}}
	var b bytes.Buffer
	printFriction(&b, rep)
	out := b.String()
	if strings.ContainsAny(out, "\x1b\x07\r") || strings.Contains(out, "\nforged") || strings.Contains(out, "\nwarn: forged") {
		t.Errorf("report: %q", out)
	}
}
