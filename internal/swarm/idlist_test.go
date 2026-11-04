package swarm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIdListTakesAListOrOneStringOfIds(t *testing.T) {
	for in, want := range map[string]string{
		`"T3"`: "T3", `"T3, T4"`: "T3 T4", `["T3","T4"]`: "T3 T4", `["T3, T4"]`: "T3 T4", `[]`: "",
	} {
		var l idList
		if err := json.Unmarshal([]byte(in), &l); err != nil || strings.Join(l, " ") != want {
			t.Errorf("idList from %s = %q, %v; want %q", in, l, err, want)
		}
	}
}

// An explicit empty list stays a list: task revise reads a nil list as "leave the dependencies as they are".
func TestAnEmptyIdListIsNotNil(t *testing.T) {
	var l idList
	if err := json.Unmarshal([]byte(`[]`), &l); err != nil || l == nil {
		t.Fatalf("idList from [] = %#v, %v; want an empty, non-nil list", l, err)
	}
}
