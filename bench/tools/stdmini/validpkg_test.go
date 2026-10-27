package main

import "testing"

// A package list cannot pass options to go list.
func TestValidPkgRefusesOptions(t *testing.T) {
	for p, want := range map[string]bool{"fmt": true, "net/http": true, "encoding/...": true, "-toolexec=/bin/sh": false, "": false, "/abs": false, "a b": false} {
		if got := validPkg(p); got != want {
			t.Errorf("validPkg(%q) = %v", p, got)
		}
	}
	if _, err := goList([]string{"-toolexec=x"}); err == nil {
		t.Error("an option reached go list")
	}
}
