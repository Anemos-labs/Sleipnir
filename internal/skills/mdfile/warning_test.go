package mdfile

import (
	"strings"
	"testing"
)

// A file name with a line break cannot add a line to a warning.
func TestWarningPathsStayOnOneLine(t *testing.T) {
	for _, w := range []Warning{
		Warnf(".sleipnir/commands/x\nwarn: forged", "", "nested too deep"),
		Skipf(".sleipnir/commands/y z.md", "y\rz", "bad name"),
		{Path: "a\nb", Msg: "m"},
	} {
		if s := w.String(); strings.ContainsAny(s, "\n\r ") {
			t.Errorf("%q spans lines", s)
		}
	}
	if got := Warnf("plain/path", "n", "m").String(); got != "plain/path: [n] m" {
		t.Errorf("plain warning = %q", got)
	}
}
