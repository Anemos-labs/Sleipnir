package web

import (
	"strings"
	"testing"
)

func TestDeepJSONIsNotIndentedPastItsBudget(t *testing.T) {
	deep := strings.Repeat("[", 20000) + strings.Repeat("]", 20000)
	if out := prettyJSON(deep); len(out) > 4*len(deep) {
		t.Fatalf("a %d-byte document became %d bytes", len(deep), len(out))
	}
	if out := prettyJSON(`{"a":[1,2]}`); !strings.Contains(out, "\n") {
		t.Errorf("a small document is still indented: %q", out)
	}
	if got := longestRun("a``b````c`", '`'); got != 4 {
		t.Errorf("longestRun = %d", got)
	}
}
