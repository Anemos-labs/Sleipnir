package main

import (
	"reflect"
	"testing"
)

// --at resolves against the trunk's savepoints and refuses what the trunk does not have.
func TestBranchPoints(t *testing.T) {
	avail := []int{1, 2, 3, 4, 6}
	for at, want := range map[string][]int{"last": {6}, "every:2": {2, 4, 6}, "sp:3,1,3": {3, 1}} {
		got, err := branchPoints(at, avail)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v %v, want %v", at, got, err, want)
		}
	}
	for _, at := range []string{"sp:5", "every:0", "every:7", "plateau", "sp:x"} {
		if got, err := branchPoints(at, avail); err == nil {
			t.Errorf("%s accepted: %v", at, got)
		}
	}
	if _, err := branchPoints("last", nil); err == nil {
		t.Error("a trunk without savepoints was accepted")
	}
}
