package anthropic

import (
	"strings"
	"testing"
)

// A block start that repeats a key keeps it once, so the rebuilt block carries the accumulated text once, not once per copy.
func TestRepeatedBlockMembersAreKeptOnce(t *testing.T) {
	raw := `{"type":"text"` + strings.Repeat(`,"text":""`, 1000) + `}`
	members, err := orderedMembers([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("%d members", len(members))
	}
	a := &blockAcc{typ: "text", members: members, sawText: true}
	a.text.WriteString(strings.Repeat("x", 100_000))
	b, _, _ := a.build()
	if len(b) > 100_100 {
		t.Fatalf("the rebuilt block is %d bytes", len(b))
	}
	if m, _ := orderedMembers([]byte(`{"a":1,"a":2}`)); len(m) != 1 || string(m[0].val) != "1" {
		t.Fatalf("%+v", m)
	}
}
