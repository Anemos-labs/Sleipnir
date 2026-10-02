package app

import (
	"encoding/json"
	"testing"
)

func TestThePlanToolLineSaysHowFarTheWorkIs(t *testing.T) {
	in := json.RawMessage(`{"items":[{"step":"read","status":"done"},{"step":"fix the parser","status":"doing"},{"step":"test"}]}`)
	if got := toolSummary("plan", in, ""); got != "1 of 3 done · now: fix the parser" {
		t.Errorf("%q", got)
	}
	if got := toolSummary("plan", json.RawMessage(`{"items":[]}`), ""); got != "" {
		t.Errorf("an empty plan: %q", got)
	}
}
