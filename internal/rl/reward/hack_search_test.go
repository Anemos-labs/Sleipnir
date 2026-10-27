package reward

import (
	"errors"
	"testing"
)

// The repository is searched once per literal, and for at most so many literals; past that the answer is unknown.
func TestRepositorySearchesAreCachedAndBounded(t *testing.T) {
	calls := 0
	s := cachedSearch(func(lit string) (bool, error) { calls++; return lit == "a", nil }, 2)
	for i := 0; i < 100; i++ {
		if had, err := s("a"); !had || err != nil {
			t.Fatal(had, err)
		}
	}
	if had, err := s("b"); had || err != nil {
		t.Fatal(had, err)
	}
	if _, err := s("c"); !errors.Is(err, errSearchBudget) {
		t.Fatalf("a third literal: %v", err)
	}
	if calls != 2 {
		t.Fatalf("%d searches, want 2", calls)
	}
}
