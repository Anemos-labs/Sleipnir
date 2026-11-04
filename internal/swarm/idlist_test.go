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
