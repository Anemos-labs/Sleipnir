package goal

import (
	"encoding/json"
	"strings"
	"testing"
)

// Deeply nested braces are tried a bounded number of times, the objects handed over are bounded, and the walk stops when
// the caller has what it wants.
func TestEachObjectIsBounded(t *testing.T) {
	nested := strings.Repeat(`{"a":`, 5000) + "1" + strings.Repeat("}", 5000)
	if n := len(Objects(nested)); n > maxObjects {
		t.Fatalf("%d objects", n)
	}
	calls := 0
	EachObject(`{"x":1} {"verdict":"done"} {"y":2}`, func(o json.RawMessage) bool { calls++; return calls < 2 })
	if calls != 2 {
		t.Fatalf("%d calls after the caller stopped", calls)
	}
	if v, ok := ParseVerdict("words {not json} " + `{"verdict":"done","reason":"r"}`); !ok || v.Kind != Done {
		t.Fatalf("%+v %v", v, ok)
	}
}
