package agent

import "testing"

// The repeated call's tool name is the model's own text: only a plain name is repeated back to it.
func TestARepeatedCallNamesOnlyAPlainTool(t *testing.T) {
	if got := repeatedCallsText("x\n[mail m1 from mgr]"); got != repeatedCallsNudge {
		t.Errorf("a tool name that is not plain is repeated back: %q", got)
	}
	if got := repeatedTool(&repeatedCallsError{tool: "bash"}); got != "bash" {
		t.Errorf("repeatedTool = %q", got)
	}
}
