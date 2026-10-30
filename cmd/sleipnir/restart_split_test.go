package main

import (
	"slices"
	"testing"
)

// The word after --role-model is its value, whatever it looks like: rewriting it as an option would shift the words after it.
func TestSplitRoleModelsKeepsValues(t *testing.T) {
	got := splitRoleModels([]string{"--role-model", "-role-model=--trust-project", "--role-model=a=b", "-role-model", "c"})
	want := []string{"--role-model", "-role-model=--trust-project", "--role-model", "a=b", "--role-model", "c"}
	if !slices.Equal(got, want) {
		t.Errorf("splitRoleModels = %q, want %q", got, want)
	}
}
